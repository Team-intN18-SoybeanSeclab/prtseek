package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "prtseek-server-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("prtseek_DB_PATH", filepath.Join(dir, "test.db"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
