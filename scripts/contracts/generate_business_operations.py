"""Generate Section 3 OpenAPI/Postman together. No runtime secrets or network IO.
Run from repository root: python scripts/contracts/generate_business_operations.py
--check compares generated artifacts without changing them.
"""
import json,re,sys,copy
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
SERVICE=ROOT/'services/business-operations-service'
UID='019a5fb0-0000-7000-8000-000000000001'
DATE='2026-10-30T12:00:00Z'
def ref(n):return {'$ref':'#/components/schemas/'+n}
def obj(props,required=(),closed=False):
 d={'type':'object','properties':props,'additionalProperties':not closed}
 if required:d['required']=list(required)
 return d
def arr(s):return {'type':'array','items':s}
def fields(**kw):return obj({k:typ(v) for k,v in kw.items()})
def typ(t):
 t=t.strip()
 if t.startswith('*'):return typ(t[1:])
 if t.startswith('[]'):return arr(typ(t[2:]))
 if t in ('ids.ID',):return {'type':'string','format':'uuid','pattern':'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$','example':UID}
 if t=='time.Time':return {'type':'string','format':'date-time','example':DATE}
 if t=='bool':return {'type':'boolean'}
 if t in ('int','int32','int64','uint64'):return {'type':'integer','format':'int64'}
 if t in ('string','permissions.Permission','State','organisation.State','Protocol'):return {'type':'string'}
 if t in ('json.RawMessage','any') or t.startswith('map['):return {'type':'object','additionalProperties':True}
 return ref(t.split('.')[-1])
S={}
for p in (SERVICE/'internal').rglob('*.go'):
 if '_test.go' in p.name or 'repositories' in p.parts:continue
 text=p.read_text()
 for m in re.finditer(r'type (\w+) struct \{([^}]+)\}',text,re.S):
  props={};required=[]
  for fm in re.finditer(r'\w+\s+([^\n`]+)\s+`json:"([^",]+)([^"`]*)"`',m[2]):
   t,name,opt=fm.groups()
   if name=='-':continue
   props[name]=typ(t)
   if 'omitempty' not in opt:required.append(name)
  if props:S[m[1]]=obj(props,required,True)
