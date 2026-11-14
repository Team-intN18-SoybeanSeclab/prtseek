package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestCTFTurnLedgerAcrossContinueAndRestart(t *testing.T) {
	s, c := ctfRegressionServer(t)
	useCTFFixtureModel(t, s, c, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "fixture complete"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "prompt_tokens_details": map[string]any{"cached_tokens": 80}}})
	})
	for i := 0; i < 2; i++ {
		_, err := s.m.pg.AppendConvActivity(c.ID, db.Activity{Kind: "user", Detail: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancelCause(t.Context())
		s.runConversationTurn(ctx, cancel, c, "fixture", s.convBusyKey(c.ID))
	}
	var count, calls, input, output, cache int
	if err := s.m.pg.QueryRow(`SELECT count(*),sum(model_calls),sum(input_tokens),sum(output_tokens),sum(cache_read_tokens) FROM conversation_turns WHERE conversation_id=$1 AND state='completed' AND reason='completed'`, c.ID).Scan(&count, &calls, &input, &output, &cache); err != nil {
		t.Fatal(err)
	}
	if count != 2 || calls != 2 || input != 200 || output != 20 || cache != 160 {
		t.Fatalf("%d %d %d %d %d", count, calls, input, output, cache)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetPathValue("id", strconv.FormatInt(c.ID, 10))
	w := httptest.NewRecorder()
	s.conversationOverview(w, r)
	var view ctfOverview
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 || view.InputTokens != 200 || view.OutputTokens != 20 || view.CacheReadTokens != 160 || view.TurnID == 0 {
		t.Fatalf("overview: %d %s %v", w.Code, w.Body, err)
	}
	_, err := s.m.pg.StartConversationTurn(c.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.initCTFExecution(); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err = s.m.pg.QueryRow(`SELECT reason FROM conversation_turns ORDER BY id DESC LIMIT 1`).Scan(&reason); err != nil || reason != "service_restart" {
		t.Fatalf("restart: %s %v", reason, err)
	}
}

func TestCTFVerificationLevelsDoNotUpgradeRepeatedRead(t *testing.T) {
	s, c := ctfRegressionServer(t)
	dir := s.conversationWorkDir(c.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := agent.NewResearchMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := m.ObserveTool("Bash", json.RawMessage(`{}`), "unique artifact absent", false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.ObserveTool("Bash", json.RawMessage(`{}`), "created unique artifact and verified result", false)
	if err != nil {
		t.Fatal(err)
	}
	o := ctfOutcome{State: "verified", Summary: "fixture", Criterion: "fixture", Verification: "local fixture", Evidence: []ctfEvidenceRef{{ID: baseline}, {ID: result}}}
	if err = s.validateCTFOutcome(c.ID, &o); err != nil || o.VerificationLevel != "observed" {
		t.Fatalf("legacy level: %+v %v", o, err)
	}
	o.VerificationLevel = "reproduced"
	o.BaselineEvidence = &ctfEvidenceRef{ID: result}
	o.ResultEvidence = &ctfEvidenceRef{ID: result}
	if err = s.validateCTFOutcome(c.ID, &o); err == nil {
		t.Fatal("same observation promoted to independent replay")
	}
	o.BaselineEvidence = &ctfEvidenceRef{ID: baseline}
	o.ExperimentID = "exp-fixture"
	if err = s.validateCTFOutcome(c.ID, &o); err == nil {
		t.Fatal("nonexistent experiment accepted")
	}
	if err = os.MkdirAll(filepath.Join(dir, ".research", "experiments", o.ExperimentID), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Record(t.Context(), agent.ResearchRecordInput{Entry: &agent.ResearchEntryInput{Kind: "hypothesis", Scope: o.ExperimentID, Summary: "fixture experiment"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.validateCTFOutcome(c.ID, &o); err != nil {
		t.Fatal(err)
	}
	o.BaselineEvidence, o.ResultEvidence = o.ResultEvidence, o.BaselineEvidence
	if err = s.validateCTFOutcome(c.ID, &o); err == nil {
		t.Fatal("reversed baseline accepted")
	}
	o.VerificationLevel = "accepted"
	o.AcceptanceEvidence = nil
	if err = s.validateCTFOutcome(c.ID, &o); err == nil {
		t.Fatal("accepted without receipt evidence")
	}
	o.AcceptanceEvidence = &ctfEvidenceRef{ID: result}
	if err = s.validateCTFOutcome(c.ID, &o); err != nil {
		t.Fatal(err)
	}
}

func TestCTFTruncatedSSEPersistsInterruptedUsage(t *testing.T) {
	s, c := ctfRegressionServer(t)
	useCTFFixtureModel(t, s, c, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial fixture response\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":4}}\n\n")
	})
	if _, err := s.m.pg.Exec(`UPDATE llm_profiles SET streaming=true WHERE id=$1`, *c.LLMProfileID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	s.runConversationTurn(ctx, cancel, c, "fixture partial stream", s.convBusyKey(c.ID))
	var state, reason string
	var input, output, calls int
	if err := s.m.pg.QueryRow(`SELECT state,reason,input_tokens,output_tokens,model_calls FROM conversation_turns WHERE conversation_id=$1`, c.ID).Scan(&state, &reason, &input, &output, &calls); err != nil {
		t.Fatal(err)
	}
	if state != "interrupted" || reason != "incomplete_stream" || input != 40 || output != 4 || calls != 1 {
		t.Fatalf("%s %s %d %d %d", state, reason, input, output, calls)
	}
	var errors int
	if err := s.m.pg.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1 AND kind='result' AND is_error=true`, c.ID).Scan(&errors); err != nil || errors != 1 {
		t.Fatalf("missing failure result: %d %v", errors, err)
	}
}

func TestCTFHTTPObservationAndExperimentThroughChat(t *testing.T) {
	s, c := ctfRegressionServer(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/answer", 302)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<nav>irrelevant fixture nav</nav><article>local fixture content</article>")
	}))
	defer page.Close()
	calls := 0
	var outputs []string
	useCTFFixtureModel(t, s, c, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		outputs = nil
		for _, m := range req.Messages {
			if m.Role == "tool" {
				var v string
				_ = json.Unmarshal(m.Content, &v)
				outputs = append(outputs, v)
			}
		}
		calls++
		if calls == 3 {
			fixtureReply(w, "observed local fixture; independent reproduction not claimed")
			return
		}
		name, args := "WebFetch", map[string]any{"url": page.URL + "/start"}
		if calls == 2 {
			name = "research_experiment"
			args = map[string]any{"hypothesis": "fixture hypothesis", "changed_variable": "fixture input", "prediction": "unique artifact"}
		}
		raw, _ := json.Marshal(args)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": fmt.Sprint("call", calls), "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}, "finish_reason": "tool_calls"}}})
	})
	ctx, cancel := context.WithCancelCause(t.Context())
	s.runConversationTurn(ctx, cancel, c, "Inspect local HTTP fixture then allocate an experiment.", s.convBusyKey(c.ID))
	if calls != 3 || len(outputs) != 2 {
		t.Fatalf("calls=%d outputs=%v", calls, outputs)
	}
	if !strings.Contains(outputs[0], `"final_url":"`+page.URL+`/answer"`) || !strings.Contains(outputs[0], `"status":302`) || strings.Contains(outputs[0], "irrelevant fixture nav") {
		t.Fatal(outputs[0])
	}
	if !strings.Contains(outputs[0], "evidence_id=t") || !strings.Contains(outputs[1], "artifact_prefix") {
		t.Fatal(outputs)
	}
	var observation struct {
		BodyFile string `json:"body_file"`
	}
	if err := json.NewDecoder(strings.NewReader(outputs[0])).Decode(&observation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.conversationWorkDir(c.ID), filepath.FromSlash(observation.BodyFile))); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetPathValue("id", strconv.FormatInt(c.ID, 10))
	w := httptest.NewRecorder()
	s.conversationFiles(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), observation.BodyFile) {
		t.Fatalf("HTTP evidence missing from file API: %s", w.Body)
	}
}
