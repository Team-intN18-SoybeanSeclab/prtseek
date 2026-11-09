package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestWorkflowEntries(t *testing.T) {
	for _, v := range []struct{ mode, key, want string }{{"standard", "", "mainagent"}, {"standard", "planner", "planner"}, {"ctf", "planner", "ctf_triage"}, {"ctf", "", "ctf_triage"}, {"custom", "my_agent", "my_agent"}} {
		got, err := workflowEntry(v.mode, v.key)
		if err != nil || got != v.want {
			t.Fatalf("%+v: %s %v", v, got, err)
		}
	}
	for _, v := range [][2]string{{"standard", "worker"}, {"ctf", "mainagent"}, {"custom", "mainagent"}, {"custom", ""}, {"unknown", "planner"}} {
		if _, err := workflowEntry(v[0], v[1]); err == nil {
			t.Fatalf("accepted %v", v)
		}
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
	if !toolsFor("ctf_triage")["delegate_agent"] || !toolsFor("ctf_planner")["delegate_agents"] {
		t.Fatal("CTF delegation tools missing")
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
	parent.AgentKey = "ctf_planner"
	parent.LLMProfileID = &id
	_, err = s.delegateConversation(context.Background(), parent, "ctf_worker", "Read the supplied local fixture and report evidence; do not use external targets.")
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
	data, _ := json.Marshal(wire["tools"])
	if bytes.Contains(data, []byte(`"delegate_agent"`)) {
		t.Fatal("worker can recursively delegate")
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
	root, err := m.pg.CreateConversation("ctf_triage", "root", nil)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := m.pg.CreateConversation("ctf_planner", "plan", nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := m.pg.CreateConversation("ctf_worker", "review", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]int64{{root.ID, planner.ID}, {planner.ID, worker.ID}} {
		if _, err := m.pg.Exec(`INSERT INTO conversation_delegations(parent_id,child_id,state) VALUES($1,$2,'completed')`, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("id", fmt.Sprint(root.ID))
	rec := httptest.NewRecorder()
	s.conversationDelegations(rec, req)
	var body struct {
		Items []struct {
			ID       int64 `json:"id"`
			ParentID int64 `json:"parent_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Items) != 2 || body.Items[1].ID != worker.ID || body.Items[1].ParentID != planner.ID {
		t.Fatalf("unexpected tree: %s", rec.Body.String())
	}
}