# Explicit inline command schemas, optional defaults are documented below.
def command(name,required=(),**kw):S[name]=obj({k:typ(v) for k,v in kw.items()},required,True)
command('DeviceLabel',('label','version'),label='string',version='int64')
command('RenewKYB',('previous_verification_id',),previous_verification_id='ids.ID')
command('Password',('password',),password='string')
command('PasswordChange',('current_password','new_password'),current_password='string',new_password='string')
command('Refresh',('refresh_token',),refresh_token='string')
command('Code',('code',),code='string')
command('MFAVerify',('code',),code='string',mfa_token='string',recovery='bool')
command('SelectOrganisation',('organisation_id',),organisation_id='ids.ID')
command('AcceptInvitation',('invitation_token',),invitation_token='string',password='string',display_name='string')
command('Evidence',('media_id','evidence_type'),media_id='ids.ID',evidence_type='string')
command('HandoverFinish',('version','reason'),version='int64',reason='string')
command('Activation',('active','version'),active='bool',version='int64',approval_id='ids.ID')
command('Reason',('reason',),reason='string')
command('ApprovalCancel',('reason','version'),reason='string',version='int64')
command('Version',('version',),version='int64')
command('CapacityDeclare',('code',),code='string')
command('CapacityRestrict',('version','status','reason'),version='int64',status='string',reason='string')
command('CapacityRefresh',('verification_id','version'),verification_id='ids.ID',version='int64')
command('Pack',('code','required_capacities','enabled'),code='string',required_capacities='[]string',enabled='bool',version='int64')
command('Lifecycle',('state','version','reason'),state='string',version='int64',reason='string',approval_id='ids.ID')
command('Representative',('account_id','verification_id','version','reason','approval_id'),account_id='ids.ID',verification_id='ids.ID',version='int64',reason='string',approval_id='ids.ID')
# Typed response projections, matching handlers (no credentials in list data).
S['Scope']=fields(organisation_id='ids.ID',kind='string',id='ids.ID')
S['Account']=fields(account_id='ids.ID',email='string',display_name='string',status='string',version='int64',created_at='time.Time')
S['Organisation']=fields(organisation_id='ids.ID',name='string',registered_name='string',registration_number='string',category='string',jurisdiction='string',currency='string',timezone='string',state='string',version='int64',representative_account_id='ids.ID',created_at='time.Time',updated_at='time.Time')
S['Hierarchy']=fields(id='ids.ID',organisation_id='ids.ID',name='string',status='string',version='int64',created_at='time.Time',updated_at='time.Time',region_id='ids.ID',branch_id='ids.ID',department_id='ids.ID',timezone='string',currency='string')
S['Session']=fields(session_id='ids.ID',device_id='ids.ID',organisation_id='ids.ID',created_at='time.Time',expires_at='time.Time',last_refreshed_at='time.Time',revoked_at='time.Time',mfa_at='time.Time')
S['Device']=fields(device_id='ids.ID',label='string',platform='string',risk_flags='[]string',created_at='time.Time',last_seen_at='time.Time',version='int64',retired_at='time.Time')
S['MFAMethod']=fields(method_id='ids.ID',kind='string',status='string',created_at='time.Time',confirmed_at='time.Time')
S['Membership']=fields(membership_id='ids.ID',organisation_id='ids.ID',name='string',status='string',role_id='ids.ID',effective_at='time.Time',expires_at='time.Time')
S['Role']=fields(role_id='ids.ID',role_key='ids.ID',name='string',version='int64',active='bool',senior_emergency_eligible='bool',created_at='time.Time',capabilities='[]string')
S['Permission']=fields(code='string',description='string')
S['Staff']=fields(staff_id='ids.ID',account_id='ids.ID',display_name='string',email='string',role_id='ids.ID',role_name='string',role_version='int64',department_id='ids.ID',team_id='ids.ID',kind='string',status='string',scope_kind='string',scope_id='ids.ID',effective_at='time.Time',expires_at='time.Time',version='int64',grants='[]scopeGrantCommand')
S['Invitation']=fields(invitation_id='ids.ID',staff_id='ids.ID',account_id='ids.ID',email='string',role_id='ids.ID',status='string',expires_at='time.Time',accepted_at='time.Time',created_at='time.Time')
S['Capacity']=fields(capacity_id='ids.ID',code='string',status='string',verification_link_id='ids.ID',effective_at='time.Time',expires_at='time.Time',restricted_at='time.Time',version='int64',authorizes='bool')
S['CapabilityPack']=fields(capability_pack_id='ids.ID',code='string',required_capacities='[]string',enabled='bool',version='int64')
S['VerificationLink']=fields(verification_link_id='ids.ID',case_id='ids.ID',provider_status='string',expires_at='time.Time',provider_updated_at='time.Time',updated_at='time.Time')
S['PolicyResult']=obj({'policy':ref('Policy'),'policy_key':typ('ids.ID')})
S['ApprovalResult']=obj({'approval':ref('View'),'version':typ('int64'),'operation_digest':{'type':'string'},'context':obj({'operation':{'type':'object'},'reason':{'type':'string'},'amount_minor':{'type':['integer','null']},'currency':{'type':['string','null']}})})
S['Evaluation']=obj({'required':{'type':'boolean'},'policy':ref('Policy'),'operation_digest':{'type':'string'},'scope':ref('Scope')})
S['Delegation']=fields(delegation_id='ids.ID',delegator_membership_id='ids.ID',delegate_membership_id='ids.ID',capability='string',scope_kind='string',scope_id='ids.ID',inherit='bool',effective_at='time.Time',expires_at='time.Time',revoked_at='time.Time',require_mfa='bool',amount_limit_minor='int64',currency='string',reason='string')
S['Emergency']=fields(break_glass_id='ids.ID',membership_id='ids.ID',scope_kind='string',scope_id='ids.ID',capabilities='[]string',reason='string',expires_at='time.Time',revoked_at='time.Time',reviewed_by='ids.ID',review_reason='string',reviewed_at='time.Time')
S['Audit']=fields(id='ids.ID',actor_id='ids.ID',action='string',target_type='string',target_id='ids.ID',correlation_id='string',change_context='json.RawMessage',occurred_at='time.Time')
S['BooleanResult']=fields(logged_out='bool',revoked='bool',removed='bool',verified='bool',changed='bool',sessions_revoked='bool',reviewed='bool')
S['Acceptance']=fields(account_id='ids.ID',organisation_id='ids.ID',staff_id='ids.ID',login_required='bool')
S['Recovery']=fields(recovery_codes='[]string')
S['TokenSelection']=fields(access_token='string',expires_at='time.Time')
S['Handover']=fields(handover_id='ids.ID',staff_id='ids.ID',successor_membership_id='ids.ID',status='string',reason='string',evidence_ids='[]ids.ID',version='int64',staff_version='int64',initiated_by='ids.ID',acknowledged_by='ids.ID',acknowledged_at='time.Time',completed_by='ids.ID',completed_at='time.Time',completion_reason='string',created_at='time.Time')
S['EffectiveCapacity']=fields(code='string',status='string',supported='bool',authorizes='bool',expires_at='time.Time')
S['Closure']=fields(ready='bool',blockers='[]string')
S['LifecycleResult']=fields(state='string',version='int64')
S['PackEvaluation']=fields(capability_pack_id='ids.ID',authorizes='bool')
# Version/default policy differs from JSON omitempty in a few command types.
for n in ('ProfilePatch','staffPatch','decisionCommand'):
 S[n]['required']=['version']+(['reason'] if n!='ProfilePatch' else [])
S['decisionCommand']['required']=['version','level','outcome','reason']
S['hierarchyCommand']['required']=[]
S['roleCommand']['required']=['name','capabilities']
S['policyCommand']['required']=['action','risk','levels']
S['approvalCommand']['required']=['action','operation','reason']
S['scopeGrantCommand']['required']=['capability','scope_kind']
S['invitationCommand']['required']=['email','role_id','grants','reason']
S['delegationCommand']['required']=['delegate_membership_id','grant','reason']
S['emergencyCommand']['required']=['scope_kind','scope_id','capabilities','reason']
# Explicit nullable projection fields are emitted as JSON null by PostgreSQL.
for name in ('Session','Hierarchy','Device','Staff','Invitation','Capacity','VerificationLink','Delegation','Emergency'):
 for k,v in S[name]['properties'].items():
  if k in ('revoked_at','mfa_at','last_refreshed_at','expires_at','restricted_at','verification_link_id','provider_updated_at','region_id','branch_id','department_id','team_id','amount_limit_minor','currency','reviewed_by','review_reason','reviewed_at','accepted_at','organisation_id'):
   v['type']=[v.get('type','string'),'null']
