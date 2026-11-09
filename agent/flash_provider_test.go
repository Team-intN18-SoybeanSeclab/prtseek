package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

func flashTestConfig(url string) Config {
	c := ConfigFrom("openai", "deepseek-flash", url, "fixture-only", "")
	c.Retry.ConnectAttempts, c.Retry.EmptyAttempts = -1, -1
	return c
}

func flashTestTools() []llm.ToolSchema {
	return []llm.ToolSchema{{Name: "inspect", Description: "Read an authorized local fixture", InputSchema: map[string]any{"type": "object"}}}
}

func TestFlashCapabilitiesAndConfigIsolation(t *testing.T) {
	for _, name := range []string{"deepseek-flash", " DEEPSEEK-FLASH ", "deepseek/deepseek-flash", "deepseek-v4.1-flash-1017", "deepseek-v4-flash"} {
		if !IsDeepSeekFlash(name) {
			t.Errorf("missing alias %q", name)
		}
		c := Config{Model: name}
		if c.CompactionWindow() != 128000 {
			t.Errorf("default window for %q = %d", name, c.CompactionWindow())
		}
		c.ContextWindowK = 256
		if c.CompactionWindow() != 256000 {
			t.Fatal("explicit window was overwritten")
		}
	}
	for _, name := range []string{"deepseek-v4-pro", "deepseek-reasoner", "my-deepseek-flash", "deepseek-flash-unknown", "deepseek-v4-flash-vision-exp", "fixture"} {
		if IsDeepSeekFlash(name) {
			t.Errorf("incorrectly specialized %q", name)
		}
		if (Config{Model: name}).CompactionWindow() != 200000 {
			t.Error("generic default changed")
		}
	}
	cap, ok := FlashModelCapabilities("deepseek-flash")
	if !ok || cap.MaxOutputTokens != 393216 || !cap.DefaultThinking {
		t.Fatalf("capability metadata %+v", cap)
	}
	cap.ReasoningEfforts[0] = "mutated"
	cap2, _ := FlashModelCapabilities("deepseek-flash")
	if cap2.ReasoningEfforts[0] != "low" {
		t.Fatal("shared mutable capability data")
	}
}

func TestFlashIndexedStreamAndRoundTrip(t *testing.T) {
	var requests atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, exists := body["thinking"]; exists {
			t.Error("default thinking was forced on wire")
		}
		if _, exists := body["reasoning_effort"]; exists {
			t.Error("default effort was overwritten")
		}
		if _, exists := body["temperature"]; exists {
			t.Error("ineffective temperature leaked into thinking request")
		}
		if _, exists := body["max_tokens"]; exists {
			t.Error("provider default output budget was overwritten")
		}
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			frames := []string{
				`{"choices":[{"delta":{"reasoning_content":"原始思考：先核对两个文件。"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"inspect","arguments":"{\"path\":"}},{"index":1,"id":"b","type":"function","function":{"name":"inspect","arguments":"{\"path\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"second.txt\"}"}},{"index":0,"function":{"arguments":"\"first.txt\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":42,"completion_tokens":15,"prompt_tokens_details":{"cached_tokens":20}}}`,
			}
			for _, frame := range frames {
				fmt.Fprintf(w, "data: %s\n\n", frame)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		// Verify the actual serialized wire fields, not just internal blocks.
		var wire []map[string]any
		_ = json.Unmarshal(body["messages"], &wire)
		found := false
		for _, m := range wire {
			if m["role"] == "assistant" {
				if m["reasoning_content"] != "原始思考：先核对两个文件。" {
					t.Errorf("lost reasoning: %v", m["reasoning_content"])
				}
				found = true
			}
		}
		if !found {
			t.Error("lost assistant history")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"verified"},"finish_reason":"stop"}]}`)
	}))
	defer remote.Close()
	p, err := flashTestConfig(remote.URL).NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	ctx := transcript.WithSessionID(t.Context(), "roundtrip")
	temperature := 0.1
	req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("inspect")}, Tools: flashTestTools(), Temperature: &temperature}
	acc := llm.NewAccumulator()
	for ev, err := range p.Stream(ctx, req) {
		if err != nil {
			t.Fatal(err)
		}
		acc.Add(ev)
	}
	msg := acc.Message()
	calls := msg.ToolUses()
	if len(calls) != 2 || string(calls[0].Input) != `{"path":"first.txt"}` || string(calls[1].Input) != `{"path":"second.txt"}` {
		t.Fatalf("interleaved arguments corrupted: %+v", calls)
	}
	if acc.Usage.InputTokens != 42 || acc.Usage.CacheReadTokens != 20 {
		t.Fatalf("usage lost: %+v", acc.Usage)
	}
	req.Messages = append(req.Messages, msg, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("a", "first", false), llm.ToolResultText("b", "second", false)}})
	reply, _, _, err := p.Complete(ctx, req)
	if err != nil || reply.Text() != "verified" {
		t.Fatalf("next tool turn: %q %v", reply.Text(), err)
	}
}

func TestFlashMalformedStreamNeverEmitsExecutableTool(t *testing.T) {
	for name, ending := range map[string]string{
		"truncated":           `data: {"choices":[{"delta":{},"finish_reason":"length"}]}` + "\n\n",
		"bad_json":            `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		"eof":                 "",
		"done_without_finish": "data: [DONE]\n\n",
		"error_frame":         `data: {"error":{"message":"upstream interrupted"}}` + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"checking\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"x\",\"function\":{\"name\":\"inspect\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n")
				fmt.Fprint(w, ending)
			}))
			defer remote.Close()
			p, _ := flashTestConfig(remote.URL).NewProvider()
			var gotErr error
			for ev, err := range p.Stream(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}, Tools: flashTestTools()}) {
				if ev.Type == llm.SEToolUseStart || ev.Type == llm.SEToolInputJSON {
					t.Fatal("incomplete tool escaped validation")
				}
				if err != nil {
					gotErr = err
				}
			}
			var protocol *FlashProtocolError
			if name == "error_frame" {
				var remote *FlashRemoteError
				if !errors.As(gotErr, &remote) || errors.As(gotErr, &protocol) {
					t.Fatalf("want remote error, got %v", gotErr)
				}
				return
			}
			if !errors.As(gotErr, &protocol) {
				t.Fatalf("want protocol error, got %v", gotErr)
			}
		})
	}
}

