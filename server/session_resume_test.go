package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/norma/harness"
)

func TestSessionResumeWorkerDoesNotReplay(t *testing.T) {
	cause := errors.New("transcript: corrupt line")
	resumeErr := &agent.SessionResumeError{SessionID: "prior-intent", Cause: cause}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"resume", resumeErr, false},
		{"wrapped_resume", fmt.Errorf("worker failed: %w", resumeErr), false},
		{"temporary_network", errors.New("temporary network failure"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableWorkerModelError(harness.ReasonModelError, tc.err); got != tc.want {
				t.Fatalf("worker retry=%v want=%v", got, tc.want)
			}
		})
	}
	if !errors.Is(fmt.Errorf("worker failed: %w", resumeErr), cause) {
		t.Fatal("original history read failure was lost")
	}
}
