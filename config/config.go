// Package config loads runtime configuration from a JSON file, with environment
// variables taking precedence. It configures the embedded SQLite database.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"strings"
)

// Database selects the SQLite file. Legacy server fields are read only to
// reject obsolete configuration instead of silently creating an empty database.
type Database struct {
	Path     string `json:"path"`
	DSN      string `json:"dsn"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
	SSLMode  string `json:"sslmode"`
}

// Config is the on-disk config file shape.
type Config struct {
	Database Database `json:"database"`
	SkillDir string   `json:"skill_dir"`
}

// BaseDir is the directory that anchors all runtime artifacts (config.json and
// the data/ store). It is the directory the running binary lives in, so a
// distributed executable keeps its files next to itself on any OS (Windows,
// Linux, …) regardless of the working directory it is launched from.
//
// When launched via `go run`, the binary is throwaway: it sits either in a temp
// build dir (cache miss → fresh link) OR straight inside the Go build cache
// (cache hit → run from $GOCACHE/.../...-d). We detect both and fall back to the
// current working directory so dev artifacts (data/, transcripts) and config
// resolve against the project dir, not the throwaway binary's location.
func BaseDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	dir := filepath.Dir(exe)
	if isGoRunDir(dir) {
		return "." // throwaway `go run` binary → use CWD
	}
	return dir
}

// isGoRunDir reports whether dir is where `go run` parked its executable: under
// the system temp dir (cache miss), or anywhere inside a Go build cache
// (…/go-build/…, the cache-hit case — NOT under os.TempDir(), which is why the
// old temp-only check failed intermittently). In both cases the binary is
// throwaway, so config/data must resolve against the CWD.
func isGoRunDir(dir string) bool {
	if tmp := os.TempDir(); tmp != "" {
		if rel, err := filepath.Rel(tmp, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	for _, seg := range strings.Split(filepath.ToSlash(dir), "/") {
		if seg == "go-build" {
			return true
		}
	}
	return false
}

// Path returns the config file path. Resolution order:
//  1. env prtseek_CONFIG (legacy ARTEX_CONFIG remains a fallback)
//  2. ./config.json in the current working directory (running from the project
//     dir — robust no matter where `go run` placed the temp/cached binary)
//  3. config.json next to the executable (a distributed binary keeps it beside)
//
// The first existing file wins. If none exist, the CWD path is returned so the
// "not found" message points at the project dir the user most likely expected.
func Path() string {
	if v := strings.TrimSpace(Env("CONFIG")); v != "" {
		return v
	}
	var candidates []string
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "config.json"))
	}
	candidates = append(candidates, filepath.Join(BaseDir(), "config.json"))
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return candidates[0]
}

// Load reads and parses the config file. A missing/unreadable file yields a zero
// Config (so callers fall back to defaults) rather than an error.
func Load() Config {
	var c Config
	b, err := os.ReadFile(Path())
	if err != nil {
		return c
	}
	_ = json.Unmarshal(b, &c)
	return c
}

// SkillDir returns the skill root directory with precedence:
//
//	env prtseek_SKILL_DIR (or legacy alias) > config file > BaseDir()/skills
//
// The directory is created if it does not exist.
func SkillDir() string {
	var d string
	if v := strings.TrimSpace(Env("SKILL_DIR")); v != "" {
		d = v
	} else if v := strings.TrimSpace(Load().SkillDir); v != "" {
		d = v
	} else {
		d = filepath.Join(BaseDir(), "skills")
	}
	_ = os.MkdirAll(d, 0o755)
	return d
}

// SQLitePath resolves an embedded database file, never a network DSN.
func SQLitePath() (path, source string, err error) {
	if v, name := LookupEnv("DB_PATH"); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), "环境变量 " + name, nil
	}
	var c Config
	b, readErr := os.ReadFile(Path())
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", "", fmt.Errorf("read config: %w", readErr)
	}
	if readErr == nil {
		if e := json.Unmarshal(b, &c); e != nil {
			return "", "", fmt.Errorf("parse config: %w", e)
		}
	}
	d := c.Database
	if strings.TrimSpace(d.Path) != "" {
		return d.Path, "配置文件 " + Path() + " (database.path)", nil
	}
	if Env("PG_DSN") != "" || d.DSN != "" || d.Host != "" || d.User != "" || d.DBName != "" || d.Port != 0 || d.Password != "" || d.SSLMode != "" {
		return "", "", fmt.Errorf("检测到旧 PostgreSQL 配置；请备份旧数据库并设置 database.path 或 prtseek_DB_PATH。不会自动迁移或覆盖旧数据")
	}
	return filepath.Join(BaseDir(), "data", "prtseek.db"), "默认本地 SQLite 文件", nil
}
