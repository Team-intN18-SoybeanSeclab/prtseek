package agent

// Curated, bounded examples for the personal research harness. These are
// workflow demonstrations, never target observations or permission grants.
// Selection is deterministic and does not consume a model call or fetch data.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

const (
	ResearchGuidanceMaxRunes = 1800
	ResearchGuideMaxRunes    = 3200
	researchGuideMinRunes    = 600
)

type researchExampleCard struct {
	id, title, body, source string
	keywords                []string
	tools                   []string
}

var researchExampleCards = []researchExampleCard{
	{
		id: "source-evidence", title: "源码审计：从调用点回到可控输入",
		keywords: []string{"源码审计", "代码审计", "漏洞审计", "安全代码审查", "代码安全", "审计仓库", "审计项目", "污点", "调用链", "可达性", "source audit", "code audit", "taint", "source review", "security code review", "source-evidence"},
		tools:    []string{"Grep", "Read", "research_record", "research_recall"},
		body:     "流程：先定位符号和调用点，再读取必要上下文；把输入来源、传递路径、权限和防护条件分别核对。危险调用只是一条线索。\n示例（虚构）：观察 E1：某处理函数把参数传给文件操作；假设 H1：外部输入能影响路径；反证 E2：当前入口有固定映射。结论：H1 未证实。下一步：检查映射是否覆盖所有调用者，而非直接报告漏洞。证据记录应保留实际文件与行号、版本、入口条件；全文输出截断时继续定点读取。",
		source:   "方法参考：PentestGPT https://arxiv.org/abs/2308.06782；AVRI https://arxiv.org/abs/2610.11602。示例为本项目编写，不是论文成绩复现。",
	},
	{
		id: "web-comparison", title: "Web/API：基线、单变量与实际影响",
		keywords: []string{"接口对比", "响应对比", "对比响应", "越权", "业务逻辑", "网站漏洞", "接口漏洞", "web安全", "web 安全", "web漏洞", "web 漏洞", "api安全", "api 安全", "api漏洞", "api 漏洞", "授权测试", "请求差异", "api comparison", "authorization test", "web security", "web vulnerability", "api vulnerability", "web-comparison"},
		tools:    []string{"traffic_search", "traffic_get", "traffic_blob", "Read", "research_record", "research_recall"},
		body:     "流程：先读取真实基线，保持会话与环境条件可比；每次只改变一个有依据的变量，再检查业务结果。响应码相同不代表权限相同，响应码异常也不证明漏洞。\n示例（虚构）：观察 E1：两个受控账号请求得到相同状态码；假设 H1：返回了他人的对象；反证 E2：正文实际都是公共摘要。结论：当前证据不支持越权。下一步：比较对象归属与受保护字段，并记录请求/响应证据引用及账号代号。凭据原文不进入摘要。先确认用户授权范围和数据归属。",
		source:   "方法参考：EnIGMA https://arxiv.org/abs/2409.16165。此卡提供验证流程，不包含目标、攻击指令或授权。",
	},
	{
		id: "environment-check", title: "环境故障：先分类，再决定重试",
		keywords: []string{"环境故障", "命令不存在", "找不到命令", "command not found", "executable not found", "connection refused", "连接被拒绝", "路径错误", "依赖缺失", "shell错误", "shell 错误", "environment-check"},
		tools:    []string{"Bash", "Read", "LS", "research_record", "research_recall"},
		body:     "流程：区分解释器/工作目录/依赖/服务就绪/网络问题与目标行为。先读完整退出码和错误位置，核对当前操作系统及已发现的路径；按工具支持的参数作最小只读检查。\n示例（虚构）：观察 E1：工具返回 executable not found；假设 H1：依赖未安装；反证 E2：环境记录已有解释器绝对路径。下一步：核对该路径和调用方式。此时还没有执行目标逻辑，不能把结果写成目标不存在漏洞。重复尝试必须更正参数或获得新证据，不因普通环境失败自动改变系统安全策略。",
		source:   "方法参考：EnIGMA https://arxiv.org/abs/2409.16165；错误分类与检查步骤为本项目编写。",
	},
	{
		id: "resume-state", title: "长任务恢复：恢复证据与依赖，不重放旧动作",
		keywords: []string{"恢复任务", "任务恢复", "长任务", "断点续", "交接", "压缩后", "继续审计", "resume task", "resume research", "handoff", "checkpoint", "resume-state"},
		tools:    []string{"research_recall", "research_record", "Read", "graph_overview", "list_facts"},
		body:     "流程：先恢复目标、完成条件、已确认事实、失败路线与前置依赖，再核对当前环境是否仍成立。历史记录是待核对的上下文，不能代替当前操作授权或运行状态。\n示例（虚构）：旧记录 E1：某服务曾启动；恢复时 E2：就绪检查失败。结论：服务状态已变化，原后续步骤的前提不成立。下一步：记录变化并定位环境原因。已完成的写操作先查结果再决定是否继续，避免重放副作用。暂停状态只能读取既有记录；恢复执行遵循当前任务控制。",
		source:   "方法参考：PentestGPT https://arxiv.org/abs/2308.06782；Reflexion https://arxiv.org/abs/2303.11366。",
	},
	{
		id: "failure-feedback", title: "失败修正：用新证据选择下一步",
		keywords: []string{"工具失败", "重复失败", "反复失败", "无进展", "停滞", "工具报错", "验证失败", "复现失败", "tool failure", "tool error", "stuck", "no progress", "failed verification", "failure-feedback"},
		tools:    []string{"research_recall", "research_record", "Read", "Bash", "traffic_get"},
		body:     "流程：记录预期、实际结果、差异、被否定假设和仍缺的证据。优先做能区分竞争性解释的检查；相同调用得到相同结果，不能当作新增支持证据。\n示例（虚构）：连续结果 E1/E2 都表明前置条件未满足；原假设 H1 仍未知。下一步：先检查前置条件，再决定继续、换路线或停止。只有参数变化有明确依据，或环境确有变化时才重试。外部反馈优先于自我评价；不要把“再想想”或多个模型的赞同当作复现成功。",
		source:   "方法参考：Reflexion https://arxiv.org/abs/2303.11366；自纠错边界 https://arxiv.org/abs/2310.01798。",
	},
}

