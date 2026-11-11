package agent

import (
	"context"
	"fmt"
	"iter"
	"sync/atomic"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// RunBudget is shared by a foreground turn and all of its delegated sessions.
// It counts logical provider requests, including compaction/continuation calls;
// provider-internal transport retries retain their existing separate limits.
type RunBudget struct {
	Limit  int64
	OnCall func(int64)
	calls  atomic.Int64
}

func (b *RunBudget) Calls() int64 { return b.calls.Load() }
func (b *RunBudget) reserve() error {
	for {
		used := b.calls.Load()
		if used >= b.Limit {
			return fmt.Errorf("本轮共享模型调用预算已用完（%d 次），请检查现有结果后继续", b.Limit)
		}
		if b.calls.CompareAndSwap(used, used+1) {
			if b.OnCall != nil {
				b.OnCall(used + 1)
			}
			return nil
		}
	}
}

type runBudgetKey struct{}

func WithRunBudget(ctx context.Context, b *RunBudget) context.Context {
	return context.WithValue(ctx, runBudgetKey{}, b)
}
func budgetProvider(ctx context.Context, provider llm.Provider) llm.Provider {
	if b, ok := ctx.Value(runBudgetKey{}).(*RunBudget); ok {
		return &runBudgetProvider{Provider: provider, budget: b}
	}
	return provider
}

type runBudgetProvider struct {
	llm.Provider
	budget *RunBudget
}

func (p *runBudgetProvider) Complete(ctx context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if err := ctx.Err(); err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	if err := p.budget.reserve(); err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	return p.Provider.Complete(ctx, r)
}
func (p *runBudgetProvider) Stream(ctx context.Context, r llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		if err := p.budget.reserve(); err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		for event, err := range p.Provider.Stream(ctx, r) {
			if !yield(event, err) {
				return
			}
		}
	}
}

type completionCheckHooks struct {
	harness.HookRunner
	check func() string
}

func (h *completionCheckHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	if ctx.Err() == nil {
		if pending := h.check(); pending != "" {
			return false, []string{pending}, ""
		}
	}
	if h.HookRunner != nil {
		return h.HookRunner.Stop(ctx, messages)
	}
	return false, nil, ""
}
func (h *completionCheckHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	if h.HookRunner != nil {
		return h.HookRunner.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}
func (h *completionCheckHooks) PostToolUse(ctx context.Context, name string, input, output []byte, failed bool) {
	if h.HookRunner != nil {
		h.HookRunner.PostToolUse(ctx, name, input, output, failed)
	}
}
func (h *completionCheckHooks) FireUserPromptSubmit(ctx context.Context, prompt string) string {
	if hook, ok := h.HookRunner.(interface {
		FireUserPromptSubmit(context.Context, string) string
	}); ok {
		return hook.FireUserPromptSubmit(ctx, prompt)
	}
	return ""
}
func (h *completionCheckHooks) FireSessionStart(ctx context.Context) {
	if hook, ok := h.HookRunner.(interface{ FireSessionStart(context.Context) }); ok {
		hook.FireSessionStart(ctx)
	}
}
func (h *completionCheckHooks) FireSessionEnd(ctx context.Context) {
	if hook, ok := h.HookRunner.(interface{ FireSessionEnd(context.Context) }); ok {
		hook.FireSessionEnd(ctx)
	}
}
