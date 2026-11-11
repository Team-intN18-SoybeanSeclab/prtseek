package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func ctfRegressionServer(t *testing.T) (*Server, *db.Conversation) {
	t.Helper()
	s, c := newPersonalBackgroundTestServer(t)
	if err := s.initAgentWorkflows(); err != nil {
		t.Fatal(err)
	}
	c.AgentKey = "command"
	if _, err := s.m.pg.Exec(`UPDATE conversations SET agent_key='command' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	return s, c
}
func uploadCTFFixture(t *testing.T, s *Server, id int64, name, content string) chatAttachment {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(content))
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/chat/upload?scope=session&id=%d", id), &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.chatUpload(w, r)
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	var result struct {
		Attachments []chatAttachment `json:"attachments"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Attachments) != 1 {
		t.Fatalf("attachments: %s %v", w.Body, err)
	}
	return result.Attachments[0]
}
func useCTFFixtureModel(t *testing.T, s *Server, c *db.Conversation, handler http.HandlerFunc) {
	t.Helper()
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	id, err := s.m.pg.SaveProfile(&db.LLMProfile{Name: t.Name(), Format: "openai", BaseURL: endpoint.URL + "/v1", APIKey: "fixture-only", Model: "fixture", Streaming: false})
	if err != nil {
		t.Fatal(err)
	}
	c.LLMProfileID = &id
	if err = s.m.pg.UpdateConversationProfile(c.ID, &id); err != nil {
		t.Fatal(err)
	}
}
func fixtureReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}})
}

func TestCTFUploadCanBeReadByAgentAndRetryUsesSameFile(t *testing.T) {
	s, c := ctfRegressionServer(t)
	attachment := uploadCTFFixture(t, s, c.ID, "题目.txt", "local-attachment-evidence-731")
	want := filepath.Join(s.conversationWorkDir(c.ID), "uploads", "题目.txt")
	if attachment.Abs != want {
		t.Fatalf("destination=%s", attachment.Abs)
	}
	resolved, err := s.resolveConversationAttachments(c.ID, []chatAttachment{attachment})
	if err != nil {
		t.Fatal(err)
	}
	manifest := composeAgentMessage("read it", resolved, s.conversationWorkDir(c.ID))
	if !strings.Contains(manifest, want) {
		t.Fatalf("wrong manifest: %s", manifest)
	}
	for range 2 {
		retry, err := s.resolveConversationAttachments(c.ID, resolved)
		if err != nil || retry[0].Abs != want {
			t.Fatalf("retry changed attachment: %v %v", retry, err)
		}
	}
	readResult := make(chan bool, 1)
	useCTFFixtureModel(t, s, c, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		for _, msg := range body.Messages {
			if msg.Role == "tool" {
				data, _ := json.Marshal(msg.Content)
				select {
				case readResult <- strings.Contains(string(data), "local-attachment-evidence-731"):
				default:
				}
				fixtureReply(w, "Attachment read and verified.")
				return
			}
		}
		args, _ := json.Marshal(map[string]any{"file_path": want})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "read-file", "type": "function", "function": map[string]any{"name": "Read", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
	})
	body, _ := json.Marshal(map[string]any{"message": "Read the attached local fixture.", "attachments": resolved})
	w := retestRequest(s.pgSendConversationMessage, http.MethodPost, c.ID, string(body))
	if w.Code != 202 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	select {
	case ok := <-readResult:
		if !ok {
			t.Fatal("agent did not read uploaded contents")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not execute Read")
	}
	waitCTFIdle(t, s, c.ID)
	second := uploadCTFFixture(t, s, c.ID, "题目.txt", "different-content")
	if second.Abs == attachment.Abs {
		t.Fatal("same-name upload overwrote original")
	}
	data, _ := os.ReadFile(attachment.Abs)
	if string(data) != "local-attachment-evidence-731" {
		t.Fatal("original changed")
	}
}

func waitCTFIdle(t *testing.T, s *Server, id int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.chatMu.Lock()
		busy := s.chatBusy[s.convBusyKey(id)]
		s.chatMu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("conversation failed to drain")
}

func TestCTFAttachmentValidationAndLegacyCopy(t *testing.T) {
	s, c := ctfRegressionServer(t)
	for _, path := range []string{"uploads/missing.bin", "../secret", "uploads/../secret", "uploads/C:secret", "uploads/nested/file"} {
		if _, err := s.resolveConversationAttachments(c.ID, []chatAttachment{{Path: path}}); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	dir := filepath.Join(s.m.dir, "sessions", fmt.Sprint(c.ID), "uploads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("preserve-me"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.resolveConversationAttachments(c.ID, []chatAttachment{{Path: "uploads/old.txt", Size: 999, Abs: "untrusted"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "old.txt"), resolved[0].Abs} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "preserve-me" {
			t.Fatal("legacy data not preserved", err)
		}
	}
	if resolved[0].Size != 11 {
		t.Fatal("client supplied metadata was trusted")
	}
}

func TestCTFAsyncReturnsEarlyAndWaitCollectsFirstResult(t *testing.T) {
	s, c := ctfRegressionServer(t)
	fast := make(chan struct{})
	slow := make(chan struct{})
	useCTFFixtureModel(t, s, c, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body["messages"])
		if strings.Contains(string(raw), "slow-fixture") {
			select {
			case <-slow:
			case <-r.Context().Done():
				return
			}
		} else {
			select {
			case <-fast:
			case <-r.Context().Done():
				return
			}
		}
		fixtureReply(w, "Blocked: no verified flag.")
	})
	ctx, run := s.beginCTFWorkflow(t.Context(), c)
	defer s.closeCTFWorkflow(run)
	toolCtx, cancelTool := context.WithCancel(ctx)
	result, err := s.parallelCTFDelegationTool(c, ctx).Call(toolCtx, json.RawMessage(`{"tasks":[{"agent_key":"solver","task":"fast-fixture local task"},{"agent_key":"solver","task":"slow-fixture local task"}]}`), nil)
	if err != nil || result.IsError {
		t.Fatal(err, result.Flatten())
	}
	cancelTool() // async children belong to the owner turn, not the tool invocation
	var started struct {
		Jobs []ctfJobView `json:"jobs"`
	}
	_ = json.Unmarshal([]byte(result.Flatten()), &started)
	if len(started.Jobs) != 2 {
		t.Fatal(result.Flatten())
	}
	close(fast)
	result, err = s.waitCTFAgents(ctx, c.ID, nil, 5*time.Second)
	if err != nil || result.IsError {
		t.Fatal(err, result.Flatten())
	}
	var waited struct {
		Jobs    []ctfJobView `json:"jobs"`
		Pending bool         `json:"pending"`
	}
	_ = json.Unmarshal([]byte(result.Flatten()), &waited)
	if !waited.Pending {
		t.Fatalf("did not return partial completion: %s", result.Flatten())
	}
	completed := 0
	for _, job := range waited.Jobs {
		if job.State == "completed" {
			completed++
			if job.Outcome.State == "verified" {
				t.Fatal("blocked answer marked verified")
			}
		}
	}
	if completed != 1 {
		t.Fatalf("expected exactly one result: %s", result.Flatten())
	}
	run.mu.Lock()
	slowJob := run.jobs[started.Jobs[1].ID]
	slowJob.stop(agent.AbortChatStoppedByUser)
	run.mu.Unlock()
	result, err = s.waitCTFAgents(ctx, c.ID, []int64{slowJob.id}, 5*time.Second)
	if err != nil || !strings.Contains(result.Flatten(), `"cancelled"`) {
		t.Fatal("child did not cancel", err, result.Flatten())
	}
	if run.completionCheck(c.ID) != "" {
		t.Fatal("collected children still block completion")
	}
}

