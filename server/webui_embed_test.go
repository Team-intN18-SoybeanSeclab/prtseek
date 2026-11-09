//go:build embedui

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddedWorkbenchRoutes(t *testing.T) {
	h := (&Server{}).webuiHandler()
	for _, path := range []string{"/chat/", "/system/agents/", "/system/llm/", "/settings/", "/function/workspace/", "/system/skills/"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if r.Code != http.StatusOK {
			t.Errorf("%s: got %d", path, r.Code)
		}
	}
	for _, path := range []string{"/dashboard", "/function/tasks", "/system/tools", "/_next/missing.js"} {
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
