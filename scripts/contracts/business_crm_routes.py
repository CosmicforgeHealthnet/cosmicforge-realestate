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
 return {
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
 if not handler.startswith('crm'):return None
 params=[('property_id','uuid',False)]
 if handler=='crmGuestPrivateGet':params += [('purpose','string',True),('reason','string',True),('reservation_id','uuid',False)]
 if handler=='crmCorporateContacts':params += [('reason','string',True)]
 if handler in ('crmGuestList','crmCorporateList'):params += [('branch_id','uuid',False)]
 return params

def crm_examples(uid):
 terms={'rate_plan_ids':[uid],'allowed_sources':['CORPORATE'],'billing_responsibility':'PAY_AT_PROPERTY','billing_reference':'approved-company-billing-reference','max_booking_minor':100000,'currency':'NGN','require_named_travelers':True,'max_nights':30}
 return {
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
