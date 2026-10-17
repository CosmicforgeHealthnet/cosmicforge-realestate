import {describe,it,expect} from 'vitest';
import definition from '../../shared/workflows.json';
import fixture from '../../shared/workspace-seed.json';
import coreStories from '../../shared/journeys.json';
import supplements from '../../shared/supplementary-journeys.json';
const stories=[...coreStories,...supplements];
import {WorkspaceEngine,MockFault} from '@/lib/mock/workspace-engine';
import type {WorkspaceSchema,WorkspaceState,Values,WorkspaceCommand} from '@/lib/contracts/workspace';
const schema=definition as WorkspaceSchema,seed=fixture as unknown as WorkspaceState;
const engine=()=>new WorkspaceEngine(schema,seed);
function path(value:unknown,key:string):unknown{return key.split('.').reduce((v,k)=>(v as Record<string,unknown>)[k],value)}
describe('shared journey specification',()=>{
 for(const story of stories)it(`${story.id}: ${story.title}`,()=>{
  const repository=engine(),aliases:Record<string,string>={};
  const resolve=(value:unknown)=>typeof value==='string'&&value.startsWith('$')?aliases[value.slice(1)]:value;
  for(const raw of story.steps){const step=raw as unknown as Record<string,unknown>;
   if(step.op==='scenario'){repository.scenario(String(step.value));continue}
   if(step.op==='actor'){repository.actor(String(step.value));continue}
   if(step.op==='alias'){aliases[String(step.alias)]=String(step.id);continue}
   if(step.op==='bind'){const rows=repository.snapshot().records.filter(r=>r.type===step.type&&r.data[String(step.key)]===resolve(step.value));expect(rows.length).toBeGreaterThan(0);aliases[String(step.alias)]=rows.at(-1)!.id;continue}
   if(step.op==='assert'){expect(path(repository.record(aliases[String(step.target)]),String(step.key))).toEqual(resolve(step.value));continue}
   if(step.op==='summary'){expect(path(repository.summary(),String(step.key))).toEqual(step.value);continue}
   const target=step.target?repository.record(aliases[String(step.target)]):undefined;
   const command:WorkspaceCommand={op:step.op as WorkspaceCommand['op'],type:target?.type??String(step.type),id:target?.id,version:target?.version,data:Object.fromEntries(Object.entries(step.data as Record<string,unknown>??{}).map(([k,v])=>[k,resolve(v)])) as Values,action:step.action as string,confirmed:true,challenge:'DEMO'};
   const revision=repository.snapshot().revision;
   if(step.error){try{repository.execute(command);throw new Error('Expected failure')}catch(error){expect(error).toBeInstanceOf(MockFault);expect((error as MockFault).code).toBe(step.error)}expect(repository.snapshot().revision).toBe(revision)}
   else{const record=repository.execute(command);if(step.alias)aliases[String(step.alias)]=record.id}
  }
 });
});
describe('guards and integrity',()=>{
 it('all fixture foreign references resolve and family schemas cover 30 modules',()=>{const r=engine();for(const row of r.snapshot().records){for(const f of r.spec(row.type).fields.filter(f=>f.kind==='relation'))if(row.data[f.key])expect(r.record(String(row.data[f.key]),f.target).id).toBe(row.data[f.key])}expect(schema.modules).toHaveLength(30)});
 it('rolls back unknown, stale, denied, offline and failed operations',()=>{for(const [scenario,code] of [['restricted','RESTRICTED'],['permission_denied','DENIED'],['expired_session','EXPIRED_SESSION'],['offline','OFFLINE'],['provider_unavailable','UNAVAILABLE'],['conflict','CONFLICT'],['invalid_input','INVALID'],['insufficient_delegation','DENIED'],['failed_operation','FAILED']]){const r=engine();r.scenario(scenario);const row=r.record('demo-property-01');const before=r.snapshot();try{r.execute({op:'update',type:'property',id:row.id,version:row.version,data:{notes:'Changed'}});throw new Error('Expected failure')}catch(error){expect((error as MockFault).code).toBe(code)}expect(r.snapshot()).toEqual(before)}});
 it('requires a mock security challenge and preserves unknown financial outcomes',()=>{const r=engine();expect(()=>r.execute({op:'transition',type:'bank',id:'demo-bank-01',version:1,action:'review'})).toThrow('Type DEMO');r.scenario('unknown_financial_outcome');expect(()=>r.execute({op:'transition',type:'rent',id:'demo-rent-01',version:1,action:'observe',challenge:'DEMO'})).toThrow('unknown');expect(r.record('demo-rent-01').status).toBe('DUE')});
 it('preserves deterministic reset and blocks provider activation',()=>{const r=engine();r.scenario('empty');expect(r.summary().properties).toBe(0);r.reset();expect(r.summary().properties).toBe(4);expect(()=>r.execute({op:'create',type:'financing',data:{title:'No lender'}})).toThrow('No lender');const copy=r.snapshot();copy.records.length=0;expect(r.snapshot().records.length).toBeGreaterThan(0)});
});
