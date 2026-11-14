package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

func (s *Server) runMeteredChat(ctx context.Context, ca *agent.ChatAgent, c *db.Conversation, session, message string, turns int, duration time.Duration, search bool, emit func(db.Activity)) (text string, runErr error) {
	root := c.ID
	if w, ok := ctx.Value(ctfWorkflowKey{}).(*ctfWorkflowRun); ok {
		root = w.rootID
	}
	id, err := s.m.pg.StartConversationTurn(c.ID, root)
	if err != nil {
		return "", err
	}
	var mu sync.Mutex
	var meterErr error
	reason := "runner_error"
	update := func(calls int, usage llm.Usage) {
		mu.Lock()
		defer mu.Unlock()
		_, err := s.m.pg.Exec(`UPDATE conversation_turns SET model_calls=model_calls+$2,input_tokens=input_tokens+$3,output_tokens=output_tokens+$4,cache_read_tokens=cache_read_tokens+$5,cache_write_tokens=cache_write_tokens+$6 WHERE id=$1`, id, calls, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
		if err != nil && meterErr == nil {
			meterErr = err
		}
	}
	ctx = agent.WithModelObserver(ctx, &agent.ModelObserver{
		Started: func() { update(1, llm.Usage{}) },
		Usage:   func(u llm.Usage) { update(0, u) },
		Terminal: func(r harness.TerminalReason) {
			if r != "" {
				reason = string(r)
			}
		},
	})
	defer func() {
		mu.Lock()
		runErr = errors.Join(runErr, meterErr)
		mu.Unlock()
		state := "completed"
		if runErr != nil || reason != "completed" {
			state = "failed"
		}
		if ctx.Err() != nil {
			state, reason = "cancelled", "cancelled"
		}
		if ctx.Err() == context.DeadlineExceeded || reason == "timeout" {
			state, reason = "timed_out", "timeout"
		}
		if reason == "incomplete_stream" {
			state = "interrupted"
		}
		_, err := s.m.pg.Exec(`UPDATE conversation_turns SET state=$2,reason=$3,finished_ms=$4 WHERE id=$1`, id, state, reason, time.Now().UnixMilli())
		runErr = errors.Join(runErr, err)
	}()
	return ca.Chat(ctx, c.AgentKey, session, message, turns, duration, search, emit)
}
