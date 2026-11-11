package agent

import (
	"context"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/sidequestion"

	"github.com/Autumn-27/norma/llm"
)

type budgetTestProvider struct {
	llm.Provider
	calls atomic.Int64
}

func (p *budgetTestProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls.Add(1)
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("fixture result")}}, "stop", llm.Usage{}, nil
}

func TestCTFModelBudgetSurvivesSideCapture(t *testing.T) {
	base := &budgetTestProvider{}
	budget := &RunBudget{Limit: 1}
	ctx := WithRunBudget(sidequestion.WithPublisher(t.Context(), func(sidequestion.Snapshot) {}), budget)
	chat := NewChatAgent(base, "fixture", t.TempDir(), nil, 128000)
	chat.SetNonStreaming(func() bool { return true })
	if _, err := chat.Chat(ctx, "solver", "conv-1", "Summarize the supplied fixture.", 2, 5*time.Second, false, nil); err != nil {
		t.Fatal(err)
	}
	if budget.Calls() != 1 || base.calls.Load() != 1 {
		t.Fatalf("side capture bypassed shared budget: counted=%d actual=%d", budget.Calls(), base.calls.Load())
	}
}
func (p *budgetTestProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { p.calls.Add(1); yield(llm.StreamEvent{}, nil) }
}
func TestCTFSharedModelBudgetCapsConcurrentRequests(t *testing.T) {
	base := &budgetTestProvider{}
	budget := &RunBudget{Limit: 3}
	ctx := WithRunBudget(t.Context(), budget)
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := budgetProvider(ctx, base)
			if i%2 == 0 {
				_, _, _, _ = p.Complete(ctx, llm.CompletionRequest{})
			} else {
				for range p.Stream(ctx, llm.CompletionRequest{}) {
				}
			}
		}()
	}
	wg.Wait()
	if budget.Calls() != 3 || base.calls.Load() != 3 {
		t.Fatalf("budget=%d provider=%d", budget.Calls(), base.calls.Load())
	}
}
