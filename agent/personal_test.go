package agent

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

func TestPersonalReadOnlySkipsDynamicToolInjection(t *testing.T) {
	oldAugment, oldResolve := ToolAugment, ToolResolve
	t.Cleanup(func() { ToolAugment, ToolResolve = oldAugment, oldResolve })
	ToolAugment = func(context.Context, string) ([]actool.CoreTool, DeferredInfo, func()) {
		t.Fatal("paused query started dynamic MCP/skill injection")
		return nil, DeferredInfo{}, nil
	}
	ToolResolve = func(context.Context, string, []actool.CoreTool) []actool.CoreTool {
		t.Fatal("paused query applied executable DB overrides")
		return nil
	}
	ctx := WithPersonalReadOnly(t.Context())
	tools, def, cleanup := AugmentTools(ctx, "mainagent", actool.DefaultTools())
	defer cleanup()
	if def.Unlock != nil || len(def.Deferred) != 0 || def.UnlockSkill != nil {
		t.Fatal("paused query retained deferred dispatch")
	}
	if len(tools) == 0 {
		t.Fatal("paused query cannot read existing local records")
	}
	for _, tool := range tools {
		if !personalReadOnlyTool(tool.Name()) {
			t.Errorf("mutation tool retained: %s", tool.Name())
		}
	}
}

type personalReadOnlyProvider struct {
	t      *testing.T
	calls  int
	marker string
}

func (p *personalReadOnlyProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(func(llm.StreamEvent, error) bool) { p.t.Fatal("unexpected streaming") }
}
func (p *personalReadOnlyProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	for _, schema := range req.Tools {
		if !personalReadOnlyTool(schema.Name) {
			p.t.Errorf("SDK injected forbidden schema after filter: %s", schema.Name)
		}
	}
	if p.calls == 1 {
		input, _ := json.Marshal(map[string]any{"file_path": p.marker, "content": "must not write"})
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "write", Name: "Write", Input: input},
			{Type: llm.BlockToolUse, ID: "web", Name: "WebFetch", Input: json.RawMessage(`{"url":"https://example.invalid"}`)},
			{Type: llm.BlockToolUse, ID: "deferred", Name: "ExecuteExtraTool", Input: json.RawMessage(`{"name":"mcp__test__mutate","input":{}}`)},
			{Type: llm.BlockToolUse, ID: "subagent", Name: "Task", Input: json.RawMessage(`{"prompt":"continue work"}`)},
			{Type: llm.BlockToolUse, ID: "resume", Name: "set_goals", Input: json.RawMessage(`{"goals":["continue"]}`)},
		}}, "tool_use", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("Paused; existing results are unchanged.")}}, "end_turn", llm.Usage{}, nil
}

func TestPersonalReadOnlyChatCannotExecuteInjectedCalls(t *testing.T) {
	dir := t.TempDir()
	p := &personalReadOnlyProvider{t: t, marker: filepath.Join(dir, "forbidden.txt")}
	ca := NewChatAgent(p, "fixture", dir, nil, 0)
	ca.SetNonStreaming(func() bool { return true })
	ctx, cancel := context.WithTimeout(WithPersonalReadOnly(t.Context()), 10*time.Second)
	defer cancel()
	if _, err := ca.Chat(ctx, "mainagent", "paused-fixture", "show existing progress", 3, 10*time.Second, true, nil); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatalf("calls=%d expected=2", p.calls)
	}
	if _, err := os.Stat(p.marker); !os.IsNotExist(err) {
		t.Fatalf("paused chat wrote a file: %v", err)
	}
}

func TestPersonalReadOnlySessionGuardBlocksSDKBypasses(t *testing.T) {
	ctx := WithPersonalReadOnly(t.Context())
	opts := agentcore.Options{EnableWebFetch: true, EnableWebSearch: true, Todos: actool.NewTodoStore(), DeferredTools: []string{"mcp__test__mutate"}, UnlockSet: actool.NewUnlockSet("mcp__test__mutate"), Plan: &agentcore.PlanOptions{}}
	personalSessionOptions(ctx, &opts)
	if opts.Tools == nil || opts.EnableWebFetch || opts.EnableWebSearch || !opts.DisableBackgroundTasks || len(opts.DeferredTools) != 0 || opts.Plan != nil || opts.Todos != nil {
		t.Fatal("SDK additions can bypass pause guard")
	}
	for _, name := range []string{"Bash", "Write", "Monitor", "TaskStop", "Task", "Subagent", "Skill", "ExecuteExtraTool", "SearchExtraTools", "set_goals", "add_intent", "WebFetch", "web_search", "mcp__test__mutate"} {
		blocked, _, _ := opts.Hooks.PreToolUse(ctx, name, []byte(`{}`))
		if !blocked {
			t.Errorf("execution guard allowed %s", name)
		}
	}
	if blocked, _, _ := opts.Hooks.PreToolUse(ctx, "Read", []byte(`{}`)); blocked {
		t.Fatal("read-only lookup blocked")
	}
}
