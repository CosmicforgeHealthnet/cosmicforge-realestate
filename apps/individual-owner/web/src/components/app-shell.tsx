'use client';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { Building2, LayoutDashboard, PanelsTopLeft, Wallet, MoreHorizontal, Settings, Moon, Sun, Wrench, ChartNoAxesCombined, Users, ShieldCheck, CalendarDays, House, Bell } from 'lucide-react';
import { useTheme } from '@/design/theme-provider';
import { PropertySelector,GlobalSearch,ProfileMenu } from './workspace-tools';
const navigation = [
  {href:'/',label:'Dashboard',icon:LayoutDashboard}, {href:'/workspace/properties',label:'Properties',icon:Building2},
  {href:'/workspace/listings',label:'Listings',icon:PanelsTopLeft}, {href:'/workspace/inquiries',label:'Leads & viewings',icon:CalendarDays},
  {href:'/workspace/tenancies',label:'Tenancies & rent',icon:Users}, {href:'/workspace/shortlets',label:'Short-lets',icon:House},
  {href:'/workspace/maintenance',label:'Maintenance',icon:Wrench}, {href:'/workspace/financial',label:'Finance & wallet',icon:Wallet},
  {href:'/workspace/reports',label:'Reports',icon:ChartNoAxesCombined}, {href:'/workspace/ownership',label:'Ownership & access',icon:ShieldCheck},
  {href:'/more',label:'All product areas',icon:MoreHorizontal}, {href:'/workspace/settings',label:'Settings',icon:Settings},
];
const mobile = [navigation[0],navigation[1],navigation[2],navigation[7],{href:'/more',label:'More',icon:MoreHorizontal}];
export function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname(); const {dark,toggle} = useTheme();
  function nav(items: typeof navigation) { return items.map(({href,label,icon:Icon})=><Link key={href} href={href} aria-current={pathname===href ? 'page':undefined}><Icon size={19} aria-hidden="true"/><span>{label}</span></Link>); }
  return <><a className="skip" href="#main">Skip to content</a><aside className="sidebar"><Link className="brand" href="/"><span className="brand-mark"><Building2 size={23}/></span><span>CosmicForge<small>REAL ESTATE</small></span></Link><div className="workspace-label"><span className="eyebrow">YOUR WORKSPACE</span><strong>Individual Owner</strong><PropertySelector/></div><nav aria-label="Main navigation"><span className="nav-group">Portfolio & operations</span>{nav(navigation.slice(0,7))}<span className="nav-group">Finance & owner controls</span>{nav(navigation.slice(7))}</nav><div className="sidebar-foot"><span className="status-dot"/> Local mock repository<small>No backend connected</small></div></aside><div className="workspace"><header className="topbar"><span className="breadcrumb">Owner workspace <span>/</span> <strong>{navigation.find(n=>n.href===pathname)?.label ?? 'Portfolio'}</strong></span><div className="header-actions"><GlobalSearch/><span className="demo-pill">MOCK MODE</span><button className="icon-button" onClick={toggle} aria-label={dark?'Switch to light theme':'Switch to dark theme'}>{dark?<Sun size={20}/>:<Moon size={20}/>}</button><Link className="icon-button notifications-link" aria-label="Notifications" href="/workspace/notifications"><Bell size={20}/></Link><ProfileMenu/></div></header><main id="main" tabIndex={-1}>{children}</main><footer className="app-footer">Fictional records · Local changes reset on reload · Interactive mock</footer></div><nav className="mobile-nav" aria-label="Mobile navigation">{nav(mobile)}</nav></>;
}
