package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAgentConnectionRequiresToolRoundTrip(t *testing.T) {
	for _, supportsTools := range []bool{false, true} {
		t.Run(fmt.Sprint(supportsTools), func(t *testing.T) {
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("x-fixture-session") == "" {
					t.Error("custom session header was lost")
				}
				var body struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
					Tools []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if len(body.Tools) != 1 || body.Tools[0].Function.Name != "connection_probe" {
					t.Error("connection test exposed other tools")
				}
				w.Header().Set("Content-Type", "application/json")
				var message any = map[string]any{"role": "assistant", "content": "OK"}
				reason := "stop"
				if supportsTools {
					if calls == 1 {
						message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "probe", "type": "function", "function": map[string]any{"name": "connection_probe", "arguments": "{}"}}}}
						reason = "tool_calls"
					} else {
						for _, msg := range body.Messages {
							if msg.Role == "tool" {
								message = map[string]any{"role": "assistant", "content": msg.Content}
							}
						}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "choices": []any{map[string]any{"message": message, "finish_reason": reason}}})
			}))
			defer endpoint.Close()
			cfg := ConfigFrom("openai", "fixture", endpoint.URL+"/v1", "test-key", "")
			cfg.Stream = false
			cfg.SessionHeaderKey = "x-fixture-session"
			_, reply, err := TestAgentConnection(t.Context(), cfg)
			if supportsTools {
				if err != nil || !strings.Contains(reply, "probe-") || calls != 2 {
					t.Fatalf("roundtrip: %q %v calls=%d", reply, err, calls)
				}
			} else if err == nil {
				t.Fatal("text-only endpoint passed tool verification")
			}
		})
	}
}

func TestAgentConnectionStreamingRoundTrip(t *testing.T) {
	var calls atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !body.Stream {
			t.Error("streaming configuration was lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var delta any
		reason := "stop"
		if n == 1 {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "probe", "type": "function", "function": map[string]any{"name": "connection_probe", "arguments": "{}"}}}}
			reason = "tool_calls"
		} else {
			text := ""
			for _, message := range body.Messages {
				if message.Role == "tool" {
					text = message.Content
				}
			}
			delta = map[string]any{"content": text}
		}
		for _, chunk := range []any{map[string]any{"id": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta}}}, map[string]any{"id": "fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reason}}}} {
			raw, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer endpoint.Close()
	cfg := ConfigFrom("openai", "fixture", endpoint.URL+"/v1", "fixture-only", "")
	cfg.Stream = true
	_, reply, err := TestAgentConnection(t.Context(), cfg)
	if err != nil || !strings.Contains(reply, "probe-") || calls.Load() != 2 {
		t.Fatalf("stream roundtrip: %q %v calls=%d", reply, err, calls.Load())
	}
}
