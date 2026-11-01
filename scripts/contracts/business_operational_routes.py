"""Reviewed contracts for executable operational handlers; no future routes."""
import re

def register_operations(root,s,route,obj,arr,ref,typ):
 def shape(name,**fields):s[name]=obj(fields,list(fields),True)
 shape('RMSRawList',items=arr({'type':'object','additionalProperties':True}),limit={'type':'integer'},offset={'type':'integer'})
 for name,fields in {'ForecastDay':['potential_demand_milli_nights','projected_sold_milli_nights'],'RMSMetrics':['booked_occupancy_bps','accepted_accommodation_minor','contractual_adr_minor','contractual_revpar_minor','realized_occupancy_bps','realized_adr_minor','net_revenue_minor','contribution_minor','conversion_bps'],'RMSPaceDay':['current_confirmed_nights','previous_confirmed_nights','pickup_nights','contractual_revenue_change_minor'],'RMSPaceResult':['observation_seconds','previous_snapshot_id'],'DemandDay':['accommodation_minor']}.items():
  for field in fields:
   prop=s[name]['properties'][field];prop['type']=[prop.get('type','string'),'null']
 for name in ('DemandDataset','RMSMetrics'):
  s[name]['properties']['currency'].update(pattern='^[A-Z]{3}$',example='NGN')
  s[name]['properties']['basis'].update(const='CONFIRMED_COMMITTED_DEMAND')
 for name in ('RMSDemandSnapshot','RMSForecastRun','RMSMetricsResult'):
  s[name]['properties']['source_digest'].update(pattern='^[a-f0-9]{64}$',example='a'*64)
 s['ForecastConfiguration']['properties']['history_days'].update(minimum=21,maximum=366)
 s['ForecastConfiguration']['properties']['minimum_comparable_days'].update(minimum=3,maximum=52)
 s['ForecastConfiguration']['properties']['minimum_history_coverage_bps'].update(minimum=5000,maximum=10000)
 s['DemandForecast']['properties']['is_calibrated_probability'].update(const=False)
 s['DemandForecast']['properties']['model'].update(const='canonical-weekday-demand-v1')
 for name in ('DemandForecast','ForecastDay','RMSForecastRun'):s[name]['properties']['status'].update(enum=['BASELINE_READY','INSUFFICIENT_DATA'])
 s['RMSPaceResult']['properties']['state'].update(enum=['COMPARABLE','NO_EARLIER_OBSERVATION'])

 shape('ReservationConflictDecisionResult',id=typ('ids.ID'),state={'enum':['RESOLVED','REJECTED']},version={'type':'integer'},canonical_reservation_id={'type':['string','null']},canonical_reservation_accepted={'type':'boolean'})
 shape('ReservationConflictDetail',conflict=ref('ReservationConflictView'),decisions=arr({'type':'object','additionalProperties':True}))
 shape('ReservationWaitlistRejected',id=typ('ids.ID'),offer_id=typ('ids.ID'),state={'enum':['WAITING','CANCELLED']},offer_state={'const':'REJECTED'},version={'type':'integer'})
 shape('ReservationRawList',items=arr({'type':'object','additionalProperties':True}),limit={'type':'integer'},offset={'type':'integer'})
 shape('ReservationLineageResult',source=ref('Reservation'),target=ref('ReservationDetail'),action={'enum':['SPLIT','MERGED']})
 shape('ReservationRiskResult',reservation_id=typ('ids.ID'),status={'enum':['UNREVIEWED','CLEAR','REVIEW','RESTRICTED']},assessments=arr({'type':'object'}))
 shape('ReservationFinancialRetryResult',id=typ('ids.ID'),state={'const':'RETRY'},version={'type':'integer'},collection_confirmed={'const':False})
 shape('ReservationFinancialQueued',id=typ('ids.ID'),state={'const':'QUEUED'},payment_authority={'const':'payment-service'})
 s['ReservationFinancialQueued']['additionalProperties']=True
 shape('ReservationWaitlistCreate',property_id=typ('ids.ID'),product_id=typ('ids.ID'),date_from={'type':'string','format':'date'},date_to={'type':'string','format':'date'},adults={'type':'integer','minimum':1},children={'type':'integer','minimum':0},quantity={'type':'integer','minimum':1},priority={'type':'integer','minimum':0,'maximum':100},guest_reference={'type':'string','maxLength':300},expires_at=typ('time.Time'),reason={'type':'string','minLength':5,'maxLength':1000})
 shape('ReservationPaymentReadbackCommand',version={'type':'integer','minimum':1},financial_group_id=typ('ids.ID'),reason={'type':'string','minLength':5,'maxLength':1000})
 shape('ReservationWaitlistCancelled',id=typ('ids.ID'),state={'const':'CANCELLED'},version={'type':'integer'})
 shape('ReservationSourceStatus',reservation_id=typ('ids.ID'),version={'type':'integer'},state={'type':'string'},allocation_state={'type':'string'},financial_state={'type':'string'},amount_minor={'type':'integer'},currency={'type':'string'},canonical_reservation_accepted={'const':True},lineage_reconciliation_required={'type':'boolean'},confirmed={'type':'boolean'})
 s['Reservation']['properties']['created_by']['type']=['string','null']
 s['Reservation']['properties']['state']['enum']=['HOLD','INQUIRY','PENDING_PAYMENT','PENDING_APPROVAL','CONFIRMED','CHECKED_IN','CHECKED_OUT','COMPLETED','CANCELLED','NO_SHOW','REJECTED','EXPIRED','DISPUTED']
 s['Reservation']['properties']['financial_state']['enum']=['PENDING','UNVERIFIED','UNKNOWN','FAILED','EXPIRED','VERIFIED','APPROVED_GUARANTEE','DISPUTED']
 s['Reservation']['properties']['allocation_state']['enum']=['ACTIVE','SPLIT_SOURCE','MERGED_SOURCE']
 s['Reservation']['properties']['risk_status']['enum']=['UNREVIEWED','CLEAR','REVIEW','RESTRICTED']
 s['ReservationPolicy']['properties']['acceptance_timezone'].update(readOnly=True,description='Set by the service on accepted reservation snapshots, preserving the contractual civil-time context.')
 s['OverbookingDay']['properties']['day'].update(format='date',example='2027-01-02')
 s['OverbookingPolicy']['properties']['max_quantity'].update(minimum=1,maximum=1000)
 s['OverbookingPolicy']['properties']['max_capacity_bps'].update(minimum=1,maximum=10000)
 s['ReservationPolicy']['properties']['hold_seconds'].update(minimum=60,maximum=900)
 s['ReservationFinancialGroupView']['properties']['allocation_relationship']['enum']=['ACTIVE_ALLOCATION','HISTORICAL_BACKING']
 for name in ('Reservation','ReservationRoom','ReservationRoomCommand','ReservationWaitlist'):
  for date in ('date_from','date_to'):
   if date in s[name]['properties']:s[name]['properties'][date].update(format='date',example='2027-01-02' if date=='date_from' else '2027-01-04')
 data={
 'reservationConflicts':('reservations:read',None,'ReservationRawList',200,False),
 'reservationConflictGet':('reservations:read',None,'ReservationConflictDetail',200,False),
 'reservationConflictDecision':('reservations:resolve','ReservationConflictDecision','ReservationConflictDecisionResult',200,True),
 'reservationOverbookingPolicies':('reservations:read',None,'ReservationRawList',200,False),
 'reservationOverbookingPolicySave':('reservations:overbook','ReservationOverbookingPolicyCommand','ReservationOverbookingPolicy',201,True),
 'reservationOverbookingQuote':('reservations:overbook','ReservationOverbookingQuote','QuoteSnapshot',201,True),
 'reservationOverbook':('reservations:overbook','ReservationOverbookingCommand','ReservationDetail',200,True),
 'reservationOverbookingReconcile':('reservations:overbook','ReservationOverbookingCommand','ReservationDetail',200,True),
 'rmsDemandList':('rms:read',None,'RMSRawList',200,False),
 'rmsDemandCreate':('rms:write','RMSDemandCommand','RMSDemandSnapshot',201,False),
 'rmsDemandGet':('rms:read',None,'RMSDemandSnapshot',200,False),
 'rmsPaceGet':('rms:read',None,'RMSPaceResult',200,False),
 'rmsMetricsGet':('rms:read',None,'RMSMetricsResult',200,False),
 'rmsForecastList':('rms:read',None,'RMSRawList',200,False),
 'rmsForecastCreate':('rms:write','RMSForecastCommand','RMSForecastRun',201,False),
 'rmsForecastGet':('rms:read',None,'RMSForecastRun',200,False),
 'reservationPolicies':('reservations:read',None,'List:ReservationPolicyView',200,False),
 'reservationPolicyCreate':('reservations:approve','ReservationPolicyCommand','ReservationPolicyView',201,True),
 'reservationList':('reservations:read',None,'List:Reservation',200,False),
 'reservationCreate':('reservations:write','ReservationCreate','ReservationDetail',201,False),
 'reservationGet':('reservations:read',None,'ReservationDetail',200,False),
 'reservationCancellationObligations':('reservations:read',None,'ReservationRawList',200,False),
 'reservationHistory':('reservations:read',None,'ReservationRawList',200,False),
 'reservationCommandCenter':('reservations:read',None,'ReservationRawList',200,False),
 'reservationConfirm':('reservations:confirm','ReservationMutation','ReservationDetail',200,False),
 'reservationCancel':('reservations:cancel','ReservationMutation','ReservationDetail',200,False),
 'reservationPartialCancel':('reservations:cancel','ReservationMutation','ReservationDetail',200,False),
 'reservationNoShow':('reservations:cancel','ReservationMutation','ReservationDetail',200,False),
 'reservationReject':('reservations:cancel','ReservationMutation','ReservationDetail',200,False),
 'reservationDispute':('reservations:resolve','ReservationMutation','ReservationDetail',200,True),
 'reservationResolveDispute':('reservations:resolve','ReservationMutation','ReservationDetail',200,True),
 'reservationRiskGet':('reservation_risk:read',None,'ReservationRiskResult',200,False),
 'reservationRiskReview':('reservation_risk:write','ReservationRiskReview','Reservation',200,True),
 'reservationCollectionRequest':('reservations:write','ReservationCollectionCommand','ReservationFinancialQueued',202,True),
 'reservationPaymentRequests':('reservations:read',None,'ReservationRawList',200,False),
 'reservationFinancialRetry':('reservations:write','ReservationFinancialRetry','ReservationFinancialRetryResult',202,True),
 'reservationPaymentReadback':('reservations:write','ReservationPaymentReadbackCommand','ReservationFinancialQueued',202,False),
 'reservationWaitlistOfferReject':('reservations:write','ReservationWaitlistRejection','ReservationWaitlistRejected',200,False),
 'reservationWaitlistList':('reservations:read',None,'List:ReservationWaitlist',200,False),
 'reservationWaitlistCreate':('reservations:write','ReservationWaitlistCreate','ReservationWaitlist',201,False),
 'reservationWaitlistOffers':('reservations:read',None,'ReservationRawList',200,False),
 'reservationWaitlistOfferCreate':('reservations:write','ReservationWaitlistOfferCommand','ReservationWaitlistOffer',201,False),
 'reservationWaitlistAccept':('reservations:write','ReservationWaitlistAccept','ReservationDetail',201,False),
 'reservationWaitlistCancel':('reservations:cancel','ReservationMutation','ReservationWaitlistCancelled',200,False),
 'reservationAmendmentQuote':('reservations:modify','ReservationAmendmentQuote','QuoteSnapshot',201,False),
 'reservationAmend':('reservations:modify','ReservationMutation','ReservationDetail',200,True),
 'reservationSplit':('reservations:modify','ReservationMutation','ReservationLineageResult',200,True),
 'reservationMerge':('reservations:modify','ReservationMutation','ReservationLineageResult',200,True),
 'reservationLineageGet':('reservations:read',None,'ReservationRawList',200,False),
 }
 found=set()
 for p in sorted((root/'services/business-operations-service/internal/http').glob('operational*.go')):
  for method,path,handler in re.findall(r'r\.(Get|Post|Put|Patch|Delete)\("([^"\n]+)",\s*h\.(\w+)\)',p.read_text(encoding='utf-8')):
   if handler not in data:raise RuntimeError('Operational handler lacks reviewed contract: '+handler)
   cap,request,response,status,mfa=data[handler]
   note='Trusted live tenant/membership and property scope; current versions and immutable history. Side-effect retries require Idempotency-Key. '
   if handler in ('reservationCreate','reservationConfirm'):note+='Canonical acceptance is separate from confirmation. Confirmation requires fresh bound Payment collection or an independently approved guarantee, exact held nights and risk eligibility. '
   if handler in ('reservationAmend','reservationSplit','reservationMerge'):note+='Exact independent amount/currency/channel-bound approval and MFA. Prices remain immutable. Stock and financial backing conserve through append-only versions/lineage. '
   if handler in ('reservationCollectionRequest','reservationPaymentReadback'):note+='202 means a durable request only. Worker uses existing authenticated Payment policy/locked-quote/receipt/intent APIs. Unknown outcomes remain pending; collection, settlement and refunds are distinct. '
   if handler.startswith('reservationOverbook'):note+='Explicit staff-only excess-demand policy and independent exact approval. Physical capacity is unchanged; ordinary booking remains bounded. Outstanding excess blocks physical assignment. Reconciliation requires real released backing; customer recovery and remote pause remain separately observable. Policy upsert returns 201 for creation or 200 for a new version. '
   if handler.startswith('rms'):note+='Retained owned pool/calendar/reservation observations and exact source digests; missing history is not filled with zeros. Forecast estimates use fixed-point thousandths of a night and non-calibrated observed coverage. Contractual booking metrics are separate from realized stays/cash/refunds/contribution/conversion, which remain null without their owning inputs. This operation does not apply rates, create capacity or authorise autopilot. '
   if handler.startswith('reservationWaitlist'):note+='Offers reserve real inventory and expire. Acceptance consumes the exact current offer once under the owning transaction. '
   route(method.upper(),path,cap,request,response,status,method!='Get',mfa,notes=note)
   route.__globals__['R'][-1]['handler']=handler
   if handler=='reservationOverbookingPolicySave':route.__globals__['R'][-1]['success_statuses']=[200]
   found.add(handler)
 if found!=set(data):raise RuntimeError('Unregistered operational contracts: '+str(set(data)-found))
 route('GET','/reservations/{id}','signed:marketplace-service',None,'ReservationSourceStatus',200,notes='Signed Marketplace caller and matching organisation delegation, certified readback capability and exact source connection ownership. No guest/private profile is exposed.')
 route.__globals__['R'][-1].update(path='/internal/v1/business-supply/reservations/{id}',transport='internal',handler='internalReservationStatus')

