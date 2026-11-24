package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestCTFTurnArchiveKeepsArtifactsAndLedger(t *testing.T) {
	s, c := ctfRegressionServer(t)
	dir := s.conversationWorkDir(c.ID)
	if err := os.MkdirAll(filepath.Join(dir, "uploads"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"uploads/笔记.txt", ".private", "turn-archive-old.zip", "jwt.key"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	large, err := os.Create(filepath.Join(dir, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = large.Truncate((8 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	large.Close()
	id, err := s.m.pg.StartConversationTurn(c.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.archiveConversationTurn(c.ID, id); err == nil {
		t.Fatal("archived an unfinished turn")
	}
	if _, err = s.m.pg.AppendConvActivity(c.ID, db.Activity{Kind: "text", Summary: "当前记录 password=fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.m.pg.Exec(`UPDATE conversation_turns SET reason='timeout',state='timed_out',finished_ms=started_ms+14400000,input_tokens=23000 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	name, err := s.archiveConversationTurn(c.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	entries := map[string][]byte{}
	for _, f := range z.File {
		body, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[f.Name] = data
	}
	var m turnArchiveSummary
	if err = json.Unmarshal(entries["summary.json"], &m); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || string(entries["files/uploads/笔记.txt"]) != "fixture" || m.Files != 1 || m.LastVisibleSummary != "当前记录 [REDACTED_CREDENTIAL]" || m.FinishedMS-m.StartedMS != 14400000 || m.InputTokens != 23000 || len(m.Skipped) != 1 || m.Skipped[0] != "large.bin" {
		t.Fatalf("unexpected archive: %+v", m)
	}
}

func TestCTFModelProfileShowsWorkingWindow(t *testing.T) {
	for _, tc := range []struct {
		model   string
		k, want int
	}{
		{"deepseek-flash", 0, 128000}, {"deepseek-flash", 1000, 1000000}, {"custom", 0, 200000}, {"custom", 1200, 1000000},
	} {
		dto := llmProfileDTO(&db.LLMProfile{Model: tc.model, ContextWindowK: tc.k})
		if dto.WorkingWindowTokens != tc.want || dto.ContextWindowK != tc.k {
			t.Fatalf("%+v: got %d", tc, dto.WorkingWindowTokens)
		}
	}
}

func TestCTFDeadlinePublishesTurnArchiveInFilePanel(t *testing.T) {
	s, c := ctfRegressionServer(t)
	c.AgentKey = "writeup"
	useCTFFixtureModel(t, s, c, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	})
	ctx, stop := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer stop()
	ctx, cancel := context.WithCancelCause(ctx)
	s.runConversationTurn(ctx, cancel, c, "归档测试", s.convBusyKey(c.ID))
	w := retestRequest(s.conversationFiles, http.MethodGet, c.ID, "")
	var result struct{ Items []ctfFile }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatalf("file panel: %d %s", w.Code, w.Body)
	}
	for _, f := range result.Items {
		if ok, _ := filepath.Match("turn-archive-*.zip", f.Name); ok {
			return
		}
	}
	t.Fatalf("deadline did not publish an archive: %+v", result)
}
