"use client";

import { useState } from "react";

import { Pencil, Pin, Plus, RefreshCw, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import type { Conversation } from "@/lib/types";

export function ConversationList({
  items,
  selectedId,
  loading,
  error,
  onSelect,
  onNew,
  onReload,
}: {
  items: Conversation[];
  selectedId?: number;
  loading: boolean;
  error: string;
  onSelect: (conversation: Conversation) => void;
  onNew: () => void;
  onReload: () => Promise<void>;
}) {
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState<number>();
  async function change(item: Conversation, kind: "rename" | "pin" | "delete") {
    let title: string | null = null;
    if (kind === "rename") {
      title = window.prompt("重命名会话", item.title);
      if (!title?.trim()) return;
    }
    if (
      kind === "delete" &&
      !window.confirm(
        `删除会话「${item.title || "未命名会话"}」及其历史记录？${item.running ? "该会话正在运行，删除也会停止运行。" : ""}`,
      )
    )
      return;
    setBusy(item.id);
    try {
      if (kind === "rename" && title) await api.renameConversation(item.id, title.trim());
      if (kind === "pin") await api.updateConversation(item.id, { pinned: !item.pinned });
      if (kind === "delete") await api.deleteConversation(item.id);
      await onReload();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(undefined);
    }
  }
  const filtered = items.filter((item) =>
    `${item.title} ${item.agent_key}`.toLowerCase().includes(query.trim().toLowerCase()),
  );
  return (
    <aside className="conversation-sidebar" aria-label="会话管理">
      <div className="space-y-3 border-b p-3">
        <div className="flex items-center justify-between">
          <h2 className="font-mono text-sm">会话管理</h2>
          <Button
            variant="ghost"
            size="icon"
            aria-label="刷新会话列表"
            disabled={loading}
            onClick={() => void onReload()}
          >
            <RefreshCw />
          </Button>
        </div>
        <Button className="w-full" onClick={onNew} disabled={loading}>
          <Plus /> 新对话
        </Button>
        <Input
          aria-label="搜索会话"
          placeholder="搜索会话、Agent…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {error && (
          <p role="alert" className="p-2 text-destructive">
            会话加载失败：{error}
          </p>
        )}
        {loading && (
          <p role="status" className="p-2 text-muted-foreground">
            正在加载会话…
          </p>
        )}
        {!loading && !error && filtered.length === 0 && (
          <p className="p-2 text-muted-foreground">{query ? "没有匹配的会话" : "暂无历史会话"}</p>
        )}
        {filtered.map((item) => (
          <div key={item.id} className="conversation-entry" data-active={selectedId === item.id}>
            <button
              type="button"
              className="w-full min-w-0 p-3 text-left"
              aria-current={selectedId === item.id ? "true" : undefined}
              onClick={() => onSelect(item)}
            >
              <span className="block truncate font-medium">
                {item.pinned && "★ "}
                {item.title || "未命名会话"}
              </span>
              <span className="mt-1 block truncate text-xs text-muted-foreground">
                {item.agent_key} · {item.running ? "运行中" : "已保存"}
              </span>
            </button>
            <div className="flex justify-end gap-1 px-2 pb-2">
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={busy !== undefined}
                aria-label={`${item.pinned ? "取消置顶" : "置顶"}：${item.title}`}
                onClick={() => void change(item, "pin")}
              >
                <Pin />
              </Button>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={busy !== undefined}
                aria-label={`重命名：${item.title}`}
                onClick={() => void change(item, "rename")}
              >
                <Pencil />
              </Button>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={busy !== undefined}
                aria-label={`删除：${item.title}`}
                onClick={() => void change(item, "delete")}
              >
                <Trash2 />
              </Button>
            </div>
          </div>
        ))}
      </div>
    </aside>
  );
}
