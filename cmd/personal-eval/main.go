// personal-eval compares the production research runtime with an ablated run
// on bounded local fixtures. It is a regression/effectiveness probe, not a
// cybersecurity benchmark or proof of real-world vulnerability discovery.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"iter"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
)

type options struct {
	Format       string        `json:"format"`
	Variant      string        `json:"variant"`
	Case         string        `json:"case_filter"`
	Repeats      int           `json:"repeats"`
	Timeout      time.Duration `json:"timeout_ns"`
	Turns        int           `json:"max_model_calls"`
	MaxOutput    int           `json:"max_output_tokens"`
	Out          string        `json:"-"`
	DryRun       bool          `json:"-"`
	NonStreaming bool          `json:"non_streaming"`
}

type runResult struct {
	Case                  string          `json:"case"`
	Variant               string          `json:"variant"`
	ResearchRuntimeActive bool            `json:"research_runtime_active"`
	Repeat                int             `json:"repeat"`
	Passed                bool            `json:"passed"`
	Checks                map[string]bool `json:"checks"`
	ElapsedSeconds        float64         `json:"elapsed_seconds"`
	Terminal              string          `json:"terminal"`
	ModelStopReason       string          `json:"last_model_stop_reason"`
	Error                 string          `json:"error,omitempty"`
	Usage                 llm.Usage       `json:"usage"`
	ModelCalls            int             `json:"model_calls"`
	ModelCallLimitHit     bool            `json:"model_call_limit_hit"`
	HTTPStatuses          []int           `json:"http_statuses"`
	ResponseModels        []string        `json:"response_models"`
	ToolCalls             int             `json:"tool_calls"`
	ToolErrors            int             `json:"tool_errors"`
	Turns                 int             `json:"turns"`
	AnswerSaves           int             `json:"answer_saves"`
	Trace                 string          `json:"trace"`
	CostUSD               *float64        `json:"cost_usd"`
}

type report struct {
	Suite          string      `json:"suite"`
	Format         string      `json:"format"`
	CreatedAt      string      `json:"created_at"`
	RequestedModel string      `json:"requested_model"`
	Effort         string      `json:"reasoning_effort"`
	EndpointOrigin string      `json:"endpoint_origin"`
	Options        options     `json:"options"`
	Notes          []string    `json:"notes"`
	Runs           []runResult `json:"runs"`
}

func main() {
	if err := execute(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "prtseek personal-eval:", redact(err.Error(), config.Env("EVAL_API_KEY")))
		os.Exit(1)
	}
}

func parseOptions(args []string, out io.Writer) (options, error) {
	o := options{}
	f := flag.NewFlagSet("personal-eval", flag.ContinueOnError)
	f.SetOutput(out)
	f.StringVar(&o.Format, "format", "openai", "provider format: openai or openai-responses (shared by both variants)")
	f.StringVar(&o.Variant, "variant", "both", "baseline, optimized, or both (provider adaptation is shared)")
	f.StringVar(&o.Case, "case", "", "comma-separated case IDs; empty runs all six")
	f.IntVar(&o.Repeats, "repeats", 1, "repetitions per case/variant (1-20)")
	f.DurationVar(&o.Timeout, "timeout", 120*time.Second, "hard wall-clock limit per run (5s-30m)")
	f.IntVar(&o.Turns, "turns", 12, "hard model-call limit per run, including continuation/recovery calls (1-100); also caps harness tool-turns")
	f.IntVar(&o.MaxOutput, "max-output", 4096, "maximum output tokens per model call (256-32768)")
	f.StringVar(&o.Out, "out", "work/personal-eval", "parent directory; each invocation creates a unique run directory")
	f.BoolVar(&o.DryRun, "dry-run", false, "print cases and budget only; no model call or file creation")
	f.BoolVar(&o.NonStreaming, "non-streaming", false, "use Complete instead of Stream")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	if o.Format != "openai" && o.Format != "openai-responses" {
		return o, errors.New("format must be openai or openai-responses")
	}
	if o.Variant != "baseline" && o.Variant != "optimized" && o.Variant != "both" {
		return o, errors.New("variant must be baseline, optimized, or both")
	}
	if o.Repeats < 1 || o.Repeats > 20 || o.Turns < 1 || o.Turns > 100 || o.Timeout < 5*time.Second || o.Timeout > 30*time.Minute || o.MaxOutput < 256 || o.MaxOutput > 32768 {
		return o, errors.New("budget outside supported bounds")
	}
	return o, nil
}

