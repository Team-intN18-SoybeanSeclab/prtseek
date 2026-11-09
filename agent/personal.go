package agent

import (
	"context"
	"strings"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

const PersonalDefaultPrompt = `你是 prtseek，用户的个人工作助手。直接使用可用工具完成当前对话中的工作，保留可回查的文件与证据。
先理解用户目标与约束；较复杂的工作用 TodoWrite 记录步骤。普通请求不创建后台任务。需要长时间持续执行时，可以建议用户点击“转后台持续执行”，但不能绕过该操作自行创建后台任务。
用户明确要求持续完成可验证目标时，在当前前台运行中持续推进；一个方向失败后记录反证并尝试有依据的其他方向。只有达成目标、用户停止、环境或授权边界阻塞，或已经穷尽有证据支持的可行方向时才结束。不要把阶段性总结或检查点当成目标完成。
完成实质阶段、得到重要结果、改变方向或遇到阻塞时，使用用户的提问语言输出简短进度：完成了什么、证据是什么、下一步是什么。不要只沉默地调用工具，也不要重复空泛状态。
只根据真实工具返回回答；区分观察、推断和已验证结论，不编造发现、进度或内部思维过程。安全研究仅在用户明确的授权范围内进行。`

type personalLanguageKey struct{}
type personalReadOnlyKey struct{}

// TaskResponseLanguage is resolved by the host from a linked task's snapshot.
var TaskResponseLanguage func(taskID int64) string

// Explicit persistence affects only the current foreground run. It never
// creates a background task or changes the saved agent configuration.
func PersonalPersistentGoal(message string) bool {
	q := strings.ToLower(strings.TrimSpace(message))
	if q == "继续" || q == "继续解题" || q == "继续探索" {
		return true
	}
	for _, marker := range []string{"获取flag", "拿到flag", "持续解题", "不要停止直到", "持续执行直到", "get the flag", "keep going until"} {
		if strings.Contains(q, marker) {
			return true
		}
	}
	return false
}

func WithResponseLanguage(ctx context.Context, language string) context.Context {
	return context.WithValue(ctx, personalLanguageKey{}, language)
}

func WithPersonalReadOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, personalReadOnlyKey{}, true)
}

func personalReadOnly(ctx context.Context) bool {
	readOnly, _ := ctx.Value(personalReadOnlyKey{}).(bool)
	return readOnly
}

func personalReadOnlyTool(name string) bool {
	switch strings.ToLower(name) {
	case "research_recall", "graph_overview", "list_findings", "list_facts", "list_assets", "get_worker_output", "node_detail", "get_worker_trace", "search_all_worker_traces", "list_worker_traces", "list_goals", "traffic_search", "traffic_get", "traffic_blob", "get_finding_traffic", "expand_digest", "expand_index", "read", "ls", "glob", "grep":
		return true
	}
	return false
}

func responseLanguageBlock(ctx context.Context) string {
	language, _ := ctx.Value(personalLanguageKey{}).(string)
	if language == "" && TaskResponseLanguage != nil {
		language = TaskResponseLanguage(RunInfoFrom(ctx).TaskID)
	}
	if language == "" {
		return ""
	}
	name := "中文"
	if language == "en" {
		name = "English"
	}
	return "\n\n用户可见的进度、重要事件、待办和最终回复使用 " + name + "。工具输出、路径和证据原文保留原样。只在有实质进展时简短回显，不虚构完成比例或内部推理。"
}

func personalTools(ctx context.Context, key string, in []actool.CoreTool) []actool.CoreTool {
	readOnly := personalReadOnly(ctx)
	if key != "personal" && !readOnly {
		return in
	}
	out := make([]actool.CoreTool, 0, len(in))
	for _, t := range in {
		if key == "personal" && t.Name() == "spawn_task" {
			continue
		}
		if readOnly && !personalReadOnlyTool(t.Name()) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// NewSession adds its own tools AFTER AugmentTools, including WebFetch, Monitor,
// and deferred-tool dispatch. Disable those additions and install an execution
// guard as well, so a hallucinated call cannot bypass the visible tool schemas.
func personalSessionOptions(ctx context.Context, opts *agentcore.Options) {
	if !personalReadOnly(ctx) {
		return
	}
	opts.Tools = personalTools(ctx, "", opts.Tools) // non-nil even when empty: no SDK defaults
	opts.EnableWebFetch = false
	opts.EnableWebSearch = false
	opts.DisableBackgroundTasks = true
	opts.DeferredTools = nil
	opts.UnlockSet = nil
	opts.Skills = nil
	opts.Plan = nil
	opts.Memory = nil
	opts.Todos = nil
	opts.AskUser = nil
	opts.Hooks = &personalReadOnlyHooks{base: opts.Hooks}
}

type personalReadOnlyHooks struct{ base harness.HookRunner }

func (h *personalReadOnlyHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	if !personalReadOnlyTool(name) {
		return true, "The task is paused. Only existing local records may be read; use the explicit resume control before performing new operations.", nil
	}
	if h.base != nil {
		return h.base.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}

func (h *personalReadOnlyHooks) PostToolUse(ctx context.Context, name string, input, result []byte, isError bool) {
	if h.base != nil {
		h.base.PostToolUse(ctx, name, input, result, isError)
	}
}
func (h *personalReadOnlyHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	if h.base != nil {
		return h.base.Stop(ctx, messages)
	}
	return false, nil, ""
}
func (h *personalReadOnlyHooks) FireUserPromptSubmit(ctx context.Context, prompt string) string {
	if base, ok := h.base.(interface {
		FireUserPromptSubmit(context.Context, string) string
	}); ok {
		return base.FireUserPromptSubmit(ctx, prompt)
	}
	return ""
}
func (h *personalReadOnlyHooks) FireSessionStart(ctx context.Context) {
	if base, ok := h.base.(interface{ FireSessionStart(context.Context) }); ok {
		base.FireSessionStart(ctx)
	}
}
func (h *personalReadOnlyHooks) FireSessionEnd(ctx context.Context) {
	if base, ok := h.base.(interface{ FireSessionEnd(context.Context) }); ok {
		base.FireSessionEnd(ctx)
	}
}
