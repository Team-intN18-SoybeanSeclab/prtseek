package db

import (
	"path/filepath"
	"testing"
)

func TestConversationUsageInterruptedLegacyAndMeteredTurns(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c, err := d.CreateConversation("solver", "fixture", nil, PersonalConversationSettings{})
	if err != nil {
		t.Fatal(err)
	}
	appendRow := func(kind string, input, output int) {
		t.Helper()
		if _, err := d.AppendConvActivity(c.ID, Activity{Kind: kind, InputTokens: &input, OutputTokens: &output}); err != nil {
			t.Fatal(err)
		}
	}
	appendRow("user", 0, 0)
	appendRow("usage", 100, 10)
	appendRow("usage", 200, 20) // Interrupted: no result.
	appendRow("user", 0, 0)
	appendRow("usage", 30, 3)
	appendRow("result", 50, 5)
	appendRow("result", 7, 1) // Older records without user boundaries.
	appendRow("user", 0, 0)
	turn, err := d.StartConversationTurn(c.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Exec(`UPDATE conversation_turns SET input_tokens=80,output_tokens=8,cache_read_tokens=60 WHERE id=$1`, turn)
	if err != nil {
		t.Fatal(err)
	}
	appendRow("usage", 80, 8)
	appendRow("result", 80, 8) // Must not count twice.
	summaries, err := d.ConversationTokenSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].InputTokens != 337 || summaries[0].OutputTokens != 34 || summaries[0].CacheReadTokens != 60 {
		t.Fatalf("%+v", summaries)
	}
	if _, err = d.StartConversationTurn(c.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.QueryRow(`SELECT count(*) FROM conversation_turns WHERE conversation_id=$1`, c.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("turn history lost: %d %v", count, err)
	}
}