func execute(args []string, out io.Writer) error {
	o, err := parseOptions(args, out)
	if err != nil {
		return err
	}
	cases, err := selectCases(o.Case)
	if err != nil {
		return err
	}
	variants := []string{o.Variant}
	if o.Variant == "both" {
		variants = []string{"baseline", "optimized"}
	}
	if o.DryRun {
		rows := []map[string]string{}
		for _, c := range cases {
			rows = append(rows, map[string]string{"id": c.ID, "title": c.Title, "task": c.Task})
		}
		return json.NewEncoder(out).Encode(map[string]any{"suite": "personal-1.2-local-v1", "format": o.Format, "available_formats": []string{"openai", "openai-responses"}, "cases": rows, "variants": variants, "repeats": o.Repeats, "max_runs": len(rows) * len(variants) * o.Repeats, "max_model_calls": len(rows) * len(variants) * o.Repeats * o.Turns, "per_run_max_model_calls": o.Turns, "per_run_seconds": o.Timeout.Seconds(), "uses_network": false, "note": "dry-run only; no credentials required, no model call"})
	}
	key := strings.TrimSpace(config.Env("EVAL_API_KEY"))
	base := strings.TrimSpace(config.Env("EVAL_BASE_URL"))
	model := strings.TrimSpace(config.Env("EVAL_MODEL"))
	effort := strings.TrimSpace(config.Env("EVAL_EFFORT"))
	if key == "" || base == "" {
		return errors.New("set prtseek_EVAL_BASE_URL and prtseek_EVAL_API_KEY in the process environment")
	}
	if model == "" {
		model = "deepseek-flash"
	}
	if effort == "" {
		effort = "high"
	}
	if effort != "low" && effort != "high" && effort != "max" {
		return errors.New("prtseek_EVAL_EFFORT must be low, high, or max")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("model endpoint must be an HTTP(S) base URL without credentials, query, or fragment")
	}
	if o.Variant != "baseline" && !agent.IsDeepSeekFlash(model) {
		return errors.New("optimized evaluation requires a recognized DeepSeek Flash model; otherwise runtime would not attach")
	}
	root, err := filepath.Abs(o.Out)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	root, err = os.MkdirTemp(root, "run-")
	if err != nil {
		return err
	}
	r := report{Suite: "personal-1.2-local-v1", Format: o.Format, CreatedAt: time.Now().UTC().Format(time.RFC3339), RequestedModel: model, Effort: effort, EndpointOrigin: u.Scheme + "://" + u.Host, Options: o, Notes: []string{
		"Local deterministic fixtures measure evidence handling, tool recovery, and answer validation; they do not establish security benchmark scores.",
		"Baseline and optimized share the selected provider format and current provider adapter. Only AttachResearchRuntime is ablated; this does not reproduce the historical 1.0 binary.",
		"Response model labels are server metadata and may be aliases; immutable model revision is not proven.",
		"Cost is null because no price schedule is configured. Usage contains adapter-reported token fields; reasoning may be included in output.",
		"Each run has isolated fixtures and state. Variant order alternates by case/repeat to reduce simple ordering bias. No external target tools are available.",
		"Objective checks are retained even when runtime_completed is false. Passing additionally requires a normal runtime terminal and final model stop reason; interrupted or truncated runs cannot pass.",
		"The model-call cap includes Complete/Stream calls, stop-hook continuations, and truncation recovery. The same cap is also applied to harness tool-turns. Provider connection, empty-response, and stream retries are disabled for both variants; rejected over-budget calls send no request.",
		"Several fixtures closely resemble the optimized variant's curated demonstrations (fixed path mapping, public summaries, changed historical readiness). This is a regression probe, not held-out evidence of general cybersecurity improvement.",
		"resume-changed checks a supplied historical file against current synthetic status within one session. It is not a two-session A/B evaluation of persistent research memory or compaction recovery.",
		"The optimized variant uses production task routing. research_runtime_active records whether research tools actually attached; an unrecognized short task may take the same runtime path as baseline.",
	}}
	cfg := agent.ConfigFrom(o.Format, model, base, key, "")
	cfg.ReasoningEffort = effort
	cfg.Stream = !o.NonStreaming
	cfg.Retry = agent.RetryConfig{ConnectAttempts: -1, EmptyAttempts: -1, StreamAttempts: -1}
	for repeat := 1; repeat <= o.Repeats; repeat++ {
		for ci, c := range cases {
			order := append([]string(nil), variants...)
			if len(order) == 2 && (ci+repeat)%2 == 0 {
				order[0], order[1] = order[1], order[0]
			}
			for _, v := range order {
				prov, e := cfg.NewProvider()
				if e != nil {
					return errors.New(redact(e.Error(), key))
				}
				work := filepath.Join(root, fmt.Sprintf("%s-%s-%02d", c.ID, v, repeat))
				run, e := runCase(context.Background(), c, v, repeat, o, model, work, prov, key)
				if e != nil {
					return errors.New(redact(e.Error(), key))
				}
				r.Runs = append(r.Runs, run)
				if e = writeJSON(filepath.Join(root, "report.json"), r); e != nil {
					return e
				}
				fmt.Fprintf(out, "%s %s #%d passed=%t tools=%d errors=%d elapsed=%.1fs\n", c.ID, v, repeat, run.Passed, run.ToolCalls, run.ToolErrors, run.ElapsedSeconds)
			}
		}
	}
	fmt.Fprintln(out, "Report:", filepath.Join(root, "report.json"))
	return nil
}

