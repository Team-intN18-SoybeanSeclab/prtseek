package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
)

func TestReserveConversationTurnInterruptsAndWaits(t *testing.T) {
	s := &Server{chatBusy: map[string]bool{"conv-1": true}, chatCancel: map[string]context.CancelCauseFunc{}}
	previous, stop := context.WithCancelCause(context.Background())
	s.chatCancel["conv-1"] = stop
	finished := make(chan struct{})
	go func() {
		<-previous.Done()
		time.Sleep(75 * time.Millisecond)
		s.chatMu.Lock()
		delete(s.chatBusy, "conv-1")
		delete(s.chatCancel, "conv-1")
		s.chatMu.Unlock()
		close(finished)
	}()
	if err := s.reserveConversationTurn(t.Context(), "conv-1", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("new turn started before previous turn exited")
	}
	if context.Cause(previous) != agent.AbortChatStoppedByUser {
		t.Fatalf("previous turn cause: %v", context.Cause(previous))
	}
	if !s.chatBusy["conv-1"] {
		t.Fatal("new turn did not reserve the conversation")
	}
}

func TestReserveConversationTurnDoesNotInterruptWithoutRequest(t *testing.T) {
	s := &Server{chatBusy: map[string]bool{"conv-1": true}, chatCancel: map[string]context.CancelCauseFunc{}}
	previous, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	s.chatCancel["conv-1"] = stop
	if err := s.reserveConversationTurn(t.Context(), "conv-1", false); err == nil {
		t.Fatal("accepted a second turn without interrupt")
	}
	if previous.Err() != nil {
		t.Fatal("non-interrupting request cancelled the previous turn")
	}
}

func TestReserveConversationTurnTimeoutKeepsBusySlot(t *testing.T) {
	s := &Server{chatBusy: map[string]bool{"conv-1": true}, chatCancel: map[string]context.CancelCauseFunc{}}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	if err := s.reserveConversationTurn(ctx, "conv-1", true); err == nil || !strings.Contains(err.Error(), "尚未发送") {
		t.Fatalf("missing timeout feedback: %v", err)
	}
	if !s.chatBusy["conv-1"] {
		t.Fatal("timed-out interrupt stole the previous turn's slot")
	}
}
