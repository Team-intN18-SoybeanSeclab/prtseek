package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	actool "github.com/Autumn-27/norma/tool"
)

func (s *Server) initAgentWorkflows() error {
	pg := s.m.pg
	if pg == nil {
		return nil
	}
	if err := pg.EnsureAgentArchives(); err != nil {
		return err
	}
	if _, err := pg.Exec(`CREATE TABLE IF NOT EXISTS conversation_delegations (
	 parent_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	 child_id BIGINT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
	 state TEXT NOT NULL DEFAULT 'running', created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')))`); err != nil {
		return err
	}
	// Interrupted children remain visible after restart; they are never silently replayed.
	_, _ = pg.Exec(`UPDATE conversation_delegations SET state='interrupted' WHERE state='running'`)
	for _, def := range []struct{ key, name, prompt string }{
		{"ctf_triage", "CTF Triage", agent.CTFTriagePrompt},
		{"ctf_planner", "CTF Planner", agent.CTFPlannerPrompt},
		{"ctf_worker", "CTF Worker", agent.CTFWorkerPrompt},
		{"ctf_writeup", "CTF WriteUP", agent.CTFWriteupPrompt},
	} {
		a, err := pg.GetAgentByKey(def.key)
		if err != nil {
			return err
		}
		if a == nil {
			a, err = pg.CreateAgent(def.key, def.name, "CTF 模式专用角色，可编辑提示词与模型配置")
			if err != nil {
				return err
			}
		}
		if err = pg.SeedPromptIfEmpty(a.ID, def.prompt); err != nil {
			return err
		}
		// Upgrade code-managed CTF prompts while preserving prompts edited in the UI.
		var current, updatedBy string
		if err = pg.QueryRow(`SELECT p.template_text,COALESCE(p.updated_by,'') FROM agents a JOIN agent_prompts p ON p.id=a.current_prompt_id WHERE a.id=$1`, a.ID).Scan(&current, &updatedBy); err != nil {
			return err
		}
		if current != def.prompt && updatedBy == "system" {
			if _, err = pg.SavePrompt(a.ID, def.prompt, "CTF workflow v2", "system"); err != nil {
				return err
			}
		}
	}
	return nil
}

func workflowMode(key string) string {
	if strings.HasPrefix(key, "ctf_") {
		return "ctf"
	}
	switch key {
	case "goals", "planner", "mainagent", "worker", "reporter":
		return "standard"
	default:
		return "custom"
	}
}

func workflowEntry(mode, key string) (string, error) {
	switch mode {
	case "ctf":
		if key != "" && key != "planner" && key != "ctf_triage" && key != "ctf_planner" {
			return "", fmt.Errorf("CTF 模式必须从探测 Agent 开始")
		}
		return "ctf_triage", nil
	case "standard":
		if key == "" {
			key = "mainagent"
		}
		if key != "mainagent" && key != "planner" {
			return "", fmt.Errorf("首条消息必须交给 planner 或 mainagent")
		}
		return key, nil
	case "custom":
		if key == "" || workflowMode(key) != "custom" {
			return "", fmt.Errorf("自定义模式必须选择一个自定义 Agent")
		}
		return key, nil
	default:
		return "", fmt.Errorf("未知模式")
	}
}

