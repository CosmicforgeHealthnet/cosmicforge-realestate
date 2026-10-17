'use client';
import Link from 'next/link';
import {
  ArrowRight, ArrowUpRight, Activity, BriefcaseBusiness, Building2,
  CalendarRange, CircleCheckBig, ClipboardList, FileText, Landmark,
  Layers, ShieldCheck, Sparkles, type LucideIcon,
} from 'lucide-react';

type Journey={eyebrow:string;intro:string;steps:[string,string,string];next:[string,string];icon:LucideIcon;tone:'sapphire'|'teal'|'amber'|'violet'};
const journeys:Record<string,Journey>={
  identity:{eyebrow:'ACCOUNT & TRUST',intro:'Set up your owner profile, identity evidence and access preferences before managing properties.',steps:['Prepare profile','Record demo verification','Manage access'],next:['verification','Property verification'],icon:ShieldCheck,tone:'teal'},
  financial:{eyebrow:'FINANCIAL CONTROLS',intro:'Manage payout preferences and tax-profile information. All balances and banking outcomes are simulated.',steps:['Review account details','Set payout preferences','Inspect example ledger'],next:['rent','Rent & deposits'],icon:Landmark,tone:'violet'},
  ownership:{eyebrow:'OWNERSHIP GOVERNANCE',intro:'Keep ownership claims, co-owner consent and delegated authority separate and traceable.',steps:['Record ownership','Request consent','Review authority'],next:['managers','Manager delegation'],icon:ShieldCheck,tone:'violet'},
  properties:{eyebrow:'PORTFOLIO STUDIO',intro:'Organize property records and units, track verification readiness, then prepare the right listings.',steps:['Create property','Add units or parcels','Prepare listing'],next:['listings','Create a listing'],icon:Building2,tone:'sapphire'},
  verification:{eyebrow:'PROPERTY EVIDENCE',intro:'Organize documents and follow the demo review process without implying certified title ownership.',steps:['Select property','Attach evidence metadata','Review case status'],next:['listings','Listing management'],icon:ShieldCheck,tone:'teal'},
  listings:{eyebrow:'PROPERTY MARKETING',intro:'Create polished listings, review details, then simulate publication only when eligible.',steps:['Create draft','Set price and availability','Review publication'],next:['pricing','Pricing & fees'],icon:Layers,tone:'sapphire'},
  pricing:{eyebrow:'COMMERCIAL TERMS',intro:'Set clear demonstration prices and fee breakdowns with transparent, non-statutory assumptions.',steps:['Choose listing','Configure price','Review cost breakdown'],next:['availability','Availability calendar'],icon:Landmark,tone:'amber'},
  availability:{eyebrow:'AVAILABILITY CONTROL',intro:'Coordinate unit dates, bookings and availability conflicts from one calendar-driven workspace.',steps:['Select unit','Configure dates','Resolve conflicts'],next:['shortlets','Short-let bookings'],icon:CalendarRange,tone:'teal'},
  inquiries:{eyebrow:'PROSPECT PIPELINE',intro:'Review interest in your listings, qualify leads and move promising prospects to a viewing.',steps:['Review new inquiry','Qualify prospect','Schedule viewing'],next:['viewings','Viewing calendar'],icon:Activity,tone:'sapphire'},
  viewings:{eyebrow:'PROPERTY VISITS',intro:'Manage viewing requests, approvals, reschedules and outcomes with clear property context.',steps:['Offer time slots','Confirm request','Record outcome'],next:['applications','Rental applications'],icon:CalendarRange,tone:'teal'},
  applications:{eyebrow:'TENANT SCREENING',intro:'Compare submitted applications and evidence access with accountable, human-reviewed decisions.',steps:['Review applicant','Inspect permitted evidence','Record decision'],next:['tenancies','Agreement & move-in'],icon:ClipboardList,tone:'violet'},
  tenancies:{eyebrow:'AGREEMENT LIFECYCLE',intro:'Draft agreements, coordinate review and preserve explicit, simulated signature and move-in states.',steps:['Draft agreement','Review signers','Manage move-in'],next:['rent','Rent obligations'],icon:FileText,tone:'sapphire'},
  rent:{eyebrow:'RENT & DEPOSITS',intro:'Understand outstanding rent, deposit cases and financial status without suggesting real settlement.',steps:['Review obligations','Track demo receipts','Resolve deposit case'],next:['renewals','Renewals & notices'],icon:Landmark,tone:'amber'},
  renewals:{eyebrow:'TENANCY CONTINUITY',intro:'Prepare renewals, notices and move-out activities with visible decisions and recorded milestones.',steps:['Review expiry','Draft renewal or notice','Manage move-out'],next:['tenancies','Tenancy records'],icon:CalendarRange,tone:'violet'},
  utilities:{eyebrow:'PROPERTY OUTGOINGS',intro:'Track utility accounts, meter readings and service-charge allocations by property and unit.',steps:['Review meters','Record usage','Reconcile charges'],next:['reports','Portfolio reports'],icon:Activity,tone:'teal'},
  commercial:{eyebrow:'COMMERCIAL PROPERTY',intro:'Handle business tenancy terms, offers and letters of intent with stage-by-stage review.',steps:['Define terms','Review offer','Track milestone'],next:['sales','Sale transactions'],icon:BriefcaseBusiness,tone:'violet'},
  land:{eyebrow:'LAND PORTFOLIO',intro:'Organize parcel information, inspections and land transaction milestones without guaranteeing title.',steps:['Record parcel','Review documents','Track offer'],next:['sales','Sale transactions'],icon:Landmark,tone:'amber'},
  sales:{eyebrow:'SALES & CLOSING',intro:'Monitor offers, diligence and closing milestones while keeping cash settlement and title transfer separate.',steps:['Review offer','Complete diligence','Track closing'],next:['ownership','Ownership controls'],icon:BriefcaseBusiness,tone:'sapphire'},
  shortlets:{eyebrow:'GUEST OPERATIONS',intro:'Coordinate booking requests, guest stays and turnover against an accurate unit availability calendar.',steps:['Configure inventory','Manage reservation','Complete turnover'],next:['availability','Availability calendar'],icon:CalendarRange,tone:'teal'},
  managers:{eyebrow:'DELEGATED OPERATIONS',intro:'Assign limited manager permissions, review activity and revoke access without transferring ownership.',steps:['Propose manager','Set permissions','Review access'],next:['ownership','Ownership authority'],icon:ShieldCheck,tone:'violet'},
  maintenance:{eyebrow:'PROPERTY CARE',intro:'Turn repair requests into reviewed quotes, scheduled work and recorded completion outcomes.',steps:['Report issue','Review quotes','Accept completed work'],next:['professionals','Professional services'],icon:ClipboardList,tone:'amber'},
  professionals:{eyebrow:'EXPERT SERVICES',intro:'Manage requests, fictional provider appointments, deliverables and disputed outcomes.',steps:['Define service','Arrange appointment','Review findings'],next:['maintenance','Maintenance requests'],icon:BriefcaseBusiness,tone:'teal'},
  notifications:{eyebrow:'OWNER INBOX',intro:'Review local alerts and follow deep links to the records requiring your attention.',steps:['Review alert','Open related record','Update preferences'],next:['settings','Account preferences'],icon:Activity,tone:'sapphire'},
  reports:{eyebrow:'PORTFOLIO INTELLIGENCE',intro:'Explore record-derived indicators, occupancy and fictional report exports without invented trends.',steps:['Set filters','Review insights','Export demo data'],next:['financial','Finance overview'],icon:Activity,tone:'violet'},
  safety:{eyebrow:'TRUST & PRIVACY',intro:'Handle disputes, permissions and privacy requests with visible case states and restricted evidence.',steps:['Create case','Review evidence','Track resolution'],next:['settings','Account settings'],icon:ShieldCheck,tone:'amber'},
  settings:{eyebrow:'PERSONAL WORKSPACE',intro:'Adjust accessibility, assisted use, privacy preferences and mock account-closure requests.',steps:['Review preferences','Set assistance','Confirm changes'],next:['identity','Profile & security'],icon:ShieldCheck,tone:'sapphire'},
  financing:{eyebrow:'PARTNER-DEPENDENT',intro:'Explore financing information and clearly unavailable external services without fictional approvals.',steps:['Review options','Understand limits','Check future availability'],next:['financial','Financial records'],icon:Landmark,tone:'amber'},
  insurance:{eyebrow:'PROTECTION RECORDS',intro:'Organize fictional policy metadata and reminders. No insurance product can be issued here yet.',steps:['View example policy','Review expiry','Check restrictions'],next:['properties','Property portfolio'],icon:ShieldCheck,tone:'teal'},
  access:{eyebrow:'CONNECTED PROPERTY',intro:'Review simulated devices and time-limited access grants. Physical hardware is never activated.',steps:['Select property','Review grant','Revoke demo access'],next:['viewings','Viewings & access'],icon:ShieldCheck,tone:'violet'},
};

