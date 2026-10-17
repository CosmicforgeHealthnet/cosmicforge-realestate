'use client';
import Link from 'next/link';
import {useState} from 'react';
import {
  ArrowRight, ArrowUpRight, Building2, Check, CircleAlert, ClipboardList,
  HousePlus, Layers3, RotateCcw, ShieldCheck, Wallet, type LucideIcon,
} from 'lucide-react';
import {useDashboard} from './use-dashboard';
import {DashboardHub} from './dashboard-hub';
import {DashboardWorkbench} from './dashboard-workbench';
import {LoadingState,ErrorState,EmptyState} from '@/components/states';
import {PropertyArt} from '@/components/property-art';
import {money} from '@/lib/utils/format';
import {usePropertyScope} from '@/components/workspace-tools';
import {useWorkspace} from '@/features/workspace/use-workspace';
import type {Scenario} from '@/lib/contracts/owner';

type Metric={label:string;value:string|number;detail:string;href:string;Icon:LucideIcon;progress?:number};
export function DashboardScreen(){
  const [scenario,setScenario]=useState<Scenario>('populated');
  const [kind,setKind]=useState('All');
  const {data:rawData,isPending,error,task,refetch}=useDashboard(scenario);
  const {propertyId,setPropertyId,drafts}=usePropertyScope();
  const workspace=useWorkspace();
  const denied=['permission_denied','expired_session'].includes(workspace.data?.scenario??'');
  const data=rawData?{...rawData,properties:denied?[]:rawData.properties.filter(p=>propertyId==='all'||p.id===propertyId),tasks:denied?[]:rawData.tasks.filter(t=>propertyId==='all'||t.propertyId===propertyId)}:undefined;
  const properties=data?.properties.filter(p=>kind==='All'||p.kind===kind)??[];
  const unitCount=data?.properties.reduce((sum,p)=>sum+p.units,0)??0;
  const occupied=data?.properties.reduce((sum,p)=>sum+p.occupied,0)??0;
  const openTasks=data?.tasks.filter(t=>!t.done).length??0;
  const occupancy=unitCount?Math.round(occupied/unitCount*100):0;
  const metrics:Metric[]=[
    {label:'Portfolio properties',value:data?.properties.length??0,detail:`${unitCount} total units · fictional data`,href:'/workspace/properties',Icon:Building2},
    {label:'Active sample listings',value:data?.properties.reduce((n,p)=>n+p.listingCount,0)??0,detail:'Manage properties on the market',href:'/workspace/listings',Icon:Layers3},
    {label:'Occupancy rate',value:`${occupancy}%`,detail:`${occupied} of ${unitCount} units occupied`,href:'/workspace/properties?tab=unit',Icon:HousePlus,progress:occupancy},
    {label:'Items needing attention',value:openTasks,detail:'Local checklist and owner decisions',href:'#owner-tasks',Icon:ClipboardList},
  ];
  return <>
    <section className="owner-dashboard-hero" aria-labelledby="dashboard-welcome">
      <div className="owner-hero-copy">
        <span className="owner-hero-eyebrow">COSMICFORGE / INDIVIDUAL OWNER <span>·</span> PORTFOLIO OVERVIEW</span>
        <h1 id="dashboard-welcome">Welcome back, Alex<span className="title-dot">.</span></h1>
        <p>Your properties, people, finances and next decisions — together in one beautifully organized workspace.</p>
        <div className="owner-hero-actions">
          <Link className="owner-hero-primary" href="/workspace/properties">Manage portfolio <ArrowUpRight size={17} aria-hidden="true"/></Link>
          <Link className="owner-hero-secondary" href="/workspace/properties/property/new">Add demo property <HousePlus size={17} aria-hidden="true"/></Link>
        </div>
      </div>
      <div className="owner-hero-aside">
        <div className="owner-hero-orbit" aria-hidden="true"><Building2 size={63} strokeWidth={1}/><div className="owner-hero-orbit-small"><Layers3 size={22}/></div></div>
        <span className="owner-hero-caption">One workspace. Every property decision.</span>
      </div>
      <details className="dashboard-qa"><summary>Developer QA scenarios</summary><label className="scenario-control">Demo scenario<select value={scenario} onChange={e=>{setPropertyId('all');drafts.clear();setScenario(e.target.value as Scenario);workspace.scenario.mutate(e.target.value==='empty'?'empty':e.target.value==='error'?'failed_operation':'normal')}}><option value="populated">Sample portfolio</option><option value="empty">Empty portfolio</option><option value="error">Repository error</option></select></label></details>
    </section>
    <div className="owner-dashboard-disclosure" role="status"><ShieldCheck size={17} aria-hidden="true"/><span><strong>Interactive demo.</strong> Fictional records only. No real payments, identity checks, government approval or provider services.</span><Link href="/demo-lab">QA scenarios <ArrowRight size={14} aria-hidden="true"/></Link></div>
    {isPending?<LoadingState/>:error?<ErrorState message={error.message} retry={()=>void refetch()}/>:denied?<section className="card state"><ShieldCheck size={30}/><h2>Protected workspace unavailable</h2><p>Financial figures and property details are hidden for this demo access state.</p><Link className="button-link" href="/demo-lab">Review access scenario</Link></section>:data?<>
      <section aria-labelledby="owner-overview-heading" className="owner-overview">
        <div className="owner-section-header"><div><span className="eyebrow">AT A GLANCE</span><h2 id="owner-overview-heading">Portfolio overview</h2></div><Link href="/workspace/reports">Explore insights <ArrowUpRight size={16} aria-hidden="true"/></Link></div>
        <div className="owner-metrics">
          {metrics.map(({label,value,detail,href,Icon,progress},index)=><Link href={href} className={`owner-metric owner-metric-${index+1}`} key={label}>
            <div className="owner-metric-top"><span>{label}</span><span className="owner-metric-icon"><Icon size={21} aria-hidden="true"/></span></div>
            <strong>{value}</strong><small>{detail}</small>
            {progress!==undefined?<div className="owner-progress" role="progressbar" aria-label="Demo occupancy rate" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress}><span style={{width:`${progress}%`}}/></div>:<span className="owner-metric-next">Explore <ArrowUpRight size={14} aria-hidden="true"/></span>}
          </Link>)}
        </div>
      </section>
      <DashboardHub/>
      <div className="owner-command-grid">
        <section id="owner-tasks" className="card owner-priorities" aria-labelledby="owner-priorities-title">
          <div className="owner-section-header"><div><span className="eyebrow">YOUR NEXT DECISIONS</span><h2 id="owner-priorities-title">Needs your attention</h2></div><span className="owner-priority-count">{openTasks} open</span></div>
          <p className="owner-section-intro">Keep your portfolio moving. Checklist actions update this demo only.</p>
          {task.error?<p role="alert" className="feedback danger">Could not update your demo checklist: {task.error.message}</p>:null}
          {data.tasks.length?<div className="owner-task-list">{data.tasks.map((t,i)=><div className={`owner-task ${t.done?'is-done':''}`} key={t.id}>
            <span className="owner-task-index">{String(i+1).padStart(2,'0')}</span>
            <div><span className="owner-task-category">{t.category}</span><h3>{t.title}</h3><span className="owner-task-state">{t.done?'Completed in demo':'Awaiting your action'}</span></div>
            <button type="button" aria-label={t.done?'Reopen demo task':'Complete demo task'} title={t.title} disabled={task.isPending} onClick={()=>task.mutate({id:t.id,done:!t.done})}>{t.done?<RotateCcw size={16}/>:<Check size={17}/>}</button>
          </div>)}</div>:<div className="owner-empty"><CircleAlert size={25}/><strong>All caught up</strong><p>No demonstration tasks are waiting in this portfolio.</p></div>}
          <Link href="/workspace/notifications" className="owner-inline-link">Open notification center <ArrowRight size={16} aria-hidden="true"/></Link>
        </section>
        <section className="card owner-featured" aria-labelledby="owner-featured-title">
          <div className="owner-section-header"><div><span className="eyebrow">ASSET COLLECTION</span><h2 id="owner-featured-title">Portfolio properties</h2></div><Link href="/workspace/properties">View all <ArrowUpRight size={16} aria-hidden="true"/></Link></div>
          <div className="owner-type-filters" aria-label="Property type">{['All','Residential','Commercial','Short-let','Land'].map(t=><button key={t} type="button" aria-pressed={kind===t} onClick={()=>setKind(t)}>{t}</button>)}</div>
          {properties.length?<div className="owner-featured-list">{properties.slice(0,4).map((p,i)=><Link className="owner-featured-property" href={`/properties/${p.id}`} key={p.id}>
            <div className="owner-featured-media"><PropertyArt variant={i}/></div>
            <div className="owner-featured-details"><span className="owner-task-category">{p.kind} · {p.units} {p.units===1?'unit':'units'}</span><h3>{p.name}</h3><p>{p.location}</p><span className="status warning">{p.verification}</span></div>
            <ArrowUpRight size={19} aria-hidden="true"/>
          </Link>)}</div>:<EmptyState title="No sample properties here">{data.properties.length?'Choose a different property type.':'Start your owner journey by adding a fictional property.'}</EmptyState>}
          <Link href="/workspace/properties/property/new" className="owner-inline-link">Add another demo property <HousePlus size={16} aria-hidden="true"/></Link>
        </section>
      </div>
      <section className="card owner-finance" aria-labelledby="owner-finance-heading">
        <div className="owner-section-header"><div><span className="eyebrow">DEMONSTRATION FINANCE</span><h2 id="owner-finance-heading">Your financial snapshot</h2><p>Balances are illustrative classifications, not cash held or moved.</p></div><Link href="/workspace/financial" className="button-link">Open wallet <ArrowUpRight size={16} aria-hidden="true"/></Link></div>
        <div className="owner-finance-grid">{data.balances.map((b,i)=><div className={`owner-finance-bucket owner-finance-${i+1}`} key={b.label}><span className="owner-finance-label">{b.label}</span><strong>{money(b.amountMinor)}</strong><p>{b.description}</p></div>)}</div>
        <div className="owner-finance-foot"><span><ShieldCheck size={16} aria-hidden="true"/> No funds or payments are processed in this demo.</span><Link href="/workspace/rent">Rent & deposit lifecycle <ArrowRight size={15} aria-hidden="true"/></Link></div>
      </section>
      <DashboardWorkbench/>
    </>:null}
  </>;
}
