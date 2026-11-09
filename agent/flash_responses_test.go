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
	"time"

	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

const responseReasoningFixture = `{"type":"reasoning","id":"rs_authentic","status":"completed","content":[{"type":"reasoning_text","text":"Check both files."}],"summary":[],"encrypted_content":"opaque-original-state"}`

func responseToolFixture(id, call, path string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"type": "function_call", "id": id, "call_id": call, "name": "inspect", "status": "completed", "arguments": fmt.Sprintf(`{"path":%q}`, path)})
	return data
}
func responseFixture(status string, items ...json.RawMessage) []byte {
	data, _ := json.Marshal(map[string]any{"id": "resp_fixture", "object": "response", "status": status, "output": items, "usage": map[string]any{"input_tokens": 37, "output_tokens": 19, "input_tokens_details": map[string]int{"cached_tokens": 12}, "output_tokens_details": map[string]int{"reasoning_tokens": 7}}})
	return data
}
func responseTestProvider(t *testing.T, url string) llm.Provider {
	t.Helper()
	cfg := flashTestConfig(url)
	cfg.Format = llm.FormatOpenAIResponses
	p, err := cfg.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func responseWriteEvent(w io.Writer, event string, payload any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}
func responseWriteParallel(w io.Writer, ending string, wrongIdentity bool) {
	responseWriteEvent(w, "response.created", map[string]any{"type": "response.created", "response": map[string]string{"status": "in_progress"}})
	responseWriteEvent(w, "response.reasoning_text.delta", map[string]any{"type": "response.reasoning_text.delta", "item_id": "rs_authentic", "output_index": 0, "delta": "Check both files."})
	for i := 1; i <= 2; i++ {
		responseWriteEvent(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": i, "item": map[string]string{"type": "function_call", "id": fmt.Sprint("item", i), "call_id": fmt.Sprint("call", i), "name": "inspect", "arguments": ""}})
	}
	for _, i := range []int{2, 1} {
		id := fmt.Sprint("item", i)
		if wrongIdentity {
			id = "wrong"
		}
		responseWriteEvent(w, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": i, "item_id": id, "delta": `{"path":`})
	}
	for _, i := range []int{1, 2} {
		responseWriteEvent(w, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": i, "item_id": fmt.Sprint("item", i), "delta": fmt.Sprintf(`"file%d"}`, i)})
	}
	if ending == "eof" {
		return
	}
	status := "completed"
	if ending == "incomplete" {
		status = "incomplete"
	}
	raw := responseFixture(status, json.RawMessage(responseReasoningFixture), responseToolFixture("item1", "call1", "file1"), responseToolFixture("item2", "call2", "file2"))
	responseWriteEvent(w, "response."+status, map[string]any{"type": "response." + status, "response": json.RawMessage(raw)})
}

func TestFlashResponsesReasoningColdRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var count atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					t.Errorf("wrong endpoint %s", r.URL.Path)
				}
				var body map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&body)
				if string(body["store"]) != "false" {
					t.Error("stateless contract lost")
				}
				if _, ok := body["previous_response_id"]; ok {
					t.Error("server state invented")
				}
				if count.Add(1) == 1 {
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						responseWriteParallel(w, "completed", false)
					} else {
						w.Header().Set("Content-Type", "application/json")
						w.Write(responseFixture("completed", json.RawMessage(responseReasoningFixture), responseToolFixture("item1", "call1", "file1"), responseToolFixture("item2", "call2", "file2")))
					}
					return
				}
				var items []json.RawMessage
				_ = json.Unmarshal(body["input"], &items)
				found := false
				for _, raw := range items {
					var item map[string]json.RawMessage
					_ = json.Unmarshal(raw, &item)
					if string(item["type"]) == `"reasoning"` {
						found = true
						if string(item["id"]) != `"rs_authentic"` || string(item["encrypted_content"]) != `"opaque-original-state"` || !strings.Contains(string(item["content"]), "Check both files.") {
							t.Errorf("reasoning item not replayed: %s", raw)
						}
					}
				}
				if !found {
					t.Error("cold resume lost reasoning item")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(responseFixture("completed", json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"verified"}]}`)))
			}))
			defer remote.Close()
			ctx := transcript.WithSessionID(t.Context(), "responses-cold")
			p := responseTestProvider(t, remote.URL)
			req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("inspect")}, Tools: flashTestTools()}
			var msg llm.Message
			var usage llm.Usage
			if stream {
				acc := llm.NewAccumulator()
				for ev, err := range p.Stream(ctx, req) {
					if err != nil {
						t.Fatal(err)
					}
					acc.Add(ev)
				}
				msg, usage = acc.Message(), acc.Usage
			} else {
				var err error
				msg, _, usage, err = p.Complete(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := msg.ToolUses()
			if len(calls) != 2 || string(calls[0].Input) != `{"path":"file1"}` || string(calls[1].Input) != `{"path":"file2"}` {
				t.Fatalf("parallel tool arguments corrupted %+v", calls)
			}
			if usage.InputTokens != 37 || usage.OutputTokens != 19 || usage.CacheReadTokens != 12 {
				t.Fatalf("usage lost %+v", usage)
			}
			var history []llm.Message
			for i := 0; i < 12; i++ {
				history = append(history, llm.UserText(strings.Repeat("previous context ", 2000)))
			}
			history = append(history, req.Messages...)
			history = append(history, msg, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("call1", "one", false), llm.ToolResultText("call2", "two", false)}})
			compact := compaction.New(compaction.Config{ContextWindow: 32000, KeepRecent: 3}, func(context.Context, []llm.Message) (string, error) { return "past verified facts", nil })
			compacted, ok := compact.Reactive(ctx, history)
			if !ok {
				t.Fatal("fixture failed to compact")
			}
			store := transcript.NewStore(t.TempDir())
			writer := store.NewWriter("responses-cold", "")
			for _, m := range compacted {
				writer.RecordMessage(m, llm.Usage{})
			}
			if writer.Err() != nil {
				t.Fatal(writer.Err())
			}
			recs, err := store.Load(store.MainPath("responses-cold"))
			if err != nil {
				t.Fatal(err)
			}
			loaded, _ := transcript.Messages(recs)
			p = responseTestProvider(t, remote.URL)
			req.Messages = llm.MessagesForAPI(loaded)
			reply, _, _, err := p.Complete(ctx, req)
			if err != nil || reply.Text() != "verified" {
				t.Fatalf("cold replay %q %v", reply.Text(), err)
			}
		})
	}
}

func TestFlashResponsesMalformedStreamNeverExecutesTools(t *testing.T) {
	for _, tc := range []struct {
		name, ending string
		wrong        bool
	}{{"EOF", "eof", false}, {"truncated", "incomplete", false}, {"identity", "completed", true}} {
		t.Run(tc.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				responseWriteParallel(w, tc.ending, tc.wrong)
			}))
			defer remote.Close()
			p := responseTestProvider(t, remote.URL)
			var got error
			for ev, err := range p.Stream(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("inspect")}, Tools: flashTestTools()}) {
				if ev.Type == llm.SEToolUseStart || ev.Type == llm.SEToolInputJSON {
					t.Fatal("unvalidated tools escaped")
				}
				if err != nil {
					got = err
				}
			}
			var protocol *FlashProtocolError
			if !errors.As(got, &protocol) {
				t.Fatalf("want protocol error: %v", got)
			}
		})
	}
}

func TestFlashResponsesThinkingModesAndLimits(t *testing.T) {
	for _, tc := range []struct{ name, cfgMode, reqMode, effort, want string }{{"default", "", "", "", ""}, {"disabled", "disabled", "", "high", "none"}, {"request_disabled", "enabled", "disabled", "max", "none"}, {"request_enabled", "disabled", "enabled", "none", "high"}, {"none", "", "", "none", "none"}, {"max", "", "", "max", "max"}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := flashTestConfig("http://fixture.invalid")
			cfg.Format = llm.FormatOpenAIResponses
			cfg.ThinkingType = tc.cfgMode
			cfg.ReasoningEffort = tc.effort
			p, err := newFlashResponsesProvider(cfg, http.DefaultClient)
			if err != nil {
				t.Fatal(err)
			}
			temp := 0.2
			raw, _, err := p.request(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}, Thinking: tc.reqMode, Temperature: &temp, MaxTokens: 393216}, false)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Reasoning *struct {
					Effort string `json:"effort"`
				} `json:"reasoning"`
				Temperature *float64 `json:"temperature"`
				Max         int      `json:"max_output_tokens"`
			}
			_ = json.Unmarshal(raw, &body)
			if tc.want == "" {
				if body.Reasoning != nil {
					t.Fatal("forced model default")
				}
			} else if body.Reasoning == nil || body.Reasoning.Effort != tc.want {
				t.Fatalf("effort mismatch %s", raw)
			}
			if (body.Temperature != nil) != (tc.want == "none") || body.Max != 393216 {
				t.Fatalf("temperature or cap contract %s", raw)
			}
			for _, cap := range []int{-1, 393217} {
				_, _, err := p.request(t.Context(), llm.CompletionRequest{MaxTokens: cap}, false)
				if err == nil {
					t.Fatal("bad cap accepted")
				}
			}
		})
	}
}

func TestFlashResponsesRejectsBadNonStreamingTools(t *testing.T) {
	for _, args := range []string{`{"path":`, `[]`, `null`} {
		t.Run(args, func(t *testing.T) {
			item, _ := json.Marshal(map[string]string{"type": "function_call", "id": "item", "call_id": "call", "name": "inspect", "arguments": args})
			msg, _, _, err := decodeFlashResponse(responseFixture("completed", item))
			if err == nil || len(msg.ToolUses()) != 0 {
				t.Fatal("bad args accepted")
			}
		})
	}
	msg, _, _, err := decodeFlashResponse(responseFixture("incomplete", responseToolFixture("item", "call", "x")))
	if err == nil || len(msg.ToolUses()) != 0 {
		t.Fatal("truncated tool accepted")
	}
}

func TestFlashResponsesRetryCancellationAndCapture(t *testing.T) {
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"type":"overloaded_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(responseFixture("completed", json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`)))
	}))
	defer remote.Close()
	cfg := flashTestConfig(remote.URL)
	cfg.Format = llm.FormatOpenAIResponses
	cfg.Retry.ConnectAttempts = 1
	cfg.Retry.ConnectInterval = time.Millisecond
	p, err := cfg.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	captureCtx, capture := llmrec.NewCapture(t.Context())
	msg, _, _, err := p.Complete(captureCtx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}})
	if err != nil || msg.Text() != "ok" || calls.Load() != 2 {
		t.Fatalf("HTTP retries %d %v", calls.Load(), err)
	}
	attempts := capture.Attempts()
	if len(attempts) != 2 || attempts[0].Status != 503 || attempts[1].Status != 200 || !strings.Contains(attempts[1].Body, `"output_text"`) || !strings.Contains(capture.RawRequest(), `"model":"deepseek-flash"`) {
		t.Fatal("Responses bypassed original raw capture stack")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, _, err = p.Complete(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
}

func TestFlashResponsesReasoningMetadataIntegrity(t *testing.T) {
	msg, _, _, err := decodeFlashResponse(responseFixture("completed", json.RawMessage(responseReasoningFixture)))
	if err != nil {
		t.Fatal(err)
	}
	b := msg.Content[0]
	b.Thinking = "changed"
	if _, err := flashReplayReasoning(b); err == nil {
		t.Fatal("replayed metadata with modified thought")
	}
}

func TestFlashResponsesUnsupportedContentIsNotSilentlyDropped(t *testing.T) {
	p, err := newFlashResponsesProvider(flashTestConfig("http://fixture.invalid"), http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []llm.BlockType{"image", "document", "audio", "future_type"} {
		t.Run(string(kind), func(t *testing.T) {
			req := llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: kind}}}}}
			_, _, err := p.request(t.Context(), req, false)
			var protocol *FlashProtocolError
			if !errors.As(err, &protocol) || protocol.Code != "unsupported_content" {
				t.Fatalf("unknown input silently dropped: %v", err)
			}
			history := flashHistory()
			history[2].Content[0].Content = []llm.ContentBlock{{Type: kind}}
			_, _, err = p.request(t.Context(), llm.CompletionRequest{Messages: history}, false)
			if !errors.As(err, &protocol) || protocol.Code != "unsupported_content" {
				t.Fatalf("unknown tool content silently dropped: %v", err)
			}
		})
	}
}

