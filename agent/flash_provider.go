package agent

// DeepSeek-specific integrity belongs in the host, not a writable module cache.
// The generic SDK remains responsible for HTTP retries and wire serialization.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"iter"
	"strings"
	"sync"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

// FlashCapabilities distinguishes API limits from our local working-set choice.
// WorkingContextTokens is an engineering default, not the model's capacity.
type FlashCapabilities struct {
	WorkingContextTokens int      `json:"working_context_tokens"`
	MaxOutputTokens      int      `json:"max_output_tokens"`
	DefaultThinking      bool     `json:"default_thinking"`
	ReasoningEfforts     []string `json:"reasoning_efforts"`
	Source               string   `json:"source"`
}

// IsDeepSeekFlash deliberately recognizes exact documented aliases, not every
// model containing "deepseek". A vendor prefix is accepted for routed profiles.
func IsDeepSeekFlash(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "deepseek/")
	switch model {
	case "deepseek-flash", "deepseek-v4.1-flash-1017", "deepseek-v4-flash":
		return true
	}
	return false
}

func FlashModelCapabilities(model string) (FlashCapabilities, bool) {
	if !IsDeepSeekFlash(model) {
		return FlashCapabilities{}, false
	}
	return FlashCapabilities{
		WorkingContextTokens: 128000,
		MaxOutputTokens:      393216,
		DefaultThinking:      true,
		ReasoningEfforts:     []string{"low", "high", "max"},
		Source:               "https://api-docs.deepseek.com/api/create-chat-completion",
	}, true
}

// FlashProtocolError is deterministic. Replaying the same malformed history or
// accepting partially generated tool arguments cannot repair it.
type FlashProtocolError struct {
	Code   string
	Detail string
}

func (e *FlashProtocolError) Error() string {
	return "DeepSeek 会话完整性错误 [" + e.Code + "]: " + e.Detail
}

func flashError(code, detail string) error { return &FlashProtocolError{Code: code, Detail: detail} }

type flashProvider struct {
	base llm.Provider
	cfg  Config
	mu   sync.Mutex
	// Recovery is only from an authentic response of THIS provider and session.
	// No fabricated placeholder is ever substituted for a missing reasoning turn.
	cache      map[string]string
	cacheOrder []string
	cacheBytes int
}

func newFlashProvider(base llm.Provider, cfg Config) *flashProvider {
	return &flashProvider{base: base, cfg: cfg, cache: make(map[string]string)}
}

func (p *flashProvider) thinking(req llm.CompletionRequest) bool {
	mode := p.cfg.ThinkingType
	if req.Thinking != "" {
		mode = req.Thinking
	}
	return mode != "disabled" // official Flash default is enabled
}

func flashReasoning(m llm.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		if block.Type == llm.BlockThinking {
			b.WriteString(block.Thinking)
		}
	}
	value := b.String()
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) == "(reasoning elided by context compaction)" {
		return "" // An old SDK placeholder is not authentic model reasoning.
	}
	return value
}

func flashIdentity(ctx context.Context, m llm.Message) string {
	sid := transcript.SessionIDFrom(ctx)
	if sid == "" {
		return "" // Never share an anonymous recovery bucket across conversations.
	}
	clean := llm.Message{Role: m.Role}
	for _, b := range m.Content {
		if b.Type != llm.BlockThinking {
			clean.Content = append(clean.Content, b)
		}
	}
	data, _ := json.Marshal(clean)
	hash := sha256.Sum256(data)
	return sid + ":" + hex.EncodeToString(hash[:])
}

func (p *flashProvider) remember(ctx context.Context, m llm.Message) {
	if len(m.ToolUses()) == 0 {
		return
	}
	key, reasoning := flashIdentity(ctx, m), flashReasoning(m)
	if key == "" || reasoning == "" || len(reasoning) > 2*1024*1024 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.cache[key]; exists {
		return
	}
	p.cache[key] = reasoning
	p.cacheOrder = append(p.cacheOrder, key)
	p.cacheBytes += len(reasoning)
	for len(p.cacheOrder) > 256 || p.cacheBytes > 16*1024*1024 {
		old := p.cacheOrder[0]
		p.cacheOrder = p.cacheOrder[1:]
		p.cacheBytes -= len(p.cache[old])
		delete(p.cache, old)
	}
}

