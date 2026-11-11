//go:build embedui

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedWorkbenchRoutes(t *testing.T) {
	h := (&Server{}).webuiHandler()
	for _, route := range []string{"/chat/", "/system/agents/", "/system/llm/", "/system/tools/", "/settings/", "/function/workspace/", "/system/skills/"} {
		expected, err := webuiDist.ReadFile("webui/dist" + route + "index.html")
		if err != nil {
			t.Fatalf("missing exported page for %s: %v", route, err)
		}
		for _, path := range []string{strings.TrimSuffix(route, "/"), route} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				r := httptest.NewRecorder()
				h.ServeHTTP(r, httptest.NewRequest(method, path, nil))
				if r.Code != http.StatusOK {
					t.Errorf("%s %s: got %d, want 200", method, path, r.Code)
				}
				if !strings.HasPrefix(r.Header().Get("Content-Type"), "text/html") {
					t.Errorf("%s %s: expected HTML content type", method, path)
				}
				if method == http.MethodGet && !bytes.Equal(r.Body.Bytes(), expected) {
					t.Errorf("GET %s did not serve its exported page", path)
				}
				if method == http.MethodHead && r.Body.Len() != 0 {
					t.Errorf("HEAD %s returned a body", path)
				}
			}
		}
	}
	for _, path := range []string{"/dashboard", "/function/tasks", "/system/not-a-route", "/_next/missing.js"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r := httptest.NewRecorder()
			h.ServeHTTP(r, httptest.NewRequest(method, path, nil))
			if r.Code != http.StatusNotFound {
				t.Errorf("%s %s: got %d", method, path, r.Code)
			}
			if method == http.MethodHead && r.Body.Len() != 0 {
				t.Errorf("HEAD %s returned a body", path)
			}
		}
	}
}