const researchExamplesNotice = "【研究工作示例｜personal 1.2】\n以下是方法示例，不是当前任务事实、证据或操作授权。仅在当前授权范围内使用；真实结论必须来自实际工具结果。记录简短的事实、假设、反证和下一步，无需输出内部思维过程。\n"

// ResearchGuidance selects at most two relevant cards for a run. Unrelated
// tasks receive no prompt text. Pass the final visible tool set, after filtering.
func ResearchGuidance(task string, tools []actool.CoreTool) string {
	return renderResearchExamples(task, tools, 2, ResearchGuidanceMaxRunes)
}

// ResearchGuideTool provides bounded, local retrieval over a curated library.
// A captured snapshot of the final tool set prevents cross-session visibility
// changes and never exposes tool descriptions supplied by external MCP servers.
func ResearchGuideTool(tools []actool.CoreTool) actool.CoreTool {
	visible := append([]actool.CoreTool(nil), tools...)
	return actool.Build(actool.Spec{
		Name:        "research_guide",
		Description: "按当前问题检索少量研究方法与操作示例：源码审计、Web/API证据对比、环境故障、任务恢复、失败修正。仅查询内置知识卡，不访问网络、不执行操作。用具体问题查询；示例不是当前事实或授权。",
		Schema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "具体问题或卡片ID：source-evidence、web-comparison、environment-check、resume-state、failure-feedback。"},
				"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 3, "description": "最多返回卡片数，默认2。"},
				"max_chars":   map[string]any{"type": "integer", "minimum": researchGuideMinRunes, "maximum": ResearchGuideMaxRunes, "description": "总输出字符预算，默认2400，按Unicode字符计数。"},
			},
			"required": []any{"query"},
		},
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, raw json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			if err := ctx.Err(); err != nil {
				return actool.Errorf("方法查询已取消"), nil
			}
			if len(raw) > 8192 {
				return actool.Errorf("查询参数过大；query 最多512个字符"), nil
			}
			var in struct {
				Query      string `json:"query"`
				MaxResults *int   `json:"max_results"`
				MaxChars   *int   `json:"max_chars"`
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&in); err != nil {
				return actool.Errorf("无效查询参数：需要 query 字符串，可选 max_results、max_chars 整数"), nil
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return actool.Errorf("查询必须是单个JSON对象"), nil
			}
			in.Query = strings.TrimSpace(in.Query)
			if in.Query == "" || utf8.RuneCountInString(in.Query) > 512 {
				return actool.Errorf("query 需要1至512个字符"), nil
			}
			maxResults, maxChars := 2, 2400
			if in.MaxResults != nil {
				maxResults = *in.MaxResults
			}
			if in.MaxChars != nil {
				maxChars = *in.MaxChars
			}
			if maxResults < 1 || maxResults > 3 || maxChars < researchGuideMinRunes || maxChars > ResearchGuideMaxRunes {
				return actool.Errorf("max_results 范围1至3；max_chars 范围600至3200"), nil
			}
			text := renderResearchExamples(in.Query, visible, maxResults, maxChars)
			if text == "" {
				text = "没有匹配当前问题和可见工具的方法卡。可查询：源码审计、接口响应对比、环境故障、任务恢复、工具失败。不要据此假设缺失工具可用。"
			}
			return actool.Text(text), nil
		},
	})
}