def operational_queries(handler):
 if handler=='rmsMetricsGet':return [('snapshot_id','uuid',True)]
 if handler=='reservationConflicts':return [('state','string',False)]
 if handler=='internalReservationStatus':return [('connection_id','uuid',True)]
 if handler=='reservationCommandCenter':return [('view','string',False),('property_id','uuid',False),('branch_id','uuid',False),('day','date',False),('date_from','date',False),('date_to','date',False),('state','string',False),('source','string',False),('channel','string',False),('search','string',False)]
 if handler=='reservationList':return [('property_id','uuid',False),('branch_id','uuid',False),('date_from','date',False),('date_to','date',False),('state','string',False),('source','string',False),('channel','string',False),('search','string',False)]
 return []

def operational_examples(uid):
 room={'product_id':uid,'quote_id':uid,'product_version':1,'pool_version':1,'date_from':'2027-01-02','date_to':'2027-01-04','quantity':1}
 booking={'property_id':uid,'source':'DIRECT','channel':'','source_reference':'direct-booker-reference','state':'HOLD','payment_responsibility':'PLATFORM_COLLECT','rooms':[room],'guests':[{'source_kind':'DIRECT','source_reference':'typed-direct-guest-reference','is_primary':True}],'reason':'Accept the exact frozen canonical quote'}
 policy={'allowed_payment_policy_ids':[uid],'allow_source_cancellation':False,'max_guarantee_minor':100000,'allowed_sources':['DIRECT','MARKETPLACE'],'allowed_guarantees':['PAY_AT_PROPERTY'],'hold_seconds':600,'require_approval':False,'require_risk_clear':True,'cancellation_hours':24,'cancellation_penalty_bps':5000,'no_show_grace_hours':2,'no_show_retain_nights':1,'check_in_local':'14:00','check_out_local':'11:00','policy_reference':'operator-approved-hotel-terms:v1'}
 return {
 'ReservationConflictDecision':{'version':1,'action':'RETRY','approval_id':uid,'reason':'Retry the exact authenticated receipt after independent inventory review','evidence_references':['approved-source-reconciliation-reference']},
 'ReservationWaitlistRejection':{'version':2,'offer_version':1,'continue_waiting':True,'reason':'Decline this offer and keep the original request waiting'},
 'ReservationOverbookingPolicyCommand':{'property_id':uid,'pool_id':uid,'expected_version':0,'definition':{'max_quantity':1,'max_capacity_bps':10000,'allowed_sources':['DIRECT'],'effective_at':'2026-11-01T00:00:00Z','expires_at':'2026-11-10T00:00:00Z','policy_reference':'approved-exception-policy:v1'},'state':'ACTIVE','approval_id':uid,'reason':'Explicit independent property overbooking policy'},
 'ReservationOverbookingQuote':{'policy_id':uid,'policy_version':1,'quote':{'product_id':uid,'date_from':'2027-01-02','date_to':'2027-01-04','adults':2,'children':0,'quantity':1,'channel':''}},
 'ReservationOverbookingCommand':{'version':1,'policy_id':uid,'policy_version':1,'approval_id':uid,'recovery_reference':'operator-recovery-case-reference','reason':'Independent review of an explicit constrained excess-demand obligation'},
 'RMSDemandCommand':{'property_id':uid,'pool_id':uid,'date_from':'2027-01-02','date_to':'2027-01-04','history_days':56},
 'RMSForecastCommand':{'snapshot_id':uid,'configuration':{'history_days':56,'minimum_comparable_days':3,'minimum_history_coverage_bps':8000}},
 'ReservationCreate':booking,
 'ReservationPolicyCommand':{'property_id':uid,'expected_version':0,'definition':policy,'effective_at':'2026-10-31T00:00:00Z','approval_id':uid,'reason':'Independently approved reservation acceptance policy'},
 'ReservationMutation':{'version':1,'reason':'Versioned reservation command with current evidence'},
 'ReservationCollectionCommand':{'version':1,'financial_group_id':uid,'payment_policy_id':uid,'payment_policy_version':'operator-configured-version','payment_policy_family':'HOTEL','payer_wallet_id':uid,'payer_type':'GUEST','consent_reference':'owning-payer-consent-reference','legal_party_reference':'canonical-payer-party-reference','email':'disposable@example.invalid','schedule':'ONE_TIME','reason':'Collect the frozen accepted obligation through Payment Service'},
 'ReservationFinancialRetry':{'version':1,'request_version':3,'reason':'Recover the original authoritative receipt with its original idempotency key'},
 'ReservationPaymentReadbackCommand':{'version':1,'financial_group_id':uid,'reason':'Read back original collection and reversal facts'},
 'ReservationAmendmentQuote':{'version':1,'quote':{'product_id':uid,'date_from':'2027-01-02','date_to':'2027-01-04','adults':2,'children':0,'quantity':1,'channel':''}},
 'ReservationWaitlistCreate':{'property_id':uid,'product_id':uid,'date_from':'2027-01-02','date_to':'2027-01-04','adults':2,'children':0,'quantity':1,'priority':0,'guest_reference':'typed-waitlisted-guest-reference','expires_at':'2026-11-15T12:00:00Z','reason':'Wait for canonical availability'},
 'ReservationWaitlistOfferCommand':{'version':1,'quote_id':uid,'product_version':1,'pool_version':1,'channel':'','reason':'Offer the current quote against a real hold'},
 'ReservationWaitlistAccept':{'version':2,'offer_id':uid,'offer_version':1,'booking':{**booking,'rooms':[{**room,'hold_id':uid,'hold_version':1}]}},
 'ReservationRiskReview':{'version':1,'status':'CLEAR','reason_code':'INDEPENDENT_REVIEW','review_reference':'authorised-review-evidence-reference','expires_at':'2026-10-31T12:00:00Z','approval_id':uid,'reason':'Independently review reservation risk evidence'},
 }
