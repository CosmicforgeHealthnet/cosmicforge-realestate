import { readFileSync,writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
const root=fileURLToPath(new URL('../',import.meta.url));
const schema=JSON.parse(readFileSync(path.join(root,'shared/workflows.json'),'utf8'));
const dashboard=JSON.parse(readFileSync(path.join(root,'shared/dashboard.json'),'utf8'));
const records=[];
const create=(type,id,status,data)=>{const item={type,id,status,version:1,createdAt:'2026-10-17',data};records.push(item);return item};
create('account','demo-account-01','ACTIVE',{title:'Alex Demo · Owner',displayName:'Alex Demo',email:'alex@example.invalid',language:'English',mfa:true});
for(const p of dashboard.properties)create('property',p.id,'ACTIVE',{title:p.name,kind:p.kind,location:p.location,annualRentMinor:p.annualRentMinor,notes:'Fictional property, no legal title is certified.'});
create('property','demo-property-04','ACTIVE',{title:'Meadow Parcel · Demo',kind:'Land',location:'Fictional address · Ogun',annualRentMinor:0,notes:'Fictional land record. No registry or title certification.'});
let unit=0;for(const p of dashboard.properties)for(let i=0;i<p.units;i++)create('unit',`demo-unit-${String(++unit).padStart(2,'0')}`,'AVAILABLE',{title:`${p.name} · Unit ${i+1}`,propertyId:p.id,kind:p.kind==='Short-let'?'Short-let':p.kind==='Commercial'?'Office':'Apartment',capacity:2,occupied:i<p.occupied});
function defaults(spec){const data={};for(const f of spec.fields){data[f.key]=f.kind==='boolean'?false:f.kind==='number'?Math.max(f.min??0,f.key==='amountMinor'?100000000:f.key==='share'?25:1):f.kind==='relation'?(records.find(r=>r.type===f.target)?.id??''):f.kind==='select'?f.options[0]:f.kind==='date'?(f.key==='end'?'2026-10-28':f.key==='expires'?'2026-12-31':'2026-10-24'):f.kind==='time'?'10:00':f.kind==='evidence'?'sample-evidence.txt':f.pattern==='email'?'sample@example.invalid':f.pattern==='lastFour'?'0000':`Sample ${f.label.toLowerCase()} · Demo`;}return data;}
const status={ownership:'DEMO_REVIEWED',verification:'DEMO_REVIEWED',listing:'DEMO_PUBLISHED',pricing:'APPLIED',availability:'CONFIRMED',application:'APPROVED',offer:'ACCEPTED',agreement:'MOVED_IN',maintenance:'QUOTES_RECEIVED',party:'ACCEPTED',consent:'APPROVED',viewing:'APPROVED',booking:'APPROVED'};
for(const spec of schema.types){if(records.some(r=>r.type===spec.id))continue;const data=defaults(spec);data.title=`${spec.label} · Demo`;
 if(spec.id==='listing')Object.assign(data,{previewed:true,propertyId:'demo-property-01',unitId:'demo-unit-01'});
 if(spec.id==='agreement')Object.assign(data,{reviewed:true,signers:true,keys:true,inspection:true});
 if(spec.id==='application')Object.assign(data,{evidenceAccess:true,assessment:'Human reviewer sample notes; no external background check.'});
 if(spec.id==='viewing')data.consent=true;
 if(spec.id==='sale'||spec.id==='transfer')data.consent=false;
 if(spec.id==='notification')Object.assign(data,{recordType:'maintenance',recordId:'demo-maintenance-01',message:'A fictional quote is ready for review. No notification was sent.'});
 if(spec.id==='booking')Object.assign(data,{propertyId:'demo-property-02',unitId:'demo-unit-03',start:'2026-10-25',end:'2026-10-28'});
 if(spec.id==='rate')data.unitId='demo-unit-03';
 if(['parcel','landterms'].includes(spec.id))data.propertyId='demo-property-04';
 if(['loi','sale'].includes(spec.id))data.propertyId='demo-property-03';
 if(spec.id==='tenancy')Object.assign(data,{start:'2026-10-01',end:'2027-09-30'});
 if(spec.id==='renewal')data.newEnd='2028-09-30';
 if(spec.id==='availability')Object.assign(data,{reason:'Available window',unitId:'demo-unit-03',propertyId:'demo-property-02',start:'2026-10-18',end:'2026-12-31'});
 create(spec.id,`demo-${spec.id}-01`,status[spec.id]??spec.initial,data);
}
// Every published listing has its own applicable price and reviewed property context.
create('listing','demo-listing-02','DEMO_PUBLISHED',{title:'Palm Studio · Short-let demo',propertyId:'demo-property-02',unitId:'demo-unit-03',transaction:'Short-let',description:'Fictional furnished studio.',previewed:true});
create('pricing','demo-pricing-02','APPLIED',{title:'Palm Studio · Nightly example',listingId:'demo-listing-02',amountMinor:5000000,feeMinor:100000,frequency:'Nightly',policy:'Illustrative service fee, not statutory.'});
create('ownership','demo-ownership-02','DEMO_REVIEWED',{title:'Palm Studio claim · Demo',propertyId:'demo-property-02',claimant:'Alex Demo',evidence:'fictional-claim.txt'});
create('verification','demo-verification-02','DEMO_REVIEWED',{title:'Palm Studio review · Demo',propertyId:'demo-property-02',claimId:'demo-ownership-02',evidence:'fictional-evidence.txt'});
const state={revision:1,scenario:'normal',actor:'owner',date:schema.date,records,activity:[],tasks:dashboard.tasks,workspaceName:dashboard.workspaceName};
writeFileSync(path.join(root,'shared/workspace-seed.json'),JSON.stringify(state,null,2)+'\n');
console.log(`Seeded ${records.length} connected fictional records.`);