func TestFlashResponsesReasoningIsLiveAndBodyCloses(t *testing.T) {
	closed := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		responseWriteEvent(w, "response.reasoning_text.delta", map[string]any{"type": "response.reasoning_text.delta", "delta": "live reasoning"})
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer remote.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	p := responseTestProvider(t, remote.URL)
	seen := false
	for ev, err := range p.Stream(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == llm.SEThinkingDelta {
			seen = ev.Text == "live reasoning"
			break
		}
	}
	if !seen {
		t.Fatal("reasoning waited for terminal response")
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("iterator break did not close response body")
	}
}

func TestFlashResponsesRemoteErrorsKeepClassification(t *testing.T) {
	for _, tc := range []struct {
		event   string
		payload any
		status  int
	}{
		{"error", map[string]string{"type": "error", "code": "invalid_api_key"}, 401},
		{"response.failed", map[string]any{"type": "response.failed", "response": map[string]any{"status": "failed", "error": map[string]string{"code": "rate_limit_exceeded"}}}, 429},
	} {
		t.Run(tc.event, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				responseWriteEvent(w, tc.event, tc.payload)
			}))
			defer remote.Close()
			p := responseTestProvider(t, remote.URL)
			var got error
			for _, err := range p.Stream(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}}) {
				if err != nil {
					got = err
				}
			}
			var upstream *FlashRemoteError
			if !errors.As(got, &upstream) || upstream.Status != tc.status {
				t.Fatalf("remote error lost: %v", got)
			}
		})
	}
}

func TestFlashEmptyChatReasoningRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var calls atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]json.RawMessage `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if calls.Add(1) > 1 {
					for _, m := range body.Messages {
						if string(m["role"]) == `"assistant"` && string(m["reasoning_content"]) != `""` {
							t.Errorf("empty original reasoning replaced: %s", m["reasoning_content"])
						}
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"\"}}]}\n\n")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":null,\"tool_calls\":[{\"index\":0,\"id\":\"empty\",\"type\":\"function\",\"function\":{\"name\":\"inspect\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","reasoning_content":null,"tool_calls":[{"id":"empty","type":"function","function":{"name":"inspect","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
				}
			}))
			defer remote.Close()
			p, err := flashTestConfig(remote.URL).NewProvider()
			if err != nil {
				t.Fatal(err)
			}
			req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}, Tools: flashTestTools()}
			var msg llm.Message
			if stream {
				acc := llm.NewAccumulator()
				for ev, err := range p.Stream(t.Context(), req) {
					if err != nil {
						t.Fatal(err)
					}
					acc.Add(ev)
				}
				msg = acc.Message()
			} else {
				msg, _, _, err = p.Complete(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
			}
			req.Messages = append(req.Messages, msg, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("empty", "done", false)}})
			msg, _, _, err = p.Complete(t.Context(), req)
			if err != nil || msg.Text() != "ok" {
				t.Fatalf("empty reasoning next turn: %v", err)
			}
		})
	}
}
