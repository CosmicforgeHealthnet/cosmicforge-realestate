'use client';
import {createContext,useContext,useEffect,useLayoutEffect,useRef,useState} from 'react';
export function StatusBadge({value}:{value:string}) { return <span className={`status state-${value.toLowerCase()}`}>{value.replaceAll('_',' ')}</span>; }
export function Dialog({title,children,onClose}:{title:string;children:React.ReactNode;onClose:()=>void}) {
  const dialog=useRef<HTMLDialogElement>(null);
  useEffect(()=>{const element=dialog.current;element?.showModal();element?.querySelector<HTMLInputElement>('input,select,textarea')?.focus();return()=>element?.close()},[]);
  return <dialog ref={dialog} aria-label={title} onCancel={onClose} className="owner-dialog"><header><h2>{title}</h2><button onClick={onClose} aria-label="Close dialog">×</button></header>{children}</dialog>;
}
export function SectionTitle({title,description,action}:{title:string;description?:string;action?:React.ReactNode}) {return <div className="section-heading"><div><h2>{title}</h2>{description?<p>{description}</p>:null}</div>{action}</div>}
export function Feedback({message,error=false}:{message:string;error?:boolean}) {return message?<p className={`feedback ${error?'danger':'success'}`} role={error?'alert':'status'}>{message}</p>:null;}
export function Calendar({events,onSelect}:{events:{id:string;title:string;date:string;end?:string;status:string}[];onSelect:(id:string)=>void}){
  const [month,setMonth]=useState('2026-10');
  const [year,number]=month.split('-').map(Number);
  const safeYear=Number.isInteger(year)&&year>=1900&&year<=2100?year:2026;
  const safeMonth=Number.isInteger(number)&&number>=1&&number<=12?number:10;
  const days=new Date(Date.UTC(safeYear,safeMonth,0)).getUTCDate();
  const offset=(new Date(Date.UTC(safeYear,safeMonth-1,1)).getUTCDay()+6)%7;
  const monthLabel=new Intl.DateTimeFormat('en',{month:'long',year:'numeric',timeZone:'UTC'}).format(new Date(Date.UTC(safeYear,safeMonth-1,1)));
  const visibleEvents=events.filter(e=>e.date?.startsWith(month)||(e.end&&e.date&&e.date<=month+'-31'&&e.end>=month+'-01'));
  function shift(delta:number){setMonth(new Date(Date.UTC(safeYear,safeMonth-1+delta,1)).toISOString().slice(0,7))}
  return <section className="calendar-panel" aria-label="Owner calendar">
    <div className="calendar-toolbar">
      <div><span className="eyebrow">OWNER SCHEDULE</span><h3>{monthLabel}</h3><small>{visibleEvents.length} demonstration {visibleEvents.length===1?'event':'events'} in view</small></div>
      <div className="calendar-month-controls">
        <button type="button" aria-label="Previous month" onClick={()=>shift(-1)}>‹</button>
        <label>Calendar month<input type="month" value={month} min="1900-01" max="2100-12" onChange={e=>setMonth(e.target.value||'2026-10')}/></label>
        <button type="button" aria-label="Next month" onClick={()=>shift(1)}>›</button>
      </div>
    </div>
    <div className="calendar-scroll" tabIndex={0} aria-label="Scroll calendar horizontally on narrow screens">
      <div className="calendar-grid">
        {['Mon','Tue','Wed','Thu','Fri','Sat','Sun'].map(day=><div className="calendar-weekday" key={day}>{day}</div>)}
        {Array.from({length:offset},(_,i)=><div key={`before-${i}`} className="calendar-blank" aria-hidden="true"/>)}
        {Array.from({length:days},(_,i)=>{
          const date=`${month}-${String(i+1).padStart(2,'0')}`;
          const items=events.filter(e=>e.date===date||Boolean(e.date&&e.end&&e.date<=date&&date<e.end));
          return <div className="calendar-day" key={date}>
            <time dateTime={date}>{i+1}</time>
            {items.slice(0,3).map(e=><button type="button" key={e.id} aria-label={`${e.title}, ${date}, ${e.status}`} onClick={()=>onSelect(e.id)}>{e.title}<small>{e.status}</small></button>)}
            {items.length>3?<small className="calendar-overflow">+{items.length-3} more</small>:null}
          </div>;
        })}
      </div>
    </div>
    <p className="calendar-caption">Fictional schedule · UTC · Choose an event to open its record.</p>
  </section>;
}
type DraftGuard={dirty:boolean;onDiscard?:()=>void};
const DraftNavigationContext=createContext<{current:DraftGuard|null}|null>(null);
export function DraftNavigationProvider({children}:{children:React.ReactNode}){
 const activeRef=useRef<DraftGuard|null>(null);
 useLayoutEffect(()=>{
  // Register once at startup, before Next's passive popstate handler. A late
  // form listener can be removed when the router unmounts the form first.
  let restoring=false;
  const pop=(event:PopStateEvent)=>{
   if(restoring){restoring=false;return}
   const guard=activeRef.current;
   if(!guard?.dirty)return;
   if(window.confirm('Discard unsaved demo changes?'))guard.onDiscard?.();
   else{event.stopImmediatePropagation();restoring=true;history.forward()}
  };
  window.addEventListener('popstate',pop);
  return()=>window.removeEventListener('popstate',pop);
 },[]);
 return <DraftNavigationContext value={activeRef}>{children}</DraftNavigationContext>;
}
export function useUnsavedGuard(dirty:boolean,onDiscard?:()=>void){
 const activeRef=useContext(DraftNavigationContext);
 useEffect(()=>{
  const guard={dirty,onDiscard};if(activeRef)activeRef.current=guard;
  const unload=(event:BeforeUnloadEvent)=>{if(dirty){event.preventDefault();event.returnValue=''}};
  const click=(event:MouseEvent)=>{const link=(event.target as Element).closest('a[href]');if(dirty&&link){if(!window.confirm('Discard unsaved demo changes?')){event.preventDefault();event.stopPropagation()}else onDiscard?.()}};
  window.addEventListener('beforeunload',unload);document.addEventListener('click',click,true);
  return()=>{if(activeRef?.current===guard)activeRef.current=null;window.removeEventListener('beforeunload',unload);document.removeEventListener('click',click,true)};
 },[dirty,onDiscard,activeRef]);
}
