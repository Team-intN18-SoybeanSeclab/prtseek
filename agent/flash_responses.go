package agent

// DeepSeek Responses is stateless. Its reasoning.content must be replayed;
// the generic SDK currently drops that content and cannot preserve item state.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
)

const flashResponsesLimit = 32 * 1024 * 1024
const flashResponsesSignature = "prtseek:deepseek-responses:v1:"
const legacyFlashResponsesSignature = "artex:deepseek-responses:v1:"

type flashResponsesProvider struct {
	cfg       Config
	client    *http.Client
	integrity *flashProvider
}

func newFlashResponsesProvider(cfg Config, client *http.Client) (*flashResponsesProvider, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.deepseek.com"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid DeepSeek Responses base URL")
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	return &flashResponsesProvider{cfg: cfg, client: client, integrity: newFlashProvider(nil, cfg)}, nil
}

type flashResponsePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type flashResponseItem struct {
	Type      string              `json:"type"`
	ID        string              `json:"id,omitempty"`
	Status    string              `json:"status,omitempty"`
	Role      string              `json:"role,omitempty"`
	Content   []flashResponsePart `json:"content,omitempty"`
	CallID    string              `json:"call_id,omitempty"`
	Name      string              `json:"name,omitempty"`
	Arguments string              `json:"arguments,omitempty"`
}
type flashResponseEnvelope struct {
	Status     string            `json:"status"`
	Output     []json.RawMessage `json:"output"`
	Error      json.RawMessage   `json:"error"`
	Incomplete *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage struct {
		Input   int `json:"input_tokens"`
		Output  int `json:"output_tokens"`
		Details struct {
			Cached int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}
type flashReasoningState struct {
	Hash  string            `json:"sha256"`
	Items []json.RawMessage `json:"items"`
}

func flashTextHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func flashEncodeReasoning(text string, items []json.RawMessage) string {
	data, _ := json.Marshal(flashReasoningState{Hash: flashTextHash(text), Items: items})
	return flashResponsesSignature + base64.RawStdEncoding.EncodeToString(data)
}
func flashReplayReasoning(b llm.ContentBlock) ([]json.RawMessage, error) {
	encoded, known := strings.CutPrefix(b.Signature, flashResponsesSignature)
	if !known {
		encoded, known = strings.CutPrefix(b.Signature, legacyFlashResponsesSignature)
	}
	if known {
		data, err := base64.RawStdEncoding.DecodeString(encoded)
		var state flashReasoningState
		if err != nil || len(data) > flashResponsesLimit || json.Unmarshal(data, &state) != nil || state.Hash != flashTextHash(b.Thinking) {
			return nil, flashError("reasoning_state", "Responses 思考原文与保存的协议状态不匹配")
		}
		var text strings.Builder
		for _, raw := range state.Items {
			var item flashResponseItem
			if json.Unmarshal(raw, &item) != nil || item.Type != "reasoning" {
				return nil, flashError("reasoning_state", "Responses 思考状态包含无效 item")
			}
			for _, part := range item.Content {
				if part.Type == "reasoning_text" {
					text.WriteString(part.Text)
				}
			}
		}
		if text.String() != b.Thinking {
			return nil, flashError("reasoning_state", "Responses 保存的 item 内容与原始思考不一致")
		}
		return state.Items, nil
	}
	if b.Thinking == "" {
		return nil, nil
	}
	raw, _ := json.Marshal(flashResponseItem{Type: "reasoning", Content: []flashResponsePart{{Type: "reasoning_text", Text: b.Thinking}}})
	return []json.RawMessage{raw}, nil
}

func (p *flashResponsesProvider) request(ctx context.Context, req llm.CompletionRequest, stream bool) ([]byte, llm.CompletionRequest, error) {
	mode := p.cfg.ThinkingType
	if req.Thinking != "" {
		mode = req.Thinking
	}
	effort := p.cfg.ReasoningEffort
	if mode == "disabled" {
		effort = "none"
	} else if mode == "enabled" && (effort == "" || effort == "none") {
		effort = "high"
	}
	if effort == "none" {
		req.Thinking = "disabled"
	}
	req, err := p.integrity.prepare(ctx, req)
	if err != nil {
		return nil, req, err
	}
	input := []json.RawMessage{}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			var value any
			switch b.Type {
			case llm.BlockThinking:
				items, err := flashReplayReasoning(b)
				if err != nil {
					return nil, req, err
				}
				input = append(input, items...)
				continue
			case llm.BlockText:
				if b.Text == "" {
					continue
				}
				value = map[string]any{"role": m.Role, "content": b.Text}
			case llm.BlockToolUse:
				value = map[string]any{"type": "function_call", "call_id": b.ID, "name": b.Name, "arguments": string(b.Input)}
			case llm.BlockToolResult:
				var text strings.Builder
				for _, part := range b.Content {
					if part.Type == llm.BlockText {
						text.WriteString(part.Text)
					} else {
						return nil, req, flashError("unsupported_content", "当前 Responses 适配器无法编码该工具结果内容；附件未发送，请使用支持该类型的输入通道")
					}
				}
				value = map[string]any{"type": "function_call_output", "call_id": b.ToolUseID, "output": text.String()}
			default:
				return nil, req, flashError("unsupported_content", "当前 Responses 适配器无法编码该消息内容；附件未发送，请使用支持该类型的输入通道")
			}
			raw, _ := json.Marshal(value)
			input = append(input, raw)
		}
	}
	body := map[string]any{"model": p.cfg.Model, "input": input, "stream": stream, "store": false}
	if len(req.System) > 0 {
		body["instructions"] = strings.Join(req.System, "\n\n")
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if effort != "" {
		body["reasoning"] = map[string]string{"effort": effort}
	}
	if len(req.Tools) > 0 {
		var list []any
		for _, t := range req.Tools {
			list = append(list, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.InputSchema})
		}
		body["tools"] = list
	}
	data, err := json.Marshal(body)
	return data, req, err
}

