import { http } from "@/lib/api";
import type { Activity } from "@/lib/types";

type OnboardingStatus = "pending" | "skipped" | "completed";
export interface OnboardingState {
  model: OnboardingStatus;
  tools: OnboardingStatus;
}
interface BackgroundRun {
  id: number;
  task_id: string;
  status: string;
  paused: boolean;
  goal: string;
  checkpoint: string;
  source_cursor: number;
  created_at: string;
  timeout_seconds: number;
}
export interface BackgroundState {
  run: BackgroundRun | null;
  activities: Activity[];
  cursor: number;
  has_more?: boolean;
}

const json = (method: string, body: unknown) => ({ method, body: JSON.stringify(body) });
export const personalApi = {
  onboarding: () => http<OnboardingState>("/personal/onboarding"),
  updateOnboarding: (patch: Partial<OnboardingState>) =>
    http<OnboardingState>("/personal/onboarding", json("PATCH", patch)),
  capabilities: (profile: number) =>
    http<{ reasoning_efforts: string[]; verified: boolean }>(`/personal/model-capabilities?profile_id=${profile}`),
  verifyEffort: (profile_id: number, reasoning_effort: string) =>
    http<{ verified: boolean; error?: string }>(
      "/personal/model-capabilities",
      json("POST", { profile_id, reasoning_effort }),
    ),
  background: (id: number, since = 0) => http<BackgroundState>(`/conversations/${id}/background?since=${since}`),
  startBackground: (id: number, goal: string, timeout: number, key: string) =>
    http<BackgroundState>(
      `/conversations/${id}/background`,
      json("POST", { goal, timeout_seconds: timeout, idempotency_key: key }),
    ),
  controlBackground: (id: number, action: "pause" | "resume" | "stop") =>
    http<BackgroundState>(`/conversations/${id}/background/control`, json("POST", { action })),
  backgroundDetail: (id: number, seq: number) =>
    http<{ detail: string }>(`/conversations/${id}/background/activities/${seq}`).then((r) => r.detail),
};
