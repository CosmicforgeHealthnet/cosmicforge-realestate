"""Reviewed Sections 6–7 route metadata; each entry must match a real registration."""
import re


def register_commercial(root, schemas, route, obj, arr, ref, typ):
    s = schemas
    s['CommercialFieldAuthority']['properties']['value']={}
    def shape(schema_name, **fields):
        s[schema_name] = obj({key: (ref(value) if isinstance(value, str) else value) for key, value in fields.items()})
    shape('CommercialRateResult', rate_plan='Plan', rate_version={'anyOf': [ref('RateVersion'), {'type': 'null'}]})
    shape('CommercialListingDetail', listing='CommercialListing', current_version='CommercialListingVersion', quality_score={'type':'integer'}, issues=arr({'type':'string'}))
    shape('CommercialQuoteDetail', quote='QuoteSnapshot', expired={'type':'boolean'})
    shape('CommercialCalendarResult', pool_id=typ('ids.ID'), pool_version={'type':'integer'}, timezone={'type':'string'}, channel={'type':'string'}, days=arr(ref('Day')), as_of=typ('time.Time'), commercial_eligibility_checked={'type':'boolean'})
    shape('CommercialPoolAvailability',product_id=typ('ids.ID'),property_id=typ('ids.ID'),pool_id=typ('ids.ID'),product_version={'type':'integer'},pool_version={'type':'integer'},rate_plan_version={'type':'integer'},available_quantity={'type':'integer'},eligible={'type':'boolean'},ineligible_reason={'type':'string'},days=arr(ref('Day')),timezone={'type':'string'})
    shape('CommercialAvailabilityResult',items=arr(ref('CommercialPoolAvailability')),as_of=typ('time.Time'),date_semantics={'const':'PROPERTY_LOCAL_CHECKOUT_EXCLUSIVE'})
    shape('CommercialCalendarWrite',pool_id=typ('ids.ID'),days=arr(ref('Day')))
    shape('CommercialContentReadiness',listing_id=typ('ids.ID'),version={'type':'integer'},listing_version_id=typ('ids.ID'),score={'type':'integer'},issues=arr({'type':'string'}),content_ready={'type':'boolean'},live_commercial_eligibility_checked={'const':False})
    shape('CommercialPublicationQueued',publication_id=typ('ids.ID'),job_id=typ('ids.ID'),desired_state={'enum':['PUBLISHED','PAUSED','WITHDRAWN']},delivery_state={'const':'PENDING'},canonical_version={'type':'integer'})
    shape('CommercialListingStopped',listing='CommercialListing',publications=arr(ref('CommercialPublicationQueued')),provider_state={'const':'PENDING_ACKNOWLEDGEMENT'})
    shape('CommercialMappingPreviewResult',preview_id=typ('ids.ID'),connection_version={'type':'integer'},digest={'type':'string'},entries=arr(ref('MappingReference')),expires_at=typ('time.Time'))
    shape('CommercialMappingResult',connection_id=typ('ids.ID'),version={'type':'integer'},mapping_state={'const':'CONFIRMED'},entries=arr(ref('MappingReference')))
    shape('CommercialMappingList',PROPERTY=arr({'type':'object'}),PRODUCT=arr({'type':'object'}),RATE_PLAN=arr({'type':'object'}))
    shape('CommercialResyncResult',job_ids=arr(typ('ids.ID')),state={'const':'QUEUED'})
    shape('CommercialConflictResult',conflict_id=typ('ids.ID'),state={'enum':['RETRY','REJECTED']},version={'type':'integer'},job_ids=arr(typ('ids.ID')))
    shape('CommercialConflictDetail',conflict='CommercialChannelConflict',provider_code={'type':'string'},history=arr({'type':'object'}),limit={'type':'integer'},offset={'type':'integer'})
    shape('CommercialOverrideResult',version={'type':'integer'},resync_required={'const':True})
    shape('CommercialKeyResult',connection_id=typ('ids.ID'),version={'type':'integer'},callback_signing_configured={'const':True})
    shape('CommercialCommissionResult',id=typ('ids.ID'),version={'type':'integer'},basis={'const':'CONTRACTUAL_ESTIMATE_RECONCILED_ACTUALS_REQUIRED'})
    shape('CommercialInputResult',input_id=typ('ids.ID'))
    shape('CommercialCurrencyResult',items=arr(ref('CommercialCurrencyPolicy')),default_precision={'const':'SHARED_ISO4217_MINOR_UNITS'},default_rounding={'const':'HALF_AWAY_FROM_ZERO'},implicit_fx={'const':False})
    # These projections contain provider-specific capability/evidence keys. Their
    # fixed control fields are typed and their variable evidence stays bounded
    # by the database and normalized provider contracts.
    for name in ('CommercialProviderList','CommercialChannelHealth','CommercialChannelMetrics','CommercialChannelProfitability','CommercialRawList'):
        s[name]={'type':'object','additionalProperties':True}
    s['CommercialRawList']['properties']={'items':arr({'type':'object'})}
    s['CommercialChannelHealth']['properties']={'connection':ref('CommercialChannelConnection'),'status':{'type':'string'},'cursor':{'type':'object'},'queued_jobs':{'type':'integer'},'unconfirmed_publications':{'type':'integer'},'confirmed_publications':{'type':'integer'},'last_success_at':{'type':['string','null'],'format':'date-time'}}
    s['CommercialChannelProfitability']['properties']={'status':{'enum':['INCOMPLETE_INPUTS','RECONCILED_SOURCE_INPUTS']},'currency':{'type':'string'},'components_minor':{'type':'object','additionalProperties':{'type':['integer','null']}},'net_contribution_minor':{'type':['integer','null']},'missing_components':arr({'type':'string'}),'stale_inputs':arr({'type':'string'}),'unreconciled_inputs':arr({'type':'string'}),'provenance':arr(ref('CommercialFinancialInput')),'financial_execution':{'const':False}}
    # handler: capability, request, response, status, MFA. Mutation retries use
    # the common authenticated transactional idempotency store unless noted.
    data={
      'productList':('products:read',None,'List:Product',200,False),
      'productCreate':('products:write','CommercialProductCreate','Product',201,True),
      'productGet':('products:read',None,'Product',200,False),
      'productPatch':('products:write','CommercialProductPatch','Product',200,True),
      'poolList':('pools:read',None,'List:Pool',200,False),
      'poolCreate':('pools:write','CommercialPoolCreate','Pool',201,True),
      'poolGet':('pools:read',None,'Pool',200,False),
      'poolPatch':('pools:write','CommercialStateChange','Pool',200,True),
      'calendarSet':('calendar:write','CommercialCalendarCommand','CommercialCalendarWrite',200,True),
      'calendarGet':('calendar:read',None,'CommercialCalendarResult',200,False),
      'availabilityGet':('calendar:read',None,'CommercialAvailabilityResult',200,False),
      'holdCreate':('holds:write','HoldCommand','Hold',201,False),
      'holdList':('holds:read',None,'List:Hold',200,False),
      'holdGet':('holds:read',None,'Hold',200,False),
      'holdCancel':('holds:write','CommercialHoldChange','Hold',200,False),
      'holdExtend':('holds:write','CommercialHoldChange','Hold',200,True),
      'holdHandoff':('holds:write',None,'Error',503,True),
      'allotmentList':('allotments:read',None,'List:CommercialAllotment',200,False),
      'allotmentCreate':('allotments:write','CommercialAllotmentCreate','CommercialAllotment',201,True),
      'allotmentRelease':('allotments:write','CommercialStateChange','CommercialAllotment',200,True),
      'rateList':('rates:read',None,'List:Plan',200,False),
      'rateCreate':('rates:write','CommercialRateCreate','CommercialRateResult',201,True),
      'rateGet':('rates:read',None,'Plan',200,False),
      'ratePatch':('rates:write','CommercialRatePatch','CommercialRateResult',200,True),
      'rateVersions':('rates:read',None,'List:RateVersion',200,False),
      'rateVersionGet':('rates:read',None,'RateVersion',200,False),
      'restrictionList':('restrictions:read',None,'List:RestrictionRecord',200,False),
      'restrictionCreate':('restrictions:write','CommercialRestrictionCreate','RestrictionRecord',201,True),
      'restrictionPatch':('restrictions:write','CommercialStateChange','RestrictionRecord',200,True),
      'promotionList':('promotions:read',None,'List:Promotion',200,False),
      'promotionCreate':('promotions:write','CommercialPromotionCreate','Promotion',201,True),
      'promotionPatch':('promotions:write','CommercialStateChange','Promotion',200,True),
      'addOnList':('promotions:read',None,'List:AddOn',200,False),
      'addOnCreate':('promotions:write','CommercialAddOnCreate','AddOn',201,True),
      'addOnPatch':('promotions:write','CommercialStateChange','AddOn',200,True),
      'packageList':('promotions:read',None,'List:CommercialPackageDetail',200,False),
      'packageCreate':('promotions:write','CommercialPackageCreate','CommercialPackageDetail',201,True),
      'packagePatch':('promotions:write','CommercialStateChange','Package',200,True),
      'quoteCreate':('pricing:read','CommercialQuoteCommand','QuoteSnapshot',201,False),
      'quoteGet':('pricing:read',None,'CommercialQuoteDetail',200,False),
      'bulkRatePreview':('rates:write','CommercialBulkRateCommand','CommercialBulkView',201,True),
      'bulkRateApply':('rates:write','CommercialBulkApply','CommercialBulkView',202,True),
      'bulkRateRollback':('rates:write','CommercialBulkRollback','CommercialBulkView',201,True),
      'bulkRateGet':('rates:read',None,'CommercialBulkView',200,False),
      'taxList':('tax:read',None,'List:CommercialTaxProfile',200,False),
      'taxCreate':('tax:write','CommercialTaxCreate','CommercialTaxProfile',201,True),
      'currencyPolicyGet':('tax:read',None,'CommercialCurrencyResult',200,False),
      'currencyPolicySet':('tax:write','CommercialCurrencyCommand','CommercialCurrencyPolicy',200,True),
      'complianceHoldList':('restrictions:read',None,'List:CommercialComplianceHold',200,False),
      'complianceHoldCreate':('restrictions:write','CommercialComplianceCreate','CommercialComplianceHold',201,True),
      'complianceHoldRevoke':('restrictions:write','CommercialStateChange','CommercialComplianceHold',200,True),
      'templateList':('content:read',None,'CommercialRawList',200,False),
      'templateCreate':('content:write','CommercialTemplateCommand','CommercialTemplateView',201,True),
      'templatePatch':('content:write','CommercialTemplateCommand','CommercialTemplateView',200,True),
      'listingList':('content:read',None,'List:CommercialListingDetail',200,False),
      'listingCreate':('content:write','CommercialListingCommand','CommercialListingDetail',201,True),
      'listingGet':('content:read',None,'CommercialListingDetail',200,False),
      'listingPatch':('content:write','CommercialListingCommand','CommercialListingDetail',200,True),
      'listingVersions':('content:read',None,'List:CommercialListingVersion',200,False),
      'listingReadiness':('content:read',None,'CommercialContentReadiness',200,False),
      'listingSubmit':('content:write','CommercialStateChange','CommercialListing',200,True),
      'listingPublish':('publication:write','CommercialPublishCommand','CommercialPublicationQueued',202,True),
      'listingPause':('publication:write','CommercialStateChange','CommercialListingStopped',202,True),
      'listingWithdraw':('publication:write','CommercialStateChange','CommercialListingStopped',202,True),
      'listingPublication':('channels:read',None,'List:CommercialPublication',200,False),
      'channelProviders':('channels:read',None,'CommercialProviderList',200,False),
      'channelList':('channels:read',None,'List:CommercialChannelConnection',200,False),
      'channelCreate':('channels:write','CommercialChannelCreate','CommercialChannelConnection',201,True),
      'channelGet':('channels:read',None,'CommercialChannelConnection',200,False),
      'channelPatch':('channels:write','CommercialStateChange','CommercialChannelConnection',200,True),
      'channelAuthorize':('channels:credentials','CommercialChannelAuthorize','CommercialChannelConnection',200,True),
      'channelWebhookKeySet':('channels:credentials','CommercialWebhookKeyCommand','CommercialKeyResult',200,True),
      'channelMappings':('channels:read',None,'CommercialMappingList',200,False),
      'channelMappingPreview':('channels:mappings','CommercialMappingPreview','CommercialMappingPreviewResult',201,True),
      'channelMappingConfirm':('channels:mappings','CommercialMappingConfirm','CommercialMappingResult',200,True),
      'channelFieldList':('channels:read',None,'CommercialRawList',200,False),
      'channelContentOverrides':('channels:read',None,'CommercialRawList',200,False),
      'channelFieldSet':('channels:override','CommercialFieldAuthority','CommercialFieldAuthorityResult',200,True),
      'channelContentOverride':('channels:override','CommercialContentOverride','CommercialOverrideResult',200,True),
      'channelHealth':('channels:read',None,'CommercialChannelHealth',200,False),
      'channelResync':('channels:write','CommercialStateChange','CommercialResyncResult',202,True),
      'channelJobs':('channels:read',None,'CommercialRawList',200,False),
      'channelSyncHistory':('channels:read',None,'List:CommercialSyncFailure',200,False),
      'channelReceipts':('channels:read',None,'CommercialRawList',200,False),
      'channelMetrics':('channels:report',None,'CommercialChannelMetrics',200,False),
      'channelProfitability':('channels:report',None,'CommercialChannelProfitability',200,False),
      'channelCommissionList':('channels:read',None,'CommercialRawList',200,False),
      'channelCommissionCreate':('channels:write','CommercialCommissionCommand','CommercialCommissionResult',201,True),
      'channelConflicts':('channels:read',None,'List:CommercialChannelConflict',200,False),
      'channelConflictGet':('channels:read',None,'CommercialConflictDetail',200,False),
      'channelConflictResolve':('channels:resolve','CommercialConflictDecision','CommercialConflictResult',202,True),
      'internalChannelReceipt':('signed:marketplace-service','CommercialReceiptCommand','CommercialReceiptResult',202,False),
      'internalChannelFinancialInput':('signed:financial-owner','CommercialFinancialInputCommand','CommercialInputResult',201,False),
      'channelProviderWebhook':('signed:provider-callback','CommercialReceiptCommand','CommercialReceiptResult',202,False),
    }
    shape('CommercialPackageDetail', **{'package':'Package','components':arr(ref('CommercialPackageComponent'))})
    shape('CommercialTemplateView',id=typ('ids.ID'),organisation_id=typ('ids.ID'),name={'type':'string'},definition='CommercialTemplateDefinition',version={'type':'integer'},created_at=typ('time.Time'))
    shape('CommercialFieldAuthorityResult',field={'type':'string'},mode={'type':'string'},version={'type':'integer'},resync_required={'const':True})
    s['CommercialReceiptResult']['properties']['canonical_reservation_accepted']={'const':False}
    s['QuoteSnapshot']['properties']['is_reservation']={'const':False}
    found=set()
    for path in sorted((root/'services/business-operations-service/internal/http').glob('commercial*.go')):
        source=path.read_text(encoding='utf-8')
        for method, suffix, handler in re.findall(r'r\.(Get|Post|Put|Patch|Delete)\("([^"\n]+)",\s*h\.(\w+)\)',source):
            if handler not in data:raise RuntimeError('Commercial handler lacks reviewed contract: '+handler)
            cap,request,response,status,mfa=data[handler]
            private=handler.startswith('internal');webhook=handler=='channelProviderWebhook'
            note='Current canonical graph, tenant scope and policy versions are revalidated. Exact minor-unit money; no implicit FX or future-domain execution. '
            if handler=='holdHandoff':note='Dependency-gated: returns 503 because canonical Section 9 reservation acceptance is unavailable. No hold is consumed and no reservation is fabricated.'
            if handler in ('channelAuthorize','channelMappingPreview','listingPublish'):note+='A real authenticated provider operation and certification evidence are required. Unavailable dependencies return 503; test fakes do not certify staging. '
            if handler in ('listingPublish','listingPause','listingWithdraw','channelResync'):note+='202 means durable desired state/job only. Provider sellability remains pending/unknown until acknowledgement AND matching readback. '
            if handler in ('bulkRateApply','bulkRateRollback'):note+='Apply queues the exact approved preview; rollback first creates a new preview requiring its own independent approval. Workers recheck initiating authority and canonical versions. '
            if handler=='channelWebhookKeySet':note+='32-byte callback secret is encrypted with connection/tenant AAD and is never echoed, audited or cached. Approval operation uses secret_fingerprint SHA-256 instead of key_base64. '
            if handler in ('internalChannelReceipt','channelProviderWebhook'):note+='Durable replay/object-version dedupe; reservation events quarantine with SECTION9_CANONICAL_RESERVATION_AUTHORITY_UNAVAILABLE and never acknowledge a canonical booking.'
            route(method.upper(),suffix,cap,request,response,status,method!='Get' and not private and not webhook and handler!='holdHandoff',mfa,notes=note)
            if private:route_list_path='/internal/v1/business-supply'+suffix
            elif webhook:route_list_path='/webhooks/v1/business-channels'+suffix
            else:route_list_path=None
            if route_list_path:
                # route's only side effect is appending the reviewed record.
                route.__globals__['R'][-1]['path']=route_list_path
                route.__globals__['R'][-1]['transport']='internal' if private else 'webhook'
            route.__globals__['R'][-1]['handler']=handler
            found.add(handler)
    if set(data)!=found:raise RuntimeError('Documented commercial handlers not registered: '+str(set(data)-found))

def commercial_queries(handler):
    sets={
      'calendarGet':[('pool_id','uuid',True),('date_from','date',True),('date_to','date',True),('channel','string',False)],
      'availabilityGet':[('product_id','uuid',False),('property_id','uuid',False),('node_id','uuid',False),('date_from','date',True),('date_to','date',True),('adults','integer',False),('children','integer',False),('channel','string',False)],
      'currencyPolicyGet':[('property_id','uuid',True)],
      'channelProfitability':[('date_from','date',True),('date_to','date',True),('currency','string',True),('product_id','uuid',False),('rate_plan_id','uuid',False),('promotion_id','uuid',False)],
    }
    return sets.get(handler,[])
