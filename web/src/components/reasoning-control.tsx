"use client";

import * as React from "react";

import { toast } from "sonner";
import { NativeSelect } from "@/components/ui/native-select";

import { personalApi } from "@/lib/personal-api";

const labels: Record<string, string> = {
  none: "关闭",
  minimal: "最少",
  low: "低",
  medium: "中",
  high: "高",
  xhigh: "最高",
  max: "最大",
  ultra: "极高",
};

export function ReasoningControl({
  profileId,
  value,
  onChange,
  disabled = false,
}: {
  profileId?: number;
  value: string;
  onChange: (value: string) => void | Promise<void>;
  disabled?: boolean;
}) {
  const [efforts, setEfforts] = React.useState<string[]>([]);
  const [busy, setBusy] = React.useState(false);
  const [loading, setLoading] = React.useState(false);
  const [hint, setHint] = React.useState("");
  const version = React.useRef(0);
  React.useEffect(() => {
    const current = ++version.current;
    setEfforts([]);
    setBusy(false);
    setHint("");
    if (!profileId) {
      setLoading(false);
      return;
    }
    setLoading(true);
    personalApi
      .capabilities(profileId)
      .then((result) => {
        if (version.current !== current) return;
        setEfforts(result.reasoning_efforts.filter(Boolean));
        setHint(
          result.reasoning_efforts.filter(Boolean).length
            ? "选择非默认强度时将验证接口支持，下一轮生效"
            : "该模型尚未确认可调强度，使用模型默认",
        );
      })
      .catch(() => {
        if (version.current === current) setHint("无法读取模型能力，暂用模型默认");
      })
      .finally(() => {
        if (version.current === current) setLoading(false);
      });
    return () => {
      version.current++;
    };
  }, [profileId]);

  async function choose(next: string) {
    if (!profileId || busy) return;
    const current = version.current;
    setBusy(true);
    try {
      if (next) {
        const result = await personalApi.verifyEffort(profileId, next);
        if (!result.verified) throw new Error(result.error || "接口没有确认此强度，请使用模型默认");
      }
      if (version.current === current) await onChange(next);
    } catch (e) {
      if (version.current === current) toast.error((e as Error).message);
    } finally {
      if (version.current === current) setBusy(false);
    }
  }

  return (
    <label
      className="flex min-h-9 items-center gap-1 rounded-md border px-2 text-xs text-muted-foreground"
      title={hint}
    >
      <span>{busy ? "验证中…" : "思考"}</span>
      <NativeSelect
        aria-label="思考强度"
        value={value}
        onChange={(e) => void choose(e.target.value)}
        disabled={disabled === true || busy || loading || !profileId}
        className="max-w-36 border-0 bg-transparent py-1 pr-8 text-foreground focus-visible:ring-0"
      >
        <option value="">模型默认</option>
        {value && !efforts.includes(value) && <option value={value}>{labels[value] ?? value}（待确认）</option>}
        {efforts.map((effort) => (
          <option key={effort} value={effort}>
            {labels[effort] ?? effort}
          </option>
        ))}
      </NativeSelect>
    </label>
  );
}
