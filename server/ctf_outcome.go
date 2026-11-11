package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

type ctfEvidenceRef struct {
	ConversationID int64  `json:"conversation_id"`
	ID             string `json:"id"`
}

const ctfTreeQuery = `WITH RECURSIVE tree(id) AS (SELECT $1 UNION SELECT d.child_id FROM conversation_delegations d JOIN tree ON d.parent_id=tree.id)`

type ctfOutcome struct {
	State        string           `json:"state"`
	Summary      string           `json:"summary,omitempty"`
	Criterion    string           `json:"criterion,omitempty"`
	Verification string           `json:"verification,omitempty"`
	Evidence     []ctfEvidenceRef `json:"evidence,omitempty"`
	Artifacts    []string         `json:"artifacts,omitempty"`
}

func (s *Server) readCTFOutcome(id int64) ctfOutcome {
	o := ctfOutcome{State: "unverified"}
	var raw string
	if err := s.m.pg.QueryRow(`SELECT report_json FROM conversation_outcomes WHERE conversation_id=$1`, id).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &o)
	}
	return o
}

func (s *Server) ctfTreeIDs(id int64) ([]int64, error) {
	rows, err := s.m.pg.Query(ctfTreeQuery+` SELECT id FROM tree`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var v int64
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		ids = append(ids, v)
	}
	return ids, rows.Err()
}

func (s *Server) ctfOutcomeTool(c *db.Conversation) actool.CoreTool {
	ref := objSchema(map[string]any{"conversation_id": map[string]any{"type": "integer", "description": "证据所属会话编号；自己的证据可省略"}, "id": strParam("真实工具证据 t... 编号")}, "id")
	return wrTool("report_outcome", "记录本次目标状态，与运行是否结束独立。verified 表示 Agent 已按 criterion 实际验证，须填写 verification 并引用可回查成功工具证据；文件存在和候选 flag 都不足以证明成功。", objSchema(map[string]any{
		"state":   map[string]any{"type": "string", "enum": []string{"unverified", "partial", "blocked", "verified"}},
		"summary": strParam("结论或实际阻塞点"), "criterion": strParam("本子任务可验证的验收条件"), "verification": strParam("实际验证方式和观测结果；不得把推断写成验证"),
		"evidence":  map[string]any{"type": "array", "maxItems": 16, "items": ref},
		"artifacts": map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string"}, "description": "自己工作目录中实际产物的相对路径"},
	}, "state", "summary", "criterion"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
		if ctx.Err() != nil {
			return actool.Errorf("run cancelled"), nil
		}
		var outcome ctfOutcome
		if len(raw) > 16384 || json.Unmarshal(raw, &outcome) != nil {
			return actool.Errorf("invalid outcome"), nil
		}
		if err := s.validateCTFOutcome(c.ID, &outcome); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		body, _ := json.Marshal(outcome)
		if _, err := s.m.pg.Exec(`INSERT INTO conversation_outcomes(conversation_id,report_json) VALUES($1,$2) ON CONFLICT(conversation_id) DO UPDATE SET report_json=excluded.report_json`, c.ID, string(body)); err != nil {
			return actool.Errorf("无法保存目标状态"), nil
		}
		return jsonResult(outcome)
	})
}

func (s *Server) validateCTFOutcome(id int64, o *ctfOutcome) error {
	switch o.State {
	case "unverified", "partial", "blocked", "verified":
	default:
		return fmt.Errorf("invalid outcome state")
	}
	if strings.TrimSpace(o.Summary) == "" || strings.TrimSpace(o.Criterion) == "" || len(o.Summary) > 4000 || len(o.Criterion) > 2000 || len(o.Verification) > 4000 || len(o.Evidence) > 16 || len(o.Artifacts) > 16 {
		return fmt.Errorf("填写简短结论与验收条件；证据和产物各最多16项")
	}
	if o.State == "verified" && (strings.TrimSpace(o.Verification) == "" || len(o.Evidence) == 0) {
		return fmt.Errorf("verified 需要实际验证说明和成功工具证据；证据不足请用 partial/unverified")
	}
	ids, err := s.ctfTreeIDs(id)
	if err != nil {
		return err
	}
	allowed := map[int64]bool{}
	for _, v := range ids {
		allowed[v] = true
	}
	success := false
	for i, ref := range o.Evidence {
		if ref.ConversationID == 0 {
			ref.ConversationID = id
			o.Evidence[i] = ref
		}
		if !allowed[ref.ConversationID] {
			return fmt.Errorf("证据会话 #%d 不属于当前任务", ref.ConversationID)
		}
		memory, err := agent.NewResearchMemory(s.conversationWorkDir(ref.ConversationID))
		if err != nil {
			return fmt.Errorf("无法读取证据目录")
		}
		state, err := memory.Snapshot()
		if err != nil {
			return err
		}
		found := false
		for _, obs := range state.Observations {
			if obs.ID != ref.ID {
				continue
			}
			root, err := os.OpenRoot(memory.EvidenceRoot())
			if err != nil {
				return err
			}
			f, err := root.Open(obs.Path)
			root.Close()
			if err != nil {
				return fmt.Errorf("证据 %s 已不可读取", ref.ID)
			}
			h := sha256.New()
			n, copyErr := io.Copy(h, io.LimitReader(f, 64<<10))
			f.Close()
			if copyErr != nil || n != obs.Bytes || hex.EncodeToString(h.Sum(nil)) != obs.SHA256 {
				return fmt.Errorf("证据 %s 内容发生变化", ref.ID)
			}
			found = true
			// Reading a prior claim or a delegation receipt does not independently
			// establish success. The verification must cite an actual observation.
			if !obs.IsError && !strings.HasPrefix(obs.Tool, "research_") && obs.Tool != "report_outcome" && !strings.HasPrefix(obs.Tool, "delegate_") && obs.Tool != "wait_agents" && obs.Tool != "cancel_agent" {
				success = true
			}
			break
		}
		if !found {
			return fmt.Errorf("证据 %s 不存在，请用 research_recall 查询真实编号", ref.ID)
		}
	}
	if o.State == "verified" && !success {
		return fmt.Errorf("verified 需要成功的实际工具观察，不能只引用派发或报告")
	}
	for _, path := range o.Artifacts {
		if !filepath.IsLocal(path) || strings.Contains(path, "\\") {
			return fmt.Errorf("产物必须使用工作目录内的相对路径")
		}
		root, err := os.OpenRoot(s.conversationWorkDir(id))
		if err != nil {
			return err
		}
		info, err := root.Stat(path)
		root.Close()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("产物 %s 不存在或不是普通文件", path)
		}
	}
	o.Summary = agent.RedactResearchText(o.Summary)
	o.Criterion = agent.RedactResearchText(o.Criterion)
	o.Verification = agent.RedactResearchText(o.Verification)
	return nil
}