R=[]
def route(method,path,cap='',req=None,res='BooleanResult',status=200,idem=False,mfa=False,public=False,notes=''):
 R.append(dict(method=method,path='/api/v1/business'+path,capability=cap,request=req,response=res,status=status,idempotent=idem,step_up=mfa,public=public,notes=notes))
route('POST','/auth/register',req='Registration',res='Registered',status=201,idem=True,public=True,notes='Registration creates a DRAFT organisation. Retry fingerprint is peppered; no token response is cached.')
route('POST','/auth/login',req='Login',res='LoginResult',public=True,notes='Returns tokens OR a short lived single use MFA challenge. Generic credential errors; account/client rate limits.')
route('POST','/auth/refresh',req='Refresh',res='Tokens',public=True,notes='Rotates once. Reuse invalidates the entire token family, including access tokens. A concurrent loser triggers family revocation.')
for path in ('logout','logout-all'):route('POST','/auth/'+path)
route('POST','/auth/password/change',req='PasswordChange',mfa=True,notes='Requires the current password; when MFA is configured, recent step-up is required. Revokes all sessions.')
route('GET','/account',res='Account')
route('POST','/auth/mfa/enrol',req='enrolMFABody',res='Enrolment',status=201,notes='kind defaults to TOTP; EMAIL uses the account email, SMS requires phone in E.164 form. TOTP returns its seed only during enrolment; EMAIL/SMS queue an encrypted Notification challenge and return no secret or code. Confirm possession before activation; only one method may be pending/active per account.')
route('POST','/auth/mfa/confirm',req='Code',res='Recovery',notes='Single confirmation returns eight one-time recovery codes; store securely. Other sessions are revoked.')
route('POST','/auth/mfa/verify',req='MFAVerify',res='Tokens',public=True,notes='With mfa_token completes login and returns Tokens; without it requires account authentication and returns {verified:true}. Recovery=true accepts one unused recovery code; TOTP replay is rejected.')
route('GET','/auth/mfa',res='List:MFAMethod')
route('POST','/auth/mfa/challenge',res='DeliveryChallenge',status=202,idem=True,notes='Authenticated EMAIL/SMS step-up challenge, bound to this live session; five-minute expiry and five attempts. TOTP uses its authenticator app. Idempotent retries replay the original queue acceptance, not delivery; expired challenges require a new key.')
route('DELETE','/auth/mfa',req='Password',mfa=True,notes='Recent MFA plus password; removes method and revokes all sessions.')
route('GET','/sessions',res='List:Session')
route('DELETE','/sessions/{session_id}')
route('GET','/devices',res='List:Device')
route('PATCH','/devices/{id}',req='DeviceLabel',res='Device')
route('DELETE','/devices/{id}',res='BooleanResult',notes='Revokes all sessions associated with this account-owned device; client labels never establish trust.')
route('GET','/memberships',res='List:Membership')
route('POST','/auth/organisation',req='SelectOrganisation',res='TokenSelection',notes='Validates a live membership and changes only this session. Use returned access token. The signed Gateway organisation must match the account token.')
route('POST','/staff/invitations/accept',req='AcceptInvitation',res='Acceptance',public=True,notes='Token from secure Notification delivery is single use and expires. Existing active accounts must authenticate as the invited account. New provisional accounts set a password; no credentials are reset for existing accounts.')
route('GET','/organisation','organisation:read',res='Organisation')
route('GET','/profile','organisation:read',res='Organisation',notes='Explicit Annex D.1 compatibility alias of GET /organisation; identical handler and security.')
route('PATCH','/profile','organisation:write','ProfilePatch','Organisation',notes='Explicit Annex D.1 compatibility alias of PATCH /organisation; identical versioning, validation and security.')
route('GET','/verification','organisation:read',res='VerificationLink',notes='Explicit Annex D.2/Annex E projection alias of GET /organisation/verification. Section 4 authority workflows are not exposed.')
route('GET','/capabilities','organisation:read',res='List:EffectiveCapacity',notes='Annex D.1 effective operating-capacity projection. Unsupported, unverified, expired and restricted capacities cannot authorize.')
route('PATCH','/organisation','organisation:write','ProfilePatch','Organisation',notes='Current version required. Registered identity fields are editable only in DRAFT; later changes require an authoritative review.')
route('POST','/organisation/verification','organisation:verify',res='Case',status=202,mfa=True,notes='Uses stable organisation-scoped provider idempotency and authenticated current-status lookup; no automatic approval.')
route('GET','/organisation/verification','organisation:read',res='VerificationLink')
route('POST','/organisation/verification/renew','organisation:verify','RenewKYB','Case',202,mfa=True,notes='Existing authenticated reverify contract; stable key from previous case. Preserves immutable linkage history and restricts operations pending renewed authoritative approval.')
route('POST','/organisation/verification/refresh','organisation:verify',res='Case')
route('POST','/organisation/verification/submit','organisation:verify',res='Case',status=202,mfa=True)
route('POST','/organisation/verification/evidence','organisation:verify','Evidence','Evidence',201,True,True)
route('GET','/organisation/capacities','organisation:read',res='List:Capacity')
route('POST','/organisation/capacities','capacity:write','CapacityDeclare','Capacity',201,True,True)
route('PATCH','/organisation/capacities/{id}','capacity:write','CapacityRestrict','Capacity',200,True,True)
route('POST','/organisation/capacities/{id}/verification','capacity:write','CapacityRefresh','Capacity',mfa=True,notes='503 until a capacity-specific authenticated provider assertion adapter is available. Generic KYB does not establish capacity eligibility.')
route('GET','/organisation/capability-packs','organisation:read',res='List:CapabilityPack')
route('POST','/organisation/capability-packs','capacity:write','Pack','CapabilityPack',200,True,True)
route('POST','/organisation/capability-packs/{id}/evaluate','organisation:read',res='PackEvaluation')
route('POST','/organisation/lifecycle','organisation:read','Lifecycle','LifecycleResult',200,True,True,notes='Primary representative only. Resume requires current KYB and exact independent approval; closure of a previously active business requires downstream readiness contracts.')
route('GET','/organisation/closure-readiness','organisation:read',res='Closure')
route('POST','/organisation/representative','organisation:write','Representative','Organisation',200,True,True,notes='503 until the Verification provider supports legal representative assertions. Requires active successor and exact independent approval; does not grant successor permissions.')
for path in ('regions','branches','departments','teams'):
 route('GET','/'+path,'hierarchy:read',res='List:Hierarchy',notes='Results filtered by trusted scope after stable database pagination; next_offset advances over scanned candidates.')
 route('POST','/'+path,'hierarchy:write','hierarchyCommand','Hierarchy',201,True,notes='Name required. Departments require branch_id; teams require department_id. Currency/timezone inherit from organisation unless specified for a branch.')
 route('GET','/'+path+'/{id}','hierarchy:read',res='Hierarchy')
 route('PATCH','/'+path+'/{id}','hierarchy:write','hierarchyCommand','Hierarchy',200,True,notes='Current version required. Lifecycle and parent changes require recent MFA and scope authority at both old and new parent.')
