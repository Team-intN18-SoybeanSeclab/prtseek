package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

func TestDryRunDoesNotNeedCredentialsOrCreateArtifacts(t *testing.T) {
	t.Setenv("ARTEX_EVAL_API_KEY", "")
	t.Setenv("ARTEX_EVAL_BASE_URL", "")
	outDir := filepath.Join(t.TempDir(), "must-not-exist")
	var b bytes.Buffer
	if err := execute([]string{"--dry-run", "--out", outDir}, &b); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Cases               []any    `json:"cases"`
		Runs                int      `json:"max_runs"`
		Network             bool     `json:"uses_network"`
		Format              string   `json:"format"`
		AvailableFormats    []string `json:"available_formats"`
		MaxModelCalls       int      `json:"max_model_calls"`
		PerRunMaxModelCalls int      `json:"per_run_max_model_calls"`
	}
	if err := json.Unmarshal(b.Bytes(), &result); err != nil || len(result.Cases) != 6 || result.Runs != 12 || result.Network || result.Format != "openai" || strings.Join(result.AvailableFormats, ",") != "openai,openai-responses" || result.MaxModelCalls != 144 || result.PerRunMaxModelCalls != 12 || strings.Contains(b.String(), "max_model_turns") {
		t.Fatalf("invalid dry run: %s %v", b.String(), err)
	}
	b.Reset()
	if err := execute([]string{"--dry-run", "--format", "openai-responses", "--out", outDir}, &b); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b.Bytes(), &result); err != nil || result.Format != "openai-responses" || result.Runs != 12 || result.Network {
		t.Fatalf("invalid Responses dry run: %s %v", b.String(), err)
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Fatal("dry-run created artifacts")
	}
	for _, args := range [][]string{{"--format", "other", "--dry-run"}, {"--format", "", "--dry-run"}, {"--case", "missing", "--dry-run"}, {"--turns", "0", "--dry-run"}, {"--variant", "other", "--dry-run"}, {"--timeout", "1s", "--dry-run"}, {"--repeats", "21", "--dry-run"}} {
		if err := execute(args, io.Discard); err == nil {
			t.Fatalf("accepted invalid args %v", args)
		}
	}
}

func fixtureCall(t *testing.T, state *fixtureState, name string, input any) actool.Result {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range state.tools() {
		if tool.Name() == name {
			if err = actool.ValidateInput(tool.InputSchema(), raw); err != nil {
				t.Fatal(err)
			}
			res, e := tool.Call(context.Background(), raw, nil)
			if e != nil {
				t.Fatal(e)
			}
			return res
		}
	}
	t.Fatalf("missing tool %s", name)
	return actool.Result{}
}

