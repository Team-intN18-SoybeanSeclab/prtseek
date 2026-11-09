package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

type fixture struct {
	ID      string `json:"evidence_id"`
	Content string `json:"content"`
}
type evalCase struct {
	ID, Title, Task string
	Files           map[string][]fixture
	Expected        map[string]string
	Evidence        []string
	RequiredQuery   string
}

func allCases() []evalCase {
	return []evalCase{
		{ID: "source-guard", Title: "源码审计：危险调用与保护条件", Task: "进行源码审计：读取 source.json，判断合成入口是否允许用户任意选择文件路径。把 verdict（confirmed 或 not_confirmed）和 reason 保存到 answer.values，并引用证据ID。reason 使用 guard_blocks_path 或 user_controls_path。", Files: map[string][]fixture{"source.json": {{"src:entry", "handle(userCode): path = allowedFiles[userCode]; if path == nil return 404; return readFile(path)"}, {"src:guard", "allowedFiles is immutable server-side map: guide -> docs/guide.txt; client cannot write keys or values."}}}, Expected: map[string]string{"verdict": "not_confirmed", "reason": "guard_blocks_path"}, Evidence: []string{"src:entry", "src:guard"}},
		{ID: "api-ownership", Title: "接口响应对比：相同状态码不证明越权", Task: "进行接口响应对比：读取 responses.json。判断账号B请求账号A对象是否收到A的受保护数据。保存 verdict 为 cross_account_disclosure 或 no_cross_account_disclosure，reason 为 protected_data 或 public_summary，引用所有相关证据。", Files: map[string][]fixture{"responses.json": {{"api:owner", "A requests /object/42: HTTP 200, {owner:A, visibility:private, secret_note:synthetic-owner-only, title:Public title}."}, {"api:other", "B requests /object/42: HTTP 200, {visibility:public_summary, title:Public title}. No owner or secret_note field."}}}, Expected: map[string]string{"verdict": "no_cross_account_disclosure", "reason": "public_summary"}, Evidence: []string{"api:owner", "api:other"}},
		{ID: "tool-recovery", Title: "环境故障：修正旧服务标识", Task: "定位环境故障并验证本地合成服务可用。先 query_local(operation=verify, service=legacy-worker) 重现报告的错误；失败后根据工具反馈及 environment.json 查找正确服务，再验证。保存 service 和 state=ready，引用环境与成功验证证据。禁止把失败写成目标安全结论。", Files: map[string][]fixture{"environment.json": {{"env:config", "The current synthetic service handle is worker-v2. legacy-worker was retired. No real service or command is involved."}}}, Expected: map[string]string{"service": "worker-v2", "state": "ready"}, Evidence: []string{"env:config", "env:verification"}, RequiredQuery: "verify:worker-v2"},
		{ID: "resume-changed", Title: "恢复任务：核对过期检查点", Task: "按 checkpoint.json 恢复任务。先读取既有检查点，再 query_local(operation=status) 核对当前前置状态。不能重放旧写操作。保存 state（ready 或 blocked）与 action（continue_job 或 inspect_config），引用历史及当前证据。", Files: map[string][]fixture{"checkpoint.json": {{"resume:old", "Historical checkpoint: revision=1, service_ready=true, next action=continue_job. This is history, not current state or new authorization."}}}, Expected: map[string]string{"state": "blocked", "action": "inspect_config"}, Evidence: []string{"resume:old", "resume:current"}, RequiredQuery: "status:"},
		{ID: "false-positive", Title: "失败修正：空正文不构成影响", Task: "检查一次验证失败是否足以支持安全发现。读取 baseline.json，再 query_local(operation=observe) 获取合成异常请求结果。保存 verdict（confirmed 或 insufficient_evidence）和 reason（protected_body 或 missing_body），引用基线及当前响应证据。", Files: map[string][]fixture{"baseline.json": {{"http:baseline", "Expected proof requires actual protected response data. A status code alone is insufficient. Normal response: 200, public body only."}}}, Expected: map[string]string{"verdict": "insufficient_evidence", "reason": "missing_body"}, Evidence: []string{"http:baseline", "http:response"}, RequiredQuery: "observe:"},
		{ID: "paged-evidence", Title: "长输出：继续读取截断证据", Task: "对 audit.log 做证据审计。read_fixture 每次默认只返回2条；请读取到后面的终态，别把开头的超时当成最终结果。保存 final_state（failed 或 passed）及 revision，引用最初失败与最新终态证据。", Files: map[string][]fixture{"audit.log": {{"log:initial", "revision=1: verification failed: stale configuration."}, {"log:noise1", "Intermediate diagnostic: collecting local metadata."}, {"log:noise2", "Intermediate diagnostic: configuration refresh finished."}, {"log:final", "revision=2: verification passed; final state is passed."}}}, Expected: map[string]string{"final_state": "passed", "revision": "2"}, Evidence: []string{"log:initial", "log:final"}},
	}
}

