package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderTemplateCommand(t *testing.T) {
	// command: string params shell-quoted; array → JSON (also quoted).
	got := renderTemplate("nmap -p {ports} {target}", map[string]any{
		"target": "10.0.0.1; rm -rf /", // injection attempt → must be single-quoted
		"ports":  []any{80.0, 443.0},
	}, shellQuote)
	if !strings.Contains(got, `'10.0.0.1; rm -rf /'`) {
		t.Fatalf("target not shell-quoted: %q", got)
	}
	if strings.Contains(got, "rm -rf /'") && !strings.Contains(got, `'10.0.0.1; rm -rf /'`) {
		t.Fatalf("possible injection leak: %q", got)
	}
	if strings.Contains(got, "{target}") || strings.Contains(got, "{ports}") {
		t.Fatalf("placeholders not replaced: %q", got)
	}
}

func TestRenderTemplateHTTP(t *testing.T) {
	// http: identity (no shell quoting) — raw substitution into url/body.
	got := renderTemplate("https://x/submit?flag={flag}", map[string]any{"flag": "CTF{abc}"}, identity)
	if got != "https://x/submit?flag=CTF{abc}" {
		t.Fatalf("http render: %q", got)
	}
}

func TestScalarStr(t *testing.T) {
	cases := []struct {
		v    any
		want string
		ok   bool
	}{
		{"hi", "hi", true},
		{true, "true", true},
		{float64(80), "80", true},   // integer-valued float → no decimal
		{float64(1.5), "1.5", true}, // real float
		{[]any{1, 2}, "", false},    // array → not scalar
		{map[string]any{}, "", false},
	}
	for _, c := range cases {
		got, ok := scalarStr(c.v)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("scalarStr(%v) = (%q,%v), want (%q,%v)", c.v, got, ok, c.want, c.ok)
		}
	}
}

