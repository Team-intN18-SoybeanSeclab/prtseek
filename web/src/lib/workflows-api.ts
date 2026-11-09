import { http } from "@/lib/api";

export interface Delegation {
  parent_id?: number;
  id: number;
  agent_key: string;
  title: string;
  state: string;
}
export const workflowsApi = {
  delegations: (id: number) => http<{ items: Delegation[] }>(`/conversations/${id}/delegations`),
};
