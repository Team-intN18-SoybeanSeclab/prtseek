"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog";
import { contestCatalog, contestMaterial, type ContestProblem, type ContestSummary } from "@/lib/contest-library";

export function ContestPicker({ disabled, onPick }: { disabled: boolean; onPick: (item: ContestProblem) => void }) {
  const [open, setOpen] = useState(false);
  const [rows, setRows] = useState<ContestSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  async function show() {
    setOpen(true); setLoading(true); setError("");
    try { setRows(await contestCatalog()); }
    catch { setError("无法读取此浏览器的比赛题库。"); } finally { setLoading(false); }
  }
  async function pick(key: string) {
    setLoading(true); setError("");
    try {
      const item = await contestMaterial(key);
      if (!item) { setRows(await contestCatalog()); throw Error("这道题已从题库移除。"); }
      onPick(item); setOpen(false);
    } catch (e) { setError(e instanceof Error ? e.message : "读取附件失败。"); } finally { setLoading(false); }
  }
  return <>
    <Button type="button" variant="ghost" size="sm" disabled={disabled} onClick={show} aria-label="从比赛题库添加">题库</Button>
    <Dialog open={open} onOpenChange={setOpen}><DialogContent>
      <DialogHeader><DialogTitle>从比赛题库添加</DialogTitle>
        <DialogDescription>选择一道题，将题面和附件加入当前草稿。</DialogDescription></DialogHeader>
      <a href="/contest-import/" target="_blank" rel="noopener noreferrer" className="text-sm underline">取题与管理</a>
      {!loading && !rows.length && !error && <p className="text-sm text-muted-foreground">题库为空。先打开「取题与管理」添加取题书签。</p>}
      {loading && <p role="status" className="text-sm">读取中…</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      <div className="max-h-80 divide-y overflow-auto">{rows.map(r => <button type="button" key={r.key}
        className="block w-full py-2 text-left text-sm hover:bg-muted" disabled={disabled || loading} onClick={() => pick(r.key)}>
        <p>{r.title}</p><span className="text-muted-foreground">{r.category && r.category + " · "}{r.source} · {r.fileCount} 个附件</span>
      </button>)}</div>
    </DialogContent></Dialog>
  </>;
}
