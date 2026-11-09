import { http } from "@/lib/api";

export interface Delegation {
  parent_id?: number;
  id: number;
  agent_key: string;
  title: string;
  state: string;
  running: boolean;
  activities: DelegationActivity[];
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
};
