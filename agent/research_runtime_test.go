package agent

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

func researchFixtureTool(name string, fail bool, calls *int) actool.CoreTool {
	return actool.Build(actool.Spec{Name: name, Schema: map[string]any{"type": "object"}, RawInput: true,
		ReadOnly: func(json.RawMessage) bool { return true }, Concurrent: func(json.RawMessage) bool { return false },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Decision{Behavior: permission.Allow}
		},
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			*calls++
			if fail {
				return actool.Result{}, errors.New("fixture failure")
			}
			return actool.Text("fixture observed"), nil
		}})
}

func researchInvoke(t *testing.T, opts *agentcore.Options, name string, input string) actool.Result {
	t.Helper()
	ctx := t.Context()
	blocked, msg, updated := opts.Hooks.PreToolUse(ctx, name, []byte(input))
	if blocked {
		return actool.Errorf(msg)
	}
	if len(updated) > 0 {
		input = string(updated)
	}
	for _, tool := range opts.Tools {
		if tool.Name() != name {
			continue
		}
		res, err := tool.Call(ctx, json.RawMessage(input), &actool.ToolContext{WorkingDir: opts.WorkingDir})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res.Flatten())
		opts.Hooks.PostToolUse(ctx, name, []byte(input), raw, res.IsError)
		return res
	}
	t.Fatalf("missing tool %s", name)
	return actool.Result{}
}

func TestResearchRuntimeActivation(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		disabled    bool
	}{{"non-flash", "gpt-fixture", false}, {"ablation", "deepseek-flash", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.disabled {
				ctx = WithResearchOptimization(ctx, false)
			}
			opts := agentcore.Options{SystemPrompt: []string{"original"}}
			if err := AttachResearchRuntime(ctx, &opts, tc.model, filepath.Join(t.TempDir(), "missing"), "源码审计"); err != nil {
				t.Fatal(err)
			}
			if opts.Hooks != nil || len(opts.Tools) != 0 || len(opts.SystemPrompt) != 1 {
				t.Fatal("unselected model was changed")
			}
		})
	}
	root := t.TempDir()
	opts := agentcore.Options{Provider: &researchRuntimeProvider{model: "deepseek-flash"}}
	if err := AttachResearchRuntime(t.Context(), &opts, "task-router", root, "源码审计"); err != nil {
		t.Fatal(err)
	}
	if _, ok := opts.Hooks.(*researchRuntime); !ok {
		t.Fatal("real routed Flash model was not activated")
	}
	if _, err := os.Stat(filepath.Join(root, ".research")); !os.IsNotExist(err) {
		t.Fatal("attach wrote state before a tool ran")
	}
	for _, model := range []string{"gpt-5-fixture", "claude-sonnet-fixture", "deepseek-flash"} {
		t.Run("ctf/"+model, func(t *testing.T) {
			ctf := agentcore.Options{SystemPrompt: []string{"original"}}
			ctx := withoutResearchGuide(WithResearchOptimization(t.Context(), true))
			if err := AttachResearchRuntime(ctx, &ctf, model, t.TempDir(), "inspect challenge"); err != nil {
				t.Fatal(err)
			}
			if _, ok := ctf.Hooks.(*researchRuntime); !ok {
				t.Fatal("CTF research hooks missing")
			}
			found := false
			for _, tool := range ctf.Tools {
				found = found || tool.Name() == "research_recall"
				if tool.Name() == "research_guide" {
					t.Fatal("generic research guide leaked into CTF toolset")
				}
			}
			if !found {
				t.Fatal("CTF research tools missing")
			}
		})
	}
}

