package db

func (d *DB) EnsureAgentArchives() error {
	_, err := d.Exec(`CREATE TABLE IF NOT EXISTS agent_archives (agent_key TEXT PRIMARY KEY, deleted_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')))`)
	return err
}

func (d *DB) AgentArchived(key string) bool {
	var found bool
	_ = d.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_archives WHERE agent_key=$1)`, key).Scan(&found)
	return found
}

// Archive retains prompts, bindings and conversation foreign keys for history.
func (d *DB) ArchiveAgent(key string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO agent_archives(agent_key) VALUES($1) ON CONFLICT DO NOTHING`, key); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE agents SET enabled=false WHERE key=$1`, key); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE agent_triggers SET enabled=false WHERE agent_key=$1`, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) RestoreAgent(key string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM agent_archives WHERE agent_key=$1`, key); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE agents SET enabled=true WHERE key=$1`, key); err != nil {
		return err
	}
	return tx.Commit()
}
