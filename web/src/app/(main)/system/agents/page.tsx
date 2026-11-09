"use client";
import * as React from "react";

import { toast } from "sonner";

import { AgentEditor } from "@/components/agent-editor";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import type { Agent } from "@/lib/types";

const BUILTIN_AGENTS = new Set([
  "goals",
  "planner",
  "mainagent",
  "worker",
  "reporter",
  "ctf_triage",
  "ctf_planner",
  "ctf_worker",
  "ctf_writeup",
]);

export default function AgentsPage() {
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [editing, setEditing] = React.useState<string | null>(null);
  const [query, setQuery] = React.useState("");
  const [group, setGroup] = React.useState("all");
  const reload = React.useCallback(() => {
    api
      .agents()
      .then(setAgents)
      .catch((e) => toast.error(e.message));
  }, []);
  React.useEffect(reload, [reload]);
  const visible = agents.filter(
    (a) =>
      BUILTIN_AGENTS.has(a.key) &&
      (group === "all" || (group === "ctf") === a.key.startsWith("ctf_")) &&
      `${a.key} ${a.name} ${a.description}`.toLowerCase().includes(query.toLowerCase()),
  );
  return (
    <div className="mx-auto w-full max-w-5xl space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Agents 管理</h1>
      </div>
      <div className="flex flex-wrap gap-3">
        <Input
          aria-label="搜索 Agent"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索 key、名称或说明"
        />
        <NativeSelect aria-label="Agent 分组" className="w-48" value={group} onChange={(e) => setGroup(e.target.value)}>
          <option value="all">全部</option>
          <option value="standard">普通</option>
          <option value="ctf">CTF</option>
        </NativeSelect>
      </div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {visible.map((agent) => (
          <section key={agent.key} className="space-y-3 border p-5">
            <h2 className="font-mono">{agent.key}</h2>
            <p className="text-sm font-medium">{agent.name}</p>
            <p className="line-clamp-3 text-sm text-muted-foreground">{agent.description}</p>
            {!agent.enabled && <p className="text-xs text-muted-foreground">已停用</p>}
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="outline" onClick={() => setEditing(agent.key)}>
                提示词与配置
              </Button>
            </div>
          </section>
        ))}
      </div>
      {visible.length === 0 && <p className="text-sm text-muted-foreground">没有匹配的 Agent</p>}
      <Sheet
        open={!!editing}
        onOpenChange={(open) => {
          if (!open) setEditing(null);
        }}
      >
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-3xl">
          <SheetHeader>
            <SheetTitle>{editing}</SheetTitle>
            <SheetDescription>提示词、模型与执行配置</SheetDescription>
          </SheetHeader>
          {editing && <AgentEditor agentKey={editing} onSaved={reload} />}
        </SheetContent>
      </Sheet>
    </div>
  );
}