func TestResearchRuntimeEvidenceAndRecovery(t *testing.T) {
	root := t.TempDir()
	calls := 0
	opts := agentcore.Options{WorkingDir: root, Tools: []actool.CoreTool{guardPanic(researchFixtureTool("read_fixture", false, &calls))}}
	if err := AttachResearchRuntime(t.Context(), &opts, "deepseek-flash", root, "检查 API 响应证据"); err != nil {
		t.Fatal(err)
	}
	wrapped := opts.Tools[0]
	if !wrapped.(interface{ AcceptsRawInput() bool }).AcceptsRawInput() || !wrapped.IsReadOnly(nil) || wrapped.IsConcurrencySafe(nil) {
		t.Fatal("wrapper changed tool capabilities")
	}
	if wrapped.CheckPermissions(t.Context(), nil, permission.Context{}).Behavior != permission.Allow {
		t.Fatal("wrapper changed permissions")
	}
	res := researchInvoke(t, &opts, "read_fixture", `{"file":"a"}`)
	if res.IsError || !strings.Contains(res.Flatten(), "evidence_id=t000001") || calls != 1 {
		t.Fatalf("missing observed result: %+v", res)
	}
	rt := opts.Hooks.(*researchRuntime)
	state, err := rt.memory.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Observations) != 1 {
		t.Fatalf("wrapper and post-hook double counted: %d", len(state.Observations))
	}
	recorded := researchInvoke(t, &opts, "research_record", `{"objective":"验证 API 访问边界","entry":{"kind":"fact","summary":"本地夹具返回可读内容","evidence_ids":["t000001"]},"next_steps":["复核另一身份响应"]}`)
	if recorded.IsError {
		t.Fatal(recorded.Flatten())
	}
	restored := agentcore.Options{}
	if err := AttachResearchRuntime(t.Context(), &restored, "deepseek-flash", root, "继续研究"); err != nil {
		t.Fatal(err)
	}
	prompt := strings.Join(restored.SystemPrompt, "\n")
	if !strings.Contains(prompt, "本地夹具返回可读内容") || !strings.Contains(prompt, "复核另一身份响应") {
		t.Fatal("restart lost persisted checkpoint")
	}
	// Tools injected by NewSession after Attach still pass through PostToolUse.
	raw, _ := json.Marshal("downloaded local fixture")
	rt.PostToolUse(t.Context(), "WebFetch", []byte(`{"url":"http://127.0.0.1/fixture"}`), raw, false)
	state, err = rt.memory.Snapshot()
	if err != nil || len(state.Observations) != 2 {
		t.Fatal("late SDK tool not observed", err)
	}
}

func TestResearchRuntimeFailureFeedbackAndControlledRetry(t *testing.T) {
	root := t.TempDir()
	failed, success := 0, 0
	opts := agentcore.Options{WorkingDir: root, Tools: []actool.CoreTool{researchFixtureTool("query_local", true, &failed), researchFixtureTool("inspect_environment", false, &success)}}
	if err := AttachResearchRuntime(t.Context(), &opts, "deepseek-flash", root, "API 故障恢复"); err != nil {
		t.Fatal(err)
	}
	input := `{"b":2,"a":1}`
	for i := 0; i < 3; i++ {
		res := researchInvoke(t, &opts, "query_local", input)
		if !res.IsError {
			t.Fatal("tool failure hidden")
		}
	}
	res := researchInvoke(t, &opts, "query_local", `{ "a":1,"b":2 }`)
	if failed != 3 || !res.IsError || !strings.Contains(res.Flatten(), "连续失败") {
		t.Fatal("equivalent input repeated side effect")
	}
	// A decision citing only the failed operation does not justify retrying.
	res = researchInvoke(t, &opts, "research_record", `{"entry":{"kind":"decision","summary":"检查环境前暂不重试","evidence_ids":["t000003"]}}`)
	if res.IsError {
		t.Fatal(res.Flatten())
	}
	if b, _, _ := opts.Hooks.PreToolUse(t.Context(), "query_local", []byte(input)); !b {
		t.Fatal("old evidence unlocked retry")
	}
	researchInvoke(t, &opts, "inspect_environment", `{}`)
	state, _ := opts.Hooks.(*researchRuntime).memory.Snapshot()
	newEvidence := state.Observations[len(state.Observations)-1].ID
	decision, _ := json.Marshal(ResearchRecordInput{RequestID: "one-retry", Entry: &ResearchEntryInput{Kind: "decision", Summary: "新观察说明可尝试一次重试", EvidenceIDs: []string{"t000003", newEvidence}}})
	res = researchInvoke(t, &opts, "research_record", string(decision))
	if res.IsError {
		t.Fatal(res.Flatten())
	}
	res = researchInvoke(t, &opts, "query_local", input)
	if failed != 4 || !res.IsError {
		t.Fatal("grounded decision failed to unlock one attempt")
	}
	researchInvoke(t, &opts, "research_record", string(decision))
	researchInvoke(t, &opts, "query_local", input)
	if failed != 4 {
		t.Fatal("idempotent decision allowed unlimited attempts")
	}
	// Poll exceptions are explicit; mutation names containing status are not exempt.
	if !researchPollingTool("TaskOutput") || !researchPollingTool("Sleep") || researchPollingTool("set_status") {
		t.Fatal("incorrect polling exception")
	}
}

