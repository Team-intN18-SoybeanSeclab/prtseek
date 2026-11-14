package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// ChatAgent is the generic, task-independent conversational runner behind the chat
// page. It generalizes MainAgent.Chat: any agent (built-in OR a custom one, by
// key) can be chatted with, multi-turn history resumed from the transcript. It is
// a PURE ASSISTANT — base tools are the SDK DefaultTools (Bash/Read/Write/Edit/
// LS/Glob/Grep) plus whatever skills/MCP the key is made visible; NO pentest
// graph/task context is injected (that stays exclusive to MainAgent).
type ChatAgent struct {
	prov            llm.Provider
	model           string
	workDir         string
	tx              *transcript.Store
	window          int
	proxyAddr       string
	proxyCACert     string
	webSearch       WebSearchOpts
	guard           *guard.Guard // optional; nil disables intercept hooks for chat
	nonStreamingFn  func() bool  // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn    func() bool  // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn     func() int   // resolver: per-reply output cap (nil/0 = send no cap)
	extraToolsFn    func(context.Context, string) []actool.CoreTool
	completionCheck func() string
}

func (c *ChatAgent) WithCompletionCheck(check func() string) *ChatAgent {
	copy := *c
	copy.completionCheck = check
	return &copy
}

func (c *ChatAgent) WithExtraTools(fn func(context.Context, string) []actool.CoreTool) *ChatAgent {
	copy := *c
	copy.extraToolsFn = fn
	return &copy
}

func NewChatAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window int) *ChatAgent {
	return &ChatAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window}
}

// SetNonStreaming wires a resolver deciding whether chat runs use the
// non-streaming model path (true = non-streaming). nil/unset = streaming.
func (c *ChatAgent) SetNonStreaming(fn func() bool) { c.nonStreamingFn = fn }

