import { notFound } from 'next/navigation';
import { redirect } from 'next/navigation';
import Link from 'next/link';
import modules from '../../../../../shared/modules.json';
import { WorkspacePreferences } from '@/features/settings/workspace-preferences';
export function generateStaticParams() { return modules.map(m=>({module:m.slug})); }
export default async function ModulePage({params}:{params:Promise<{module:string}>}) {
  const {module:slug} = await params; const area = modules.find(m=>m.slug===slug);
  if (!area) notFound();
  if (slug === 'dashboard') redirect('/');
  return <><div className="page-heading"><div><p className="eyebrow">PRODUCT AREA {area.id} · FOUNDATION ROUTE</p><h1>{area.title}</h1><p>Prepared for the next functional implementation phase.</p></div><span className="badge">NOT_STARTED</span></div><section className="card state"><h2>{area.gate==='C'?'External capability unavailable':'Workflow implementation pending'}</h2><p>This route establishes navigation only. The complete {area.title.toLowerCase()} workflow has not been implemented.</p><p>{area.pairIds.length ? `Visual reference: ${area.pairIds.join(', ')}.` : 'No dedicated Stitch pair. Supporting screens will be designed in the next phase.'}</p><p>Mock-only foundation. No live payments, verification, signatures, messaging or device commands.</p><Link href="/more">Browse product areas</Link></section>{slug==='settings'?<WorkspacePreferences/>:null}{slug==='properties'?<Link className="panel-link" href="/properties/demo-property-01">View sample property overview →</Link>:null}</>;
}