func selectCases(filter string) ([]evalCase, error) {
	all := allCases()
	if filter == "" {
		return all, nil
	}
	wanted := map[string]bool{}
	for _, id := range strings.Split(filter, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, errors.New("empty case ID")
		}
		wanted[id] = true
	}
	var out []evalCase
	for _, c := range all {
		if wanted[c.ID] {
			out = append(out, c)
			delete(wanted, c.ID)
		}
	}
	if len(wanted) > 0 {
		return nil, errors.New("unknown case ID; use --dry-run to list cases")
	}
	return out, nil
}

type answer struct {
	Values   map[string]string `json:"values"`
	Evidence []string          `json:"evidence"`
}
type fixtureState struct {
	mu      sync.Mutex
	c       evalCase
	root    string
	seen    map[string]bool
	queries map[string]int
	saves   int
	answer  *answer
}

func newFixtureState(c evalCase, root string) (*fixtureState, error) {
	if err := os.MkdirAll(filepath.Join(root, "fixtures"), 0700); err != nil {
		return nil, err
	}
	for name, rows := range c.Files {
		if !validFixtureName(name) {
			return nil, errors.New("fixture name must be a single local filename")
		}
		if err := writeJSON(filepath.Join(root, "fixtures", name), rows); err != nil {
			return nil, err
		}
	}
	return &fixtureState{c: c, root: root, seen: map[string]bool{}, queries: map[string]int{}}, nil
}