export function WorkspaceExperience({slug,count,statusCount,propertyScoped}:{
  slug:string;count:number;statusCount:number;propertyScoped:boolean;
}){
  const journey=journeys[slug];
  if(!journey)return null;
  const Icon=journey.icon;
  return <section className={`workspace-experience workspace-experience-${journey.tone}`} aria-label="Product workflow guide">
    <div className="experience-intro">
      <div className="experience-intro-heading"><span className="experience-icon"><Icon size={25} strokeWidth={1.7} aria-hidden="true"/></span>
        <div><span className="eyebrow">{journey.eyebrow}</span><h2>From overview to outcome.</h2></div>
      </div>
      <p>{journey.intro}</p>
      <div className="experience-figures">
        <span><strong>{count}</strong> local {count===1?'record':'records'}</span>
        <span><strong>{statusCount}</strong> status {statusCount===1?'type':'types'}</span>
        {propertyScoped?<span><CircleCheckBig size={13} aria-hidden="true"/> Property scope applied</span>:null}
      </div>
    </div>
    <div className="experience-steps">
      <div className="experience-steps-title"><Sparkles size={16} aria-hidden="true"/><strong>Recommended journey</strong></div>
      <ol>{journey.steps.map((step,i)=><li key={step}><span>{String(i+1).padStart(2,'0')}</span>{step}</li>)}</ol>
      <Link href={`/workspace/${journey.next[0]}`}>Continue to {journey.next[1]} <ArrowUpRight size={15} aria-hidden="true"/></Link>
    </div>
  </section>;
}