route('GET','/staff','staff:read',res='List:Staff')
route('POST','/staff','staff:invite','invitationCommand','Invitation',201,True,True,notes='Notification Service delivers a protected token; raw token never returned. After first activation, exact independent STAFF_ROLE_CHANGE approval is required. During initial onboarding only the representative may configure privileges with MFA.')
route('GET','/staff/me',res='Staff')
route('GET','/staff/{staff_id}/handover','staff:read',res='Handover')
route('POST','/staff/{staff_id}/handover','staff:write','handoverCommand','Handover',201,True,True,notes='Evidence references only. Successor must already have scope authority; departing staff lose access immediately. No permission is transferred by handover.')
route('POST','/staff/{staff_id}/handover/{id}/acknowledge','staff:read','HandoverFinish','Handover',200,True,True,notes='Only the designated active successor can acknowledge.')
route('POST','/staff/{staff_id}/handover/{id}/complete','staff:write','HandoverFinish','Handover',200,True,True,notes='An administrator other than departing staff completes the acknowledged handover. Final termination uses PATCH staff; completed evidence is immutable.')
route('GET','/staff/{staff_id}','staff:read',res='Staff')
route('PATCH','/staff/{staff_id}','staff:write','staffPatch','Staff',200,True,True,notes='Normal termination requires completed acknowledged handover; immediate suspension remains available. Role/scope expansion needs exact independent approval after activation; self escalation forbidden. Suspension/termination immediately revoke sessions selected into this organisation.')
route('GET','/staff/invitations','staff:invite',res='List:Invitation')
route('POST','/staff/invitations/{id}/revoke','staff:invite','Reason','BooleanResult',200,True,True)
route('GET','/permissions','permissions:read',res='List:Permission')
route('GET','/roles','roles:read',res='List:Role')
route('POST','/roles','roles:write','roleCommand','Role',201,True,True,notes='Creates explicit versioned capability mapping. Exact independent STAFF_ROLE_CHANGE approval required after activation; role name never authorizes.')
route('GET','/roles/{id}','roles:read',res='Role')
route('PATCH','/roles/{id}','roles:write','roleCommand','Role',201,True,True,notes='Creates next immutable role version. Existing memberships retain their current version until an approved reassignment.')
route('POST','/roles/{id}/activation','roles:write','Activation','Role',200,True,True)
route('GET','/approval-policies','policies:read',res='List:PolicyResult')
route('POST','/approval-policies','policies:write','policyCommand','PolicyResult',201,True,True)
route('GET','/approval-policies/{id}','policies:read',res='PolicyResult')
route('PATCH','/approval-policies/{id}','policies:write','policyCommand','PolicyResult',201,True,True,notes='Creates immutable next policy version. Existing approval requests keep their original policy snapshot.')
route('POST','/approval-policies/{id}/activation','policies:write','Activation','PolicyResult',200,True,True)
route('POST','/approval-policies/evaluate','approvals:create','approvalCommand','Evaluation',mfa=True)
route('GET','/approvals','approvals:read',res='List:ApprovalResult')
route('POST','/approvals','approvals:create','approvalCommand','ApprovalResult',201,True,True,notes='Binds canonical operation digest and immutable policy version; amounts are integer minor units with currency. Drafts must be submitted before decisions.')
route('GET','/approvals/{id}','approvals:read',res='ApprovalResult')
route('POST','/approvals/{id}/decision','approvals:decide','decisionCommand','ApprovalResult',200,True,True,notes='Also requires current-level approver capability, scope and amount limit. Ordered independent approvers; no self approval or one actor across independent levels. Atomic row lock and version protect concurrency.')
route('POST','/approvals/{id}/cancel','approvals:create','ApprovalCancel','ApprovalResult',200,True,True)
route('POST','/approvals/{id}/submit','approvals:create','ApprovalCancel','ApprovalResult',200,True,True)
route('GET','/delegations',res='List:Delegation',notes='Only delegator/delegate membership grants are listed.')
route('POST','/delegations','delegations:write','delegationCommand','Delegation',201,True,True,notes='Scoped read, hierarchy-write and explicit approval capabilities, at most 30 days. Approval decisions retain delegator provenance and cannot approve a delegator request or reuse one authority across independent requirements. Must fit direct authority and recipient role ceiling. Chaining prohibited; source revocation is checked on every use.')
route('DELETE','/delegations/{id}','delegations:write','Reason','BooleanResult',200,True,True)
route('GET','/break-glass',res='List:Emergency',notes='Lists the authenticated member’s grants; independent reviewers use the audited grant reference.')
route('POST','/break-glass','break_glass:request','emergencyCommand','Emergency',201,True,True,notes='Designated senior role; 20-character reason; branch/department/team only; hierarchy:read or staff:read; 30–900 seconds. Exact independent BREAK_GLASS_ACCESS approval after activation; every use audited; explicit denies and tenant boundaries remain enforced.')
route('DELETE','/break-glass/{id}','break_glass:request','Reason','BooleanResult',200,True,True)
route('POST','/break-glass/{id}/review','break_glass:review','Reason','BooleanResult',200,True,True,notes='Independent reviewer only; immutable once completed; revokes access immediately.')
route('GET','/audit-events','audit:read',res='List:Audit',mfa=True)
# Request examples are syntactically representative, not seeded demo records.
def example(schema,depth=0):
 if depth>20:return {}
 if 'oneOf'in schema:return example(schema['oneOf'][0],depth+1)
 if '$ref'in schema:return example(S[schema['$ref'].split('/')[-1]],depth+1)
 t=schema.get('type','object');t=t[0] if isinstance(t,list) else t
 if t=='object':return {k:example(v,depth+1) for k,v in schema.get('properties',{}).items() if k in schema.get('required',schema.get('properties',{}))}
 if t=='array':return [example(schema['items'],depth+1)]
 if t=='boolean':return True
 if t=='integer':return 1
 return schema.get('example',schema.get('enum',['example'])[0])
