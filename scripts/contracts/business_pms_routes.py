"""Executable hospitality contracts. Provider unknowns are represented explicitly."""
import copy
def register_pms(s,obj,arr,ref,typ):
 def shape(name,**fields):s[name]=obj(fields,list(fields),True)
 uid=typ('ids.ID');integer={'type':'integer'}
 s['PMSDayAutomationCommand']['properties']['mode']['enum']=['MANUAL','AUDIT_ONLY','CLOSE_IF_CLEAR']
 s['PMSDayAutomationPolicy']['properties']['mode']['enum']=['MANUAL','AUDIT_ONLY','CLOSE_IF_CLEAR']
 for name in ('PMSDayAutomationCommand','PMSDayAutomationPolicy'):
  s[name]['properties']['max_days_per_run'].update(minimum=1,maximum=7)
  s[name]['properties']['hospitality_policy_version'].update(minimum=0)
 s['PMSDayAutomationRevoked']['properties']['state']['const']='REVOKED'
 s['PMSDayAutomationPolicyView']=copy.deepcopy(s['PMSDayAutomationPolicy'])
 s['PMSDayAutomationPolicyView']['properties']['approval_id']={'anyOf':[uid,{'type':'null'}]}
 s['PMSDayAutomationPolicyView']['properties']['recorded_state']={'enum':['SUPERSEDED','REVOKED','MANUAL','EXPIRED','NOT_YET_EFFECTIVE','RECORDED']}
 s['PMSDayAutomationPolicyView']['properties']['source_review']={'anyOf':[obj({'state':{'enum':['READY','QUARANTINED']},'consecutive_failures':{'type':'integer','minimum':0},'last_error_code':{'enum':['','SOURCE_TRANSACTION_FAILED']},'next_check_at':typ('time.Time')},['state','consecutive_failures','last_error_code','next_check_at'],True),{'type':'null'}]}
 s['PMSDayAutomationPolicyView']['required']+=['recorded_state','source_review']
 shape('PMSDayAutomationPolicyList',items=arr(ref('PMSDayAutomationPolicyView')),limit=integer,offset=integer)
 shape('PMSDayAutomationRun',id=uid,organisation_id=uid,property_id=uid,policy_id=uid,business_day_id={'anyOf':[uid,{'type':'null'}]},source_digest={'type':'string','pattern':'^[a-f0-9]{64}$','example':'a'*64},state={'enum':['AUDITED','BLOCKED','CLOSED','AUTHORITY_REVOKED','POLICY_STALE','SOURCE_REVIEW_REQUIRED']},audit_id={'anyOf':[uid,{'type':'null'}]},close_id={'anyOf':[uid,{'type':'null'}]},evidence={'type':'object','additionalProperties':True},created_by_service={'const':'business-operations-service'},created_at=typ('time.Time'))
 shape('PMSDayAutomationRunList',items=arr(ref('PMSDayAutomationRun')),limit=integer,offset=integer)


 shape('PMSFiscalReviewResult',review=ref('FiscalReview'),folio_version=integer,financial_effect={'const':'DETAIL_ONLY'},changed={'type':'boolean','example':True})
 shape('PMSFiscalReviewList',items=arr(ref('FiscalReview')),limit=integer,offset=integer)
 s['PMSItem']['description']='The accepted accommodation posting is one gross booking obligation. fiscal_review.breakdown is detail only; its components are never charged again.'
 s['FiscalBreakdown']['properties']['state']['enum']=['ITEMIZED','LEGACY_AGGREGATE_REVIEW_REQUIRED','OPERATOR_REVIEWED']
 s['FiscalReview']['properties']['state']['enum']=['ITEMIZED','LEGACY_AGGREGATE_REVIEW_REQUIRED','OPERATOR_REVIEWED']
 s['FiscalReview']['properties']['source_kind']['enum']=['ACCEPTED_QUOTE','OPERATOR_ATTESTED']
 s['FiscalReview']['properties']['digest'].update(pattern='^[a-f0-9]{64}$',example='a'*64)
 s['FiscalBreakdown']['properties']['currency'].update(pattern='^[A-Z]{3}$',example='NGN')

 s['FiscalBreakdown']['properties']['financial_effect']={'const':'DETAIL_ONLY'}
 s['FiscalComponent']['properties']['kind']['enum']=['ACCOMMODATION_NET','ADD_ON_NET','TAX','FEE','ACCEPTED_SERVICES_NET']
 s['PMSFiscalNetAllocation']['properties']['kind']['enum']=['ACCOMMODATION_NET','ADD_ON_NET']
 s['QuoteSnapshot']['properties']['fiscal_allocation_version'].update(enum=['PROPORTIONAL_PRETAX_V1'],example='PROPORTIONAL_PRETAX_V1')

 s['PMSGroupBooking']=copy.deepcopy(s['ReservationCreate'])
 s['PMSGroupBooking']['properties']['source']['enum']=['GROUP','CORPORATE']
 s['PMSGroupBooking']['properties']['state']['enum']=['HOLD','CONFIRMED']
 s['PMSGroupBooking']['properties']['group_name']['const']=''
 s['PMSGroupBooking']['required']=list(dict.fromkeys(s['PMSGroupBooking']['required']+['group_id']))
 s['PMSGroupBlockCommand']['properties']['booking']=ref('PMSGroupBooking')
 s['PMSOfflineResult']['properties']['result']={'type':['object','null'],'additionalProperties':True}
 shape('PMSOfflineResolved',id=uid,disposition={'enum':['RETRY_ONLINE','CANCELLED']},original_operation_applied={'const':False})
 shape('PMSRoomMoveResult',stay_id=uid,assignment_id=uid,version=integer,reservation_version=integer,access_state={'const':'UNKNOWN'})
 shape('PMSList',items=arr({'type':'object','additionalProperties':True}),limit=integer,offset=integer)
 shape('PMSCheckInResult',stay_id=uid,folio_id=uid,reservation_version=integer,access_state={'const':'UNKNOWN'})
 shape('PMSCheckoutResult',stay_id=uid,state={'const':'DEPARTED'},folio_state={'enum':['CLOSED','SETTLEMENT_PENDING']},balance_minor=integer,reservation_version=integer,room_state={'const':'VACANT_DIRTY'},access_state={'const':'UNKNOWN'})
 shape('PMSFolioView',folio=ref('PMSFolio'),items=arr(ref('PMSItem')),balance_minor=integer,payment_truth={'const':'PAYMENT_SERVICE'})
 shape('PMSFolioReconciled',folio=ref('PMSFolio'),balance_minor=integer,payment_truth={'const':'PAYMENT_SERVICE'})
 shape('PMSFolioReviewRevoked',id=uid,review_id=uid,state={'const':'REVOKED'})
 shape('PMSFolioPostingResult',folio=ref('PMSFolio'),item_id=uid)
 shape('PMSFrontDesk',property_id=uid,business_date={'type':'string','format':'date'},timezone={'type':'string'},posting_cutoff_local={'type':'string'},observed_at={'type':'string','format':'date-time'},counts={'type':'object','additionalProperties':integer},reservations=arr({'type':'object','additionalProperties':True}),limit=integer,offset=integer)
 shape('PMSHospitalityPolicyCreated',id=uid,version=integer,cutoff_local={'type':'string'},applies_to={'const':'NEXT_OPENED_BUSINESS_DAY'})
 shape('PMSAuditResult',id=uid,state={'enum':['READY','EXCEPTIONS']},snapshot_digest={'type':'string'},snapshot=ref('PMSAuditSnapshot'))
 shape('PMSDayCloseResult',id=uid,state={'const':'CLOSED'},business_day_id=uid,next_business_day=ref('PMSBusinessDay'))
 shape('PMSDrawerCreated',id=uid,state={'const':'ACTIVE'},version=integer)
 shape('PMSHandoverCreated',id=uid,state={'const':'ACKNOWLEDGED'})
 shape('PMSCashAttestationResult',source_version=integer,duplicate={'type':'boolean'})
 s['PMSCashAttestationResult']['properties']['id']=uid
 shape('PMSGroupDetail',group=ref('PMSGroup'),reservations=arr({'type':'object','additionalProperties':True}),limit=integer,offset=integer)
 shape('PMSRoomingResult',id=uid,version=integer,state={'enum':['ACTIVE','REVOKED']},reservation_version=integer)
 shape('PMSGroupCheckInsResult',group_id=uid,items=arr(ref('PMSCheckInResult')),atomic={'const':True})
 shape('PMSGroupCheckOutsResult',group_id=uid,items=arr(ref('PMSCheckoutResult')),atomic={'const':True})
 shape('PMSMasterFolioCreated',id=uid,group_id=uid,currency={'type':'string'},source_folio_count=integer,payment_execution={'const':'PAYMENT_SERVICE'})
 shape('PMSMasterFolioView',id=uid,group_id=uid,currency={'type':'string'},billing_reference={'type':'string'},items=arr({'type':'object','additionalProperties':True}),balance_minor=integer,all_source_folios_closed={'type':'boolean'},basis={'const':'CURRENT_SOURCE_FOLIOS_AND_PAYMENT_READBACKS'})
 shape('PMSEventBookingCreated',id=uid,reservation_id=uid,reservation_state={'type':'string'},date_from={'type':'string','format':'date'},date_to={'type':'string','format':'date'},capacity_authority={'const':'CANONICAL_RESERVATION'},conversation_verified={'const':False})
 return {
 'pmsGroups':('pms:read',None,'PMSList',200,False),
 'pmsGroupCreate':('pms:write','PMSGroupCreate','PMSGroup',201,False),
 'pmsGroupGet':('pms:read',None,'PMSGroupDetail',200,False),
 'pmsGroupPatch':('pms:write','PMSGroupPatch','PMSGroup',200,True),
 'pmsGroupBlocks':('pms:read',None,'PMSList',200,False),
 'pmsGroupBlockCreate':('pms:write','PMSGroupBlockCommand','ReservationDetail',201,False),
 'pmsGroupRooming':('pms:read',None,'PMSList',200,False),
 'pmsGroupRoomingSave':('pms:write','PMSRoomingCommand','PMSRoomingResult',201,True),
 'pmsGroupCheckIn':('pms:write','PMSGroupCheckIns','PMSGroupCheckInsResult',201,True),
 'pmsGroupCheckOut':('pms:write','PMSGroupCheckOuts','PMSGroupCheckOutsResult',200,True),
 'pmsMasterFolios':('folios:read',None,'PMSList',200,False),
 'pmsMasterFolioCreate':('folios:write','PMSMasterFolioCommand','PMSMasterFolioCreated',201,True),
 'pmsMasterFolioGet':('folios:read',None,'PMSMasterFolioView',200,False),
 'pmsEventBookings':('pms:read',None,'PMSList',200,False),
 'pmsEventBookingCreate':('pms:write','PMSEventBookingCommand','PMSEventBookingCreated',201,False),
 'pmsHospitalityPolicies':('night_audit:read',None,'PMSList',200,False),
 'pmsHospitalityPolicy':('night_audit:write','PMSHospitalityPolicyCommand','PMSHospitalityPolicyCreated',201,True),
 'pmsDayAutomationPolicies':('night_audit:read',None,'PMSDayAutomationPolicyList',200,False),
 'pmsDayAutomationPolicy':('night_audit:write','PMSDayAutomationCommand','PMSDayAutomationPolicy',201,True),
 'pmsDayAutomationRuns':('night_audit:read',None,'PMSDayAutomationRunList',200,False),
 'pmsDayAutomationRevoke':('night_audit:write','PMSVersionCommand','PMSDayAutomationRevoked',201,True),
 'pmsNightAudits':('night_audit:read',None,'PMSList',200,False),
 'pmsNightAuditValidate':('night_audit:write','PMSAuditCommand','PMSAuditResult',201,False),
 'pmsBusinessDayClose':('night_audit:write','PMSDayCloseCommand','PMSDayCloseResult',200,True),
 'pmsBusinessDayCloses':('night_audit:read',None,'PMSList',200,False),
 'pmsCashDrawers':('cashier:read',None,'PMSList',200,False),
 'pmsCashDrawerCreate':('cashier:write','PMSDrawerCommand','PMSDrawerCreated',201,True),
 'pmsCashierShifts':('cashier:read',None,'PMSList',200,False),
 'pmsCashierOpen':('cashier:write','PMSCashierOpenCommand','PMSCashierShift',201,True),
 'pmsCashierClose':('cashier:write','PMSCashierCloseCommand','PMSCashierShift',200,True),
 'pmsCashierHandover':('cashier:write','PMSCashierHandoverCommand','PMSHandoverCreated',201,True),
 'pmsOfflineReplay':('pms:write','PMSOfflineCommand','PMSOfflineResult',201,False),
 'pmsOfflineReceipts':('pms:read',None,'PMSList',200,False),
 'pmsOfflineTasks':('pms:read',None,'PMSList',200,False),
 'pmsOfflineResolve':('pms:approve','PMSOfflineResolution','PMSOfflineResolved',201,True),
 'pmsGuestRequests':('pms:read',None,'PMSList',200,False),
 'pmsGuestRequestCreate':('pms:write','PMSGuestRequestCommand','PMSGuestRequest',201,False),
 'pmsGuestRequestPatch':('pms:write','PMSGuestRequestUpdate','PMSGuestRequest',200,False),
 'pmsRoomMove':('pms:write','PMSRoomMoveCommand','PMSRoomMoveResult',200,True),
 'pmsFrontDesk':('pms:read',None,'PMSFrontDesk',200,False),
 'pmsRoomStatuses':('room_status:read',None,'PMSList',200,False),
 'pmsRoomStatusGet':('room_status:read',None,'PMSRoomStatus',200,False),
 'pmsRoomStatus':('room_status:write','PMSRoomStatusCommand','PMSRoomStatus',200,False),
 'pmsAssignments':('pms:read',None,'PMSList',200,False),
 'pmsAssign':('pms:write','PMSAssignmentCommand','PMSAssignment',201,False),
 'pmsAssignmentRelease':('pms:write','PMSVersionCommand','PMSAssignment',200,False),
 'pmsStays':('pms:read',None,'PMSList',200,False),
 'pmsStayGet':('pms:read',None,'PMSStayDetail',200,False),
 'pmsCheckIn':('pms:write','PMSCheckInCommand','PMSCheckInResult',201,False),
 'pmsCheckOut':('pms:write','PMSCheckoutCommand','PMSCheckoutResult',200,False),
 'pmsFolios':('folios:read',None,'PMSList',200,False),
 'pmsFolioGet':('folios:read',None,'PMSFolioView',200,False),
 'pmsFolioReconcile':('folios:write','PMSVersionCommand','PMSFolioReconciled',200,False),
 'pmsCloseReviews':('folios:read',None,'PMSList',200,False),
 'pmsOperationExceptions':('pms:read',None,'PMSList',200,False),
 'pmsFiscalReviews':('folios:read',None,'PMSFiscalReviewList',200,False),
 'pmsFiscalReview':('folios:write','PMSFiscalReviewCommand','PMSFiscalReviewResult',201,True),
 'pmsFolioClose':('folios:write','PMSVersionCommand','PMSFolioClosed',200,True),
 'pmsFolioCloseReview':('folios:write','PMSFolioCloseReviewCommand','PMSFolioCloseReview',201,True),
 'pmsFolioReviewRevoke':('folios:write','PMSVersionCommand','PMSFolioReviewRevoked',201,True),
 'pmsFolioPost':('folios:write','PMSFolioPosting','PMSFolioPostingResult',201,False),
 }
