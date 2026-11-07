"""Contracts for executable CRM identity/corporate commands, not future CRM modules."""

def register_crm(s,obj,arr,ref,typ):
 def shape(name,**fields):s[name]=obj(fields,list(fields),True)
 uid=typ('ids.ID');integer={'type':'integer','minimum':1}
 shape('CRMStayHistoryRebuilt',guest_id=uid,property_id=uid,reservations_reconciled={'type':'integer','minimum':0},source={'const':'OWNED_CANONICAL_RESERVATIONS_STAYS_AND_PAYMENT_READBACKS'})
 shape('CRMGuestRegistered',guest=ref('CRMGuest'),source_kind={'enum':['DIRECT','WALK_IN','LEGACY','CORPORATE']},source_reference={'type':'string'})
 shape('CRMGuestList',items=arr(ref('CRMGuestView')),limit={'type':'integer'},offset={'type':'integer'})
 shape('CRMRawList',items=arr({'type':'object','additionalProperties':True}),limit={'type':'integer'},offset={'type':'integer'})
 shape('CRMConsentResult',id=uid,guest_id=uid,property_id=uid,purpose={'enum':['OPERATIONAL_CONTACT','MARKETING','PREFERENCES','ACCESSIBILITY','LOYALTY']},state={'enum':['GRANTED','WITHDRAWN']},version=integer)
 shape('CRMPrivateResult',id=uid,guest_id=uid,property_id=uid,version=integer)
 shape('CRMPrivateView',guest_id=uid,property_id=uid,version={'type':'integer','minimum':0},preferences=ref('CRMPrivateProfile'))
 shape('CRMIdentityResult',id=uid,guest_id=uid,property_id=uid,source_kind={'type':'string'},state={'const':'ACTIVE'},version=integer)
 shape('CRMIdentityRevoked',id=uid,guest_id=uid,state={'const':'REVOKED'},version=integer)
 shape('CRMGuestScopeResult',id=uid,guest_id=uid,property_id=uid,state={'enum':['ACTIVE','REVOKED']},version=integer)
 shape('CRMCorporateScopeResult',id=uid,corporate_id=uid,property_id=uid,state={'enum':['ACTIVE','REVOKED']},version=integer)
 shape('CRMCorporateContactsView',corporate_id=uid,contacts=arr(ref('CRMCorporateContact')))
 shape('CRMTravelerResult',id=uid,guest_id=uid,corporate_id=uid,contract_id=uid,state={'enum':['ACTIVE','REVOKED']},version=integer)
 shape('CRMSourceAssertionResult',id=uid,source_version=integer,state={'enum':['ACTIVE','RESTRICTED','REVOKED']})
 for name,field,values in [('CRMGuestCreate','source_kind',['DIRECT','WALK_IN','LEGACY','CORPORATE']),('CRMGuestPatch','state',['ACTIVE','RESTRICTED','ARCHIVED']),('CRMGuest','state',['ACTIVE','RESTRICTED','ARCHIVED','ANONYMISED','MERGED']),('CRMConsentCommand','purpose',['OPERATIONAL_CONTACT','MARKETING','PREFERENCES','ACCESSIBILITY','LOYALTY']),('CRMConsentCommand','state',['GRANTED','WITHDRAWN']),('CRMCorporateContractCommand','state',['ACTIVE','SUSPENDED']),('CRMTravelerCommand','state',['ACTIVE','REVOKED']),('CRMGuestScopeCommand','state',['ACTIVE','REVOKED']),('CRMSourceAssertion','state',['ACTIVE','RESTRICTED','REVOKED'])]:s[name]['properties'][field]['enum']=values
 for name,fields in [('CRMCorporateContractCommand',['definition','effective_at','expires_at']),('CRMTravelerCommand',['policy','effective_at','expires_at'])]:
  s[name]['required']=[k for k in s[name].get('required',[]) if k not in fields]
  s[name]['allOf']=[{'if':{'properties':{'state':{'const':'ACTIVE'}},'required':['state']},'then':{'required':fields}}]
 s['CRMCorporateTerms']['properties']['currency'].update(pattern='^[A-Z]{3}$',example='NGN')
 s['CRMRecoveryCaseCreate']['properties']['issue_type']['enum']=['OVERBOOKING','ROOM_MOVE','SERVICE_FAILURE','BILLING_DISPUTE','ACCESS_FAILURE','DELAYED_CHECK_IN','CLEANLINESS','MAINTENANCE','INCORRECT_CHARGE','OTHER']
 for name in ('CRMRecoveryCaseCreate','CRMRecoveryCase'):s[name]['properties']['severity']['enum']=['LOW','MEDIUM','HIGH','CRITICAL']
 s['CRMRecoveryCase']['properties']['state']['enum']=['OPEN','ASSIGNED','INVESTIGATING','REMEDY_PROPOSED','PENDING_APPROVAL','APPROVED','EXECUTION_PENDING','RESOLVED','CLOSED','REJECTED','ESCALATED','CANCELLED']
 s['CRMRecoveryTransition']['properties']['action']['enum']=['ASSIGN','INVESTIGATE','ESCALATE','SUBMIT','REJECT_REMEDY','RESOLVE','REJECT','CANCEL','CLOSE','REOPEN']
 s['CRMRecoveryRemedyItem']['properties']['kind']['enum']=['ALTERNATIVE_ROOM','SERVICE_REPLACEMENT','COMPLIMENTARY_SERVICE','DISCOUNT','LOYALTY_BENEFIT','FUTURE_BOOKING_CREDIT','REFUND_REQUEST','FINANCIAL_ADJUSTMENT_REQUEST','OTHER_NONCASH']
 s['CRMRecoveryRemedyCommand']['properties']['remedies'].update(minItems=1,maxItems=10)
 s['CRMRecoveryApproval']['properties']['remedy_digest'].update(pattern='^[a-f0-9]{64}$',example='a'*64)
 for field in ('reservation_id','stay_id','assigned_to','approval_id'):s['CRMRecoveryCase']['properties'][field]={'anyOf':[uid,{'type':'null'}]}
 for field in ('remedy_digest','currency','outcome','communication_reference'):s['CRMRecoveryCase']['properties'][field]={'type':['string','null']}
 for field in ('follow_up_at','resolved_at','closed_at'):s['CRMRecoveryCase']['properties'][field]={'type':['string','null'],'format':'date-time'}
 s['CRMRecoveryCase']['properties']['remedy']={'type':['array','null'],'items':ref('CRMRecoveryRemedyItem')}
 shape('CRMRecoveryDetail',case=ref('CRMRecoveryCase'),actions=arr({'type':'object','additionalProperties':True}),compensations=arr({'type':'object','additionalProperties':True}))
 shape('CRMRecoveryOutcome',case=ref('CRMRecoveryCase'),compensation_id=uid,state={'enum':['EXECUTED','DECLINED']},execution_reference={'anyOf':[uid,{'type':'null'}]})
 s['LoyaltyProgramCommand']['properties']['state']['enum']=['ACTIVE','SUSPENDED','RETIRED']
 s['LoyaltyProgramDefinition']['properties']['allowed_sources']['items']['enum']=['DIRECT','MARKETPLACE','OTA','CORPORATE','WALK_IN','GROUP']
 s['LoyaltyRuleCommand']['properties']['kind']['enum']=['EARN_PER_NIGHT','EARN_PER_SPEND','QUALIFYING_STAY_BONUS']
 s['LoyaltyRuleCommand']['properties']['state']['enum']=['ACTIVE','RETIRED']
 s['LoyaltyRewardCommand']['properties']['kind']['enum']=['COMPLIMENTARY_SERVICE','LATE_CHECKOUT','UPGRADE_ELIGIBILITY','FUTURE_STAY_BENEFIT']
 s['LoyaltyRewardCommand']['properties']['state']['enum']=['ACTIVE','RETIRED']
 s['LoyaltyAccountView']['properties']['payment_wallet']={'const':False}
 s['LoyaltyAccountView']['properties']['transactions']=arr({'type':'object','additionalProperties':True})
 s['LoyaltyAccountView']['properties']['entitlements']=arr({'type':'object','additionalProperties':True})
 shape('LoyaltyRecorded',id=uid,version={'type':'integer','minimum':1})
 s['LoyaltyRecorded']['additionalProperties']=True
 shape('LoyaltyProgramList',items=arr({'type':'object','additionalProperties':True}))
 shape('LoyaltyLedgerResult',duplicate={'type':'boolean'})
 s['LoyaltyLedgerResult']['additionalProperties']=True
 shape('LoyaltyReconciled',account_id=uid,reversed_transaction_ids=arr(uid),expired_transaction_ids=arr(uid),balance_points={'type':'integer'})
 s['CRMPrivacyRequestCommand']['properties']['kind']['enum']=['EXPORT','ACCESS','CORRECTION','ERASURE','RESTRICTION']
 s['CRMPrivacyRequestCommand']['properties']['requester_kind']['enum']=['GUEST','AUTHORISED_REPRESENTATIVE']
 s['CRMPrivacyDecision']['properties']['decision']['enum']=['APPROVE','REJECT']
 s['CRMLegalHoldCommand']['properties']['scope']['enum']=['GUEST_PROFILE','ALL_GUEST_DATA']
 s['CRMVIPCommand']['properties']['tier']['enum']=['NONE','VIP','VVIP']
 s['CRMVIPCommand']['properties']['basis']['enum']=['OPERATOR_REVIEWED','LOYALTY_TIER','CORPORATE_CONTRACT','REPEAT_STAYS']
 s['CRMSegmentCommand']['properties']['kind']['enum']=['STAY_FREQUENCY','CORPORATE','LOYALTY_TIER','VIP','MARKETING_ELIGIBLE','SOURCE_CHANNEL']
 s['CRMSegmentCommand']['properties']['state']['enum']=['ACTIVE','RETIRED']
 s['CRMSegmentCommand']['description']='Closed source-backed criteria only; no free-form attributes, so protected personal characteristics cannot be targeted.'
 shape('CRMPrivacyRequestView',id=uid,guest_id=uid,property_id=uid,kind={'enum':['EXPORT','ACCESS','CORRECTION','ERASURE','RESTRICTION']},state={'enum':['RECEIVED','APPROVED','COMPLETED','REJECTED','BLOCKED_BY_LEGAL_HOLD','CANCELLED']},due_at={'type':'string','format':'date-time'},version=integer)
 s['CRMPrivacyRequestView']['additionalProperties']=True
 shape('CRMPrivacyExport',privacy_request_id=uid,guest_id=uid,property_id=uid,scope={'const':'BUSINESS_OWNED_CRM_DATA_FOR_PROPERTY'},data={'type':'object','additionalProperties':True},external_owners=arr({'type':'object','additionalProperties':True}))
 shape('CRMRecorded',id=uid)
 s['CRMRecorded']['additionalProperties']=True
 shape('CRMItems',items=arr({'type':'object','additionalProperties':True}))
 s['CRMItems']['additionalProperties']=True
 shape('CRMSegmentEvaluation',segment_key=uid,evaluation_id=uid,member_count={'type':'integer','minimum':0},joined={'type':'integer','minimum':0},exited={'type':'integer','minimum':0},source_digest={'type':'string','pattern':'^[a-f0-9]{64}$','example':'a'*64})
 s['CRMReport']['properties']['metrics']={'type':'object','additionalProperties':True}
 s['CRMReport']['properties']['cohorts']=arr({'type':'object','additionalProperties':True})
 s['CRMReport']['properties']['definitions']={'type':'object','additionalProperties':{'type':'string'}}
 return {
 'crmReport':('guests:read',None,'CRMReport',200,False),
 'crmPrivacyRequestCreate':('guest_privacy:write','CRMPrivacyRequestCommand','CRMPrivacyRequestView',201,False),
 'crmPrivacyRequests':('guest_privacy:write',None,'CRMRawList',200,False),
 'crmPrivacyRequestGet':('guest_privacy:write',None,'CRMPrivacyRequestView',200,False),
 'crmPrivacyDecision':('guest_privacy:write','CRMPrivacyDecision','CRMPrivacyRequestView',200,True),
 'crmPrivacyExport':('guest_private:read',None,'CRMPrivacyExport',200,True),
 'crmLegalHolds':('guest_privacy:write',None,'CRMItems',200,False),
 'crmLegalHoldCreate':('guest_privacy:write','CRMLegalHoldCommand','CRMRecorded',201,True),
 'crmLegalHoldRelease':('guest_privacy:write','CRMLegalHoldRelease','CRMRecorded',200,True),
 'crmVIPGet':('guests:read',None,'CRMItems',200,False),
 'crmVIPSave':('guests:write','CRMVIPCommand','CRMRecorded',201,False),
 'crmSegments':('guests:read',None,'CRMItems',200,False),
 'crmSegmentSave':('guests:write','CRMSegmentCommand','CRMRecorded',201,False),
 'crmSegmentEvaluate':('guests:write','Reason','CRMSegmentEvaluation',200,False),
 'crmSegmentMembers':('guests:read',None,'CRMItems',200,False),
 'crmRecoveryCases':('recovery:read',None,'CRMRawList',200,False),
 'crmRecoveryCaseCreate':('recovery:write','CRMRecoveryCaseCreate','CRMRecoveryCase',201,False),
 'crmRecoveryCaseGet':('recovery:read',None,'CRMRecoveryDetail',200,False),
 'crmRecoveryCaseTransition':('recovery:write','CRMRecoveryTransition','CRMRecoveryCase',200,False),
 'crmRecoveryRemedy':('recovery:write','CRMRecoveryRemedyCommand','CRMRecoveryCase',200,False),
 'crmRecoveryApprove':('recovery:approve','CRMRecoveryApproval','CRMRecoveryCase',200,True),
 'crmRecoveryCompensationOutcome':('recovery:write','CRMRecoveryExecution','CRMRecoveryOutcome',200,False),
 'loyaltyPrograms':('loyalty:read',None,'LoyaltyProgramList',200,False),
 'loyaltyProgramSave':('loyalty:write','LoyaltyProgramCommand','LoyaltyRecorded',201,True),
 'loyaltyRuleSave':('loyalty:write','LoyaltyRuleCommand','LoyaltyRecorded',201,True),
 'loyaltyRewardSave':('loyalty:write','LoyaltyRewardCommand','LoyaltyRecorded',201,True),
 'loyaltyEnrol':('loyalty:write','LoyaltyEnrolCommand','LoyaltyRecorded',201,False),
 'loyaltyAccountGet':('loyalty:read',None,'LoyaltyAccountView',200,False),
 'loyaltyAccrue':('loyalty:write','LoyaltyAccrualCommand','LoyaltyLedgerResult',201,False),
 'loyaltyReconcile':('loyalty:write','Reason','LoyaltyReconciled',200,False),
 'loyaltyRedeem':('loyalty:write','LoyaltyRedemptionCommand','LoyaltyLedgerResult',201,False),
 'crmStayHistory':('guests:read',None,'CRMStayHistoryList',200,False),
 'crmStayHistoryRebuild':('guests:write','CRMStayHistoryRebuild','CRMStayHistoryRebuilt',200,True),
 'crmGuestCreate':('guests:write','CRMGuestCreate','CRMGuestRegistered',201,False),
 'crmGuestList':('guests:read',None,'CRMGuestList',200,False),
 'crmGuestGet':('guests:read',None,'CRMGuestView',200,False),
 'crmGuestPatch':('guests:write','CRMGuestPatch','CRMGuest',200,True),
 'crmGuestLinks':('guests:read',None,'CRMRawList',200,False),
 'crmGuestLink':('guests:write','CRMIdentityLinkCommand','CRMIdentityResult',201,True),
 'crmGuestLinkRevoke':('guest_privacy:write','CRMVersionCommand','CRMIdentityRevoked',200,True),
 'crmGuestScope':('guests:write','CRMGuestScopeCommand','CRMGuestScopeResult',200,True),
 'crmGuestConsents':('guests:read',None,'CRMRawList',200,False),
 'crmGuestConsent':('guest_privacy:write','CRMConsentCommand','CRMConsentResult',201,False),
 'crmGuestPrivateSave':('guest_private:write','CRMPrivateCommand','CRMPrivateResult',200,False),
 'crmGuestPrivateGet':('guest_private:read',None,'CRMPrivateView',200,False),
 'crmCorporateCreate':('corporate:write','CRMCorporateCreate','CRMCorporateCustomer',201,True),
 'crmCorporateList':('corporate:read',None,'CRMRawList',200,False),
 'crmCorporateGet':('corporate:read',None,'CRMCorporateCustomer',200,False),
 'crmCorporatePatch':('corporate:write','CRMCorporatePatch','CRMCorporateCustomer',200,True),
 'crmCorporateContacts':('corporate:write',None,'CRMCorporateContactsView',200,True),
 'crmCorporateContracts':('corporate:read',None,'CRMRawList',200,False),
 'crmCorporateContractSave':('corporate:write','CRMCorporateContractCommand','CRMCorporateContract',201,True),
 'crmCorporateTravelers':('corporate:read',None,'CRMRawList',200,False),
 'crmCorporateTravelerSave':('corporate:write','CRMTravelerCommand','CRMTravelerResult',200,True),
 'crmCorporateScope':('corporate:write','CRMGuestScopeCommand','CRMCorporateScopeResult',200,True),
 }

