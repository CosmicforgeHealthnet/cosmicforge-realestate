"""Generate Sections 3â€“7 OpenAPI/Postman together. No runtime secrets or network IO.
Run from repository root: python scripts/contracts/generate_business_operations.py
--check compares generated artifacts without changing them.
"""
import json,re,sys,copy,os,tempfile
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
SERVICE=ROOT/'services/business-operations-service'
def write_contract(path,text):
 path.parent.mkdir(parents=True,exist_ok=True)
 if not path.resolve().is_relative_to(ROOT.resolve()):raise RuntimeError('Contract output outside repository')
 temporary=None
 try:
  with tempfile.NamedTemporaryFile(mode='w',encoding='utf-8',newline='\n',dir=path.parent,prefix='.business-contract-',suffix='.tmp',delete=False) as output:
   temporary=Path(output.name);output.write(text)
  os.replace(temporary,path)
 finally:
  if temporary is not None:temporary.unlink(missing_ok=True)

UID='019a5fb0-0000-7000-8000-000000000001'
DATE='2026-10-30T12:00:00Z'
def ref(n):return {'$ref':'#/components/schemas/'+n}
def obj(props,required=(),closed=False):
 d={'type':'object','properties':props,'additionalProperties':not closed}
 if required:d['required']=list(required)
 return d
def arr(s):return {'type':'array','items':s}
def fields(**kw):return obj({k:typ(v) for k,v in kw.items()})
TYPE_NAMES={
 'reservations':{'Policy':'ReservationPolicy','Reservation':'Reservation','Room':'ReservationRoom','RoomCommand':'ReservationRoomCommand','GuestCommand':'ReservationGuestCommand','Create':'ReservationCreate','Detail':'ReservationDetail','Command':'ReservationMutation','SourceCommand':'ReservationSourceCommand','Acceptance':'ReservationAcceptance','FinancialGroupView':'ReservationFinancialGroupView'},
 'payment':{'Policy':'PaymentPolicy','Component':'PaymentComponent','Quote':'PaymentLockedQuote','IntentInput':'PaymentIntentInput','Intent':'PaymentIntent','Receipt':'PaymentReceipt'},
}
def typ(t,package=None):
 t=t.strip()
 if t.startswith('*'):return typ(t[1:],package)
 if t.startswith('[]'):return arr(typ(t[2:],package))
 if t in ('ids.ID',):return {'type':'string','format':'uuid','pattern':'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$','example':UID}
 if t=='time.Time':return {'type':'string','format':'date-time','example':DATE}
 if t=='bool':return {'type':'boolean'}
 if t=='float64':return {'type':'number'}
 if t in ('int','int32','int64','uint64'):return {'type':'integer','format':'int64'}
 if t in ('string','permissions.Permission','State','organisation.State','Protocol','Kind','portfolio.Kind'):return {'type':'string'}
 if t in ('json.RawMessage','any') or t.startswith('map['):return {'type':'object','additionalProperties':True}
 if '.' in t:
  namespace,name=t.split('.',1)
  return ref(TYPE_NAMES.get(namespace,{}).get(name,name))
 if package in TYPE_NAMES:return ref(TYPE_NAMES[package].get(t,t))
 return ref(t.split('.')[-1])
S={}
for p in (SERVICE/'internal').rglob('*.go'):
 if '_test.go' in p.name or 'repositories' in p.parts:continue
 text=p.read_text(encoding='utf-8')
 package=re.search(r'^package (\w+)',text,re.M)[1]
 for m in re.finditer(r'type (\w+) struct \{([^}]+)\}',text,re.S):
  props={};required=[]
  for fm in re.finditer(r'\w+\s+([^\n`]+)\s+`json:"([^",]+)([^"`]*)"`',m[2]):
   t,name,opt=fm.groups()
   if name=='-':continue
   props[name]=typ(t,package)
   if 'omitempty' not in opt:required.append(name)
  if props:S[TYPE_NAMES.get(package,{}).get(m[1],m[1])]=obj(props,required,True)
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
route('GET','/break-glass',res='List:Emergency',notes='Lists the authenticated memberâ€™s grants; independent reviewers use the audited grant reference.')
route('POST','/break-glass','break_glass:request','emergencyCommand','Emergency',201,True,True,notes='Designated senior role; 20-character reason; branch/department/team only; hierarchy:read or staff:read; 30â€“900 seconds. Exact independent BREAK_GLASS_ACCESS approval after activation; every use audited; explicit denies and tenant boundaries remain enforced.')
route('DELETE','/break-glass/{id}','break_glass:request','Reason','BooleanResult',200,True,True)
route('POST','/break-glass/{id}/review','break_glass:review','Reason','BooleanResult',200,True,True,notes='Independent reviewer only; immutable once completed; revokes access immediately.')
route('GET','/audit-events','audit:read',res='List:Audit',mfa=True)
# Section 4 canonical inventory graph; actual handler registrations are drift-tested.
node_kinds={'portfolios':('PORTFOLIO','portfolio'),'developments':('DEVELOPMENT','development'),'development-phases':('DEVELOPMENT_PHASE','development'),'parcels':('PARCEL','parcel'),'properties':('PROPERTY','property'),'buildings':('BUILDING','structure'),'blocks':('BLOCK','structure'),'floors':('FLOOR','structure'),'units':('UNIT','structure'),'room-types':('ROOM_TYPE','structure'),'rooms':('ROOM','structure'),'facilities':('FACILITY','structure'),'common-areas':('COMMON_AREA','structure'),'assets':('ASSET','assets')}
S['Geometry']=obj({'type':{'type':'string','enum':['Polygon','MultiPolygon']},'coordinates':{'type':'array','items':{}}},('type','coordinates'),True)
S['Attributes']=S.get('Attributes',{})
for name in ('InventoryCreate','InventoryImportRow','InventoryImportInput','SpaceGraphCommand'):
 if name not in S:raise RuntimeError('missing Section 4 input '+name)
