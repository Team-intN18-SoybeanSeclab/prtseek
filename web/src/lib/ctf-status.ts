export function executionLabel(state: string, running = false): string {
  if (running || state === "running") return "执行中";
  return ({ completed: "运行结束", cancelled: "已取消", interrupted: "已中断", timed_out: "已超时", failed: "执行失败", idle: "待命" } as Record<string, string>)[state] ?? "状态未知";
}

export function outcomeLabel(state: string): string {
  return ({ verified: "已验证 · Agent 报告", partial: "部分进展", blocked: "目标受阻", unverified: "目标待验证" } as Record<string, string>)[state] ?? "目标待验证";
}

export function elapsedLabel(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  return seconds < 60 ? `${seconds} 秒` : `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
}