def crm_queries(handler):
 if handler=='loyaltyPrograms':return [('property_id','uuid',True)]
 if handler in ('crmPrivacyRequests',):return [('property_id','uuid',True),('state','string',False)]
 if handler in ('crmSegments',):return [('property_id','uuid',True)]
 if handler=='crmReport':return [('property_id','uuid',True),('date_from','date',True),('date_to','date',True)]
 if handler in ('crmPrivacyRequestGet','crmPrivacyExport','crmLegalHoldRelease','crmSegmentEvaluate','crmSegmentMembers'):return []
 if handler=='crmRecoveryCases':return [('property_id','uuid',True),('state','string',False)]
 if not handler.startswith('crm'):return None
 params=[('property_id','uuid',False)]
 if handler=='crmGuestPrivateGet':params += [('purpose','string',True),('reason','string',True),('reservation_id','uuid',False)]
 if handler=='crmCorporateContacts':params += [('reason','string',True)]
 if handler in ('crmGuestList','crmCorporateList'):params += [('branch_id','uuid',False)]
 return params

def crm_examples(uid):
 terms={'rate_plan_ids':[uid],'allowed_sources':['CORPORATE'],'billing_responsibility':'PAY_AT_PROPERTY','billing_reference':'approved-company-billing-reference','max_booking_minor':100000,'currency':'NGN','require_named_travelers':True,'max_nights':30}
 return {
 'CRMPrivacyRequestCommand':{'kind':'EXPORT','requester_kind':'GUEST','verification_reference':'front-desk-identity-check-reference','reason':'Record a verified data-subject export request'},
 'CRMPrivacyDecision':{'version':1,'decision':'APPROVE','reason':'Approve the verified request within its legal scope'},
 'CRMLegalHoldCommand':{'scope':'ALL_GUEST_DATA','authority_reference':'court-order-or-counsel-instruction-reference','reason':'Preserve evidence under a legal hold'},
 'CRMLegalHoldRelease':{'version':1,'approval_id':uid,'reason':'Release the hold after independent legal review'},
 'CRMVIPCommand':{'expected_version':0,'tier':'VIP','basis':'REPEAT_STAYS','evidence_reference':'stay-history-review-reference','reason':'Classify the guest from source-backed stay history'},
 'CRMSegmentCommand':{'property_id':uid,'expected_version':0,'name':'Frequent guests','kind':'STAY_FREQUENCY','definition':{'min_stays':3,'window_days':365},'state':'ACTIVE','reason':'Define a source-backed stay frequency segment'},
 'CRMRecoveryCaseCreate':{'property_id':uid,'guest_id':uid,'reservation_id':uid,'issue_type':'OVERBOOKING','severity':'HIGH','summary':'Guest relocated after a physical room shortage','evidence_reference':'front-desk-incident-reference','reason':'Open a service recovery case for an actual affected guest'},
 'CRMRecoveryTransition':{'version':1,'action':'ASSIGN','assignee_id':uid,'reason':'Assign the case to the accountable duty manager'},
 'CRMRecoveryRemedyCommand':{'version':3,'remedies':[{'kind':'ALTERNATIVE_ROOM','amount_minor':0,'points':0,'description':'Comparable room at the partner property'},{'kind':'LOYALTY_BENEFIT','amount_minor':0,'points':500,'description':'Goodwill loyalty points'}],'reason':'Propose the exact remedy for independent approval'},
 'CRMRecoveryApproval':{'version':5,'remedy_digest':'a'*64,'approval_id':uid,'reason':'Approve the exact proposed remedy digest'},
 'CRMRecoveryExecution':{'version':1,'decline':False,'evidence_reference':'relocation-voucher-reference','reason':'Record the verified delivered remedy outcome'},
 'LoyaltyProgramCommand':{'property_id':uid,'expected_version':0,'name':'Property guest rewards','state':'ACTIVE','definition':{'tiers':[{'code':'BASE','min_points':0},{'code':'GOLD','min_points':1000}],'points_expiry_days':365,'corporate_excluded':True,'allowed_sources':['DIRECT','WALK_IN']},'effective_at':'2026-11-01T00:00:00Z','approval_id':uid,'reason':'Independently approved optional loyalty policy'},
 'LoyaltyRuleCommand':{'expected_version':0,'kind':'EARN_PER_NIGHT','state':'ACTIVE','definition':{'points_per_night':100,'points_per_major_unit':0,'bonus_points':0,'min_nights':1},'effective_at':'2026-11-01T00:00:00Z','approval_id':uid,'reason':'Independently approved earning rule'},
 'LoyaltyRewardCommand':{'expected_version':0,'name':'Late checkout','kind':'LATE_CHECKOUT','points_cost':300,'validity_days':90,'state':'ACTIVE','reason':'Define a non-cash reward'},
 'LoyaltyEnrolCommand':{'program_key':uid,'guest_id':uid,'reason':'Enrol the consenting guest'},
 'LoyaltyAccrualCommand':{'stay_id':uid,'reason':'Award the completed qualifying stay once'},
 'LoyaltyRedemptionCommand':{'reward_id':uid,'request_reference':uid,'reason':'Redeem the reward at the guest request'},
 'CRMStayHistoryRebuild':{'guest_version':1,'reason':'Reconcile current owned booking and physical stay facts without trusting event payloads'},
 'CRMGuestCreate':{'property_id':uid,'profile':{'display_name':'Example guest','language':'en'},'source_kind':'WALK_IN','registration_reference':'operator-captured-registration-reference','reason':'Record an actual local guest registration'},
 'CRMGuestPatch':{'version':1,'profile':{'display_name':'Corrected guest display name','language':'en'},'reason':'Correct an operator-reviewed business guest profile'},
 'CRMConsentCommand':{'expected_version':0,'purpose':'OPERATIONAL_CONTACT','state':'GRANTED','evidence_reference':'guest-captured-consent-form-reference','reason':'Record explicit captured purpose-specific consent'},
 'CRMPrivateCommand':{'expected_version':0,'preferences':{'email':'disposable@example.invalid','contact_preference':'EMAIL'},'reason':'Store protected contact only after current operational consent'},
 'CRMIdentityLinkCommand':{'guest_version':1,'source_kind':'MARKETPLACE','source_reference':'opaque-marketplace-customer-reference','projection_id':uid,'approval_id':uid,'evidence_reference':'reviewed-source-and-registration-evidence','reason':'Link authenticated source evidence after independent review'},
 'CRMGuestScopeCommand':{'property_id':uid,'expected_version':0,'state':'ACTIVE','approval_id':uid,'reason':'Expand property scope only under exact independent approval'},
 'CRMVersionCommand':{'version':1,'reason':'Revoke an exact current identity link without erasing evidence'},
 'CRMCorporateCreate':{'property_id':uid,'legal_name':'Example corporate customer','registration_reference':'corporate-registration-reference','contacts':[{'name':'Travel officer','email':'disposable@example.invalid','representative_reference':'company-authority-evidence-reference'}],'reason':'Register protected corporate customer contacts'},
 'CRMCorporatePatch':{'version':1,'state':'SUSPENDED','reason':'Suspend new corporate booking eligibility'},
 'CRMCorporateContractCommand':{'expected_version':0,'state':'ACTIVE','definition':terms,'effective_at':'2026-11-02T00:00:00Z','expires_at':'2027-01-01T00:00:00Z','approval_id':uid,'reason':'Approve canonical negotiated plans and bounded billing responsibility'},
 'CRMTravelerCommand':{'guest_id':uid,'contract_id':uid,'expected_version':0,'state':'ACTIVE','policy':terms,'effective_at':'2026-11-02T00:00:00Z','expires_at':'2027-01-01T00:00:00Z','approval_id':uid,'reason':'Authorize a named traveler within the exact corporate contract'},
 'CRMSourceAssertion':{'organisation_id':uid,'connection_id':uid,'property_id':uid,'source_reference':'opaque-customer-reference','source_version':1,'state':'ACTIVE','expires_at':'2026-11-02T13:00:00Z','evidence_reference':'owning-source-assertion-reference'},
 }
