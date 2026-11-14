package agent

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

func TestCaptureIncompleteAndCancelledStreams(t *testing.T) {
	for _, tc := range []struct {
		name             string
		cancel, terminal bool
	}{{"missing", false, false}, {"cancelled", true, false}, {"completed", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx, trace := intercept.WithTrace(ctx, "fixture", nil)
			defer trace.Finish()
			var rows []db.Activity
			events := func(yield func(harness.Event, error) bool) {
				yield(harness.Event{Kind: harness.KindText, Text: "partial response"}, nil)
				yield(harness.Event{Kind: harness.KindUsage, Usage: &llm.Usage{InputTokens: 31, OutputTokens: 7}}, nil)
				if tc.cancel {
					cancel()
				}
				if tc.terminal {
					yield(harness.Event{Kind: harness.KindResult, Terminal: &harness.Terminal{Reason: harness.ReasonCompleted, Text: "partial response", Usage: llm.Usage{InputTokens: 31, OutputTokens: 7}}}, nil)
				} else {
					yield(harness.Event{Kind: harness.KindResult}, nil)
				}
			}
			_, reason, err := captureEvents(ctx, iter.Seq2[harness.Event, error](events), func(a db.Activity) { rows = append(rows, a) }, trace)
			if tc.terminal {
				if err != nil || reason != harness.ReasonCompleted {
					t.Fatalf("%s %v", reason, err)
				}
			} else if tc.cancel {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrIncompleteRun) {
				t.Fatal(err)
			}
			last := rows[len(rows)-1]
			if last.Kind != "result" || last.InputTokens == nil || *last.InputTokens != 31 || *last.OutputTokens != 7 || last.IsError == tc.terminal {
				t.Fatalf("bad terminal row: %+v", last)
			}
		})
	}
}
