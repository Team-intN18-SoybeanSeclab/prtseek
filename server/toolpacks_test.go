package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type toolpackTransport func(*http.Request) (*http.Response, error)

func (f toolpackTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func toolpackZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, e := w.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write([]byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func toolpackHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func toolpackTestManager(t *testing.T, badTemplate bool) *toolpackManager {
	return toolpackTestManagerVersion(t, badTemplate, "v1.2.3")
}
func toolpackTestManagerVersion(t *testing.T, badTemplate bool, version string) *toolpackManager {
	t.Helper()
	m := newToolpackManager(t.TempDir())
	shortVersion := strings.TrimPrefix(version, "v")
	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "macOS"
	}
	bin := "nuclei"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	engine := toolpackZip(t, map[string]string{bin: "fixture executable " + version})
	templates := toolpackZip(t, map[string]string{"nuclei-templates-" + shortVersion + "/http/test.yaml": "id: test\ninfo:\n  name: Test"})
	name := "nuclei_" + shortVersion + "_" + osName + "_" + runtime.GOARCH + ".zip"
	eURL := "https://github.com/projectdiscovery/nuclei/releases/download/" + version + "/" + name
	eSumName := "nuclei_" + shortVersion + "_checksums.txt"
	eSum := "https://github.com/projectdiscovery/nuclei/releases/download/" + version + "/" + eSumName
	eAPI := "https://api.github.com/repos/projectdiscovery/nuclei/releases/assets/123"
	eSumAPI := "https://api.github.com/repos/projectdiscovery/nuclei/releases/assets/124"
	tDownloadURL := "https://codeload.github.com/projectdiscovery/nuclei-templates/zip/refs/tags/" + version
	tSumName := "nuclei-templates-" + shortVersion + "_checksums.txt"
	tSum := "https://github.com/projectdiscovery/nuclei-templates/releases/download/" + version + "/" + tSumName
	release := func(tag string, assets []map[string]any) []byte {
		b, _ := json.Marshal(map[string]any{"tag_name": tag, "assets": assets})
		return b
	}
	tHash := toolpackHash(templates)
	if badTemplate {
		tHash = strings.Repeat("0", 64)
	}
	body := map[string][]byte{
		"https://api.github.com/repos/projectdiscovery/nuclei/releases/latest":           release(version, []map[string]any{{"name": name, "browser_download_url": eURL, "url": eAPI, "digest": "sha256:" + toolpackHash(engine), "size": len(engine)}, {"name": eSumName, "browser_download_url": eSum, "url": eSumAPI}}),
		"https://api.github.com/repos/projectdiscovery/nuclei-templates/releases/latest": release(version, []map[string]any{{"name": tSumName, "browser_download_url": tSum}}),
		eAPI: engine, tDownloadURL: templates, eSumAPI: []byte(toolpackHash(engine) + "  " + name + "\n"), tSum: []byte(tHash + "  nuclei-templates-" + shortVersion + ".zip\n"),
	}
	m.client = &http.Client{Transport: toolpackTransport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/releases/assets/") && r.Header.Get("Accept") != "application/octet-stream" {
			return nil, fmt.Errorf("asset API request is missing the binary content header")
		}
		b, ok := body[r.URL.String()]
		if !ok {
			return nil, fmt.Errorf("unexpected URL %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b)), Header: make(http.Header)}, nil
	})}
	m.versionProbe = func(_ context.Context, path string) (string, error) {
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
		return "Nuclei Engine Version: " + version, nil
	}
	return m
}
func waitToolpack(t *testing.T, m *toolpackManager) toolpackState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := m.snapshot()
		if s.Status != "installing" {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("installation did not finish")
	return toolpackState{}
}

func TestToolpackInstallAndReload(t *testing.T) {
	m := toolpackTestManager(t, false)
	if err := m.install(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	s := waitToolpack(t, m)
	if s.Status != "ready" || !s.Enabled || s.Engine.Version != "v1.2.3" || s.Templates.Version != "v1.2.3" {
		t.Fatalf("unexpected state %+v", s)
	}
	if _, err := os.Stat(filepath.Join(s.Templates.Path, "http", "test.yaml")); err != nil {
		t.Fatal(err)
	}
	reloaded := newToolpackManager(filepath.Dir(m.root))
	r := reloaded.snapshot()
	if r.Engine.Path != s.Engine.Path || r.Templates.SHA256 != s.Templates.SHA256 || !r.Enabled {
		t.Fatal("installed state was not restored")
	}
	m.mu.Lock()
	m.state.Running = 1
	m.mu.Unlock()
	if err := m.uninstall(); err == nil {
		t.Fatal("uninstall must not remove running binaries")
	}
	m.mu.Lock()
	m.state.Running = 0
	m.state.Enabled = false
	m.mu.Unlock()
	if _, err := m.scan(context.Background(), []byte(`{"targets":["http://127.0.0.1"],"templates":["http/test.yaml"]}`)); err == nil {
		t.Fatal("disabled package should reject execution")
	}
	if err := m.uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Engine.Path); !os.IsNotExist(err) {
		t.Fatal("owned engine not removed")
	}
}