func flashHistory() []llm.Message {
	return []llm.Message{
		llm.UserText("inspect fixture"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "authentic reasoning"}, {Type: llm.BlockToolUse, ID: "one", Name: "inspect", Input: json.RawMessage(`{"path":"fixture.txt"}`)}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("one", "evidence: fixture exists", false)}},
	}
}

func TestFlashHistoryRepairIsAuthenticAndSessionScoped(t *testing.T) {
	p := newFlashProvider(nil, Config{Model: "deepseek-flash"})
	ctx := transcript.WithSessionID(t.Context(), "owner")
	history := flashHistory()
	p.remember(ctx, history[1])
	history[1].Content = history[1].Content[1:]
	req := llm.CompletionRequest{Messages: history, Tools: flashTestTools()}
	got, err := p.prepare(ctx, req)
	if err != nil || flashReasoning(got.Messages[1]) != "authentic reasoning" {
		t.Fatalf("restore failed: %v", err)
	}
	if flashReasoning(req.Messages[1]) != "" {
		t.Fatal("caller history mutated")
	}
	for _, sid := range []string{"other", ""} {
		other, err := p.prepare(transcript.WithSessionID(t.Context(), sid), req)
		if err != nil || flashReasoning(other.Messages[1]) != "" {
			t.Fatalf("cross-session recovery %q: %v", sid, err)
		}
	}
	changed := append([]llm.Message(nil), history...)
	changed[1].Content = append([]llm.ContentBlock(nil), changed[1].Content...)
	changed[1].Content[0].Input = json.RawMessage(`{"path":"different.txt"}`)
	other, err := p.prepare(ctx, llm.CompletionRequest{Messages: changed, Tools: flashTestTools()})
	if err != nil || flashReasoning(other.Messages[1]) != "" {
		t.Fatal("recovered reasoning onto different action")
	}
}

func TestFlashHistoryStructuralErrorsDoNotReachServer(t *testing.T) {
	var requests atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer remote.Close()
	p, _ := flashTestConfig(remote.URL).NewProvider()
	cases := map[string][]llm.Message{
		"unanswered":       flashHistory()[:2],
		"orphan":           flashHistory()[2:],
		"duplicate_result": append(flashHistory(), flashHistory()[2]),
	}
	for name, history := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{Messages: history, Tools: flashTestTools()})
			var protocol *FlashProtocolError
			if !errors.As(err, &protocol) {
				t.Fatalf("want structural error: %v", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("malformed history reached upstream")
	}
}

