package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func newConversationDeleteFixture(t *testing.T) (*Server, *db.Conversation) {
	t.Helper()
	pg, err := db.Open(filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	m := &Manager{pg: pg, dir: t.TempDir(), tasks: map[string]*Task{}}
	s := &Server{ctx: t.Context(), m: m, engine: NewEngine(m), chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}}
	if err := s.initAgentWorkflows(); err != nil {
		t.Fatal(err)
	}
	c, err := pg.CreateConversation("solve", "local deletion fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func conversationDeleteFixtureWrite(t *testing.T, root, relative string) string {
	t.Helper()
	name := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("synthetic deletion fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func conversationDeleteFixturePaths(id int64) []string {
	key := fmt.Sprintf("conv-%d", id)
	return []string{
		filepath.Join("sessions", key, ".research", "evidence", "deep", "observation.txt"),
		filepath.Join("sessions", fmt.Sprint(id), "uploads", "legacy.txt"),
		filepath.Join("transcripts", key+".jsonl"),
		filepath.Join("transcripts", key, "subagents", "agent-fixture.jsonl"),
		filepath.Join("noa", "chat-"+key, "tier2", "archive.txt"),
	}
}

func TestConversationDeleteFilesRemovesExactOwnedPaths(t *testing.T) {
	root := t.TempDir()
	removed := conversationDeleteFixturePaths(1)
	kept := append(conversationDeleteFixturePaths(10),
		filepath.Join("sessions", "conv-1-extra", "notes.txt"),
		filepath.Join("transcripts", "conv-1.jsonl.bak"),
		filepath.Join("noa", "chat-conv-10-extra", "state.json"),
		"unrelated.txt")
	for _, name := range append(append([]string{}, removed...), kept...) {
		conversationDeleteFixtureWrite(t, root, name)
	}
	if err := deleteConversationFiles(root, 1); err != nil {
		t.Fatal(err)
	}
	for _, name := range removed {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("owned file remained: %s (%v)", name, err)
		}
	}
	for _, name := range kept {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("unselected file changed: %s (%v)", name, err)
		}
	}
	for _, name := range []string{"sessions/conv-1", "sessions/1", "transcripts/conv-1", "noa/chat-conv-1"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("owned directory remained: %s (%v)", name, err)
		}
	}
	if err := deleteConversationFiles(root, 1); err != nil {
		t.Fatalf("missing files should be harmless: %v", err)
	}
}

func TestConversationDeleteFilesUnlinksSelectedSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	victim := conversationDeleteFixtureWrite(t, outside, "source.txt")
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "sessions", "conv-1")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := deleteConversationFiles(root, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("selected link remained: %v", err)
	}
	if body, err := os.ReadFile(victim); err != nil || string(body) != "synthetic deletion fixture" {
		t.Fatalf("cleanup traversed selected symlink: %v %q", err, body)
	}
}

func TestConversationDeleteFilesRejectsSymlinkParent(t *testing.T) {
	for _, parent := range []string{"sessions", "transcripts", "noa"} {
		t.Run(parent, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			victim := conversationDeleteFixtureWrite(t, outside, "conv-1/source.txt")
			if err := os.Symlink(outside, filepath.Join(root, parent)); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			if err := deleteConversationFiles(root, 1); err == nil {
				t.Fatal("symlink parent was accepted")
			}
			if _, err := os.Stat(victim); err != nil {
				t.Fatalf("cleanup escaped data root: %v", err)
			}
		})
	}
}

func TestConversationDeleteFilesFailurePreservesDatabaseRecord(t *testing.T) {
	s, c := newConversationDeleteFixture(t)
	file := conversationDeleteFixtureWrite(t, s.m.dir, filepath.Join("sessions", s.convBusyKey(c.ID), "archive.zip"))
	obstruction := conversationDeleteFixtureWrite(t, s.m.dir, "transcripts")
	deleteFromList := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/conversations/"+fmt.Sprint(c.ID), nil)
		req.SetPathValue("id", fmt.Sprint(c.ID))
		response := httptest.NewRecorder()
		s.pgDeleteConversation(response, req)
		return response
	}
	if response := deleteFromList(); response.Code != http.StatusInternalServerError {
		t.Fatalf("delete with invalid parent: %d %s", response.Code, response.Body.String())
	}
	if row, err := s.m.pg.GetConversation(c.ID); err != nil || row == nil {
		t.Fatalf("failed cleanup lost retryable conversation: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("failed preflight changed workspace: %v", err)
	}
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if response := deleteFromList(); response.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(s.conversationWorkDir(c.ID)); !os.IsNotExist(err) {
		t.Fatalf("successful retry left workspace: %v", err)
	}
	if row, err := s.m.pg.GetConversation(c.ID); err != nil || row != nil {
		t.Fatalf("successful retry left conversation: %v", err)
	}
}

