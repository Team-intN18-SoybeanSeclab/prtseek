package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// If a failed resume were ignored, this provider would execute a visible local
// side effect. The fresh-session case proves the fixture can actually do so.
type resumeFixtureProvider struct{ calls atomic.Int32 }

func (p *resumeFixtureProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if p.calls.Add(1) == 1 {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "resume-probe-1", Name: "resume_probe", Input: json.RawMessage(`{}`)},
		}}, "tool_use", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("resume fixture complete")}}, "end_turn", llm.Usage{}, nil
}

func (p *resumeFixtureProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		msg, stop, _, _ := p.Complete(ctx, req)
		events := []llm.StreamEvent{{Type: llm.SEMessageStart}}
		for _, block := range msg.Content {
			if block.Type == llm.BlockToolUse {
				events = append(events,
					llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: block.ID, ToolName: block.Name},
					llm.StreamEvent{Type: llm.SEToolInputJSON, Text: string(block.Input)})
			} else {
				events = append(events, llm.StreamEvent{Type: llm.SETextDelta, Text: block.Text})
			}
		}
		events = append(events, llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop}, llm.StreamEvent{Type: llm.SEMessageStop})
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func TestSessionResumeProductionChat(t *testing.T) {
	for _, mode := range []string{"streaming", "non_streaming"} {
		for _, history := range []string{"corrupt_jsonl", "unreadable_path", "new_session"} {
			t.Run(mode+"/"+history, func(t *testing.T) {
				root := t.TempDir()
				store := transcript.NewStore(filepath.Join(root, "transcripts"))
				const sessionID = "resume-fixture"
				path := store.MainPath(sessionID)
				var before []byte
				switch history {
				case "corrupt_jsonl":
					msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("先前动作已完成，不要重复执行。")}}
					if err := store.Append(path, transcript.Record{UUID: "prior-action", SessionID: sessionID, Type: "message", Message: &msg}); err != nil {
						t.Fatal(err)
					}
					valid, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					before = append(valid, []byte(`{"type":"message","message":`)...)
					if err := os.WriteFile(path, before, 0600); err != nil {
						t.Fatal(err)
					}
				case "unreadable_path":
					// A directory occupying the JSONL path fails deterministically on
					// Windows as well as Unix, without depending on ACL privileges.
					if err := os.MkdirAll(path, 0700); err != nil {
						t.Fatal(err)
					}
				}

				var toolCalls atomic.Int32
				marker := filepath.Join(root, "side-effect.txt")
				probe := actool.Build(actool.Spec{
					Name: "resume_probe", Schema: map[string]any{"type": "object"}, RawInput: true,
					Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
						toolCalls.Add(1)
						if err := os.WriteFile(marker, []byte("executed"), 0600); err != nil {
							return actool.Result{}, err
						}
						return actool.Text("local side effect recorded"), nil
					},
				})
				oldAugment, oldResolve := ToolAugment, ToolResolve
				t.Cleanup(func() { ToolAugment, ToolResolve = oldAugment, oldResolve })
				ToolResolve = nil
				ToolAugment = func(context.Context, string) ([]actool.CoreTool, DeferredInfo, func()) {
					return []actool.CoreTool{probe}, DeferredInfo{}, nil
				}
				provider := &resumeFixtureProvider{}
				chat := NewChatAgent(provider, "resume-fixture", root, store, 128000)
				chat.SetNonStreaming(func() bool { return mode == "non_streaming" })
				activities := 0
				answer, err := chat.Chat(t.Context(), "personal", sessionID, "继续先前任务", 3, 10*time.Second, false, func(db.Activity) { activities++ })
				if history == "new_session" {
					if err != nil || answer != "resume fixture complete" || provider.calls.Load() != 2 || toolCalls.Load() != 1 {
						t.Fatalf("fresh session answer=%q model=%d tool=%d err=%v", answer, provider.calls.Load(), toolCalls.Load(), err)
					}
					if _, err := os.Stat(marker); err != nil {
						t.Fatalf("fresh-session fixture did not execute its tool: %v", err)
					}
					if records, err := store.Load(path); err != nil || len(records) < 4 {
						t.Fatalf("fresh-session transcript records=%d err=%v", len(records), err)
					}
					return
				}
				var resumeErr *SessionResumeError
				if !errors.As(err, &resumeErr) || resumeErr.SessionID != sessionID || errors.Unwrap(resumeErr) == nil {
					t.Fatalf("expected typed resume failure with its original cause, got %v", err)
				}
				if answer != "" || provider.calls.Load() != 0 || toolCalls.Load() != 0 || activities != 0 {
					t.Fatalf("failed resume continued: answer=%q model=%d tool=%d activities=%d", answer, provider.calls.Load(), toolCalls.Load(), activities)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("failed resume produced a side effect: %v", err)
				}
				if history == "corrupt_jsonl" {
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("failed resume changed damaged history: %v", err)
					}
					var syntaxErr *json.SyntaxError
					if !errors.As(resumeErr, &syntaxErr) {
						t.Fatalf("JSON corruption cause was lost: %v", resumeErr.Cause)
					}
				} else if info, err := os.Stat(path); err != nil || !info.IsDir() {
					t.Fatalf("failed resume replaced unreadable path: %v", err)
				}
			})
		}
	}
}
