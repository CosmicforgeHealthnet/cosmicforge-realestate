import type { Dashboard, Scenario } from '@/lib/contracts/owner';
import type { WorkspaceRepository } from '@/lib/contracts/workspace';
export interface OwnerRepository extends WorkspaceRepository {
  getDashboard(scenario: Scenario): Promise<Dashboard>;
  setTaskDone(id: string, done: boolean): Promise<void>;
  renameWorkspace(name: string): Promise<void>;
}
