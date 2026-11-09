package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

// Norma v0.4.3's neutral streaming events don't carry tool indexes. Passing
// interleaved OpenAI argument fragments through it corrupts parallel calls.
// This narrow transport serializes complete indexed tool calls at finish while
// forwarding text and reasoning immediately. It sits above the raw recorder.
type flashStreamTransport struct{ base http.RoundTripper }

func (t flashStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Norma inserts this invented text when a thinking tool turn has no text.
	// Real Flash responses may have empty/null reasoning. Preserve that absence
	// on the wire instead of claiming an invented chain of thought.
	if req.Body != nil {
		data, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(data, &body) == nil {
			var messages []map[string]json.RawMessage
			if json.Unmarshal(body["messages"], &messages) == nil {
				changed := false
				for _, m := range messages {
					var value string
					_ = json.Unmarshal(m["reasoning_content"], &value)
					var role string
					_ = json.Unmarshal(m["role"], &role)
					missingToolReasoning := role == "assistant" && len(m["tool_calls"]) > 0 && (len(m["reasoning_content"]) == 0 || string(m["reasoning_content"]) == "null")
					if value == "(reasoning elided by context compaction)" || missingToolReasoning {
						m["reasoning_content"] = json.RawMessage(`""`)
						changed = true
					}
				}
				if changed {
					body["messages"], _ = json.Marshal(messages)
					data, _ = json.Marshal(body)
				}
			}
		}
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(data))
		req.ContentLength = int64(len(data))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode != http.StatusOK ||
		!strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return resp, err
	}
	resp.Body = newFlashSSEBody(resp.Body)
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
	return resp, nil
}

const flashStreamLimit = 4 * 1024 * 1024

// FlashRemoteError is a provider-side failure, not deterministic malformed
// local history. The host's committed-output guard decides whether replay is
// safe. Keep only normalized classifications here, never arbitrary wire text.
type FlashRemoteError struct {
	Status int
	Code   string
}

func (e *FlashRemoteError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("DeepSeek SSE: status %d: %s", e.Status, e.Code)
	}
	return "DeepSeek SSE: " + e.Code
}

func flashRemoteError(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	status := 0
	for _, key := range []string{"status", "status_code", "code"} {
		var n int
		if json.Unmarshal(fields[key], &n) != nil {
			var text string
			_ = json.Unmarshal(fields[key], &text)
			n, _ = strconv.Atoi(text)
		}
		if n >= 400 && n <= 599 {
			status = n
			break
		}
	}
	// Provider variants place their classification in code, type, or message.
	// The original response remains available in the existing raw recorder.
	s := strings.ToLower(string(raw))
	code := "remote_stream_error"
	// An explicit upstream status outranks wording inside an error message.
	// For example, a 401 mentioning quota must not become a failover signal.
	if status != 0 {
		if status == 402 {
			code = "insufficient_quota"
		}
		return &FlashRemoteError{Status: status, Code: code}
	}
	switch {
	case IsQuotaExhaustedMessage(s):
		code = "insufficient_quota"
	case strings.Contains(s, "context_length") || strings.Contains(s, "maximum context") || strings.Contains(s, "context length"):
		status, code = 413, "context_length_exceeded"
	case strings.Contains(s, "invalid_api_key") || strings.Contains(s, "authentication"):
		status, code = 401, "authentication_error"
	case strings.Contains(s, "invalid_request"):
		status, code = 400, "invalid_request_error"
	case strings.Contains(s, "permission_denied"):
		status, code = 403, "permission_denied"
	case strings.Contains(s, "rate_limit"):
		status, code = 429, "rate_limit_exceeded"
	case strings.Contains(s, "overload"):
		status, code = 503, "overloaded_error"
	}
	return &FlashRemoteError{Status: status, Code: code}
}

type flashWireTool struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type flashSSEBody struct {
	body     io.ReadCloser
	scanner  *bufio.Scanner
	out      bytes.Buffer
	tools    map[int]*flashWireTool
	toolSize int
	finished bool
	err      error
}

func newFlashSSEBody(body io.ReadCloser) *flashSSEBody {
	scan := bufio.NewScanner(body)
	scan.Buffer(make([]byte, 64*1024), flashStreamLimit)
	return &flashSSEBody{body: body, scanner: scan, tools: make(map[int]*flashWireTool)}
}

func (b *flashSSEBody) Close() error { return b.body.Close() }

func (b *flashSSEBody) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for b.out.Len() == 0 && b.err == nil {
		b.err = b.next()
	}
	if b.out.Len() > 0 {
		return b.out.Read(dst)
	}
	return 0, b.err
}

