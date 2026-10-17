'use client';
import Link from 'next/link';
import { useState } from 'react';
import { ArrowRight, Building2, ClipboardList, Layers, ShieldCheck, Wallet, Check, RotateCcw, ArrowUpRight } from 'lucide-react';
import { useDashboard } from './use-dashboard';
import { LoadingState, ErrorState, EmptyState } from '@/components/states';
import { PropertyArt } from '@/components/property-art';
import { money } from '@/lib/utils/format';
import {usePropertyScope} from '@/components/workspace-tools';
import {useWorkspace} from '@/features/workspace/use-workspace';
import {DashboardWorkbench} from './dashboard-workbench';
import type { Scenario } from '@/lib/contracts/owner';
export function DashboardScreen() {
  const [scenario,setScenario] = useState<Scenario>('populated');
  const [kind,setKind] = useState('All');
  const {data:rawData,isPending,error,task,refetch} = useDashboard(scenario);
  const {propertyId,setPropertyId,drafts}=usePropertyScope(),workspace=useWorkspace();
  const denied=['permission_denied','expired_session'].includes(workspace.data?.scenario??'');
  const data=rawData?{...rawData,properties:denied?[]:rawData.properties.filter(p=>propertyId==='all'||p.id===propertyId),tasks:denied?[]:rawData.tasks.filter(t=>propertyId==='all'||t.propertyId===propertyId)}:undefined;
  const properties = data?.properties.filter(p=>kind==='All'||p.kind===kind) ?? [];
  const openTasks = data?.tasks.filter(t=>!t.done).length ?? 0;
  return <>
    <div className="page-heading"><div><p className="eyebrow">YOUR PORTFOLIO, AT A GLANCE</p><h1>Welcome back, Alex<span className="title-dot">.</span></h1><p>A clear view of your properties and what needs attention.</p></div><details className="dashboard-qa"><summary>Developer QA scenarios</summary><label className="scenario-control">Demo scenario<select value={scenario} onChange={e=>{setPropertyId('all');drafts.clear();setScenario(e.target.value as Scenario);workspace.scenario.mutate(e.target.value==='empty'?'empty':e.target.value==='error'?'failed_operation':'normal')}}><option value="populated">Sample portfolio</option><option value="empty">Empty portfolio</option><option value="error">Repository error</option></select></label></details></div>
    <div className="notice"><ShieldCheck size={24} aria-hidden="true"/><div><strong>A workspace built around owner control</strong><p>Mock mode · All records are fictional. Verification is pending and financial services are unavailable.</p></div><span className="badge">Foundation preview</span></div>
    {isPending ? <LoadingState/> : error ? <ErrorState message={error.message} retry={()=>void refetch()}/> : data ? <>
    <section className="metrics" aria-label="Portfolio summary">
      {[
        {label:'Portfolio properties',value:data.properties.length,detail:`${data.properties.reduce((n,p)=>n+p.units,0)} units across your demo portfolio`,Icon:Building2},
        {label:'Sample listings',value:data.properties.reduce((n,p)=>n+p.listingCount,0),detail:'Demonstration records only',Icon:Layers},
        {label:'Occupied units',value:`${data.properties.reduce((n,p)=>n+p.occupied,0)} / ${data.properties.reduce((n,p)=>n+p.units,0)}`,detail:'Illustrative occupancy snapshot',Icon:Wallet},
        {label:'Open demo tasks',value:openTasks,detail:'Local checklist · no external actions',Icon:ClipboardList},
      ].map(({label,value,detail,Icon},index)=><article className="metric card" key={label}><div className="metric-top"><span>{label}</span><span className="metric-icon"><Icon size={20} aria-hidden="true"/></span></div><strong>{value}</strong><small>{detail}</small><Link href={['/workspace/properties','/workspace/listings','/workspace/properties?tab=unit','/#owner-tasks'][index]}>View details</Link></article>)}
    </section>
    <section className="card finance"><div className="section-heading"><div><p className="eyebrow">DEMONSTRATION LEDGER</p><h2>Finance overview</h2></div><Link href="/workspace/financial">Explore wallet <ArrowUpRight size={16}/></Link></div><div className="balances">{data.balances.map(b=><article key={b.label}><span className={`balance-label ${b.state}`}>{b.label}</span><strong>{money(b.amountMinor)}</strong><p>{b.description}</p></article>)}</div><div className="finance-foot"><span>Derived fictional ledger · 17 Oct 2026, 09:00 UTC</span><button disabled title="Payments require a later approved integration">Payout unavailable</button></div></section>
    <div className="dashboard-columns"><section className="card portfolio"><div className="section-heading"><div><p className="eyebrow">{data.workspaceName}</p><h2>Portfolio properties</h2></div><span className="count">{data.properties.length}</span></div><div className="filter-row" aria-label="Property type">{['All','Residential','Commercial','Short-let'].map(t=><button key={t} aria-pressed={kind===t} onClick={()=>setKind(t)}>{t}</button>)}</div>{properties.length ? <div className="property-list">{properties.map((p,i)=><Link className="property-row" href={`/properties/${p.id}`} key={p.id}><div className="thumbnail"><PropertyArt variant={i}/></div><div><span className="eyebrow">{p.kind} · {p.units} {p.units===1?'unit':'units'}</span><h3>{p.name}</h3><p>{p.location}</p><span className="status warning">{p.verification}</span></div><ArrowRight size={20} aria-hidden="true"/></Link>)}</div>:<EmptyState title="No sample properties here">{data.properties.length ? 'Choose another property type.' : 'This is the new-owner empty state. Create a fictional property to begin.'}</EmptyState>}<Link className="panel-link" href="/workspace/properties">Open property workspace <ArrowRight size={16}/></Link></section>
    <div className="right-column"><section id="owner-tasks" className="card tasks"><div className="section-heading"><div><p className="eyebrow">LOCAL CHECKLIST</p><h2>Needs your attention</h2></div><span className="count">{openTasks}</span></div><p className="muted">Checking a task updates this demo session only.</p>{task.error ? <p role="alert">{task.error.message}</p>:null}{data.tasks.length ? data.tasks.map(t=><div className={`task-row ${t.done?'done':''}`} key={t.id}><span className="eyebrow">{t.category}</span><h3>{t.title}</h3><button className="text-button" disabled={task.isPending} onClick={()=>task.mutate({id:t.id,done:!t.done})}>{t.done?<RotateCcw size={15}/>:<Check size={15}/>} {t.done?'Reopen demo task':'Complete demo task'}</button></div>):<p className="muted">No demo tasks yet.</p>}</section><section className="card safeguards"><ShieldCheck size={24} aria-hidden="true"/><h2>Your next chapter</h2><p>Continue connected demo workflows. External services remain unavailable.</p><Link href="/more">Browse all 30 areas <ArrowRight size={16}/></Link></section></div></div>
    <DashboardWorkbench/></>:null}
  </>;
}