func runCase(parent context.Context, c evalCase, variant string, repeat int, o options, model, work string, prov llm.Provider, secret string) (runResult, error) {
	r := runResult{Case: c.ID, Variant: variant, Repeat: repeat, Checks: map[string]bool{}, Trace: filepath.Join(work, "trace.jsonl")}
	if err := os.MkdirAll(work, 0700); err != nil {
		return r, err
	}
	state, err := newFixtureState(c, work)
	if err != nil {
		return r, err
	}
	f, err := os.OpenFile(r.Trace, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return r, err
	}
	defer f.Close()
	trace := json.NewEncoder(f)
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	defer cancel()
	ctx = agent.WithResearchOptimization(ctx, variant == "optimized")
	meter := &observedProvider{base: prov, models: map[string]bool{}, callLimit: o.Turns}
	opts := agentcore.Options{Provider: meter, SystemPrompt: []string{"你在隔离的本地评测夹具中完成任务。所有数据都是合成数据。仅使用给定工具读取证据、查询模拟状态并保存答案；没有外部网络或shell工具。读取工具返回内容后按任务要求调用 save_answer，正文不能代替保存答案。保留证据ID；不要编造文件、结果或已执行操作。"}, Tools: state.tools(), PermissionMode: permission.ModeBypass, WorkingDir: work, MaxTurns: o.Turns, MaxDuration: o.Timeout, MaxTokens: o.MaxOutput, MaxConcurrency: 1, DisableBackgroundTasks: true, NonStreaming: o.NonStreaming}
	if err := agent.AttachResearchRuntime(ctx, &opts, model, work, c.Task); err != nil {
		return r, err
	}
	for _, tool := range opts.Tools {
		if tool.Name() == "research_recall" {
			r.ResearchRuntimeActive = true
			break
		}
	}
	s := agentcore.NewSession(opts)
	defer s.Close()
	toolNames := map[string]string{}
	for ev, e := range s.Prompt(ctx, c.Task) {
		if e != nil {
			r.Error = redact(e.Error(), secret)
			break
		}
		row := map[string]any{"kind": ev.Kind, "elapsed_ms": time.Since(start).Milliseconds()}
		switch ev.Kind {
		case harness.KindToolUse:
			if ev.ToolUse != nil {
				r.ToolCalls++
				toolNames[ev.ToolUse.ID] = ev.ToolUse.Name
				row["tool"] = ev.ToolUse.Name
				row["id"] = ev.ToolUse.ID
				row["input"] = redact(string(ev.ToolUse.Input), secret)
			}
		case harness.KindToolResult:
			if ev.ToolResult != nil {
				if ev.ToolResult.IsError {
					r.ToolErrors++
				}
				row["tool"] = toolNames[ev.ToolResult.ToolUseID]
				row["id"] = ev.ToolResult.ToolUseID
				row["is_error"] = ev.ToolResult.IsError
				var b strings.Builder
				for _, block := range ev.ToolResult.Content {
					b.WriteString(block.Text)
				}
				row["output"] = redact(b.String(), secret)
			}
		case harness.KindUsage:
			if ev.Usage != nil {
				r.Usage = *ev.Usage
				row["usage"] = *ev.Usage
			}
		case harness.KindResult:
			if ev.Terminal != nil {
				r.Terminal = string(ev.Terminal.Reason)
				r.Usage = ev.Terminal.Usage
				r.Turns = ev.Terminal.Turns
				if ev.Terminal.Err != nil {
					r.Error = redact(ev.Terminal.Err.Error(), secret)
				}
				row["reason"] = r.Terminal
			}
		case harness.KindThinking:
			continue // raw internal reasoning is deliberately not persisted
		case harness.KindText:
			continue // final prose is not the objective completion oracle
		default:
			continue
		}
		if e = trace.Encode(row); e != nil {
			return r, e
		}
	}
	if r.Terminal == "" && ctx.Err() != nil {
		r.Terminal = "timeout"
		r.Error = ctx.Err().Error()
	}
	r.ElapsedSeconds = time.Since(start).Seconds()
	r.Checks = state.check()
	r.AnswerSaves = state.saves
	meter.mu.Lock()
	r.ModelStopReason = meter.stopReason
	r.ModelCalls = meter.calls
	r.ModelCallLimitHit = meter.limitHit
	r.HTTPStatuses = append([]int(nil), meter.statuses...)
	for m := range meter.models {
		r.ResponseModels = append(r.ResponseModels, redact(m, secret))
	}
	meter.mu.Unlock()
	// Norma can surface an exhausted max_tokens continuation as completed.
	// Require the final provider response to end naturally as well as a normal
	// runtime terminal; a correct artifact alone cannot pass an interrupted run.
	r.Checks["runtime_completed"] = r.Terminal == string(harness.ReasonCompleted) && r.Error == "" && (r.ModelStopReason == "end_turn" || r.ModelStopReason == "stop_sequence")
	r.Passed = true
	for _, ok := range r.Checks {
		r.Passed = r.Passed && ok
	}
	sort.Strings(r.ResponseModels)
	return r, writeJSON(filepath.Join(work, "result.json"), r)
}

