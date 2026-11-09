"use client";

import * as React from "react";

import { toast } from "sonner";

import { AgentEditor } from "@/components/agent-editor";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import type { Agent } from "@/lib/types";

const DEFAULT_ROLES = [
  { key: "triage", phase: "01 / INTAKE", name: "CTF Triage", description: "盘点题目、附件、环境、范围与 flag 格式。" },
  { key: "command", phase: "02 / COMMAND", name: "CTF Command", description: "拆分目标、组织并行 Solver、收敛真实证据。" },
  { key: "solver", phase: "03 / EXECUTE", name: "CTF Solver", description: "分析题目材料并输出可复现的关键证据。" },
  { key: "writeup", phase: "04 / ARCHIVE", name: "CTF WriteUP", description: "整理已验证结果、EXP 与可复现 WRITEUP。" },
] as const;

export default function AgentsPage() {
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [editing, setEditing] = React.useState<string | null>(null);
  const [query, setQuery] = React.useState("");
  const reload = React.useCallback(() => {
    void api.agents().then(setAgents).catch((e) => toast.error(e.message));
  }, []);
  React.useEffect(reload, [reload]);

  const visible = DEFAULT_ROLES
    .map((role) => ({ role, agent: agents.find((agent) => agent.key === role.key) }))
    .filter(({ role, agent }) => `${role.key} ${role.name} ${role.description} ${agent?.description ?? ""}`.toLowerCase().includes(query.trim().toLowerCase()));

  return (
    <div className="mx-auto w-full max-w-5xl space-y-6">
      <div>
        <div className="prts-section-code">CTF / DEFAULT AGENTS</div>
        <h1 className="text-xl font-semibold">Agent 编组</h1>
      </div>
      <Input aria-label="搜索 Agent" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="搜索 key、名称或说明" />
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {visible.map(({ role, agent }) => (
          <section key={role.key} className="space-y-3 border p-5">
            <div className="font-mono text-xs text-primary">{role.phase}</div>
            <h2 className="font-mono">{role.key}</h2>
            <p className="text-sm font-medium">{agent?.name || role.name}</p>
            <p className="min-h-10 text-sm text-muted-foreground">{agent?.description || role.description}</p>
            {!agent && <p className="text-xs text-destructive">角色尚未初始化；重启服务后会自动创建。</p>}
            {agent && !agent.enabled && <p className="text-xs text-muted-foreground">已停用</p>}
            <Button size="sm" variant="outline" disabled={!agent} onClick={() => setEditing(role.key)}>配置 Agent</Button>
          </section>
        ))}
      </div>
      {visible.length === 0 && <p className="text-sm text-muted-foreground">没有匹配的默认 Agent。</p>}
      <Sheet open={!!editing} onOpenChange={(open) => !open && setEditing(null)}>
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-3xl">
          <SheetHeader><SheetTitle>{editing}</SheetTitle><SheetDescription>编辑默认角色的提示词、模型、执行配置与可见资源。</SheetDescription></SheetHeader>
          {editing && <AgentEditor agentKey={editing} onSaved={reload} />}
        </SheetContent>
      </Sheet>
    </div>
  );
}