// prepare keeps complete tool exchanges intact before Norma can silently drop
// half pairs. It clones the outer structures because history belongs to the host.
func (p *flashProvider) prepare(ctx context.Context, req llm.CompletionRequest) (llm.CompletionRequest, error) {
	if req.MaxTokens < 0 || req.MaxTokens > 393216 {
		return req, flashError("output_budget", "max_tokens 必须为 1..393216，或 0 使用模型默认值")
	}
	if p.thinking(req) {
		req.Temperature = nil // documented no-op in thinking mode; avoid false control
	}
	req.Messages = append([]llm.Message(nil), req.Messages...)
	pending := map[string]bool{}
	seen := map[string]bool{}
	for i, m := range req.Messages {
		if m.Role == llm.RoleAssistant {
			if len(pending) != 0 {
				return req, flashError("unanswered_tool", "历史工具调用缺少结果；请从完整检查点恢复，不要重复执行已有副作用")
			}
			uses := m.ToolUses()
			for _, block := range m.Content {
				if block.Type == llm.BlockThinking && strings.TrimSpace(block.Thinking) == "(reasoning elided by context compaction)" {
					return req, flashError("synthetic_reasoning", "历史含旧 SDK 生成的思考占位，不能作为原始模型思考回传")
				}
			}
			for _, call := range uses {
				if err := validateFlashTool(call); err != nil {
					return req, err
				}
				if seen[call.ID] {
					return req, flashError("duplicate_tool_id", "历史中有重复的工具调用 ID")
				}
				pending[call.ID], seen[call.ID] = true, true
			}
			if len(uses) > 0 && len(req.Tools) > 0 && p.thinking(req) && flashReasoning(m) == "" {
				key := flashIdentity(ctx, m)
				p.mu.Lock()
				reasoning := p.cache[key]
				p.mu.Unlock()
				if reasoning == "" {
					// The official API may legitimately return zero reasoning text,
					// even in enabled/high mode. Missing text alone cannot prove loss.
					continue
				}
				blocks := make([]llm.ContentBlock, 0, len(m.Content)+1)
				blocks = append(blocks, llm.ContentBlock{Type: llm.BlockThinking, Thinking: reasoning})
				req.Messages[i].Content = append(blocks, m.Content...)
			}
			continue
		}
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				if !pending[b.ToolUseID] {
					return req, flashError("orphan_tool_result", "工具结果没有对应的待完成调用，或被重复写入")
				}
				delete(pending, b.ToolUseID)
			}
		}
	}
	if len(pending) != 0 {
		return req, flashError("unanswered_tool", "请求中包含没有结果的工具调用；请先恢复原工具状态")
	}
	return req, nil
}

func validateFlashTool(b llm.ContentBlock) error {
	if b.ID == "" || b.Name == "" {
		return flashError("incomplete_tool", "工具调用缺少 ID 或名称")
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(b.Input, &args); err != nil || args == nil {
		return flashError("invalid_arguments", "工具参数必须是完整 JSON 对象，拒绝替换为默认空参数")
	}
	return nil
}

func validateFlashResponse(msg llm.Message, stop string) error {
	uses := msg.ToolUses()
	if len(uses) == 0 {
		return nil
	}
	if stop != "tool_use" && stop != "end_turn" {
		return flashError("truncated_tool", "模型工具回复未完整结束（"+stop+"）；本次工具没有执行")
	}
	seen := map[string]bool{}
	for _, call := range uses {
		if err := validateFlashTool(call); err != nil {
			return err
		}
		if seen[call.ID] {
			return flashError("duplicate_tool_id", "同一回复包含重复工具调用 ID")
		}
		seen[call.ID] = true
	}
	return nil
}

func (p *flashProvider) validateResponse(req llm.CompletionRequest, msg llm.Message, stop string) error {
	if err := validateFlashResponse(msg, stop); err != nil {
		return err
	}
	for _, block := range msg.Content {
		if block.Type == llm.BlockThinking && strings.TrimSpace(block.Thinking) == "(reasoning elided by context compaction)" {
			return flashError("synthetic_reasoning", "工具回复包含思考占位文本；本次工具未执行")
		}
	}
	return nil
}

func (p *flashProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	req, err := p.prepare(ctx, req)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	msg, stop, usage, err := p.base.Complete(ctx, req)
	if err == nil {
		err = p.validateResponse(req, msg, stop)
	}
	if err != nil {
		return llm.Message{}, stop, usage, err
	}
	p.remember(ctx, msg)
	return msg, stop, usage, nil
}

func (p *flashProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		req, err := p.prepare(ctx, req)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		acc := llm.NewAccumulator()
		// Buffer executable events until the entire response is valid. Reasoning
		// and text remain live, while a dropped stream cannot trigger a tool.
		var tools, terminal []llm.StreamEvent
		for ev, err := range p.base.Stream(ctx, req) {
			if err != nil {
				yield(llm.StreamEvent{}, err)
				return
			}
			acc.Add(ev)
			switch ev.Type {
			case llm.SEToolUseStart, llm.SEToolInputJSON:
				tools = append(tools, ev)
			case llm.SEMessageDelta, llm.SEMessageStop:
				terminal = append(terminal, ev)
			default:
				if !yield(ev, nil) {
					return
				}
			}
		}
		msg := acc.Message()
		if acc.StopReason == "" {
			yield(llm.StreamEvent{}, flashError("incomplete_stream", "SSE 缺少 finish_reason；未执行任何缓冲的工具调用"))
			return
		}
		if err := p.validateResponse(req, msg, acc.StopReason); err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		p.remember(ctx, msg)
		for _, ev := range append(tools, terminal...) {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

var _ llm.Provider = (*flashProvider)(nil)
var _ error = (*FlashProtocolError)(nil)
