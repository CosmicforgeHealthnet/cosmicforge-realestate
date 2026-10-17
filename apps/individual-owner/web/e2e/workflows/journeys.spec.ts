import {test,expect,type Page} from '@playwright/test';
import {readFileSync} from 'node:fs';
import type {WorkspaceSchema,WorkspaceState,Values,DomainRecord} from '../../src/lib/contracts/workspace';
const schema=JSON.parse(readFileSync(new URL('../../../shared/workflows.json',import.meta.url),'utf8')) as WorkspaceSchema;
const journeys=[...JSON.parse(readFileSync(new URL('../../../shared/journeys.json',import.meta.url),'utf8')),...JSON.parse(readFileSync(new URL('../../../shared/supplementary-journeys.json',import.meta.url),'utf8'))] as {id:string;title:string;steps:Record<string,unknown>[]}[];
async function lab(page:Page){if(!page.url().endsWith('/demo-lab'))await page.locator('a.qa-link').click();await expect(page.getByRole('heading',{name:'Deterministic demo laboratory',exact:true})).toBeVisible();const enable=page.getByRole('button',{name:'Enable developer QA',exact:true});if(await enable.count())await enable.click()}
async function snapshot(page:Page):Promise<WorkspaceState>{await lab(page);await page.getByText('Inspect fictional state',{exact:true}).click();return JSON.parse((await page.locator('#qa-state').textContent())!)}
async function searchLink(page:Page,query:string,href:string){await page.getByRole('button',{name:'Search workspace…'}).click();const dialog=page.getByRole('dialog',{name:'Search workspace',exact:true});await dialog.getByRole('searchbox').fill(query);await dialog.locator(`a[href="${href}"]`).click()}
async function openFamily(page:Page,type:string){const spec=schema.types.find(t=>t.id===type)!,area=schema.modules.find(m=>m.slug===spec.module)!;await searchLink(page,area.title,`/workspace/${spec.module}`);await page.locator(`.workspace-tabs a[href="/workspace/${spec.module}?tab=${type}"]`).click();return spec}
async function form(page:Page,type:string,data:Values,record?:DomainRecord,error=false){const spec=schema.types.find(t=>t.id===type)!;
 if(record){await searchLink(page,String(record.data.title),`/workspace/${spec.module}/${type}/${record.id}`);await page.getByRole('link',{name:'Edit record',exact:true}).click()}
 else{await openFamily(page,type);await page.getByRole('link',{name:`New ${spec.label.toLowerCase()}`,exact:true}).click()}
 await expect(page.locator('#field-title')).toBeVisible();
 for(let step=0;step<(spec.wizard?3:1);step++){
  for(const field of spec.fields){if(!(field.key in data))continue;const input=page.locator(`#field-${field.key}`);if(field.kind==='boolean'){const checkbox=page.locator(`input[name="${field.key}"]`);if(await checkbox.count())await checkbox.setChecked(Boolean(data[field.key]));continue}if(!await input.count())continue;const value=data[field.key];if(field.kind==='select'||field.kind==='relation')await input.selectOption(String(value));else await input.fill(String(value))}
  if(spec.wizard&&step<2)await page.getByRole('button',{name:'Next step',exact:true}).click();
 }
 if(spec.sensitive)await page.getByLabel('Security confirmation',{exact:true}).fill('DEMO');
 await page.getByRole('button',{name:'Save demo record',exact:true}).click();
 if(error){await expect(page.getByRole('main').getByRole('alert').first()).toBeVisible();return ''}
 await expect(page.locator('.detail-toolbar')).toBeVisible();return decodeURIComponent(new URL(page.url()).pathname.split('/').at(-1)!);
}
function at(value:unknown,path:string):unknown{return path.split('.').reduce((v,k)=>(v as Record<string,unknown>)[k],value)}
for(const journey of journeys)test(`Journey ${journey.id}: ${journey.title} through rendered forms and decisions`,async({page})=>{
 test.setTimeout(180000);
 const external:string[]=[],errors:string[]=[];page.on('request',r=>{const u=new URL(r.url());if(['http:','https:'].includes(u.protocol)&&!['127.0.0.1','localhost'].includes(u.hostname))external.push(r.url())});page.on('pageerror',e=>errors.push(e.message));page.on('dialog',d=>d.accept());
 await page.goto('/');const aliases:Record<string,string>={};let state=await snapshot(page);
 const resolve=(v:unknown)=>typeof v==='string'&&v.startsWith('$')?aliases[v.slice(1)]:v;
 for(const raw of journey.steps){const step=raw as unknown as Record<string,unknown>;
  if(step.op==='scenario'){await lab(page);await page.getByLabel('QA scenario',{exact:true}).selectOption(String(step.value));await expect(page.getByRole('status').filter({hasText:'Scenario applied'})).toBeVisible();state=await snapshot(page);continue}
  if(step.op==='actor'){await lab(page);await page.getByLabel('Simulated actor',{exact:true}).selectOption(String(step.value));await expect(page.getByLabel('Simulated actor')).toHaveValue(String(step.value));state=await snapshot(page);continue}
  if(step.op==='alias'){aliases[String(step.alias)]=String(step.id);continue}
  if(step.op==='bind'){const rows=state.records.filter(r=>r.type===step.type&&r.data[String(step.key)]===resolve(step.value));expect(rows.length).toBeGreaterThan(0);aliases[String(step.alias)]=rows.at(-1)!.id;continue}
  if(step.op==='assert'){expect(at(state.records.find(r=>r.id===aliases[String(step.target)]),String(step.key))).toEqual(resolve(step.value));continue}
  if(step.op==='summary')continue; // Exact derived totals are asserted by the two engine suites and report UI checks.
  const target=step.target?state.records.find(r=>r.id===aliases[String(step.target)]):undefined,previousRevision=state.revision;
  const values=Object.fromEntries(Object.entries(step.data as Record<string,unknown>??{}).map(([k,v])=>[k,resolve(v)])) as Values;
  if(step.op==='create'||step.op==='update'){const id=await form(page,target?.type??String(step.type),values,target,Boolean(step.error));if(step.alias&&!step.error)aliases[String(step.alias)]=id}
  else{const spec=schema.types.find(t=>t.id===target!.type)!,action=spec.actions.find(a=>a.id===step.action)!;await searchLink(page,String(target!.data.title),`/workspace/${spec.module}/${target!.type}/${target!.id}`);await page.getByRole('button',{name:action.label,exact:true}).click();if(action.stepUp||action.confirm){const dialog=page.getByRole('dialog',{name:action.label,exact:true});if(action.stepUp)await dialog.getByLabel('Security confirmation',{exact:true}).fill('DEMO');await dialog.getByRole('button',{name:'Confirm demo action',exact:true}).click();if(step.error){await expect(dialog.getByRole('alert')).toBeVisible();await dialog.getByRole('button',{name:'Cancel',exact:true}).click()}}if(step.error)await expect(page.getByRole('main').getByRole('alert').first()).toBeVisible();else await expect(page.getByRole('status').filter({hasText:'Recorded in demo'})).toBeVisible()}
  state=await snapshot(page);expect(state.revision).toBe(step.error?previousRevision:previousRevision+1);
 }
 expect(external).toEqual([]);expect(errors).toEqual([]);
});