S['Error']=obj({'error':obj({'code':{'type':'string'},'message':{'type':'string'},'request_id':{'type':'string'}},('code','message'))},('error',))
# Fix parser name collision: Evidence request and response share the same key.
S['EvidenceResponse']=fields(evidence_id='ids.ID',media_id='ids.ID',evidence_type='string')
for r in R:
 if r['response']=='Evidence':r['response']='EvidenceResponse'
# Provider case contract includes a nested subject.
S['Case']=obj({'verification_id':typ('ids.ID'),'verification_type':{'type':'string'},'subject':ref('Subject'),'status':{'type':'string'},'reason_code':{'type':'string'},'decided_at':{'type':['string','null'],'format':'date-time'},'expires_at':{'type':['string','null'],'format':'date-time'},'updated_at':typ('time.Time'),'supersedes_verification_id':typ('ids.ID')})
for n in ('Tokens','LoginResult','Enrolment','Registered','Policy','View','Decision','Level','Subject'):
 if n not in S:raise RuntimeError('missing actual Go schema '+n)
# Representative safe request examples; IDs are placeholders for real created records.
EXAMPLES={
 'Registration':{'email':'owner@example.test','password':'{{business_password}}','display_name':'Business Owner','organisation_name':'Example Business'},
 'hierarchyCommand':{'name':'Regional office'},
 'roleCommand':{'name':'Branch Reader','capabilities':['hierarchy:read','staff:read']},
 'policyCommand':{'action':'STAFF_ROLE_CHANGE','risk':'HIGH','scope_kind':'COMPANY','levels':[{'capability':'staff:approve','count':1,'require_mfa':True}]},
 'approvalCommand':{'action':'STAFF_ROLE_CHANGE','risk':'HIGH','scope_kind':'COMPANY','operation':{'resource':'role_template','name':'Branch Reader','capabilities':['hierarchy:read','staff:read']},'reason':'Independent review of staff role configuration'},
 'decisionCommand':{'version':1,'level':0,'outcome':'APPROVED','reason':'Supporting evidence reviewed'},
 'invitationCommand':{'email':'staff@example.test','role_id':UID,'scope_kind':'BRANCH','scope_id':UID,'kind':'EMPLOYEE','grants':[{'capability':'hierarchy:read','scope_kind':'BRANCH','scope_id':UID,'inherit':True}],'reason':'Staff onboarding for assigned branch'},
 'delegationCommand':{'delegate_membership_id':UID,'grant':{'capability':'hierarchy:read','scope_kind':'BRANCH','scope_id':UID,'inherit':True,'expires_at':'2026-10-31T12:00:00Z'},'reason':'Temporary absence cover'},
 'emergencyCommand':{'scope_kind':'BRANCH','scope_id':UID,'capabilities':['hierarchy:read'],'reason':'Emergency incident requires limited branch inspection','duration_seconds':300},
 'CapacityDeclare':{'code':'PROPERTY_MANAGEMENT'},
 'CapacityRestrict':{'version':1,'status':'SUSPENDED','reason':'Compliance investigation'},
 'Pack':{'code':'MANAGEMENT_OPERATIONS','required_capacities':['PROPERTY_MANAGEMENT'],'enabled':True},
 'Lifecycle':{'version':1,'state':'CLOSURE_PENDING','reason':'Business closure requested'},
 'Reason':{'reason':'Independent security review completed'},
 'ApprovalCancel':{'version':1,'reason':'Requester withdrew the proposed operation'},
 'staffPatch':{'version':1,'status':'SUSPENDED','reason':'Immediate security containment'},
 'enrolMFABody':{'password':'{{business_password}}','kind':'TOTP'},
 'ProfilePatch':{'version':1,'registered_name':'Example Business Limited','registration_number':'EXAMPLE-001','jurisdiction':'NG','currency':'NGN','timezone':'Africa/Lagos'},
}
# Exclude implementation-only structs and ensure no dangling references.
used=set()
def include(s):
 if isinstance(s,dict):
  if '$ref'in s:
   n=s['$ref'].split('/')[-1]
   if n not in used:
    used.add(n);include(S[n])
  for v in s.values():include(v)
 elif isinstance(s,list):
  for v in s:include(v)
