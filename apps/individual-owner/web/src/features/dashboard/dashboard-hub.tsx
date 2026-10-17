'use client';
import Link from 'next/link';
import {useState} from 'react';
import {
  ArrowRight, ArrowUpRight, BadgeCheck, Bell, BookOpenCheck, BriefcaseBusiness,
  Building2, CalendarCheck2, ChartColumnIncreasing, CircleDollarSign,
  ClipboardList, FileCheck2, HousePlus, KeyRound, Landmark, Layers3,
  LockKeyhole, MessagesSquare, ReceiptText, Settings2, ShieldCheck,
  Sparkles, UsersRound, Wallet, Wrench, type LucideIcon,
} from 'lucide-react';
import modules from '../../../../shared/modules.json';

type Shortcut={label:string;detail:string;href:string;icon:LucideIcon};
const essentials:Shortcut[]=[
  {label:'Properties',detail:'Manage your portfolio',href:'/workspace/properties',icon:Building2},
  {label:'Create listing',detail:'Prepare a new listing',href:'/workspace/listings/listing/new',icon:HousePlus},
  {label:'Applications',detail:'Review prospective tenants',href:'/workspace/applications',icon:FileCheck2},
  {label:'Tenancies',detail:'Agreements and move-in',href:'/workspace/tenancies',icon:KeyRound},
  {label:'Finance & wallet',detail:'Balances and statements',href:'/workspace/financial',icon:Wallet},
  {label:'Short-lets',detail:'Bookings and availability',href:'/workspace/shortlets',icon:CalendarCheck2},
  {label:'Maintenance',detail:'Repairs and appointments',href:'/workspace/maintenance',icon:Wrench},
  {label:'Reports',detail:'Portfolio intelligence',href:'/workspace/reports',icon:ChartColumnIncreasing},
];
const iconBySlug:Record<string,LucideIcon>={
  dashboard:ChartColumnIncreasing,identity:BadgeCheck,financial:Wallet,ownership:ShieldCheck,
  properties:Building2,verification:FileCheck2,listings:HousePlus,pricing:CircleDollarSign,
  availability:CalendarCheck2,inquiries:MessagesSquare,viewings:CalendarCheck2,
  applications:UsersRound,tenancies:BookOpenCheck,rent:ReceiptText,renewals:CalendarCheck2,
  utilities:Layers3,commercial:BriefcaseBusiness,land:Landmark,sales:CircleDollarSign,
  shortlets:KeyRound,managers:UsersRound,maintenance:Wrench,professionals:BriefcaseBusiness,
  notifications:Bell,reports:ChartColumnIncreasing,safety:ShieldCheck,settings:Settings2,
  financing:Landmark,insurance:ShieldCheck,access:LockKeyhole,
};
const groups=[
  {name:'Portfolio & ownership',min:1,max:6},
  {name:'Marketing & tenancy',min:7,max:16},
  {name:'Operations & transactions',min:17,max:23},
  {name:'Insights & account',min:24,max:30},
];
export function DashboardHub(){
  const [expanded,setExpanded]=useState(false);
  return <section className="owner-hub" aria-labelledby="owner-hub-title">
    <div className="owner-hub-heading">
      <div><span className="eyebrow">YOUR PROPERTY COMMAND CENTER</span><h2 id="owner-hub-title">Everything you need, one click away.</h2><p>Move straight from overview to action — across the entire owner platform.</p></div>
      <button type="button" aria-expanded={expanded} aria-controls="owner-all-modules" onClick={()=>setExpanded(v=>!v)}>
        {expanded?'Hide full directory':'Explore all 30 areas'} <ArrowRight size={16} aria-hidden="true"/>
      </button>
    </div>
    <div className="owner-essentials">
      {essentials.map(({label,detail,href,icon:Icon})=><Link key={href} className="owner-essential" href={href}>
        <span className="owner-essential-icon"><Icon size={21} strokeWidth={1.8} aria-hidden="true"/></span>
        <span className="owner-essential-copy"><strong>{label}</strong><small>{detail}</small></span>
        <ArrowUpRight className="owner-essential-arrow" size={17} aria-hidden="true"/>
      </Link>)}
    </div>
    <div className="owner-hub-foot">
      <span><Sparkles size={15} aria-hidden="true"/> Full platform access, with fictional demonstration records.</span>
      <Link href="/more">Browse platform directory <ArrowRight size={15} aria-hidden="true"/></Link>
    </div>
    {expanded?<div id="owner-all-modules" className="owner-module-directory">
      {groups.map(group=><div className="owner-module-group" key={group.name}>
        <h3>{group.name}</h3>
        <div className="owner-module-links">{modules.filter(m=>Number(m.id)>=group.min&&Number(m.id)<=group.max).map(m=>{
          const Icon=iconBySlug[m.slug]??ClipboardList;
          return <Link key={m.id} href={m.slug==='dashboard'?'/':`/workspace/${m.slug}`}>
            <Icon size={16} aria-hidden="true"/><span>{m.title}</span><ArrowUpRight size={13} aria-hidden="true"/>
          </Link>;
        })}</div>
      </div>)}
    </div>:null}
  </section>;
}