S['InventoryCreate']['required']=['code','name','attributes']
S['InventoryPatch']['required']=['version','reason']
S['InventoryMove']['required']=['version','tree_version','reason','approval_id']
S['InventoryLifecycle']['required']=['version','tree_version','state','reason']
S['InventoryMembership']['required']=['version','tree_version','parent_id','parent_version','relation','remove','reason','approval_id']
S['InventoryMapping']['required']=['provider','external_id','source_reference','reason']
S['InventoryVerification']['required']=['version']
S['InventoryMedia']['required']=['version','media_id','purpose','reason']
S['ImportControl']['required']=['version','reason']
S['Node']['properties']['kind']={'type':'string','enum':[x[0] for x in node_kinds.values()]}
S['InventoryImportRow']['properties']['kind']=S['Node']['properties']['kind']
S['InventoryHierarchy']=obj({'root_id':typ('ids.ID'),'items':arr(ref('Node')),'active_counts_on_page':{'type':'object','additionalProperties':{'type':'integer'}},'limit':{'type':'integer'},'offset':{'type':'integer'}},('root_id','items'))
S['InventoryHistory']=obj({'id':typ('ids.ID'),'actor_id':typ('ids.ID'),'operation':{'type':'string'},'before':{'type':['object','null']},'after':ref('Node'),'created_at':typ('time.Time'),'correlation_id':{'type':'string'}})
S['MappingResult']=fields(object_type='string',mapping_id='ids.ID',node_id='ids.ID',provider='string',external_id='string',source_reference='string',version='int64')
S['MediaLink']=fields(media_link_id='ids.ID',media_id='ids.ID',purpose='string',provider_status='string',access_classification='string',checked_at='time.Time',version='int64')
S['SpaceGraphResult']=fields(space_graph_id='ids.ID',media_link_id='ids.ID',coordinate_system='string',spaces='[]InventorySpace',version='int64',created_at='time.Time',confirmed_by='ids.ID')
S['ImportJob']=obj({'import_id':typ('ids.ID'),'provider':{'type':'string'},'format':{'type':'string'},'branch_id':typ('ids.ID'),'status':{'type':'string'},'record_count':{'type':'integer'},'checkpoint':{'type':'integer'},'version':{'type':'integer'},'outcomes':{'type':'object','additionalProperties':{'type':'integer'}},'last_error':{'type':'string'},'created_at':typ('time.Time'),'updated_at':typ('time.Time')})
S['ImportRecordResult']=fields(kind='string',code='string',parent_key='string',parent_id='ids.ID',portfolio_ids='[]ids.ID',row_number='int',source_key='string',node_id='ids.ID',action='string',status='string',validation_errors='[]string',error_code='string',applied_version='int64')
S['ImportRecordResult']['properties']['applied_version']['type']=['integer','null']
S['ImportRecovery']=obj({'import_id':typ('ids.ID'),'items':arr({'type':'object','additionalProperties':True}),'policy':{'type':'string'},'limit':{'type':'integer'},'offset':{'type':'integer'}})
S['RecoveryResult']=fields(import_id='ids.ID',recovery_state='string',restored='int',reconciliation_tasks='int',remaining='int',version='int64')
for path,(kind,cap) in node_kinds.items():
 route('GET','/'+path,cap+':read',res='List:Node',notes='Bounded deterministic pagination; branch_id, state and q filters. Only current authorised resource scopes are returned.')
 route('POST','/'+path,cap+':write','InventoryCreate','Node',201,True,notes='Creates a DRAFT canonical identity; no legal authority or availability is inferred. Physical parents inherit context; portfolio memberships are distinct. Non-root types require a physical parent; rooms require a same-property room_type_id.')
 route('GET','/'+path+'/{id}',cap+':read',res='Node')
 route('PATCH','/'+path+'/{id}',cap+':write','InventoryPatch','Node',200,True,notes='Versioned metadata update. Previously activated structural changes require the controlled move workflow and downstream dependency evidence.')
for path in ('units','room-types','rooms','facilities'):
 route('GET','/properties/{id}/'+path,'structure:read',res='List:Node')
 route('POST','/properties/{id}/'+path,'structure:write','InventoryCreate','Node',201,True,notes='Explicit parent_id may identify a physical descendant of this property; cross-property attachment is rejected.')
