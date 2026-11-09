package db

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteCollectionsAndDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	d, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	var mode string
	if e = d.QueryRow(`PRAGMA journal_mode`).Scan(&mode); e != nil || mode != "wal" {
		t.Fatalf("WAL: %s %v", mode, e)
	}
	var raw string
	if e = d.QueryRow(`SELECT collection_append('[]',$1)`, "reporter").Scan(&raw); e != nil || raw != `["reporter"]` {
		t.Fatalf("append: %s %v", raw, e)
	}
	if e = d.QueryRow(`SELECT collection_remove('["reporter","custom-agent"]',$1)`, "reporter").Scan(&raw); e != nil || raw != `["custom-agent"]` {
		t.Fatalf("remove: %s %v", raw, e)
	}
	body := `{ "request": "preserve whitespace" }`
	if e = d.EnsureLLMRecordsTable(); e != nil {
		t.Fatal(e)
	}
	if _, e = d.Exec(`INSERT INTO llm_records(raw_request) VALUES($1)`, body); e != nil {
		t.Fatal(e)
	}
	archive, _, e := queryArchiveRows(d, `SELECT * FROM llm_records`)
	if e != nil {
		t.Fatal(e)
	}
	var rows []map[string]any
	if e = json.Unmarshal(archive, &rows); e != nil {
		t.Fatal(e)
	}
	if rows[0]["raw_request"] != body {
		t.Fatalf("archive changed text: %#v", rows[0]["raw_request"])
	}
	// Text resembling a timestamp must not be normalized as a DATETIME column.
	body = "2026-11-04T12:34:56+08:00"
	if _, e = d.Exec(`UPDATE llm_records SET raw_request=$1`, body); e != nil {
		t.Fatal(e)
	}
	archive, _, e = queryArchiveRows(d, `SELECT * FROM llm_records`)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := d.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`DELETE FROM llm_records`); e != nil {
		t.Fatal(e)
	}
	if _, e = insertSQLiteRows(tx, "llm_records", archive, false); e != nil {
		t.Fatal(e)
	}
	if e = tx.QueryRow(`SELECT raw_request FROM llm_records`).Scan(&raw); e != nil || raw != body {
		t.Fatalf("archive normalized text: %q %v", raw, e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = d.SetSetting("sqlite-test", "persisted"); e != nil {
		t.Fatal(e)
	}
	d.Close()
	d, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	if v, _, e := d.GetSetting("sqlite-test"); e != nil || v != "persisted" {
		t.Fatalf("reopen: %s %v", v, e)
	}
}

func TestRetiredIntegrationSettingsAreRemoved(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "retired-integration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.SetSetting("prtstrike_connection_v1", `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"prtstrike_status", "prtstrike_ctf_probe"} {
		if _, err := d.Exec(`INSERT INTO tools(key,system) VALUES($1,true)`, key); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.seedBuiltins(); err != nil {
		t.Fatal(err)
	}
	if value, _, err := d.GetSetting("prtstrike_connection_v1"); err != nil || value != "" {
		t.Fatalf("retired setting remains: %q, %v", value, err)
	}
	for _, key := range []string{"prtstrike_status", "prtstrike_ctf_probe"} {
		var count int
		if err := d.QueryRow(`SELECT count(*) FROM tools WHERE key=$1`, key).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retired tool %s remains: %d, %v", key, count, err)
		}
	}
}

func TestSQLiteQueueOrderSurvivesClockRollback(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	older, err := d.CreateTask("older", "queue", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := d.CreateTask("newer", "queue", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	previousClock := time.Now().UTC().Add(time.Hour)
	if _, err := d.Exec(`UPDATE tasks SET queued=true,queued_at=$2 WHERE id=$1`, older.ID, previousClock); err != nil {
		t.Fatal(err)
	}
	if err := d.Enqueue(newer.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	var first, retry time.Time
	if err := d.QueryRow(`SELECT queued_at FROM tasks WHERE id=$1`, newer.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if !first.After(previousClock) {
		t.Fatal("clock rollback moved a new task ahead of the queue")
	}
	if err := d.Enqueue(newer.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT queued_at FROM tasks WHERE id=$1`, newer.ID).Scan(&retry); err != nil {
		t.Fatal(err)
	}
	if !retry.Equal(first) {
		t.Fatal("idempotent enqueue changed the queue position")
	}
}
