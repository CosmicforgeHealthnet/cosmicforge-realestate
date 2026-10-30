"""Business event schema catalogue and producer drift gate."""
import json,re,sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
SERVICE=ROOT/'services/business-operations-service'
uuid={'type':'string','format':'uuid','pattern':'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'}
string={'type':'string'}
events=set()
for p in (SERVICE/'internal').rglob('*.go'):
 if not p.name.endswith('_test.go'):events.update(re.findall(r'"(business\.[a-z_.]+\.v[1-9][0-9]*)"',p.read_text()))
changed=[]
for name in sorted(events):
 props={'organisation_id':uuid,'target_id':uuid,'action':string,'reason':string,'expired_at':{'type':'string','format':'date-time'}};required=['organisation_id']
 if name=='business.notification.requested.v1':props={'job_id':uuid,'key_id':string,'ciphertext':{'type':'string','contentEncoding':'base64','minLength':32}};required=list(props)
 elif name=='business.account.registered.v1':props.update(account_id=uuid,organisation_state={'const':'DRAFT'});required+=['account_id','organisation_state']
 elif name=='business.staff.updated.v1':props.update(staff_id=uuid,account_id=uuid,status=string,version={'type':'integer','minimum':1});required+=['staff_id','account_id','status','version']
 elif name=='business.capacity.changed.v1':props.update(capacity_id=uuid,code=string,status={'enum':['DECLARED','PENDING','ACTIVE','RESTRICTED','SUSPENDED','EXPIRED']},expires_at={'type':['string','null'],'format':'date-time'});required+=['capacity_id','code','status']
 elif name in ('business.verified.v1','business.restricted.v1','business.organisation.verification_pending.v1'):props.update(verification_id=uuid,state={'enum':['ACTIVE','RESTRICTED','KYB_PENDING']},expires_at={'type':['string','null'],'format':'date-time'});required+=['state'];
 elif name.startswith('business.approval.'):
  props.update(approval_id=uuid,requester_id=uuid,state={'enum':['DRAFT','PENDING','APPROVED','REJECTED','EXPIRED','CANCELLED']},level={'type':'integer','minimum':0},policy_id=uuid,policy_version={'type':'integer','minimum':1});required+=['approval_id','state','policy_id','policy_version']
 else:required+=['target_id']
 schema={'$schema':'https://json-schema.org/draft/2020-12/schema','$id':'https://contracts.cosmicforge.realestate/events/business/'+name+'.schema.json','title':name,'description':'Producer business-operations-service. Canonical subject realestate.'+name+'. State and outbox commit atomically; at-least-once consumers deduplicate event_id. Secret-bearing Notification requests are encrypted, not plaintext.','allOf':[{'$ref':'../../schemas/event-envelope.schema.json'}],'properties':{'event_type':{'const':name},'producer':{'const':'business-operations-service'},'payload':{'type':'object','properties':props,'required':required,'additionalProperties':True}}}
 path=ROOT/'contracts/events/business'/f'{name}.schema.json';text=json.dumps(schema,indent=2)+'\n'
 if not path.exists() or path.read_text()!=text:
  changed.append(str(path.relative_to(ROOT)))
  if '--check'not in sys.argv:path.parent.mkdir(parents=True,exist_ok=True);path.write_text(text)
existing={p.name.removesuffix('.schema.json') for p in (ROOT/'contracts/events/business').glob('*.schema.json')}
if existing-events:raise SystemExit('Unreferenced Business schemas: '+str(existing-events))
if '--check'in sys.argv and changed:raise SystemExit('Business event schema drift: '+','.join(changed))
print(f'{len(events)} Business event schemas '+('verified' if '--check'in sys.argv else 'generated'))