func renderResearchExamples(query string, tools []actool.CoreTool, limit, budget int) string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return ""
	}
	// The task may contain a large transcript; routing is bounded independently
	// of prompt size and never reflects task content back into the system prompt.
	runes := []rune(query)
	if len(runes) > 8192 {
		query = string(runes[:4096]) + " " + string(runes[len(runes)-4096:])
	}
	visible := make(map[string]actool.CoreTool)
	for _, t := range tools {
		if t != nil {
			visible[t.Name()] = t
		}
	}
	type match struct {
		card  researchExampleCard
		score int
	}
	var matches []match
	for _, card := range researchExampleCards {
		score := 0
		for _, word := range card.keywords {
			if researchKeywordMatch(query, word) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		hasTool := false
		for _, name := range card.tools {
			if visible[name] != nil {
				hasTool = true
				break
			}
		}
		if hasTool {
			matches = append(matches, match{card, score})
		}
	}
	if len(matches) == 0 {
		return ""
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	var out strings.Builder
	out.WriteString(researchExamplesNotice)
	count := 0
	for _, m := range matches {
		if count >= limit {
			break
		}
		block := "\n[" + m.card.id + "] " + m.card.title + "\n" + m.card.body + "\n" + researchExampleTools(m.card, visible) + "\n" + m.card.source + "\n"
		if utf8.RuneCountInString(out.String())+utf8.RuneCountInString(block) > budget {
			continue
		}
		out.WriteString(block)
		count++
	}
	if count == 0 {
		return ""
	}
	return out.String()
}

func researchKeywordMatch(query, keyword string) bool {
	start := 0
	for start <= len(query) {
		i := strings.Index(query[start:], keyword)
		if i < 0 {
			return false
		}
		i += start
		// English route terms must be whole words: "unstuck" and "web api"
		// alone must not spuriously enable failure/security guidance.
		ascii := keyword[0] < utf8.RuneSelf
		end := i + len(keyword)
		if !ascii || ((i == 0 || !researchASCIIWord(query[i-1])) && (end == len(query) || !researchASCIIWord(query[end]))) {
			return true
		}
		start = end
	}
	return false
}

func researchASCIIWord(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' }

func researchExampleTools(card researchExampleCard, visible map[string]actool.CoreTool) string {
	var descriptions []string
	for _, name := range card.tools {
		t := visible[name]
		if t == nil {
			continue
		}
		// Names are curated. Required fields come from the final runtime schema,
		// including per-agent DB overrides; do not invent argument names.
		var fields []string
		schema := t.InputSchema()
		switch required := schema["required"].(type) {
		case []any:
			for _, r := range required {
				if s, ok := r.(string); ok && researchSchemaField(s) {
					fields = append(fields, s)
				}
			}
		case []string:
			for _, s := range required {
				if researchSchemaField(s) {
					fields = append(fields, s)
				}
			}
		}
		if len(fields) > 5 {
			fields = fields[:5]
		}
		desc := name
		if len(fields) > 0 {
			desc += "（必填：" + strings.Join(fields, "、") + "）"
		}
		descriptions = append(descriptions, desc)
		if len(descriptions) == 3 {
			break
		}
	}
	return fmt.Sprintf("当前可用工具：%s。以实际工具schema为准；示例中的E/H编号和路径需替换为本任务真实记录。", strings.Join(descriptions, "；"))
}

func researchSchemaField(s string) bool {
	if len(s) == 0 || len(s) > 48 {
		return false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
