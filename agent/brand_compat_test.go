package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestBrandProviderEnvironmentCompatibility(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "local-fixture-key")
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, suffix := range []string{"PROVIDER", "MODEL", "BASE_URL", "PROXY", "STREAM"} {
		name := "prtseek_LLM_" + suffix
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ARTEX_LLM_"+suffix, "")
	}
	t.Setenv("ARTEX_LLM_PROVIDER", "openai")
	t.Setenv("ARTEX_LLM_MODEL", "legacy-model")
	t.Setenv("ARTEX_LLM_BASE_URL", "http://legacy.invalid")
	t.Setenv("ARTEX_LLM_STREAM", "false")
	legacy, ok := FromEnv()
	if !ok || legacy.Model != "legacy-model" || legacy.Format != llm.FormatOpenAI || legacy.Stream {
		t.Fatalf("legacy provider config=%+v ok=%v", legacy, ok)
	}
	t.Setenv("prtseek_LLM_PROVIDER", "openai-responses")
	t.Setenv("prtseek_LLM_MODEL", "preferred-model")
	t.Setenv("prtseek_LLM_BASE_URL", "http://preferred.invalid")
	t.Setenv("prtseek_LLM_STREAM", "true")
	preferred, ok := FromEnv()
	if !ok || preferred.Model != "preferred-model" || preferred.BaseURL != "http://preferred.invalid" || preferred.Format != llm.FormatOpenAIResponses || !preferred.Stream {
		t.Fatalf("preferred provider config=%+v ok=%v", preferred, ok)
	}
}

func TestBrandPersistedReasoningCompatibility(t *testing.T) {
	const text = "Check both files."
	item := json.RawMessage(responseReasoningFixture)
	signature := flashEncodeReasoning(text, []json.RawMessage{item})
	if !strings.HasPrefix(signature, "prtseek:") {
		t.Fatal("new reasoning state did not use the current brand")
	}
	for _, stored := range []string{signature, strings.Replace(signature, "prtseek:", "artex:", 1)} {
		block := llm.ContentBlock{Type: llm.BlockThinking, Thinking: text, Signature: stored}
		items, err := flashReplayReasoning(block)
		if err != nil || len(items) != 1 || string(items[0]) != string(item) {
			t.Fatalf("persisted reasoning did not survive rename: %v", err)
		}
		block.Thinking = "changed text"
		if _, err := flashReplayReasoning(block); err == nil {
			t.Fatal("legacy compatibility bypassed reasoning integrity")
		}
	}
}

func TestBrandWorkerMarkerCompatibility(t *testing.T) {
	const requestID = "request-before-rename"
	if !strings.Contains(workerChatMarker(requestID), "prtseek_WORKER_CHAT") {
		t.Fatal("new worker turn retained the old marker")
	}
	for _, marker := range []string{workerChatMarker(requestID), "<!-- ARTEX_WORKER_CHAT:" + requestID + " -->"} {
		if !hasWorkerChatMessage([]llm.Message{llm.UserText(marker)}, requestID) {
			t.Fatal("saved user turn no longer recognized after rename")
		}
		assistant := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(marker)}}
		if hasWorkerChatMessage([]llm.Message{assistant}, requestID) {
			t.Fatal("non-user marker incorrectly suppressed new input")
		}
	}
}
