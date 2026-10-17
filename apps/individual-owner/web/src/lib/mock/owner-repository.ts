import seed from '../../../../shared/dashboard.json';
import type { Dashboard, Scenario } from '@/lib/contracts/owner';
import type { OwnerRepository } from '@/lib/repositories/owner-repository';
import schema from '../../../../shared/workflows.json';
import universe from '../../../../shared/workspace-seed.json';
import { WorkspaceEngine } from './workspace-engine';
import type {WorkspaceSchema,WorkspaceState,WorkspaceCommand} from '@/lib/contracts/workspace';
export class MockOwnerRepository implements OwnerRepository {
  readonly engine=new WorkspaceEngine(schema as WorkspaceSchema,universe as unknown as WorkspaceState);
  async readWorkspace(){return this.engine.snapshot()}
  async execute(command:WorkspaceCommand){return this.engine.execute(command)}
  async setScenario(scenario:string){this.engine.scenario(scenario)}
  async setActor(actor:string){this.engine.actor(actor)}
  async resetDemo(){this.engine.reset()}
  async getDashboard(scenario: Scenario): Promise<Dashboard> {
    if (scenario === 'error') throw new Error('Simulated repository failure. Switch to the populated scenario to recover.');
    const state=this.engine.snapshot(),summary=this.engine.summary();
    const snapshot:Dashboard={...structuredClone(seed),workspaceName:state.workspaceName,tasks:state.tasks,
      properties:state.records.filter(r=>r.type==='property'&&r.status!=='ARCHIVED').map(r=>({id:r.id,name:String(r.data.title),location:String(r.data.location),kind:String(r.data.kind),units:state.records.filter(u=>u.type==='unit'&&u.data.propertyId===r.id).length,occupied:state.records.filter(u=>u.type==='unit'&&u.data.propertyId===r.id&&u.data.occupied).length,listingCount:state.records.filter(l=>l.type==='listing'&&l.data.propertyId===r.id&&l.status==='DEMO_PUBLISHED').length,verification:state.records.some(v=>v.type==='verification'&&v.data.propertyId===r.id&&v.status==='DEMO_REVIEWED')?'Demo review recorded':'Pending review',annualRentMinor:Number(r.data.annualRentMinor??0)})),
      balances:seed.balances.map((b,i)=>({...b,amountMinor:summary.buckets[['AVAILABLE','PENDING','HELD','RESTRICTED'][i]]}))};
    if (scenario === 'empty') return { ...snapshot, properties: [], tasks: [], balances: snapshot.balances.map(b => ({...b, amountMinor: 0})) };
    return snapshot;
  }
  async setTaskDone(id: string, done: boolean) {
    this.engine.setTaskDone(id,done);
  }
  async renameWorkspace(name: string) {
    const clean = name.trim();
    if (clean.length < 2 || clean.length > 60) throw new Error('Use 2–60 characters.');
    this.engine.renameWorkspace(clean);
  }
}
