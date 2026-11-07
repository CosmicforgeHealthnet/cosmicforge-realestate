"""Executable in-house PMS amendment contracts: extension, early departure and unit releases."""
def register_pms_amendments(s,obj,arr,ref,typ):
 def shape(name,**fields):s[name]=obj(fields,list(fields),True)
 uid=typ('ids.ID');integer={'type':'integer'};date={'type':'string','format':'date','example':'2026-12-03'}
 s['PMSStayExtensionCommand']['properties']['payer_mode']['enum']=['RESERVATION_TERMS','CORPORATE','SPLIT']
 s['PMSStayExtensionCommand']['properties']['date_to'].update(format='date',example='2026-12-05')
 s['PMSStayExtensionPayer']['properties']['payer_kind']['enum']=['GUEST','CORPORATE']
 s['PMSStayExtensionPayer']['properties']['responsibility']['enum']=['PLATFORM_COLLECT','PAY_AT_PROPERTY','CORPORATE_CREDIT','CHANNEL_COLLECT']
 s['PMSStayExtensionPayer']['properties']['financial_state']['enum']=['PENDING','APPROVED_GUARANTEE']
 s['PMSStayExtensionResult']['properties']['access_state']['const']='UNKNOWN'
 s['PMSEarlyDepartureSegmentPayer']['properties']['collection']['enum']=['COLLECTED','NOT_COLLECTED','GUARANTEE','UNKNOWN','EXTERNAL_UNKNOWN']
 s['PMSEarlyDepartureResult']['properties']['financial_state']['enum']=['NO_CREDIT','FOLIO_CREDIT_ONLY','REFUND_PENDING_EXTERNAL','READBACK_REQUIRED','EXTERNAL_COLLECTION_UNKNOWN']
 s['PMSEarlyDepartureResult']['properties']['refundable_minor']={'type':['integer','null']}
 s['PMSEarlyDepartureResult']['properties']['refund_executed']={'const':False}
 s['PMSEarlyDepartureResult']['properties']['departure']={'type':'object','additionalProperties':True}
 shape('PMSStayExtensionList',items=arr({'type':'object','additionalProperties':True}),stay_id=uid)
 s['PMSEarlyDepartureRecord']=obj({'id':uid,'stay_id':uid,'reservation_id':uid,'departure_day':date,'unused_nights':integer,'unused_minor':integer,'penalty_minor':integer,'credit_minor':integer,'refundable_minor':{'type':['integer','null']},'financial_state':{'enum':['NO_CREDIT','FOLIO_CREDIT_ONLY','REFUND_PENDING_EXTERNAL','READBACK_REQUIRED','EXTERNAL_COLLECTION_UNKNOWN']},'line_effects':arr({'type':'object','additionalProperties':True}),'refund_requests':arr({'type':'object','additionalProperties':True})},['id','stay_id','financial_state','refund_requests'],False)
 shape('ReservationUnitReleaseList',reservation_id=uid,releases=arr({'type':'object','additionalProperties':True}),days=arr({'type':'object','additionalProperties':True}))
 return {
 'pmsStayExtensions':('pms:read',None,'PMSStayExtensionList',200,False),
 'pmsStayExtend':('pms:write','PMSStayExtensionCommand','PMSStayExtensionResult',201,True),
 'pmsStayExtensionCollection':('folios:write','PMSStayExtensionCollection','ReservationFinancialQueued',202,True),
 'pmsEarlyDeparturePreview':('pms:read',None,'PMSEarlyDepartureTerms',200,False),
 'pmsEarlyDepartureGet':('pms:read',None,'PMSEarlyDepartureRecord',200,False),
 'pmsEarlyDeparture':('pms:write','PMSEarlyDepartureCommand','PMSEarlyDepartureResult',200,True),
 'reservationUnitReleases':('reservations:read',None,'ReservationUnitReleaseList',200,False),
 }

def pms_amendment_notes(handler):
 if handler=='pmsStayExtend':return 'Extends one in-house unit slot: the single-unit quote must start at the current unit departure for the same product, occupancy and channel. New nights commit canonical stock through an atomic hold, a contiguous extension line, a continuation assignment of the same physical room, an accepted folio posting and a revoke/re-issue access request. Any guarantee share (pay at property, corporate credit) needs reservations:modify, folios:write, recent MFA and an exact independent COMMERCIAL_PMS_STAY_EXTENSION approval bound to the guaranteed amount; corporate shares need a live profile, current contract and active traveler association. Any failure rolls back every stock, folio and assignment change. '
 if handler=='pmsStayExtensionCollection':return 'Queues only the platform-collected guest share through the existing durable Payment worker; 202 is never collected money and a second charge is never created to resolve uncertainty. '
 if handler in ('pmsEarlyDeparture','pmsEarlyDeparturePreview','pmsEarlyDepartureGet'):return 'Unused nights are those from the actual departure civil date; the frozen reservation cancellation window and penalty decide the credit. Preview never mutates. The command needs folios:write, reservations:modify, recent MFA, an exact independent COMMERCIAL_PMS_EARLY_DEPARTURE approval bound to credit plus outstanding exposure, and the previewed credit/outstanding values. Unit nights return to canonical stock through an immutable ledger without splitting the line; other units stay in house. Collected backing creates only a DEPENDENCY_BLOCKED refund request pending authorised Payment/Admin execution; unknown collection stays READBACK_REQUIRED. '
 if handler=='reservationUnitReleases':return 'Per-date conservation: accepted quantity = remaining committed quantity + released unit nights. '
 return ''

def pms_amendment_examples(uid):
 reason='Record the exact reviewed in-house amendment with its evidence'
 return {
 'PMSStayExtensionCommand':{'version':1,'reservation_version':3,'assignment_version':1,'folio_version':2,'quote_id':uid,'product_version':1,'pool_version':1,'date_to':'2026-12-05','payer_mode':'SPLIT','corporate_customer_id':uid,'corporate_contract_id':uid,'corporate_amount_minor':5000,'approval_id':uid,'reason':reason},
 'PMSStayExtensionCollection':{'payment_policy_id':uid,'payment_policy_version':'operator-configured-version','payment_policy_family':'HOTEL','payer_wallet_id':uid,'consent_reference':'explicit-guest-consent-reference','legal_party_reference':'canonical-guest-party-reference','email':'guest@example.invalid','schedule':'ONE_TIME','reason':reason},
 'PMSEarlyDepartureCommand':{'version':2,'reservation_version':4,'folio_version':3,'expected_credit_minor':5000,'expected_outstanding_minor':15000,'approval_id':uid,'reason':reason},
 }
