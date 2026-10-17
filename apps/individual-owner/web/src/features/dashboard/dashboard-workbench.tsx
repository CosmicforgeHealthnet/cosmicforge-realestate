'use client';
import Link from 'next/link';
import {useRouter} from 'next/navigation';
import {ArrowRight,ArrowUpRight,Bell,CalendarDays,ChartNoAxesCombined,Clock3,Wrench} from 'lucide-react';
import {useWorkspace,recordPath,deriveWorkspace} from '@/features/workspace/use-workspace';
import {Calendar} from '@/components/workspace-ui';
import {usePropertyScope} from '@/components/workspace-tools';

const finished=new Set(['COMPLETED','CANCELLED','CLOSED','CHECKED_OUT','DECLINED','ACCEPTED','RESOLVED','REVOKED','NO_SHOW']);
export function DashboardWorkbench(){
  const {data}=useWorkspace();
  const {propertyId}=usePropertyScope();
  const router=useRouter();
  if(!data)return null;
  if(['permission_denied','expired_session'].includes(data.scenario))return <section className="card state"><h2>{data.scenario==='permission_denied'?'Permission denied':'Demo session expired'}</h2><Link href="/demo-lab">Review developer QA state</Link></section>;
  const projection=deriveWorkspace(data);
  const summary=projection.summary(propertyId);
  const records=data.records.filter(r=>propertyId==='all'||projection.propertyOf(r)===propertyId);
  const upcoming=records.filter(r=>['viewing','booking','maintenance','appointment','preventive','loi'].includes(r.type)&&!finished.has(r.status));
  const activity=data.activity.filter(a=>propertyId==='all'||a.propertyId===propertyId);
  const unitCount=Number(summary.units)||0, occupied=Number(summary.occupied)||0;
  const occupancy=unitCount?Math.min(100,Math.max(0,occupied/unitCount*100)):0;
  const stats=[
    {label:'Occupied units',value:`${occupied} / ${unitCount}`,bar:occupancy},
    {label:'Demo listings',value:String(summary.listings),bar:null},
    {label:'Completed maintenance',value:String(summary.maintenanceCompleted),bar:null},
  ];
  return <>
    <div className="owner-operational-grid">
      <section className="card owner-operations" aria-labelledby="owner-ops-title">
        <div className="owner-section-header"><div><span className="eyebrow">COMING UP</span><h2 id="owner-ops-title">Operations & schedule</h2></div><CalendarDays size={23} aria-hidden="true"/></div>
        <div className="owner-ops-stats">
          <Link href="/workspace/viewings"><CalendarDays size={17} aria-hidden="true"/><span>Viewings awaiting outcome</span><strong>{records.filter(r=>r.type==='viewing'&&['REQUESTED','APPROVED'].includes(r.status)).length}</strong></Link>
          <Link href="/workspace/shortlets"><Clock3 size={17} aria-hidden="true"/><span>Active demo bookings</span><strong>{summary.bookings}</strong></Link>
          <Link href="/workspace/maintenance"><Wrench size={17} aria-hidden="true"/><span>Open maintenance</span><strong>{summary.maintenanceOpen}</strong></Link>
          <Link href="/workspace/notifications"><Bell size={17} aria-hidden="true"/><span>Unread local alerts</span><strong>{records.filter(r=>r.type==='notification'&&r.status==='UNREAD').length}</strong></Link>
        </div>
        <h3 className="owner-subheading">Upcoming owner activity</h3>
        {upcoming.length?<div className="owner-upcoming-list">{upcoming.slice(0,5).map(r=><Link href={recordPath(r.type,r.id)} key={r.id}><span className="owner-upcoming-dot" aria-hidden="true"/><span><strong>{String(r.data.title)}</strong><small>{String(r.data.date??r.data.start??'Date pending')} · {r.status}</small></span><ArrowUpRight size={16} aria-hidden="true"/></Link>)}</div>:<p className="owner-section-intro">No upcoming demonstration events. New bookings, appointments and viewings will appear here.</p>}
      </section>
      <section className="card owner-performance" aria-labelledby="owner-performance-title">
        <div className="owner-section-header"><div><span className="eyebrow">LIVE IN THIS DEMO</span><h2 id="owner-performance-title">Portfolio intelligence</h2></div><ChartNoAxesCombined size={23} aria-hidden="true"/></div>
        <div className="owner-occupancy-visual"><div className="owner-occupancy-ring" style={{'--owner-fill':`${occupancy}%`} as React.CSSProperties}><div><strong>{Math.round(occupancy)}%</strong><span>Occupancy</span></div></div><div><h3>Healthy visibility into your assets</h3><p>Occupancy is calculated from fictional unit records, not an invented revenue trend.</p><Link href="/workspace/reports">Explore all insights <ArrowUpRight size={14} aria-hidden="true"/></Link></div></div>
        <div className="owner-performance-stats">{stats.map(s=><div key={s.label}><span>{s.label}</span><strong>{s.value}</strong></div>)}</div>
        <div className="owner-performance-note">Figures refresh from the active mock portfolio and selected property scope.</div>
      </section>
    </div>
    <div className="owner-operational-grid owner-bottom-grid">
      <section className="card owner-calendar-card" aria-labelledby="owner-calendar-title">
        <div className="owner-section-header"><div><span className="eyebrow">YOUR TIME</span><h2 id="owner-calendar-title">Portfolio calendar</h2></div><Link href="/workspace/availability">Manage dates <ArrowUpRight size={16} aria-hidden="true"/></Link></div>
        <Calendar events={upcoming.map(r=>({id:r.id,title:`${r.data.title}${r.data.unitId?` · ${r.data.unitId}`:''}`,end:String(r.data.end??''),date:String(r.data.date??r.data.start??''),status:r.status}))} onSelect={id=>{const found=data.records.find(r=>r.id===id);if(found)router.push(recordPath(found.type,id))}}/>
      </section>
      <section className="card owner-activity" aria-labelledby="owner-activity-title">
        <div className="owner-section-header"><div><span className="eyebrow">LATEST UPDATES</span><h2 id="owner-activity-title">Recent activity</h2></div><Link href="/workspace/notifications">All updates <ArrowUpRight size={16} aria-hidden="true"/></Link></div>
        {activity.length?<ol>{activity.slice(0,9).map(a=><li key={a.id}><span className="owner-activity-dot" aria-hidden="true"/><div><Link href={recordPath(a.type,a.recordId)}>{a.message}</Link><small>{a.date} · Demo event</small></div></li>)}</ol>:<div className="owner-empty"><Clock3 size={25}/><strong>No recent changes</strong><p>Actions across your portfolio will appear here automatically.</p></div>}
        <Link className="owner-inline-link" href="/more">Explore every owner tool <ArrowRight size={15} aria-hidden="true"/></Link>
      </section>
    </div>
  </>;
}
