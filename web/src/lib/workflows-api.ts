import { http } from "@/lib/api";

export interface Delegation {
  parent_id?: number;
  id: number;
  agent_key: string;
  title: string;
  state: string;
  running: boolean;
  activities: DelegationActivity[];
  outcome?: CTFOutcome;
}

interface CTFOutcome {
  state: "unverified" | "partial" | "blocked" | "verified";
  summary?: string;
  criterion?: string;
  verification?: string;
  verification_level?: "inference" | "observed" | "reproduced" | "accepted";
  evidence?: { conversation_id: number; id: string }[];
  artifacts?: string[];
}

export interface ConversationOverview {
  state: string;
  started_ms: number;
  finished_ms: number;
  elapsed_ms: number;
  model_calls: number;
  model_call_limit: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  turn_id?: number;
  end_reason?: string;
  active_children: number;
  latest?: DelegationActivity;
  outcome: CTFOutcome;
}

export interface ConversationFile {
  conversation_id: number;
  name: string;
  path: string;
  workspace_path: string;
  kind: "input" | "artifact" | "evidence";
  size: number;
  modified_ms: number;
}

export interface DelegationActivity {
  seq: number;
  worker: string;
  ts: string;
  kind: string;
  tool?: string;
  is_error: boolean;
  summary: string;
}
export const workflowsApi = {
  delegations: (id: number) => http<{ items: Delegation[] }>(`/conversations/${id}/delegations`),
  overview: (id: number) => http<ConversationOverview>(`/conversations/${id}/overview`),
  files: (id: number) => http<{ items: ConversationFile[]; truncated: boolean; workspace_path: string }>(`/conversations/${id}/files`),
  preview: (id: number, file: ConversationFile) => http<{ content?: string; binary?: boolean; too_large?: boolean }>(`/conversations/${id}/file?conversation_id=${file.conversation_id}&path=${encodeURIComponent(file.path)}`),
};