func TestToolpackChecksumFailurePreservesInstalledComponent(t *testing.T) {
	m := toolpackTestManager(t, true)
	if err := m.install(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	s := waitToolpack(t, m)
	if s.Status != "failed" || s.Engine.Path == "" || s.Templates.Path != "" || s.Enabled {
		t.Fatalf("checksum failure falsely registered usable package: %+v", s)
	}
	if !strings.Contains(s.Error, "SHA-256") {
		t.Fatalf("missing integrity failure: %s", s.Error)
	}
	if _, err := os.Stat(m.executable(s.Engine)); err != nil {
		t.Fatal("previously completed engine was lost")
	}
}

func TestToolpackUpdatePrunesOnlyIdleVersions(t *testing.T) {
	m := toolpackTestManager(t, false)
	if err := m.install(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	before := waitToolpack(t, m)
	updatedFixture := toolpackTestManagerVersion(t, false, "v1.2.4")
	m.mu.Lock()
	m.state.Running++
	m.state.Enabled = false
	m.catalog = nil
	m.client = updatedFixture.client
	m.versionProbe = updatedFixture.versionProbe
	m.mu.Unlock()
	if err := m.install(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	after := waitToolpack(t, m)
	if after.Status != "ready" || after.Engine.Path == before.Engine.Path || after.Engine.Version != "v1.2.4" || after.Templates.Version != "v1.2.4" {
		t.Fatal("update did not install a new version directory")
	}
	if after.Enabled {
		t.Fatal("updating a disabled tool must not enable it")
	}
	if _, err := os.Stat(before.Engine.Path); err != nil {
		t.Fatal("update removed a binary referenced by a running call")
	}
	m.mu.Lock()
	m.state.Running--
	m.cleanupObsoleteLocked()
	m.mu.Unlock()
	for _, path := range []string{before.Engine.Path, before.Templates.Path} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("obsolete version was not removed after becoming idle")
		}
	}
	for _, path := range []string{after.Engine.Path, after.Templates.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("cleanup removed the active version")
		}
	}
}

func TestToolpackZipRejectsEscape(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "C:/outside", "..\\outside"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "fixture.zip")
			if err := os.WriteFile(archive, toolpackZip(t, map[string]string{name: "bad"}), 0600); err != nil {
				t.Fatal(err)
			}
			if err := extractToolpackZip(context.Background(), archive, filepath.Join(root, "files")); err == nil {
				t.Fatalf("accepted unsafe name %q", name)
			}
		})
	}
}

func TestToolpackChecksumSelectsExactAsset(t *testing.T) {
	hash := strings.Repeat("a", 64)
	if got, err := checksumFor([]byte(hash+"  wanted.zip\n"), "wanted.zip"); err != nil || got != hash {
		t.Fatal(got, err)
	}
	if _, err := checksumFor([]byte(hash+"  other.zip\n"), "wanted.zip"); err == nil {
		t.Fatal("accepted another asset checksum")
	}
	for _, addr := range []string{"http://github.com/projectdiscovery/nuclei/releases/download/v1/a.zip", "https://github.com.evil.test/projectdiscovery/nuclei/releases/download/v1/a.zip", "https://github.com/other/nuclei/releases/download/v1/a.zip"} {
		if officialToolpackURL(addr, "nuclei") {
			t.Fatalf("accepted unofficial source %s", addr)
		}
	}
	canonical := "https://github.com/projectdiscovery/nuclei/releases/download/v1.2.3/a.zip"
	for _, addr := range []string{"https://api.github.com.evil.test/repos/projectdiscovery/nuclei/releases/assets/1", "https://api.github.com/repos/other/nuclei/releases/assets/1", "https://api.github.com/repos/projectdiscovery/nuclei/releases/assets/1/other"} {
		if got := toolpackAssetDownloadURL(canonical, addr, "nuclei"); got != canonical {
			t.Fatalf("accepted unofficial API download %s", got)
		}
	}
}

func TestToolpackRejectsOutOfRootState(t *testing.T) {
	data := t.TempDir()
	m := newToolpackManager(data)
	m.state.Enabled = true
	m.state.Engine.Path = t.TempDir()
	m.state.Templates.Path = t.TempDir()
	if err := m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	r := newToolpackManager(data).snapshot()
	if r.Enabled || r.Status != "failed" {
		t.Fatal("foreign paths must not become an enabled tool")
	}
}

func TestToolpackCancellationAndInterruptedRecovery(t *testing.T) {
	m := newToolpackManager(t.TempDir())
	started := make(chan struct{})
	m.client = &http.Client{Transport: toolpackTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	if err := m.install(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	<-started
	if restored := newToolpackManager(filepath.Dir(m.root)).snapshot(); restored.Status != "failed" || restored.Enabled {
		t.Fatal("interrupted installation must not be marked installed")
	}
	m.mu.Lock()
	m.cancel()
	m.mu.Unlock()
	s := waitToolpack(t, m)
	if s.Status != "cancelled" || s.Enabled {
		t.Fatalf("unexpected cancellation %+v", s)
	}
}
