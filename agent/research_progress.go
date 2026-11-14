package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

type researchProgressObservation struct {
	signature, evidence string
	failed              bool
}

// Normalize only tool-owned HTTP metadata. Arbitrary output is hashed exactly;
// fuzzy text matching would confuse different successful observations.
func progressSignature(name, result string) string {
	var response httpObservation
	if name == "WebFetch" && json.Unmarshal([]byte(result), &response) == nil && response.SHA256 != "" {
		result = fmt.Sprintf("%d/%d/%s/%t", response.Status, response.BodyBytes, response.SHA256, response.BodyTruncated)
	}
	h := sha256.Sum256([]byte(name + "\x00" + result))
	return hex.EncodeToString(h[:])
}

// Called with r.mu held. This is a soft review gate, not a semantic verdict.
// It never blocks an otherwise permitted call or starts extra model requests.
func (r *researchRuntime) observeProgress(ctx context.Context, name, result, id string, failed bool) string {
	r.stageSteps++
	sig := progressSignature(name, result)
	r.recent = append(r.recent, researchProgressObservation{sig, id, failed})
	if len(r.recent) > 8 {
		r.recent = r.recent[len(r.recent)-8:]
	}
	repeats, failures := 0, 0
	ids := []string{}
	for _, o := range r.recent {
		if o.signature == sig {
			repeats++
		}
		if o.failed {
			failures++
		}
		if o.evidence != "" {
			ids = append(ids, o.evidence)
		}
	}
	stalled := repeats >= 3 || failures >= 4
	if r.stageSteps-r.lastReview < 4 || (!stalled && r.stageSteps%8 != 0) {
		return ""
	}
	r.lastReview = r.stageSteps
	summary := fmt.Sprintf("自动阶段检查点：已执行 %d 次非轮询工具；最近 %d 次中失败 %d 次，本次响应相同 %d 次。此记录仅描述调用结果，不确认假设或目标完成。", r.stageSteps, len(r.recent), failures, repeats)
	note := "\n阶段复核：用 research_record 简记当前假设、证据、反证和下一步可区分变量。"
	if stalled {
		note += "跨参数结果重复或多次失败：先检查 Shell/代理/证书/重定向与初始状态，再选择一个能区分假设的对照实验；没有新证据就换来源或明确阻塞，不把相同响应当进展。"
	}
	if len(ids) > 0 {
		_, err := r.memory.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "hypothesis", Summary: summary, EvidenceIDs: ids}})
		if err != nil {
			note += " 自动检查点保存失败: " + err.Error()
		}
	}
	return note
}

func researchExperimentTool(memory *ResearchMemory) actool.CoreTool {
	return actool.Build(actool.Spec{Name: "research_experiment", Description: "开始可区分假设的实验：记录假设、改变变量、预期观测，分配唯一目录和产物前缀，避免前一实验遗留文件污染验证。目录隔离不等于远程状态已复位；必须先记录基线，再执行实验并用新证据验证。", Schema: map[string]any{
		"type": "object", "properties": map[string]any{"hypothesis": map[string]any{"type": "string"}, "changed_variable": map[string]any{"type": "string"}, "prediction": map[string]any{"type": "string"}}, "required": []string{"hypothesis", "changed_variable", "prediction"}},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Decision{Behavior: permission.Allow}
		},
		Run: func(ctx context.Context, raw json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var in struct {
				Hypothesis      string `json:"hypothesis"`
				ChangedVariable string `json:"changed_variable"`
				Prediction      string `json:"prediction"`
			}
			if len(raw) > 6000 || json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Hypothesis) == "" || strings.TrimSpace(in.ChangedVariable) == "" || strings.TrimSpace(in.Prediction) == "" {
				return actool.Errorf("需要简短假设、改变变量和预期观测（最多6000字节）"), nil
			}
			id := "exp-" + rand.Text()
			path := filepath.Join(".research", "experiments", id)
			root, err := os.OpenRoot(memory.root)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			defer root.Close()
			if err = root.MkdirAll(path, 0700); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			_, err = memory.Record(ctx, ResearchRecordInput{Entry: &ResearchEntryInput{Kind: "hypothesis", Scope: id, Summary: in.Hypothesis + "；改变变量：" + in.ChangedVariable + "；预期：" + in.Prediction}})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out, _ := json.Marshal(map[string]string{"experiment_id": id, "artifact_dir": filepath.ToSlash(path), "artifact_prefix": id, "next": "先检查本实验唯一产物不存在并记录基线；远程实验使用此唯一前缀或经验证的状态复位。重新读取已有产物只能证明观察可重复，不能声称独立复现。"})
			return actool.Text(string(out)), nil
		},
	})
}
