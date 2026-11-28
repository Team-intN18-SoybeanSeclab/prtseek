"use client";

import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { contestCatalog, type ContestSummary, updateContestLibrary, validateMaterial } from "@/lib/contest-library";

type Row = { id: string; title: string; category: string };
type Pending = { seq: number; resolve: (value: unknown) => void; reject: (err: Error) => void };

function message(e: unknown) { return e instanceof Error ? e.message : "取题失败。"; }

export default function ContestImport() {
  const [origin, setOrigin] = useState("");
  const [connected, setConnected] = useState(false);
  const [mode, setMode] = useState("ctfd");
  const [eventUrl, setEventUrl] = useState("");
  const [bookmark, setBookmark] = useState("");
  const [rows, setRows] = useState<Row[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [catalog, setCatalog] = useState<ContestSummary[]>([]);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const channel = useRef<MessagePort | null>(null);
  const waiting = useRef<Pending | null>(null);
  const serial = useRef(0);
  const bookmarkLink = useRef<HTMLAnchorElement>(null);

  useEffect(() => {
    try { setEventUrl(localStorage.getItem("prtseek.last-contest") || ""); } catch {}
    void contestCatalog().then(setCatalog).catch(e => setError(message(e)));
    const xhr = new XMLHttpRequest();
    xhr.open("GET", "/contest-reader.js");
    xhr.onload = () => {
      if (xhr.status !== 200 || !xhr.responseText.includes("function prtseekContestReader(")) {
        setError("读取取题书签失败，请刷新重试。");
        return;
      }
      const script = xhr.responseText + "\nprtseekContestReader(" + JSON.stringify(location.origin) + ");";
      setBookmark("javascript:" + encodeURIComponent("void(function(){\n" + script + "\n})()"));
    };
    xhr.onerror = () => setError("读取取题书签失败，请刷新重试。");
    xhr.send();
    return () => xhr.abort();
  }, []);
  useEffect(() => {
    if (bookmark) bookmarkLink.current?.setAttribute("href", bookmark);
  }, [bookmark]);

  useEffect(() => {
    if (!location.hash) return;
    let source: URL;
    try { source = new URL(decodeURIComponent(location.hash.slice(1))); } catch { return; }
    if (!["https:", "http:"].includes(source.protocol) || source.origin === location.origin) return;
    setOrigin(source.origin);
    if (source.hostname === "ichunqiu.com" || source.hostname.endsWith(".ichunqiu.com")) setMode("page");
    if (!window.opener) {
      setError("浏览器隔离了赛事窗口。请在同一浏览器登录两个站点，并从题面重新点击书签。");
      return;
    }
    const bridge = new MessageChannel();
    const timeout = window.setTimeout(() => setError("连接超时。请保持赛事页面打开，回到题面重新点击取题书签。"), 8000);
    bridge.port1.onmessage = ({data}) => {
      if (data?.ready === true) {
        clearTimeout(timeout);
        channel.current = bridge.port1;
        setConnected(true);
        setError("");
        return;
      }
      const pending = waiting.current;
      if (!pending || data?.seq !== pending.seq) return;
      if (typeof data.error === "string") pending.reject(Error(data.error));
      else pending.resolve(data.value);
    };
    // Delay the transferable handshake until after React's development effect replay.
    const start = window.setTimeout(() => window.opener?.postMessage("prtseek-contest-ready-v1", source.origin, [bridge.port2]), 0);
    return () => {
      clearTimeout(start);
      clearTimeout(timeout);
      waiting.current?.reject(Error("取题窗口已关闭。"));
      channel.current = null;
      bridge.port1.close();
      bridge.port2.close();
    };
  }, []);

  async function call(action: string, id?: string): Promise<unknown> {
    const port = channel.current;
    if (!port || !window.opener || window.opener.closed) throw Error("赛事窗口已关闭，请在题面重新点击书签。");
    const seq = ++serial.current;
    let timer = 0;
    try {
      return await new Promise((resolve, reject) => {
        waiting.current = { seq, resolve, reject };
        timer = window.setTimeout(() => reject(Error("赛事页面没有响应。请重新连接后重试。")), action === "read" ? 180000 : 20000);
        port.postMessage({ seq, action, id });
      });
    } finally {
      clearTimeout(timer);
      waiting.current = null;
    }
  }

  async function list() {
    setBusy(true); setError(""); setStatus(""); setSelected([]); setRows([]);
    try {
      const items = await call(mode === "ctfd" ? "list-ctfd" : "list-page");
      if (!Array.isArray(items) || items.length > 2000 || !items.every(r => r && typeof r.title === "string" && r.title.length <= 240 &&
        typeof r.category === "string" && r.category.length <= 120 && typeof r.id === "string" && /^(page|[1-9]\d*)$/.test(r.id))) throw Error("赛事题单格式无效。");
      setRows(items);
      if (items.length === 1) setSelected([items[0].id]);
      if (!items.length) setStatus("当前账号没有可见题目。");
    } catch (e) { setError(message(e)); } finally { setBusy(false); }
  }

  async function pull() {
    setBusy(true); setError("");
    const failures: string[] = [];
    let count = 0;
    try {
      for (const [index, id] of selected.entries()) {
        const title = rows.find(row => row.id === id)?.title || id;
        setStatus("正在取回 " + (index + 1) + "/" + selected.length + " · " + title);
        try {
          const item = validateMaterial(await call("read", id) as Parameters<typeof validateMaterial>[0], origin);
          await updateContestLibrary(item.key, item);
          count++;
          setSelected(prev => prev.filter(key => key !== id));
        } catch (e) {
          failures.push(title + "：" + message(e));
          if (!window.opener || window.opener.closed) break;
        }
      }
      setCatalog(await contestCatalog());
      setStatus("已存入题库 " + count + " 题。");
      if (failures.length) setError(failures.join("\n"));
    } catch (e) { setError(message(e)); } finally { setBusy(false); }
  }

  return <main className="mx-auto grid w-full max-w-3xl gap-6 p-6">
    <h1 className="text-xl font-semibold">比赛题库</h1>
    {!origin && <form className="grid gap-2" onSubmit={event => {
      event.preventDefault();
      try {
        const url = new URL(eventUrl.trim());
        if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) throw Error("请输入赛事官网的 HTTP 或 HTTPS 地址。");
        window.open(url.href, "_blank", "noopener,noreferrer");
        try { localStorage.setItem("prtseek.last-contest", url.origin + url.pathname); } catch {}
        setError("");
      } catch { setError("请输入完整的赛事官网地址。"); }
    }}>
      <label className="font-medium" htmlFor="contest-url">赛事入口</label>
      <div className="flex gap-2"><input className="min-w-0 flex-1 rounded border bg-background px-3 text-sm"
        id="contest-url" type="url" required placeholder="https://…" value={eventUrl} onChange={e => setEventUrl(e.target.value)}/>
        <Button type="submit">打开赛事并登录</Button></div>
      <p className="text-sm text-muted-foreground">账号和验证码在平台官网填写。取题使用当前浏览器的登录状态。</p>
    </form>}
    {origin && <section className="grid gap-3 rounded border p-4">
      <h2 className="font-medium">{origin}</h2>
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="读取方式" className="rounded border bg-background p-2" value={mode} disabled={busy}
          onChange={event => { setMode(event.target.value); setRows([]); setSelected([]); setError(""); setStatus(""); }}>
          <option value="ctfd">CTFd 题单</option><option value="page">当前题面</option>
        </select>
        <Button disabled={!connected || busy} onClick={list}>{connected ? "读取题目" : "连接中"}</Button>
      </div>
      {rows.length > 0 && <>
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" disabled={busy}
          checked={selected.length === rows.length} onChange={e => setSelected(e.target.checked ? rows.map(r => r.id) : [])}/>全选（{rows.length}）</label>
        <div className="max-h-72 overflow-y-auto divide-y">{rows.map(r => <label key={r.id} className="flex items-center gap-2 py-2 text-sm">
          <input type="checkbox" disabled={busy} checked={selected.includes(r.id)}
            onChange={e => setSelected(prev => e.target.checked ? [...prev, r.id] : prev.filter(k => k !== r.id))}/>
          <span>{r.title}</span><span className="ml-auto text-muted-foreground">{r.category}</span>
        </label>)}</div>
        <Button className="w-fit" disabled={busy || !selected.length} onClick={pull}>导入所选题目（{selected.length}）</Button>
      </>}
    </section>}
    {status && <p role="status" className="text-sm">{status}</p>}
    {error && <p role="alert" className="whitespace-pre-line text-sm text-destructive">{error}</p>}
    <section className="grid gap-2">
      <h2 className="font-medium">取题书签</h2>
      <p className="text-sm text-muted-foreground">将下方链接拖到书签栏。在同一浏览器登录赛事官网，打开 CTFd 的题目列表，或在其他平台选中题面正文，再点击书签。</p>
      <div className="flex items-center gap-3">
        <a ref={bookmarkLink} draggable={!!bookmark} onClick={e => { e.preventDefault(); setStatus("请把链接拖到书签栏，或复制地址后新建书签。"); }}
          className="cursor-grab rounded border px-3 py-2 text-sm">prtseek 取题</a>
        <Button variant="ghost" size="sm" disabled={!bookmark} onClick={async () => {
          try { await navigator.clipboard.writeText(bookmark); setStatus("书签地址已复制。"); }
          catch { setError("浏览器未允许复制，请将链接拖到书签栏。"); }
        }}>复制书签地址</Button>
      </div>
    </section>
    <section className="grid gap-2">
      <div className="flex items-center justify-between"><h2 className="font-medium">本机题目（{catalog.length}）</h2><a href="/chat/" className="text-sm underline">打开会话</a></div>
      {!catalog.length && <p className="text-sm text-muted-foreground">取回的题面和附件会保存在此浏览器。</p>}
      {catalog.map(row => <div key={row.key} className="flex items-center justify-between gap-3 border-b py-2 text-sm">
        <div><p>{row.title}</p><p className="text-muted-foreground">{row.source} · {row.fileCount} 个附件</p></div>
        <Button variant="ghost" size="sm" disabled={busy} onClick={async () => {
          try { await updateContestLibrary(row.key); setCatalog(await contestCatalog()); } catch (e) { setError(message(e)); }
        }}>移除</Button>
      </div>)}
    </section>
  </main>;
}