func TestConversationDeleteFilesWaitsForBusyWriter(t *testing.T) {
	s, c := newConversationDeleteFixture(t)
	key := s.convBusyKey(c.ID)
	cancelled, releaseWriter := make(chan struct{}), make(chan struct{})
	var cancelOnce, releaseOnce sync.Once
	s.chatMu.Lock()
	s.chatBusy[key] = true
	s.chatCancel[key] = func(error) { cancelOnce.Do(func() { close(cancelled) }) }
	s.chatMu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	deleted, finished := make(chan error, 1), make(chan struct{})
	go func() { deleted <- s.deleteConversationWithFiles(ctx, c.ID); close(finished) }()
	writerResult := make(chan error, 1)
	writerFinished := make(chan struct{})
	latePath := filepath.Join(s.conversationWorkDir(c.ID), ".research", "late-write.txt")
	go func() {
		defer close(writerFinished)
		<-releaseWriter
		err := os.MkdirAll(filepath.Dir(latePath), 0700)
		if err == nil {
			err = os.WriteFile(latePath, []byte("last synthetic writer output"), 0600)
		}
		s.chatMu.Lock()
		delete(s.chatBusy, key)
		delete(s.chatCancel, key)
		s.chatMu.Unlock()
		writerResult <- err
	}()
	defer func() { releaseOnce.Do(func() { close(releaseWriter) }); cancel(); <-writerFinished; <-finished }()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("delete did not cancel busy conversation")
	}
	select {
	case err := <-deleted:
		t.Fatalf("delete returned before writer exited: %v", err)
	default:
	}
	if row, err := s.m.pg.GetConversation(c.ID); err != nil || row == nil {
		t.Fatalf("database row removed before writer finished: %v", err)
	}
	releaseOnce.Do(func() { close(releaseWriter) })
	if err := <-writerResult; err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(latePath); !os.IsNotExist(err) {
		t.Fatalf("late writer recreated deleted workspace: %v", err)
	}
	if row, err := s.m.pg.GetConversation(c.ID); err != nil || row != nil {
		t.Fatalf("conversation was not deleted: %v", err)
	}
}

func TestConversationDeleteFilesDrainsOperationsAndClosesAdmission(t *testing.T) {
	s, c := newConversationDeleteFixture(t)
	if err := s.beginConversationFileOperation(c.ID); err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { s.endConversationFileOperation(c.ID) }) }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	deleted, finished := make(chan error, 1), make(chan struct{})
	go func() { deleted <- s.deleteConversationWithFiles(ctx, c.ID); close(finished) }()
	defer func() { release(); cancel(); <-finished }()
	deadline, closed := time.Now().Add(2*time.Second), false
	for time.Now().Before(deadline) {
		if err := s.beginConversationFileOperation(c.ID); err != nil {
			closed = true
			break
		}
		s.endConversationFileOperation(c.ID)
		time.Sleep(5 * time.Millisecond)
	}
	if !closed {
		t.Fatal("delete did not close operation admission")
	}
	select {
	case err := <-deleted:
		t.Fatalf("delete did not wait for admitted upload: %v", err)
	default:
	}
	late := conversationDeleteFixtureWrite(t, s.m.dir, filepath.Join("sessions", keyForDeleteTest(c.ID), "uploads", "late-upload.txt"))
	release()
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(late); !os.IsNotExist(err) {
		t.Fatalf("admitted upload recreated deleted directory: %v", err)
	}
	if err := s.beginConversationFileOperation(c.ID); err == nil {
		s.endConversationFileOperation(c.ID)
		t.Fatal("deleted conversation admitted a new writer")
	}
}

func keyForDeleteTest(id int64) string { return fmt.Sprintf("conv-%d", id) }

func TestConversationDeleteFilesBatchPreservesUnselectedConversations(t *testing.T) {
	s, first := newConversationDeleteFixture(t)
	kept, err := s.m.pg.CreateConversation("solve", "keep synthetic fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.m.pg.CreateConversation("writeup", "delete synthetic fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*db.Conversation{first, kept, last} {
		for _, name := range conversationDeleteFixturePaths(c.ID) {
			conversationDeleteFixtureWrite(t, s.m.dir, name)
		}
	}
	body, _ := json.Marshal(map[string]any{"ids": []int64{first.ID, last.ID}})
	request := httptest.NewRequest(http.MethodPost, "/api/conversations/delete/batch", bytes.NewReader(body))
	response := httptest.NewRecorder()
	s.pgDeleteConversationsBatch(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, c := range []*db.Conversation{first, kept, last} {
		row, err := s.m.pg.GetConversation(c.ID)
		if err != nil || (row != nil) != (c.ID == kept.ID) {
			t.Fatalf("wrong batch database result for %d: %v %v", c.ID, row, err)
		}
		for _, name := range conversationDeleteFixturePaths(c.ID) {
			_, err := os.Stat(filepath.Join(s.m.dir, name))
			if c.ID == kept.ID && err != nil {
				t.Fatalf("unselected files removed: %s: %v", name, err)
			}
			if c.ID != kept.ID && !os.IsNotExist(err) {
				t.Fatalf("selected files retained: %s: %v", name, err)
			}
		}
	}
}
