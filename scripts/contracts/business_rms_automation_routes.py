"""Executable Section 8 RMS automation, analytics and advanced recommendation contracts."""

def register_rms_automation(s,obj,arr,ref,typ):
 def shape(name,**fields):s[name]=obj(fields,list(fields),True)
 sha={'type':'string','pattern':'^[a-f0-9]{64}$','example':'a'*64}
 s['RMSRawObject']={'type':'object','additionalProperties':True}
 mode=['APPROVAL_AUTOMATION','AUTOPILOT_WITH_LIMITS']
 for name in ('RMSAutomationActivation','RMSAutomationActivateCommand'):s[name]['properties']['mode']['enum']=mode
 s['RMSAutomationActivation']['properties']['state']['enum']=['ACTIVE','PAUSED','REVOKED','EXPIRED']
 s['RMSAutomationActivation']['properties']['limits_digest'].update(sha)
 s['RMSAutomationRun']['properties']['stage']['enum']=['QUEUED','SOURCE_CAPTURED','FORECAST_READY','RECOMMENDED','APPROVAL_PENDING','APPROVED','APPLIED','NO_CHANGE','BLOCKED','REJECTED','EXPIRED','FAILED']
 s['RMSAutomationRun']['properties']['trigger']['enum']=['SCHEDULED','MANUAL']
 s['RMSAutomationRun']['properties']['mode']['enum']=mode
 s['AutomationLimits']['properties']['compliance_restrictions']['items']['enum']=['DECREASE_ONLY','INCREASE_ONLY']
 s['AutomationLimits']['properties']['eligible_models'].update(minItems=1,maxItems=8)
 for field,lo,hi in [('max_daily_change_bps',0,1000),('max_cumulative_change_bps',0,5000),('approval_threshold_bps',0,1000),('horizon_days',1,90),('history_days',21,366),('run_interval_seconds',300,86400),('drift_threshold_bps',1,10000),('minimum_coverage_bps',5000,10000)]:
  s['AutomationLimits']['properties'][field].update(minimum=lo,maximum=hi)
 s['RMSMarketInputCommand']['properties']['kind']['enum']=['HOLIDAY','LOCAL_EVENT','COMPETITOR_RATE','MARKET_DEMAND_INDEX','CHANNEL_COMMISSION','OPERATING_COST']
 s['RMSMarketInputCommand']['properties']['evidence_digest'].update(sha)
 for name in ('RMSAnalyticsCommand','RMSAnalyticsSnapshot'):s[name]['properties']['scope_kind']['enum']=['PROPERTY','BRANCH','PORTFOLIO']
 for field in ('input_digest','result_digest'):s['RMSAnalyticsSnapshot']['properties'][field].update(sha)
 s['AnalyticsResult']['properties']['model']['const']='canonical-pace-cancellation-v1'
 s['AnalyticsResult']['properties']['is_calibrated_probability']['const']=False
 s['AnalyticsResult']['properties']['status']['enum']=['BASELINE_READY','INSUFFICIENT_DATA']
 s['RMSScenarioResult']['properties']['mutated']['const']=False
 s['ScenarioSimulation']['properties']['basis']['const']='FIXED_FORECAST_DEMAND_ALTERNATIVES'
 kinds=['DERIVED_RATE','RATE_RANGE','MIN_LOS','MAX_LOS','BOOKING_WINDOW','EARLY_BOOKER','LAST_MINUTE','LONG_STAY','CHANNEL_ALLOCATION','CHANNEL_PRICING','OVERBOOKING']
 for name in ('RMSAdvancedCommand','RMSAdvancedRecommendation','AdvancedProposal'):s[name]['properties']['kind']['enum']=kinds
 for name in ('RMSAdvancedRecommendation','AdvancedProposal'):s[name]['properties']['application']['enum']=['CANONICAL_RATE_VERSION','CANONICAL_RESTRICTION','CANONICAL_PROMOTION','ADVISORY']
 s['RMSAdvancedRecommendation']['properties']['state']['enum']=['DRAFT','PENDING_APPROVAL','APPROVED','APPLIED','REJECTED','EXPIRED','ACKNOWLEDGED']
 s['RMSAdvancedRecommendation']['properties']['change_digest'].update(sha)
 s['RMSAdvancedDecisionCommand']['properties']['action']['enum']=['SUBMIT','APPROVE','APPLY','REJECT','EXPIRE','ACKNOWLEDGE']
 s['RMSFindingResolveCommand']['properties']['resolution'].update(minLength=5,maxLength=1000)
 shape('RMSAutomationList',items=arr({'type':'object','additionalProperties':True}),limit={'type':'integer'},offset={'type':'integer'})
 return {
 'rmsAutomationActivations':('rms:read',None,'RMSAutomationList',200,False),
 'rmsAutomationActivate':('rms:write','RMSAutomationActivateCommand','RMSAutomationActivation',201,True),
 'rmsAutomationActivationGet':('rms:read',None,'RMSAutomationActivation',200,False),
 'rmsAutomationPause':('rms:write','RMSAutomationStateCommand','RMSAutomationActivation',200,True),
 'rmsAutomationResume':('rms:write','RMSAutomationStateCommand','RMSAutomationActivation',200,True),
 'rmsAutomationRevoke':('rms:write','RMSAutomationStateCommand','RMSAutomationActivation',200,True),
 'rmsAutomationRunTrigger':('rms:write','RMSAutomationRunCommand','RMSAutomationRun',202,False),
 'rmsAutomationRuns':('rms:read',None,'RMSAutomationList',200,False),
 'rmsAutomationRunGet':('rms:read',None,'RMSAutomationRunDetail',200,False),
 'rmsAutomationJobs':('rms:read',None,'RMSAutomationList',200,False),
 'rmsMonitoringFindings':('rms:read',None,'RMSAutomationList',200,False),
 'rmsMonitoringFindingResolve':('rms:write','RMSFindingResolveCommand','RMSRawObject',200,True),
 'rmsOutcomes':('rms:read',None,'RMSAutomationList',200,False),
 'rmsMarketInputs':('rms:read',None,'RMSAutomationList',200,False),
 'rmsMarketInputCreate':('rms:write','RMSMarketInputCommand','RMSRawObject',201,True),
 'rmsAnalyticsList':('rms:read',None,'RMSAutomationList',200,False),
 'rmsAnalyticsCreate':('rms:write','RMSAnalyticsCommand','RMSAnalyticsSnapshot',201,False),
 'rmsAnalyticsGet':('rms:read',None,'RMSAnalyticsSnapshot',200,False),
 'rmsScenarioSimulate':('rms:read','RMSScenarioCommand','RMSScenarioResult',200,False),
 'rmsModels':('rms:read',None,'RMSAutomationList',200,False),
 'rmsAdvancedList':('rms:read',None,'RMSAutomationList',200,False),
 'rmsAdvancedCreate':('rms:write','RMSAdvancedCommand','RMSAdvancedRecommendation',201,False),
 'rmsAdvancedGet':('rms:read',None,'RMSAdvancedRecommendation',200,False),
 'rmsAdvancedDecide':('rms:write','RMSAdvancedDecisionCommand','RMSAdvancedRecommendation',200,True),
 }
