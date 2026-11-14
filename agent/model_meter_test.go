package agent

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type usageFixtureProvider struct{ llm.Provider }

func (*usageFixtureProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{}, "", llm.Usage{InputTokens: 17, OutputTokens: 3}, errors.New("fixture failure after usage")
}
func (*usageFixtureProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for _, u := range []llm.Usage{{InputTokens: 100, CacheReadTokens: 80}, {OutputTokens: 4}, {InputTokens: 100, OutputTokens: 9, CacheReadTokens: 80}, {InputTokens: 100, OutputTokens: 9, CacheReadTokens: 80}} {
			if !yield(llm.StreamEvent{Usage: u}, nil) {
				return
			}
		}
		yield(llm.StreamEvent{}, errors.New("fixture stream interrupted"))
	}
}
func TestModelMeterPartialSnapshotsAndBudget(t *testing.T) {
	calls := 0
	var total llm.Usage
	ctx := WithModelObserver(t.Context(), &ModelObserver{Started: func() { calls++ }, Usage: func(u llm.Usage) { total.Add(u) }})
	p := budgetProvider(WithRunBudget(ctx, &RunBudget{Limit: 2}), meterProvider(ctx, &usageFixtureProvider{}))
	for range p.Stream(ctx, llm.CompletionRequest{}) {
	}
	_, _, _, _ = p.Complete(ctx, llm.CompletionRequest{})
	_, _, _, err := p.Complete(ctx, llm.CompletionRequest{})
	if err == nil || calls != 2 || total != (llm.Usage{InputTokens: 117, OutputTokens: 12, CacheReadTokens: 80}) {
		t.Fatalf("calls=%d total=%+v err=%v", calls, total, err)
	}
}
func TestModelMeterConsumerStopsEarly(t *testing.T) {
	var total llm.Usage
	ctx := WithModelObserver(t.Context(), &ModelObserver{Started: func() {}, Usage: func(u llm.Usage) { total.Add(u) }})
	for range meterProvider(ctx, &usageFixtureProvider{}).Stream(ctx, llm.CompletionRequest{}) {
		break
	}
	if total.InputTokens != 100 || total.CacheReadTokens != 80 {
		t.Fatalf("lost partial usage: %+v", total)
	}
}

type truncatedStreamProvider struct{ llm.Provider }

func (*truncatedStreamProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial"}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: llm.Usage{InputTokens: 21, OutputTokens: 2}}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil) // SDK synthetic EOF marker.
	}
}
func TestModelMeterRejectsSyntheticStopWithoutFinishReason(t *testing.T) {
	var total llm.Usage
	var terminalErr error
	ctx := WithModelObserver(t.Context(), &ModelObserver{Started: func() {}, Usage: func(u llm.Usage) { total.Add(u) }})
	for _, err := range meterProvider(ctx, &truncatedStreamProvider{}).Stream(ctx, llm.CompletionRequest{}) {
		if err != nil {
			terminalErr = err
		}
	}
	if !errors.Is(terminalErr, ErrIncompleteRun) || total.InputTokens != 21 || total.OutputTokens != 2 {
		t.Fatalf("err=%v usage=%+v", terminalErr, total)
	}
}