route('GET','/developments/{id}/phases','development:read',res='List:Node')
route('POST','/developments/{id}/phases','development:write','InventoryCreate','Node',201,True)
route('GET','/inventory-nodes/{id}/hierarchy','structure:read',res='InventoryHierarchy')
S['InventorySummary']=obj({'root_id':typ('ids.ID'),'tree_version':typ('int64'),'visible_count':typ('int'),'counts_by_kind':{'type':'object','additionalProperties':typ('int')},'active_counts_by_kind':{'type':'object','additionalProperties':typ('int')},'counts_by_state':{'type':'object','additionalProperties':typ('int')}})
route('GET','/inventory-nodes/{id}/summary','structure:read',res='InventorySummary',notes='Counts only currently authorised resources, including collection membership. Bounded to 10000 nodes; larger migrations require operational review.')
route('GET','/inventory-nodes/{id}/history','structure:read',res='List:InventoryHistory')
route('POST','/inventory-nodes/{id}/move','inventory:restructure','InventoryMove','Node',200,True,True,notes='Exact independent INVENTORY_RESTRUCTURE approval, source and destination scope, snapshot versions and dependency safety. Approval operation is {node_id,command:<body without approval_id>}. Never-active draft subtrees need no external dependency assertion; prior-active structures fail closed without a complete current assertion.')
route('POST','/inventory-nodes/{id}/lifecycle','inventory:restructure','InventoryLifecycle','Node',200,True,True,notes='Activation requires current KYB, supported verified capacity and applicable property authority. Destructive prior-active transitions require exact approval and dependency assertions. Draft archival preserves canonical IDs; cascade must be explicit.')
route('POST','/inventory-nodes/{id}/relationships','inventory:restructure','InventoryMembership','Node',200,True,True,notes='Typed MEMBERSHIP or ASSOCIATES only; CONTAINS changes use move. Independent approval binds the exact node/command; group membership never replaces a physical parent. Optional previous_parent_id + previous_parent_version atomically transfers a collection membership; both portfolio scopes and subtree revisions are required.')
route('GET','/inventory-nodes/{id}/mappings','structure:read',res='List:MappingResult')
route('POST','/inventory-nodes/{id}/mappings','inventory:mappings','InventoryMapping','MappingResult',200,True,True,notes='Provider+object_type+external_id is tenant-unique. Remapping requires expected_node_id, mapping version, node_version/tree_version and previous_node_version/previous_tree_version plus exact INVENTORY_MAPPING_CHANGE approval, both resource scopes and applicable dependency assertions.')
route('GET','/inventory-nodes/{id}/verification','structure:read',res='VerificationProjection')
route('POST','/inventory-nodes/{id}/verification','inventory:verify','InventoryVerification','VerificationProjection',200,True,True,notes='Authenticated owning Verification case read-back. Property/parcel subject and domain must match. create_case uses a stable provider key; stale projection cannot authorize a newer structural version.')
route('GET','/inventory-nodes/{id}/media','structure:read',res='List:MediaLink')
route('POST','/inventory-nodes/{id}/media','inventory:media','InventoryMedia','MediaLink',200,True,True,notes='Authenticated Media metadata and explicit owner validation. Stores references/status only, no bytes. Supported purposes PHOTO, DOCUMENT, SURVEY, FLOOR_PLAN, DIGITAL_TWIN, WARRANTY.')
route('POST','/inventory-nodes/{id}/media/{link_id}/access','inventory:media',res='Access',mfa=True,notes='Fresh metadata read plus owning Media purpose-bound HTTPS URL; no URL or document bytes are stored; audited, no-store response.')
route('GET','/inventory-nodes/{id}/space-graphs','structure:read',res='List:SpaceGraphResult')
route('POST','/inventory-nodes/{id}/space-graphs','inventory:media','SpaceGraphCommand','SpaceGraphResult',201,True,True,notes='Operator-confirmed coordinates reference existing same-property canonical space IDs and a fresh READY floor-plan/digital-twin media link. No 3D generation or competing property graph.')
route('POST','/imports','imports:write','InventoryImportInput','ImportJob',202,True,True,notes='One MiB bounded structured JSON/CSV, maximum 1000 rows. No external URL/file fetching. Async preview does not mutate inventory; stable source keys, typed dependencies and exact provider IDs determine identity.')
route('GET','/imports','imports:read',res='List:ImportJob')
route('GET','/imports/{id}','imports:read',res='ImportJob')
route('GET','/imports/{id}/preview','imports:read',res='ImportJob')
route('GET','/imports/{id}/records','imports:read',res='List:ImportRecordResult')
for action in ('commit','pause','resume','cancel'):route('POST','/imports/{id}/'+action,'imports:write','ImportControl','ImportJob',200,True,True,notes='Current job version and reason; rechecks current membership/session/scopes. Partial execution requires explicit acceptance of validation errors; unexpected execution failures pause until partial continuation is reviewed. Resume uses the current authenticated initiator and durable checkpoint.')
route('GET','/imports/{id}/recovery-plan','imports:read',res='ImportRecovery')
route('POST','/imports/{id}/rollback','imports:write','ImportControl','RecoveryResult',200,True,True,notes='At most 20 reverse-order recovery actions per command. Repeat with new key/current version for further batches. Only unchanged never-active draft identities are restored/soft-archived. Changed versions, authority loss or acquired dependencies produce reconciliation tasks; mappings retain traceability, never delete valid data.')
# Request examples are syntactically representative, not seeded demo records.
from business_commercial_routes import register_commercial, commercial_examples,commercial_queries
register_commercial(ROOT,S,route,obj,arr,ref,typ)
from business_operational_routes import register_operations,operational_examples,operational_queries
register_operations(ROOT,S,route,obj,arr,ref,typ)

def example(schema,depth=0):
 if depth>20:return {}
 if "example" in schema:return copy.deepcopy(schema["example"])
 if "enum" in schema:return copy.deepcopy(schema["enum"][0])
 if 'const'in schema:return copy.deepcopy(schema['const'])
 if 'oneOf'in schema:return example(schema['oneOf'][0],depth+1)
 if 'anyOf'in schema:return example(schema['anyOf'][0],depth+1)
 if '$ref'in schema:return example(S[schema['$ref'].split('/')[-1]],depth+1)
 t=schema.get('type','object');t=t[0] if isinstance(t,list) else t
 if t=='object':return {k:example(v,depth+1) for k,v in schema.get('properties',{}).items() if k in schema.get('required',schema.get('properties',{}))}
 if t=='array':return [example(schema['items'],depth+1)]
 if t=='boolean':return True
 if t=='integer':return max(1,schema.get('minimum',1))
 if t=='number':return 1.0
 return schema.get('example',schema.get('enum',['example'])[0])