func TestFlashDisabledThinkingAndOutputBudget(t *testing.T) {
	p := newFlashProvider(nil, Config{Model: "deepseek-flash", ThinkingType: "disabled"})
	history := flashHistory()
	history[1].Content = history[1].Content[1:]
	temp := 0.3
	req := llm.CompletionRequest{Messages: history, Tools: flashTestTools(), Temperature: &temp}
	got, err := p.prepare(t.Context(), req)
	if err != nil || got.Temperature != &temp {
		t.Fatalf("disabled thinking contract: %v", err)
	}
	req.Thinking = "enabled"
	if got, err := p.prepare(t.Context(), req); err != nil || got.Temperature != nil {
		t.Fatal("per-request enabled ignored")
	}
	req.Messages = []llm.Message{llm.UserText("test")}
	for _, budget := range []int{-1, 393217} {
		req.MaxTokens = budget
		if _, err := p.prepare(t.Context(), req); err == nil {
			t.Errorf("accepted budget %d", budget)
		}
	}
	req.MaxTokens = 393216
	if _, err := p.prepare(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	placeholder := flashHistory()
	placeholder[1].Content[0].Thinking = "(reasoning elided by context compaction)"
	req.Messages = placeholder
	if _, err := p.prepare(t.Context(), req); err == nil {
		t.Fatal("accepted fake reasoning placeholder")
	}
}

func TestFlashTranscriptAndCompactionPreserveReasoning(t *testing.T) {
	ctx := transcript.WithSessionID(t.Context(), "cold-resume")
	var history []llm.Message
	for i := 0; i < 12; i++ {
		history = append(history, llm.UserText(strings.Repeat("past context ", 2000)))
	}
	history = append(history, flashHistory()...)
	c := compaction.New(compaction.Config{ContextWindow: 32000, KeepRecent: 3}, func(context.Context, []llm.Message) (string, error) {
		return "Verified previous facts and evidence references.", nil
	})
	compacted, ok := c.Reactive(ctx, history)
	if !ok {
		t.Fatal("fixture did not trigger actual compaction")
	}
	store := transcript.NewStore(t.TempDir())
	writer := store.NewWriter("cold-resume", "")
	for _, m := range compacted {
		writer.RecordMessage(m, llm.Usage{})
	}
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	recs, err := store.Load(store.MainPath("cold-resume"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := transcript.Messages(recs)
	api := llm.MessagesForAPI(loaded)
	// Cold provider: recovery cache is deliberately empty.
	p := newFlashProvider(nil, Config{Model: "deepseek-flash"})
	req, err := p.prepare(ctx, llm.CompletionRequest{Messages: api, Tools: flashTestTools()})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range req.Messages {
		if len(m.ToolUses()) > 0 {
			found = true
			if flashReasoning(m) != "authentic reasoning" {
				t.Fatal("compaction or cold resume lost reasoning")
			}
		}
	}
	if !found {
		t.Fatal("compaction dropped active exchange")
	}
}

func TestFlashNonStreamingRejectsPartialArguments(t *testing.T) {
	for _, tc := range []struct{ arguments, stop string }{{`{"path":`, "tool_calls"}, {`{"path":"x"}`, "length"}, {`[]`, "tool_calls"}, {`null`, "tool_calls"}} {
		t.Run(tc.arguments+tc.stop, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "reasoning_content": "original", "tool_calls": []any{map[string]any{"id": "x", "type": "function", "function": map[string]any{"name": "inspect", "arguments": tc.arguments}}}}, "finish_reason": tc.stop}}})
			}))
			defer remote.Close()
			p, _ := flashTestConfig(remote.URL).NewProvider()
			msg, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}, Tools: flashTestTools()})
			if err == nil || len(msg.ToolUses()) != 0 {
				t.Fatalf("partial call accepted: %+v %v", msg, err)
			}
		})
	}
}

