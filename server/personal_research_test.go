package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// TestMain selects a temporary SQLite database, never a development database.
func newPersonalResearchTestServer(t *testing.T) (*Server, *db.Conversation) {
	t.Helper()
	return newPersonalBackgroundTestServer(t)
}

func personalResearchRequest(s *Server, conversationID int64, query url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/conversations/"+strconv.FormatInt(conversationID, 10)+"/research?"+query.Encode(), nil)
	r.SetPathValue("id", strconv.FormatInt(conversationID, 10))
	w := httptest.NewRecorder()
	s.personalResearchState(w, r)
	return w
}

type personalResearchTestResponse struct {
	Enabled bool                        `json:"enabled"`
	Version string                      `json:"version"`
	Memory  *agent.ResearchRecallResult `json:"memory"`
}

func personalResearchReadResponse(t *testing.T, w *httptest.ResponseRecorder) personalResearchTestResponse {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("research response: status=%d body=%s", w.Code, w.Body.String())
	}
	var body personalResearchTestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != "1.2.0" {
		t.Fatalf("unexpected research version %q", body.Version)
	}
	return body
}

func personalResearchProfile(t *testing.T, s *Server, c *db.Conversation, model string) int64 {
	t.Helper()
	id, err := s.m.pg.SaveProfile(&db.LLMProfile{
		Name: "research-fixture-" + strconv.FormatInt(c.ID, 10), Format: "openai",
		BaseURL: "http://127.0.0.1:1/v1", APIKey: "fixture-not-a-real-key", Model: model,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.UpdateConversationProfile(c.ID, &id); err != nil {
		t.Fatal(err)
	}
	c.LLMProfileID = &id
	t.Cleanup(func() {
		// The outer fixture deletes this conversation and its task afterwards.
		_ = s.m.pg.UpdateConversationProfile(c.ID, nil)
		_, _ = s.m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id)
	})
	return id
}

func TestPersonalResearchReadDoesNotCreateMemory(t *testing.T) {
	s, c := newPersonalResearchTestServer(t)
	personalResearchProfile(t, s, c, "deepseek-flash")
	root := filepath.Join(s.m.dir, "sessions", s.convBusyKey(c.ID))
	body := personalResearchReadResponse(t, personalResearchRequest(s, c.ID, nil))
	if !body.Enabled || body.Memory != nil {
		t.Fatalf("fresh session should have enabled=true and memory=null: %+v", body)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("GET created a session directory or stat failed: %v", err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	body = personalResearchReadResponse(t, personalResearchRequest(s, c.ID, url.Values{"verify": {"true"}}))
	if body.Memory == nil || len(body.Memory.Entries) != 0 || len(body.Memory.Observations) != 0 {
		t.Fatalf("existing empty session returned unexpected memory: %+v", body.Memory)
	}
	if _, err := os.Stat(filepath.Join(root, ".research")); !os.IsNotExist(err) {
		t.Fatalf("read-only GET created research files or stat failed: %v", err)
	}
}

func TestPersonalResearchConversationIsolationAndEvidenceHealth(t *testing.T) {
	s, first := newPersonalResearchTestServer(t)
	second, err := s.m.pg.CreateConversation("auto", "separate research session", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.m.pg.DeleteConversation(second.ID); err != nil {
			t.Error(err)
		}
	})
	personalResearchProfile(t, s, first, "deepseek-flash")
	personalResearchProfile(t, s, second, "fixture-other-model")
	roots := make(map[int64]string)
	for _, c := range []*db.Conversation{first, second} {
		root := filepath.Join(s.m.dir, "sessions", s.convBusyKey(c.ID))
		roots[c.ID] = root
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		marker := "session-marker-" + strconv.FormatInt(c.ID, 10)
		if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte(marker), 0600); err != nil {
			t.Fatal(err)
		}
		memory, err := agent.NewResearchMemory(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := memory.Record(context.Background(), agent.ResearchRecordInput{Entry: &agent.ResearchEntryInput{
			Kind: "fact", Summary: marker,
			Evidence: []agent.ResearchEvidenceInput{{Path: "evidence.txt", Description: "local fixture"}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []*db.Conversation{first, second} {
		// Extra path/root parameters cannot override the conversation-selected root.
		query := url.Values{"verify": {"true"}, "path": {roots[second.ID]}, "root": {roots[second.ID]}}
		body := personalResearchReadResponse(t, personalResearchRequest(s, c.ID, query))
		if body.Enabled != (c.ID == first.ID) {
			t.Fatalf("model capability leaked across sessions: conversation=%d enabled=%v", c.ID, body.Enabled)
		}
		if body.Memory == nil || len(body.Memory.Entries) != 1 || body.Memory.Entries[0].Summary != "session-marker-"+strconv.FormatInt(c.ID, 10) {
			t.Fatalf("wrong session memory: conversation=%d memory=%+v", c.ID, body.Memory)
		}
		if len(body.Memory.EvidenceHealth) != 1 || body.Memory.EvidenceHealth[0].Status != "current" {
			t.Fatalf("original evidence was not current: %+v", body.Memory.EvidenceHealth)
		}
	}
	if err := os.WriteFile(filepath.Join(roots[first.ID], "evidence.txt"), []byte("changed fixture evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	body := personalResearchReadResponse(t, personalResearchRequest(s, first.ID, url.Values{"verify": {"true"}}))
	if body.Memory == nil || len(body.Memory.EvidenceHealth) != 1 || body.Memory.EvidenceHealth[0].Status != "changed" {
		t.Fatalf("changed evidence was not detected: %+v", body.Memory)
	}
	other := personalResearchReadResponse(t, personalResearchRequest(s, second.ID, url.Values{"verify": {"true"}}))
	if other.Memory == nil || len(other.Memory.EvidenceHealth) != 1 || other.Memory.EvidenceHealth[0].Status != "current" {
		t.Fatalf("one session's evidence change affected another: %+v", other.Memory)
	}
}

func TestPersonalResearchQueryLimitsAndMissingConversation(t *testing.T) {
	s, c := newPersonalResearchTestServer(t)
	personalResearchProfile(t, s, c, "deepseek-flash")
	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"limit", strings.Repeat("a", 500), http.StatusOK},
		{"over_limit", strings.Repeat("a", 501), http.StatusBadRequest},
		{"utf8_byte_limit", strings.Repeat("界", 167), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := personalResearchRequest(s, c.ID, url.Values{"q": {tc.query}})
			if w.Code != tc.want {
				t.Fatalf("query length=%d status=%d want=%d body=%s", len(tc.query), w.Code, tc.want, w.Body.String())
			}
		})
	}
	if w := personalResearchRequest(s, -1, nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing conversation: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPersonalResearchBackgroundUsesConversationRootAndSnapshotModel(t *testing.T) {
	s, c := newPersonalResearchTestServer(t)
	oldLanguage, oldRoot := agent.TaskResponseLanguage, agent.TaskResearchRoot
	t.Cleanup(func() { agent.TaskResponseLanguage, agent.TaskResearchRoot = oldLanguage, oldRoot })
	if err := s.initPersonalSettings(); err != nil {
		t.Fatal(err)
	}
	profileID := personalResearchProfile(t, s, c, "deepseek-flash")
	cfg, ok := s.loadProfileConfig(profileID)
	if !ok {
		t.Fatal("fixture model profile unavailable")
	}
	snapshot, err := json.Marshal(makePersonalRunSnapshot(cfg, profileID, "", "zh"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.m.CreateTaskWithOptions("research background", "continue local fixture research", db.TaskCreateOptions{
		WorkerCount: 1, TimeoutSeconds: 3600,
		PersonalHandoff: &db.PersonalHandoffInput{ConversationID: c.ID, IdempotencyKey: "research-root", Checkpoint: "existing local evidence", ConfigSnapshot: snapshot},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(s.m.dir, "sessions", s.convBusyKey(c.ID))
	if got := agent.TaskResearchRoot(taskID); got != wantRoot {
		t.Fatalf("background root=%q want conversation root=%q", got, wantRoot)
	}
	if got := agent.TaskResearchRoot(-1); got != "" {
		t.Fatalf("missing task acquired a root: %q", got)
	}
	if _, err := s.m.pg.Exec(`UPDATE llm_profiles SET model=$1 WHERE id=$2`, "fixture-profile-changed-after-handoff", profileID); err != nil {
		t.Fatal(err)
	}
	runtime := &taskLLMRuntime{s: s, taskID: task.ID, agentKey: "personal"}
	if got := runtime.ResearchModel(); got != "deepseek-flash" {
		t.Fatalf("research mode resolved %q instead of frozen deepseek-flash model", got)
	}
}

func TestPersonalResearchFlashProtocolErrorsDoNotRetry(t *testing.T) {
	protocolErr := &agent.FlashProtocolError{Code: "missing_reasoning", Detail: "fixture protocol failure"}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"protocol", protocolErr, false},
		{"wrapped_protocol", fmt.Errorf("outer transport: %w", protocolErr), false},
		{"transport_eof", io.EOF, true},
		{"wrapped_transport_eof", fmt.Errorf("read stream: %w", io.EOF), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableStreamError(tc.err); got != tc.want {
				t.Errorf("stream retry=%v want=%v", got, tc.want)
			}
			if got := retryableWorkerModelError(harness.ReasonModelError, tc.err); got != tc.want {
				t.Errorf("worker retry=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestPersonalResearchCommittedStreamErrorDoesNotReplay(t *testing.T) {
	for _, profileID := range []int64{0, 11} {
		for _, event := range []llm.StreamEvent{
			{Type: llm.SETextDelta, Text: "partial answer"},
			{Type: llm.SEToolUseStart},
		} {
			for _, cause := range []error{io.EOF, &agent.FlashRemoteError{Status: 503, Code: "overloaded"}} {
				name := fmt.Sprintf("profile_%d/%s/%T", profileID, event.Type, cause)
				t.Run(name, func(t *testing.T) {
					provider := &scriptedLLMProvider{events: []llm.StreamEvent{event}, err: cause}
					hooks := taskLLMStreamHooks{
						current: func() (taskLLMSelection, error) {
							return taskLLMSelection{profileID: profileID, provider: provider}, nil
						},
						exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
							t.Fatal("non-quota stream failure must not exhaust a profile")
							return db.TaskLLMTransition{}, nil
						},
					}
					events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
					if len(events) != 1 || events[0].Type != event.Type || events[0].Text != event.Text {
						t.Fatalf("committed event was lost or replayed: %+v", events)
					}
					if provider.calls != 1 {
						t.Fatalf("committed stream was called %d times", provider.calls)
					}
					if !isTaskLLMRuntimeError(err) || !errors.Is(err, cause) {
						t.Fatalf("stream error lost replay classification or original cause: %T %v", err, err)
					}
					if retryableWorkerModelError(harness.ReasonModelError, err) {
						t.Fatal("outer worker would replay an intent after committed output")
					}
				})
			}
		}
	}
}