S['Node']['example']={'id':UID,'organisation_id':UID,'branch_id':UID,'tree_version':1,'parents':[],'kind':'PROPERTY','non_sellable':False,'code':'PROPERTY-001','name':'Example Property','state':'DRAFT','timezone':'Africa/Lagos','currency':'NGN','policy_code':'STANDARD','policy_version':1,'attributes':{'classification':'RESIDENTIAL_BUILDING'},'version':1,'created_at':DATE,'updated_at':DATE,'created_by':UID,'updated_by':UID,'verification':{'status':'UNVERIFIED'}}
S['Attributes']['example']={'classification':'RESIDENTIAL_BUILDING'}
S['Access']['example']={'url':'https://media.example.test/temporary-authorised-reference','expires_at':DATE}
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
EXAMPLES.update({
 'InventoryCreate':{'branch_id':UID,'code':'PROPERTY-001','name':'Example Property','attributes':{'classification':'RESIDENTIAL_BUILDING','address':'Example business address'}},
 'InventoryPatch':{'version':1,'name':'Revised property name','reason':'Approved metadata correction'},
 'InventoryMove':{'version':1,'tree_version':1,'parent_version':1,'parent_id':UID,'detach':False,'reason':'Reviewed physical hierarchy correction','approval_id':UID},
 'InventoryLifecycle':{'version':1,'tree_version':1,'state':'ACTIVE','cascade':False,'reason':'Verified operating prerequisites satisfied'},
 'InventoryMembership':{'version':1,'tree_version':1,'parent_version':1,'parent_id':UID,'relation':'MEMBERSHIP','remove':False,'reason':'Reviewed portfolio association','approval_id':UID},
 'InventoryMapping':{'provider':'legacy-pms','external_id':'PROPERTY-42','source_reference':'approved-import-2026','reason':'Preserve legacy canonical identity'},
 'InventoryVerification':{'version':1,'verification_id':UID,'create_case':False},
 'InventoryMedia':{'version':1,'media_id':UID,'purpose':'FLOOR_PLAN','reason':'Link approved physical floor plan'},
 'SpaceGraphCommand':{'version':1,'media_link_id':UID,'coordinate_system':'LOCAL_METRES','spaces':[{'node_id':UID,'label':'Confirmed physical room','points':[[0,0,0],[5,0,0],[5,4,0],[0,4,0]]}],'confirmed':True,'reason':'Operator confirms source-space correspondence'},
 'InventoryImportInput':{'branch_id':UID,'provider':'legacy-pms','format':'JSON','records':[{'source_key':'property-42','kind':'PROPERTY','create':{'code':'PROP-42','name':'Imported Property','attributes':{'classification':'HOTEL'}}}]},
 'ImportControl':{'version':1,'allow_partial':False,'reason':'Reviewed migration lifecycle operation'},
})
EXAMPLES.update({
 'CommercialPoolCreate':{'property_id':UID,'node_id':UID,'name':'Verified apartment nightly stock','member_ids':[UID]},
 'CommercialProductCreate':{'property_id':UID,'node_id':UID,'pool_id':UID,'rate_plan_id':UID,'name':'Apartment nights','min_guests':1,'max_guests':2,'policy_code':'STANDARD','policy_version':1,'channels':[]},
 'CommercialQuoteCommand':{'product_id':UID,'date_from':'2026-12-01','date_to':'2026-12-03','adults':2,'children':0,'quantity':1,'channel':''},
 'CommercialStateChange':{'version':1,'state':'ACTIVE','reason':'Verified prerequisites independently reviewed'},
 'CommercialChannelCreate':{'property_id':UID,'provider_code':'COSMICFORGE_MARKETPLACE','provider_account_id':'operator-configured-provider-account','reason':'Configure a signed supply integration'},
 'CommercialMappingPreview':{'version':1,'entries':[{'kind':'PROPERTY','external_id':'external-property-42','canonical_id':UID,'canonical_version':1,'expected_mapping_version':0}],'reason':'Inspect actual provider object ownership before confirmation'},
 'CommercialConflictDecision':{'version':1,'action':'RECONCILE','reason':'Read back the immutable provider command after investigation','approval_id':UID},
 'CommercialFieldAuthority':{'field':'title','mode':'MANUAL_OVERRIDE','value':'Reviewed channel title','source_reference':'operator:review-42','expires_at':'2026-11-15T12:00:00Z','version':0,'reason':'Apply independently reviewed channel text','approval_id':UID},
})
EXAMPLES.update(commercial_examples(UID))
EXAMPLES.update(operational_examples(UID))
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
O={'openapi':'3.1.0','info':{'title':'CosmicForge Business Operations â€” Sections 3â€“7','version':'1.4.0','description':'Canonical public prefix /api/v1/business. The explicit legacy /api/v1/business-operations alias has identical authentication and handlers; it is not a second API. All traffic crosses the Gateway; direct service calls require its signed internal identity. Trusted tenant comes from live session and membership, never an X-Organisation-ID override. Provider-specific assertion paths return 503 when unavailable. No SSO provider or downstream money movement is claimed.'},'servers':[{'url':'{base_url}','variables':{'base_url':{'default':'http://localhost:8080'}}}],'security':[{'BusinessBearer':[]}],'paths':{},'components':{'securitySchemes':{'BusinessBearer':{'type':'http','scheme':'bearer','bearerFormat':'Ed25519 JWT'}},'schemas':{}}}
folders={n:{'name':n,'item':[]} for n in ('00 Auth & Security','01 Organisation & Staff','02 Roles, Permissions & Approvals','03 Portfolio & Property Hierarchy','05 Listings & Content','06 Inventory & Availability','07 Rate Plans, Restrictions & Promotions','08 Channel Manager','09 Revenue Management','10 Reservations','11 Front Desk','12 Folio, Cashier & Night Audit','13 Guest CRM & Marketing','_Internal','_Webhooks','_Admin')}
error_defs={'400':('VALIDATION_ERROR','Invalid request fields.'),'401':('AUTHENTICATION_REQUIRED','Authentication required.'),'403':('PERMISSION_DENIED','Capability or scope denied.'),'404':('RESOURCE_NOT_FOUND','Resource not found.'),'409':('VERSION_CONFLICT','Version changed or idempotency conflicts.'),'429':('RATE_LIMITED','Try again later.'),'503':('SERVICE_UNAVAILABLE','Required provider is unavailable.')}
for r in R:
 method,path=r['method'],r['path'];name=method+' '+path
 collection=next((part for part in reversed(path.split('/')) if part in node_kinds or part=='phases'),None)
 if collection=='phases':collection='development-phases'
 graph_kind=node_kinds[collection][0] if collection else None
 response=r['response'];schema=ref(response) if not response.startswith('List:') else obj({'items':arr(ref(response[5:])),'limit':{'type':'integer'},'offset':{'type':'integer'},'next_offset':{'type':'integer'}},('items',))
 if path.endswith('/auth/mfa/verify'):schema={'oneOf':[ref('Tokens'),obj({'verified':{'type':'boolean'}},('verified',),True)]}
 wrapped=obj({'data':schema},('data',))
 parameters=[]
 for param in re.findall(r'\{([^}]+)\}',path):parameters.append({'name':param,'in':'path','required':True,'schema':typ('ids.ID')})
 if response.startswith('List:') and response!='List:MFAMethod':
  parameters +=[{'name':'limit','in':'query','schema':{'type':'integer','minimum':1,'maximum':100,'default':50}},{'name':'offset','in':'query','schema':{'type':'integer','minimum':0,'maximum':100000,'default':0}}]
 if path in ('/api/v1/business/branches','/api/v1/business/regions','/api/v1/business/departments','/api/v1/business/teams','/api/v1/business/staff') and method=='GET':parameters.append({'name':'status','in':'query','schema':{'type':'string'}})
 if path=='/api/v1/business/approvals' and method=='GET':parameters.append({'name':'state','in':'query','schema':{'type':'string','enum':['DRAFT','PENDING','APPROVED','REJECTED','EXPIRED','CANCELLED']}})
 if method=='GET' and path.removeprefix('/api/v1/business/').split('/')[0] in node_kinds and response=='List:Node':
  parameters += [{'name':'branch_id','in':'query','schema':typ('ids.ID')},{'name':'state','in':'query','schema':{'type':'string','enum':['DRAFT','ACTIVE','INACTIVE','RETIRED','ARCHIVED']}},{'name':'q','in':'query','schema':{'type':'string','maxLength':200}}]
 parameters.append({'name':'X-Request-ID','in':'header','required':False,'schema':{'type':'string','maxLength':128}})
 for key,kind,required in commercial_queries(r.get('handler',''))+operational_queries(r.get('handler','')):
  querySchema={'type':'integer','minimum':0} if kind=='integer' else typ('ids.ID') if kind=='uuid' else {'type':'string','format':'date'} if kind=='date' else {'type':'string'}
  parameters.append({'name':key,'in':'query','required':required,'schema':querySchema})
 if r['idempotent']:parameters.append({'name':'Idempotency-Key','in':'header','required':True,'schema':{'type':'string','minLength':8,'maxLength':128}})
 desc='Authentication: '+('public account entry through authenticated Gateway' if r['public'] else 'business access JWT plus live account/session')+'. '
 desc+='Permission: '+(r['capability'] or 'own account / current membership')+'. Scope: trusted organisation and resolved resource hierarchy. '
 if r['step_up']:desc+='Recent MFA (under five minutes) required where configured; see operation-specific rules. '
 desc+=('Idempotency-Key is required; replay returns the original committed result, changed payload conflicts. ' if r['idempotent'] else 'Optimistic version, token single-use or documented provider key protects applicable retries. ')+r['notes']
 response_example=example(schema)
 if graph_kind and response in ('Node','List:Node'):
  item=copy.deepcopy(S['Node']['example']);item.update({'kind':graph_kind,'non_sellable':graph_kind=='COMMON_AREA','code':graph_kind+'-001','name':'Example '+graph_kind.replace('_',' ').title(),'attributes':{'classification':{'UNIT':'APARTMENT','COMMON_AREA':'NON_SELLABLE','ASSET':'AIR_CONDITIONER','PROPERTY':'MIXED_USE'}.get(graph_kind,graph_kind)}})
  if graph_kind not in ('PORTFOLIO','DEVELOPMENT','PARCEL','PROPERTY'):item['parents']=[{'edge_id':UID,'parent_id':UID,'relation':'CONTAINS'}]
  if graph_kind=='ROOM':item['room_type_id']=UID
  response_example=item if response=='Node' else {'items':[item],'limit':50,'offset':0}
 responses={str(r['status']):{'description':'Committed success','content':{'application/json':{'schema':wrapped,'example':{'data':response_example}}}}}
 for success_status in r.get('success_statuses',[]):responses[str(success_status)]=copy.deepcopy(responses[str(r['status'])]);responses[str(success_status)]['description']='Committed new version'
 for code,(err,msg) in error_defs.items():responses[code]={'description':msg,'content':{'application/json':{'schema':ref('Error'),'example':{'error':{'code':err,'message':msg,'request_id':'example-request'}}}}}
 op={'operationId':re.sub('[^a-zA-Z0-9]+','_',name).strip('_'),'summary':name,'description':desc,'x-permission':r['capability'] or 'self','x-step-up':r['step_up'],'x-idempotency-key':r['idempotent'],'parameters':parameters,'responses':responses}
 if r['public']:op['security']=[]
 if r.get('transport')=='internal':
  O['components']['securitySchemes']['ServiceBearer']={'type':'http','scheme':'bearer','bearerFormat':'Short-lived signed service JWT'}
  op['security']=[{'ServiceBearer':[]}];op['description']='Private service network only. '+r['capability']+'. Signed organisation delegation must match the owning connection. '+r['notes']
 if r.get('transport')=='webhook':
  O['components']['securitySchemes']['SupplyCallbackSignature']={'type':'apiKey','in':'header','name':'X-CF-Signature','description':'v1=HMAC-SHA256(timestamp + newline + canonical request path + newline + raw body), per-connection 32-byte key.'}
  op['security']=[{'SupplyCallbackSignature':[]}];op['description']='Provider callback via the configured Gateway. Per-connection signature and timestamp, certified capability and durable event/object-version dedupe required. '+r['notes'];parameters.extend([{'name':'X-CF-Timestamp','in':'header','required':True,'schema':{'type':'string','description':'Unix seconds; at most five minutes old or one minute ahead.'}}])

 body=None
 if r['request']:
  body=copy.deepcopy(EXAMPLES.get(r['request'],example(ref(r['request']))));
  if r['request']=='InventoryCreate' and graph_kind:
   body={'code':graph_kind+'-001','name':'Example '+graph_kind.replace('_',' ').title(),'attributes':{'classification':{'UNIT':'APARTMENT','COMMON_AREA':'NON_SELLABLE','PROPERTY':'MIXED_USE','ASSET':'AIR_CONDITIONER'}.get(graph_kind,graph_kind)}}
   if graph_kind in ('PORTFOLIO','DEVELOPMENT','PARCEL','PROPERTY'):body['branch_id']=UID
   else:body['parent_id']=UID
   if graph_kind=='ROOM':body['room_type_id']=UID
   if graph_kind=='FLOOR':body['attributes']={'floor_number':1}
   if graph_kind=='PARCEL':body['attributes'].update({'area_square_metres':'1250.25','geometry':{'type':'Polygon','coordinates':[[[3,6],[4,6],[4,7],[3,7],[3,6]]]}})
  if method=='POST' and path.endswith('/departments'):body['branch_id']=UID
  if method=='POST' and path.endswith('/teams'):body['department_id']=UID
  if method=='PATCH' and r['request']=='hierarchyCommand':body['version']=1
  if r.get('handler') in ('templatePatch','listingPatch'):body['version']=1
  if r.get('handler')=='complianceHoldRevoke':body['approval_id']=UID
  if r.get('handler')=='restrictionPatch':body.update({'state':'REVOKED','approval_id':UID})
  if r.get('handler')=='channelPatch':body['state']='SUSPENDED'
  if r.get('handler') in ('promotionPatch','addOnPatch','packagePatch'):body['approval_id']=UID
  op['requestBody']={'required':True,'content':{'application/json':{'schema':ref(r['request']),'example':body}}}
 O['paths'].setdefault(path,{})[method.lower()]=op;include(op)
 rawurl='{{base_url}}'+re.sub(r'\{([^}]+)\}',lambda m:'{{business_'+m[1]+'}}',path)
 headers=[{'key':'X-Request-ID','value':'{{$guid}}'},{'key':'Content-Type','value':'application/json'}]
 if r['idempotent']:headers.append({'key':'Idempotency-Key','value':'{{business_idempotency_key}}','description':'Set a new stable value per intended command; reuse only for exact retries.'})
 request={'method':method,'header':headers,'url':{'raw':rawurl,'host':['{{base_url}}'],'path':rawurl.split('}}',1)[1].strip('/').split('/')},'description':desc,'auth':({'type':'noauth'} if r['public'] else {'type':'bearer','bearer':[{'key':'token','value':'{{business_access_token}}','type':'string'}]})}
 if r.get('transport')=='internal':
  rawurl=rawurl.replace('{{base_url}}','{{business_internal_url}}');request['url']['raw']=rawurl;request['url']['host']=['{{business_internal_url}}'];request['auth']={'type':'bearer','bearer':[{'key':'token','value':'{{business_service_token}}','type':'string'}]}
 if r.get('transport')=='webhook':
  request['auth']={'type':'noauth'};headers.extend([{'key':'X-CF-Timestamp','value':'{{business_callback_timestamp}}'},{'key':'X-CF-Signature','value':'{{business_callback_signature}}'}])
 query=[]
 for parameter in parameters:
  if parameter['in']=='query':
   key=parameter['name'];value=('2027-01-02' if key=='date_from' else '2027-01-04' if key=='date_to' else 'NGN' if key=='currency' else '{{business_'+key+'}}')
   query.append({'key':key,'value':value,'disabled':not parameter.get('required',False)})
 if query:
  request['url']['query']=query;request['url']['raw']+=('?' if any(not q['disabled'] for q in query) else '')+ '&'.join(q['key']+'='+q['value'] for q in query if not q['disabled'])
 if body is not None:
  body=copy.deepcopy(body)
  for k in list(body):
   if k in ('password','current_password','new_password','invitation_token','refresh_token','mfa_token','key_base64') or (k=='code' and r['request'] in ('Code','MFAVerify')):body[k]='{{business_'+k+'}}'
  if 'email' in body:body['email']='{{business_payer_email}}' if r['request']=='ReservationCollectionCommand' else '{{business_email}}'
  def interpolate(value):
   if isinstance(value,dict):
    for k,v in value.items():
     if k.endswith('_id') and isinstance(v,str) and v==UID:
      variable={'scope_id':('business_organisation_id' if value.get('scope_kind')=='COMPANY' else 'business_branch_id'),'previous_verification_id':'business_verification_id','successor_membership_id':'business_successor_membership_id'}.get(k,'business_'+k)
      if k=='policy_id' and r['request'] in ('ReservationOverbookingQuote','ReservationOverbookingCommand'):variable='business_overbooking_policy_id'
      value[k]='{{'+variable+'}}'
     else:interpolate(v)
   elif isinstance(value,list):
    for child in value:interpolate(child)
  interpolate(body)
  if r['request']=='InventoryCreate' and graph_kind and 'parent_id' in body:body['parent_id']='{{business_'+{'DEVELOPMENT_PHASE':'development','BLOCK':'building','FLOOR':'block','UNIT':'floor','ROOM':'floor','ASSET':'room'}.get(graph_kind,'property')+'_id}}'
  request['body']={'mode':'raw','raw':json.dumps(body,indent=2),'options':{'raw':{'language':'json'}}}
 success_statuses=sorted(set([r['status']]+r.get('success_statuses',[])))
 tests=[f'pm.test("Documented status", () => pm.expect([{",".join(map(str,success_statuses))},400,401,403,404,409,429,503]).to.include(pm.response.code));','const body = pm.response.json();','pm.test("Consistent envelope", () => pm.expect(body).to.have.property(pm.response.code < 300 ? "data" : "error"));', 'if (pm.response.code < 300) { const d = body.data; const tokens = d.tokens || d; if (tokens.access_token) pm.environment.set("business_access_token", tokens.access_token); if (tokens.refresh_token) pm.environment.set("business_refresh_token", tokens.refresh_token); for (const k of ["account_id","organisation_id","session_id","device_id","method_id","role_id","staff_id","invitation_id","capacity_id","capability_pack_id","delegation_id","break_glass_id","mfa_token","verification_id","handover_id","import_id","mapping_id","media_link_id","space_graph_id"]) if (d[k]) pm.environment.set("business_"+k,d[k]); if (d.handover_id) pm.environment.set("business_handover_id",d.handover_id); if (d.id) pm.environment.set("business_id",d.id); if (d.approval) pm.environment.set("business_approval_id",d.approval.approval_id || d.approval.id); if (d.policy) pm.environment.set("business_policy_id",d.policy.policy_id || d.policy.id); if (d.version) pm.environment.set("business_version",d.version); }']
 target=''
 for family,variable in [('roles','role_id'),('approval-policies','policy_id'),('approvals','approval_id'),('branches','branch_id'),('departments','department_id'),('teams','team_id'),('regions','region_id'),('devices','device_id'),('delegations','delegation_id'),('break-glass','break_glass_id'),('organisation/capacities','capacity_id'),('organisation/capability-packs','capability_pack_id')]:
  if path.startswith('/api/v1/business/'+family+'/'):target='business_'+variable
 if graph_kind and '{id}' in path:
  target='business_'+('property_id' if '/properties/{id}/' in path else 'development_id' if '/developments/{id}/' in path else graph_kind.lower()+'_id')
 if '/inventory-nodes/{id}' in path:target='business_node_id'
 if '/revenue/demand-snapshots/{id}' in path:target='business_snapshot_id'
 if '/revenue/forecasts/{id}' in path:target='business_forecast_id'
 if '/revenue/policies/{id}' in path:target='business_rms_policy_id'
 if '/revenue/recommendations/{id}' in path:target='business_recommendation_id'
 if '/imports/{id}' in path:target='business_import_id'
 if '/reservations/{id}' in path:target='business_reservation_id'
 if '/waitlist/{id}' in path:target='business_waitlist_id'
 if '/handover/{id}' in path:target='business_handover_id'
 prerequest=['if (!pm.environment.get("business_idempotency_key")) pm.environment.set("business_idempotency_key", pm.variables.replaceIn("{{$guid}}"));']
 if target:prerequest.append(f'if (pm.environment.get("{target}")) pm.variables.set("business_id",pm.environment.get("{target}"));')
 resource={**{('/'+k):v[0].lower()+'_id' for k,v in node_kinds.items()},'/regions':'region_id','/branches':'branch_id','/departments':'department_id','/teams':'team_id'}.get(path.removeprefix('/api/v1/business'))
 if graph_kind and r['request']=='InventoryCreate':resource=graph_kind.lower()+'_id'
 commercial_capture={'Pool':'pool_id','Product':'product_id','CommercialChannelConnection':'connection_id','CommercialBulkView':'bulk_preview_id','CommercialListing':'listing_id','ReservationPolicyView':'reservation_policy_id','ReservationOverbookingPolicy':'overbooking_policy_id','RMSDemandSnapshot':'demand_snapshot_id','RMSForecastRun':'forecast_id','RMSPolicy':'rms_policy_id','RMSRecommendation':'recommendation_id','RMSSimulationRecord':'simulation_id','ReservationWaitlist':'waitlist_id','ReservationWaitlistOffer':'waitlist_offer_id','Promotion':'promotion_id','AddOn':'add_on_id','Package':'package_id'}
 if response in commercial_capture:tests.append(f'if(pm.response.code<300) pm.environment.set("business_{commercial_capture[response]}",body.data.id);')
 if response=='RMSRecommendation':tests.append('if(pm.response.code<300){pm.environment.set("business_change_digest",body.data.change_digest);pm.environment.set("business_recommendation_version",body.data.version);}')
 if response=='RMSPolicy':tests.append('if(pm.response.code<300)pm.environment.set("business_rms_policy_version",body.data.version);')
 if response=='RMSDemandSnapshot':tests.append('if(pm.response.code<300){pm.environment.set("business_snapshot_id",body.data.id);pm.environment.set("business_source_digest",body.data.source_digest);}')
 if response=='ReservationOverbookingPolicy':tests.append('if(pm.response.code<300)pm.environment.set("business_overbooking_policy_version",body.data.version);')
 if response=='CommercialMappingPreviewResult':tests.append('if(pm.response.code<300){pm.environment.set("business_mapping_preview_id",body.data.preview_id);pm.environment.set("business_mapping_digest",body.data.digest);}')
 if response=='CommercialBulkView':tests.append('if(pm.response.code<300){pm.environment.set("business_bulk_snapshot_digest",body.data.snapshot.digest);pm.environment.set("business_bulk_version",body.data.version);}')
 if response=='CommercialRateResult':tests.append('if(pm.response.code<300){pm.environment.set("business_rate_plan_id",body.data.rate_plan.id);if(body.data.rate_version)pm.environment.set("business_rate_version_id",body.data.rate_version.id);}')
 if response=='CommercialListingDetail':tests.append('if(pm.response.code<300){pm.environment.set("business_listing_id",body.data.listing.id);pm.environment.set("business_listing_version_id",body.data.current_version.id);}')
 if response=='ReservationDetail':tests.append('if(pm.response.code<300){pm.environment.set("business_reservation_id",body.data.reservation.id);pm.environment.set("business_reservation_version",body.data.reservation.version);if(body.data.rooms.length)pm.environment.set("business_reservation_room_id",body.data.rooms[0].id);const activeGroup=body.data.financial_groups.find(g=>g.allocation_relationship==="ACTIVE_ALLOCATION");if(activeGroup)pm.environment.set("business_financial_group_id",activeGroup.id);}')
 if response=='QuoteSnapshot':tests.append('if(pm.response.code<300){pm.environment.set("business_quote_id",body.data.quote_id);pm.test("Quote is not a reservation",()=>pm.expect(body.data.is_reservation).to.eql(false));}')
 if response=='CommercialReceiptResult':tests.append('if(pm.response.code<300) pm.test("No fabricated reservation acceptance",()=>pm.expect(body.data.canonical_reservation_accepted).to.eql(false));')
 if response=='Node':tests.append('if(pm.response.code<300){pm.environment.set("business_node_id",body.data.id);pm.environment.set("business_tree_version",body.data.tree_version);}')
 if resource and method=='POST':tests.append(f'if (pm.response.code===201) {{ pm.test("Created identifier",()=>pm.expect(body.data.id).to.match(/^[0-9a-f-]{{36}}$/)); pm.environment.set("business_{resource}",body.data.id); }}')
 if path.endswith('/auth/register'):tests.append('if (pm.response.code===201) pm.test("Draft organisation only",()=>{ pm.expect(body.data.organisation_state).to.eql("DRAFT"); pm.expect(body.data.account_id).to.be.a("string"); pm.expect(body.data.organisation_id).to.be.a("string"); });')
 if response=='ApprovalResult':tests.append('if (pm.response.code<300) pm.test("Approval identity and state",()=>{pm.expect(body.data.approval.id).to.be.a("string");pm.expect(["DRAFT","PENDING","APPROVED","REJECTED","CANCELLED","EXPIRED"]).to.include(body.data.approval.state);pm.environment.set("business_approval_version",body.data.version);});')
 if response=='Role':tests.append('if (pm.response.code<300 && body.data.role_id) pm.test("Role version",()=>pm.expect(body.data.version).to.be.above(0));')
 if path.endswith('/auth/login'):tests.append('if (pm.response.code===200) pm.test("MFA or access credentials",()=>{if(body.data.mfa_required) pm.expect(body.data.mfa_token).to.be.a("string");else pm.expect(body.data.tokens.access_token).to.be.a("string");});')
 if path.endswith('/staff') and method=='POST':tests.append('if(pm.response.code===201) pm.test("Invitation reference",()=>pm.expect(body.data.invitation_id).to.be.a("string"));')
 examples=[]
 for code,res in responses.items():examples.append({'name':res['description'],'originalRequest':copy.deepcopy(request),'status':res['description'],'code':int(code),'header':[{'key':'Content-Type','value':'application/json'}],'body':json.dumps(res['content']['application/json']['example'],indent=2)})
 folder='00 Auth & Security' if any(path.startswith('/api/v1/business/'+p) for p in ('auth','account','sessions','devices','memberships')) else ('02 Roles, Permissions & Approvals' if any('/'+p in path for p in ('roles','permissions','approval','delegations','break-glass','audit-events')) else '01 Organisation & Staff')
 if any(path.startswith('/api/v1/business/'+p) for p in tuple(node_kinds)+('inventory-nodes','imports')):folder='03 Portfolio & Property Hierarchy'
 if any(path.startswith('/api/v1/business/'+p) for p in ('listings','listing-templates')):folder='05 Listings & Content'
 if any(path.startswith('/api/v1/business/'+p) for p in ('inventory-pools','sellable-products','holds','allotments','calendar','availability','compliance-holds')) or path=='/api/v1/business/inventory':folder='06 Inventory & Availability'
 if any(path.startswith('/api/v1/business/'+p) for p in ('rate-plans','rate-plan-versions','rate-quotes','rates/','restrictions','promotions','packages','add-ons','tax-profiles','currency-policy')):folder='07 Rate Plans, Restrictions & Promotions'
 if path.startswith(('/api/v1/business/channels','/api/v1/business/channel-conflicts')):folder='08 Channel Manager'
 if path.startswith('/api/v1/business/revenue'):folder='09 Revenue Management'
 if path.startswith(('/api/v1/business/reservations','/api/v1/business/reservation-','/api/v1/business/waitlist')):folder='10 Reservations'
 if path.startswith('/internal/'):folder='_Internal'
 if path.startswith('/webhooks/'):folder='_Webhooks'
 folders[folder]['item'].append({'name':name,'request':request,'response':examples,'event':[{'listen':'prerequest','script':{'type':'text/javascript','exec':prerequest}},{'listen':'test','script':{'type':'text/javascript','exec':tests}}]})