func TestEnsureSchema(t *testing.T) {
	// empty → thin {args:string}
	m := ensureSchema(nil)
	props, _ := m["properties"].(map[string]any)
	if _, ok := props["args"]; !ok {
		t.Fatalf("empty schema should default to args: %v", m)
	}
	// non-empty → passthrough
	raw := json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"}}}`)
	m2 := ensureSchema(raw)
	p2, _ := m2["properties"].(map[string]any)
	if _, ok := p2["target"]; !ok {
		t.Fatalf("non-empty schema should pass through: %v", m2)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shellQuote(a'b) = %q", got)
	}
}

// TestExecPython runs a real Python script end-to-end: it must read params from
// stdin JSON and the mirrored env var, then print — verifying the whole script
// param-passing path using the same validated detection as script tools.
func TestExecPython(t *testing.T) {
	interp := detectPython()
	if interp == "" {
		t.Skip("a runnable Python 3 interpreter is unavailable")
	}
	code := `import json,sys,os
a = json.load(sys.stdin)
print("stdin_target=" + a["target"])
print("env_target=" + os.environ.get("TOOL_TARGET",""))
print("port=" + str(a["port"]))
`
	out, err := execPython(context.Background(), interp, "test", code,
		map[string]any{"target": "example.com", "port": float64(8080)},
		t.TempDir(), nil, 10*time.Second)
	if err != nil {
		t.Fatalf("execPython error: %v", err)
	}
	for _, want := range []string{"stdin_target=example.com", "env_target=example.com", "port=8080"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// TestRunHTTPTool functionally exercises the http executor against a local server:
// method/url/header/body templates are rendered from params, the request is sent,
// and the response status+body are returned. No recording proxy → s.m untouched.
func TestRunHTTPTool(t *testing.T) {
	var gotMethod, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	execRaw, _ := json.Marshal(map[string]any{
		"method":  "POST",
		"url":     srv.URL + "/submit?flag={flag}",
		"headers": map[string]string{"Authorization": "Bearer {token}"},
		"body":    `{"flag":"{flag}"}`,
	})
	res, err := (&Server{}).runHTTPTool(context.Background(), execRaw,
		map[string]any{"flag": "CTF{x}", "token": "sekret"}, nil)
	if err != nil {
		t.Fatalf("runHTTPTool: %v", err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	var out struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	_ = json.Unmarshal([]byte(res.Content[0].Text), &out)

	if gotMethod != "POST" {
		t.Fatalf("method: %q", gotMethod)
	}
	if gotAuth != "Bearer sekret" {
		t.Fatalf("header template not rendered: %q", gotAuth)
	}
	if gotBody != `{"flag":"CTF{x}"}` {
		t.Fatalf("body template not rendered: %q", gotBody)
	}
	if out.Status != 201 || !strings.Contains(out.Body, `"ok":true`) {
		t.Fatalf("response not captured: %+v", out)
	}
}

func TestDetectPython(t *testing.T) {
	p := detectPython()
	if p == "" {
		t.Skip("a runnable Python 3 interpreter is unavailable")
	}
	if !filepath.IsAbs(p) || !probePython3(p) {
		t.Fatalf("detected interpreter is not an absolute runnable Python 3 path: %q", p)
	}
	t.Logf("validated Python 3 interpreter: %s", p)
}

func TestDetectPythonSkipsUnusableCandidates(t *testing.T) {
	dir := t.TempDir()
	py3, py := filepath.Join(dir, "python3.exe"), filepath.Join(dir, "python.exe")
	for _, tc := range []struct {
		name      string
		available map[string]string
		runnable  map[string]bool
		want      string
	}{
		{"store alias falls back", map[string]string{"python3": py3, "python": py}, map[string]bool{py: true}, py},
		{"working python3 preferred", map[string]string{"python3": py3, "python": py}, map[string]bool{py3: true, py: true}, py3},
		{"only python available", map[string]string{"python": py}, map[string]bool{py: true}, py},
		{"neither candidate runnable", map[string]string{"python3": py3, "python": py}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := detectPythonWith(func(name string) (string, error) {
				if path, ok := tc.available[name]; ok {
					return path, nil
				}
				return "", errors.New("not found")
			}, func(path string) bool { return tc.runnable[path] })
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPython3Version(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   bool
	}{
		{"Python 3.12.0\r\n", true}, {"Python 3.15.0a1\n", true},
		{"Python 2.7.18", false}, {"Python 30.1.0", false}, {"Python 3.bad", false},
		{"Python was not found; run without arguments to install", false}, {"", false},
	} {
		if got := isPython3Version(tc.output); got != tc.want {
			t.Errorf("isPython3Version(%q)=%v, want %v", tc.output, got, tc.want)
		}
	}
}

func TestPythonInterpreterRepairPreservesUserPaths(t *testing.T) {
	local := t.TempDir()
	alias := filepath.Join(local, "Microsoft", "WindowsApps", "python3.exe")
	custom := filepath.Join(local, "MyPython", "python.exe")
	replacement := filepath.Join(local, "Programs", "Python312", "python.exe")
	for _, tc := range []struct {
		name, stored, platform, detected string
		runnable, wantSave               bool
		want                             string
	}{
		{"unset is seeded", "", "windows", replacement, false, true, replacement},
		{"broken store alias repaired", alias, "windows", replacement, false, true, replacement},
		{"working store python preserved", alias, "windows", replacement, true, false, alias},
		{"broken explicit path preserved", custom, "windows", replacement, false, false, custom},
		{"explicit command preserved", "my-python", "windows", replacement, false, false, "my-python"},
		{"other platform preserved", alias, "linux", replacement, false, false, alias},
		{"no usable replacement does not save", alias, "windows", "", false, false, ""},
		{"nested lookalike preserved", filepath.Join(filepath.Dir(alias), "other", "python3.exe"), "windows", replacement, false, false, filepath.Join(filepath.Dir(alias), "other", "python3.exe")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probed := false
			got, save := selectPythonInterpreter(tc.stored, func(path string) bool { return isWindowsStorePython(path, tc.platform, local) }, func(path string) bool { probed = true; return tc.runnable }, func() string { return tc.detected })
			if got != tc.want || save != tc.wantSave {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, save, tc.want, tc.wantSave)
			}
			if tc.stored != "" && !isWindowsStorePython(tc.stored, tc.platform, local) && probed {
				t.Fatal("an explicit non-Store setting must not be probed or replaced")
			}
		})
	}
}

// The http tool must truncate an oversized response through norma's Capture, the
// same valve every other tool uses — a session with no OutputDir falls back to a
// head+tail cut, so a body far past the default cap comes back shortened.
func TestHTTPToolTruncatesLargeBody(t *testing.T) {
	big := strings.Repeat("x", 40000) // past Capture's 30000 default
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	execRaw, _ := json.Marshal(map[string]any{"method": "GET", "url": srv.URL})
	res, err := (&Server{}).runHTTPTool(context.Background(), execRaw, map[string]any{}, nil)
	if err != nil {
		t.Fatalf("runHTTPTool: %v", err)
	}
	out := res.Flatten()
	if len(out) >= 40000 {
		t.Fatalf("oversized http body was not truncated: len=%d", len(out))
	}
}
