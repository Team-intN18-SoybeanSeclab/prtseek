package db

import (
	"database/sql"
	"errors"
)

type PersonalConversationSettings struct {
	ReasoningEffort  string `json:"reasoning_effort"`
	ResponseLanguage string `json:"response_language"`
}

func (d *DB) EnsurePersonalSettings() error {
	_, err := d.Exec(`CREATE TABLE IF NOT EXISTS personal_conversation_settings (
	 conversation_id BIGINT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
	 reasoning_effort TEXT NOT NULL DEFAULT '', response_language TEXT NOT NULL DEFAULT '',
	 updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')))`)
	return err
}

func (d *DB) PersonalConversationSettings(id int64) (PersonalConversationSettings, error) {
	var out PersonalConversationSettings
	err := d.QueryRow(`SELECT reasoning_effort,response_language FROM personal_conversation_settings WHERE conversation_id=$1`, id).Scan(&out.ReasoningEffort, &out.ResponseLanguage)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	return out, err
}

// SetPersonalConversationProfile atomically updates the profile and run options.
func (d *DB) SetPersonalConversationProfile(id int64, profile *int64, effort, language string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE conversations SET llm_profile_id=$2,updated_at=(strftime('%Y-%m-%d %H:%M:%f','now')) WHERE id=$1`, id, profile)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	_, err = tx.Exec(`INSERT INTO personal_conversation_settings(conversation_id,reasoning_effort,response_language)
	 VALUES($1,$2,$3) ON CONFLICT(conversation_id) DO UPDATE SET reasoning_effort=EXCLUDED.reasoning_effort,response_language=EXCLUDED.response_language,updated_at=(strftime('%Y-%m-%d %H:%M:%f','now'))`, id, effort, language)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) SetPersonalConversationLanguage(id int64, language string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO personal_conversation_settings(conversation_id,response_language) VALUES($1,$2)
	 ON CONFLICT(conversation_id) DO UPDATE SET response_language=EXCLUDED.response_language,updated_at=(strftime('%Y-%m-%d %H:%M:%f','now'))`, id, language)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE conversation_task_runs SET config_snapshot=json_set(config_snapshot,'$.response_language',$2) WHERE conversation_id=$1 AND active`, id, language)
	if err != nil {
		return err
	}
	return tx.Commit()
}
