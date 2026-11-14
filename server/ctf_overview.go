package server

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

type ctfOverview struct {
	State            string       `json:"state"`
	StartedMS        int64        `json:"started_ms"`
	FinishedMS       int64        `json:"finished_ms"`
	ElapsedMS        int64        `json:"elapsed_ms"`
	ModelCalls       int64        `json:"model_calls"`
	ModelCallLimit   int          `json:"model_call_limit"`
	InputTokens      int64        `json:"input_tokens"`
	OutputTokens     int64        `json:"output_tokens"`
	CacheReadTokens  int64        `json:"cache_read_tokens"`
	CacheWriteTokens int64        `json:"cache_write_tokens"`
	TurnID           int64        `json:"turn_id"`
	EndReason        string       `json:"end_reason,omitempty"`
	ActiveChildren   int          `json:"active_children"`
	Latest           *ActivityDTO `json:"latest,omitempty"`
	Outcome          ctfOutcome   `json:"outcome"`
}

func (s *Server) conversationOverview(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	view := ctfOverview{State: "idle", ModelCallLimit: ctfMaxModelCalls, Outcome: s.readCTFOutcome(c.ID)}
	_ = s.m.pg.QueryRow(`SELECT state,started_ms,finished_ms,model_calls FROM conversation_executions WHERE conversation_id=$1`, c.ID).Scan(&view.State, &view.StartedMS, &view.FinishedMS, &view.ModelCalls)
	_ = s.m.pg.QueryRow(`SELECT id,reason,state,started_ms,finished_ms FROM conversation_turns WHERE conversation_id=$1 ORDER BY id DESC LIMIT 1`, c.ID).Scan(&view.TurnID, &view.EndReason, &view.State, &view.StartedMS, &view.FinishedMS)
	if view.StartedMS > 0 {
		end := view.FinishedMS
		if end == 0 {
			end = time.Now().UnixMilli()
		}
		view.ElapsedMS = max(0, end-view.StartedMS)
	}
	ids, err := s.ctfTreeIDs(c.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// One aggregate query for the entire task tree; avoid per-child polling queries.
	err = s.m.pg.QueryRow(ctfTreeQuery+`, `+db.ConversationUsageCTE+`
	 SELECT COALESCE(SUM(a.input_tokens),0),COALESCE(SUM(a.output_tokens),0),COALESCE(SUM(a.cache_read_tokens),0),COALESCE(SUM(a.cache_write_tokens),0)
	 FROM conversation_usage a JOIN tree ON tree.id=a.conversation_id`, c.ID).Scan(&view.InputTokens, &view.OutputTokens, &view.CacheReadTokens, &view.CacheWriteTokens)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var latest db.Activity
	err = s.m.pg.QueryRow(ctfTreeQuery+` SELECT a.id,a.worker,a.kind,COALESCE(a.tool,''),a.is_error,a.summary,a.created_at
	 FROM conversation_activities a JOIN tree ON tree.id=a.conversation_id
	 WHERE a.kind NOT IN ('usage','thinking','user') ORDER BY a.id DESC LIMIT 1`, c.ID).Scan(&latest.ID, &latest.Worker, &latest.Kind, &latest.Tool, &latest.IsError, &latest.Summary, &latest.CreatedAt)
	if err == nil {
		dto := activityDTO(latest)
		view.Latest = &dto
	}
	s.chatMu.Lock()
	for _, id := range ids {
		if id != c.ID && s.chatBusy[s.convBusyKey(id)] {
			view.ActiveChildren++
		}
	}
	s.chatMu.Unlock()
	s.workflowMu.Lock()
	run := s.workflowRuns[c.ID]
	s.workflowMu.Unlock()
	if run != nil {
		view.ModelCalls = run.budget.Calls()
	}
	writeJSON(w, 200, view)
}

type ctfFile struct {
	ConversationID int64  `json:"conversation_id"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	WorkspacePath  string `json:"workspace_path"`
	Kind           string `json:"kind"`
	Size           int64  `json:"size"`
	ModifiedMS     int64  `json:"modified_ms"`
}

func (s *Server) conversationFiles(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	ids, err := s.ctfTreeIDs(c.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	files := []ctfFile{}
	truncated := false
	visited := 0
	// List user inputs and deliverables across the whole tree before evidence.
	// Otherwise a busy parent's evidence directory can exhaust the limit before
	// the child's answer or EXP is even visited.
	for _, evidence := range []bool{false, true} {
		for _, id := range ids {
			root, err := os.OpenRoot(s.conversationWorkDir(id))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				writeErr(w, 500, "无法读取会话文件")
				return
			}
			walk := func(path string, d fs.DirEntry, walkErr error) error {
				visited++
				if visited > 2000 || len(files) >= 300 {
					truncated = true
					return fs.SkipAll
				}
				if walkErr != nil {
					return nil
				}
				if d.Type()&os.ModeSymlink != 0 {
					return nil
				}
				if d.IsDir() {
					if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "__pycache__" {
						return fs.SkipDir
					}
					if !evidence && (path == ".research" || path == "cmd-output") {
						return fs.SkipDir
					}
					return nil
				}
				if strings.HasPrefix(path, ".research/") && !strings.HasPrefix(path, ".research/evidence/") && !strings.HasPrefix(path, ".research/http/") && !strings.HasPrefix(path, ".research/experiments/") {
					return nil
				}
				info, err := d.Info()
				if err != nil || !info.Mode().IsRegular() {
					return nil
				}
				kind := "artifact"
				if strings.HasPrefix(path, "uploads/") {
					kind = "input"
				}
				if strings.HasPrefix(path, ".research/") || strings.HasPrefix(path, "cmd-output/") {
					kind = "evidence"
				}
				files = append(files, ctfFile{ConversationID: id, Name: d.Name(), Path: path, WorkspacePath: filepath.ToSlash(filepath.Join("sessions", s.convBusyKey(id), path)), Kind: kind, Size: info.Size(), ModifiedMS: info.ModTime().UnixMilli()})
				return nil
			}
			if evidence {
				for _, path := range []string{".research/experiments", ".research/evidence", ".research/http", "cmd-output"} {
					_ = fs.WalkDir(root.FS(), path, walk)
					if truncated {
						break
					}
				}
			} else {
				_ = fs.WalkDir(root.FS(), ".", walk)
			}
			root.Close()
			if truncated {
				break
			}
		}
		if truncated {
			break
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Kind != files[j].Kind {
			return files[i].Kind < files[j].Kind
		}
		return files[i].ModifiedMS > files[j].ModifiedMS
	})
	writeJSON(w, 200, map[string]any{"items": files, "truncated": truncated, "workspace_path": filepath.ToSlash(filepath.Join("sessions", s.convBusyKey(c.ID)))})
}

func (s *Server) conversationFile(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("conversation_id"), 10, 64)
	if err != nil {
		id = c.ID
	}
	ids, err := s.ctfTreeIDs(c.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	allowed := false
	for _, candidate := range ids {
		if candidate == id {
			allowed = true
		}
	}
	path := r.URL.Query().Get("path")
	if !allowed || !filepath.IsLocal(path) || strings.Contains(path, "\\") {
		writeErr(w, 400, "非法会话文件路径")
		return
	}
	root, err := os.OpenRoot(s.conversationWorkDir(id))
	if err != nil {
		writeErr(w, 404, "文件不存在")
		return
	}
	defer root.Close()
	f, err := root.Open(path)
	if err != nil {
		writeErr(w, 404, "文件不存在或不可访问")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeErr(w, 400, "不是普通文件")
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(info.Name())+`"`)
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
		return
	}
	const previewLimit = 128 << 10
	if info.Size() > previewLimit {
		writeJSON(w, 200, map[string]any{"too_large": true})
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, previewLimit+1))
	if err != nil {
		writeErr(w, 500, "读取失败")
		return
	}
	if len(data) > previewLimit || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		writeJSON(w, 200, map[string]any{"binary": true})
		return
	}
	writeJSON(w, 200, map[string]any{"content": string(data)})
}
