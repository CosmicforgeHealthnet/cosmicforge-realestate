import seed from '../../../../shared/dashboard.json';
import type { Dashboard, Scenario } from '@/lib/contracts/owner';
import type { OwnerRepository } from '@/lib/repositories/owner-repository';
export class MockOwnerRepository implements OwnerRepository {
  private state: Dashboard = structuredClone(seed);
  async getDashboard(scenario: Scenario): Promise<Dashboard> {
    if (scenario === 'error') throw new Error('Simulated repository failure. Switch to the populated scenario to recover.');
    const snapshot = structuredClone(this.state);
    if (scenario === 'empty') return { ...snapshot, properties: [], tasks: [], balances: snapshot.balances.map(b => ({...b, amountMinor: 0})) };
    return snapshot;
  }
  async setTaskDone(id: string, done: boolean) {
    const task = this.state.tasks.find(t => t.id === id);
    if (!task) throw new Error('Sample task not found.');
    task.done = done;
  }
  async renameWorkspace(name: string) {
    const clean = name.trim();
    if (clean.length < 2 || clean.length > 60) throw new Error('Use 2–60 characters.');
    this.state.workspaceName = clean;
  }
}
