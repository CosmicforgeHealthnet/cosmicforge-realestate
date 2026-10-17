import {
  Activity, BadgeCheck, Bell, BookOpenCheck, BriefcaseBusiness,
  Building2, CalendarClock, CalendarDays, CircleDollarSign, ClipboardCheck,
  FileCheck2, FileText, KeyRound, Landmark, ListChecks, LockKeyhole,
  MapPinned, MessageSquare, ReceiptText, ShieldCheck, UsersRound,
  Wallet, Wrench, type LucideIcon,
} from 'lucide-react';

const icon:Record<string,LucideIcon>={
  account:ShieldCheck,session:LockKeyhole,verification:BadgeCheck,bank:Wallet,
  tax:ReceiptText,ownership:ShieldCheck,owner:UsersRound,consent:FileCheck2,
  property:Building2,unit:KeyRound,parcel:MapPinned,media:Building2,
  listing:Building2,pricing:CircleDollarSign,availability:CalendarDays,
  inquiry:MessageSquare,lead:MessageSquare,viewing:CalendarClock,
  application:FileCheck2,offer:BriefcaseBusiness,agreement:BookOpenCheck,
  tenancy:BookOpenCheck,rent:ReceiptText,deposit:Wallet,renewal:CalendarClock,
  notice:FileText,moveout:ClipboardCheck,utility:Activity,meter:Activity,
  servicecharge:CircleDollarSign,loi:BriefcaseBusiness,sale:Landmark,
  booking:CalendarDays,turnover:ListChecks,damage:FileText,
  manager:UsersRound,maintenance:Wrench,quote:ReceiptText,
  professional:BriefcaseBusiness,appointment:CalendarClock,
  notification:Bell,report:Activity,dispute:ShieldCheck,privacy:LockKeyhole,
  preference:LockKeyhole,financing:Landmark,insurance:ShieldCheck,
  device:KeyRound,access:KeyRound,
};
const typesWithAmber=new Set(['sale','offer','quote','rent','deposit','pricing','financing','tax']);
const typesWithTeal=new Set(['property','unit','booking','viewing','availability','maintenance','professional']);
const typesWithViolet=new Set(['agreement','application','manager','consent','dispute','privacy','ownership']);
export function WorkspaceRecordIcon({type}:{type:string}){
 const Icon=icon[type]??FileText;
 const tone=typesWithAmber.has(type)?'amber':typesWithTeal.has(type)?'teal':typesWithViolet.has(type)?'violet':'sapphire';
 return <span className={`entity-record-icon entity-record-icon-${tone}`} aria-hidden="true"><Icon size={21} strokeWidth={1.8}/></span>;
}
