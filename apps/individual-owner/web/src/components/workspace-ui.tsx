'use client';
import { useEffect,useRef,useState } from 'react';
export function StatusBadge({value}:{value:string}) { return <span className={`status state-${value.toLowerCase()}`}>{value.replaceAll('_',' ')}</span>; }
export function Dialog({title,children,onClose}:{title:string;children:React.ReactNode;onClose:()=>void}) {
  const dialog=useRef<HTMLDialogElement>(null);
  useEffect(()=>{const element=dialog.current;element?.showModal();element?.querySelector<HTMLInputElement>('input,select,textarea')?.focus();return()=>element?.close()},[]);
  return <dialog ref={dialog} aria-label={title} onCancel={onClose} className="owner-dialog"><header><h2>{title}</h2><button onClick={onClose} aria-label="Close dialog">×</button></header>{children}</dialog>;
}
export function SectionTitle({title,description,action}:{title:string;description?:string;action?:React.ReactNode}) {return <div className="section-heading"><div><h2>{title}</h2>{description?<p>{description}</p>:null}</div>{action}</div>}
export function Feedback({message,error=false}:{message:string;error?:boolean}) {return message?<p className={`feedback ${error?'danger':'success'}`} role={error?'alert':'status'}>{message}</p>:null;}
export function Calendar({events,onSelect}:{events:{id:string;title:string;date:string;status:string}[];onSelect:(id:string)=>void}) {
  const [month,setMonth]=useState('2026-10');const [year,number]=month.split('-').map(Number),days=new Date(year,number,0).getDate();
  return <section className="calendar-panel" aria-label="Owner calendar"><label>Calendar month<input type="month" value={month} onChange={e=>setMonth(e.target.value||'2026-10')}/></label><div className="calendar-grid">{Array.from({length:days},(_,i)=>{const date=`${month}-${String(i+1).padStart(2,'0')}`;const items=events.filter(e=>e.date===date);return <div className="calendar-day" key={date}><time dateTime={date}>{i+1}</time>{items.map(e=><button key={e.id} onClick={()=>onSelect(e.id)}>{e.title}<small>{e.status}</small></button>)}</div>})}</div><p>Demonstration calendar · UTC</p></section>;
}
export function useUnsavedGuard(dirty:boolean,onDiscard?:()=>void) {
  useEffect(()=>{
    const unload=(event:BeforeUnloadEvent)=>{if(dirty){event.preventDefault();event.returnValue=''}};
    const click=(event:MouseEvent)=>{const link=(event.target as Element).closest('a[href]');if(dirty&&link){if(!window.confirm('Discard unsaved demo changes?')){event.preventDefault();event.stopPropagation()}else onDiscard?.()}};
    const pop=()=>{if(dirty){if(!window.confirm('Discard unsaved demo changes?'))history.forward();else onDiscard?.()}};
    window.addEventListener('beforeunload',unload);document.addEventListener('click',click,true);window.addEventListener('popstate',pop);
    return()=>{window.removeEventListener('beforeunload',unload);document.removeEventListener('click',click,true);window.removeEventListener('popstate',pop)};
  },[dirty,onDiscard]);
}
