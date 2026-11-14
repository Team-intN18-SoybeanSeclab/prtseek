package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func fetchFixture(t *testing.T, tool actool.CoreTool, url, dir string, extract bool) (httpObservation, actool.Result) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"url": url, "extract": extract, "max_chars": 500})
	result, err := tool.Call(t.Context(), raw, &actool.ToolContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var o httpObservation
	if !result.IsError {
		if err = json.Unmarshal([]byte(result.Flatten()), &o); err != nil {
			t.Fatal(err)
		}
	}
	return o, result
}
func TestHTTPObservationRedirectBodyBudgetAndEmptySuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/article", 302)
		case "/article":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<nav>noise-navigation</nav><main><h1>signal</h1><p>" + strings.Repeat("content ", 300) + "</p><a href='/proof'>source</a></main><script src='/app.js'></script>"))
		case "/large":
			_, _ = w.Write([]byte(strings.Repeat("x", httpBodyLimit+200)))
		case "/empty":
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	tool := newObservedWebFetch("", "")
	o, res := fetchFixture(t, tool, server.URL+"/start", dir, true)
	if res.IsError || o.Status != 200 || o.FinalURL != server.URL+"/article" || len(o.Redirects) != 1 || o.Redirects[0].Status != 302 || !o.TextTruncated || strings.Contains(o.Content, "noise-navigation") || !strings.Contains(o.Content, "signal") {
		t.Fatalf("%s", res.Flatten())
	}
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(o.Artifact)))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	if o.SHA256 != hex.EncodeToString(h[:]) || o.BodyBytes != len(b) {
		t.Fatal("artifact hash mismatch")
	}
	o, res = fetchFixture(t, tool, server.URL+"/large", dir, false)
	if res.IsError || !o.BodyTruncated || o.BodyBytes != httpBodyLimit || !strings.Contains(o.Note, "前缀") {
		t.Fatalf("%s", res.Flatten())
	}
	o, res = fetchFixture(t, tool, server.URL+"/empty", dir, false)
	if res.IsError || !strings.Contains(o.Note, "空正文") {
		t.Fatalf("%s", res.Flatten())
	}
	_, res = fetchFixture(t, tool, server.URL+"/404", dir, false)
	if !res.IsError {
		t.Fatal("404 not marked as error")
	}
}
func TestHTTPObservationTLSCAAndNoProxyFallback(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("trusted fixture")) }))
	defer server.Close()
	dir := t.TempDir()
	_, res := fetchFixture(t, newObservedWebFetch("", ""), server.URL, dir, false)
	if !res.IsError {
		t.Fatal("untrusted TLS accepted")
	}
	cert := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	o, res := fetchFixture(t, newObservedWebFetch("", cert), server.URL, dir, false)
	if res.IsError || o.Content != "trusted fixture" {
		t.Fatalf("%s", res.Flatten())
	}
	_, res = fetchFixture(t, newObservedWebFetch("http://127.0.0.1:1", cert), server.URL, dir, false)
	if !res.IsError || !strings.Contains(res.Flatten(), "configured_proxy") {
		t.Fatalf("unexpected proxy fallback: %s", res.Flatten())
	}
	_, res = fetchFixture(t, newObservedWebFetch("", filepath.Join(dir, "missing.pem")), server.URL, dir, false)
	if !res.IsError || !strings.Contains(res.Flatten(), "配置错误") {
		t.Fatal(res.Flatten())
	}
}
func TestFocusedHTMLPreservesExtractedEvidence(t *testing.T) {
	out := focusedHTML(`<nav>noise</nav><article>answer<a href="/detail">detail</a></article><!-- marker --><form action="/check"><input name="token" value="fixture"></form><script src="/code.js"></script>`, true)
	for _, want := range []string{"answer", "/detail", "marker", "action=/check", "name=token", "src=/code.js"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s: %s", want, out)
		}
	}
	if strings.Contains(out, "noise") {
		t.Fatal(out)
	}
}

func TestHTTPObservationRedirectLoopAndInvalidURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", 302) }))
	defer server.Close()
	tool := newObservedWebFetch("", "")
	dir := t.TempDir()
	for _, u := range []string{server.URL + "/loop", "file:///secret", "http://user:password@localhost/"} {
		_, res := fetchFixture(t, tool, u, dir, false)
		if !res.IsError {
			t.Fatalf("accepted invalid/loop URL: %s", u)
		}
	}
}

func TestHostHTTPEnvironmentMatchesConfiguredRoute(t *testing.T) {
	for _, proxy := range []string{"", "http://127.0.0.1:8080"} {
		env := hostHTTPEnv(proxy, "fixture-ca.pem")
		set := map[string]string{}
		for _, v := range env {
			key, value, _ := strings.Cut(v, "=")
			set[key] = value
		}
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			if value, ok := set[key]; !ok || value != proxy {
				t.Fatalf("%s route mismatch: %q", key, value)
			}
		}
		if set["NO_PROXY"] != "" || set["REQUESTS_CA_BUNDLE"] != "fixture-ca.pem" || set["CURL_CA_BUNDLE"] != "fixture-ca.pem" {
			t.Fatal(env)
		}
	}
}
