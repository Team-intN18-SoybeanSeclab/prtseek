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
  const id = profile ? Number(profile.id) : undefined;

  async function loadModels() {
    setBusy("models");
    setError("");
    try {
      const result = await api.fetchLLMModels(format, baseUrl.trim(), key, "", id);
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
      const result = await api.testLLM(format, model.trim(), baseUrl.trim(), key, "", "", "", id, true, "");
      if (!result.ok) throw new Error(result.error || "模型未返回有效回复");
      setTested(`连接成功 · ${result.latency_ms ?? "—"} ms${result.reply ? ` · ${result.reply}` : ""}`);
      const saved = await api.saveLLMProfile({
        ...(profile ? { ...profile, id } : {}),
        name: name.trim() || model.trim(),
        format,
        base_url: baseUrl.trim(),
        api_key: key,
        model: model.trim(),
        // Personal mode deliberately does not retain invisible request modifiers.
        proxy: "",
        session_header_key: "",
        reasoning_effort: "",
        thinking_type: "",
        streaming: true,
        pool_exclude: true,
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
      {profile && (profile.proxy || profile.session_header_key) && (
        <p className="text-sm text-amber-600">
          此旧配置含额外连接参数。保存为基本配置会移除这些参数，请以本次连接测试结果为准。
        </p>
      )}
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