O={'openapi':'3.1.0','info':{'title':'CosmicForge Business Operations — Section 3','version':'1.0.0','description':'Canonical public prefix /api/v1/business. The explicit legacy /api/v1/business-operations alias has identical authentication and handlers; it is not a second API. All traffic crosses the Gateway; direct service calls require its signed internal identity. Trusted tenant comes from live session and membership, never an X-Organisation-ID override. Provider-specific assertion paths return 503 when unavailable. No SSO provider or downstream money movement is claimed.'},'servers':[{'url':'{base_url}','variables':{'base_url':{'default':'http://localhost:8080'}}}],'security':[{'BusinessBearer':[]}],'paths':{},'components':{'securitySchemes':{'BusinessBearer':{'type':'http','scheme':'bearer','bearerFormat':'Ed25519 JWT'}},'schemas':{}}}
folders={n:{'name':n,'item':[]} for n in ('00 Auth & Security','01 Organisation & Staff','02 Roles, Permissions & Approvals','_Internal','_Webhooks','_Admin')}
error_defs={'400':('VALIDATION_ERROR','Invalid request fields.'),'401':('AUTHENTICATION_REQUIRED','Authentication required.'),'403':('PERMISSION_DENIED','Capability or scope denied.'),'404':('RESOURCE_NOT_FOUND','Resource not found.'),'409':('VERSION_CONFLICT','Version changed or idempotency conflicts.'),'429':('RATE_LIMITED','Try again later.'),'503':('SERVICE_UNAVAILABLE','Required provider is unavailable.')}
for r in R:
 method,path=r['method'],r['path'];name=method+' '+path
 response=r['response'];schema=ref(response) if not response.startswith('List:') else obj({'items':arr(ref(response[5:])),'limit':{'type':'integer'},'offset':{'type':'integer'},'next_offset':{'type':'integer'}},('items',))
 if path.endswith('/auth/mfa/verify'):schema={'oneOf':[ref('Tokens'),obj({'verified':{'type':'boolean'}},('verified',),True)]}
 wrapped=obj({'data':schema},('data',))
 parameters=[]
 for param in re.findall(r'\{([^}]+)\}',path):parameters.append({'name':param,'in':'path','required':True,'schema':typ('ids.ID')})
 if response.startswith('List:') and response!='List:MFAMethod':
  parameters +=[{'name':'limit','in':'query','schema':{'type':'integer','minimum':1,'maximum':100,'default':50}},{'name':'offset','in':'query','schema':{'type':'integer','minimum':0,'maximum':100000,'default':0}}]
 if path in ('/api/v1/business/branches','/api/v1/business/regions','/api/v1/business/departments','/api/v1/business/teams','/api/v1/business/staff') and method=='GET':parameters.append({'name':'status','in':'query','schema':{'type':'string'}})
 if path=='/api/v1/business/approvals' and method=='GET':parameters.append({'name':'state','in':'query','schema':{'type':'string','enum':['DRAFT','PENDING','APPROVED','REJECTED','EXPIRED','CANCELLED']}})
 parameters.append({'name':'X-Request-ID','in':'header','required':False,'schema':{'type':'string','maxLength':128}})
 if r['idempotent']:parameters.append({'name':'Idempotency-Key','in':'header','required':True,'schema':{'type':'string','minLength':8,'maxLength':128}})
 desc='Authentication: '+('public account entry through authenticated Gateway' if r['public'] else 'business access JWT plus live account/session')+'. '
 desc+='Permission: '+(r['capability'] or 'own account / current membership')+'. Scope: trusted organisation and resolved resource hierarchy. '
 if r['step_up']:desc+='Recent MFA (under five minutes) required where configured; see operation-specific rules. '
 desc+=('Idempotency-Key is required; replay returns the original committed result, changed payload conflicts. ' if r['idempotent'] else 'Optimistic version, token single-use or documented provider key protects applicable retries. ')+r['notes']
 responses={str(r['status']):{'description':'Committed success','content':{'application/json':{'schema':wrapped,'example':{'data':example(schema)}}}}}
 for code,(err,msg) in error_defs.items():responses[code]={'description':msg,'content':{'application/json':{'schema':ref('Error'),'example':{'error':{'code':err,'message':msg,'request_id':'example-request'}}}}}
 op={'operationId':re.sub('[^a-zA-Z0-9]+','_',name).strip('_'),'summary':name,'description':desc,'x-permission':r['capability'] or 'self','x-step-up':r['step_up'],'x-idempotency-key':r['idempotent'],'parameters':parameters,'responses':responses}
 if r['public']:op['security']=[]
 body=None
 if r['request']:
  body=copy.deepcopy(EXAMPLES.get(r['request'],example(ref(r['request']))));
  if method=='POST' and path.endswith('/departments'):body['branch_id']=UID
  if method=='POST' and path.endswith('/teams'):body['department_id']=UID
  if method=='PATCH' and r['request']=='hierarchyCommand':body['version']=1
  op['requestBody']={'required':True,'content':{'application/json':{'schema':ref(r['request']),'example':body}}}
 O['paths'].setdefault(path,{})[method.lower()]=op;include(op)
 rawurl='{{base_url}}'+re.sub(r'\{([^}]+)\}',lambda m:'{{business_'+m[1]+'}}',path)
 headers=[{'key':'X-Request-ID','value':'{{$guid}}'},{'key':'Content-Type','value':'application/json'}]
 if r['idempotent']:headers.append({'key':'Idempotency-Key','value':'{{business_idempotency_key}}','description':'Set a new stable value per intended command; reuse only for exact retries.'})
 request={'method':method,'header':headers,'url':{'raw':rawurl,'host':['{{base_url}}'],'path':rawurl.split('}}',1)[1].strip('/').split('/')},'description':desc,'auth':({'type':'noauth'} if r['public'] else {'type':'bearer','bearer':[{'key':'token','value':'{{business_access_token}}','type':'string'}]})}
 if body is not None:
  body=copy.deepcopy(body)
  for k in list(body):
   if k in ('password','current_password','new_password','invitation_token','refresh_token','mfa_token','code'):body[k]='{{business_'+k+'}}'
  if 'email' in body:body['email']='{{business_email}}'
  def interpolate(value):
   if isinstance(value,dict):
    for k,v in value.items():
     if k.endswith('_id') and isinstance(v,str) and v==UID:
      variable={'scope_id':('business_organisation_id' if value.get('scope_kind')=='COMPANY' else 'business_branch_id'),'previous_verification_id':'business_verification_id','successor_membership_id':'business_successor_membership_id'}.get(k,'business_'+k)
      value[k]='{{'+variable+'}}'
     else:interpolate(v)
   elif isinstance(value,list):
    for child in value:interpolate(child)
  interpolate(body)
  request['body']={'mode':'raw','raw':json.dumps(body,indent=2),'options':{'raw':{'language':'json'}}}
 tests=[f'pm.test("Documented status", () => pm.expect([{r["status"]},400,401,403,404,409,429,503]).to.include(pm.response.code));','const body = pm.response.json();','pm.test("Consistent envelope", () => pm.expect(body).to.have.property(pm.response.code < 300 ? "data" : "error"));', 'if (pm.response.code < 300) { const d = body.data; const tokens = d.tokens || d; if (tokens.access_token) pm.environment.set("business_access_token", tokens.access_token); if (tokens.refresh_token) pm.environment.set("business_refresh_token", tokens.refresh_token); for (const k of ["account_id","organisation_id","session_id","device_id","method_id","role_id","staff_id","invitation_id","capacity_id","capability_pack_id","delegation_id","break_glass_id","mfa_token","verification_id","handover_id"]) if (d[k]) pm.environment.set("business_"+k,d[k]); if (d.handover_id) pm.environment.set("business_handover_id",d.handover_id); if (d.id) pm.environment.set("business_id",d.id); if (d.approval) pm.environment.set("business_approval_id",d.approval.approval_id || d.approval.id); if (d.policy) pm.environment.set("business_policy_id",d.policy.policy_id || d.policy.id); if (d.version) pm.environment.set("business_version",d.version); }']
 target=''
 for family,variable in [('roles','role_id'),('approval-policies','policy_id'),('approvals','approval_id'),('branches','branch_id'),('departments','department_id'),('teams','team_id'),('regions','region_id'),('devices','device_id'),('delegations','delegation_id'),('break-glass','break_glass_id'),('organisation/capacities','capacity_id'),('organisation/capability-packs','capability_pack_id')]:
  if path.startswith('/api/v1/business/'+family+'/'):target='business_'+variable
 if '/handover/{id}' in path:target='business_handover_id'
 prerequest=['if (!pm.environment.get("business_idempotency_key")) pm.environment.set("business_idempotency_key", pm.variables.replaceIn("{{$guid}}"));']
 if target:prerequest.append(f'if (pm.environment.get("{target}")) pm.variables.set("business_id",pm.environment.get("{target}"));')
 resource={'/regions':'region_id','/branches':'branch_id','/departments':'department_id','/teams':'team_id'}.get(path.removeprefix('/api/v1/business'))
 if resource and method=='POST':tests.append(f'if (pm.response.code===201) {{ pm.test("Created identifier",()=>pm.expect(body.data.id).to.match(/^[0-9a-f-]{{36}}$/)); pm.environment.set("business_{resource}",body.data.id); }}')
 if path.endswith('/auth/register'):tests.append('if (pm.response.code===201) pm.test("Draft organisation only",()=>{ pm.expect(body.data.organisation_state).to.eql("DRAFT"); pm.expect(body.data.account_id).to.be.a("string"); pm.expect(body.data.organisation_id).to.be.a("string"); });')
 if response=='ApprovalResult':tests.append('if (pm.response.code<300) pm.test("Approval identity and state",()=>{pm.expect(body.data.approval.id).to.be.a("string");pm.expect(["DRAFT","PENDING","APPROVED","REJECTED","CANCELLED","EXPIRED"]).to.include(body.data.approval.state);pm.environment.set("business_approval_version",body.data.version);});')
 if response=='Role':tests.append('if (pm.response.code<300 && body.data.role_id) pm.test("Role version",()=>pm.expect(body.data.version).to.be.above(0));')
 if path.endswith('/auth/login'):tests.append('if (pm.response.code===200) pm.test("MFA or access credentials",()=>{if(body.data.mfa_required) pm.expect(body.data.mfa_token).to.be.a("string");else pm.expect(body.data.tokens.access_token).to.be.a("string");});')
 if path.endswith('/staff') and method=='POST':tests.append('if(pm.response.code===201) pm.test("Invitation reference",()=>pm.expect(body.data.invitation_id).to.be.a("string"));')
 examples=[]
 for code,res in responses.items():examples.append({'name':res['description'],'originalRequest':copy.deepcopy(request),'status':res['description'],'code':int(code),'header':[{'key':'Content-Type','value':'application/json'}],'body':json.dumps(res['content']['application/json']['example'],indent=2)})
 folder='00 Auth & Security' if any(path.startswith('/api/v1/business/'+p) for p in ('auth','account','sessions','devices','memberships')) else ('02 Roles, Permissions & Approvals' if any('/'+p in path for p in ('roles','permissions','approval','delegations','break-glass','audit-events')) else '01 Organisation & Staff')
 folders[folder]['item'].append({'name':name,'request':request,'response':examples,'event':[{'listen':'prerequest','script':{'type':'text/javascript','exec':prerequest}},{'listen':'test','script':{'type':'text/javascript','exec':tests}}]})
