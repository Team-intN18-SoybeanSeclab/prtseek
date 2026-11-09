"use client";

import * as React from "react";

import { ArrowUpIcon, ChevronDownIcon, Paperclip, Square, X, ZapIcon } from "lucide-react";
import { toast } from "sonner";

import styles from "@/components/chat-workspace.module.css";
import { ConversationList } from "@/components/conversation-list";
import { ReasoningControl } from "@/components/reasoning-control";
import { Transcript } from "@/components/transcript";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { mergeActivities } from "@/lib/activity-merge";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";
import { shouldSubmitOnKey, useChatSendMode } from "@/lib/chat-send-mode";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import type { Activity, Agent, ChatAttachment, Conversation, LLMProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

function workflowModeLabel(mode?: string) {
  if (mode === "ctf") return "CTF";
  if (mode === "custom") return "自定义";
  return "普通";
}

import { type Delegation, workflowsApi } from "@/lib/workflows-api";

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
      <div className={styles.composerInput}>
        {onFilesChange && (
          <>
            <input
              ref={picker}
              className="hidden"
              type="file"
              multiple
              onChange={(event) => {
                onFilesChange([...files, ...Array.from(event.target.files ?? [])]);
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
        {running ? (
          <Button size="icon" variant="destructive" onClick={onStop} aria-label="停止运行">
            <Square />
          </Button>
        ) : (
          <Button
            size="icon"
            className={styles.send}
            onClick={onSend}
            disabled={disabled || (!value.trim() && files.length === 0)}
            aria-label="发送消息"
          >
            <ArrowUpIcon />
          </Button>
        )}
      </div>
    </div>
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
  const [mode, setMode] = React.useState<"standard" | "ctf">("standard");
  const [entry, setEntry] = React.useState("mainagent");
  const target = mode === "ctf" ? "ctf_triage" : entry;
  const agentKey = agents.find((a) => a.key === target && a.enabled)?.key ?? "";
  const [llmProfileId, setLlmProfileId] = React.useState<number | null>(null);
  const [effort, setEffort] = React.useState("");
  const [input, setInput] = React.useState("");
  const [files, setFiles] = React.useState<File[]>([]);
  const [sending, setSending] = React.useState(false);

  async function send() {
    const msg = input.trim();
    if ((!msg && files.length === 0) || !agentKey || sending) return;
    const profile =
      llmProfileId == null ? profiles.find((p) => p.is_default) : profiles.find((p) => Number(p.id) === llmProfileId);
    if (!profile?.api_key_hint) {
      window.dispatchEvent(new Event("prtseek-configure-model"));
      return;
    }
    setSending(true);
    let created: Conversation | undefined;
    try {
      const c = await api.createConversation(agentKey, "", Number(profile.id), effort, mode);
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
        <div className="mb-2 flex flex-wrap gap-3 px-6 text-sm">
          <label htmlFor="workflow-mode">
            模式{" "}
            <NativeSelect
              id="workflow-mode"
              aria-label="工作模式"
              className="ml-2"
              value={mode}
              disabled={sending}
              onChange={(e) => {
                const next = e.target.value as "standard" | "ctf";
                setMode(next);
              }}
            >
              <option value="standard">普通</option>
              <option value="ctf">CTF</option>
            </NativeSelect>
          </label>
          {mode === "standard" ? (
            <label htmlFor="workflow-entry">
              首条输入{" "}
              <NativeSelect
                id="workflow-entry"
                aria-label="入口 Agent"
                className="ml-2"
                value={entry}
                disabled={sending}
                onChange={(e) => setEntry(e.target.value)}
              >
                <option value="mainagent">mainagent</option>
                <option value="planner">planner</option>
              </NativeSelect>
            </label>
          ) : (
            <span className="self-center text-muted-foreground">
              探测 → Planner → 并行 Workers → Final → EXP + WriteUP
            </span>
          )}
          {!agentKey && <span className="text-destructive">入口 Agent 不可用，请在管理中恢复</span>}
        </div>
        <Composer
          value={input}
          onChange={setInput}
          onSend={send}
          disabled={sending || !agentKey}
          placeholder="输入你的目标…"
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
        />
      </div>
    </div>
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
    if ((!input.trim() && files.length === 0) || busy || running) return;
    setBusy(true);
    try {
      const attachments = files.length > 0 ? (await api.chatUpload("session", String(conv.id), files)).attachments : [];
      await api.sendConversationMessage(conv.id, input.trim() || "请分析已附加的题目文件。", attachments);
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
    try {
      await api.stopConversation(conv.id);
      setRunning(false);
    } catch (e) {
      toast.error((e as Error).message);
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
    <>
      <div className="px-6 py-2 text-xs text-muted-foreground">
        {workflowModeLabel(conv.mode)} · {conv.agent_key}
      </div>
      {delegations.length > 0 && (
        <div className="flex flex-wrap gap-2 px-6 pb-2">
          {delegations.map((job) => (
            <a
              key={job.id}
              className="border px-3 py-1 text-xs"
              href={`/chat?c=${job.id}`}
              target="_blank"
              rel="noopener noreferrer"
              title={job.title}
            >
              #{job.id} {job.agent_key} · {job.state} · {job.title} ↗
            </a>
          ))}
        </div>
      )}
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
          disabled={busy || running}
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
    </>
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
      const items = await api.conversations();
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
    api
      .agents()
      .then((items) => {
        if (live) setAgents(items);
      })
      .catch((e) => toast.error(e.message));
    const refreshProfiles = () => {
      api
        .llmProfiles()
        .then((items) => {
          if (live) setProfiles(items);
        })
        .catch((e) => toast.error(e.message));
    };
    refreshProfiles();
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
