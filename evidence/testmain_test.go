package evidence

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, e := os.MkdirTemp("", "prtseek-evidence-")
	if e != nil {
		panic(e)
	}
	os.Setenv("prtseek_DB_PATH", filepath.Join(dir, "test.db"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
