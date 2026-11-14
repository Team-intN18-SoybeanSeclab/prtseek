package agent

import (
	"context"
	"iter"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// ModelObserver belongs to one Chat turn. Children must install their own
// observer even though they inherit the parent's shared RunBudget.
type ModelObserver struct {
	Started  func()
	Usage    func(llm.Usage)
	Terminal func(harness.TerminalReason)
}
type modelObserverKey struct{}

func WithModelObserver(ctx context.Context, observer *ModelObserver) context.Context {
	return context.WithValue(ctx, modelObserverKey{}, observer)
}
func meterProvider(ctx context.Context, provider llm.Provider) llm.Provider {
	if observer, ok := ctx.Value(modelObserverKey{}).(*ModelObserver); ok {
		meter := &modelMeterProvider{Provider: provider, observer: observer}
		if source, ok := provider.(interface{ ResearchModel() string }); ok {
			return &modelMeterResearchProvider{modelMeterProvider: meter, source: source}
		}
		return meter
	}
	return provider
}

type modelMeterResearchProvider struct {
	*modelMeterProvider
	source interface{ ResearchModel() string }
}

func (p *modelMeterResearchProvider) ResearchModel() string { return p.source.ResearchModel() }

type modelMeterProvider struct {
	llm.Provider
	observer *ModelObserver
}

func (p *modelMeterProvider) Complete(ctx context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.observer.Started()
	m, stop, usage, err := p.Provider.Complete(ctx, r)
	p.observer.Usage(usage)
	if err == nil && stop == "" {
		err = ErrIncompleteRun
	}
	return m, stop, usage, err
}
func usageDelta(previous *llm.Usage, current llm.Usage) llm.Usage {
	next := llm.Usage{InputTokens: max(previous.InputTokens, current.InputTokens), OutputTokens: max(previous.OutputTokens, current.OutputTokens), CacheReadTokens: max(previous.CacheReadTokens, current.CacheReadTokens), CacheWriteTokens: max(previous.CacheWriteTokens, current.CacheWriteTokens)}
	delta := llm.Usage{InputTokens: next.InputTokens - previous.InputTokens, OutputTokens: next.OutputTokens - previous.OutputTokens, CacheReadTokens: next.CacheReadTokens - previous.CacheReadTokens, CacheWriteTokens: next.CacheWriteTokens - previous.CacheWriteTokens}
	*previous = next
	return delta
}
func (p *modelMeterProvider) Stream(ctx context.Context, r llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.observer.Started()
		var received llm.Usage
		finished, failed := false, false
		for event, err := range p.Provider.Stream(ctx, r) {
			finished = finished || event.StopReason != ""
			failed = failed || err != nil
			if delta := usageDelta(&received, event.Usage); delta != (llm.Usage{}) {
				p.observer.Usage(delta) // Persist before yielding, including interrupted streams.
			}
			if !yield(event, err) {
				return
			}
		}
		// Some adapters synthesize message_stop at EOF. Require the provider's
		// actual finish reason so a truncated SSE body is never a completed turn.
		if !finished && !failed {
			err := ErrIncompleteRun
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			yield(llm.StreamEvent{}, err)
		}
	}
}