type researchHookFixture struct {
	blocked            bool
	post, starts, ends int
}

func (h *researchHookFixture) PreToolUse(context.Context, string, []byte) (bool, string, []byte) {
	return h.blocked, "original guard", []byte(`{"normalized":true}`)
}
func (h *researchHookFixture) PostToolUse(context.Context, string, []byte, []byte, bool) { h.post++ }
func (h *researchHookFixture) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return h.blocked, nil, "original stop"
}
func (h *researchHookFixture) FireUserPromptSubmit(context.Context, string) string {
	return "original submit"
}
func (h *researchHookFixture) FireSessionStart(context.Context) { h.starts++ }
func (h *researchHookFixture) FireSessionEnd(context.Context)   { h.ends++ }

func TestResearchRuntimePreservesGuardAndStop(t *testing.T) {
	base := &researchHookFixture{blocked: true}
	opts := agentcore.Options{Hooks: base}
	if err := AttachResearchRuntime(t.Context(), &opts, "deepseek-flash", t.TempDir(), "源码审计"); err != nil {
		t.Fatal(err)
	}
	rt := opts.Hooks.(*researchRuntime)
	if b, msg, _ := rt.PreToolUse(t.Context(), "Bash", nil); !b || msg != "original guard" {
		t.Fatal("guard bypassed")
	}
	rt.observations = 8
	if p, _, msg := rt.Stop(t.Context(), nil); !p || msg != "original stop" {
		t.Fatal("base stop bypassed")
	}
	base.blocked = false
	if b, _, in := rt.PreToolUse(t.Context(), "Read", nil); b || string(in) != `{"normalized":true}` {
		t.Fatal("updated guard input lost")
	}
	if _, blocks, _ := rt.Stop(t.Context(), nil); len(blocks) != 1 {
		t.Fatal("missing research checkpoint request")
	}
	if _, blocks, _ := rt.Stop(t.Context(), nil); len(blocks) != 0 {
		t.Fatal("checkpoint request loop")
	}
	rt.FireSessionStart(t.Context())
	rt.FireSessionEnd(t.Context())
	if rt.FireUserPromptSubmit(t.Context(), "x") != "original submit" || base.starts != 1 || base.ends != 1 {
		t.Fatal("optional hooks lost")
	}
	plain := agentcore.Options{}
	if err := AttachResearchRuntime(t.Context(), &plain, "deepseek-flash", t.TempDir(), "你好"); err != nil {
		t.Fatal(err)
	}
	if plain.Hooks != nil || len(plain.Tools) != 0 || len(plain.SystemPrompt) != 0 {
		t.Fatal("plain chat injected research overhead")
	}
}

func TestResearchRuntimeUnreadableMemoryDoesNotBlockChat(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".research"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".research", "state.json")
	broken := []byte("incomplete previous state")
	if err := os.WriteFile(path, broken, 0600); err != nil {
		t.Fatal(err)
	}
	base := &researchHookFixture{}
	opts := agentcore.Options{Hooks: base, SystemPrompt: []string{"existing rules"}}
	if err := AttachResearchRuntime(t.Context(), &opts, "deepseek-flash", root, "你好"); err != nil {
		t.Fatal(err)
	}
	if opts.Hooks != base || len(opts.Tools) != 0 || !strings.Contains(strings.Join(opts.SystemPrompt, "\n"), "不覆盖原文件") {
		t.Fatal("failed enhancement did not degrade transparently")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(broken) {
		t.Fatal("damaged state overwritten", err)
	}
}

func TestResearchRuntimePausedReadsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	ctx := WithPersonalReadOnly(t.Context())
	calls := 0
	opts := agentcore.Options{EnableWebFetch: true, Tools: []actool.CoreTool{researchFixtureTool("Bash", false, &calls), researchFixtureTool("Read", false, &calls)}}
	if err := AttachResearchRuntime(ctx, &opts, "deepseek-flash", root, "查看已有证据"); err != nil {
		t.Fatal(err)
	}
	personalSessionOptions(ctx, &opts)
	for _, tool := range opts.Tools {
		if !personalReadOnlyTool(tool.Name()) {
			t.Fatalf("paused tool %s", tool.Name())
		}
	}
	res := researchInvoke(t, &opts, "research_recall", `{}`)
	if res.IsError {
		t.Fatal(res.Flatten())
	}
	researchInvoke(t, &opts, "Read", `{}`)
	if b, _, _ := opts.Hooks.PreToolUse(ctx, "research_record", nil); !b {
		t.Fatal("paused record permitted")
	}
	if _, err := os.Stat(filepath.Join(root, ".research")); !os.IsNotExist(err) {
		t.Fatal("paused query wrote research state")
	}
	if strings.Contains(strings.Join(opts.SystemPrompt, "\n"), "research_guide，") {
		t.Fatal("paused prompt recommends hidden tools")
	}
}

func TestResearchRuntimeLinkedTaskRoot(t *testing.T) {
	conversationRoot := t.TempDir()
	taskRoot := t.TempDir()
	old := TaskResearchRoot
	TaskResearchRoot = func(id int64) string {
		if id == 42 {
			return conversationRoot
		}
		return ""
	}
	t.Cleanup(func() { TaskResearchRoot = old })
	ctx := WithRunInfo(t.Context(), RunInfo{TaskID: 42})
	opts := agentcore.Options{}
	if err := AttachResearchRuntime(ctx, &opts, "deepseek-flash", taskRoot, "继续"); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(conversationRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := opts.Hooks.(*researchRuntime).memory.EvidenceRoot(); got != want {
		t.Fatalf("handoff lost conversation evidence root: got %q, want %q", got, want)
	}
}

type researchRuntimeProvider struct {
	model    string
	complete func(llm.CompletionRequest) (llm.Message, string, llm.Usage, error)
}

func (p *researchRuntimeProvider) ResearchModel() string { return p.model }
func (p *researchRuntimeProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("unexpected streaming"))
	}
}
func (p *researchRuntimeProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.complete(req)
}

func TestResearchRuntimeProductionChat(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture.txt")
	if err := os.WriteFile(fixture, []byte("local-response-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := &researchRuntimeProvider{model: "deepseek-flash"}
	p.complete = func(req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		calls++
		found := false
		for _, tool := range req.Tools {
			if tool.Name == "research_record" {
				found = true
			}
		}
		if !found {
			t.Error("production chat missing research tools")
		}
		if calls == 1 {
			in, _ := json.Marshal(map[string]any{"file_path": fixture})
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "read1", Name: "Read", Input: in}}}, "tool_use", llm.Usage{}, nil
		}
		wire, _ := json.Marshal(req.Messages)
		if !strings.Contains(string(wire), "evidence_id=t000001") {
			t.Error("observed evidence did not reach next model turn")
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("fixture inspected")}}, "end_turn", llm.Usage{}, nil
	}
	ca := NewChatAgent(p, "task-router", root, nil, 128000)
	ca.SetNonStreaming(func() bool { return true })
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	ctx = WithModelObserver(ctx, &ModelObserver{Started: func() {}, Usage: func(llm.Usage) {}})
	if _, err := ca.Chat(ctx, "personal", "fixture-research", "核对本地 API 响应证据", 3, 15*time.Second, false, nil); err != nil {
		t.Fatal(err)
	}
	memory, err := NewResearchMemory(filepath.Join(root, "sessions", "fixture-research"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := memory.Snapshot()
	if err != nil || len(state.Observations) != 1 || calls != 2 {
		t.Fatalf("production evidence=%+v calls=%d err=%v", state.Observations, calls, err)
	}
}