func (s *Server) conversationTools(parent *db.Conversation) func(context.Context, string) []actool.CoreTool {
	return func(ctx context.Context, key string) []actool.CoreTool {
		tools := []actool.CoreTool{}
		if key != "mainagent" && key != "planner" && key != "ctf_triage" && key != "ctf_planner" {
			return tools
		}
		tools = append(tools, wrTool("delegate_agent", "把一个明确子任务交给下一阶段 Agent，等待并返回真实结果。CTF Triage 交给 ctf_planner，CTF Planner 交给 ctf_worker 或 ctf_writeup；普通模式可交给 worker、reporter 或自定义执行 Agent。",
			objSchema(map[string]any{"agent_key": strParam("目标 Agent key"), "task": strParam("子任务与验收标准，包含输入材料绝对路径")}, "agent_key", "task"),
			func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
				var req struct {
					AgentKey string `json:"agent_key"`
					Task     string `json:"task"`
				}
				if err := json.Unmarshal(raw, &req); err != nil {
					return actool.Errorf("invalid delegation"), nil
				}
				if key == "ctf_planner" && req.AgentKey == "ctf_worker" {
					return actool.Errorf("use delegate_agents to dispatch 2 to 6 independent ctf_worker tasks in parallel"), nil
				}
				return s.delegateConversation(ctx, parent, req.AgentKey, req.Task)
			}))
		if key == "ctf_planner" {
			tools = append(tools, s.parallelCTFDelegationTool(parent))
		}
		return tools
	}
}

func (s *Server) parallelCTFDelegationTool(parent *db.Conversation) actool.CoreTool {
	taskSchema := objSchema(map[string]any{
		"agent_key": strParam("目标 Agent key；并行分析应使用 ctf_worker"),
		"task":      strParam("不同角度的子任务、goal、材料路径与验收标准"),
	}, "agent_key", "task")
	return wrTool("delegate_agents", "并行执行 2 至 6 个相互独立的 CTF 子任务，并按输入顺序返回全部真实结果。",
		objSchema(map[string]any{"tasks": map[string]any{"type": "array", "minItems": 2, "maxItems": 6, "items": taskSchema}}, "tasks"),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var req struct {
				Tasks []struct {
					AgentKey string `json:"agent_key"`
					Task     string `json:"task"`
				} `json:"tasks"`
			}
			if json.Unmarshal(raw, &req) != nil || len(req.Tasks) < 2 || len(req.Tasks) > 6 {
				return actool.Errorf("tasks must contain 2 to 6 items"), nil
			}
			// Validate the entire batch before starting any child.
			seen := map[string]bool{}
			for _, task := range req.Tasks {
				text := strings.TrimSpace(task.Task)
				if task.AgentKey != "ctf_worker" || text == "" || len(task.Task) > 64000 || seen[text] {
					return actool.Errorf("each task must target ctf_worker with a distinct, nonempty task of at most 64000 bytes"), nil
				}
				seen[text] = true
			}
			type item struct {
				AgentKey string `json:"agent_key"`
				Result   string `json:"result"`
				IsError  bool   `json:"is_error"`
			}
			results := make([]item, len(req.Tasks))
			var wg sync.WaitGroup
			for i := range req.Tasks {
				i := i
				wg.Add(1)
				go func() {
					defer wg.Done()
					res, err := s.delegateConversation(ctx, parent, req.Tasks[i].AgentKey, req.Tasks[i].Task)
					results[i].AgentKey = req.Tasks[i].AgentKey
					if err != nil {
						results[i].Result, results[i].IsError = err.Error(), true
						return
					}
					results[i].Result, results[i].IsError = res.Flatten(), res.IsError
				}()
			}
			wg.Wait()
			return jsonResult(map[string]any{"results": results})
		})
}