func TestFlashStreamIsIncrementalAndClosesBody(t *testing.T) {
	reader, writer := io.Pipe()
	body := newFlashSSEBody(reader)
	defer body.Close()
	go func() { fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"live\"}}]}\n\n") }()
	buf := make([]byte, 2048)
	n, err := body.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), "live") {
		t.Fatalf("reasoning buffered until finish: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("closed")); err == nil {
		t.Fatal("closing adapter did not close underlying response")
	}
	_ = writer.Close()
}

func TestFlashCurrentToolResponseAllowsAuthenticEmptyReasoning(t *testing.T) {
	for _, tc := range []struct {
		name, configured, requested, reasoning string
		allowed                                bool
	}{
		{name: "default", allowed: true},
		{name: "enabled", configured: "enabled", allowed: true},
		{name: "disabled", configured: "disabled", allowed: true},
		{name: "request_enabled", configured: "disabled", requested: "enabled", allowed: true},
		{name: "request_disabled", configured: "enabled", requested: "disabled", allowed: true},
		{name: "whitespace", reasoning: "  \n\t", allowed: true},
		{name: "sdk_placeholder", reasoning: " (reasoning elided by context compaction) "},
		{name: "authentic", reasoning: "Check the authorized fixture.", allowed: true},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					call := map[string]any{"index": 0, "id": "valid", "type": "function", "function": map[string]any{"name": "inspect", "arguments": `{}`}}
					message := map[string]any{"role": "assistant", "tool_calls": []any{call}, "reasoning_content": tc.reasoning}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": message, "finish_reason": "tool_calls"}}})
						fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "tool_calls"}}})
					}
				}))
				defer remote.Close()
				cfg := flashTestConfig(remote.URL)
				cfg.ThinkingType = tc.configured
				p, err := cfg.NewProvider()
				if err != nil {
					t.Fatal(err)
				}
				req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("inspect")}, Tools: flashTestTools(), Thinking: tc.requested}
				var gotErr error
				var count int
				if stream {
					for ev, err := range p.Stream(t.Context(), req) {
						if err != nil {
							gotErr = err
						}
						if ev.Type == llm.SEToolUseStart {
							count++
						}
					}
				} else {
					msg, _, _, err := p.Complete(t.Context(), req)
					gotErr, count = err, len(msg.ToolUses())
				}
				if tc.allowed {
					if gotErr != nil || count != 1 {
						t.Fatalf("valid tool rejected: count=%d err=%v", count, gotErr)
					}
				} else {
					var protocol *FlashProtocolError
					if count != 0 || !errors.As(gotErr, &protocol) || protocol.Code != "synthetic_reasoning" {
						t.Fatalf("missing reasoning exposed tools: count=%d err=%v", count, gotErr)
					}
				}
			})
		}
	}
}

func TestFlashRemoteErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		payload string
		status  int
		code    string
	}{
		{`{"type":"overloaded_error","message":"private upstream details"}`, 503, "overloaded_error"},
		{`{"code":"rate_limit_exceeded"}`, 429, "rate_limit_exceeded"},
		{`{"code":"context_length_exceeded"}`, 413, "context_length_exceeded"},
		{`{"type":"invalid_request_error"}`, 400, "invalid_request_error"},
		{`{"code":"invalid_api_key"}`, 401, "authentication_error"},
		{`{"code":"insufficient_quota"}`, 0, "insufficient_quota"},
		{`{"status_code":502,"message":"private upstream details"}`, 502, "remote_stream_error"},
		{`{"code":"422"}`, 422, "remote_stream_error"},
		{`{"status":401,"message":"insufficient_quota overloaded"}`, 401, "remote_stream_error"},
		{`{"status":503,"message":"context length"}`, 503, "remote_stream_error"},
		{`{"message":"private upstream details"}`, 0, "remote_stream_error"},
	} {
		t.Run(tc.code+tc.payload, func(t *testing.T) {
			body := newFlashSSEBody(io.NopCloser(strings.NewReader("data: {\"error\":" + tc.payload + "}\n\n")))
			defer body.Close()
			_, err := io.ReadAll(body)
			var remote *FlashRemoteError
			var protocol *FlashProtocolError
			if !errors.As(err, &remote) || errors.As(err, &protocol) || remote.Status != tc.status || remote.Code != tc.code {
				t.Fatalf("classification: %+v", err)
			}
			if strings.Contains(err.Error(), "private upstream details") {
				t.Fatal("remote detail leaked into public error")
			}
		})
	}
}
