package db

import "time"

func (d *DB) EnsureConversationTurns() error {
	_, err := d.Exec(`CREATE TABLE IF NOT EXISTS conversation_turns (
	 id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	 root_id BIGINT NOT NULL, start_activity_id BIGINT NOT NULL, started_ms BIGINT NOT NULL, finished_ms BIGINT NOT NULL DEFAULT 0,
	 state TEXT NOT NULL DEFAULT 'running', reason TEXT NOT NULL DEFAULT '', model_calls BIGINT NOT NULL DEFAULT 0,
	 input_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0,
	 cache_read_tokens BIGINT NOT NULL DEFAULT 0, cache_write_tokens BIGINT NOT NULL DEFAULT 0);
	 CREATE INDEX IF NOT EXISTS conversation_turns_conv ON conversation_turns(conversation_id,id)`)
	return err
}

func (d *DB) StartConversationTurn(id, root int64) (int64, error) {
	r, err := d.Exec(`INSERT INTO conversation_turns(conversation_id,root_id,start_activity_id,started_ms)
	 SELECT $1,$2,COALESCE(MAX(id),0),$3 FROM conversation_activities WHERE conversation_id=$1`, id, root, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// Legacy usage snapshots are cumulative within a prompt. A user message starts
// a new prompt and a result closes it (including older result-only records).
// Only activities before the first metered turn count; this avoids counting the
// same request from both activities and the durable provider ledger.
const ConversationUsageCTE = `usage_boundaries AS (
	 SELECT conversation_id,MIN(start_activity_id) AS cutoff FROM conversation_turns GROUP BY conversation_id
), legacy_grouped AS (
	 SELECT a.*, SUM(CASE WHEN a.kind IN ('user','result') THEN 1 ELSE 0 END)
	 OVER (PARTITION BY a.conversation_id ORDER BY a.id ROWS UNBOUNDED PRECEDING)
	 - CASE WHEN a.kind='result' THEN 1 ELSE 0 END AS turn_group
	 FROM conversation_activities a LEFT JOIN usage_boundaries b ON b.conversation_id=a.conversation_id
	 WHERE b.cutoff IS NULL OR a.id<=b.cutoff
), usage_parts AS (
	 SELECT conversation_id,MAX(COALESCE(input_tokens,0)) AS input_tokens,MAX(COALESCE(output_tokens,0)) AS output_tokens,
	 MAX(COALESCE(cache_read_tokens,0)) AS cache_read_tokens,MAX(COALESCE(cache_write_tokens,0)) AS cache_write_tokens
	 FROM legacy_grouped WHERE kind IN ('usage','result') GROUP BY conversation_id,turn_group
	 UNION ALL SELECT conversation_id,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens FROM conversation_turns
), conversation_usage AS (
	 SELECT conversation_id,SUM(input_tokens) AS input_tokens,SUM(output_tokens) AS output_tokens,
	 SUM(cache_read_tokens) AS cache_read_tokens,SUM(cache_write_tokens) AS cache_write_tokens
	 FROM usage_parts GROUP BY conversation_id
)`