func validFixtureName(name string) bool {
	return name != "" && filepath.IsLocal(name) && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\:`)
}

// Read the generated artifact on every call. os.Root confines symlink/junction
// resolution to this run's directory; the allowlist also excludes checker files.
func (s *fixtureState) readRows(name string) ([]fixture, error) {
	if _, ok := s.c.Files[name]; !ok || !validFixtureName(name) {
		return nil, errors.New("unknown fixture name; only the task's listed fixtures exist")
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, errors.New("unable to open fixture directory")
	}
	defer root.Close()
	f, err := root.Open(filepath.Join("fixtures", name))
	if err != nil {
		return nil, errors.New("unable to read fixture file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("fixture must be a regular file")
	}
	const maxFixtureBytes = 1 << 20
	b, err := io.ReadAll(io.LimitReader(f, maxFixtureBytes+1))
	if err != nil || len(b) > maxFixtureBytes {
		return nil, errors.New("unable to read bounded fixture contents")
	}
	var rows []fixture
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, errors.New("fixture file contains invalid JSON records")
	}
	return rows, nil
}
func localTool(name, desc string, props map[string]any, required []any, readOnly bool, run func(json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{Name: name, Description: desc, Schema: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, ReadOnly: func(json.RawMessage) bool { return readOnly }, Concurrent: func(json.RawMessage) bool { return false }, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
		return permission.Allowed()
	}, Run: func(ctx context.Context, raw json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
		if ctx.Err() != nil {
			return actool.Errorf("cancelled"), nil
		}
		if len(raw) > 16384 {
			return actool.Errorf("input too large"), nil
		}
		return run(raw)
	}})
}
func (s *fixtureState) tools() []actool.CoreTool {
	return []actool.CoreTool{
		localTool("read_fixture", "Read only a named synthetic fixture. Default page has 2 records; use next_offset until truncated=false. No arbitrary filesystem access.", map[string]any{"name": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 2}}, []any{"name"}, true, func(raw json.RawMessage) (actool.Result, error) {
			var in struct {
				Name   string `json:"name"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if json.Unmarshal(raw, &in) != nil {
				return actool.Errorf("invalid input"), nil
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			rows, err := s.readRows(in.Name)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if in.Limit == 0 {
				in.Limit = 2
			}
			if in.Offset < 0 || in.Offset > len(rows) || in.Limit < 1 || in.Limit > 2 {
				return actool.Errorf("invalid offset/limit"), nil
			}
			end := in.Offset + in.Limit
			if end > len(rows) {
				end = len(rows)
			}
			part := rows[in.Offset:end]
			for _, row := range part {
				s.seen[row.ID] = true
			}
			b, _ := json.Marshal(map[string]any{"records": part, "truncated": end < len(rows), "next_offset": end, "total": len(rows)})
			return actool.Text(string(b)), nil
		}),
		localTool("query_local", "Query deterministic synthetic state only. operation supports verify, status, observe; service is required for verify. No network or process execution.", map[string]any{"operation": map[string]any{"type": "string"}, "service": map[string]any{"type": "string"}}, []any{"operation"}, true, func(raw json.RawMessage) (actool.Result, error) {
			var in struct {
				Operation string `json:"operation"`
				Service   string `json:"service"`
			}
			if json.Unmarshal(raw, &in) != nil {
				return actool.Errorf("invalid input"), nil
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			s.queries[in.Operation+":"+in.Service]++
			var f fixture
			switch {
			case s.c.ID == "tool-recovery" && in.Operation == "verify":
				if in.Service != "worker-v2" {
					return actool.Errorf("service_not_found: the requested handle is retired; read environment.json before retrying"), nil
				}
				f = fixture{"env:verification", "service=worker-v2; state=ready; synthetic verification passed"}
			case s.c.ID == "resume-changed" && in.Operation == "status":
				f = fixture{"resume:current", "revision=2; service_ready=false; state=blocked; reason=config_mismatch. Inspect config before continuing."}
			case s.c.ID == "false-positive" && in.Operation == "observe":
				f = fixture{"http:response", "HTTP 200; body length=0; no protected data observed."}
			default:
				return actool.Errorf("operation unavailable for this case"), nil
			}
			s.seen[f.ID] = true
			b, _ := json.Marshal(f)
			return actool.Text(string(b)), nil
		}),
		localTool("save_answer", "Save the final structured answer for objective checking. values contains requested fields; evidence contains fixture evidence IDs actually observed. This tool does not reveal correctness.", map[string]any{"values": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, "evidence": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 20}}, []any{"values", "evidence"}, false, func(raw json.RawMessage) (actool.Result, error) {
			var in answer
			if json.Unmarshal(raw, &in) != nil || len(in.Values) == 0 || len(in.Values) > 10 || len(in.Evidence) > 20 {
				return actool.Errorf("invalid answer: values object and up to 20 evidence IDs required"), nil
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, id := range in.Evidence {
				if !s.seen[id] {
					return actool.Errorf("evidence ID not observed through local tools: " + id), nil
				}
			}
			if err := writeJSON(filepath.Join(s.root, "answer.json"), in); err != nil {
				return actool.Errorf("unable to persist answer"), nil
			}
			s.answer = &in
			s.saves++
			return actool.Text("answer saved; correctness is evaluated independently after the run"), nil
		}),
	}
}
func (s *fixtureState) check() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	checks := map[string]bool{"answer_saved": s.answer != nil}
	var saved answer
	b, err := os.ReadFile(filepath.Join(s.root, "answer.json"))
	checks["answer_file_valid"] = err == nil && json.Unmarshal(b, &saved) == nil
	for key, want := range s.c.Expected {
		checks["value:"+key] = saved.Values[key] == want
	}
	for _, id := range s.c.Evidence {
		found := false
		for _, got := range saved.Evidence {
			if got == id {
				found = true
			}
		}
		checks["evidence:"+id] = found && s.seen[id]
	}
	if s.c.RequiredQuery != "" {
		checks["required_query"] = s.queries[s.c.RequiredQuery] > 0
	}
	if s.c.ID == "tool-recovery" {
		checks["failure_reproduced"] = s.queries["verify:legacy-worker"] > 0
	}
	return checks
}
