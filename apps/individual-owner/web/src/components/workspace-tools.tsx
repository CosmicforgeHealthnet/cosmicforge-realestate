'use client';
import Link from 'next/link';
import { createContext,useContext,useState } from 'react';
import modules from '../../../shared/modules.json';
import { useDashboard } from '@/features/dashboard/use-dashboard';
import { Dialog } from './workspace-ui';
const ScopeContext=createContext<{propertyId:string;setPropertyId:(id:string)=>void}>({propertyId:'all',setPropertyId:()=>{}});
export function ScopeProvider({children}:{children:React.ReactNode}) {const [propertyId,setPropertyId]=useState('all');return <ScopeContext value={{propertyId,setPropertyId}}>{children}</ScopeContext>}
export const usePropertyScope=()=>useContext(ScopeContext);
export function PropertySelector(){const {data}=useDashboard();const {propertyId,setPropertyId}=usePropertyScope();return <label className="property-selector">Portfolio scope<select value={propertyId} onChange={e=>setPropertyId(e.target.value)}><option value="all">All properties</option>{data?.properties.map(p=><option value={p.id} key={p.id}>{p.name}</option>)}</select></label>}
export function GlobalSearch(){const [open,setOpen]=useState(false);const [search,setSearch]=useState('');const {data}=useDashboard();return <><button className="search-trigger" onClick={()=>setOpen(true)}>Search workspace…</button>{open?<Dialog title="Search workspace" onClose={()=>setOpen(false)}><label>Search properties and product areas<input autoFocus type="search" value={search} onChange={e=>setSearch(e.target.value)}/></label><div className="search-results">{data?.properties.filter(p=>p.name.toLowerCase().includes(search.toLowerCase())).map(p=><Link onClick={()=>setOpen(false)} key={p.id} href={`/properties/${p.id}`}>{p.name}<small>Property overview</small></Link>)}{modules.filter(m=>m.title.toLowerCase().includes(search.toLowerCase())).map(m=><Link onClick={()=>setOpen(false)} key={m.id} href={m.id==='01'?'/':`/workspace/${m.slug}`}>{m.title}<small>Product area {m.id}</small></Link>)}</div></Dialog>:null}</>}
export function ProfileMenu(){return <details className="profile-menu"><summary className="avatar" aria-label="Demo account menu">AD</summary><nav aria-label="Account menu"><Link href="/workspace/identity">Profile & security</Link><Link href="/workspace/settings">Appearance & accessibility</Link><Link href="/workspace/safety">Privacy & disputes</Link></nav></details>}