// Reuse the host HTTP stack: every attempt retains capture, session routing,
// context cancellation, timeout policy and the shared per-profile limiter.
func (p *flashResponsesProvider) do(ctx context.Context, data []byte, stream bool) (*http.Response, error) {
	retries := p.cfg.Retry.ConnectAttempts
	if retries == 0 {
		retries = 3
	}
	if retries < 0 {
		retries = 0
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.BaseURL, "/")+"/responses", bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		} else {
			req.Header.Set("Accept", "application/json")
		}
		resp, err := p.client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		if ctx.Err() != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return nil, ctx.Err()
		}
		retry := err != nil
		if resp != nil {
			switch resp.StatusCode {
			case 408, 429, 500, 502, 503, 504:
				retry = true
			}
		}
		if attempt >= retries || !retry {
			if err != nil {
				return nil, err
			}
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			var envelope struct {
				Error json.RawMessage `json:"error"`
			}
			_ = json.Unmarshal(raw, &envelope)
			remote := flashRemoteError(envelope.Error).(*FlashRemoteError)
			remote.Status = resp.StatusCode
			if resp.StatusCode == 402 {
				remote.Code = "insufficient_quota"
			}
			return nil, remote
		}
		if resp != nil {
			resp.Body.Close()
		}
		delay := p.cfg.Retry.ConnectInterval
		if delay <= 0 {
			delay = 500 * time.Millisecond * time.Duration(1<<min(attempt, 4))
			if delay > 8*time.Second {
				delay = 8 * time.Second
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func decodeFlashResponse(raw []byte) (llm.Message, string, llm.Usage, error) {
	var r flashResponseEnvelope
	if json.Unmarshal(raw, &r) != nil {
		return llm.Message{}, "", llm.Usage{}, flashError("invalid_response", "Responses JSON 无效")
	}
	usage := llm.Usage{InputTokens: r.Usage.Input, OutputTokens: r.Usage.Output, CacheReadTokens: r.Usage.Details.Cached}
	if len(r.Error) > 0 && string(r.Error) != "null" {
		return llm.Message{}, "", usage, flashRemoteError(r.Error)
	}
	stop := "end_turn"
	switch r.Status {
	case "completed":
	case "incomplete":
		stop = "max_tokens"
	default:
		return llm.Message{}, "", usage, flashError("incomplete_response", "Responses 缺少有效终态")
	}
	msg := llm.Message{Role: llm.RoleAssistant}
	for _, rawItem := range r.Output {
		var item flashResponseItem
		if json.Unmarshal(rawItem, &item) != nil {
			return llm.Message{}, stop, usage, flashError("invalid_item", "Responses output item 无效")
		}
		switch item.Type {
		case "reasoning":
			var text strings.Builder
			for _, part := range item.Content {
				if part.Type == "reasoning_text" {
					text.WriteString(part.Text)
				}
			}
			msg.Content = append(msg.Content, llm.ContentBlock{Type: llm.BlockThinking, Thinking: text.String(), Signature: flashEncodeReasoning(text.String(), []json.RawMessage{rawItem})})
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					msg.Content = append(msg.Content, llm.TextBlock(part.Text))
				}
			}
		case "function_call":
			if item.Status != "" && item.Status != "completed" {
				return llm.Message{}, stop, usage, flashError("truncated_tool", "Responses 工具 item 未完成")
			}
			msg.Content = append(msg.Content, llm.ContentBlock{Type: llm.BlockToolUse, ID: item.CallID, Name: item.Name, Input: json.RawMessage(item.Arguments)})
		case "custom_tool_call", "tool_search_call":
			return llm.Message{}, stop, usage, flashError("unsupported_tool", "Responses 返回了本 agent 未声明的工具类型")
		}
	}
	if r.Status == "completed" && len(msg.ToolUses()) > 0 {
		stop = "tool_use"
	}
	if err := validateFlashResponse(msg, stop); err != nil {
		return llm.Message{}, stop, usage, err
	}
	return msg, stop, usage, nil
}

func (p *flashResponsesProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	data, req, err := p.request(ctx, req, false)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	resp, err := p.do(ctx, data, false)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, flashResponsesLimit+1))
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	if len(raw) > flashResponsesLimit {
		return llm.Message{}, "", llm.Usage{}, flashError("oversize_response", "Responses 超过本地32MiB限制")
	}
	msg, stop, usage, err := decodeFlashResponse(raw)
	if err == nil {
		err = p.integrity.validateResponse(req, msg, stop)
	}
	if err != nil {
		return llm.Message{}, stop, usage, err
	}
	p.integrity.remember(ctx, msg)
	return msg, stop, usage, nil
}

var _ llm.Provider = (*flashResponsesProvider)(nil)
