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
type CalendarEvent={id:string;title:string;date:string;end?:string;status:string};
const calendarWeekdays=['Mon','Tue','Wed','Thu','Fri','Sat','Sun'] as const;
export function calendarMonthLayout(month:string){
  const match=/^(\d{4})-(0[1-9]|1[0-2])$/.exec(month);
  const year=match?Number(match[1]):2026, index=match?Number(match[2])-1:9;
  const monthStart=new Date(Date.UTC(year,index,1));
  const monthEnd=new Date(Date.UTC(year,index+1,1));
  const offset=(monthStart.getUTCDay()+6)%7;
  return {year,index,offset,days:new Date(Date.UTC(year,index+1,0)).getUTCDate(),
    first:monthStart.toISOString().slice(0,10),next:monthEnd.toISOString().slice(0,10)};
}
export function calendarEventOnDay(event:CalendarEvent,date:string){
  // End dates are exclusive for stays and multi-day blocks; single-day
  // appointments have no end, and still appear on their start date.
  return event.date===date||Boolean(event.end&&event.date&&event.date<=date&&date<event.end);
}
export function Calendar({events,onSelect}:{events:CalendarEvent[];onSelect:(id:string)=>void}){
  const [month,setMonth]=useState('2026-10');
  const [selectedDay,setSelectedDay]=useState<string|null>(null);
  const [showAll,setShowAll]=useState(false);
  const layout=calendarMonthLayout(month);
  const label=new Intl.DateTimeFormat('en',{month:'long',year:'numeric',timeZone:'UTC'})
    .format(new Date(Date.UTC(layout.year,layout.index,1)));
  const validEvents=events.filter(e=>/^\d{4}-\d{2}-\d{2}$/.test(e.date??''));
  const monthEvents=validEvents.filter(e=>e.date<layout.next&&(e.end?e.end>layout.first:e.date>=layout.first))
    .sort((a,b)=>a.date.localeCompare(b.date)||a.title.localeCompare(b.title));
  const agenda=selectedDay?monthEvents.filter(e=>calendarEventOnDay(e,selectedDay)):monthEvents;
  const shownAgenda=showAll?agenda:agenda.slice(0,5);
  function navigateMonth(offset:number){
    const next=new Date(Date.UTC(layout.year,layout.index+offset,1)).toISOString().slice(0,7);
    setMonth(next);setSelectedDay(null);setShowAll(false);
  }
  return <section className="calendar-panel owner-calendar" aria-label="Owner calendar">
    <div className="calendar-toolbar">
      <div><span className="eyebrow">OWNER SCHEDULE</span><h3>{label}</h3>
        <small>{monthEvents.length} demonstration {monthEvents.length===1?'event':'events'} this month</small>
      </div>
      <div className="calendar-month-controls" role="group" aria-label="Calendar navigation">
        <button type="button" aria-label="Previous month" onClick={()=>navigateMonth(-1)}>‹</button>
        <label htmlFor="owner-calendar-month">Month<input id="owner-calendar-month" type="month" value={month} min="1900-01" max="2100-12"
          onChange={e=>{if(/^\d{4}-(0[1-9]|1[0-2])$/.test(e.target.value)){setMonth(e.target.value);setSelectedDay(null);setShowAll(false)}}}/></label>
        <button type="button" aria-label="Next month" onClick={()=>navigateMonth(1)}>›</button>
      </div>
    </div>
    <div className="calendar-weekdays" aria-hidden="true">
      {calendarWeekdays.map(day=><span key={day}>{day}</span>)}
    </div>
    <div className="calendar-grid" role="group" aria-label={`${label} month view`}>
      {Array.from({length:layout.offset},(_,i)=><div className="calendar-blank" key={`before-${i}`} aria-hidden="true"/>)}
      {Array.from({length:layout.days},(_,i)=>{
        const day=i+1,date=`${month}-${String(day).padStart(2,'0')}`;
        const dayEvents=monthEvents.filter(e=>calendarEventOnDay(e,date));
        return <div className={`calendar-day${selectedDay===date?' is-selected':''}`} key={date}>
          <button type="button" className="calendar-date" aria-pressed={selectedDay===date} aria-label={`Show events on ${date}`}
            onClick={()=>{setSelectedDay(current=>current===date?null:date);setShowAll(false)}}>
            <time dateTime={date}>{day}</time>
            {dayEvents.length>0?<span className="calendar-day-count" aria-hidden="true">{dayEvents.length}</span>:null}
          </button>
          {dayEvents.length?<div className="calendar-cell-events">
            {dayEvents.slice(0,2).map(e=><button type="button" key={e.id} className="calendar-event" onClick={()=>onSelect(e.id)}
              title={`${e.title} · ${e.status}`} aria-label={`${e.title}, ${date}, ${e.status}`}>
              <span className="calendar-event-dot" aria-hidden="true"/><span className="calendar-event-text">{e.title}</span>
            </button>)}
            {dayEvents.length>2?<button type="button" className="calendar-overflow" onClick={()=>{setSelectedDay(date);setShowAll(true)}}>
              +{dayEvents.length-2} more
            </button>:null}
          </div>:null}
        </div>;
      })}
    </div>
    <div className="calendar-agenda" aria-label="Calendar event agenda">
      <div className="calendar-agenda-heading">
        <div><span className="eyebrow">SCHEDULE DETAILS</span><h4>{selectedDay?`Events · ${selectedDay}`:'This month’s activity'}</h4></div>
        {selectedDay?<button type="button" onClick={()=>{setSelectedDay(null);setShowAll(false)}}>Show whole month</button>:null}
      </div>
      {agenda.length?<div className="calendar-agenda-list">{shownAgenda.map(e=><button key={e.id} type="button" onClick={()=>onSelect(e.id)}>
        <span className="calendar-agenda-icon" aria-hidden="true">●</span>
        <span className="calendar-agenda-description"><strong>{e.title}</strong><small>{e.date}{e.end?` → ${e.end}`:''} · {e.status}</small></span>
        <span className="calendar-agenda-chevron" aria-hidden="true">↗</span>
      </button>)}</div>:<p className="calendar-empty">No demonstration events {selectedDay?'on this day':'in this month'}.</p>}
      {!showAll&&agenda.length>shownAgenda.length?<button type="button" className="calendar-show-all" onClick={()=>setShowAll(true)}>
        View all {agenda.length} events
      </button>:null}
    </div>
    <p className="calendar-caption">Fictional schedule · UTC · Select a day or event to explore.</p>
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
