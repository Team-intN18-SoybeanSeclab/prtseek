package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestPersonalReasoningCandidates(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  []string
	}{
		{"deepseek-flash", []string{"low", "high", "max"}},
		{"deepseek-v4-pro", []string{"low", "high", "max"}},
		{" DeepSeek-Flash ", []string{"low", "high", "max"}},
		{"deepseek-unknown", nil},
		{"gpt-5-test", []string{"low", "medium", "high"}},
		{"claude-sonnet-4", []string{"low", "medium", "high"}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := reasoningCandidates(agent.Config{Model: tc.model}); !slices.Equal(got, tc.want) {
				t.Fatalf("efforts = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPersonalModelErrorsDoNotExposeCredentials(t *testing.T) {
	const secret = "test-fixture-secret-never-print-82719"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid model/key ` + secret + `"}}`))
	}))
	defer remote.Close()
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	s := &Server{}
	body, _ := json.Marshal(map[string]any{"provider": "openai", "model": "missing-model", "base_url": remote.URL + "/v1", "api_key": secret, "streaming": false})
	for _, handler := range []http.HandlerFunc{s.testLLM, s.pgListModels} {
		response := retestRequest(handler, http.MethodPost, 0, string(body))
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("credential escaped into API response")
		}
		if !strings.Contains(response.Body.String(), `"ok":false`) {
			t.Fatalf("failed model incorrectly passed: %s", response.Body)
		}
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("credential escaped into connection-test logs")
	}
	if !strings.Contains(logs.String(), "[REDACTED]") {
		t.Fatal("fixture did not exercise response redaction")
	}
}

func TestListModelsFollowsPaginationAndDeduplicates(t *testing.T) {
	var requests int
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"model-c"},{"id":"model-a"}],"has_more":true,"last_id":"model-a"}`))
		case "model-a":
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"}],"has_more":false,"last_id":"model-b"}`))
		default:
			t.Fatalf("unexpected cursor: %q", r.URL.Query().Get("after_id"))
		}
	}))
	defer remote.Close()

	s := &Server{}
	body, _ := json.Marshal(map[string]any{
		"provider": "openai", "base_url": remote.URL + "/v1", "api_key": "test-key",
	})
	response := retestRequest(s.pgListModels, http.MethodPost, 0, string(body))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		OK     bool     `json:"ok"`
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || !slices.Equal(result.Models, []string{"model-c", "model-a", "model-b"}) {
		t.Fatalf("models = %v", result.Models)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestPersonalLanguageStableUntilExplicitChange(t *testing.T) {
	cases := []struct{ message, previous, want string }{
		{"帮我检查本地文件", "", "zh"}, {"Please inspect local files", "", "en"},
		{"curl http://localhost", "zh", "zh"}, {"继续，用英文回复", "zh", "en"}, {"reply in Chinese", "en", "zh"},
	}
	for _, c := range cases {
		if got := inferPersonalLanguage(c.message, c.previous); got != c.want {
			t.Errorf("%q: got %s want %s", c.message, got, c.want)
		}
	}
}

func TestPersonalEffortProbeAndConversationIsolation(t *testing.T) {
	s, c := newPersonalBackgroundTestServer(t)
	var wire map[string]any
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer endpoint.Close()
	id, err := s.m.pg.SaveProfile(&db.LLMProfile{Name: "effort contract fixture", Format: "openai", BaseURL: endpoint.URL + "/v1", APIKey: "local-test-key", Model: "gpt-5-test", Streaming: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.m.pg.Exec(`UPDATE conversations SET llm_profile_id=NULL WHERE id=$1`, c.ID)
		_, _ = s.m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id)
	})
	c.LLMProfileID = &id
	if err := s.validatePersonalEffort(c, "high"); err == nil {
		t.Fatal("unverified effort accepted")
	}
	body, _ := json.Marshal(map[string]any{"profile_id": id, "reasoning_effort": "high"})
	w := retestRequest(s.personalModelCapabilities, http.MethodPost, 0, string(body))
	if w.Code != 200 {
		t.Fatalf("probe: %d %s", w.Code, w.Body)
	}
	if wire["reasoning_effort"] != "high" || wire["model"] != "gpt-5-test" {
		t.Fatalf("actual request differs: %+v", wire)
	}
	if err := s.updatePersonalProfile(c, &id, "high", "zh", nil); err != nil {
		t.Fatal(err)
	}
	got := s.personalConversationView(c)
	if got.EffectiveModel != "gpt-5-test" || got.EffectiveReasoningEffort != "high" {
		t.Fatalf("effective view: %+v", got)
	}
	other, err := s.m.pg.CreateConversation("auto", "other conversation", &id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.m.pg.DeleteConversation(other.ID)
	if s.personalConversationView(other).ReasoningEffort != "" {
		t.Fatal("effort leaked to another conversation")
	}
	p, _ := s.m.pg.ProfileByID(id)
	if p.ReasoningEffort != "" {
		t.Fatal("shared profile was mutated")
	}
	_, _ = s.m.pg.Exec(`UPDATE llm_profiles SET model='different-model' WHERE id=$1`, id)
	if err := s.validatePersonalEffort(c, "high"); err == nil {
		t.Fatal("old probe stayed valid after model change")
	}
}

func TestPersonalOnboardingPersistsSkippedState(t *testing.T) {
	s, _ := newPersonalBackgroundTestServer(t)
	previous, _, _ := s.m.pg.GetSetting("personal_onboarding_v1")
	t.Cleanup(func() { _ = s.m.pg.SetSetting("personal_onboarding_v1", previous) })
	w := retestRequest(s.personalOnboarding, http.MethodPatch, 0, `{"model":"skipped","tools":"skipped"}`)
	if w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	w = retestRequest(s.personalOnboarding, http.MethodGet, 0, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"model":"skipped"`) || !strings.Contains(w.Body.String(), `"tools":"skipped"`) {
		t.Fatalf("reload lost skipped state: %s", w.Body)
	}
}

func TestPersonalBuildDoesNotUseUpstreamUpdater(t *testing.T) {
	old := BuildVersion
	BuildVersion = "personal-v1.0.0"
	defer func() { BuildVersion = old }()
	s := &Server{}
	for _, h := range []http.HandlerFunc{s.updateApply, s.updateRollback} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodPost, "/", nil))
		if w.Code != 409 {
			t.Fatalf("upstream action allowed: %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.updateCheck(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"has_update":false`) {
		t.Fatalf("check: %s", w.Body)
	}
}
