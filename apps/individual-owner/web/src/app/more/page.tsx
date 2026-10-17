'use client';
import Link from 'next/link';
import { useState } from 'react';
import modules from '../../../../shared/modules.json';
export default function More() {
  const [search,setSearch] = useState('');
  const filtered = modules.filter(m=>m.title.toLowerCase().includes(search.toLowerCase()));
  return <><div className="page-heading"><div><p className="eyebrow">WORKSPACE DIRECTORY</p><h1>All product areas</h1><p>Explore connected fictional workflows. Provider-dependent actions remain unavailable.</p></div></div><label className="directory-search">Find a product area<input type="search" value={search} onChange={e=>setSearch(e.target.value)} placeholder="Search listings, maintenance, security…"/></label><div className="module-grid">{filtered.map(m=><Link className="card module-card" href={m.slug==='dashboard'?'/':`/workspace/${m.slug}`} key={m.id}><span className="eyebrow">AREA {m.id}</span><h2>{m.title}</h2><span className="status">{m.gate==='C'?'PROVIDER GATED':'MOCK WORKSPACE'}</span><p>{m.pairIds.length ? `Reference ${m.pairIds.join(', ')}`:'Extended workspace screens'}</p></Link>)}</div>{!filtered.length?<p role="status">No product areas match.</p>:null}<Link className="panel-link" href="/demo-lab">Developer QA laboratory →</Link></>;
}