type observedProvider struct {
	base       llm.Provider
	mu         sync.Mutex
	calls      int
	statuses   []int
	models     map[string]bool
	stopReason string
	callLimit  int
	limitHit   bool
}

// Count at the entry to the provider, including model continuations that the
// harness does not count as tool-turns. No underlying provider (and therefore
// no network request) is reached after the limit is exhausted.
func (p *observedProvider) beginCall() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls >= p.callLimit {
		p.limitHit = true
		return fmt.Errorf("model call limit reached (%d); request not sent", p.callLimit)
	}
	p.calls++
	p.stopReason = ""
	return nil
}

func (p *observedProvider) Complete(ctx context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if err := p.beginCall(); err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	ctx, c := llmrec.NewCapture(ctx)
	defer p.record(c)
	msg, reason, usage, err := p.base.Complete(ctx, r)
	p.mu.Lock()
	p.stopReason = reason
	p.mu.Unlock()
	return msg, reason, usage, err
}
func (p *observedProvider) Stream(ctx context.Context, r llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if err := p.beginCall(); err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		ctx, c := llmrec.NewCapture(ctx)
		defer p.record(c)
		for ev, err := range p.base.Stream(ctx, r) {
			if ev.StopReason != "" {
				p.mu.Lock()
				p.stopReason = ev.StopReason
				p.mu.Unlock()
			}
			if !yield(ev, err) {
				return
			}
		}
	}
}
func (p *observedProvider) record(c *llmrec.Capture) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range c.Attempts() {
		p.statuses = append(p.statuses, a.Status)
		for _, line := range strings.Split(a.Body, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
			var v struct {
				Model string `json:"model"`
			}
			if json.Unmarshal([]byte(line), &v) == nil && v.Model != "" {
				p.models[v.Model] = true
			}
		}
	}
}
func redact(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[REDACTED_SECRET]")
	}
	return s
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
