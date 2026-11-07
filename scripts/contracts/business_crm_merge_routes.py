"""Contracts for authorized guest merge and durable CRM notifications (Section 11)."""

KINDS=['WAITLIST_OFFER','WAITLIST_EXPIRED','RESERVATION_CHANGE','ARRIVAL_NOTICE','STAY_NOTICE','SERVICE_RECOVERY_UPDATE','COMPENSATION_STATUS','LOYALTY_ENROLLED','LOYALTY_REWARD','CORPORATE_BOOKING_UPDATE']

def register_crm_merge(s,obj,arr,ref,typ):
 def shape(name,**fields):s[name]=obj(fields,list(fields),True)
 uid=typ('ids.ID');nuid={'anyOf':[uid,{'type':'null'}]};ntime={'type':['string','null'],'format':'date-time'};nstr={'type':['string','null']}
 s['CRMGuestMergePreviewCommand']['properties']['match_basis']['enum']=['SHARED_SOURCE_IDENTITY','VERIFIED_OWNERSHIP']
 for name in ('CRMGuestMerge','CRMMergePreview'):s[name]['properties']['risk']['enum']=['STANDARD','HIGH']
 s['CRMGuestMerge']['properties']['state']['enum']=['PREVIEWED','MERGED','REVERSED','CANCELLED']
 s['CRMGuestMerge']['properties']['approval_id']=nuid
 for field in ('merged_at','reversed_at'):s['CRMGuestMerge']['properties'][field]=ntime
 s['CRMGuestMerge']['properties']['reversal_reason']=nstr
 for name in ('CRMGuestMerge','CRMGuestMergeExecute'):s[name]['properties']['preview_digest'].update(pattern='^[a-f0-9]{64}$',example='a'*64)
 shape('CRMGuestMergeList',items=arr({'type':'object','additionalProperties':True}),canonical_guest_id=uid,limit={'type':'integer'},offset={'type':'integer'})
 t=s['CRMNotificationTemplateCommand']['properties']
 t['kind']['enum']=KINDS
 t['purpose']['enum']=['OPERATIONAL','MARKETING']
 t['provider_template_key'].update(pattern='^[a-z0-9][a-z0-9._-]{2,99}$',example='business.guest.waitlist_offer')
 t['channels'].update(minItems=1,maxItems=3,items={'enum':['EMAIL','SMS','PUSH']})
 s['CRMNotificationTemplate']['properties']['state']['enum']=['ACTIVE','RETIRED']
 s['CRMNotificationTemplate']['properties']['retired_at']=ntime
 s['CRMNotificationSuppressionCommand']['properties']['scope']['enum']=['ALL','MARKETING','KIND']
 s['CRMNotificationSuppressionCommand']['properties']['kind']['enum']=KINDS
 s['CRMNotificationSuppression']['properties']['kind']=nstr
 s['CRMNotificationSuppression']['properties']['lifted_at']=ntime
 s['CRMNotificationSuppression']['properties']['state']['enum']=['ACTIVE','LIFTED']
 j=s['CRMNotificationJob']['properties']
 j['state']['enum']=['PENDING','RUNNING','RETRY','ACCEPTED','SUPPRESSED','CANCELLED','EXPIRED','FAILED','BLOCKED']
 j['delivery_status']['enum']=['NOT_SENT','ACCEPTED_BY_NOTIFICATION_SERVICE','PROVIDER_SUPPRESSED']
 j['kind']['enum']=KINDS
 for field in ('guest_id','corporate_id','resolved_guest_id','template_id','provider_notification_id'):j[field]=nuid
 for field in ('channel','lease_owner','outcome_reason','last_error'):j[field]=nstr
 j['template_version']={'type':['integer','null']}
 j['accepted_at']=ntime
 j['provider_suppressed']={'type':['array','null'],'items':{'type':'object','additionalProperties':True}}
 s['CRMNotificationJobDetail']['properties']['delivery_confirmation']={'const':'UNKNOWN'}
 return {
 'crmGuestMergePreview':('guests:write','CRMGuestMergePreviewCommand','CRMGuestMerge',201,False),
 'crmGuestMerges':('guests:read',None,'CRMGuestMergeList',200,False),
 'crmGuestMergeGet':('guests:read',None,'CRMGuestMergeDetail',200,False),
 'crmGuestMergeExecute':('guest_privacy:write','CRMGuestMergeExecute','CRMGuestMergeResult',200,True),
 'crmGuestMergeCancel':('guests:write','CRMVersionCommand','CRMGuestMerge',200,False),
 'crmGuestMergeReverse':('guest_privacy:write','CRMGuestMergeReverse','CRMGuestMergeResult',200,True),
 'crmNotificationTemplates':('guests:read',None,'CRMItems',200,False),
 'crmNotificationTemplateCreate':('guest_privacy:write','CRMNotificationTemplateCommand','CRMNotificationTemplate',201,True),
 'crmNotificationTemplateRetire':('guest_privacy:write','CRMReasonCommand','CRMNotificationTemplate',200,True),
 'crmNotificationJobs':('guests:read',None,'CRMRawList',200,False),
 'crmNotificationJobGet':('guests:read',None,'CRMNotificationJobDetail',200,False),
 'crmNotificationJobCancel':('guests:write','CRMVersionCommand','CRMNotificationJob',200,False),
 'crmNotificationJobRetry':('guests:write','CRMVersionCommand','CRMNotificationJob',200,False),
 'crmNotificationSuppressions':('guests:read',None,'CRMItems',200,False),
 'crmNotificationSuppressionCreate':('guests:write','CRMNotificationSuppressionCommand','CRMNotificationSuppression',201,False),
 'crmNotificationSuppressionLift':('guest_privacy:write','CRMVersionCommand','CRMNotificationSuppression',200,True),
 'crmWaitlistGuestBind':('reservations:write','CRMWaitlistGuestBinding','CRMWaitlistGuestBound',201,False),
 }

