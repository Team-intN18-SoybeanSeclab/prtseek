package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestBrandHealthAndUpdateProtection(t *testing.T) {
	old := BuildVersion
	t.Cleanup(func() { BuildVersion = old })
	s := &Server{}
	for _, version := range []string{"prtseek-v1.3.0", "prtseek-dev", "personal-v1.2.0"} {
		BuildVersion = version
		w := httptest.NewRecorder()
		s.health(w, httptest.NewRequest("GET", "/api/health", nil))
		var health map[string]any
		if json.Unmarshal(w.Body.Bytes(), &health) != nil || health["service"] != "prtseek" || health["version"] != version {
			t.Fatalf("wrong public health identity: %s", w.Body.String())
		}
		w = httptest.NewRecorder()
		s.updateCheck(w, httptest.NewRequest("GET", "/api/update/check", nil))
		var update map[string]any
		if json.Unmarshal(w.Body.Bytes(), &update) != nil || update["has_update"] != false || update["mode"] != "personal" {
			t.Fatalf("fork exposed upstream update: %s", w.Body.String())
		}
		for _, handler := range []http.HandlerFunc{s.updateApply, s.updateRollback} {
			w = httptest.NewRecorder()
			handler(w, httptest.NewRequest("POST", "/api/update", nil))
			if w.Code != http.StatusConflict {
				t.Fatalf("fork update/rollback was not blocked: %d", w.Code)
			}
		}
	}
}

func TestBrandLoginAndTokenCompatibility(t *testing.T) {
	for _, username := range []string{"prtseek", "ARTEX"} {
		if !validLoginUsername(username) {
			t.Fatalf("current or legacy username rejected: %s", username)
		}
	}
	if validLoginUsername("unrelated-user") {
		t.Fatal("unrelated administrator username accepted")
	}
	key := []byte("fixture-signing-key-not-a-real-secret")
	token, err := signJWT(key)
	if err != nil || !verifyJWT(token, key) {
		t.Fatalf("new token failed verification: %v", err)
	}
	claims := &jwt.RegisteredClaims{}
	if _, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return key, nil }); err != nil || claims.Subject != "prtseek" {
		t.Fatalf("new subject=%q err=%v", claims.Subject, err)
	}
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: "ARTEX", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).SignedString(key)
	if err != nil || !verifyJWT(legacy, key) {
		t.Fatalf("pre-rename administrator token invalidated: %v", err)
	}
	r := httptest.NewRequest("GET", "/api/tasks", nil)
	r.AddCookie(&http.Cookie{Name: "artex_token", Value: legacy})
	if extractToken(r) != legacy {
		t.Fatal("pre-rename cookie was not read")
	}
	r.AddCookie(&http.Cookie{Name: "prtseek_token", Value: token})
	if extractToken(r) != token {
		t.Fatal("current cookie did not take precedence")
	}
}

func TestBrandBasicAuthCookieCompatibility(t *testing.T) {
	s, _ := basicAuthTestServer(t)
	cfg := basicAuthConfig{Enabled: true, Revision: "fixture-revision"}
	// Mint exactly the old cookie format, independently of the new setter.
	mac := hmac.New(sha256.New, s.jwtKey)
	mac.Write([]byte("artex/http-basic-auth/session/v1"))
	legacyToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: cfg.Revision, Audience: jwt.ClaimStrings{"artex-basic-auth"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).SignedString(mac.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "artex_basic_auth", Value: legacyToken})
	if !s.validBasicAuthCookie(r, cfg) {
		t.Fatal("pre-rename gate cookie was invalidated")
	}
	w := httptest.NewRecorder()
	if err := s.setBasicAuthCookie(w, r, cfg); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != "prtseek_basic_auth" || cookies[1].Name != "artex_basic_auth" || cookies[1].MaxAge != -1 {
		t.Fatal("new gate cookie did not replace and expire the old name")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookies[0])
	if !s.validBasicAuthCookie(r, cfg) {
		t.Fatal("new gate cookie cannot authenticate")
	}
	cfg.Revision = "changed-credentials"
	if s.validBasicAuthCookie(r, cfg) {
		t.Fatal("branding compatibility bypassed credential revocation")
	}
}

func TestBrandRepeaterEnvironmentCompatibility(t *testing.T) {
	t.Setenv("prtseek_REPEATER_ALLOW_LOCAL", "")
	if err := os.Unsetenv("prtseek_REPEATER_ALLOW_LOCAL"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_REPEATER_ALLOW_LOCAL", "1")
	if !repeaterAllowLocal() {
		t.Fatal("legacy repeater opt-in lost")
	}
	t.Setenv("prtseek_REPEATER_ALLOW_LOCAL", "0")
	if repeaterAllowLocal() {
		t.Fatal("primary disable did not override legacy opt-in")
	}
}