func (b *flashSSEBody) next() error {
	var data strings.Builder
	for b.scanner.Scan() {
		line := strings.TrimSuffix(b.scanner.Text(), "\r")
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			return b.transform(strings.TrimSuffix(data.String(), "\n"))
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
			if data.Len() > flashStreamLimit {
				return flashError("oversize_frame", "SSE 单帧超过本地 4 MiB 限制")
			}
		}
	}
	if err := b.scanner.Err(); err != nil {
		return err
	}
	if data.Len() > 0 {
		return b.transform(strings.TrimSuffix(data.String(), "\n"))
	}
	if len(b.tools) > 0 && !b.finished {
		return flashError("truncated_tool", "SSE 在工具参数完成前断开；工具未执行")
	}
	return io.EOF
}

func (b *flashSSEBody) transform(data string) error {
	if strings.TrimSpace(data) == "[DONE]" {
		if len(b.tools) > 0 && !b.finished {
			return flashError("truncated_tool", "SSE 没有工具完成标记；工具未执行")
		}
		b.out.WriteString("data: [DONE]\n\n")
		return nil
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return flashError("invalid_frame", "无法解析 SSE JSON 帧")
	}
	if raw := frame["error"]; len(raw) > 0 && string(raw) != "null" {
		return flashRemoteError(raw)
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(frame["choices"], &choices); err != nil {
		return flashError("invalid_frame", "SSE choices 不是有效数组")
	}
	if len(choices) == 0 {
		b.emit(data) // usage-only frame
		return nil
	}
	choice := choices[0]
	var delta map[string]json.RawMessage
	if len(choice["delta"]) > 0 && string(choice["delta"]) != "null" {
		if err := json.Unmarshal(choice["delta"], &delta); err != nil {
			return flashError("invalid_frame", "SSE delta 不是有效对象")
		}
	}
	if delta == nil {
		delta = make(map[string]json.RawMessage)
	}
	var fragments []flashWireTool
	if raw := delta["tool_calls"]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &fragments); err != nil {
			return flashError("invalid_tool_frame", "无法解析工具调用片段")
		}
	}
	delete(delta, "tool_calls")
	for _, frag := range fragments {
		if b.finished || frag.Index < 0 || frag.Index > 1024 {
			return flashError("invalid_tool_index", "工具调用片段序号或顺序无效")
		}
		call := b.tools[frag.Index]
		if call == nil {
			call = &flashWireTool{Index: frag.Index, Type: "function"}
			b.tools[frag.Index] = call
		}
		if frag.ID != "" {
			if call.ID != "" && call.ID != frag.ID {
				return flashError("changed_tool_id", "同一工具序号在流中更换了调用 ID")
			}
			call.ID = frag.ID
		}
		if frag.Type != "" && frag.Type != "function" {
			return flashError("invalid_tool_type", "收到非 function 工具调用")
		}
		call.Function.Name += frag.Function.Name
		call.Function.Arguments += frag.Function.Arguments
		b.toolSize += len(frag.Function.Name) + len(frag.Function.Arguments) + len(frag.ID)
		if b.toolSize > flashStreamLimit {
			return flashError("oversize_tool", "工具调用数据超过本地 4 MiB 限制")
		}
	}
	var stop string
	_ = json.Unmarshal(choice["finish_reason"], &stop)
	if stop != "" {
		if len(b.tools) > 0 {
			if stop != "tool_calls" && stop != "stop" {
				return flashError("truncated_tool", "模型在工具参数生成完成前结束（"+stop+"）；工具未执行")
			}
			indexes := make([]int, 0, len(b.tools))
			for index := range b.tools {
				indexes = append(indexes, index)
			}
			sort.Ints(indexes)
			complete := make([]flashWireTool, 0, len(indexes))
			seen := map[string]bool{}
			for _, index := range indexes {
				call := b.tools[index]
				if err := validateFlashTool(llm.ContentBlock{ID: call.ID, Name: call.Function.Name, Input: json.RawMessage(call.Function.Arguments)}); err != nil {
					return err
				}
				if seen[call.ID] {
					return flashError("duplicate_tool_id", "回复中有重复的工具调用 ID")
				}
				seen[call.ID] = true
				complete = append(complete, *call)
			}
			encoded, _ := json.Marshal(complete)
			delta["tool_calls"] = encoded
			b.tools = make(map[int]*flashWireTool)
		}
		b.finished = true
	}
	encoded, _ := json.Marshal(delta)
	choice["delta"] = encoded
	encoded, _ = json.Marshal(choices)
	frame["choices"] = encoded
	encoded, _ = json.Marshal(frame)
	b.emit(string(encoded))
	return nil
}

func (b *flashSSEBody) emit(data string) {
	b.out.WriteString("data: ")
	b.out.WriteString(data)
	b.out.WriteString("\n\n")
}
