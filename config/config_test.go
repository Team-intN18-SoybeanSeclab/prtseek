package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSQLitePathPrecedence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("prtseek_CONFIG", file)
	t.Setenv("prtseek_DB_PATH", "")
	t.Setenv("prtseek_PG_DSN", "")
	if err := os.WriteFile(file, []byte(`{"database":{"path":"local.db"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if p, _, e := SQLitePath(); e != nil || p != "local.db" {
		t.Fatalf("config: %s %v", p, e)
	}
	t.Setenv("prtseek_DB_PATH", "override.db")
	if p, _, e := SQLitePath(); e != nil || p != "override.db" {
		t.Fatalf("env: %s %v", p, e)
	}
	t.Setenv("prtseek_DB_PATH", "")
	t.Setenv("prtseek_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	if p, _, e := SQLitePath(); e != nil || filepath.Base(p) != "prtseek.db" {
		t.Fatalf("default: %s %v", p, e)
	}
	t.Setenv("prtseek_PG_DSN", "postgres://old/database")
	if _, _, e := SQLitePath(); e == nil {
		t.Fatal("legacy server config silently ignored")
	}
}

func TestSQLitePathRejectsMalformedConfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("prtseek_CONFIG", file)
	t.Setenv("prtseek_DB_PATH", "")
	t.Setenv("ARTEX_DB_PATH", "")
	if err := os.WriteFile(file, []byte(`{"database":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SQLitePath(); err == nil {
		t.Fatal("malformed config silently selected a new database")
	}
}
