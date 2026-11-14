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

export function endReasonLabel(reason: string): string {
  return ({ completed: "正常结束", incomplete_stream: "响应流中断", service_restart: "服务重启中断", cancelled: "已取消", timeout: "达到时间上限", max_turns: "达到步数上限", model_error: "模型请求失败", runner_error: "运行异常", aborted_streaming: "流式响应被中止", aborted_tools: "工具执行被中止", blocking_limit: "上下文达到上限", prompt_too_long: "上下文过长", image_error: "图片处理失败", stop_hook_prevented: "结束检查未通过", hook_stopped: "执行被检查规则停止" } as Record<string, string>)[reason] ?? reason;
}

export function verificationLevelLabel(level: string): string {
  return ({ inference: "推断", observed: "实际观察", reproduced: "隔离实验复现", accepted: "平台接受" } as Record<string, string>)[level] ?? "未分级";
}
