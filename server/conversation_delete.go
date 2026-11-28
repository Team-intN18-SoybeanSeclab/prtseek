package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/sidequestion"
)

// Admission covers short setup/upload writes as well as the chatBusy runner.
// Checking existence here closes the gap after an HTTP handler read its row.
func (s *Server) beginConversationFileOperation(id int64) error {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if s.conversationDeleting[id] {
		return errors.New("会话正在删除，请稍后重试")
	}
	c, err := s.m.pg.GetConversation(id)
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New("会话已不存在")
	}
	if s.conversationFileOps == nil {
		s.conversationFileOps = map[int64]int{}
	}
	s.conversationFileOps[id]++
	return nil
}

func (s *Server) endConversationFileOperation(id int64) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	s.conversationFileOps[id]--
	if s.conversationFileOps[id] <= 0 {
		delete(s.conversationFileOps, id)
	}
}

// File-panel writes to session directories use the same admission barrier.
func (s *Server) beginWorkspaceConversationWrite(abs string) (func(), error) {
	parts := strings.Split(filepath.ToSlash(s.wsRel(abs)), "/")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "sessions") {
		return func() {}, nil
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(parts[1]), "conv-"), 10, 64)
	if err != nil || id <= 0 {
		return func() {}, nil
	}
	if err = s.beginConversationFileOperation(id); err != nil {
		return nil, err
	}
	return func() { s.endConversationFileOperation(id) }, nil
}

// Exact IDs, not a prefix glob: deleting conv-1 must never select conv-10.
// Rooted removal unlinks a target symlink without traversing its destination.
func deleteConversationFiles(dataDir string, id int64) error {
	if id <= 0 {
		return errors.New("invalid conversation ID")
	}
	base, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("会话数据根目录不可安全访问")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("会话数据根目录已更改")
	}
	n := strconv.FormatInt(id, 10)
	key := "conv-" + n
	groups := []struct {
		parent string
		names  []string
		dir    *os.Root
	}{
		{parent: "sessions", names: []string{key, n}},
		{parent: "transcripts", names: []string{key + ".jsonl", key}},
		{parent: "noa", names: []string{"chat-" + key}},
	}
	// Pin all existing parents before touching their entries.
	for i := range groups {
		group := &groups[i]
		info, err := root.Lstat(group.parent)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s 不是安全的会话数据目录", group.parent)
		}
		group.dir, err = root.OpenRoot(group.parent)
		if err != nil {
			return err
		}
		defer group.dir.Close()
		opened, err := group.dir.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return fmt.Errorf("%s 目录已更改", group.parent)
		}
	}
	for _, group := range groups {
		if group.dir == nil {
			continue
		}
		for _, name := range group.names {
			if err := group.dir.RemoveAll(name); err != nil {
				return fmt.Errorf("清理 %s 失败: %w", filepath.Join(group.parent, name), err)
			}
		}
	}
	return nil
}

func (s *Server) deleteConversationWithFiles(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("invalid conversation ID")
	}
	s.chatMu.Lock()
	if s.conversationDeleting[id] {
		s.chatMu.Unlock()
		return errors.New("会话正在删除")
	}
	if s.conversationDeleting == nil {
		s.conversationDeleting = map[int64]bool{}
	}
	s.conversationDeleting[id] = true
	s.chatMu.Unlock()
	defer func() { s.chatMu.Lock(); delete(s.conversationDeleting, id); s.chatMu.Unlock() }()
	c, err := s.m.pg.GetConversation(id)
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New("conversation not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.cancelConversation(id)
		s.chatMu.Lock()
		busy := s.chatBusy[s.convBusyKey(id)] || s.conversationFileOps[id] > 0
		s.chatMu.Unlock()
		if !busy {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("会话仍在停止或写入中，文件与记录尚未删除，请稍后重试")
		case <-ticker.C:
		}
	}
	// A previously admitted background handoff can link a task while we wait.
	if run, err := s.m.pg.PersonalBackgroundRun(id, ""); err != nil {
		return err
	} else if run != nil {
		return errors.New("此会话关联后台执行记录，请保留会话以便回查结果")
	}
	// Approval side questions identify their parent by approval ID instead of
	// conversation ID. Drain both kinds before removing the owning DB row.
	approvals := map[int64]bool{}
	rows, err := s.m.pg.QueryContext(ctx, `SELECT id FROM intercept_pending WHERE conversation_id=$1`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var approval int64
		if err := rows.Scan(&approval); err != nil {
			rows.Close()
			return err
		}
		approvals[approval] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	match := func(p sidequestion.Parent) bool { return p.ConversationID == id || approvals[p.ApprovalID] }
	if s.side != nil {
		s.side.commands.Lock()
		defer s.side.commands.Unlock()
		for _, done := range s.cancelSideWhere(match) {
			select {
			case <-done:
			case <-ctx.Done():
				return errors.New("会话旁路任务仍在停止，请稍后重试")
			}
		}
		s.side.mu.Lock()
		for key, snap := range s.side.pending {
			if match(snap.Parent) {
				delete(s.side.pending, key)
				delete(s.side.seen, key)
			}
		}
		for key, snap := range s.side.latest {
			if match(snap.Parent) {
				delete(s.side.latest, key)
				delete(s.side.seen, key)
			}
		}
		s.side.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Keep the row visible on any filesystem error so the user can retry. A
	// successful response means the files have physically been removed.
	if err := deleteConversationFiles(s.m.dir, id); err != nil {
		return fmt.Errorf("工作区未能完整清理，会话记录保留：%w", err)
	}
	return s.m.pg.DeleteConversation(id)
}