func TestCTFOutcomeRequiresIntactEvidenceAndExistingArtifacts(t *testing.T) {
	s, c := ctfRegressionServer(t)
	if err := os.MkdirAll(s.conversationWorkDir(c.ID), 0700); err != nil {
		t.Fatal(err)
	}
	o := ctfOutcome{State: "verified", Summary: "claim", Criterion: "expected output", Verification: "ran local checker"}
	if err := s.validateCTFOutcome(c.ID, &o); err == nil {
		t.Fatal("unsubstantiated success accepted")
	}
	m, err := agent.NewResearchMemory(s.conversationWorkDir(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.ObserveTool("Bash", json.RawMessage(`{"command":"fixture-check"}`), "checker exit=0, output=fixture-pass", false)
	if err != nil {
		t.Fatal(err)
	}
	o.Evidence = []ctfEvidenceRef{{ID: id}}
	if err = s.validateCTFOutcome(c.ID, &o); err != nil {
		t.Fatal(err)
	}
	o.Artifacts = []string{"missing-exp.py"}
	if err = s.validateCTFOutcome(c.ID, &o); err == nil {
		t.Fatal("nonexistent artifact accepted")
	}
	o.Artifacts = nil
	state, _ := m.Snapshot()
	if err = os.WriteFile(filepath.Join(m.EvidenceRoot(), state.Observations[0].Path), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.validateCTFOutcome(c.ID, &o); err == nil {
		t.Fatal("tampered evidence accepted")
	}
}

func TestCTFRestartMarksInterruptedAndPreservesReports(t *testing.T) {
	s, c := ctfRegressionServer(t)
	if err := s.startCTFExecution(c.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.Exec(`UPDATE conversation_executions SET model_calls=3 WHERE conversation_id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.m.pg.Exec(`INSERT INTO conversation_outcomes(conversation_id,report_json) VALUES($1,$2)`, c.ID, `{"state":"partial","summary":"saved evidence"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.initCTFExecution(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.m.pg.QueryRow(`SELECT state FROM conversation_executions WHERE conversation_id=$1`, c.ID).Scan(&state); err != nil || state != "interrupted" {
		t.Fatal(state, err)
	}
	if s.readCTFOutcome(c.ID).State != "partial" {
		t.Fatal("restart lost checkpoint")
	}
	var calls int64
	if err = s.m.pg.QueryRow(`SELECT model_calls FROM conversation_executions WHERE conversation_id=$1`, c.ID).Scan(&calls); err != nil || calls != 3 {
		t.Fatal("restart lost model call count", calls, err)
	}
}

func TestCTFModelCallsPersistBeforeRequest(t *testing.T) {
	s, c := ctfRegressionServer(t)
	useCTFFixtureModel(t, s, c, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		var calls int64
		if err := s.m.pg.QueryRow(`SELECT model_calls FROM conversation_executions WHERE conversation_id=$1`, c.ID).Scan(&calls); err != nil || calls < 1 {
			t.Error("in-flight request not persisted", calls, err)
		}
		fixtureReply(w, "Fixture response without delegation.")
	})
	w := retestRequest(s.pgSendConversationMessage, http.MethodPost, c.ID, `{"message":"Respond with the supplied local fixture."}`)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	waitCTFIdle(t, s, c.ID)
	var calls int64
	if err := s.m.pg.QueryRow(`SELECT model_calls FROM conversation_executions WHERE conversation_id=$1`, c.ID).Scan(&calls); err != nil || calls < 1 {
		t.Fatal("completed request count missing", calls, err)
	}
}

func TestCTFOverviewCountsTreeTokensOnceAndScopesFiles(t *testing.T) {
	s, c := ctfRegressionServer(t)
	child, err := s.m.pg.CreateConversation("solver", "child", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.m.pg.Exec(`INSERT INTO conversation_delegations(parent_id,child_id,state) VALUES($1,$2,'completed')`, c.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id            int64
		kind          string
		input, output int
	}{{c.ID, "usage", 100, 10}, {c.ID, "result", 100, 20}, {c.ID, "usage", 30, 5}, {child.ID, "usage", 50, 5}, {child.ID, "result", 50, 10}} {
		i, o := item.input, item.output
		_, err = s.m.pg.AppendConvActivity(item.id, db.Activity{Kind: item.kind, Summary: "fixture", InputTokens: &i, OutputTokens: &o})
		if err != nil {
			t.Fatal(err)
		}
	}
	w := retestRequest(s.conversationOverview, http.MethodGet, c.ID, "")
	var view ctfOverview
	if err = json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body, err)
	}
	if view.InputTokens != 180 || view.OutputTokens != 35 {
		t.Fatalf("tokens counted twice or lost: %+v", view)
	}
	a := uploadCTFFixture(t, s, child.ID, "exp.py", "print('local fixture')")
	w = retestRequest(s.conversationFiles, http.MethodGet, c.ID, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "exp.py") {
		t.Fatal(w.Code, w.Body)
	}
	for _, query := range []string{fmt.Sprintf("conversation_id=%d&path=uploads/exp.py", child.ID), "path=../secret", fmt.Sprintf("conversation_id=%d&path=uploads/exp.py", child.ID+999)} {
		r := httptest.NewRequest(http.MethodGet, "/file?"+query, nil)
		r.SetPathValue("id", fmt.Sprint(c.ID))
		rec := httptest.NewRecorder()
		s.conversationFile(rec, r)
		if strings.HasPrefix(query, fmt.Sprintf("conversation_id=%d&", child.ID)) {
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "local fixture") {
				t.Fatal(rec.Code, rec.Body)
			}
		} else if rec.Code != 400 {
			t.Fatal("out-of-tree preview accepted", rec.Code)
		}
	}
	if _, err = os.Stat(a.Abs); err != nil {
		t.Fatal("preview modified file", err)
	}
}

func TestCTFFilesPrioritizeTreeArtifactsOverEvidence(t *testing.T) {
	s, c := ctfRegressionServer(t)
	child, err := s.m.pg.CreateConversation("solver", "child", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.m.pg.Exec(`INSERT INTO conversation_delegations(parent_id,child_id,state) VALUES($1,$2,'completed')`, c.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	evidenceDir := filepath.Join(s.conversationWorkDir(c.ID), ".research", "evidence")
	if err = os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := range 310 {
		if err = os.WriteFile(filepath.Join(evidenceDir, fmt.Sprintf("t%04d.txt", i)), []byte("fixture evidence"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	uploadCTFFixture(t, s, child.ID, "challenge.txt", "fixture input")
	if err = os.WriteFile(filepath.Join(s.conversationWorkDir(child.ID), "answer.txt"), []byte("fixture answer"), 0600); err != nil {
		t.Fatal(err)
	}
	w := retestRequest(s.conversationFiles, http.MethodGet, c.ID, "")
	var response struct {
		Items     []ctfFile `json:"items"`
		Truncated bool      `json:"truncated"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body, err)
	}
	if !response.Truncated || len(response.Items) != 300 {
		t.Fatalf("file cap not enforced: truncated=%v files=%d", response.Truncated, len(response.Items))
	}
	input, artifact := false, false
	for _, file := range response.Items {
		if file.ConversationID != child.ID {
			continue
		}
		input = input || file.Path == "uploads/challenge.txt" && file.Kind == "input"
		artifact = artifact || file.Path == "answer.txt" && file.Kind == "artifact"
	}
	if !input || !artifact {
		t.Fatalf("parent evidence hid child files: input=%v artifact=%v", input, artifact)
	}
}

func TestCTFDispatchLimitsApplyAcrossNestedRoles(t *testing.T) {
	s, c := ctfRegressionServer(t)
	useCTFFixtureModel(t, s, c, func(w http.ResponseWriter, r *http.Request) {
		t.Error("limit check reached model")
		fixtureReply(w, "unexpected")
	})
	ctx, run := s.beginCTFWorkflow(t.Context(), c)
	defer s.closeCTFWorkflow(run)
	deep := context.WithValue(ctx, ctfDepthKey{}, ctfMaxDepth)
	if _, err := s.startCTFChild(deep, c, "solver", "bounded task"); err == nil {
		t.Fatal("depth cap missing")
	}
	run.active = ctfMaxActive
	if _, err := s.startCTFChild(ctx, c, "solver", "bounded task"); err == nil {
		t.Fatal("active cap missing")
	}
	run.active = 0
	run.total = ctfMaxJobs
	if _, err := s.startCTFChild(ctx, c, "solver", "bounded task"); err == nil {
		t.Fatal("total cap missing")
	}
	if len(run.jobs) != 0 {
		t.Fatal("limit created jobs")
	}
}

func TestCTFWorkflowCloseCancelsAndDrainsAllChildren(t *testing.T) {
	s, c := ctfRegressionServer(t)
	started := make(chan struct{}, 2)
	useCTFFixtureModel(t, s, c, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			t.Error("cancelled request remained connected")
		}
	})
	ctx, run := s.beginCTFWorkflow(t.Context(), c)
	defer s.closeCTFWorkflow(run)
	for _, task := range []string{"first isolated task", "second isolated task"} {
		if _, err := s.startCTFChild(ctx, c, "solver", task); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("children did not start")
		}
	}
	s.closeCTFWorkflow(run)
	for _, job := range run.jobs {
		select {
		case <-job.done:
		default:
			t.Fatal("child remained alive")
		}
		if job.state != "cancelled" {
			t.Fatal("cancel state", job.state)
		}
	}
	s.chatMu.Lock()
	busy := len(s.chatBusy)
	s.chatMu.Unlock()
	if busy != 0 {
		t.Fatal("child busy slots were not released")
	}
}

func TestCTFProfileAdvancedSettingsAreHonored(t *testing.T) {
	s, c := ctfRegressionServer(t)
	id, err := s.m.pg.SaveProfile(&db.LLMProfile{Name: t.Name(), Format: "openai", Model: "fixture", BaseURL: "http://127.0.0.1:1/v1", APIKey: "fixture-only", Proxy: "http://127.0.0.1:2", SessionHeaderKey: "x-local-session", Streaming: false, ReasoningEffort: "high", MaxTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	c.LLMProfileID = &id
	cfg, _, ok := s.personalProfileConfig(c)
	if !ok || cfg.Proxy != "http://127.0.0.1:2" || cfg.SessionHeaderKey != "x-local-session" || cfg.Stream || cfg.MaxTokens != 4096 || cfg.ReasoningEffort != "high" {
		t.Fatal("advanced configuration lost")
	}
	snapshot := makePersonalRunSnapshot(cfg, id, "", "zh")
	data, _ := json.Marshal(snapshot)
	if strings.Contains(string(data), cfg.APIKey) || strings.Contains(string(data), cfg.Proxy) {
		t.Fatal("snapshot copied a credential-bearing value")
	}
	if snapshot.ConnectionBinding == "" || snapshot.Version != 4 {
		t.Fatal("connection binding missing")
	}
}
