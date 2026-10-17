'use client';
import Link from 'next/link';
import {useState,useEffect,useCallback} from 'react';
import {useRouter} from 'next/navigation';
import {useForm,type Resolver} from 'react-hook-form';
import {zodResolver} from '@hookform/resolvers/zod';
import {z} from 'zod';
import type {ModelSpec,DomainRecord,Values,WorkspaceState} from '@/lib/contracts/workspace';
import {useWorkspace,recordPath} from './use-workspace';
import {useUnsavedGuard,Feedback,Dialog} from '@/components/workspace-ui';
import {usePropertyScope} from '@/components/workspace-tools';
function formSchema(spec:ModelSpec){return z.object(Object.fromEntries(spec.fields.map(f=>{
 let value:z.ZodType=f.kind==='boolean'?z.boolean():f.kind==='number'?z.number().int().min(f.min??0).max(f.max??9000000000000):z.string().trim().max(f.kind==='number'?9000000000000:f.max??1000);
 if(f.required&&f.kind!=='boolean'&&f.kind!=='number')value=z.string().trim().min(1,`${f.label} is required.`).max(f.max??1000);
 return [f.key,value];
})));}
export function RecordForm({spec,record,state,propertyId,onDone}:{spec:ModelSpec;record?:DomainRecord;state:WorkspaceState;propertyId:string;onDone?:()=>void}){
 const router=useRouter(),{command}=useWorkspace(),[version,setVersion]=useState(record?.version),{drafts}=usePropertyScope(),draftKey=`${spec.id}:${record?.id??'new'}`;
 const [savedDraft]=useState(()=>drafts.get(draftKey)),[restored,setRestored]=useState(()=>Boolean(drafts.get(draftKey)));
 const [step,setStep]=useState(savedDraft?.step??0),[challenge,setChallenge]=useState(''),[preview,setPreview]=useState('');
 const initial=Object.fromEntries(spec.fields.map(f=>[f.key,record?.data[f.key]??(f.key==='propertyId'&&propertyId!=='all'?propertyId:f.kind==='boolean'?false:f.kind==='number'?f.min??0:f.kind==='select'?f.options?.[0]??'':'')])) as Values;
 const {register,handleSubmit,setValue,trigger,reset,subscribe,formState:{errors,isDirty}}=useForm<Values>({defaultValues:savedDraft?.data??initial,resolver:zodResolver(formSchema(spec)) as unknown as Resolver<Values>});
 const discard=useCallback(()=>{drafts.delete(draftKey);setRestored(false)},[drafts,draftKey]);
 useUnsavedGuard(isDirty||restored,discard);
 useEffect(()=>subscribe({formState:{values:true},callback:({values})=>drafts.set(draftKey,{data:values,step})}),[subscribe,drafts,draftKey,step]);
 const groups=spec.wizard?3:1,chunk=Math.ceil(spec.fields.length/groups),fields=spec.fields.slice(step*chunk,(step+1)*chunk);
 async function submit(data:Values){try{const saved=await command.mutateAsync({op:record?'update':'create',type:spec.id,id:record?.id,version:version,data,challenge});reset(saved.data);discard();onDone?.();router.push(recordPath(spec.id,saved.id))}catch{}}
 return <section className="card record-form"><div className="section-heading"><div><p className="eyebrow">{record?'EDIT VERSION '+version:'NEW FICTIONAL RECORD'}</p><h2>{spec.label}</h2></div><span className="badge">{step+1} / {groups}</span></div><p>Use fictional information only. Changes stay in this local demo. Evidence selection stores filename metadata, never file contents.</p>
 {spec.id==='preference'?<p>Language preference is recorded for review; the demonstration interface remains English. Large text and reduced motion apply in this session.</p>:null}
 {spec.wizard?<ol className="wizard-progress">{['Property & purpose','Details & terms','Review & save'].map((label,i)=><li key={label} aria-current={step===i?'step':undefined}>{i+1}. {label}</li>)}</ol>:null}
 <form onSubmit={handleSubmit(submit)} noValidate><div className="form-grid">{fields.map(f=><div key={f.key} className={f.kind==='textarea'?'field full-width':'field'}>
 {f.kind==='boolean'?<label className="checkbox-field"><input type="checkbox" {...register(f.key)}/>{f.label}</label>:<><label htmlFor={`field-${f.key}`}>{f.label}{f.required?' *':''}</label>
 {f.kind==='select'||f.kind==='relation'?<select id={`field-${f.key}`} {...register(f.key)} aria-invalid={Boolean(errors[f.key])}><option value="">Choose {f.label.toLowerCase()}</option>{f.kind==='select'?f.options?.map(v=><option key={v} value={v}>{v}</option>):state.records.filter(r=>r.type===f.target&&!['ARCHIVED','REVOKED'].includes(r.status)).map(r=><option key={r.id} value={r.id}>{String(r.data.title)} · {r.id}</option>)}</select>:f.kind==='textarea'?<textarea id={`field-${f.key}`} {...register(f.key)} rows={4}/>:<input id={`field-${f.key}`} type={f.kind==='number'?'number':f.kind==='date'?'date':f.kind==='time'?'time':'text'} {...register(f.key,f.kind==='number'?{valueAsNumber:true}:{})} min={f.min} max={f.max} aria-invalid={Boolean(errors[f.key])}/>}
 </>}
 {f.kind==='evidence'?<div className="evidence-tools"><label className="file-picker">Select fictional file<input type="file" accept=".txt,.pdf,.png,.jpg,.jpeg" onChange={event=>{const file=event.target.files?.[0];if(file&&file.size<=5*1024*1024)setValue(f.key,file.name,{shouldDirty:true,shouldValidate:true});else setPreview('Choose a file smaller than 5 MB. No file was recorded.')}}/></label><button type="button" onClick={()=>setValue(f.key,'sample-evidence.txt',{shouldDirty:true})}>Choose sample evidence</button><button type="button" onClick={()=>setPreview('Preview: sample-evidence.txt · Fictional evidence metadata only. No document upload or ownership verification has occurred.')}>Preview sample document</button></div>:null}
 {errors[f.key]?<p className="danger" role="alert">{String(errors[f.key]?.message)}</p>:null}
 {f.kind==='number'&&f.key.toLowerCase().includes('minor')?<small>100 minor units = NGN 1. Example amount, not a statutory charge.</small>:null}
 </div>)}</div>
 {spec.sensitive?<label className="security-confirmation">Simulated security confirmation<input aria-label="Security confirmation" value={challenge} onChange={e=>setChallenge(e.target.value)} placeholder="Type DEMO" autoComplete="off"/><small>Never enter a real password, identity number or verification code.</small></label>:null}
 <Feedback error message={command.error?.message??''}/><div className="form-actions">{step>0?<button type="button" onClick={()=>setStep(s=>s-1)}>Previous</button>:null}{step<groups-1?<button className="primary" type="button" onClick={async event=>{event.preventDefault();if(await trigger(fields.map(f=>f.key)))setStep(s=>s+1)}}>Next step</button>:<button className="primary" disabled={command.isPending}>{command.isPending?'Saving local record…':'Save demo record'}</button>}<Link href={`/workspace/${spec.module}?tab=${spec.id}`}>Cancel</Link>{record?<button type="button" onClick={()=>{reset(initial);setVersion(record.version);command.reset()}}>Reload latest version</button>:null}</div></form>
 {preview?<Dialog title="Document metadata preview" onClose={()=>setPreview('')}><p>{preview}</p></Dialog>:null}</section>;
}
