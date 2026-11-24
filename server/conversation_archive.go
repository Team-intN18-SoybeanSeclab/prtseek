package server

import (
	"archive/zip"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/artex/agent"
)

type turnArchiveSummary struct {
	ConversationID     int64      `json:"conversation_id"`
	TurnID             int64      `json:"turn_id"`
	State              string     `json:"state"`
	Reason             string     `json:"end_reason"`
	StartedMS          int64      `json:"started_ms"`
	FinishedMS         int64      `json:"finished_ms"`
	ModelCalls         int64      `json:"model_calls"`
	InputTokens        int64      `json:"input_tokens"`
	OutputTokens       int64      `json:"output_tokens"`
	LastReport         ctfOutcome `json:"last_conversation_report"`
	LastVisibleSummary string     `json:"last_visible_summary,omitempty"`
	Files              int        `json:"files_included"`
	Skipped            []string   `json:"files_skipped,omitempty"`
	ScanLimited        bool       `json:"scan_limit_reached,omitempty"`
}

// The archive is a user-facing snapshot of a finished turn. Publish only after
// the ZIP has closed so the file panel cannot offer a partially written ZIP.
func (s *Server) archiveConversationTurn(conversationID, turnID int64) (string, error) {
	summary := turnArchiveSummary{ConversationID: conversationID, TurnID: turnID}
	err := s.m.pg.QueryRow(`SELECT state,reason,started_ms,finished_ms,model_calls,input_tokens,output_tokens
		FROM conversation_turns WHERE id=$1 AND conversation_id=$2 AND finished_ms>0`, turnID, conversationID).
		Scan(&summary.State, &summary.Reason, &summary.StartedMS, &summary.FinishedMS, &summary.ModelCalls, &summary.InputTokens, &summary.OutputTokens)
	if err != nil {
		return "", err
	}
	err = s.m.pg.QueryRow(`SELECT COALESCE((SELECT substr(a.summary,1,4096) FROM conversation_activities a
		JOIN conversation_turns t ON t.conversation_id=a.conversation_id
		WHERE t.id=$1 AND a.id>t.start_activity_id AND a.kind='text' AND a.summary<>''
		ORDER BY a.id DESC LIMIT 1),'')`, turnID).Scan(&summary.LastVisibleSummary)
	if err != nil {
		return "", err
	}
	summary.LastVisibleSummary = agent.RedactResearchText(summary.LastVisibleSummary)
	summary.LastReport = s.readCTFOutcome(conversationID)
	root, err := os.OpenRoot(s.m.dir)
	if err != nil {
		return "", err
	}
	// Resolve each directory beneath the data root. A symlink must not redirect
	// the archive into a different session, even during a concurrent rename.
	for _, dir := range []string{"sessions", s.convBusyKey(conversationID)} {
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return "", fmt.Errorf("会话目录不可用")
		}
		child, err := root.OpenRoot(dir)
		root.Close()
		if err != nil {
			return "", err
		}
		opened, err := child.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			child.Close()
			return "", fmt.Errorf("会话目录已更改")
		}
		root = child
	}
	defer root.Close()
	nonce := rand.Text()
	temp := ".turn-archive-" + nonce
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer root.Remove(temp)
	w := zip.NewWriter(file)
	writeErr := writeTurnArchive(w, root, &summary)
	zipErr := w.Close()
	closeErr := file.Close()
	for _, e := range []error{writeErr, zipErr, closeErr} {
		if e != nil {
			return "", e
		}
	}
	name := fmt.Sprintf("turn-archive-%d-%s.zip", turnID, nonce)
	if err := root.Rename(temp, name); err != nil {
		return "", err
	}
	return name, nil
}

func writeTurnArchive(w *zip.Writer, root *os.Root, summary *turnArchiveSummary) error {
	const maxFile = 8 << 20
	remaining := int64(64 << 20)
	visited := 0
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		visited++
		if visited > 2000 {
			summary.ScanLimited = true
			return fs.SkipAll
		}
		if name == "." {
			return walkErr
		}
		if !archiveEntryAllowed(name) {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		skip := func() error {
			summary.Skipped = append(summary.Skipped, name)
			return nil
		}
		if walkErr != nil {
			return skip()
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxFile || info.Size() > remaining {
			return skip()
		}
		f, err := root.Open(name)
		if err != nil {
			return skip()
		}
		defer f.Close()
		before, err := f.Stat()
		if err != nil || !os.SameFile(info, before) || before.Size() != info.Size() {
			return skip()
		}
		data, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
		after, statErr := f.Stat()
		if err != nil || statErr != nil || int64(len(data)) != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(before.ModTime()) {
			return skip()
		}
		dst, err := w.Create("files/" + name)
		if err != nil {
			return err
		}
		if _, err = dst.Write(data); err != nil {
			return err
		}
		remaining -= int64(len(data))
		summary.Files++
		return nil
	})
	if err != nil {
		return err
	}
	dst, err := w.Create("summary.json")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(dst)
	enc.SetIndent("", "  ")
	return enc.Encode(summary)
}

// WalkDir prunes excluded directories before visiting their descendants.
func archiveEntryAllowed(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	if !validUploadName(base) || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "credentials") ||
		strings.EqualFold(name, "cmd-output") || strings.HasPrefix(strings.ToLower(name), "turn-archive-") {
		return false
	}
	switch base {
	case "node_modules", "__pycache__", "jwt.key", "id_rsa", "id_ed25519":
		return false
	}
	switch filepath.Ext(base) {
	case ".pem", ".p12", ".pfx":
		return false
	}
	return true
}