func (s *Server) delegateConversation(ctx context.Context, parent *db.Conversation, key, task string) (actool.Result, error) {
	if strings.TrimSpace(task) == "" || len(task) > 64000 {
		return actool.Errorf("task is empty or too long"), nil
	}
	if key == parent.AgentKey || key == "planner" || key == "mainagent" || key == "ctf_triage" || key == "goals" {
		return actool.Errorf("cannot delegate back to a coordinator"), nil
	}
	if parent.AgentKey == "ctf_triage" && key != "ctf_planner" {
		return actool.Errorf("CTF triage target: ctf_planner"), nil
	}
	if parent.AgentKey == "ctf_planner" && key != "ctf_worker" && key != "ctf_writeup" {
		return actool.Errorf("CTF planner targets: ctf_worker, ctf_writeup"), nil
	}
	a, err := s.m.pg.GetAgentByKey(key)
	if err != nil || a == nil || !a.Enabled || s.m.pg.AgentArchived(key) {
		return actool.Errorf("target agent unavailable"), nil
	}
	if err := ctx.Err(); err != nil {
		return actool.Errorf("delegation cancelled"), nil
	}
	settings := s.personalConversationSettings(parent.ID)
	// Match the parent's effective model, not a mutable global default.
	_, profile, ok := s.personalProfileConfig(parent)
	if !ok {
		return actool.Errorf("parent model unavailable"), nil
	}
	var pin *int64
	if profile > 0 {
		pin = &profile
	}
	child, err := s.m.pg.CreateConversation(key, firstLine(task, 80), pin, settings)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	if _, err = s.m.pg.Exec(`INSERT INTO conversation_delegations(parent_id,child_id) VALUES($1,$2)`, parent.ID, child.ID); err != nil {
		return actool.Errorf(err.Error()), nil
	}
	state := "failed"
	defer func() {
		_, _ = s.m.pg.Exec(`UPDATE conversation_delegations SET state=$2 WHERE child_id=$1`, child.ID, state)
	}()
	_, _ = s.m.pg.AppendConvActivity(parent.ID, db.Activity{Worker: parent.AgentKey, Kind: "text", Summary: fmt.Sprintf("已分派给 %s · 会话 #%d", key, child.ID)})
	_, _ = s.m.pg.AppendConvActivity(child.ID, db.Activity{Worker: key, Kind: "user", Summary: firstLine(task, 200), Detail: task})
	ca := s.personalChatAgent(child)
	if ca == nil {
		return actool.Errorf("child model unavailable"), nil
	}
	ca = ca.WithExtraTools(s.conversationTools(child))
	budget := time.Duration(a.RunSecs) * time.Second
	if budget <= 0 || budget > 30*time.Minute {
		budget = 30 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	childCtx, stop := context.WithCancelCause(intercept.WithConvID(runCtx, child.ID))
	defer stop(nil)
	busyKey := s.convBusyKey(child.ID)
	s.chatMu.Lock()
	s.chatBusy[busyKey] = true
	s.chatCancel[busyKey] = stop
	s.chatMu.Unlock()
	defer func() { s.chatMu.Lock(); delete(s.chatBusy, busyKey); delete(s.chatCancel, busyKey); s.chatMu.Unlock() }()
	result, err := ca.Chat(childCtx, key, busyKey, task, a.MaxTurns, budget, a.WebSearch, func(rec db.Activity) { _, _ = s.m.pg.AppendConvActivity(child.ID, rec) })
	if err != nil {
		if childCtx.Err() != nil {
			state = "cancelled"
		}
		return actool.Errorf(fmt.Sprintf("child conversation #%d: %v", child.ID, err)), nil
	}
	state = "completed"
	return jsonResult(map[string]any{"conversation_id": child.ID, "agent_key": key, "state": state, "result": result})
}

func (s *Server) conversationDelegations(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid conversation")
		return
	}
	rows, err := pg.Query(`WITH RECURSIVE descendants(child_id) AS (
	 SELECT child_id FROM conversation_delegations WHERE parent_id=$1
	 UNION
	 SELECT d.child_id FROM conversation_delegations d JOIN descendants p ON d.parent_id=p.child_id
	) SELECT c.id,c.agent_key,c.title,d.state,d.parent_id FROM descendants tree
	 JOIN conversation_delegations d ON d.child_id=tree.child_id
	 JOIN conversations c ON c.id=d.child_id ORDER BY c.id`, id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type item struct {
		ID       int64  `json:"id"`
		AgentKey string `json:"agent_key"`
		Title    string `json:"title"`
		State    string `json:"state"`
		ParentID int64  `json:"parent_id"`
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.ID, &v.AgentKey, &v.Title, &v.State, &v.ParentID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
