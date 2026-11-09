package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// Explicitly opt in: this downloads and executes the official portable package.
// All target traffic is confined to the local httptest server below.
func TestToolpackLiveInstall(t *testing.T) {
	if os.Getenv("ARTEX_TOOLPACK_LIVE_TEST") != "1" {
		t.Skip("set ARTEX_TOOLPACK_LIVE_TEST=1 to validate official downloads and a local fixture")
	}
	base, err := filepath.Abs(filepath.Join("..", ".dev"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	data := os.Getenv("ARTEX_TOOLPACK_LIVE_DATA")
	reuseInstalled := data != ""
	if reuseInstalled {
		data, err = filepath.Abs(data)
		if err != nil || !withinToolpackRoot(base, data) {
			t.Fatal("live fixture may only reuse an installation inside this checkout's .dev directory")
		}
	} else {
		data, err = os.MkdirTemp(base, "toolpack-validation-")
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("validation data: %s", data)
	m := newToolpackManager(data)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if !reuseInstalled {
		if err := m.install(ctx, "all"); err != nil {
			t.Fatal(err)
		}
	}
	lastStage := ""
	for m.snapshot().Status == "installing" {
		s := m.snapshot()
		if s.Stage != lastStage {
			t.Log(s.Stage)
			lastStage = s.Stage
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	s := m.snapshot()
	if s.Status != "ready" || !s.Enabled {
		t.Fatalf("official installation failed: %+v", s)
	}
	t.Logf("installed engine=%s templates=%s", s.Engine.Version, s.Templates.Version)
	version, err := m.version(ctx, m.executable(s.Engine))
	if err != nil {
		t.Fatalf("real executable version check: %v", err)
	}
	t.Log(strings.TrimSpace(version))
	var localRequests atomic.Int64
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localRequests.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("artex-toolpack-local-smoke"))
	}))
	defer fixture.Close()
	templateFile, err := os.CreateTemp(filepath.Join(s.Templates.Path, "http"), "artex-local-smoke-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	templatePath := templateFile.Name()
	defer func() {
		_ = templateFile.Close()
		if err := os.Remove(templatePath); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove local template fixture: %v", err)
		}
	}()
	template := "id: artex-local-smoke\ninfo:\n  name: ARTEX local toolpack fixture\n  author: artex\n  severity: info\nhttp:\n  - method: GET\n    path:\n      - '{{BaseURL}}/'\n    matchers:\n      - type: word\n        words:\n          - artex-toolpack-local-smoke\n"
	if _, err := templateFile.WriteString(template); err != nil {
		t.Fatal(err)
	}
	if err := templateFile.Close(); err != nil {
		t.Fatal(err)
	}
	templateRelative, err := filepath.Rel(s.Templates.Path, templatePath)
	if err != nil {
		t.Fatal(err)
	}
	templateRelative = filepath.ToSlash(templateRelative)
	raw, _ := json.Marshal(map[string]any{"targets": []string{fixture.URL}, "templates": []string{templateRelative}})
	out, err := m.scan(ctx, raw)
	if err != nil || !strings.Contains(out, `"template-id":"artex-local-smoke"`) {
		t.Fatalf("local detection failed: %v\n%s", err, out)
	}
	t.Logf("local match confirmed at %s", fixture.URL)

	// Exercise the same CoreTool adapters exposed to the Agent, while keeping
	// enable/disable checks private to this manager. Do not rewrite the running
	// application's state.json or change its user configuration.
	toolServer := &Server{}
	toolpackManagers.Store(toolServer, m)
	defer toolpackManagers.Delete(toolServer)
	collectTools := func() map[string]actool.CoreTool {
		result := make(map[string]actool.CoreTool)
		for _, tool := range toolServer.toolpackTools() {
			result[tool.Name()] = tool
		}
		return result
	}
	heldTools := collectTools()
	if len(heldTools) != 2 || heldTools["nuclei_templates"] == nil || heldTools["nuclei_scan"] == nil {
		t.Fatalf("installed tools were not exposed: %v", heldTools)
	}
	query, _ := json.Marshal(map[string]string{"query": filepath.Base(templatePath)})
	assertTemplateSearch := func(tool actool.CoreTool) {
		t.Helper()
		result, err := tool.Call(ctx, query, nil)
		if err != nil || result.IsError {
			t.Fatalf("CoreTool template lookup failed: %v\n%s", err, result.Flatten())
		}
		var found struct {
			Templates []string `json:"templates"`
		}
		if err := json.Unmarshal([]byte(result.Flatten()), &found); err != nil {
			t.Fatalf("CoreTool template lookup returned invalid JSON: %v", err)
		}
		if len(found.Templates) != 1 || found.Templates[0] != templateRelative {
			t.Fatalf("CoreTool template lookup did not find the fixture: %+v", found)
		}
	}
	assertToolScan := func(tool actool.CoreTool) {
		t.Helper()
		requestsBefore := localRequests.Load()
		result, err := tool.Call(ctx, raw, nil)
		if err != nil || result.IsError {
			t.Fatalf("CoreTool local scan failed: %v\n%s", err, result.Flatten())
		}
		matched := false
		for _, line := range strings.Split(result.Flatten(), "\n") {
			var finding struct {
				TemplateID string `json:"template-id"`
				MatchedAt  string `json:"matched-at"`
			}
			if json.Unmarshal([]byte(line), &finding) == nil && finding.TemplateID == "artex-local-smoke" && finding.MatchedAt == fixture.URL+"/" {
				matched = true
			}
		}
		if !matched || localRequests.Load() <= requestsBefore {
			t.Fatalf("CoreTool did not return a match from the local HTTP target: %s", result.Flatten())
		}
	}
	assertTemplateSearch(heldTools["nuclei_templates"])
	assertToolScan(heldTools["nuclei_scan"])
	t.Log("Agent CoreTool template lookup and local HTTP match confirmed")

	m.mu.Lock()
	m.state.Enabled = false
	m.mu.Unlock()
	if tools := collectTools(); len(tools) != 0 {
		t.Fatal("disabled toolpack still exposes Agent tools")
	}
	requestsBeforeDisabled := localRequests.Load()
	for name, input := range map[string]json.RawMessage{"nuclei_templates": query, "nuclei_scan": raw} {
		result, err := heldTools[name].Call(ctx, input, nil)
		if err != nil || !result.IsError {
			t.Fatalf("previously held %s did not reject a call while disabled: %v\n%s", name, err, result.Flatten())
		}
	}
	if localRequests.Load() != requestsBeforeDisabled {
		t.Fatal("disabled CoreTool reached the HTTP target")
	}
	m.mu.Lock()
	m.state.Enabled = true
	m.mu.Unlock()
	if tools := collectTools(); len(tools) != 2 {
		t.Fatal("re-enabled toolpack did not expose both Agent tools")
	}
	assertTemplateSearch(heldTools["nuclei_templates"])
	assertToolScan(heldTools["nuclei_scan"])
	t.Log("disabled tools hidden and retained CoreTools rejected; re-enable restored real calls")

	reloaded := newToolpackManager(data)
	if r := reloaded.snapshot(); r.Engine.Path != s.Engine.Path || r.Templates.Path != s.Templates.Path || !r.Enabled {
		t.Fatal("real installed state did not survive reload")
	}
	t.Log("installed paths and enabled state survived reload without changing runtime configuration")
	if reuseInstalled || os.Getenv("ARTEX_TOOLPACK_LIVE_KEEP") == "1" {
		t.Log("keeping installed package for manual review")
		return
	}
	if err := m.uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Engine.Path); !os.IsNotExist(err) {
		t.Fatal("real package uninstall left engine directory")
	}
	if _, err := os.Stat(s.Templates.Path); !os.IsNotExist(err) {
		t.Fatal("real package uninstall left templates directory")
	}
	t.Log("real package uninstall confirmed")
}
