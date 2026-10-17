import type {WorkspaceSchema,WorkspaceState,DomainRecord,WorkspaceCommand,Values,ModelSpec} from '@/lib/contracts/workspace';
export class MockFault extends Error {constructor(public code:string,message:string){super(message);this.name='MockFault'}}
const fail=(code:string,message:string):never=>{throw new MockFault(code,message)};
const activeBooking=(r:DomainRecord)=>['REQUESTED','APPROVED','CHECKED_IN'].includes(r.status);
const overlap=(a:Values,b:Values)=>String(a.start)<String(b.end)&&String(a.end)>String(b.start);
export class WorkspaceEngine {
  private state:WorkspaceState;
  constructor(public readonly schema:WorkspaceSchema,private readonly seed:WorkspaceState){this.state=structuredClone(seed)}
  snapshot(){return structuredClone(this.state)}
  renameWorkspace(name:string){this.state.workspaceName=name}
  setTaskDone(id:string,done:boolean){const task=this.state.tasks.find(t=>t.id===id)??fail('NOT_FOUND','Sample task not found.');task.done=done}
  spec(type:string):ModelSpec{return this.schema.types.find(t=>t.id===type)??fail('NOT_FOUND','Record family not found.')}
  reset(){this.state=structuredClone(this.seed)}
  scenario(value:string){
    if(!this.schema.scenarios.includes(value))fail('INVALID','Unknown demo scenario.');
    this.reset();this.state.scenario=value;
    if(value==='empty'){this.state.records=this.state.records.filter(r=>['account','preference'].includes(r.type));this.state.tasks=[]}
    if(['pending_verification','correction_required'].includes(value))for(const r of this.state.records.filter(r=>['identity','verification'].includes(r.type)))r.status=value==='pending_verification'?'PENDING':'CORRECTION';
  }
  actor(value:string){if(!['owner','manager'].includes(value))fail('INVALID','Unknown demo actor.');this.state.actor=value}
  record(id:string,type?:string){return this.state.records.find(r=>r.id===id&&(!type||r.type===type))??fail('NOT_FOUND','The selected demo record no longer exists.')}
  propertyOf(r:DomainRecord,depth=0):string{
    if(r.type==='property')return r.id;if(r.data.propertyId)return String(r.data.propertyId);if(depth>8)return '';
    for(const key of ['listingId','unitId','tenancyId','offerId','agreementId','applicationId','requestId','bookingId','utilityId','budgetId','saleId','deviceId','claimId']){const related=this.state.records.find(x=>x.id===r.data[key]);if(related){const property=this.propertyOf(related,depth+1);if(property)return property}}
    return '';
  }
  canManage(type:string,propertyId:string){
    if(this.state.actor==='owner')return true;
    const capability=['maintenance','quote','preventive'].includes(type)?'maintenance':type==='viewing'?'viewings':type==='listing'?'listings':'';
    return Boolean(capability&&this.state.records.some(r=>r.type==='assignment'&&r.status==='ACTIVE'&&r.data.propertyId===propertyId&&r.data[capability]===true&&String(r.data.expires)>=this.state.date));
  }
  private permission(type:string,propertyId:string){
    const s=this.state.scenario;
    if(s==='permission_denied')fail('DENIED','This demo actor has no permission to view or change owner records.');
    if(s==='expired_session'&&type!=='account')fail('EXPIRED_SESSION','Demo session expired. Return to the QA lab or simulate login.');
    if(s==='restricted')fail('RESTRICTED','This account is restricted in the selected scenario. Changes are unavailable.');
    if(s==='offline')fail('OFFLINE','Offline demonstration: cached records are read-only. No commands are queued.');
    if(s==='provider_unavailable')fail('UNAVAILABLE','Simulated service unavailable. No change was recorded.');
    if(s==='failed_operation')fail('FAILED','The simulated operation failed. No state was changed.');
    if(s==='insufficient_delegation'||!this.canManage(type,propertyId))fail('DENIED','Delegated permission is missing, expired, suspended or revoked. Owner title and payout authority cannot be delegated.');
  }
  private validate(spec:ModelSpec,data:Values,id?:string){
    if(this.state.scenario==='invalid_input')fail('INVALID','QA scenario rejected the submitted values. Correct the form or reset the scenario.');
    for(const f of spec.fields){const v=data[f.key];
      if(f.required&&(v===undefined||v===''||v===null))fail('INVALID',`${f.label} is required.`);
      if(v===undefined||v==='')continue;
      if(f.kind==='number'&&(!Number.isSafeInteger(Number(v))||Number(v)<(f.min??0)||Number(v)>(f.max??9000000000000)))fail('INVALID',`${f.label} must be an integer in the displayed range.`);
      if(['text','textarea'].includes(f.kind)&&String(v).length>(f.max??(f.kind==='textarea'?1000:200)))fail('INVALID',`${f.label} is too long.`);
      if(f.kind==='boolean'&&typeof v!=='boolean')fail('INVALID',`${f.label} must be true or false.`);
      if(f.kind==='select'&&!f.options?.includes(String(v)))fail('INVALID',`Choose a valid ${f.label.toLowerCase()}.`);
      if(f.kind==='date'&&(!/^\d{4}-\d{2}-\d{2}$/.test(String(v))||!Number.isFinite(Date.parse(String(v)))||new Date(String(v)).toISOString().slice(0,10)!==v))fail('INVALID',`Choose a valid ${f.label.toLowerCase()}.`);
      if(f.kind==='time'&&!/^([01]\d|2[0-3]):[0-5]\d$/.test(String(v)))fail('INVALID','Use a valid time in UTC.');
      if(f.pattern==='email'&&!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(String(v)))fail('INVALID','Enter a fictional email address.');
      if(f.pattern==='lastFour'&&!/^\d{4}$/.test(String(v)))fail('INVALID','Store only four fictional account-ending digits.');
      if(f.kind==='evidence'&&(!/\.(txt|pdf|png|jpe?g)$/i.test(String(v))||/[\\/]/.test(String(v))))fail('INVALID','Select a fictional TXT, PDF, PNG or JPEG filename only.');
      if(f.kind==='relation'){
        const related=this.record(String(v),f.target);
        if(['ARCHIVED','REVOKED'].includes(related.status))fail('RESTRICTED',`${f.label} is archived or revoked.`);
        if(data.propertyId&&related.type!=='property'){const property=this.propertyOf(related);if(property&&property!==data.propertyId)fail('INVALID',`${f.label} belongs to another property.`)}
      }
    }
    const known=new Set(spec.fields.map(f=>f.key));for(const key of Object.keys(data))if(!known.has(key))fail('INVALID',`Unsupported field: ${key}`);
    if(this.state.records.some(r=>r.type===spec.id&&r.id!==id&&String(r.data.title).trim().toLowerCase()===String(data.title).trim().toLowerCase()))fail('CONFLICT','A record with this title already exists.');
    if(data.start&&data.end&&String(data.start)>=String(data.end))fail('INVALID','End date must be after start date.');
    if(spec.id==='party'&&((data.role==='Representative'&&data.share!==0)||(data.role==='Co-owner'&&Number(data.share)<1)))fail('INVALID','Representatives have zero title share; co-owner shares must be positive.');
    if(['accessgrant','documentgrant'].includes(spec.id)&&String(data.expires)<this.state.date)fail('INVALID','Choose a grant expiry on or after the demo date.');
    if(spec.guard==='shares'){const total=this.state.records.filter(r=>r.type==='party'&&r.id!==id&&r.data.propertyId===data.propertyId&&!['DECLINED'].includes(r.status)).reduce((n,r)=>n+Number(r.data.share),0);if(total+Number(data.share)>100)fail('INVALID','Combined proposed ownership shares cannot exceed 100%.')}
    if(spec.guard==='meter'){const previous=this.state.records.filter(r=>r.type==='meter'&&r.id!==id&&r.data.utilityId===data.utilityId&&String(r.data.date)<=String(data.date));if(previous.some(r=>Number(r.data.reading)>Number(data.reading)))fail('INVALID','Meter reading cannot decrease from the previous recorded value.')}
    if(spec.id==='renewal'&&String(data.newEnd)<=String(this.record(String(data.tenancyId),'tenancy').data.end))fail('INVALID','Renewal end must extend the tenancy.');
    if(spec.id==='availability'&&data.reason!=='Available window'&&this.state.records.some(r=>r.type==='booking'&&r.data.unitId===data.unitId&&activeBooking(r)&&overlap(data,r.data)))fail('CONFLICT','These dates overlap an active booking.');
    if(spec.id==='booking')this.guard('booking',{id:id??'',type:'booking',data,status:'REQUESTED',version:0,createdAt:this.state.date});
    if(['parcel','landterms'].includes(spec.id)&&this.record(String(data.propertyId),'property').data.kind!=='Land')fail('INVALID','Choose a land property for this record.');
    if(spec.id==='loi'&&this.record(String(data.propertyId),'property').data.kind!=='Commercial')fail('INVALID','Choose a commercial property for this letter of intent.');
    if(spec.id==='listing'){
      const property=this.record(String(data.propertyId),'property');
      if(['Rent','Short-let'].includes(String(data.transaction))&&!data.unitId)fail('INVALID','Choose a unit for a rental or short-let listing.');
      if(data.transaction==='Land lease'&&property.data.kind!=='Land')fail('INVALID','Land lease listings require a land property.');
      if(data.transaction==='Short-let'&&this.record(String(data.unitId),'unit').data.kind!=='Short-let')fail('INVALID','Short-let listings require a short-let unit.');
    }
  }
  private guard(name:string,r:DomainRecord){const data=r.data,records=this.state.records,property=this.propertyOf(r);
    if(name==='claim_reviewed'&&!records.some(x=>x.type==='ownership'&&x.id===data.claimId&&x.status==='DEMO_REVIEWED'))fail('PENDING','Record a simulated ownership-claim review before property review.');
    if(['publish','consent'].includes(name)){const purpose=name==='publish'?'Listing':'Ownership transfer';for(const party of records.filter(x=>x.type==='party'&&x.data.propertyId===property&&x.data.role==='Co-owner'&&x.status==='ACCEPTED'))if(!records.some(x=>x.type==='consent'&&x.data.partyId===party.id&&x.data.purpose===purpose&&x.status==='APPROVED'&&String(x.data.expires)>=this.state.date))fail('CONSENT','Each accepted co-owner must record current consent for this purpose.');}
    if(name==='quote_choice'&&records.some(x=>x.type==='quote'&&x.id!==r.id&&x.data.requestId===data.requestId&&x.status==='APPROVED'))fail('CONFLICT','A quote is already approved for this request.');
    if(name==='publish'){
      if(!records.some(x=>x.type==='verification'&&x.data.propertyId===property&&x.status==='DEMO_REVIEWED'))fail('PENDING','A separate simulated property review is required.');
      if(!records.some(x=>x.type==='pricing'&&x.data.listingId===r.id&&x.status==='APPLIED'))fail('INVALID','Apply an example pricing model before publication.');
      if(!records.some(x=>x.type==='availability'&&x.data.propertyId===property&&x.status==='CONFIRMED'&&x.data.reason==='Available window'))fail('STALE','Confirm a sample availability window before publication.');
      if(!data.previewed)fail('INVALID','Review the listing preview checkbox before publication.');
    }
    if(name==='consent'&&!data.consent)fail('CONSENT','All required demo consents must be recorded separately.');
    if(name==='consent_expiry'&&this.state.scenario==='expired_consent')fail('CONSENT','Occupant consent has expired in this scenario.');
    if(name==='delegation_consent'&&!records.some(x=>x.type==='consent'&&x.data.propertyId===property&&x.data.purpose==='Delegation'&&x.status==='APPROVED'&&String(x.data.expires)>=this.state.date))fail('CONSENT','Record current co-owner consent for delegation first.');
    if(name==='manager_access'&&(r.status!=='ACTIVE'||String(data.expires)<this.state.date))fail('DENIED','This manager assignment is revoked, suspended or expired.');
    if(name==='property_archive'&&records.some(x=>['listing','tenancy','booking'].includes(x.type)&&this.propertyOf(x)===r.id&&['DEMO_PUBLISHED','ACTIVE','APPROVED','CHECKED_IN'].includes(x.status)))fail('RESTRICTED','Active listings, tenancies or bookings must be resolved before archiving.');
    if(name==='tenancy_close'){
      if(!data.keysReturned||!data.inspection)fail('PENDING','Complete inspection and key-return records first.');
      if(!records.some(x=>x.type==='deposit'&&x.data.tenancyId===r.id&&x.status==='RESOLVED')||records.some(x=>x.type==='deposit'&&x.data.tenancyId===r.id&&x.status!=='RESOLVED'))fail('PENDING','Resolve all demonstration deposit cases before closure.');
    }
    if(name==='closure'&&records.some(x=>x.type==='tenancy'&&x.status!=='CLOSED'||x.type==='dispute'&&!['RESOLVED'].includes(x.status)||x.type==='deposit'&&x.status!=='RESOLVED'))fail('RESTRICTED','Closure is blocked by active tenancies or unresolved cases. No backend account is deleted.');
    if(name==='booking'){
      if(this.state.scenario==='stale_availability')fail('STALE','Availability is stale. Refresh the QA scenario before retrying.');
      const unit=this.record(String(data.unitId),'unit');if(unit.data.kind!=='Short-let')fail('INVALID','Choose a short-let unit.');
      if(!records.some(x=>x.type==='rate'&&x.data.unitId===data.unitId))fail('INVALID','Configure an example unit rate first.');
      if(records.some(x=>x.id!==r.id&&x.type==='booking'&&x.data.unitId===data.unitId&&activeBooking(x)&&overlap(data,x.data)))fail('CONFLICT','The short-let unit is already reserved for these dates.');
      if(records.some(x=>x.type==='availability'&&x.data.unitId===data.unitId&&x.status==='BLOCKED'&&x.data.reason!=='Available window'&&overlap(data,x.data)))fail('CONFLICT','An owner or maintenance block overlaps these dates.');
    }
  }
  private nextId(type:string){let n=1;while(this.state.records.some(r=>r.id===`demo-${type}-${String(n).padStart(2,'0')}`))n++;return `demo-${type}-${String(n).padStart(2,'0')}`}
  private template(data:Values,r:DomainRecord):Values{return Object.fromEntries(Object.entries(data).map(([key,value])=>[key,typeof value==='string'&&value.startsWith('$')?(value==='$id'?r.id:r.data[value.slice(1)]??''):value]))}
  private createEffect(type:string,status:string,data:Values){
    const item:DomainRecord={id:this.nextId(type),type,status,version:1,createdAt:this.state.date,data};this.state.records.push(item);return item;
  }
  execute(command:WorkspaceCommand):DomainRecord{
    const before=this.snapshot();try{return this.apply(command)}catch(error){this.state=before;throw error}
  }
  private apply(command:WorkspaceCommand){
    const spec=this.spec(command.type);if(spec.readOnly)fail('UNAVAILABLE',spec.provider??'This provider is unavailable.');
    const existing=command.id?this.record(command.id,command.type):undefined;
    const data:Values={...existing?.data,...command.data};
    this.permission(command.type,existing?this.propertyOf(existing):String(data.propertyId??(data.listingId?this.propertyOf(this.record(String(data.listingId))):'')));
    if(existing&&(command.version!==existing.version||this.state.scenario==='conflict'))fail('CONFLICT','This record changed. Reload the latest version before retrying.');
    let result:DomainRecord;
    if(command.op==='create'||command.op==='update'){
      if(spec.sensitive&&command.challenge!=='DEMO')fail('STEP_UP','Type DEMO to confirm this local security step.');
      this.validate(spec,data,existing?.id);
      if(command.op==='update'&&!existing)fail('NOT_FOUND','Select a record to edit.');
      if(existing){existing.data=data;existing.version++;result=existing}else result=this.createEffect(spec.id,spec.initial,data);
    }else{
      result=existing??fail('NOT_FOUND','Select a record first.');
      const transition=spec.actions.find(a=>a.id===command.action)??fail('INVALID','Unknown demo action.');
      if(!transition.from.includes(result.status))fail('CONFLICT','This action is no longer allowed in the current state.');
      if(transition.confirm&&!command.confirmed)fail('CONFIRMATION','Review and confirm this demo action.');
      if(transition.stepUp&&command.challenge!=='DEMO')fail('STEP_UP','Type DEMO to confirm this simulated security step. Never enter a real code or password.');
      if(transition.financial&&this.state.scenario==='unknown_financial_outcome')fail('UNKNOWN_OUTCOME','Financial outcome unknown in this scenario. No optimistic balance change or retry payment is allowed.');
      if(this.state.scenario==='stale_availability'&&spec.id==='availability')fail('STALE','Availability is stale; reset the scenario to refresh.');
      for(const requirement of transition.requires??[])if(!data[requirement.key])fail('INVALID',requirement.message);
      if(transition.guard)this.guard(transition.guard,result);
      if(transition.id==='movein'){
        const listing=this.record(String(data.listingId),'listing');
        if(this.state.records.some(t=>t.type==='tenancy'&&t.status==='ACTIVE'&&this.record(String(t.data.listingId)).data.unitId===listing.data.unitId))fail('CONFLICT','This unit already has an active tenancy.');
      }
      if(transition.copy){result=this.createEffect(spec.id,'DRAFT',{...result.data,title:`${result.data.title} · Copy ${this.state.revision}`})}
      else{if(transition.to!=='SAME')result.status=transition.to;result.version++}
      for(const e of transition.effects??[])this.createEffect(e.type,e.status,this.template(e.data,result));
      const update=transition.updateRelated;if(update){const related=this.record(String(result.data[update.via]),update.type);Object.assign(related.data,this.template(update.sets,result));if(update.status)related.status=update.status;related.version++}
      if(transition.id==='mfa')this.state.scenario='normal';
      if(spec.id==='tenancy'&&transition.id==='close'){const listing=this.record(String(result.data.listingId));const unit=this.state.records.find(x=>x.id===listing.data.unitId);if(unit){unit.data.occupied=false;unit.version++}}
      if(spec.id==='agreement'&&transition.id==='movein'){const listing=this.record(String(result.data.listingId));const unit=this.state.records.find(x=>x.id===listing.data.unitId);if(unit){unit.data.occupied=true;unit.version++}}
    }
    this.state.revision++;
    const message=`${command.op==='transition'?spec.actions.find(a=>a.id===command.action)?.label:command.op==='create'?'Created':'Updated'} · ${result.data.title}. Recorded in demo — no real transaction occurred.`;
    this.state.activity.unshift({id:`demo-event-${this.state.revision}`,type:result.type,recordId:result.id,propertyId:this.propertyOf(result),date:this.state.date,message,revision:this.state.revision});
    if(!['notification','preference'].includes(result.type)&&this.state.records.filter(r=>r.type==='preference').at(-1)?.data.inApp!==false)this.createEffect('notification','UNREAD',{title:`${spec.label} updated`,recordType:result.type,recordId:result.id,channel:'In-app only',message});
    return structuredClone(result);
  }
  summary(propertyId='all',start='0000-00-00',end='9999-99-99'){
    const rows=this.state.records.filter(r=>(propertyId==='all'||this.propertyOf(r)===propertyId)&&r.createdAt>=start&&r.createdAt<=end);
    const count=(type:string,status?:string)=>rows.filter(r=>r.type===type&&(!status||r.status===status)).length;
    const buckets=Object.fromEntries(['PENDING','HELD','AVAILABLE','RESTRICTED'].map(status=>[status,rows.filter(r=>r.type==='rent'&&r.status===status).reduce((n,r)=>n+Number(r.data.amountMinor),0)]));
    if(this.state.scenario==='restricted'){buckets.RESTRICTED=Object.values(buckets).reduce((n,v)=>n+v,0);buckets.PENDING=buckets.HELD=buckets.AVAILABLE=0}
    return {properties:rows.filter(r=>r.type==='property'&&r.status!=='ARCHIVED').length,units:count('unit'),occupied:rows.filter(r=>r.type==='unit'&&r.data.occupied).length,listings:count('listing','DEMO_PUBLISHED'),maintenanceOpen:rows.filter(r=>r.type==='maintenance'&&!['COMPLETED'].includes(r.status)).length,maintenanceCompleted:count('maintenance','COMPLETED'),bookings:rows.filter(r=>r.type==='booking'&&activeBooking(r)).length,buckets};
  }
}
