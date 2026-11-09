package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrPersonalBackgroundActive = errors.New("conversation already has an active background task")
	ErrPersonalHandoffExists    = errors.New("background handoff already exists")
)

// PersonalHandoffInput is committed in the SAME transaction as the task and its
// exploration. There is no interval in which a task can run without its source
// conversation or checkpoint being known.
type PersonalHandoffInput struct {
	ConversationID int64
	IdempotencyKey string
	SourceCursor   int64
	Checkpoint     string
	ConfigSnapshot json.RawMessage
}

type PersonalBackgroundRun struct {
	ID             int64           `json:"id"`
	ConversationID int64           `json:"conversation_id"`
	TaskID         int64           `json:"-"`
	IdempotencyKey string          `json:"idempotency_key"`
	SourceCursor   int64           `json:"source_cursor"`
	Checkpoint     string          `json:"checkpoint"`
	ConfigSnapshot json.RawMessage `json:"config_snapshot"`
	State          string          `json:"state"`
	Active         bool            `json:"active"`
	CreatedAt      time.Time       `json:"created_at"`
}

const personalBackgroundSchema = `
CREATE TABLE IF NOT EXISTS conversation_task_runs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 conversation_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE RESTRICT,
 task_id BIGINT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
 idempotency_key TEXT NOT NULL,
 source_cursor BIGINT NOT NULL DEFAULT 0,
 checkpoint TEXT NOT NULL,
 config_snapshot TEXT NOT NULL DEFAULT '{}',
 state TEXT NOT NULL DEFAULT 'active',
 active BOOLEAN NOT NULL DEFAULT true,
 created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
 UNIQUE(conversation_id,idempotency_key)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_personal_one_background
 ON conversation_task_runs(conversation_id) WHERE active;
`

func (d *DB) InitPersonalBackgroundSchema() error {
	_, err := d.Exec(personalBackgroundSchema)
	return err
}

// lockPersonalHandoff serializes both duplicate requests and different requests
// for the same conversation. Called before any task/exploration is inserted.
func lockPersonalHandoff(tx *sql.Tx, h *PersonalHandoffInput) error {
	if h == nil {
		return nil
	}
	if h.ConversationID <= 0 || strings.TrimSpace(h.IdempotencyKey) == "" || len(h.IdempotencyKey) > 128 {
		return fmt.Errorf("invalid background handoff identity")
	}
	if !json.Valid(h.ConfigSnapshot) {
		return fmt.Errorf("invalid background configuration snapshot")
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM conversations WHERE id=$1`, h.ConversationID).Scan(&id); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM conversation_task_runs WHERE conversation_id=$1 AND idempotency_key=$2)`, h.ConversationID, h.IdempotencyKey).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrPersonalHandoffExists
	}
	if _, err := tx.Exec(`UPDATE conversation_task_runs AS r SET active=false
 FROM tasks t WHERE r.task_id=t.id AND r.conversation_id=$1 AND r.active AND t.status IN ('done','failed','timeout')`, h.ConversationID); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM conversation_task_runs WHERE conversation_id=$1 AND active)`, h.ConversationID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrPersonalBackgroundActive
	}
	return nil
}

func insertPersonalHandoff(tx *sql.Tx, task *Task, h *PersonalHandoffInput) error {
	if h == nil {
		return nil
	}
	if _, err := tx.Exec(`INSERT INTO conversation_task_runs(conversation_id,task_id,idempotency_key,source_cursor,checkpoint,config_snapshot)
 VALUES ($1,$2,$3,$4,$5,$6)`, h.ConversationID, task.ID, h.IdempotencyKey, h.SourceCursor, h.Checkpoint, string(h.ConfigSnapshot)); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"text": h.Checkpoint, "summary": "前台会话交接记录（含原始记录与文件位置）", "source_conversation_id": h.ConversationID, "source_cursor": h.SourceCursor})
	_, err := tx.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,'hint',$2,10,'active','human')`, task.ExplorationID, string(payload))
	return err
}

const personalRunCols = `id,conversation_id,task_id,idempotency_key,source_cursor,checkpoint,config_snapshot,state,active,created_at`

func scanPersonalRun(row interface{ Scan(...any) error }) (*PersonalBackgroundRun, error) {
	var r PersonalBackgroundRun
	err := row.Scan(&r.ID, &r.ConversationID, &r.TaskID, &r.IdempotencyKey, &r.SourceCursor, &r.Checkpoint, &r.ConfigSnapshot, &r.State, &r.Active, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

func (d *DB) PersonalBackgroundRun(conversationID int64, key string) (*PersonalBackgroundRun, error) {
	if key != "" {
		return scanPersonalRun(d.QueryRow(`SELECT `+personalRunCols+` FROM conversation_task_runs WHERE conversation_id=$1 AND idempotency_key=$2`, conversationID, key))
	}
	return scanPersonalRun(d.QueryRow(`SELECT `+personalRunCols+` FROM conversation_task_runs WHERE conversation_id=$1 ORDER BY id DESC LIMIT 1`, conversationID))
}

func (d *DB) PersonalBackgroundForTask(taskID int64) (*PersonalBackgroundRun, error) {
	return scanPersonalRun(d.QueryRow(`SELECT `+personalRunCols+` FROM conversation_task_runs WHERE task_id=$1`, taskID))
}

func (d *DB) SetPersonalBackgroundState(id int64, state string, active bool) error {
	// A late status poll must not replace the explicit user-stop reason with the
	// engine's compatible terminal value (failed).
	_, err := d.Exec(`UPDATE conversation_task_runs SET state=CASE WHEN state='stopped' THEN state ELSE $2 END,active=$3 WHERE id=$1`, id, state, active)
	return err
}

// On process restart, never replay an interrupted external operation silently.
// The existing engine may reset intents to open, but persisted pause prevents
// their execution until the user explicitly resumes after seeing the notice.
func (d *DB) RecoverPersonalBackgroundTasks() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE conversation_task_runs AS r SET active=false FROM tasks t
 WHERE r.task_id=t.id AND t.status IN ('done','failed','timeout')`); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE conversation_task_runs AS r SET state='needs_review' FROM tasks t
 WHERE r.task_id=t.id AND r.active AND t.status NOT IN ('done','failed','timeout') AND NOT t.paused`); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE tasks AS t SET paused=true,queued=false,queued_at=NULL
 FROM conversation_task_runs r WHERE r.task_id=t.id AND r.active AND r.state='needs_review'`); err != nil {
		return err
	}
	return tx.Commit()
}
