package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	actool "github.com/Autumn-27/norma/tool"
)

const (
	ctfMaxActive     = 8
	ctfMaxJobs       = 24
	ctfMaxDepth      = 4
	ctfMaxModelCalls = 200
)

type ctfWorkflowKey struct{}
type ctfDepthKey struct{}
type ctfWorkflowRun struct {
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	rootID        int64
	budget        *agent.RunBudget
	jobs          map[int64]*ctfJob
	active, total int
	closed        bool
	changed       chan struct{}
	all           sync.WaitGroup
}
type ctfJob struct {
	id, parentID                int64
	key, state, result, failure string
	done                        chan struct{}
	stop                        context.CancelCauseFunc
	delivered                   bool
}
type ctfJobView struct {
	ID       int64      `json:"conversation_id"`
	AgentKey string     `json:"agent_key"`
	State    string     `json:"state"`
	Result   string     `json:"result,omitempty"`
	Error    string     `json:"error,omitempty"`
	Outcome  ctfOutcome `json:"outcome"`
}

func (s *Server) initCTFExecution() error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS conversation_executions (conversation_id BIGINT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE, root_id BIGINT NOT NULL, state TEXT NOT NULL, started_ms BIGINT NOT NULL, finished_ms BIGINT NOT NULL DEFAULT 0, model_calls BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS conversation_outcomes (conversation_id BIGINT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE, report_json TEXT NOT NULL)`,
		`UPDATE conversation_executions SET state='interrupted', finished_ms=CAST(strftime('%s','now') AS INTEGER)*1000 WHERE state='running'`,
	} {
		if _, err := s.m.pg.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) beginCTFWorkflow(ctx context.Context, c *db.Conversation) (context.Context, *ctfWorkflowRun) {
	if existing, ok := ctx.Value(ctfWorkflowKey{}).(*ctfWorkflowRun); ok {
		return ctx, existing
	}
	runCtx, cancel := context.WithCancel(ctx)
	w := &ctfWorkflowRun{ctx: runCtx, cancel: cancel, rootID: c.ID, budget: &agent.RunBudget{Limit: ctfMaxModelCalls}, jobs: map[int64]*ctfJob{}, changed: make(chan struct{})}
	w.budget.OnCall = func(count int64) {
		_, _ = s.m.pg.Exec(`UPDATE conversation_executions SET model_calls=MAX(model_calls,$2) WHERE conversation_id=$1`, c.ID, count)
	}
	runCtx = context.WithValue(runCtx, ctfWorkflowKey{}, w)
	runCtx = agent.WithRunBudget(runCtx, w.budget)
	w.ctx = runCtx
	s.workflowMu.Lock()
	if s.workflowRuns == nil {
		s.workflowRuns = map[int64]*ctfWorkflowRun{}
	}
	s.workflowRuns[c.ID] = w
	s.workflowMu.Unlock()
	return runCtx, w
}

func (s *Server) closeCTFWorkflow(w *ctfWorkflowRun) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		w.all.Wait()
		return
	}
	w.closed = true
	w.cancel()
	w.mu.Unlock()
	w.all.Wait() // a new user turn cannot race an old child's transcript writer
	_, _ = s.m.pg.Exec(`UPDATE conversation_executions SET model_calls=$2 WHERE conversation_id=$1`, w.rootID, w.budget.Calls())
	s.workflowMu.Lock()
	delete(s.workflowRuns, w.rootID)
	s.workflowMu.Unlock()
}

func (s *Server) startCTFExecution(id, root int64) error {
	if _, err := s.m.pg.Exec(`INSERT INTO conversation_executions(conversation_id,root_id,state,started_ms) VALUES($1,$2,'running',$3)
	 ON CONFLICT(conversation_id) DO UPDATE SET root_id=excluded.root_id,state='running',started_ms=excluded.started_ms,finished_ms=0,model_calls=0`, id, root, time.Now().UnixMilli()); err != nil {
		return err
	}
	_, err := s.m.pg.Exec(`DELETE FROM conversation_outcomes WHERE conversation_id=$1`, id)
	return err
}
func (s *Server) finishCTFExecution(id int64, state string) {
	_, _ = s.m.pg.Exec(`UPDATE conversation_executions SET state=$2,finished_ms=$3 WHERE conversation_id=$1`, id, state, time.Now().UnixMilli())
}

func (w *ctfWorkflowRun) signalLocked() { close(w.changed); w.changed = make(chan struct{}) }
func (w *ctfWorkflowRun) completionCheck(parentID int64) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var ids []int64
	for _, job := range w.jobs {
		if job.parentID == parentID && (job.state == "running" || !job.delivered) {
			ids = append(ids, job.id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return fmt.Sprintf("子任务 %v 尚在执行或结果尚未收取。使用 wait_agents 等待并读取真实结果；不再需要的分支用 cancel_agent 停止，再收取结果。不能在交付前遗留运行中的子任务。", ids)
}

func (s *Server) conversationTools(parent *db.Conversation) func(context.Context, string) []actool.CoreTool {
	return func(ownerCtx context.Context, key string) []actool.CoreTool {
		if !isDefaultCTFAgent(key) {
			return nil
		}
		tools := []actool.CoreTool{
			wrTool("delegate_agent", "同步执行一个明确的 CTF 子任务并返回结果；简单题优先只派一个 solver。", objSchema(map[string]any{"agent_key": strParam("目标角色"), "task": strParam("材料、目标、边界、验收条件")}, "agent_key", "task"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
				var req struct {
					AgentKey string `json:"agent_key"`
					Task     string `json:"task"`
				}
				if json.Unmarshal(raw, &req) != nil {
					return actool.Errorf("invalid delegation"), nil
				}
				return s.delegateConversation(ctx, parent, req.AgentKey, req.Task)
			}),
			s.ctfOutcomeTool(parent),
		}
		if key == "command" {
			tools = append(tools, s.parallelCTFDelegationTool(parent, ownerCtx))
		}
		if _, ok := ownerCtx.Value(ctfWorkflowKey{}).(*ctfWorkflowRun); ok {
			tools = append(tools,
				wrTool("wait_agents", "等待本轮直属子任务；任一新结果到达即返回。结果包含独立的执行与目标状态。空 ids 表示本轮全部直属子任务。", objSchema(map[string]any{"ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 30}}), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
					var req struct {
						IDs     []int64 `json:"ids"`
						Timeout int     `json:"timeout_seconds"`
					}
					if json.Unmarshal(raw, &req) != nil {
						return actool.Errorf("invalid wait"), nil
					}
					if req.Timeout == 0 {
						req.Timeout = 20
					}
					if req.Timeout < 1 || req.Timeout > 30 {
						return actool.Errorf("timeout_seconds must be 1..30"), nil
					}
					return s.waitCTFAgents(ctx, parent.ID, req.IDs, time.Duration(req.Timeout)*time.Second)
				}),
				wrTool("cancel_agent", "停止一个本轮直属子任务及其后代；随后用 wait_agents 收取最终状态。", objSchema(map[string]any{"id": map[string]any{"type": "integer"}}, "id"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
					var req struct {
						ID int64 `json:"id"`
					}
					if json.Unmarshal(raw, &req) != nil {
						return actool.Errorf("invalid cancellation"), nil
					}
					w := ownerCtx.Value(ctfWorkflowKey{}).(*ctfWorkflowRun)
					w.mu.Lock()
					job := w.jobs[req.ID]
					if job == nil || job.parentID != parent.ID {
						w.mu.Unlock()
						return actool.Errorf("not a child of this conversation"), nil
					}
					job.stop(agent.AbortChatStoppedByUser)
					w.mu.Unlock()
					return jsonResult(map[string]any{"conversation_id": req.ID, "status": "stopping"})
				}),
			)
		}
		return tools
	}
}

func (s *Server) parallelCTFDelegationTool(parent *db.Conversation, owner ...context.Context) actool.CoreTool {
	taskSchema := objSchema(map[string]any{"agent_key": strParam("solver"), "task": strParam("互补方向、材料、目标与验收标准")}, "agent_key", "task")
	return wrTool("delegate_agents", "启动 1 至 6 个互补 Solver 并立即返回会话编号。使用 wait_agents 逐个收取进展；简单题只启动一个。", objSchema(map[string]any{"tasks": map[string]any{"type": "array", "minItems": 1, "maxItems": 6, "items": taskSchema}}, "tasks"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
		var req struct {
			Tasks []struct {
				AgentKey string `json:"agent_key"`
				Task     string `json:"task"`
			} `json:"tasks"`
		}
		if json.Unmarshal(raw, &req) != nil || len(req.Tasks) < 1 || len(req.Tasks) > 6 {
			return actool.Errorf("tasks must contain 1 to 6 items"), nil
		}
		w, ok := ctx.Value(ctfWorkflowKey{}).(*ctfWorkflowRun)
		if !ok {
			return actool.Errorf("workflow runtime unavailable"), nil
		}
		seen := map[string]bool{}
		for _, task := range req.Tasks {
			q := strings.TrimSpace(task.Task)
			if task.AgentKey != "solver" || q == "" || len(q) > 64000 || seen[q] {
				return actool.Errorf("tasks must be distinct solver assignments, 1..64000 bytes each"), nil
			}
			seen[q] = true
		}
		w.mu.Lock()
		capacity := w.active+len(req.Tasks) <= ctfMaxActive && w.total+len(req.Tasks) <= ctfMaxJobs
		w.mu.Unlock()
		if !capacity {
			return actool.Errorf(fmt.Sprintf("本轮子任务容量不足，请先等待已有任务；总任务上限 %d，同时执行上限 %d", ctfMaxJobs, ctfMaxActive)), nil
		}
		var jobs []ctfJobView
		childContext := ctx
		if len(owner) > 0 {
			childContext = owner[0]
		}
		for _, task := range req.Tasks {
			job, err := s.startCTFChild(childContext, parent, task.AgentKey, task.Task)
			if err != nil {
				return jsonResult(map[string]any{"jobs": jobs, "error": err.Error()})
			}
			jobs = append(jobs, ctfJobView{ID: job.id, AgentKey: job.key, State: "running"})
		}
		return jsonResult(map[string]any{"jobs": jobs, "next": "wait_agents"})
	})
}

func (s *Server) delegateConversation(ctx context.Context, parent *db.Conversation, key, task string) (actool.Result, error) {
	_, exists := ctx.Value(ctfWorkflowKey{}).(*ctfWorkflowRun)
	ctx, w := s.beginCTFWorkflow(ctx, parent)
	if !exists {
		defer s.closeCTFWorkflow(w)
	}
	job, err := s.startCTFChild(ctx, parent, key, task)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	<-job.done
	w.mu.Lock()
	job.delivered = true
	view := ctfJobView{ID: job.id, AgentKey: job.key, State: job.state, Result: job.result, Error: job.failure}
	w.mu.Unlock()
	view.Outcome = s.readCTFOutcome(job.id)
	result, err := jsonResult(view)
	result.IsError = view.State == "failed" || view.State == "timed_out"
	return result, err
}

func (s *Server) startCTFChild(ctx context.Context, parent *db.Conversation, key, task string) (*ctfJob, error) {
	w, ok := ctx.Value(ctfWorkflowKey{}).(*ctfWorkflowRun)
	if !ok {
		return nil, fmt.Errorf("workflow runtime unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	depth, _ := ctx.Value(ctfDepthKey{}).(int)
	if depth >= ctfMaxDepth {
		return nil, fmt.Errorf("派发深度已达 %d，请直接分析或返回现有证据", ctfMaxDepth)
	}
	if key == parent.AgentKey || !isDefaultCTFAgent(parent.AgentKey) || !isDefaultCTFAgent(key) {
		return nil, fmt.Errorf("不能派发给自身或非默认 CTF 角色")
	}
	if strings.TrimSpace(task) == "" || len(task) > 64000 {
		return nil, fmt.Errorf("task is empty or too long")
	}
	a, err := s.m.pg.GetAgentByKey(key)
	if err != nil || a == nil || !a.Enabled || s.m.pg.AgentArchived(key) {
		return nil, fmt.Errorf("target agent unavailable")
	}
	_, profile, ok := s.personalProfileConfig(parent)
	if !ok {
		return nil, fmt.Errorf("parent model unavailable")
	}
	w.mu.Lock()
	if w.closed || w.ctx.Err() != nil {
		w.mu.Unlock()
		return nil, fmt.Errorf("workflow cancelled")
	}
	if w.active >= ctfMaxActive || w.total >= ctfMaxJobs {
		w.mu.Unlock()
		return nil, fmt.Errorf("本轮派发达到上限：同时 %d，总计 %d；先等待已有任务", ctfMaxActive, ctfMaxJobs)
	}
	w.active++
	w.total++
	w.all.Add(1)
	w.mu.Unlock()
	started := false
	defer func() {
		if !started {
			w.mu.Lock()
			w.active--
			w.total--
			w.mu.Unlock()
			w.all.Done()
		}
	}()
	var pin *int64
	if profile > 0 {
		pin = &profile
	}
	child, err := s.m.pg.CreateConversation(key, firstLine(task, 80), pin, s.personalConversationSettings(parent.ID))
	if err != nil {
		return nil, err
	}
	defer func() {
		if !started {
			_, _ = s.m.pg.Exec(`UPDATE conversation_delegations SET state='failed' WHERE child_id=$1`, child.ID)
			s.finishCTFExecution(child.ID, "failed")
		}
	}()
	if _, err = s.m.pg.Exec(`INSERT INTO conversation_delegations(parent_id,child_id) VALUES($1,$2)`, parent.ID, child.ID); err != nil {
		return nil, err
	}
	childTask := task + "\n\n父会话材料目录（只读引用，产物写自己的工作目录）：" + s.conversationWorkDir(parent.ID)
	childTask += "\n原始题目目录：" + s.conversationWorkDir(w.rootID) + "\n先核对真实文件。结束前使用 report_outcome 返回目标状态、验收条件与证据；正常回答结束并不等于目标完成。"
	ca := s.personalChatAgent(child)
	if ca == nil {
		_, _ = s.m.pg.Exec(`UPDATE conversation_delegations SET state='failed' WHERE child_id=$1`, child.ID)
		return nil, fmt.Errorf("child model unavailable")
	}
	budget := time.Duration(a.RunSecs) * time.Second
	if budget <= 0 || budget > 30*time.Minute {
		budget = 30 * time.Minute
	}
	runCtx, timeoutCancel := context.WithTimeout(ctx, budget)
	childCtx, stop := context.WithCancelCause(intercept.WithConvID(runCtx, child.ID))
	childCtx = context.WithValue(childCtx, ctfDepthKey{}, depth+1)
	childCtx = agent.WithResponseLanguage(childCtx, s.personalConversationSettings(parent.ID).ResponseLanguage)
	job := &ctfJob{id: child.ID, parentID: parent.ID, key: key, state: "running", done: make(chan struct{}), stop: stop}
	if err = s.startCTFExecution(child.ID, w.rootID); err != nil {
		timeoutCancel()
		stop(err)
		return nil, err
	}
	_, _ = s.m.pg.AppendConvActivity(child.ID, db.Activity{Worker: key, Kind: "user", Summary: firstLine(task, 200), Detail: childTask})
	_, _ = s.m.pg.AppendConvActivity(parent.ID, db.Activity{Worker: parent.AgentKey, Kind: "text", Summary: fmt.Sprintf("已分派给 %s · 会话 #%d", key, child.ID)})
	busyKey := s.convBusyKey(child.ID)
	s.chatMu.Lock()
	s.chatBusy[busyKey] = true
	s.chatCancel[busyKey] = stop
	s.chatMu.Unlock()
	w.mu.Lock()
	w.jobs[child.ID] = job
	w.signalLocked()
	w.mu.Unlock()
	started = true
	go func() {
		defer w.all.Done()
		defer timeoutCancel()
		defer stop(nil)
		ca = ca.WithExtraTools(s.conversationTools(child)).WithCompletionCheck(func() string { return w.completionCheck(child.ID) })
		result, callErr := ca.Chat(childCtx, key, busyKey, childTask, a.MaxTurns, budget, a.WebSearch, func(rec db.Activity) { _, _ = s.m.pg.AppendConvActivity(child.ID, rec) })
		// Drain descendants before releasing the conversation's busy slot.
		stop(agent.AbortChatTurnFinished)
		w.mu.Lock()
		var descendants []*ctfJob
		for _, other := range w.jobs {
			if other.parentID == child.ID {
				descendants = append(descendants, other)
			}
		}
		w.mu.Unlock()
		for _, other := range descendants {
			<-other.done
		}
		state, failure := "completed", ""
		if callErr != nil {
			state, failure = "failed", callErr.Error()
		}
		// Read the inherited/timeout context; our own cleanup cancellation above
		// must not turn an otherwise completed result into a cancellation.
		if runCtx.Err() != nil {
			state, failure = "cancelled", runCtx.Err().Error()
		}
		if cause := context.Cause(childCtx); cause != nil && cause != agent.AbortChatTurnFinished {
			state, failure = "cancelled", cause.Error()
		}
		if runCtx.Err() == context.DeadlineExceeded {
			state = "timed_out"
		}
		s.finishCTFExecution(child.ID, state)
		_, _ = s.m.pg.Exec(`UPDATE conversation_delegations SET state=$2 WHERE child_id=$1`, child.ID, state)
		s.chatMu.Lock()
		delete(s.chatBusy, busyKey)
		delete(s.chatCancel, busyKey)
		s.chatMu.Unlock()
		w.mu.Lock()
		job.state, job.result, job.failure = state, result, failure
		w.active--
		close(job.done)
		w.signalLocked()
		w.mu.Unlock()
	}()
	return job, nil
}

func (s *Server) waitCTFAgents(ctx context.Context, parentID int64, ids []int64, timeout time.Duration) (actool.Result, error) {
	w, ok := ctx.Value(ctfWorkflowKey{}).(*ctfWorkflowRun)
	if !ok {
		return actool.Errorf("workflow runtime unavailable"), nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	expired := false
	for {
		w.mu.Lock()
		selected := ids
		if len(selected) == 0 {
			for id, j := range w.jobs {
				if j.parentID == parentID {
					selected = append(selected, id)
				}
			}
		}
		ready, active := false, false
		for _, id := range selected {
			job := w.jobs[id]
			if job == nil || job.parentID != parentID {
				w.mu.Unlock()
				return actool.Errorf(fmt.Sprintf("unknown child conversation %d", id)), nil
			}
			if job.state == "running" {
				active = true
			} else if !job.delivered {
				ready = true
			}
		}
		if ready || !active || expired {
			views := make([]ctfJobView, 0, len(selected))
			for _, id := range selected {
				job := w.jobs[id]
				if job.state != "running" {
					job.delivered = true
				}
				views = append(views, ctfJobView{ID: id, AgentKey: job.key, State: job.state, Result: job.result, Error: job.failure})
			}
			w.mu.Unlock()
			for i := range views {
				views[i].Outcome = s.readCTFOutcome(views[i].ID)
			}
			return jsonResult(map[string]any{"jobs": views, "pending": active})
		}
		changed := w.changed
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			return actool.Errorf("wait cancelled"), nil
		case <-timer.C:
			expired = true
		case <-changed:
		}
	}
}
