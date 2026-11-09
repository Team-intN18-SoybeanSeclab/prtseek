package agent

import (
	"fmt"

	"github.com/Autumn-27/norma/agentcore"
)

// SessionResumeError stops a run before its first model or tool call when its
// persisted conversation cannot be read. Retrying the whole intent must not
// silently turn that conversation into a fresh run and repeat prior effects.
type SessionResumeError struct {
	SessionID string
	Cause     error
}

func (e *SessionResumeError) Error() string {
	return fmt.Sprintf("会话 %q 的历史记录读取失败；本轮已停止，未调用模型或执行工具。请检查历史文件后再继续。", e.SessionID)
}

func (e *SessionResumeError) Unwrap() error { return e.Cause }

func resumeSession(s *agentcore.Session, sessionID string) error {
	if err := s.Resume(sessionID); err != nil {
		return &SessionResumeError{SessionID: sessionID, Cause: err}
	}
	// The SDK already treats an absent transcript as a new session. Other read
	// errors, including a corrupt JSONL line, must reach the caller unchanged.
	return nil
}
