import type { Dashboard, Scenario } from '@/lib/contracts/owner';
export interface OwnerRepository {
  getDashboard(scenario: Scenario): Promise<Dashboard>;
  setTaskDone(id: string, done: boolean): Promise<void>;
  renameWorkspace(name: string): Promise<void>;
}
