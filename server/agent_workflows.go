package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	actool "github.com/Autumn-27/norma/tool"
)

var defaultCTFAgents = map[string]bool{
	"triage":  true,
	"command": true,
	"solver":  true,
	"writeup": true,
}

func isDefaultCTFAgent(key string) bool { return defaultCTFAgents[key] }

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
	// Retire the short-lived prefixed workflow without deleting its historical
	// conversations. Fresh operational roles use concise, user-editable keys.
	for _, legacy := range []string{"ctf_triage", "ctf_planner", "ctf_worker", "ctf_verifier", "ctf_writeup"} {
		if a, _ := pg.GetAgentByKey(legacy); a != nil && !pg.AgentArchived(legacy) {
			_ = pg.ArchiveAgent(legacy)
		}
	}
	for _, def := range []struct{ key, name, description, prompt string }{
		{"triage", "CTF Triage", "CTF 入口与材料盘点：识别题型、附件、授权范围和 flag 线索。", agent.CTFTriagePrompt},
		{"command", "CTF Command", "CTF 编排与任务分解：建立目标、并行派发 Solver 并收敛证据。", agent.CTFPlannerPrompt},
		{"solver", "CTF Solver", "CTF 解题执行：分析分派材料、验证链路并回传可复现证据。", agent.CTFWorkerPrompt},
		{"writeup", "CTF WriteUP", "CTF 交付归档：基于真实证据整理 WRITEUP、EXP 与复现步骤。", agent.CTFWriteupPrompt},
	} {
		a, err := pg.GetAgentByKey(def.key)
		if err != nil {
			return err
		}
		if a == nil {
			a, err = pg.CreateAgent(def.key, def.name, def.description)
			if err != nil {
				return err
			}
		} else if a.Description == "" || a.Description == "CTF 模式专用角色，可编辑提示词与模型配置" || a.Description == "默认 CTF 编组角色；可配置提示词、模型与可见资源。" {
			if err = pg.UpdateAgentMeta(a.Key, a.Name, def.description); err != nil {
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
			if _, err = pg.SavePrompt(a.ID, def.prompt, "CTF workflow v4", "system"); err != nil {
				return err
			}
		}
	}
	return nil
}

// workflowMode keeps the conversation surface CTF-only without imposing one
// rigid agent graph: the operator can choose any enabled CTF-role/custom agent.
func workflowMode(_ string) string { return "ctf" }

func workflowEntry(_, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "triage", nil
	}
	key = strings.TrimSpace(key)
	if !isDefaultCTFAgent(key) {
		return "", fmt.Errorf("仅支持默认 CTF Agent")
	}
	return key, nil
}

func (s *Server) conversationTools(parent *db.Conversation) func(context.Context, string) []actool.CoreTool {
	return func(ctx context.Context, key string) []actool.CoreTool {
		if !isDefaultCTFAgent(key) {
			return nil
		}
		tools := []actool.CoreTool{wrTool("delegate_agent", "将一个有明确目标、材料、边界与验收标准的 CTF 子任务交给默认角色之一（triage、command、solver、writeup），等待并返回真实结果。不要虚构分派。",
			objSchema(map[string]any{"agent_key": strParam("目标 Agent key"), "task": strParam("子任务与验收标准，包含输入材料绝对路径")}, "agent_key", "task"),
			func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
				var req struct {
					AgentKey string `json:"agent_key"`
					Task     string `json:"task"`
				}
				if err := json.Unmarshal(raw, &req); err != nil {
					return actool.Errorf("invalid delegation"), nil
				}
				return s.delegateConversation(ctx, parent, req.AgentKey, req.Task)
			})}
		if key == "command" {
			tools = append(tools, s.parallelCTFDelegationTool(parent))
		}
		return tools
	}
}

func (s *Server) parallelCTFDelegationTool(parent *db.Conversation) actool.CoreTool {
	taskSchema := objSchema(map[string]any{
		"agent_key": strParam("目标 Agent key；默认并行分析角色为 solver"),
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
				if task.AgentKey != "solver" || text == "" || len(task.Task) > 64000 || seen[text] {
					return actool.Errorf("each task must target solver with a distinct, nonempty task of at most 64000 bytes"), nil
				}
				seen[text] = true
			}
			type item struct {
				AgentKey string `json:"agent_key"`
				Result   any    `json:"result"`
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
					results[i].IsError = res.IsError
					var payload map[string]any
					if !res.IsError && json.Unmarshal([]byte(res.Flatten()), &payload) == nil {
						results[i].Result = payload
					} else {
						results[i].Result = res.Flatten()
					}
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
	if key == parent.AgentKey {
		return actool.Errorf("cannot delegate to the current agent"), nil
	}
	if !isDefaultCTFAgent(parent.AgentKey) || !isDefaultCTFAgent(key) {
		return actool.Errorf("only default CTF agents are available"), nil
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
	// Child agents have separate working directories. Surface the parent and
	// original session workspaces explicitly, so uploaded challenge files and
	// evidence remain discoverable even after several delegation hops.
	rootID := parent.ID
	_ = s.m.pg.QueryRow(`WITH RECURSIVE lineage(id) AS (
	 SELECT $1 UNION ALL SELECT d.parent_id FROM conversation_delegations d JOIN lineage l ON d.child_id=l.id
	) SELECT id FROM lineage WHERE id NOT IN (SELECT child_id FROM conversation_delegations) LIMIT 1`, parent.ID).Scan(&rootID)
	parentDir := filepath.Join(s.m.dir, "sessions", s.convBusyKey(parent.ID))
	workspaces := "父会话材料目录（只读引用，产物写入自己的工作目录）：" + parentDir
	if rootID != parent.ID {
		workspaces += "\n原始题目/上传附件目录：" + filepath.Join(s.m.dir, "sessions", s.convBusyKey(rootID))
	}
	childTask := task + "\n\n" + workspaces + "\n先核对实际文件与证据；不要假设目录中存在尚未验证的材料。"
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
	_, _ = s.m.pg.AppendConvActivity(child.ID, db.Activity{Worker: key, Kind: "user", Summary: firstLine(task, 200), Detail: childTask})
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
	result, err := ca.Chat(childCtx, key, busyKey, childTask, a.MaxTurns, budget, a.WebSearch, func(rec db.Activity) { _, _ = s.m.pg.AppendConvActivity(child.ID, rec) })
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
		ID         int64         `json:"id"`
		AgentKey   string        `json:"agent_key"`
		Title      string        `json:"title"`
		State      string        `json:"state"`
		ParentID   int64         `json:"parent_id"`
		Running    bool          `json:"running"`
		Activities []ActivityDTO `json:"activities"`
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
	// The delegation list is also the live operator feed for a parent session.
	// A bounded tail keeps polling cheap while still exposing the latest worker
	// reasoning, tool calls and result messages without opening child tabs.
	s.chatMu.Lock()
	for i := range items {
		items[i].Running = s.chatBusy[s.convBusyKey(items[i].ID)]
		if items[i].Running {
			items[i].State = "running"
		}
	}
	s.chatMu.Unlock()
	for i := range items {
		activity, _, activityErr := pg.ConvActivityPage(items[i].ID, 0, 12)
		if activityErr != nil {
			writeErr(w, 500, activityErr.Error())
			return
		}
		items[i].Activities = make([]ActivityDTO, 0, len(activity))
		for _, record := range activity {
			items[i].Activities = append(items[i].Activities, activityDTO(record))
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