func TestFixtureAnswersRequireObservedEvidenceAndObjectiveValues(t *testing.T) {
	for _, c := range allCases() {
		t.Run(c.ID, func(t *testing.T) {
			s, err := newFixtureState(c, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if res := fixtureCall(t, s, "save_answer", answer{c.Expected, c.Evidence}); !res.IsError {
				t.Fatal("unobserved evidence accepted")
			}
			if res := fixtureCall(t, s, "read_fixture", map[string]any{"name": "../../secret"}); !res.IsError {
				t.Fatal("arbitrary path accepted")
			}
			for name, rows := range c.Files {
				for i := 0; i < len(rows); i += 2 {
					if res := fixtureCall(t, s, "read_fixture", map[string]any{"name": name, "offset": i}); res.IsError {
						t.Fatal(res.Flatten())
					}
				}
			}
			if c.ID == "tool-recovery" {
				if res := fixtureCall(t, s, "query_local", map[string]string{"operation": "verify", "service": "legacy-worker"}); !res.IsError {
					t.Fatal("expected failure missing")
				}
			}
			if c.RequiredQuery != "" {
				parts := strings.SplitN(c.RequiredQuery, ":", 2)
				if res := fixtureCall(t, s, "query_local", map[string]string{"operation": parts[0], "service": parts[1]}); res.IsError {
					t.Fatal(res.Flatten())
				}
			}
			if res := fixtureCall(t, s, "save_answer", answer{map[string]string{"verdict": "confidently_completed"}, c.Evidence}); res.IsError {
				t.Fatal(res.Flatten())
			}
			pass := true
			for _, ok := range s.check() {
				pass = pass && ok
			}
			if pass {
				t.Fatal("wrong answer passed")
			}
			if res := fixtureCall(t, s, "save_answer", answer{c.Expected, c.Evidence}); res.IsError {
				t.Fatal(res.Flatten())
			}
			for key, ok := range s.check() {
				if !ok {
					t.Errorf("correct observed answer failed %s", key)
				}
			}
			if err = os.WriteFile(filepath.Join(s.root, "answer.json"), []byte(`{"values":{"verdict":"tampered"},"evidence":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			pass = true
			for _, ok := range s.check() {
				pass = pass && ok
			}
			if pass {
				t.Fatal("checker ignored artifact tampering")
			}
		})
	}
}

func TestReadFixtureUsesCurrentDiskContents(t *testing.T) {
	c := allCases()[0]
	s, err := newFixtureState(c, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.root, "fixtures", "source.json")
	changed := []fixture{{"disk:new", "This content was changed on disk after fixture initialization."}}
	if err := writeJSON(path, changed); err != nil {
		t.Fatal(err)
	}
	res := fixtureCall(t, s, "read_fixture", map[string]string{"name": "source.json"})
	if res.IsError || !strings.Contains(res.Flatten(), changed[0].Content) || !s.seen["disk:new"] || s.seen["src:entry"] {
		t.Fatalf("tool did not read modified disk artifact: %s", res.Flatten())
	}
	// The tool exposes actual content, while hidden expected answers remain
	// checker-owned and are not rewritten by changes to the data file.
	if c.Expected["verdict"] != "not_confirmed" {
		t.Fatal("fixture mutation changed hidden expected answer")
	}
	if res := fixtureCall(t, s, "save_answer", answer{c.Expected, c.Evidence}); !res.IsError {
		t.Fatal("in-memory original records counted as observed disk evidence")
	}
	if err := os.WriteFile(path, []byte("invalid fixture json"), 0600); err != nil {
		t.Fatal(err)
	}
	if res := fixtureCall(t, s, "read_fixture", map[string]string{"name": "source.json"}); !res.IsError {
		t.Fatal("malformed disk fixture accepted")
	}
	for _, name := range []string{"../answer.json", "source.json/../source.json", "C:\\outside.json", "answer.json"} {
		if res := fixtureCall(t, s, "read_fixture", map[string]string{"name": name}); !res.IsError {
			t.Fatalf("non-allowlisted path accepted: %s", name)
		}
	}
}

type scriptedAction struct {
	name  string
	input any
}
type scriptedProvider struct {
	actions     []scriptedAction
	calls       int
	requests    []llm.CompletionRequest
	secret      string
	endReason   string
	endError    error
	waitContext bool
}

func (p *scriptedProvider) Complete(ctx context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.requests = append(p.requests, r)
	p.calls++
	if c := llmrec.CaptureFrom(ctx); c != nil {
		body := c.TeeResponse(200, io.NopCloser(strings.NewReader(`{"model":"fixture-flash-revision"}`)))
		_, _ = io.Copy(io.Discard, body)
		_ = body.Close()
	}
	u := llm.Usage{InputTokens: 10, OutputTokens: 2}
	if p.calls > len(p.actions) {
		if p.waitContext {
			<-ctx.Done()
			return llm.Message{}, "", u, ctx.Err()
		}
		reason := p.endReason
		if reason == "" {
			reason = "end_turn"
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done " + p.secret)}}, reason, u, p.endError
	}
	a := p.actions[p.calls-1]
	raw, _ := json.Marshal(a.input)
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: fmt.Sprintf("call-%d", p.calls), Name: a.name, Input: raw}}}, "tool_use", u, nil
}
func (p *scriptedProvider) Stream(ctx context.Context, r llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		msg, reason, usage, err := p.Complete(ctx, r)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		for _, b := range msg.Content {
			switch b.Type {
			case llm.BlockText:
				if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: b.Text}, nil) {
					return
				}
			case llm.BlockToolUse:
				if !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: b.ID, ToolName: b.Name}, nil) || !yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: string(b.Input)}, nil) {
					return
				}
			}
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: reason, Usage: usage}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func actionsForCase(c evalCase) []scriptedAction {
	var actions []scriptedAction
	if c.ID == "tool-recovery" {
		actions = append(actions, scriptedAction{"query_local", map[string]string{"operation": "verify", "service": "legacy-worker"}})
	}
	for name, rows := range c.Files {
		for i := 0; i < len(rows); i += 2 {
			actions = append(actions, scriptedAction{"read_fixture", map[string]any{"name": name, "offset": i}})
		}
	}
	if c.RequiredQuery != "" {
		parts := strings.SplitN(c.RequiredQuery, ":", 2)
		actions = append(actions, scriptedAction{"query_local", map[string]string{"operation": parts[0], "service": parts[1]}})
	}
	return append(actions, scriptedAction{"save_answer", answer{c.Expected, c.Evidence}})
}

func TestEvaluationRunsActualHarnessForBothVariants(t *testing.T) {
	for _, c := range allCases() {
		for _, v := range []string{"baseline", "optimized"} {
			t.Run(c.ID+"-"+v, func(t *testing.T) {
				root := t.TempDir()
				p := &scriptedProvider{actions: actionsForCase(c), secret: "fixture-secret-do-not-save"}
				o := options{Timeout: 10 * time.Second, Turns: 12, MaxOutput: 4096, NonStreaming: true}
				r, err := runCase(context.Background(), c, v, 1, o, "deepseek-flash", root, p, p.secret)
				if err != nil || !r.Passed {
					t.Fatalf("harness failed: %+v %v", r, err)
				}
				if r.ToolCalls < len(p.actions) || r.ModelCalls < 1 || r.Usage.InputTokens <= 0 || r.CostUSD != nil || len(r.ResponseModels) != 1 || r.ResponseModels[0] != "fixture-flash-revision" {
					t.Fatalf("missing actual telemetry: %+v", r)
				}
				if c.ID == "tool-recovery" && r.ToolErrors < 1 {
					t.Fatal("actual tool failure not counted")
				}
				hasMemory := false
				for _, tool := range p.requests[0].Tools {
					if tool.Name == "research_recall" {
						hasMemory = true
					}
				}
				// All six ask for evidence work. A method card need not match
				// for the broader production evidence runtime to attach.
				wantRuntime := v == "optimized"
				if hasMemory != wantRuntime || r.ResearchRuntimeActive != hasMemory {
					t.Fatal("runtime ablation did not affect tool set")
				}
				trace, err := os.ReadFile(r.Trace)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(trace), p.secret) || strings.Contains(string(trace), `"kind":"thinking"`) {
					t.Fatal("secret or raw reasoning persisted")
				}
			})
		}
	}
}

func TestTextOnlyCompletionCannotPassObjectiveChecks(t *testing.T) {
	c := allCases()[0]
	p := &scriptedProvider{}
	r, err := runCase(context.Background(), c, "baseline", 1, options{Timeout: 10 * time.Second, Turns: 3, MaxOutput: 512, NonStreaming: true}, "deepseek-flash", t.TempDir(), p, "")
	if err != nil || r.Passed || r.AnswerSaves != 0 {
		t.Fatalf("text-only completion passed: %+v %v", r, err)
	}
}

func TestCorrectArtifactCannotPassIncompleteRuntime(t *testing.T) {
	for _, mode := range []string{"model_error", "timeout", "max_turns", "max_tokens"} {
		t.Run(mode, func(t *testing.T) {
			c := allCases()[0]
			p := &scriptedProvider{actions: actionsForCase(c)}
			o := options{Timeout: 3 * time.Second, Turns: 12, MaxOutput: 512, NonStreaming: true}
			switch mode {
			case "model_error":
				p.endError = fmt.Errorf("synthetic provider failure after correct answer")
			case "timeout":
				p.waitContext = true
				o.Timeout = 100 * time.Millisecond
			case "max_turns":
				o.Turns = len(p.actions)
			case "max_tokens":
				p.endReason = "max_tokens"
			}
			r, err := runCase(context.Background(), c, "baseline", 1, o, "deepseek-flash", t.TempDir(), p, "")
			if err != nil {
				t.Fatal(err)
			}
			if r.Passed || r.Checks["runtime_completed"] || r.AnswerSaves != 1 {
				t.Fatalf("incorrect runtime completion: %+v", r)
			}
			for key, ok := range r.Checks {
				if key != "runtime_completed" && !ok {
					t.Errorf("objective evidence lost after runtime failure: %s", key)
				}
			}
			if r.Terminal == "" || ((mode == "model_error" || mode == "timeout") && r.Error == "") {
				t.Fatalf("terminal/error diagnostics lost: %+v", r)
			}
			if mode == "max_tokens" && r.ModelStopReason != "max_tokens" {
				t.Fatalf("final truncation reason lost: %+v", r)
			}
		})
	}
}

func TestModelCallHardLimitIncludesRecoveryAndStopContinuations(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		for _, variant := range []string{"baseline", "optimized"} {
			for _, mode := range []string{"truncation", "stop_hook"} {
				t.Run(fmt.Sprintf("%s/%s/non_streaming=%t", variant, mode, nonStreaming), func(t *testing.T) {
					c := allCases()[0]
					p := &scriptedProvider{}
					o := options{Timeout: 10 * time.Second, Turns: 2, MaxOutput: 512, NonStreaming: nonStreaming}
					if mode == "truncation" {
						p.endReason = "max_tokens"
					} else {
						// Nine real tool observations exceed the runtime's eight-
						// observation checkpoint threshold. The tenth model call
						// ends naturally, then the optimized Stop hook asks for a
						// checkpoint; that eleventh request must never reach p.
						for i := 0; i < 8; i++ {
							p.actions = append(p.actions, scriptedAction{"read_fixture", map[string]string{"name": "source.json"}})
						}
						p.actions = append(p.actions, scriptedAction{"save_answer", answer{c.Expected, c.Evidence}})
						o.Turns = 10
					}
					r, err := runCase(context.Background(), c, variant, 1, o, "deepseek-flash", t.TempDir(), p, "")
					if err != nil {
						t.Fatal(err)
					}
					if p.calls != o.Turns || len(p.requests) != o.Turns || r.ModelCalls != o.Turns || len(r.HTTPStatuses) != o.Turns {
						t.Fatalf("hard limit or actual telemetry mismatch: provider_calls=%d report=%+v", p.calls, r)
					}
					wantLimit := mode == "truncation" || variant == "optimized"
					if wantLimit {
						if !r.ModelCallLimitHit || r.Passed || r.Checks["runtime_completed"] || r.Terminal != "model_error" || !strings.Contains(r.Error, "model call limit reached") {
							t.Fatalf("continuation escaped call budget: %+v", r)
						}
					} else if r.ModelCallLimitHit || !r.Passed || r.Terminal != "completed" {
						t.Fatalf("natural completion at exact call limit incorrectly failed: %+v", r)
					}
					if mode == "stop_hook" {
						for key, ok := range r.Checks {
							if key != "runtime_completed" && !ok {
								t.Errorf("correct answer discarded after stop-hook budget failure: %s", key)
							}
						}
					}
				})
			}
		}
	}
}