O['components']['schemas']={k:S[k] for k in sorted(used)}
collection_path=ROOT/'postman/CosmicForge_RealEstate.postman_collection.json'
collection=json.loads(collection_path.read_text(encoding='utf-8-sig'))
folder=next(x for x in collection['item'] if x['name']=='03 Business Operations');folder['description']=O['info']['description'];folder['item']=list(folders.values())
folders['_Internal']['description']='Signed service-only supply receipts and authoritative financial inputs. Service audience, caller allowlist and organisation delegation required; never routed by the public Gateway.'
folders['_Webhooks']['description']='Gateway-authenticated Marketplace business-supply.v1 HMAC callbacks. Exact raw body/path/timestamp signature and configured connection required. Booking.com and Airbnb callback signature contracts remain unavailable.'
folders['_Admin']['description']='No frontend administration bypass. Provider certification and least-privilege provisioning remain authorised operator actions.'
existing={v['key'] for v in collection.setdefault('variable',[])}
for key,value in {'business_access_token':'','business_refresh_token':'','business_email':'','business_password':'','business_current_password':'','business_new_password':'','business_mfa_token':'','business_code':'','business_invitation_token':'','business_idempotency_key':'','business_id':'','business_session_id':'','business_organisation_id':'','business_role_id':'','business_approval_id':'','business_policy_id':'','business_version':'','business_internal_url':'http://localhost:8080','business_service_token':'','business_key_base64':'','business_callback_timestamp':'','business_callback_signature':''}.items():
 if key not in existing:collection['variable'].append({'key':key,'value':value,'type':'string'})
outputs={ROOT/'contracts/public/openapi/business-operations.yaml':json.dumps(O,indent=2)+'\n',collection_path:json.dumps(collection,indent=2,ensure_ascii=False)+'\n',SERVICE/'contracts/routes.json':json.dumps(R,indent=2)+'\n'}
changed=[]
for p,text in outputs.items():
 if not p.exists() or p.read_text(encoding='utf-8-sig')!=text:
  changed.append(str(p.relative_to(ROOT)))
  if '--check' not in sys.argv:write_contract(p,text)
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