func (c *ChatAgent) nonStreaming() bool { return c.nonStreamingFn != nil && c.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether chat runs use the experimental
// noa context-compression mechanism. nil/unset = off (built-in compaction). Read
// per run so the settings toggle takes effect without rebuilding the agent.
func (c *ChatAgent) SetNoaEnabled(fn func() bool) { c.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (c *ChatAgent) SetMaxTokens(fn func() int) { c.maxTokensFn = fn }

func (c *ChatAgent) maxTokens() int {
	if c.maxTokensFn == nil {
		return 0
	}
	return c.maxTokensFn()
}

// SetProxy points the chat agent's WebFetch/Bash at the recording proxy plus the
// CA cert it trusts (empty addr = direct). Kept for parity with the other agents.
func (c *ChatAgent) SetProxy(addr, caCert string) { c.proxyAddr, c.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the chat agent (off by default).
func (c *ChatAgent) SetWebSearch(o WebSearchOpts) { c.webSearch = o }

// SetGuard attaches a guard (with user-configured intercept rules) to this chat
// agent. Must be called before Chat; safe to call multiple times.
func (c *ChatAgent) SetGuard(g *guard.Guard) { c.guard = g }

// chatWorkDirSpec returns a working-directory notice appended to every chat
// agent's system prompt. Mirrors artifactSpec but without pentest-specific
// wording ("payload", "抓响应体") that would be odd in a general assistant.
func chatWorkDirSpec(workDir string) string {
	return "\n\n**文件输出规约**：需要写文件时，一律写到工作目录 " + workDir + "（这是默认 CWD，相对路径即落在这里，也可用该绝对路径）——不要写 /tmp 或其他绝对路径。\n" + shellEnvironment()
}

// chatSystem renders the DB-managed prompt body for agentKey. Custom agents have
// no per-key in-code default, so DefaultAssistantPrompt is the render fallback.
func chatSystem(agentKey, dataDir, workDir string) string {
	prompt := renderSystem(agentKey, DefaultAssistantPrompt, chatVars{DataDir: dataDir, Now: nowStr()})
	if agentKey == "triage" {
		prompt += "\n\n协作约定：材料足够且几步可解决时直接 delegate_agent 给 solver；需要多个互补方向时再交给 command。缺少题目或附件时明确缺口，不能只用文字声称已经分工。"
	}
	if agentKey == "command" {
		prompt += "\n\n协作约定：按复杂度使用 delegate_agents 启动 1 至 6 个互补 solver；简单题只需一个。此工具立即返回编号，使用 wait_agents 等待任一结果，及时利用突破口、用 cancel_agent 停止无价值分支，并收取取消后的结果。每项任务写清材料、目标、边界、产物和验收标准。最多同时8个、单轮总计24个子任务、深度4层，整轮共享200次模型调用；不要为用满并行度而重复分工。收敛后按需要交给 writeup。"
	}
	if agentKey == "triage" || agentKey == "command" || agentKey == "solver" || agentKey == "writeup" {
		prompt += "\n\n" + CTFEvidencePriority + "\n\n" + ctfRoleMethod(agentKey)
		prompt += "\n实验前用 research_experiment 记录假设、改变变量和预期，使用唯一目录/远程产物前缀，并先记录基线。只读到答案用 verification_level=observed；独立复现用 reproduced，提供 experiment_id、baseline_evidence、result_evidence；平台接受用 accepted 并提供 acceptance_evidence。重复读取旧产物不能证明从初始状态独立复现；平台接受必须有真实回执。阶段复核只写结论、证据、反证、下一步，不要求展示内部思维。"
		prompt += "\n\n交付前调用 report_outcome：明确验收条件、目标状态（unverified/partial/blocked/verified）、结论、证据编号及产物相对路径。verified 必须说明实际验证方法并引用成功工具证据；候选 flag 或只有推断时只能用 partial/unverified。引用子任务证据时携带真实 conversation_id。运行正常结束不代表目标已达成。"
	}
	return prompt + chatWorkDirSpec(workDir)
}

func chatTodos(ctf bool) *actool.TodoStore {
	if ctf {
		return nil
	}
	return actool.NewTodoStore()
}

// chatVars carries the runtime variables a custom agent's prompt may reference.
// DataDir (server data root) + Now (server wall-clock, refreshed each turn) are the
// universal ones; any other {{.X}} fails to render and falls back to
// DefaultAssistantPrompt.
type chatVars struct{ DataDir, Now string }

// Chat runs ONE turn of a conversation with the agent identified by agentKey,
// resuming prior history keyed by sessionID. maxTurns is the per-turn agent step
// budget (0 = unlimited). maxDuration is the wall-clock run budget per turn
// (0 = unlimited); the timer resets each time Chat is called, so a new user
// message always starts a fresh countdown. webSearch gates network search for
// THIS agent (the global backend/key still come from the chat agent's config,
// but each agent decides on/off). emit receives each execution step (thinking /
// tool_use / tool_result / text / result), tagged with the agent key as the
// worker lane.
func (c *ChatAgent) Chat(ctx context.Context, agentKey, sessionID, message string, maxTurns int, maxDuration time.Duration, webSearch bool, emit func(db.Activity)) (string, error) {
	persistentGoal := agentKey == "personal" && PersonalPersistentGoal(message)
	if persistentGoal {
		// The user explicitly requested a persistent foreground goal. Keep the
		// existing cancellation and final-answer paths, but avoid settlement
		// solely because this run reached the usual step or time budget.
		maxTurns, maxDuration = 0, 0
	}
	// gate the global web-search opts by this agent's own flag.
	ws := c.webSearch
	if !webSearch {
		ws.Enabled = false
	}

	// Per-session working directory: <workDir>/sessions/<sessionID>/
	// Isolates file writes across conversations, mirroring how workers use i<intentID>/.
	sessionWorkDir := filepath.Join(c.workDir, "sessions", sessionID)
	_ = os.MkdirAll(sessionWorkDir, 0o755)
	ctx = intercept.WithReviewWorkingDirectory(ctx, sessionWorkDir)

	// Pure assistant: DefaultTools as the base; AugmentTools layers in the key's
	// visible skills/MCP and lets the DB tools table filter/override. DefaultTools
	// have no tools-table rows, so they always pass through.
	ctf := ctfRoleMethod(agentKey) != ""
	base := actool.DefaultTools()
	if ctf {
		// Delegation, not SDK background tasks, owns CTF parallelism. Sleep and
		// TodoWrite add competing control paths without helping solve a challenge.
		base = defaultToolsExcept("Sleep")
	}
	ctx = WithRunInfo(ctx, RunInfo{SessionID: sessionID})
	base = hostTools(base, c.proxyAddr, c.proxyCACert)
	tools, def, cleanup := AugmentTools(ctx, agentKey, base)
	defer cleanup()
	if c.extraToolsFn != nil {
		tools = append(tools, c.extraToolsFn(ctx, agentKey)...)
	}

	system, boundary := deferredSystem(chatSystem(agentKey, c.workDir, sessionWorkDir)+responseLanguageBlock(ctx), def)
	opts := agentcore.Options{
		Provider:               budgetProvider(ctx, meterProvider(ctx, c.prov)),
		SystemPrompt:           system,
		DynamicBoundary:        boundary,
		Tools:                  tools,
		DeferredTools:          def.Deferred,
		UnlockSet:              def.Unlock,
		PermissionMode:         permission.ModeBypass,
		DisableBackgroundTasks: agentKey == "personal" || ctf,
		EnableWebFetch:         false, // Host adapter records redirects and bounded response evidence.
		WebFetchProxy:          c.proxyAddr,
		WebFetchCACert:         c.proxyCACert,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:       ws.Enabled,
		WebSearchBackend:      ws.Backend,
		BraveSearchAPIKey:     ws.BraveKey,
		TavilySearchAPIKey:    ws.TavilyKey,
		DeepSeekSearchBaseURL: ws.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  ws.DeepSeekAPIKey,
		DeepSeekSearchModel:   ws.DeepSeekModel,
		WebSearchProxy:        ws.Proxy,
		BashEnv:               hostHTTPEnv(c.proxyAddr, c.proxyCACert), // Same explicit route/CA as WebFetch.
		WorkingDir:            sessionWorkDir,
		MaxTurns:              maxTurns,
		MaxDuration:           maxDuration,
		Compaction:            compactionConfig(c.window),
		Todos:                 chatTodos(ctf),
		// large tool output spills to cmd-output/ under the session dir.
		// 截断上限用 SDK 默认(tool.Capture 的 30000 字符)。
		ToolOutputDir: filepath.Join(sessionWorkDir, "cmd-output"),
		// 命中预算(步数)→ SDK 跑收尾:输出一句总结。Prompt 与收尾轮数按本 agent key 后台可编辑
		// (自定义 agent 各自一份;留空/0 用通用默认:10 轮)。
		Settlement:   wrapupSettlement(agentKey, nil),
		NonStreaming: c.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    c.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
	}
	if persistentGoal {
		opts.SystemPrompt = append(opts.SystemPrompt, "本轮用户明确要求持续推进目标。不要因尝试过几个方向、写出阶段总结或保存检查点就结束；继续依据新证据探索。只有取得可验证结果、用户中止，或明确说明环境/授权/可行路线的实际阻塞时才给最终答复。")
	}
	if c.guard != nil {
		opts.Hooks = c.guard.Hooks()
	}
	if c.tx != nil { // persist raw human↔AI conversation; one accumulating file per thread
		opts.Transcript = c.tx
		opts.SessionID = sessionID
	}
	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	enableNoa(&opts, c.noaEnabledFn, c.workDir, "chat-"+sessionID, noaWarn("chat-"+sessionID))
	ctx = attachSideCapture(ctx, &opts)
	if agentKey == "triage" || agentKey == "command" || agentKey == "solver" || agentKey == "writeup" {
		ctx = WithResearchOptimization(ctx, true)
		ctx = withoutResearchGuide(ctx)
	}
	if err := AttachResearchRuntime(ctx, &opts, c.model, sessionWorkDir, message); err != nil {
		return "", err
	}
	personalSessionOptions(ctx, &opts)
	if c.completionCheck != nil {
		opts.Hooks = &completionCheckHooks{HookRunner: opts.Hooks, check: c.completionCheck}
	}
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload prior conversation so the agent has context across turns (each Chat is
	// a fresh session). First turn: no file yet → Resume loads nothing and proceeds.
	if c.tx != nil {
		if err := resumeSession(s, sessionID); err != nil {
			return "", err
		}
	}
	// re-unlock skill-gated MCPs from prior Skill() calls in the reloaded history so
	// revealed tools stay callable across the fresh session.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, reason, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = agentKey
			emit(r)
		}
	})
	if observer, ok := ctx.Value(modelObserverKey{}).(*ModelObserver); ok && observer.Terminal != nil {
		observer.Terminal(reason)
	}
	if err == nil && reason != "completed" {
		err = fmt.Errorf("执行结束：%s", reason)
	}
	return text, err
}
