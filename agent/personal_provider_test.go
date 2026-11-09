package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

func TestPersonalReasoningEffortWireFormats(t *testing.T) {
	for _, model := range []string{"fixture-model", "deepseek-flash"} {
		for _, format := range []llm.Format{llm.FormatOpenAI, llm.FormatOpenAIResponses, llm.FormatAnthropic} {
			for _, effort := range []string{"", "low", "high", "max"} {
				t.Run(model+"/"+string(format)+"/"+effort, func(t *testing.T) {
					var body map[string]any
					var endpoint string
					remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						endpoint = r.URL.Path
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						w.Header().Set("Content-Type", "application/json")
						switch format {
						case llm.FormatAnthropic:
							_, _ = w.Write([]byte(`{"id":"fixture","type":"message","role":"assistant","model":"fixture-model","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
						case llm.FormatOpenAIResponses:
							_, _ = w.Write([]byte(`{"id":"resp_fixture","object":"response","status":"completed","model":"fixture-model","output":[{"type":"message","id":"msg_fixture","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
						default:
							_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
						}
					}))
					defer remote.Close()
					baseURL := remote.URL + "/v1"
					if format == llm.FormatAnthropic {
						baseURL = remote.URL + "/v1/messages"
					}
					cfg := ConfigFrom(string(format), model, baseURL, "local-fixture-key", "")
					cfg.ReasoningEffort, cfg.Stream = effort, false
					_, reply, err := TestConnection(t.Context(), cfg)
					if err != nil || reply != "OK" {
						t.Fatalf("wire round trip: %q %v", reply, err)
					}
					if body["model"] != model {
						t.Fatalf("wrong requested model: %v", body["model"])
					}
					var got any
					switch format {
					case llm.FormatOpenAI:
						got = body["reasoning_effort"]
						if endpoint != "/v1/chat/completions" {
							t.Errorf("endpoint %s", endpoint)
						}
					case llm.FormatOpenAIResponses:
						if v, ok := body["reasoning"].(map[string]any); ok {
							got = v["effort"]
						}
						if endpoint != "/v1/responses" {
							t.Errorf("endpoint %s", endpoint)
						}
					case llm.FormatAnthropic:
						if v, ok := body["output_config"].(map[string]any); ok {
							got = v["effort"]
						}
						if endpoint != "/v1/messages" {
							t.Errorf("endpoint %s", endpoint)
						}
					}
					if effort == "" && got != nil {
						t.Fatalf("default emitted effort %v", got)
					}
					if effort != "" && got != effort {
						t.Fatalf("effort=%v want %s", got, effort)
					}
					if _, present := body["thinking"]; present {
						t.Fatal("model default emitted a thinking switch")
					}
				})
			}
		}
	}
}

func TestPersonalForegroundAndGoalRuntimeContract(t *testing.T) {
	var request map[string]any
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer remote.Close()
	cfg := ConfigFrom("openai", "fixture-model", remote.URL+"/v1", "local-key", "")
	provider, err := cfg.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	chat := NewChatAgent(provider, cfg.Model, t.TempDir(), nil, 10000)
	chat.SetNonStreaming(func() bool { return true })
	if _, err := chat.Chat(t.Context(), "personal", "foreground-fixture", "Inspect existing local results", 1, 10*time.Second, true, nil); err != nil {
		t.Fatal(err)
	}
	tools, _ := request["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("foreground lost its normal tools")
	}
	for _, v := range tools {
		name := v.(map[string]any)["function"].(map[string]any)["name"]
		switch name {
		case "spawn_task", "TaskOutput", "TaskStop", "TaskList", "Monitor":
			t.Errorf("foreground exposed implicit background tool %v", name)
		}
	}
	oldLanguage := TaskResponseLanguage
	TaskResponseLanguage = func(taskID int64) string {
		if taskID == 42 {
			return "en"
		}
		return ""
	}
	defer func() { TaskResponseLanguage = oldLanguage }()
	DecomposeGoalsWithProvider(t.Context(), provider, t.TempDir(), "Complete local contract check", "", nil, nil, 42, true, 0, nil)
	encoded, _ := json.Marshal(request["messages"])
	if !strings.Contains(string(encoded), "使用 English") {
		t.Fatalf("goal decomposition lost task language: %s", encoded)
	}
}
