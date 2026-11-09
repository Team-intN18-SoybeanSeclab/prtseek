package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func newPersonalBackgroundTestServer(t *testing.T) (*Server, *db.Conversation) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("independent test SQLite not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{pg: pg, dir: t.TempDir(), tasks: map[string]*Task{}}
	s := &Server{ctx: ctx, m: m, engine: NewEngine(m), chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}}
	c, err := pg.CreateConversation("auto", "background test", nil)
	if err != nil {
		pg.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		for _, task := range m.List() {
			s.engine.StopTask(task.ID)
			id, _ := strconv.ParseInt(task.ID, 10, 64)
			if err := pg.DeleteTask(id); err != nil {
				t.Error(err)
			}
		}
		if err := pg.DeleteConversation(c.ID); err != nil {
			t.Error(err)
		}
		pg.Close()
	})
	return s, c
}

func personalTestRun(t *testing.T, s *Server, c *db.Conversation, key string) (*Task, *db.PersonalBackgroundRun) {
	t.Helper()
	task, err := s.m.CreateTaskWithOptions("background", "continue work", db.TaskCreateOptions{WorkerCount: 1, TimeoutSeconds: 3600, PersonalHandoff: &db.PersonalHandoffInput{ConversationID: c.ID, IdempotencyKey: key, Checkpoint: "existing evidence", ConfigSnapshot: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.m.pg.PersonalBackgroundRun(c.ID, key)
	if err != nil || run == nil {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	return task, run
}

func TestPersonalBackgroundBusyAndIdempotencyHTTP(t *testing.T) {
	s, c := newPersonalBackgroundTestServer(t)
	s.chatBusy[s.convBusyKey(c.ID)] = true
	w := retestRequest(s.personalCreateBackground, http.MethodPost, c.ID, `{"idempotency_key":"busy","goal":"inspect local fixture"}`)
	if w.Code != 409 {
		t.Fatalf("busy conversion: %d %s", w.Code, w.Body)
	}
	if run, err := s.m.pg.PersonalBackgroundRun(c.ID, ""); err != nil || run != nil {
		t.Fatalf("busy request created work: %+v %v", run, err)
	}
	delete(s.chatBusy, s.convBusyKey(c.ID))
	task, run := personalTestRun(t, s, c, "same-key")
	// A retry uses the recorded run, even if the foreground lane is now busy.
	s.chatBusy[s.convBusyKey(c.ID)] = true
	w = retestRequest(s.personalCreateBackground, http.MethodPost, c.ID, `{"idempotency_key":"same-key","goal":"should not replace the existing goal"}`)
	if w.Code != 200 {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	var body struct {
		Run personalBackgroundDTO `json:"run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Run.ID != run.ID || body.Run.TaskID != task.ID || body.Run.Goal != "continue work" {
		t.Fatalf("retry changed task: %+v", body.Run)
	}
	if body.Run.Status != "running" {
		t.Fatalf("goal decomposition leaked internal created state: %s", body.Run.Status)
	}
	delete(s.chatBusy, s.convBusyKey(c.ID))
	w = retestRequest(s.personalCreateBackground, http.MethodPost, c.ID, `{"idempotency_key":"different-key","goal":"second operation"}`)
	if w.Code != 409 {
		t.Fatalf("second active run: %d %s", w.Code, w.Body)
	}
	if len(s.m.List()) != 1 {
		t.Fatal("HTTP retry created a second task")
	}
}

func TestPersonalCheckpointPreservesVisibleHistory(t *testing.T) {
	s, c := newPersonalBackgroundTestServer(t)
	appendActivity := func(a db.Activity) int64 {
		t.Helper()
		id, err := s.m.pg.AppendConvActivity(c.ID, a)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	appendActivity(db.Activity{Kind: "user", Summary: "只检查本地文件", Detail: `{"text":"只检查本地文件","attachments":[{"name":"scope.txt"}]}`})
	for i := 0; i < 40; i++ {
		appendActivity(db.Activity{Kind: "text", Summary: strings.Repeat("已核对文件。", 200)})
	}
	evidence := strings.Repeat("raw-evidence-", 3000)
	cursor := appendActivity(db.Activity{Kind: "tool_result", Tool: "Read", Summary: "证据已保存", Detail: evidence})
	last := appendActivity(db.Activity{Kind: "thinking", Summary: "private-thought-marker", Detail: "private-thought-detail"})
	checkpoint, gotCursor, err := s.personalCheckpoint(c, "request-1", "finish checking")
	if err != nil {
		t.Fatal(err)
	}
	if gotCursor != last || gotCursor < cursor {
		t.Fatalf("cursor=%d expected=%d", gotCursor, last)
	}
	if len(checkpoint) > 50000 {
		t.Fatalf("unbounded prompt preview: %d bytes", len(checkpoint))
	}
	files, err := filepath.Glob(filepath.Join(s.m.dir, "personal-checkpoints", "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("checkpoint files=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), evidence) || strings.Contains(string(data), "private-thought") {
		t.Fatal("checkpoint lost full evidence or copied hidden reasoning")
	}
	if !strings.Contains(checkpoint, files[0]) || !strings.Contains(checkpoint, filepath.Join("sessions", s.convBusyKey(c.ID))) {
		t.Fatalf("source references absent: %s", checkpoint)
	}
	scan := bufio.NewScanner(strings.NewReader(string(data)))
	scan.Buffer(make([]byte, 4096), 1024*1024)
	count := 0
	for scan.Scan() {
		if !json.Valid(scan.Bytes()) {
			t.Fatal("invalid checkpoint JSONL")
		}
		count++
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 42 {
		t.Fatalf("visible records=%d expected=42", count)
	}
	// A failed handoff can be retried with the same key on Windows.
	if _, _, err := s.personalCheckpoint(c, "request-1", "finish checking"); err != nil {
		t.Fatalf("checkpoint retry failed: %v", err)
	}
}

func TestPersonalBackgroundPauseDrainAndStop(t *testing.T) {
	s, c := newPersonalBackgroundTestServer(t)
	task, run := personalTestRun(t, s, c, "control")
	if !s.engine.beginTaskOperation(task.ID) {
		t.Fatal("cannot register test operation")
	}
	w := retestRequest(s.personalControlBackground, http.MethodPost, c.ID, `{"action":"pause"}`)
	if w.Code != 200 {
		t.Fatalf("pause: %d %s", w.Code, w.Body)
	}
	var out struct {
		Run personalBackgroundDTO `json:"run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Run.Paused || out.Run.Status != "pausing" {
		t.Fatalf("tool drain reported as finished pause: %+v", out.Run)
	}
	w = retestRequest(s.personalControlBackground, http.MethodPost, c.ID, `{"action":"resume"}`)
	if w.Code != 409 {
		t.Fatalf("resume while draining: %d %s", w.Code, w.Body)
	}
	if s.chatBusy[s.convBusyKey(c.ID)] || s.chatBusy[task.ID] {
		t.Fatal("rejected resume leaked message locks")
	}
	w = retestRequest(s.personalControlBackground, http.MethodPost, c.ID, `{"action":"stop"}`)
	if w.Code != 200 {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	run, _ = s.m.pg.PersonalBackgroundRun(c.ID, "")
	if run.Active || run.State != "stopped" {
		t.Fatalf("stop state=%+v", run)
	}
	if !s.runPersonalBackgroundMessage(s.ctx, c, "start another operation") {
		t.Fatal("terminal but draining task released foreground execution")
	}
	w = retestRequest(s.personalCreateBackground, http.MethodPost, c.ID, `{"idempotency_key":"after-stop","goal":"next work"}`)
	if w.Code != 409 {
		t.Fatalf("new conversion while draining: %d %s", w.Code, w.Body)
	}
	s.engine.decInflight(task.ID)
	if s.runPersonalBackgroundMessage(s.ctx, c, "start another operation") {
		t.Fatal("fully stopped task did not release foreground execution")
	}
	w = retestRequest(s.personalControlBackground, http.MethodPost, c.ID, `{"action":"resume"}`)
	if w.Code != 409 {
		t.Fatalf("stopped task resumed: %d %s", w.Code, w.Body)
	}
	// Stop remains idempotent while completed runs retain their success state.
	w = retestRequest(s.personalControlBackground, http.MethodPost, c.ID, `{"action":"stop"}`)
	if w.Code != 200 {
		t.Fatalf("repeated stop: %d %s", w.Code, w.Body)
	}
}

func TestPersonalBackgroundActivityCursorAndTerminalStatus(t *testing.T) {
	s, c := newPersonalBackgroundTestServer(t)
	task, run := personalTestRun(t, s, c, "activity")
	workerSeq, err := task.Store.AppendActivity(db.Activity{Worker: "planner", Kind: "text", Summary: "plan ready"})
	if err != nil {
		t.Fatal(err)
	}
	mainSeq, err := task.Store.AppendActivity(db.Activity{Worker: "mainagent", Kind: "text", Summary: "already in conversation"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.personalBackgroundResponse(run, 0)
	if err != nil {
		t.Fatal(err)
	}
	items := body["activities"].([]ActivityDTO)
	if len(items) != 1 || body["cursor"].(int64) != mainSeq || workerSeq >= mainSeq {
		t.Fatalf("dedup/cursor failed: %v", body)
	}
	body, err = s.personalBackgroundResponse(run, mainSeq)
	if err != nil {
		t.Fatal(err)
	}
	if len(body["activities"].([]ActivityDTO)) != 0 {
		t.Fatal("cursor replayed activity")
	}
	if err := s.m.ApplyTaskPauseOrigin(task.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.SetTaskStatusGuarded(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	body, err = s.personalBackgroundResponse(run, 0)
	if err != nil {
		t.Fatal(err)
	}
	if body["run"].(*personalBackgroundDTO).Status != "done" {
		t.Fatalf("paused flag hid terminal result: %v", body)
	}
	w := retestRequest(s.personalControlBackground, http.MethodPost, c.ID, `{"action":"stop"}`)
	if w.Code != 409 {
		t.Fatalf("stop overwrote successful result: %d %s", w.Code, w.Body)
	}
}

func TestPersonalBackgroundProfileChangeIsPausedAndAtomic(t *testing.T) {
	s, c := newPersonalBackgroundTestServer(t)
	makeProfile := func(name, model string) int64 {
		t.Helper()
		id, err := s.m.pg.SaveProfile(&db.LLMProfile{Name: name, Format: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "fixture-not-a-real-key", Model: model})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
		return id
	}
	one, two := makeProfile("background-profile-one", "fixture-one"), makeProfile("background-profile-two", "fixture-two")
	if err := s.updatePersonalProfile(c, &one, "", "zh", nil); err != nil {
		t.Fatal(err)
	}
	task, run := personalTestRun(t, s, c, "switch")
	if _, err := s.preparePersonalProfileChange(c.ID); err == nil {
		t.Fatal("model switch allowed during active execution")
	}
	if err := s.m.ApplyTaskPauseOrigin(task.ID, false); err != nil {
		t.Fatal(err)
	}
	s.engine.BeginLLMCall(task.ID)
	if _, err := s.preparePersonalProfileChange(c.ID); err == nil {
		t.Fatal("model switch allowed while cancelled model call is draining")
	}
	s.engine.EndLLMCall(task.ID)
	allowed, err := s.preparePersonalProfileChange(c.ID)
	if err != nil || allowed == nil || allowed.ID != run.ID {
		t.Fatalf("paused switch rejected: %+v %v", allowed, err)
	}
	s.taskAgents = map[string]*taskAgentBundle{task.ID: {}}
	if err := s.updatePersonalProfile(c, &two, "", "en", allowed); err != nil {
		t.Fatal(err)
	}
	updated, err := s.m.pg.GetConversation(c.ID)
	if err != nil || updated.LLMProfileID == nil || *updated.LLMProfileID != two {
		t.Fatalf("conversation profile mismatch: %+v %v", updated, err)
	}
	settings, err := s.m.pg.PersonalConversationSettings(c.ID)
	if err != nil || settings.ResponseLanguage != "en" {
		t.Fatalf("settings mismatch: %+v %v", settings, err)
	}
	stored, err := s.m.pg.PersonalBackgroundRun(c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var snap personalRunSnapshot
	if err := json.Unmarshal(stored.ConfigSnapshot, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.ProfileID != two || snap.Model != "fixture-two" || snap.ResponseLanguage != "en" || s.taskAgents[task.ID] != nil {
		t.Fatalf("snapshot/runtime mismatch: %+v", snap)
	}
	before := string(stored.ConfigSnapshot)
	if err := s.m.SetTaskPaused(task.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.updatePersonalProfile(c, &one, "", "zh", allowed); err == nil {
		t.Fatal("stale paused handle changed active snapshot")
	}
	stored, _ = s.m.pg.PersonalBackgroundRun(c.ID, "")
	updated, _ = s.m.pg.GetConversation(c.ID)
	settings, _ = s.m.pg.PersonalConversationSettings(c.ID)
	if string(stored.ConfigSnapshot) != before || *updated.LLMProfileID != two || settings.ResponseLanguage != "en" {
		t.Fatal("failed update partially changed configuration")
	}
}
