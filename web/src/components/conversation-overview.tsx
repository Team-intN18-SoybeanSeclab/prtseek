"use client";

import * as React from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api } from "@/lib/api";
import { elapsedLabel, executionLabel, outcomeLabel } from "@/lib/ctf-status";
import { hideResearchEvidenceAnnotation } from "@/lib/research-evidence";
import { type ConversationFile, type ConversationOverview as Overview, workflowsApi } from "@/lib/workflows-api";

export function ConversationOverview({ id, overview }: { id: number; overview: Overview | null }) {
  const [open, setOpen] = React.useState(false);
  const [files, setFiles] = React.useState<ConversationFile[]>([]);
  const [fileState, setFileState] = React.useState("");
  const [preview, setPreview] = React.useState<{ name: string; text: string } | null>(null);
  const [truncated, setTruncated] = React.useState(false);
  const previewRequest = React.useRef(0);

  async function loadFiles() {
    setFileState("正在读取会话文件…");
    try {
      const result = await workflowsApi.files(id);
      setFiles(result.items);
      setTruncated(result.truncated);
      setFileState("");
    } catch (e) { setFileState((e as Error).message); }
  }
  async function showFile(file: ConversationFile) {
    const version = ++previewRequest.current;
    setPreview({ name: file.name, text: "正在读取…" });
    try {
      const result = await workflowsApi.preview(id, file);
      if (version === previewRequest.current) setPreview({ name: file.name, text: result.content ?? (result.too_large ? "文件较大，请下载查看。" : "二进制文件，请下载查看。") });
    } catch (e) { if (version === previewRequest.current) setPreview({ name: file.name, text: (e as Error).message }); }
  }

  return <div className="shrink-0 border-b bg-background px-4 py-2 text-xs text-muted-foreground">
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
      {overview && <>
        <span>执行：{executionLabel(overview.state)}</span>
        <span className={overview.outcome.state === "blocked" ? "text-amber-600 dark:text-amber-400" : "text-foreground"}>目标：{outcomeLabel(overview.outcome.state)}</span>
        <span>本轮 {elapsedLabel(overview.elapsed_ms)}</span>
        <span title="包含所有子会话；按接口报告的用量累计">累计 Token {(overview.input_tokens + overview.output_tokens).toLocaleString()}</span>
        <span title="本轮与子任务共享的逻辑模型请求预算；底层网络重试另计">调用 {overview.model_calls}/{overview.model_call_limit}</span>
        {overview.active_children > 0 && <span>{overview.active_children} 个子任务执行中</span>}
      </>}
      <Button size="sm" variant="outline" className="ml-auto" onClick={() => { setOpen(true); void loadFiles(); }}>会话文件</Button>
    </div>
    {overview?.latest && <p className="mt-1 truncate" title={hideResearchEvidenceAnnotation(overview.latest.summary)}>最近动态：{overview.latest.worker} · {overview.latest.tool || overview.latest.kind} · {hideResearchEvidenceAnnotation(overview.latest.summary)}</p>}
    {overview?.outcome.summary && <details className="mt-1">
      <summary className="cursor-pointer text-foreground">目标报告：{overview.outcome.summary}</summary>
      <div className="space-y-1 py-2 whitespace-pre-wrap break-words">
        <p>验收条件：{overview.outcome.criterion}</p>
        {overview.outcome.verification && <p>验证记录：{overview.outcome.verification}</p>}
        {!!overview.outcome.evidence?.length && <p>证据：{overview.outcome.evidence.map((e) => `#${e.conversation_id}/${e.id}`).join("、")}</p>}
      </div>
    </details>}
    <Dialog open={open} onOpenChange={(value) => { setOpen(value); if (!value) { previewRequest.current++; setPreview(null); } }}>
      <DialogContent className="flex max-h-[85dvh] flex-col sm:max-w-3xl">
        <DialogHeader><DialogTitle>本题文件与产物</DialogTitle><DialogDescription>包含当前会话及子任务的附件、脚本、WriteUP 和证据。</DialogDescription></DialogHeader>
        <div className="flex items-center justify-between gap-2">
          <a className="text-sm underline" href={`/function/workspace?path=${encodeURIComponent(`sessions/conv-${id}`)}`}>打开会话工作区</a>
          <Button size="sm" variant="outline" onClick={() => void loadFiles()}>刷新文件</Button>
        </div>
        {fileState && <p role="status" className="text-sm">{fileState}</p>}
        <div className="min-h-0 overflow-y-auto">
          {!fileState && files.length === 0 && <p className="py-8 text-center text-sm text-muted-foreground">此会话尚无文件。</p>}
          <ul className="divide-y">
            {files.map((file) => <li key={`${file.conversation_id}/${file.path}`} className="flex flex-wrap items-center gap-2 py-2 text-sm">
              <button className="min-w-0 flex-1 text-left hover:underline" onClick={() => void showFile(file)}>
                <span className="block break-all font-medium">{file.name}</span>
                <span className="block break-all text-xs text-muted-foreground">{({ input: "附件", artifact: "产物", evidence: "证据" })[file.kind]} · #{file.conversation_id} · {file.path} · {file.size.toLocaleString()} B</span>
              </button>
              <Button size="sm" variant="ghost" onClick={() => void api.workspaceDownload(file.workspace_path).catch((e) => toast.error(e.message))}>下载</Button>
            </li>)}
          </ul>
          {truncated && <p className="py-2 text-sm text-muted-foreground">文件较多，仅显示部分条目；可在工作区查看全部。</p>}
          {preview && <section className="mt-3 border-t pt-3"><strong className="break-all text-sm">{preview.name}</strong><pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-muted p-3 text-xs">{preview.text}</pre></section>}
        </div>
      </DialogContent>
    </Dialog>
  </div>;
}