def crm_merge_queries(handler):
 if handler=='crmNotificationTemplates':return [('property_id','uuid',True)]
 if handler=='crmNotificationJobs':return [('property_id','uuid',True),('guest_id','uuid',False),('source_kind','string',False),('source_id','uuid',False),('state','string',False)]
 if handler in ('crmGuestMergeGet','crmGuestMergeExecute','crmGuestMergeCancel','crmGuestMergeReverse','crmNotificationTemplateCreate','crmNotificationTemplateRetire','crmNotificationJobGet','crmNotificationJobCancel','crmNotificationJobRetry','crmWaitlistGuestBind'):return []
 return None

def crm_merge_examples(uid):
 return {
 'CRMGuestMergePreviewCommand':{'target_guest_id':uid,'match_basis':'SHARED_SOURCE_IDENTITY','ownership_evidence_reference':'front-desk-identity-review-reference','reason':'Preview a source-backed duplicate guest merge'},
 'CRMGuestMergeExecute':{'version':1,'preview_digest':'a'*64,'approval_id':uid,'reason':'Execute the exact reviewed merge preview'},
 'CRMGuestMergeReverse':{'version':2,'approval_id':uid,'reason':'Reverse an incorrect merge from its recorded lineage'},
 'CRMNotificationTemplateCommand':{'property_id':uid,'kind':'WAITLIST_OFFER','provider_template_key':'business.guest.waitlist_offer','purpose':'OPERATIONAL','channels':['EMAIL','SMS'],'approval_id':uid,'reason':'Approve the operational waitlist offer template'},
 'CRMReasonCommand':{'reason':'Retire the template and stop new deliveries'},
 'CRMNotificationSuppressionCommand':{'scope':'MARKETING','evidence_reference':'guest-unsubscribe-request-reference','reason':'Record the guest request to stop marketing messages'},
 'CRMWaitlistGuestBinding':{'guest_id':uid,'evidence_reference':'waitlist-request-registration-reference','reason':'Bind the registered guest who requested this waitlist entry'},
 }