def pms_queries(handler):
 if not handler.startswith('pms'):return None
 if handler=='pmsFrontDesk':return [('property_id','uuid',True),('reference','string',False)]
 return [('property_id','uuid',False),('branch_id','uuid',False)]
def pms_examples(uid):
 reason='Operator-reviewed current physical occupancy or obligation evidence'
 return {
 'PMSDayAutomationCommand':{'property_id':uid,'expected_version':0,'mode':'AUDIT_ONLY','hospitality_policy_version':1,'effective_at':'2026-12-01T00:00:00Z','expires_at':'2026-12-02T00:00:00Z','max_days_per_run':1,'approval_id':uid,'reason':'Authorize a bounded property night audit without waiving any exception'},
 'PMSFiscalReviewCommand':{'folio_version':2,'item_id':uid,'expected_review_version':0,'reason':'Derive original accepted fiscal source detail without repricing or adding a charge'},
 'PMSFolioCloseReviewCommand':{'version':2,'automatic_close':True,'expires_at':'2026-12-02T01:00:00Z','reason':reason},
 'PMSGroupCreate':{'property_id':uid,'name':'Reviewed conference group','source_reference':'actual-group-sales-reference','billing_reference':'group-billing-authority-reference','reason':reason},
 'PMSGroupBlockCommand':{'booking':{'property_id':uid,'source':'GROUP','channel':'','source_reference':'actual-group-booking-reference','state':'HOLD','rooms':[{'product_id':uid,'quote_id':uid,'product_version':1,'pool_version':1,'date_from':'2026-12-01','date_to':'2026-12-03','quantity':1}],'guests':[{'guest_id':uid,'source_kind':'DIRECT','source_reference':'actual-registered-source-reference','is_primary':True}],'payment_responsibility':'PLATFORM_COLLECT','group_id':uid,'reason':reason}},
 'PMSGroupPatch':{'version':1,'state':'CANCELLED','reason':reason},
 'PMSRoomingCommand':{'reservation_id':uid,'reservation_version':2,'reservation_room_id':uid,'slot':1,'expected_version':0,'guest_id':uid,'source_kind':'DIRECT','source_reference':'registered-guest-source-reference','registration_reference':'actual-arrival-registration-reference','state':'ACTIVE','approval_id':uid,'reason':reason},
 'PMSGroupCheckIns':{'items':[{'assignment_id':uid,'assignment_version':1,'reservation_version':4,'room_status_version':3,'guest_id':uid,'registration_reference':'actual-arrival-registration-reference','reason':reason}],'reason':reason},
 'PMSGroupCheckOuts':{'items':[{'stay_id':uid,'version':1,'reservation_version':5,'folio_version':1,'approval_id':uid,'reason':reason}],'reason':reason},
 'PMSMasterFolioCommand':{'property_id':uid,'group_id':uid,'folio_ids':[uid],'folio_versions':{uid:1},'currency':'NGN','billing_reference':'approved-group-billing-reference','approval_id':uid,'reason':reason},
 'PMSEventBookingCommand':{'property_id':uid,'group_id':uid,'reservation_id':uid,'reservation_version':2,'facility_id':uid,'source_reference':'actual-conference-booking-reference','conversation_reference':'opaque-cf-meet-conversation-reference','reason':reason},
 'PMSHospitalityPolicyCommand':{'property_id':uid,'expected_version':0,'cutoff_local':'04:00','approval_id':uid,'reason':reason},
 'PMSAuditCommand':{'property_id':uid,'reason':reason},
 'PMSDayCloseCommand':{'property_id':uid,'business_day_id':uid,'version':1,'audit_id':uid,'snapshot_digest':'a'*64,'reason':reason},
 'PMSDrawerCommand':{'property_id':uid,'code':'FRONT-DESK-1','currency':'NGN','reason':reason},
 'PMSCashierOpenCommand':{'drawer_id':uid,'opening_minor':10000,'reason':reason},
 'PMSCashierCloseCommand':{'version':1,'closing_minor':10000,'reason':reason},
 'PMSCashierHandoverCommand':{'version':2,'declared_minor':10000,'evidence_reference':'receiver-count-evidence-reference','reason':reason},
 'PMSCashAttestation':{'organisation_id':uid,'property_id':uid,'shift_id':uid,'source_version':1,'expected_movement_minor':0,'currency':'NGN','source_reference':'owning-payment-cash-reconciliation-reference','observed_at':'2026-11-02T00:00:00Z','expires_at':'2026-11-02T01:00:00Z'},
 'PMSOfflineCommand':{'property_id':uid,'client_operation_id':uid,'original_action_at':'2026-11-02T00:00:00Z','action':'FOLIO_POST','target_id':uid,'folio_posting':{'version':1,'kind':'MEALS','source_reference':'actual-offline-restaurant-order','quantity':1,'amount_minor':1200,'currency':'NGN','policy_reference':'accepted-service-policy-reference','reason':reason},'reason':reason},
 'PMSOfflineResolution':{'disposition':'RETRY_ONLINE','evidence_reference':'reviewed-stale-client-reconciliation-reference','reason':reason},
 'PMSRoomMoveCommand':{'stay_id':uid,'version':1,'reservation_version':3,'assignment_version':2,'room_id':uid,'room_status_version':3,'reason':reason},
 'PMSGuestRequestCommand':{'stay_id':uid,'kind':'SERVICE','evidence_reference':'actual-guest-request-reference','reason':reason},
 'PMSGuestRequestUpdate':{'version':1,'state':'RESOLVED','evidence_reference':'actual-service-fulfilment-evidence','reason':reason},
 'PMSRoomStatusCommand':{'property_id':uid,'room_id':uid,'expected_version':0,'state':'VACANT_DIRTY','evidence_reference':'actual-room-observation-reference','reason':reason},
 'PMSAssignmentCommand':{'reservation_id':uid,'reservation_version':2,'reservation_room_id':uid,'room_id':uid,'room_status_version':3,'slot':1,'reason':reason},
 'PMSVersionCommand':{'version':1,'reason':reason},
 'PMSCheckInCommand':{'assignment_id':uid,'assignment_version':1,'reservation_version':2,'room_status_version':3,'guest_id':uid,'registration_reference':'captured-registration-evidence-reference','reason':reason},
 'PMSCheckoutCommand':{'stay_id':uid,'version':1,'reservation_version':3,'folio_version':1,'approval_id':uid,'reason':reason},
 'PMSFolioPosting':{'version':1,'kind':'MEALS','source_reference':'actual-restaurant-order-reference','quantity':1,'amount_minor':1200,'currency':'NGN','policy_reference':'accepted-menu-and-tax-policy-reference','reason':reason},
 }
