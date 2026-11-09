"use client";

import * as React from "react";

import { ArrowUpIcon, BotIcon, ChevronDownIcon, ChevronRightIcon, Paperclip, Square, TerminalIcon, X, ZapIcon } from "lucide-react";
import { toast } from "sonner";

import styles from "@/components/chat-workspace.module.css";
import { ConversationList } from "@/components/conversation-list";
import { ReasoningControl } from "@/components/reasoning-control";
import { Transcript } from "@/components/transcript";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { mergeActivities } from "@/lib/activity-merge";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";
import { shouldSubmitOnKey, useChatSendMode } from "@/lib/chat-send-mode";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { hideResearchEvidenceAnnotation } from "@/lib/research-evidence";
import type { Activity, Agent, ChatAttachment, Conversation, LLMProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

import { type Delegation, type DelegationActivity, workflowsApi } from "@/lib/workflows-api";

const legacyAgentKeys = new Set(["goals", "planner", "mainagent", "worker", "reporter", "ctf_triage", "ctf_planner", "ctf_worker", "ctf_verifier", "ctf_writeup"]);

// Composer is the shared bottom input (textarea grows to a cap, Enter sends,
// Shift+Enter newlines) — the same affordance across DraftChat and ChatView.
function Composer({
  value,
  onChange,
  onSend,
  disabled,
  placeholder,
  running,
  onStop,
  files = [],
  onFilesChange,
}: {
  value: string;
  onChange: (value: string) => void;
  onSend: () => void;
  disabled: boolean;
  placeholder: string;
  running?: boolean;
  onStop?: () => void;
  files?: File[];
  onFilesChange?: (files: File[]) => void;
}) {
  const sendMode = useChatSendMode();
  const picker = React.useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = React.useState(false);
  const appendFiles = React.useCallback((incoming: File[]) => {
    if (!incoming.length) return;
    const known = new Set(files.map((file) => `${file.name}:${file.size}:${file.lastModified}`));
    onFilesChange?.([...files, ...incoming.filter((file) => !known.has(`${file.name}:${file.size}:${file.lastModified}`))]);
  }, [files, onFilesChange]);
  return (
    <div className={styles.composer}>
      {files.length > 0 && (
        <div className="flex flex-wrap gap-2 px-1 pb-2">
          {files.map((file, index) => (
            <span
              key={`${file.name}-${file.size}-${index}`}
              className="flex items-center gap-1 border px-2 py-1 text-xs"
            >
              {file.name}
              <button
                type="button"
                aria-label={`移除附件 ${file.name}`}
                onClick={() => onFilesChange?.(files.filter((_, item) => item !== index))}
              >
                <X className="size-3" />
              </button>
            </span>
          ))}
        </div>
      )}
      <div
        className={styles.composerInput}
        data-dragging={dragging || undefined}
        onDragEnter={(event) => { event.preventDefault(); if (onFilesChange) setDragging(true); }}
        onDragOver={(event) => event.preventDefault()}
        onDragLeave={(event) => { if (event.currentTarget === event.target) setDragging(false); }}
        onDrop={(event) => { event.preventDefault(); setDragging(false); appendFiles(Array.from(event.dataTransfer.files)); }}
      >
        {onFilesChange && (
          <>
            <input
              ref={picker}
              className="hidden"
              type="file"
              multiple
              onChange={(event) => {
                appendFiles(Array.from(event.target.files ?? []));
                event.target.value = "";
              }}
            />
            <Button
              type="button"
              size="icon"
              variant="ghost"
              disabled={disabled}
              onClick={() => picker.current?.click()}
              aria-label="添加附件"
            >
              <Paperclip />
            </Button>
          </>
        )}
        <textarea
          aria-label="消息"
          rows={3}
          value={value}
          placeholder={placeholder}
          className="min-w-0 flex-1 resize-none"
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={(event) => {
            if (shouldSubmitOnKey(event, sendMode)) {
              event.preventDefault();
              if (!disabled && (value.trim() || files.length > 0)) onSend();
            }
          }}
        />
        {running && (
          <Button size="icon" variant="ghost" onClick={onStop} disabled={disabled} aria-label="仅停止当前回复" title="仅停止当前回复">
            <Square />
          </Button>
        )}
        <Button
          size="icon"
          className={styles.send}
          onClick={onSend}
          disabled={disabled || (!value.trim() && files.length === 0)}
          aria-label={running ? "打断并发送消息" : "发送消息"}
          title={running ? "打断当前回复并发送" : "发送消息"}
        >
          <ArrowUpIcon />
        </Button>
      </div>
      {running && <p className={styles.interruptHint}>当前回复进行中；发送新消息会打断它。也可以只停止当前回复。</p>}
    </div>
  );
}

function AgentPicker({ agents, value, onChange, disabled }: { agents: Agent[]; value: string; onChange: (key: string) => void; disabled?: boolean }) {
  const [open, setOpen] = React.useState(false);
  const selected = agents.find((agent) => agent.key === value);
  return (
    <Popover open={open} onOpenChange={disabled ? undefined : setOpen}>
      <PopoverTrigger asChild>
        <button type="button" disabled={disabled} className={cn(styles.profileButton, "disabled:pointer-events-none disabled:opacity-40")}>
          <BotIcon className="size-3.5 shrink-0" />
          <span className="max-w-48 truncate">{selected?.name || value || "选择 Agent"}</span>
          <ChevronDownIcon className="size-3" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className={cn(styles.profileMenu, "w-72 max-w-[calc(100vw-24px)]")}>
        <p className={cn(styles.eyebrow, "px-2 py-2")}>AGENT / 入口角色</p>
        {agents.map((agent) => (
          <button key={agent.key} type="button" className={cn(styles.profileOption, agent.key === value && styles.profileOptionActive)} onClick={() => { onChange(agent.key); setOpen(false); }}>
            <span className="text-sm">{agent.name}</span><span className="text-muted-foreground text-xs">{agent.key}{agent.description ? ` · ${agent.description}` : ""}</span>
          </button>
        ))}
      </PopoverContent>
    </Popover>
  );
}

// LLMProfileRow shows the active LLM config below the composer and lets the user
// switch it via a Popover. `selected` is the profile id or null for default.
function LLMProfileRow({
  profiles,
  selected,
  onChange,
  disabled,
  rightSlot,
  effort = "",
  onEffortChange,
  effectiveModel,
}: {
  profiles: LLMProfile[];
  selected: number | null;
  onChange: (id: number | null) => void;
  disabled?: boolean;
  rightSlot?: React.ReactNode;
  effort?: string;
  onEffortChange?: (value: string) => void | Promise<void>;
  effectiveModel?: string;
}) {
  const [open, setOpen] = React.useState(false);
  const activeDefault = profiles.find((p) => p.is_default);
  const current = selected != null ? profiles.find((p) => Number(p.id) === selected) : null;
  const label = current ? current.name : `默认${activeDefault ? `（${activeDefault.name}）` : ""}`;
  const resolved = current ?? activeDefault;

  return (
    <div className={styles.profileBar}>
      <Popover open={open} onOpenChange={disabled ? undefined : setOpen}>
        <PopoverTrigger asChild>
          <button
            type="button"
            disabled={disabled}
            className={cn(styles.profileButton, "disabled:pointer-events-none disabled:opacity-40")}
            title={`${label}${effectiveModel ? ` · 实际模型 ${effectiveModel}` : ""}`}
          >
            <ZapIcon className="size-3.5 shrink-0" />
            <span className="max-w-48 truncate">
              {effectiveModel ? effectiveModel : resolved?.model ? resolved.model : "选择模型"}
            </span>
            <ChevronDownIcon className="size-3" />
          </button>
        </PopoverTrigger>
        <PopoverContent align="start" className={cn(styles.profileMenu, "w-72 max-w-[calc(100vw-24px)]")}>
          <p className={cn(styles.eyebrow, "px-2 py-2")}>MODEL / 模型配置</p>
          {/* default option */}
          <button
            type="button"
            onClick={() => {
              onChange(null);
              setOpen(false);
            }}
            className={cn(styles.profileOption, selected == null && styles.profileOptionActive)}
          >
            <span className="text-sm">默认{activeDefault ? `（${activeDefault.name}）` : ""}</span>
            {activeDefault && (
              <span className="text-muted-foreground text-xs">
                {activeDefault.format} · {activeDefault.model}
              </span>
            )}
          </button>
          {profiles.map((p) => (
            <button
              key={p.id}
              type="button"
              onClick={() => {
                onChange(Number(p.id));
                setOpen(false);
              }}
              className={cn(styles.profileOption, selected === Number(p.id) && styles.profileOptionActive)}
            >
              <span className="text-sm">{p.name}</span>
              <span className="text-muted-foreground text-xs">
                {p.format} · {p.model}
              </span>
            </button>
          ))}
          <button
            type="button"
            className="mt-1 w-full border-t px-2 py-3 text-left text-xs hover:bg-muted"
            onClick={() => {
              setOpen(false);
              window.dispatchEvent(new Event("prtseek-configure-model"));
            }}
          >
            添加模型配置…
          </button>
        </PopoverContent>
      </Popover>
      {onEffortChange && (
        <ReasoningControl
          profileId={resolved ? Number(resolved.id) : undefined}
          value={effort}
          onChange={onEffortChange}
          disabled={disabled}
        />
      )}
      {rightSlot}
    </div>
  );
}

// DraftChat is the default right-pane view: a fresh chat (agent picker in the
// header, centered empty state, composer) with NO conversation created yet. The
// conversation is created lazily on the first send (ChatGPT-style), then the
// parent switches to the real ChatView.
function DraftChat({
  agents,
  profiles,
  onStarted,
}: {
  agents: Agent[];
  profiles: LLMProfile[];
  onStarted: (c: Conversation, pending?: { input?: string; attachments?: ChatAttachment[] }) => void;
}) {
  const [agentKey, setAgentKey] = React.useState("triage");
  const [llmProfileId, setLlmProfileId] = React.useState<number | null>(null);
  const [effort, setEffort] = React.useState("");
  const [input, setInput] = React.useState("");
  const [files, setFiles] = React.useState<File[]>([]);
  const [sending, setSending] = React.useState(false);

  React.useEffect(() => {
    if (agents.some((agent) => agent.key === agentKey)) return;
    setAgentKey(agents[0]?.key ?? "");
  }, [agents, agentKey]);

  async function send() {
    const msg = input.trim();
    if ((!msg && files.length === 0) || sending) return;
    if (!agentKey) return toast.error("请先创建或恢复一个 Agent。");
    const profile =
      llmProfileId == null ? profiles.find((p) => p.is_default) : profiles.find((p) => Number(p.id) === llmProfileId);
    if (!profile?.api_key_hint) {
      window.dispatchEvent(new Event("prtseek-configure-model"));
      return;
    }
    setSending(true);
    let created: Conversation | undefined;
    try {
      const c = await api.createConversation(agentKey, "", Number(profile.id), effort);
      created = c;
      const attachments = files.length > 0 ? (await api.chatUpload("session", String(c.id), files)).attachments : [];
      await api.sendConversationMessage(c.id, msg || "请分析已附加的题目文件。", attachments);
      setFiles([]);
      onStarted({ ...c, running: true });
    } catch (e) {
      toast.error("发送失败：" + (e as Error).message);
      if (created) onStarted(created, { input });
      setSending(false);
    }
  }

  return (
    <div className="flex min-h-0 flex-1 items-center justify-center px-3 sm:px-8">
      <div className="w-full max-w-3xl">
        <Composer
          value={input}
          onChange={setInput}
          onSend={send}
          disabled={sending || !agentKey}
          placeholder="输入 CTF 题目、附件线索、目标或 flag 格式…"
          files={files}
          onFilesChange={setFiles}
        />
        <LLMProfileRow
          profiles={profiles}
          selected={llmProfileId}
          onChange={(id) => {
            setLlmProfileId(id);
            setEffort("");
          }}
          effort={effort}
          onEffortChange={setEffort}
          disabled={sending}
          rightSlot={<AgentPicker agents={agents} value={agentKey} onChange={setAgentKey} disabled={sending || agents.length === 0} />}
        />
      </div>
    </div>
  );
}

function delegationStateLabel(state: string, running: boolean) {
  if (running || state === "running") return "执行中";
  if (state === "completed") return "已完成";
  if (state === "cancelled") return "已取消";
  return "失败";
}

function delegationActivityLabel(activity: DelegationActivity) {
  const prefix = activity.kind === "tool_use" || activity.kind === "tool_result" ? activity.tool || "工具" : activity.kind;
  return `${prefix} · ${hideResearchEvidenceAnnotation(activity.summary) || "等待新的动态…"}`;
}

function DelegationConsole({ delegations }: { delegations: Delegation[] }) {
  const activeCount = delegations.filter((job) => job.running || job.state === "running").length;
  return (
    <section className={styles.delegationConsole} aria-label="子 Agent 动态">
      <div className={styles.delegationHeading}>
        <div>
          <span className={styles.delegationCode}>FIELD OPERATORS / 实时状态</span>
          <strong>子 Agent</strong>
        </div>
        <span className={styles.delegationCount} data-live={activeCount > 0}>
          <span className={styles.delegationCountDot} />
          {activeCount ? `${activeCount} 执行中` : `${delegations.length} 已记录`}
        </span>
      </div>
      <div className={styles.delegationGrid}>
        {delegations.map((job) => {
          const live = job.running || job.state === "running";
          return (
            <details key={job.id} className={styles.delegationCard} open={live || undefined}>
              <summary>
                <span className={live ? styles.delegationPulse : styles.delegationMark} />
                <BotIcon className="size-3.5 shrink-0" />
                <span className={styles.delegationAgent}>{job.agent_key}</span>
                <span className={styles.delegationState} data-live={live}>
                  {delegationStateLabel(job.state, job.running)}
                </span>
                <ChevronRightIcon className={styles.delegationChevron} />
              </summary>
              <div className={styles.delegationBody}>
                <div className={styles.delegationTaskMeta}>SESSION #{job.id} <span>· {job.activities.length} 条动态</span></div>
                <p className={styles.delegationTitle}>{job.title || "未命名子任务"}</p>
                {job.activities.length > 0 ? (
                  <ol className={styles.delegationActivity}>
                    {job.activities.slice(-8).map((activity) => (
                      <li key={activity.seq} data-error={activity.is_error || undefined}>
                        {activity.kind === "tool_use" || activity.kind === "tool_result" ? (
                          <TerminalIcon className="size-3 shrink-0" />
                        ) : (
                          <span className={styles.delegationEventDot} />
                        )}
                        <span title={hideResearchEvidenceAnnotation(activity.summary)}>{delegationActivityLabel(activity)}</span>
                      </li>
                    ))}
                  </ol>
                ) : (
                  <p className={styles.delegationEmpty}>已分派，等待 Worker 回传第一条动态。</p>
                )}
                <a className={styles.delegationLink} href={`/chat?c=${job.id}`} target="_blank" rel="noopener noreferrer">
                  查看完整子会话 ↗
                </a>
              </div>
            </details>
          );
        })}
      </div>
    </section>
  );
}

// ChatView is the right pane for one conversation — mirrors the task detail's
// main-agent console: header with agent + live badge + token/duration meta, a
// scroll-stick transcript (chat mode), and the composer.
function ChatView({
  conv,
  profiles,
  initial,
  onConvUpdated,
}: {
  conv: Conversation;
  profiles: LLMProfile[];
  initial?: { input?: string };
  onConvUpdated: () => void;
}) {
  const [messages, setMessages] = React.useState<Activity[]>([]);
  const [delegations, setDelegations] = React.useState<Delegation[]>([]);
  const [input, setInput] = React.useState(initial?.input ?? "");
  const [files, setFiles] = React.useState<File[]>([]);
  const [running, setRunning] = React.useState(!!conv.running);
  const [busy, setBusy] = React.useState(false);
  const [hasMore, setHasMore] = React.useState(false);
  const [loadingOlder, setLoadingOlder] = React.useState(false);
  const [error, setError] = React.useState("");
  const cursor = React.useRef(0);
  const end = React.useRef<HTMLDivElement>(null);
  const follow = React.useRef(true);
  const fetchDetail = React.useCallback((seq: number) => api.conversationMsgDetail(conv.id, seq), [conv.id]);

  React.useEffect(() => {
    let live = true;
    let timer: ReturnType<typeof setTimeout>;
    let loaded = false;
    async function poll() {
      try {
        const jobs = await workflowsApi.delegations(conv.id).catch(() => null);
        if (live && jobs) setDelegations(jobs.items);
        if (!loaded) {
          const result = await api.conversationHistory(conv.id, 0, 200);
          if (!live) return;
          setMessages(result.items);
          setHasMore(result.hasMore);
          cursor.current = result.cursor;
          setRunning(result.running);
          loaded = true;
        } else {
          const result = await api.conversationMessages(conv.id, cursor.current);
          if (!live) return;
          setMessages((items) => mergeActivities(items, result.items));
          cursor.current = Math.max(cursor.current, result.cursor);
          setRunning(result.running);
        }
        setError("");
      } catch (e) {
        if (live) setError((e as Error).message);
      } finally {
        if (live) timer = setTimeout(() => void poll(), 1000);
      }
    }
    void poll();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [conv.id]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: message changes are the scroll trigger.
  React.useEffect(() => {
    if (follow.current) end.current?.scrollIntoView({ block: "end" });
  }, [messages]);

  async function loadOlder() {
    if (loadingOlder || !messages.length) return;
    setLoadingOlder(true);
    try {
      const result = await api.conversationHistory(conv.id, messages[0].seq, 200);
      follow.current = false;
      setMessages((items) => mergeActivities(result.items, items));
      setHasMore(result.hasMore);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setLoadingOlder(false);
    }
  }

  async function send() {
    if ((!input.trim() && files.length === 0) || busy) return;
    setBusy(true);
    try {
      const attachments = files.length > 0 ? (await api.chatUpload("session", String(conv.id), files)).attachments : [];
      await api.sendConversationMessage(conv.id, input.trim() || "请分析已附加的题目文件。", attachments, running);
      setInput("");
      setFiles([]);
      setRunning(true);
      follow.current = true;
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function stop() {
    if (busy) return;
    setBusy(true);
    try {
      await api.stopConversation(conv.id);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function changeProfile(id: number | null, effort = "") {
    setBusy(true);
    try {
      await api.updateConversationProfile(conv.id, id, effort);
      onConvUpdated();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.chatSplit} data-ops={delegations.length > 0 || undefined}>
      <main className={styles.chatMain}>
      <div className={styles.missionBar}>
        <span className={styles.missionEyebrow}>CTF CONTROL / SESSION #{conv.id}</span>
        <span className={styles.missionRole}>{conv.agent_key}</span>
        <span className={styles.missionRun} data-live={running}>{running ? "● 运行中" : "待命"}</span>
      </div>
      {error && (
        <p role="alert" className="px-6 py-2 text-sm text-destructive">
          记录加载失败：{error}
        </p>
      )}
      <div
        className="min-h-0 flex-1 overflow-y-auto"
        onScroll={(event) => {
          const el = event.currentTarget;
          follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
      >
        <div className="mx-auto w-full max-w-4xl px-4 py-6">
          {hasMore && (
            <Button variant="ghost" size="sm" disabled={loadingOlder} onClick={() => void loadOlder()}>
              加载更早记录
            </Button>
          )}
          <Transcript activity={messages} live={running} chat fetchDetail={fetchDetail} />
          <div ref={end} />
        </div>
      </div>
      <div className="mx-auto w-full max-w-4xl px-3 pb-3">
        <Composer
          value={input}
          onChange={setInput}
          onSend={() => void send()}
          disabled={busy}
          running={running}
          onStop={() => void stop()}
          placeholder="输入消息…"
          files={files}
          onFilesChange={setFiles}
        />
        <LLMProfileRow
          profiles={profiles}
          selected={conv.llm_profile_id ?? null}
          onChange={(id) => void changeProfile(id)}
          effort={conv.reasoning_effort ?? ""}
          onEffortChange={(effort) => changeProfile(conv.llm_profile_id ?? null, effort)}
          effectiveModel={conv.effective_model}
          disabled={busy || running}
        />
      </div>
      </main>
      {delegations.length > 0 && <aside className={styles.operationsRail}><DelegationConsole delegations={delegations} /></aside>}
    </div>
  );
}

function conversationStorageKey() {
  return `prtseek.active-conversation:${auth.getCurrentUser()?.username ?? "operator"}`;
}

function rememberConversation(id: number | null) {
  const value = id ? String(id) : "new";
  setLocalStorageValue(conversationStorageKey(), value);
  window.history.replaceState(window.history.state, "", `/chat?c=${value}`);
}

export default function ChatPage() {
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [selected, setSelected] = React.useState<Conversation | null>(null);
  const [conversations, setConversations] = React.useState<Conversation[]>([]);
  const [pending, setPending] = React.useState<{ input?: string; attachments?: ChatAttachment[] }>();
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [showHistory, setShowHistory] = React.useState(false);
  const activeId = React.useRef<number | null>(null);
  const request = React.useRef(0);

  const reload = React.useCallback(async () => {
    const version = ++request.current;
    setLoading(true);
    try {
      const items = (await api.conversations()).filter((item) => !legacyAgentKeys.has(item.agent_key));
      if (version !== request.current) return;
      setConversations(items);
      const current = items.find((item) => item.id === activeId.current) ?? null;
      activeId.current = current?.id ?? null;
      setSelected(current);
      rememberConversation(activeId.current);
      setError("");
    } catch (e) {
      if (version === request.current) setError((e as Error).message);
    } finally {
      if (version === request.current) setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    let live = true;
    const restore = () => {
      const value =
        new URLSearchParams(window.location.search).get("c") ?? getLocalStorageValue(conversationStorageKey());
      const id = Number(value);
      activeId.current = Number.isSafeInteger(id) && id > 0 ? id : null;
      setPending(undefined);
      void reload();
    };
    restore();
    const refreshProfiles = () => {
      api
        .llmProfiles()
        .then((items) => {
          if (live) setProfiles(items);
        })
        .catch((e) => toast.error(e.message));
    };
    refreshProfiles();
    api.agents().then((items) => setAgents(items.filter((agent) => !legacyAgentKeys.has(agent.key)))).catch((e) => toast.error(e.message));
    window.addEventListener("popstate", restore);
    window.addEventListener("prtseek-models-updated", refreshProfiles);
    return () => {
      live = false;
      request.current++;
      window.removeEventListener("popstate", restore);
      window.removeEventListener("prtseek-models-updated", refreshProfiles);
    };
  }, [reload]);

  function selectConversation(conversation: Conversation | null) {
    activeId.current = conversation?.id ?? null;
    setSelected(conversation);
    setPending(undefined);
    rememberConversation(activeId.current);
    setShowHistory(false);
  }

  return (
    <div data-content-padding="false" className={cn(styles.workspace, "prts-chat-workspace")}>
      <div className="conversation-mobile-bar">
        <Button
          variant="outline"
          aria-expanded={showHistory}
          aria-controls="conversation-history"
          onClick={() => setShowHistory(!showHistory)}
        >
          {showHistory ? "返回对话" : "会话管理"}
        </Button>
        <span className="truncate">{selected?.title || "新对话"}</span>
      </div>
      <div className="conversation-layout" data-history-open={showHistory}>
        <div id="conversation-history" className="conversation-history">
          <ConversationList
            items={conversations}
            selectedId={selected?.id}
            loading={loading}
            error={error}
            onSelect={selectConversation}
            onNew={() => selectConversation(null)}
            onReload={reload}
          />
        </div>
        <div className="conversation-main">
          {selected ? (
            <ChatView
              key={selected.id}
              conv={selected}
              profiles={profiles}
              initial={pending}
              onConvUpdated={() => void reload()}
            />
          ) : loading ? (
            <p role="status" className="p-6">
              正在恢复会话…
            </p>
          ) : error ? (
            <div className="space-y-3 p-6">
              <p role="alert">{error}</p>
              <Button onClick={() => void reload()}>重试</Button>
            </div>
          ) : (
            <DraftChat
              agents={agents}
              profiles={profiles}
              onStarted={(conversation, draft) => {
                selectConversation(conversation);
                setPending(draft);
                setConversations((items) => [conversation, ...items.filter((item) => item.id !== conversation.id)]);
              }}
            />
          )}
        </div>
      </div>
    </div>
  );
}
