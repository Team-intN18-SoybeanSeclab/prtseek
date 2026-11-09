package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestWorkflowEntryDefaultsToTriageAndKeepsSelectedAgent(t *testing.T) {
	for _, v := range [][3]string{{"", "", "triage"}, {"ctf", "command", "command"}, {"ctf", "solver", "solver"}} {
		got, err := workflowEntry(v[0], v[1])
		if err != nil || got != v[2] {
			t.Fatalf("%v: %s %v", v, got, err)
		}
	}
	if _, err := workflowEntry("ctf", "custom_solver"); err == nil {
		t.Fatal("custom agent should be rejected")
	}
}

func TestCTFDelegationToolsAreExposed(t *testing.T) {
	s := &Server{}
	toolsFor := func(key string) map[string]bool {
		out := map[string]bool{}
		for _, tool := range s.conversationTools(&db.Conversation{AgentKey: key})(context.Background(), key) {
			out[tool.Name()] = true
		}
		return out
	}
	if !toolsFor("triage")["delegate_agent"] || !toolsFor("command")["delegate_agents"] || !toolsFor("solver")["delegate_agent"] {
		t.Fatal("CTF delegation tools missing")
	}
	if len(toolsFor("custom_solver")) != 0 {
		t.Fatal("custom agents should not expose delegation tools")
	}
	if toolsFor("solver")["delegate_agents"] || toolsFor("writeup")["delegate_agents"] {
		t.Fatal("only command gets the parallel delegation tool")
	}
}

func TestDelegatedConversationPersistsResult(t *testing.T) {
	s, parent := newPersonalBackgroundTestServer(t)
	if err := s.initAgentWorkflows(); err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Verified fixture result"},"finish_reason":"stop"}]}`))
	}))
	defer endpoint.Close()
	id, err := s.m.pg.SaveProfile(&db.LLMProfile{Name: "delegation fixture", Format: "openai", BaseURL: endpoint.URL + "/v1", APIKey: "test-only", Model: "fixture", Streaming: false})
	if err != nil {
		t.Fatal(err)
	}
	parent.AgentKey = "command"
	parent.LLMProfileID = &id
	_, err = s.delegateConversation(context.Background(), parent, "writeup", "Write a fixture note from the verified local evidence; do not use external targets.")
	if err != nil {
		t.Fatal(err)
	}
	var childID int64
	var state string
	if err = s.m.pg.QueryRow(`SELECT child_id,state FROM conversation_delegations WHERE parent_id=$1`, parent.ID).Scan(&childID, &state); err != nil {
		t.Fatal(err)
	}
	defer s.m.pg.DeleteConversation(childID)
	if state != "completed" {
		t.Fatalf("delegation state %s", state)
	}
	child, err := s.m.pg.GetConversation(childID)
	if err != nil || child.LLMProfileID == nil || *child.LLMProfileID != id {
		t.Fatalf("child profile mismatch: %v %v", child, err)
	}
	if wire["model"] != "fixture" {
		t.Fatalf("model: %v", wire["model"])
	}
	messages, _ := json.Marshal(wire["messages"])
	wantWorkspace := filepath.Join(s.m.dir, "sessions", s.convBusyKey(parent.ID))
	encodedWorkspace, _ := json.Marshal(wantWorkspace)
	if !bytes.Contains(messages, encodedWorkspace[1:len(encodedWorkspace)-1]) {
		t.Fatalf("child did not receive parent evidence path %q", wantWorkspace)
	}
	data, _ := json.Marshal(wire["tools"])
	if !bytes.Contains(data, []byte(`"delegate_agent"`)) {
		t.Fatal("default writeup agent should retain delegation")
	}
	if !bytes.Contains(data, []byte(`"research_recall"`)) {
		t.Fatal("non-DeepSeek CTF child lost research tools")
	}
	for _, unwanted := range []string{"Sleep", "TodoWrite", "TaskOutput", "TaskStop", "TaskList", "Monitor", "research_guide"} {
		quoted, _ := json.Marshal(unwanted)
		if bytes.Contains(data, quoted) {
			t.Fatalf("CTF child exposed unrelated SDK tool %s", unwanted)
		}
	}
}
func TestConversationDelegationsIncludesNestedWorkers(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	root, err := m.pg.CreateConversation("triage", "root", nil)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := m.pg.CreateConversation("command", "plan", nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := m.pg.CreateConversation("solver", "review", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]int64{{root.ID, planner.ID}, {planner.ID, worker.ID}} {
		if _, err := m.pg.Exec(`INSERT INTO conversation_delegations(parent_id,child_id,state) VALUES($1,$2,'completed')`, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.pg.AppendConvActivity(worker.ID, db.Activity{Worker: "solver", Kind: "tool_use", Tool: "Bash", Summary: "strings challenge.bin"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("id", fmt.Sprint(root.ID))
	rec := httptest.NewRecorder()
	s.conversationDelegations(rec, req)
	var body struct {
		Items []struct {
			ID         int64         `json:"id"`
			ParentID   int64         `json:"parent_id"`
			Activities []ActivityDTO `json:"activities"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Items) != 2 || body.Items[1].ID != worker.ID || body.Items[1].ParentID != planner.ID || len(body.Items[1].Activities) != 1 || body.Items[1].Activities[0].Tool != "Bash" {
		t.Fatalf("unexpected tree: %s", rec.Body.String())
	}
}