O['components']['schemas']={k:S[k] for k in sorted(used)}
collection_path=ROOT/'postman/CosmicForge_RealEstate.postman_collection.json'
collection=json.loads(collection_path.read_text(encoding='utf-8-sig'))
folder=next(x for x in collection['item'] if x['name']=='03 Business Operations');folder['description']=O['info']['description'];folder['item']=list(folders.values())
for name in ('_Internal','_Webhooks','_Admin'):folders[name]['description']='No Section 3 frontend operations. Internal events are broker-only; provider operations are described in the owning service contracts. No public callback or administration bypass is exposed.'
existing={v['key'] for v in collection.setdefault('variable',[])}
for key,value in {'business_access_token':'','business_refresh_token':'','business_email':'','business_password':'','business_current_password':'','business_new_password':'','business_mfa_token':'','business_code':'','business_invitation_token':'','business_idempotency_key':'','business_id':'','business_session_id':'','business_organisation_id':'','business_role_id':'','business_approval_id':'','business_policy_id':'','business_version':''}.items():
 if key not in existing:collection['variable'].append({'key':key,'value':value,'type':'string'})
outputs={ROOT/'contracts/public/openapi/business-operations.yaml':json.dumps(O,indent=2)+'\n',collection_path:json.dumps(collection,indent=2,ensure_ascii=False)+'\n',SERVICE/'contracts/routes.json':json.dumps(R,indent=2)+'\n'}
changed=[]
for p,text in outputs.items():
 if not p.exists() or p.read_text(encoding='utf-8-sig')!=text:
  changed.append(str(p.relative_to(ROOT)))
  if '--check' not in sys.argv:p.parent.mkdir(parents=True,exist_ok=True);p.write_text(text,encoding='utf-8')
if '--check'in sys.argv and changed:raise SystemExit('Contract drift: '+', '.join(changed))
if '--validate' in sys.argv:
 from jsonschema import Draft202012Validator,RefResolver
 resolver=RefResolver.from_schema(O)
 for path,methods in O['paths'].items():
  for method,op in methods.items():
   if 'requestBody' in op:
    media=op['requestBody']['content']['application/json'];Draft202012Validator(media['schema'],resolver=resolver).validate(media['example'])
   for status,response in op['responses'].items():
    media=response['content']['application/json'];Draft202012Validator(media['schema'],resolver=resolver).validate(media['example'])
print(f'{len(R)} routes; {len(used)} typed schemas; '+('verified' if '--check'in sys.argv else 'generated'))
