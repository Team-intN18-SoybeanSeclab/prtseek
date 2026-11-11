"use client";

import * as React from "react";

import { Check, ChevronDown, Plus, Search, Settings2, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { api } from "@/lib/api";
import type { LLMProfile } from "@/lib/types";

export function ModelEditor({
  profile,
  onSaved,
  initial = false,
}: {
  profile?: LLMProfile;
  onSaved: () => void;
  initial?: boolean;
}) {
  const [name, setName] = React.useState(profile?.name ?? "");
  const [format, setFormat] = React.useState<LLMProfile["format"]>(profile?.format ?? "openai");
  const [baseUrl, setBaseUrl] = React.useState(profile?.base_url ?? "");
  const [key, setKey] = React.useState("");
  const [model, setModel] = React.useState(profile?.model ?? "");
  const [models, setModels] = React.useState<string[]>([]);
  const [modelQuery, setModelQuery] = React.useState("");
  const [modelsOpen, setModelsOpen] = React.useState(false);
  const [makeDefault, setMakeDefault] = React.useState(initial === true || profile?.is_default === true);
  const [busy, setBusy] = React.useState("");
  const [error, setError] = React.useState("");
  const [tested, setTested] = React.useState("");
  const [advanced, setAdvanced] = React.useState({
    proxy: profile?.proxy ?? "",
    session_header_key: profile?.session_header_key ?? "",
    reasoning_effort: profile?.reasoning_effort ?? "",
    thinking_type: profile?.thinking_type ?? "",
    streaming: profile?.streaming ?? true,
    context_window_k: profile?.context_window_k ?? 0,
    max_tokens: profile?.max_tokens ?? 0,
    max_tokens_field: profile?.max_tokens_field ?? "",
  });
  const id = profile ? Number(profile.id) : undefined;

  async function loadModels() {
    setBusy("models");
    setError("");
    try {
      const result = await api.fetchLLMModels(format, baseUrl.trim(), key, advanced.proxy, id);
      if (!result.ok || !result.models?.length)
        throw new Error(result.error || "接口没有返回模型列表，请手动填写模型 ID。");
      const complete = [...new Set(result.models)].sort((a, b) => a.localeCompare(b));
      setModels(complete);
      if (!model) setModel(result.models[0]);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }
  async function save() {
    setError("");
    setTested("");
    if (!model.trim() || (!key.trim() && !profile?.api_key_hint)) {
      setError("请填写 API Key 和模型。");
      return;
    }
    setBusy("save");
    try {
      const result = await api.testLLM(format, model.trim(), baseUrl.trim(), key, advanced.proxy, advanced.thinking_type, advanced.reasoning_effort, id, advanced.streaming, advanced.session_header_key, true, { max_tokens: advanced.max_tokens, max_tokens_field: advanced.max_tokens_field });
      if (!result.ok) throw new Error(result.error || "模型未返回有效回复");
      if (!result.tools_verified) throw new Error("工具调用能力尚未通过验证。");
      setTested(`连接与工具往返已验证 · ${result.latency_ms ?? "—"} ms`);
      const saved = await api.saveLLMProfile({
        ...(profile ? { ...profile, id } : {}),
        name: name.trim() || model.trim(),
        format,
        base_url: baseUrl.trim(),
        api_key: key,
        model: model.trim(),
        ...advanced,
        pool_exclude: profile?.pool_exclude ?? true,
      });
      if (makeDefault || initial) await api.activateLLMProfile(String(saved.id));
      toast.success("模型配置已验证并保存");
      window.dispatchEvent(new Event("prtseek-models-updated"));
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="model-format">接口类型</Label>
        <NativeSelect
          id="model-format"
          className="w-full"
          value={format}
          onChange={(e) => setFormat(e.target.value as LLMProfile["format"])}
          disabled={!!busy}
        >
          <option value="openai">OpenAI 兼容接口</option>
          <option value="openai-responses">Responses 接口</option>
          <option value="anthropic">Anthropic 接口</option>
        </NativeSelect>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="model-url">Base URL</Label>
        <Input
          id="model-url"
          placeholder={format === "anthropic" ? "https://api.anthropic.com" : "https://api.example.com/v1"}
          value={baseUrl}
          onChange={(e) => setBaseUrl(e.target.value)}
          disabled={!!busy}
          autoComplete="off"
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="model-key">API Key / SK</Label>
        <Input
          id="model-key"
          type="password"
          placeholder={profile?.api_key_hint ? `已保存 ${profile.api_key_hint}，留空保持` : "输入服务商提供的密钥"}
          value={key}
          onChange={(e) => setKey(e.target.value)}
          disabled={!!busy}
          autoComplete="new-password"
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="model-id">模型</Label>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Input
            id="model-id"
            placeholder="选择或输入模型 ID"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            disabled={!!busy}
          />
          <Button
            type="button"
            variant="outline"
            onClick={loadModels}
            disabled={!!busy || (!key && !profile?.api_key_hint)}
          >
            {busy === "models" ? "获取中…" : "获取模型"}
          </Button>
        </div>
        {models.length > 0 && (
          <Popover open={modelsOpen} onOpenChange={setModelsOpen}>
            <PopoverTrigger asChild>
              <Button type="button" variant="outline" className="w-full justify-between font-normal">
                <span>浏览全部模型</span>
                <span className="flex items-center gap-2 text-muted-foreground">
                  {models.length} 个
                  <ChevronDown className="size-4" />
                </span>
              </Button>
            </PopoverTrigger>
            <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] gap-0 border p-0">
              <div className="flex items-center gap-2 border-b px-3">
                <Search className="size-4 shrink-0 text-muted-foreground" />
                <Input
                  aria-label="搜索模型"
                  value={modelQuery}
                  onChange={(event) => setModelQuery(event.target.value)}
                  placeholder="按模型 ID 搜索"
                  className="border-0 bg-transparent px-0 focus-visible:ring-0"
                />
              </div>
              <div role="listbox" aria-label="完整模型列表" className="max-h-80 overflow-y-auto p-1">
                {models
                  .filter((item) => item.toLowerCase().includes(modelQuery.trim().toLowerCase()))
                  .map((item) => (
                    <button
                      key={item}
                      type="button"
                      role="option"
                      aria-selected={model === item}
                      className="flex min-h-11 w-full items-center justify-between border-b px-3 py-2 text-left font-mono text-sm last:border-b-0 hover:bg-accent focus-visible:bg-accent"
                      onClick={() => {
                        setModel(item);
                        setModelsOpen(false);
                      }}
                    >
                      <span className="min-w-0 break-all">{item}</span>
                      {model === item && <Check className="ml-3 size-4 shrink-0 text-primary" />}
                    </button>
                  ))}
                {models.every((item) => !item.toLowerCase().includes(modelQuery.trim().toLowerCase())) && (
                  <p className="p-4 text-center text-sm text-muted-foreground">没有匹配的模型</p>
                )}
              </div>
            </PopoverContent>
          </Popover>
        )}
        <p className="text-xs text-muted-foreground">
          {models.length > 0
            ? `已读取全部 ${models.length} 个模型；可搜索、选择或直接填写模型 ID。`
            : "服务不支持模型列表时，可以直接填写模型 ID。"}
        </p>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="model-name">配置名称（可选）</Label>
        <Input
          id="model-name"
          placeholder="默认使用模型名称"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={!!busy}
        />
      </div>
      {!initial && (
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={makeDefault}
            onChange={(e) => setMakeDefault(e.target.checked)}
            disabled={!!busy}
          />
          设为默认模型
        </label>
      )}
      <details className="rounded-md border p-3">
        <summary className="cursor-pointer text-sm">高级连接与模型参数</summary>
        <div className="mt-3 space-y-3">
          <p className="text-xs text-muted-foreground">未修改的参数会保留。保存前验证真实工具调用和结果回传。</p>
          <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={advanced.streaming} disabled={!!busy} onChange={(e) => setAdvanced({ ...advanced, streaming: e.target.checked })} />使用流式响应</label>
          <div className="space-y-1"><Label htmlFor="model-proxy">连接代理</Label><Input id="model-proxy" value={advanced.proxy} disabled={!!busy} placeholder="http://127.0.0.1:7890" onChange={(e) => setAdvanced({ ...advanced, proxy: e.target.value })} /></div>
          <div className="space-y-1"><Label htmlFor="model-session-header">自定义会话头名称</Label><Input id="model-session-header" value={advanced.session_header_key} disabled={!!busy} onChange={(e) => setAdvanced({ ...advanced, session_header_key: e.target.value })} /></div>
          <div className="space-y-1"><Label htmlFor="model-effort">默认思考强度</Label><Input id="model-effort" value={advanced.reasoning_effort} disabled={!!busy} placeholder="留空使用接口默认值" onChange={(e) => setAdvanced({ ...advanced, reasoning_effort: e.target.value })} /></div>
          <div className="space-y-1"><Label htmlFor="model-thinking">思考模式</Label><NativeSelect id="model-thinking" value={advanced.thinking_type} disabled={!!busy} onChange={(e) => setAdvanced({ ...advanced, thinking_type: e.target.value })}><option value="">接口默认</option><option value="enabled">开启</option><option value="disabled">关闭</option></NativeSelect></div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1"><Label htmlFor="model-context">上下文容量（K Token）</Label><Input id="model-context" type="number" min={0} value={advanced.context_window_k} disabled={!!busy} onChange={(e) => setAdvanced({ ...advanced, context_window_k: Math.max(0, Number(e.target.value)) })} /></div>
            <div className="space-y-1"><Label htmlFor="model-output">输出上限（Token）</Label><Input id="model-output" type="number" min={0} value={advanced.max_tokens} disabled={!!busy} onChange={(e) => setAdvanced({ ...advanced, max_tokens: Math.max(0, Number(e.target.value)) })} /></div>
          </div>
          <p className="text-xs text-muted-foreground">容量 0 使用应用默认值；输出上限 0 使用接口默认值。</p>
          <div className="space-y-1"><Label htmlFor="model-output-field">输出上限字段</Label><NativeSelect id="model-output-field" value={advanced.max_tokens_field} disabled={!!busy} onChange={(e) => setAdvanced({ ...advanced, max_tokens_field: e.target.value })}><option value="">max_tokens（默认）</option><option value="max_completion_tokens">max_completion_tokens</option></NativeSelect></div>
        </div>
      </details>
      {error && (
        <p role="alert" className="rounded-md bg-destructive/10 p-3 text-sm text-destructive whitespace-pre-wrap">
          {error}
        </p>
      )}
      {tested && (
        <p role="status" className="text-sm text-emerald-600">
          {tested}
        </p>
      )}
      <Button className="w-full" onClick={save} disabled={!!busy}>
        {busy === "save" ? "正在测试并保存…" : "测试并保存"}
      </Button>
    </div>
  );
}

export function ModelSettings() {
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [editor, setEditor] = React.useState<LLMProfile | "new" | null>(null);
  const load = React.useCallback(async () => {
    try {
      setProfiles(await api.llmProfiles());
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);
  React.useEffect(() => {
    void load();
  }, [load]);
  async function activate(p: LLMProfile) {
    try {
      await api.activateLLMProfile(String(p.id));
      await load();
      window.dispatchEvent(new Event("prtseek-models-updated"));
    } catch (e) {
      toast.error((e as Error).message);
    }
  }
  async function remove(p: LLMProfile) {
    if (!window.confirm(`删除模型配置“${p.name}”？历史会话不会被删除。`)) return;
    try {
      await api.deleteLLMProfile(String(p.id));
      await load();
      window.dispatchEvent(new Event("prtseek-models-updated"));
    } catch (e) {
      toast.error((e as Error).message);
    }
  }
  return (
    <section className="space-y-5">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold">模型设置</h1>
          <p className="mt-1 text-sm text-muted-foreground">保存连接信息，在每段对话下方选择模型和思考强度。</p>
        </div>
        <Button onClick={() => setEditor("new")}>
          <Plus className="size-4" />
          添加模型
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      )}
      {loading ? (
        <p className="text-sm text-muted-foreground">正在读取模型…</p>
      ) : profiles.length === 0 ? (
        <div className="rounded-xl border border-dashed p-8 text-center">
          <p className="mb-4 text-muted-foreground">还没有模型配置，添加后即可开始对话。</p>
          <Button onClick={() => setEditor("new")}>配置第一个模型</Button>
        </div>
      ) : (
        <div className="space-y-3">
          {profiles.map((p) => (
            <article key={p.id} className="flex flex-wrap items-center justify-between gap-4 rounded-xl border p-4">
              <div className="min-w-0">
                <div className="flex items-center gap-2 font-medium">
                  {p.name}
                  {p.is_default && <span className="rounded bg-primary/10 px-2 py-0.5 text-xs text-primary">默认</span>}
                </div>
                <p className="mt-1 break-all text-sm text-muted-foreground">
                  {p.model} · {p.format}
                </p>
              </div>
              <div className="flex items-center gap-1">
                {!p.is_default && (
                  <Button variant="ghost" size="sm" onClick={() => activate(p)}>
                    <Check className="size-4" />
                    设为默认
                  </Button>
                )}
                <Button variant="ghost" size="sm" onClick={() => setEditor(p)}>
                  <Settings2 className="size-4" />
                  编辑
                </Button>
                <Button variant="ghost" size="icon" aria-label={`删除 ${p.name}`} onClick={() => remove(p)}>
                  <Trash2 className="size-4" />
                </Button>
              </div>
            </article>
          ))}
        </div>
      )}
      <Dialog
        open={editor !== null}
        onOpenChange={(open) => {
          if (!open) setEditor(null);
        }}
      >
        <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{editor === "new" ? "添加模型" : "编辑模型"}</DialogTitle>
          </DialogHeader>
          {editor && (
            <ModelEditor
              key={typeof editor === "string" ? editor : editor.id}
              profile={editor === "new" ? undefined : editor}
              initial={profiles.length === 0}
              onSaved={() => {
                setEditor(null);
                void load();
              }}
            />
          )}
        </DialogContent>
      </Dialog>
    </section>
  );
}
