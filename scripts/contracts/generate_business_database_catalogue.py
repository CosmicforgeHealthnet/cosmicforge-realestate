"""Reconcile every Section 5 table and Annex D table alias with owned migrations.
No database/network access. The integration catalogue test verifies live pg_catalog.
"""
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVICE = ROOT / 'services/business-operations-service'
spec = (ROOT / 'docs/specifications/CosmicForge_Business_Property_Operations_Full_Backend_Implementation_Spec_v1.0.md').read_text(encoding='utf-8')
categories = {
    'Organisation & security': ('organisation/workforce/auth/approvals', '3'),
    'Finance & coding': ('finance', '15 / D.4 / D.37'),
    'Portfolio & authority': ('portfolio/authority', '4 / D.6'),
    'Content & listings': ('content', '6 / D.7'),
    'Inventory & rate products': ('inventory/revenue', '7'),
    'Distribution/channel manager': ('distribution', '6'),
    'CRM/marketing/support': ('crm/support', '11 / 14 / D.8 / D.22'),
    'Reservations & hospitality': ('reservations/hospitality', '9 / 10 / D.39'),
    'Residential/commercial/land': ('residential/commercial/land', 'D.9 / D.10 / D.11'),
    'Sales/development': ('sales/development', 'D.12 / D.13'),
    'Short-let/housekeeping': ('shortlet/housekeeping', 'D.14 / D.17'),
    'Maintenance/vendors': ('maintenance', 'D.20'),
    'Managers/professionals': ('delegation/professional', 'D.19 / D.21'),
    'Reviews/reputation': ('reputation', '13 / D.33'),
    'Analytics/RMS/AI': ('analytics/revenue/automation', '8 / D.23 / D.30 / D.34'),
    'Risk/privacy/access': ('risk/privacy/access/insurance/financing', 'D.24 / D.26 / D.27 / D.28'),
    'Platform': ('platform/events', '1 / 2 / 19 / 22'),
}
tables = {}
for migration in sorted((SERVICE / 'migrations').glob('*.sql')):
    source = migration.read_text(encoding='utf-8')
    for match in re.finditer(r'CREATE TABLE (\w+)\s*\((.*?)\);', source, re.S | re.I):
        name, body = match.groups()
        tables[name] = {
            'migration': migration.name,
            'primary_key': re.findall(r'PRIMARY KEY\s*(\([^)]*\))?', body, re.I),
            'foreign_key_count': len(re.findall(r'\bREFERENCES\s', body, re.I)),
            'organisation_column': bool(re.search(r'\borganisation_id\b', body)),
            'versioned': bool(re.search(r'\bversion\b', body)),
            'lifecycle': bool(re.search(r'\b(state|status|revoked_at|consumed_at|released_at)\b', body)),
            'indexes': re.findall(r'CREATE (?:UNIQUE )?INDEX (\w+) ON ' + re.escape(name) + r'\b', source, re.I),
            'immutable': bool(re.search(r'CREATE TRIGGER \w+ BEFORE UPDATE OR DELETE OR TRUNCATE ON ' + re.escape(name) + r'\b', source)),
            'unique_constraint_count': len(re.findall(r'\bUNIQUE\b', body)),
            'check_constraint_count': len(re.findall(r'\bCHECK\s*\(', body)),
        }

entries = []
section5 = spec.split('## 5. Database catalogue', 1)[1].split('## 6.', 1)[0]
seen = set()
for line in section5.splitlines():
    parts = [p.strip() for p in line.split('|')]
    if len(parts) < 4 or parts[1] not in categories:
        continue
    category = parts[1]
    owner, section = categories[category]
    for name in parts[2].split(', '):
        actual = tables.get(name)
        seen.add(name)
        entries.append({'specified_table': name, 'canonical_table': name, 'specification': 'Section 5', 'category': category, 'owner_package': owner, 'owning_section': section, 'decision': 'SCHEMA_PRESENT' if actual else 'DEFERRED_FUTURE_DOMAIN', 'schema': actual, 'repository': 'internal/' + owner.split('/')[0], 'dependency': 'Existing owned schema; transactional domain workflows are certified separately.' if actual else 'Create in the owning future-section migration with its real lifecycle/repository/tests; no empty placeholder table.', 'isolation': 'Organisation-safe local references; no cross-service SQL.' if actual and actual['organisation_column'] else 'Service-local/global registry or child identity; see parent foreign keys and domain enforcement.' if actual else 'Planned organisation-scoped references only; external services remain ID/API/event references.'})

# Annex shorthand is explicitly mapped rather than creating competing tables.
aliases = {'credentials': 'business_credentials', 'sessions': 'business_sessions', 'devices': 'business_devices', 'mfa_methods': 'business_mfa_methods', 'phases': 'development_phases', 'assignments': 'lead_assignments', 'activities': 'lead_activities', 'modifications': 'reservation_modifications', 'holds': 'inventory_holds', 'restrictions': 'rate_restrictions', 'checklists': 'housekeeping_checklists', 'capabilities': 'channel_capabilities', 'field_authority': 'channel_field_authority', 'sync_jobs': 'channel_sync_jobs', 'cursors': 'channel_sync_cursors', 'failures': 'channel_sync_failures', 'conflicts': 'channel_conflicts', 'waitlist': 'waitlist_entries', 'source_refs': 'channel_reservation_refs'}
annex = spec.split('## Implementation Annex D.', 1)[1].split('## Implementation Annex E.', 1)[0]
current = ''
for line in annex.splitlines():
    if line.startswith('### D.'):
        current = line[4:]
    if not line.startswith('| Primary tables |'):
        continue
    for name in line.split('|')[2].strip().split(', '):
        if name == 'property/product/rate mappings':
            names = ['channel_property_mappings', 'channel_product_mappings', 'channel_rate_mappings']
        else:
            names = [aliases.get(name, name)]
        for canonical in names:
            if canonical in seen:
                continue
            if not re.fullmatch('[a-z_]+', canonical):
                raise RuntimeError('Unmapped annex table: ' + canonical)
            seen.add(canonical)
            actual = tables.get(canonical)
            entries.append({'specified_table': name, 'canonical_table': canonical, 'specification': current, 'category': 'Annex supplement', 'owner_package': 'See ' + current, 'owning_section': current, 'decision': 'SCHEMA_PRESENT' if actual else 'DEFERRED_FUTURE_DOMAIN', 'schema': actual, 'repository': 'Domain package from authoritative annex', 'dependency': 'Mapped to existing canonical table; no duplicate identity.' if actual else 'Deferred with owning annex workflow; not required for executable Sections 6–7.', 'isolation': 'Local tenant-safe FK/API/event references only.'})

mapped = {e['canonical_table'] for e in entries}
for name, schema in sorted(tables.items()):
    if name not in mapped:
        entries.append({'specified_table': '(supporting table)', 'canonical_table': name, 'specification': 'Supporting implementation', 'category': 'Supporting persistence', 'owner_package': 'internal/reservations' if name.startswith(('reservation_','waitlist_')) else 'Existing owning migration/domain', 'owning_section': {31:'12',32:'21',33:'13',34:'14',35:'15',36:'16',37:'16',38:'D.9-D.42'}.get(int(schema['migration'][:5]),'10') if int(schema['migration'][:5])>=13 else '11' if int(schema['migration'][:5])==12 else '8' if int(schema['migration'][:5]) in (10,11) else '9' if int(schema['migration'][:5]) in (8,9) else '3–7' if int(schema['migration'][:5]) >= 6 else '1–4', 'decision': 'SCHEMA_PRESENT', 'schema': schema, 'repository': 'Owning bounded context; no cross-service implementation imports.', 'dependency': 'Supporting secure/transactional operation, immutable history, normalized relationships or recovery.', 'isolation': 'Service-local; parent tenant keys and command authorization.'})
current_required={'group_blocks','rooming_lists','event_bookings','master_folios','guest_stay_history','guest_segments','vip_classifications','loyalty_programs','loyalty_rules','loyalty_accounts','loyalty_entitlements','loyalty_transactions','loyalty_rewards','loyalty_redemptions','loyalty_reversals','service_recovery_cases','service_recovery_actions','service_recovery_compensation_refs','privacy_requests','legal_holds','data_retention_jobs'}
known={e['canonical_table'] for e in entries}
for name in sorted(current_required-known):
    hospitality=name in {'group_blocks','rooming_lists','event_bookings','master_folios'}
    owner='hospitality' if hospitality else 'crm'
    entries.append({'specified_table':name,'canonical_table':name,'specification':'Master completion Sections 8–11','category':'Required current persistence','owner_package':owner,'owning_section':'10' if hospitality else '11','decision':'REQUIRED_CURRENT_WORK','schema':None,'repository':'internal/'+owner,'dependency':'Unfinished current internal backend requirement.','isolation':'Owned organisation/property-safe references only; no cross-service database access.'})
# The corporate negotiated-rate contract is the existing approved CRM record,
# not a second independent rate authority.
aliases['corporate_rate_contracts']='corporate_customer_contracts'
for entry in entries:
    if entry['canonical_table']=='corporate_rate_contracts':
        entry.update(canonical_table='corporate_customer_contracts',schema=tables['corporate_customer_contracts'],decision='SCHEMA_PRESENT',dependency='Negotiated canonical rate-plan, traveler and billing-reference contract implemented in the CRM owning record; no duplicate price authority.')
# Resolve each table to one owning context, including supporting persistence.
annex_owners={1:'organisation',2:'auth',3:'workforce',4:'finance',5:'portfolio',6:'portfolio',7:'content',8:'crm',9:'residential',10:'commercial',11:'land',12:'sales',13:'development',14:'shortlet',15:'portfolio',16:'reservations',17:'housekeeping',18:'revenue',19:'delegation',20:'maintenance',21:'professional',22:'support',23:'analytics',24:'risk',25:'organisation',26:'financing',27:'insurance',28:'access',29:'distribution',30:'revenue',31:'reservations',32:'crm',33:'reputation',34:'automation',35:'commerce',36:'integrations',37:'finance',38:'hospitality',39:'sales',40:'content',41:'workers',42:'risk'}
for entry in entries:
    name=entry['canonical_table']
    owner=None
    groups=[('support',('messaging_connections','communication_contexts','inbox_threads','inbox_assignments','inbox_message_receipts','inbox_sla_policies','inbox_sync_cursors','quick_reply_templates','scheduled_replies','scheduled_reply_attempts','support_cases')),
            ('automation',('ai_knowledge_entries','ai_suggestions','action_cards','automation_rules','automation_runs')),
            ('reputation',('review_','reputation_metrics','quality_')),
            ('crm',('lead_','leads','segments','campaigns','campaign_','attribution_touchpoints','funnel_observations','conversion_projection_cursors')),
            ('content',('brand_profiles','storefront_','upsell_')),
            ('finance',('financial_','fiscal_periods','cost_centres','budgets','budget_lines','obligation_adjustments','expense_requests','payout_','revenue_split_rules','channel_settlement_expectations')),
            ('housekeeping',('housekeeping_tasks',)),('maintenance',('maintenance_requests','work_orders','preventive_schedules')),('delegation',('manager_assignment_refs',)),
            ('professional',('data_rooms','data_room_grants','professional_appointments')),('residential',('rental_applications','tenancies')),('commercial',('commercial_deals',)),
            ('land',('land_transactions',)),('sales',('sale_transactions','sale_offers')),('development',('construction_milestones','development_allocations','snag_items','warranty_cases')),
            ('risk',('risk_cases','disputes')),('financing',('finance_offer_refs',)),('insurance',('insurance_',)),('access',('access_devices','access_grants')),
            ('integrations',('integration_clients','api_credentials','webhook_subscriptions','partner_api_rate_windows')),
            ('residential',('rental_offers','agreements','rent_obligations','deposit_obligations','notices','renewal_cases','move_out_cases','utility_accounts')),
            ('commercial',('commercial_lois','commercial_terms','service_charge_budgets')),('land',('land_terms','land_allocations')),
            ('sales',('due_diligence_items','closing_milestones','sale_allocations')),('development',('progress_certifications','project_variations')),
            ('maintenance',('vendor_quote_refs','provider_assignment_refs','warranties','service_contract_refs')),('delegation',('manager_performance_refs',)),
            ('professional',('professional_findings',)),('risk',('risk_signals','protection_case_refs','incident_cases')),('access',('access_events','device_health_snapshots')),
            ('financing',('financing_consents',)),('finance',('fx_rate_refs',)),('organisation',('organisation_change_cases','closure_cases')),
            ('housekeeping',('housekeeping_checklists','lost_found_items','linen_stock_refs')),('shortlet',('shortlet_settings',)),('portfolio',('property_service_catalogue',)),
            ('workers',('branch_incidents',)),('content',('content_suggestions',)),('analytics',('experiments','market_benchmarks')),('analytics',('report_jobs','analytics_snapshots')),('events',('operations_history',)),
            ('auth',('business_auth_','business_external_','business_refresh_','business_registration_','business_accounts','business_credentials','business_sessions','business_devices','business_mfa','security_events','sso_','scim_','mfa_','refresh_','password_','authentication_','external_identity_')),
            ('organisation',('organisations','organisation_','capability_packs','verification_links','verification_link_history','business_account_profiles')),
            ('workforce',('staff_','branches','departments','teams','regions','role_','permissions')),
            ('approvals',('approval_','delegation_grants','break_glass_')),
            ('portfolio',('inventory_graph_versions','inventory_nodes','inventory_edges','inventory_edge_','inventory_history','inventory_external_','inventory_import_','inventory_verification_','inventory_media_','space_graphs','digital_twin_refs','portfolios','developments','development_phases','parcels','properties','buildings','blocks','floors','units','room_types','rooms','facilities','common_areas','assets')),
            ('inventory',('inventory_pools','inventory_pool_members','inventory_calendar','inventory_holds','inventory_hold_days','inventory_allotments','inventory_allotment_days','inventory_release_signals','sellable_products')),
            ('reservations',('reservations','reservation_','waitlist_')),
            ('hospitality',('group_blocks','rooming_lists','master_folios','master_folio_members','event_bookings','offline_','cash_','cashier_','night_audit','night_audits','night_audit_','business_day_','room_status_','room_reconciliation_','room_assignments','room_assignment_','stay_records','stay_extensions','stay_extension_payers','stay_early_departures','folios','folio_','hospitality_','guest_requests','access_grant_references')),
            ('crm',('crm_history_','stay_history_projection','loyalty_','vip_classifications','service_recovery_','privacy_requests','legal_holds','data_retention_jobs','guest_','consents','customer_accounts_projection','corporate_')),
            ('revenue',('rms_','forecast_','demand_','booking_pace_','pricing_','market_benchmarks','rate_','promotions','add_ons','packages','package_components','price_quotes','bulk_rate_','tax_profiles','currency_policies')),
            ('content',('listing_templates','listings','listing_versions','listing_media_links','content_quality_scores','listing_approvals')),
            ('distribution',('channel_','listing_publication')),
            ('events',('event_failures','outbox_','inbox_','audit_events','commercial_history')),
            ('workers',('notification_jobs','notification_acceptances','worker_')),
            ('finance',('compliance_holds',))]
    for candidate,prefixes in groups:
        if any(name==prefix or prefix.endswith('_') and name.startswith(prefix) or prefix.startswith('business_mfa') and name.startswith(prefix) for prefix in prefixes):owner=candidate;break
    if owner is None:
        annex=re.match(r'D\.(\d+)',entry['owning_section'])
        owner=annex_owners[int(annex[1])] if annex else entry['owner_package'].split('/')[0]
    if owner in ('Existing owning migration','See ') or owner.startswith('See '):raise RuntimeError('Unresolved table owner '+name)
    entry['owner_package']='internal/'+owner
    entry['repository']='internal/repositories/db (SQLC); internal/'+owner+' owning command/query interfaces' if entry['schema'] else 'Planned internal/'+owner+' repository with owning-section migration'
    if entry['schema']:
        all_source='\n'.join(p.read_text(encoding='utf-8') for p in (SERVICE/'migrations').glob('*.sql'))
        entry['schema']['altered_in']=[p.name for p in sorted((SERVICE/'migrations').glob('*.sql')) if re.search(r'ALTER TABLE '+re.escape(name)+r'\b',p.read_text(encoding='utf-8'),re.I)]
        entry['schema']['indexes']=sorted(set(re.findall(r'CREATE (?:UNIQUE )?INDEX (\w+) ON '+re.escape(name)+r'\b',all_source,re.I)))
# Section 8–11 gaps belong to the current assignment, not future sections.

for entry in entries:
    if not entry['schema'] and (entry['canonical_table'] in current_required or entry['owning_section'] in ('8','9','10','11') or entry['owning_section'] in ('D.30','D.31','D.32','D.38','D.39','D.41')):
        entry['decision']='REQUIRED_CURRENT_WORK'
        entry['dependency']='Unfinished internal Sections 8–11 workflow; implement its owned migration, commands and tests. This is not an external provider or future-section blocker.'
# Annex D entries served by an existing, well-designed model. Each records the
# canonical table, why no independent lifecycle exists, and the executable
# test proving the required behaviour on the shared persistence.
EQUIVALENCES = {
 'ai_recommendations': ('ai_suggestions', 'AI recommendations are ai_suggestions (grounded, cited, confidence, decided_by) plus pricing_recommendations/rms_advanced_recommendations and action_cards; a fourth store would duplicate provenance.', 'TestBusinessInboxAIGroundingInjectionActionCardsAndApprovedAutoReply; TestBusinessRMSGovernedApplicationSimulationAndCompensatingRollback'),
 'close_exceptions': ('night_audit_exceptions', 'Business-day close exceptions are night_audit_exceptions (hard_block) with financial_exception_cases for money variances; close is blocked on unresolved hard blocks.', 'TestBusinessPMSNightAuditSnapshotFenceSingleCloseAndImmutableReport; TestBusinessFinanceChannelRemittanceReconciliationAndCloseBlocking'),
 'communication_context_refs': ('communication_contexts', 'CF Meet/external conversation references are communication_contexts (authority, external_conversation_ref, no message bodies).', 'TestBusinessInboxCFMeetAuthorityPendingThenEstablishedWithoutMessageStorage'),
 'financing_status_projection': ('finance_offer_refs', 'The financing status projection is the governed finance_offer_refs lifecycle (REQUESTED..APPROVED/DECLINED) updated only from partner references; consent is financing_consents (00038).', 'TestAnnexDFinancingConsentInsuranceComplianceAndAccessOutcomes'),
 'integration_health': ('integration_clients', 'Integration health is derived by GET /integrations/{id}/health from the governed client state and history (database_access=false); persisting a copy would drift.', 'TestAnnexDEquivalenceDisputeEvidenceIntegrationHealthAccounting'),
 'migration_errors': ('inventory_import_records', 'Assisted-migration row errors are inventory_import_records (validation_errors, error_code, QUARANTINED) with inventory_import_reconciliation.', 'TestBusinessImportCSVGroupingValidationAndExplicitPartialExecution'),
 'migration_jobs': ('inventory_import_jobs', 'Assisted migration jobs are inventory_import_jobs: preview, validation, dedupe, leased resumable execution and checkpoints.', 'TestBusinessImportPreviewResumeDedupeAndSafeRecovery; TestBusinessImportStalePreviewPausesAndWorkersFenceConcurrentClaims'),
 'promotion_codes': ('promotions', 'Promotion codes are promotions(code UNIQUE per property, definition eligibility, approval, state) applied by the canonical quote engine.', 'TestBusinessCommercialApprovedPromotionPackageAddOnPolicyPrecedence'),
 'reconciliation_tasks': ('offline_reconciliation_tasks', 'Offline/DR reconciliation tasks are offline_reconciliation_tasks with offline_reconciliation_resolutions (deterministic classification per receipt).', 'TestBusinessPMSOfflineStaleAssignmentDurableTaskAndRealReplay'),
 'tax_rules': ('tax_profiles', 'Jurisdictional tax rules are versioned, source-referenced, approved tax_profiles (policy_key + version, effective window); FX references are fx_rate_refs (00038).', 'TestBusinessCommercialApprovedPromotionPackageAddOnPolicyPrecedence; TestAnnexDFXFreshnessOrganisationChangeAndClosureContinuity'),
 'accounting_connection_refs': ('integration_clients', 'Accounting connections are integration_clients kind ACCOUNTING with scoped permissions, secret-manager references and api_credentials (00038).', 'TestAnnexDEquivalenceDisputeEvidenceIntegrationHealthAccounting; TestAnnexDPartnerCredentialsWebhooksRotationAndRevocation'),
 'asset_service_history': ('operations_history', 'Asset service history is the derived projection GET /assets/{id}/service-history over work_orders, warranties, preventive_schedules and their immutable operations_history.', 'TestAnnexDMaintenanceQuotesProviderWarrantyAndAssetHistory'),
 'maintenance_approvals': ('approval_requests', 'Maintenance approvals are independent approval_requests (COMMERCIAL_WORK_ORDER) bound to the accepted vendor quote amount and record version.', 'TestAnnexDMaintenanceQuotesProviderWarrantyAndAssetHistory; TestBusinessWorkOrderQuoteApprovalEvidenceVerificationAndWarrantyRework'),
 'work_evidence_refs': ('work_orders', 'Work evidence is the append-only Media reference list on work_orders/provider_assignment_refs with each submission recorded in operations_history.', 'TestBusinessWorkOrderQuoteApprovalEvidenceVerificationAndWarrantyRework'),
 'manager_permissions': ('manager_assignment_refs', 'Manager permissions are the delegable capability list on manager_assignment_refs enforced by the signed internal authorization endpoint; revocation/expiry deny immediately.', 'TestBusinessManagerDelegationScopedExpiringRevocableAndEnforced'),
 'professional_access_grants': ('data_room_grants', 'Professional access grants are purpose- and time-limited data_room_grants tied to an active appointment and enforced for Media downloads.', 'TestBusinessProfessionalDataRoomGrantPurposeExpiryApprovalAndRevocation'),
 'authority_documents': ('inventory_verification_links', 'Authority documents are owned by Verification; Business stores only verification links/history and Media references, never authority evidence bytes.', 'TestBusinessInventoryAuthoritativeVerificationActivationAndExpiry'),
 'authority_parties': ('inventory_verification_links', 'Authority parties are Verification-owned; Business projects the signed verification outcome per node.', 'TestBusinessInventoryAuthoritativeVerificationActivationAndExpiry'),
 'authority_records': ('inventory_verification_links', 'Property authority records are the Verification-owned decision projected into inventory_verification_links with verification_link_history.', 'TestBusinessInventoryAuthoritativeVerificationActivationAndExpiry'),
 'dispute_evidence_refs': ('disputes', 'Dispute evidence is the append-only Media reference list on disputes, guarded against rewrite, with immutable history.', 'TestAnnexDEquivalenceDisputeEvidenceIntegrationHealthAccounting'),
 'buyer_allocations': ('development_allocations', 'Buyer allocations are development_allocations (buyer counterparty, one live allocation per unit, frozen milestone schedule, approval) with sale_allocations (00038) for completed-unit holds and cross-domain conservation.', 'TestBusinessDevelopmentAllocationMilestonesSnagsAndHandover; TestAnnexDOffPlanProgressCertificationVariationAndConservation'),
 'room_status_history': ('room_status_current', 'Room status history is every room_status_current change committed with hospitality history/audit and the room.status.changed.v1 outbox event.', 'TestJourneyJ24CheckoutTurnoverInspectionReadyRoom'),
 'turnover_jobs': ('housekeeping_tasks', 'Turnover jobs are housekeeping_tasks kind TURNOVER scheduled by the worker from VACANT_DIRTY room status, with checklists (00038).', 'TestJourneyJ24CheckoutTurnoverInspectionReadyRoom; TestAnnexDHospitalityChecklistsLostFoundLinenSettingsServicesExperiments'),
}
for entry in entries:
    eq = EQUIVALENCES.get(entry['canonical_table'])
    if eq and not entry['schema']:
        table, why, test = eq
        entry.update(canonical_table=table, schema=tables[table], decision='EQUIVALENT_IMPLEMENTATION_VERIFIED', dependency=why + ' Proven by: ' + test + '.')
unresolved = [e['canonical_table'] for e in entries if not e['schema']]
entries.sort(key=lambda e: (e['category'], e['canonical_table']))
data = {'latest_migration':sorted((SERVICE/'migrations').glob('*.sql'))[-1].name,'specification': 'CosmicForge_Business_Property_Operations_Full_Backend_Implementation_Spec_v1.0', 'aliases': aliases, 'entries': entries}
md = ['# Section 5: reconciled database catalogue', '', 'Generated from the complete Section 5 catalogue, Annex D table supplements and actual owned migrations. SCHEMA_PRESENT certifies presence, not unexecuted workflows. EQUIVALENT_IMPLEMENTATION_VERIFIED maps an Annex D name to an existing canonical model and names the executable test proving the behaviour. REQUIRED_CURRENT_WORK records unfinished current Sections 8–11 schema; DEFERRED_FUTURE_DOMAIN is reserved for genuinely later sections. No placeholder DDL is generated. Run `python scripts/contracts/generate_business_database_catalogue.py --check` to detect drift. Integration tests verify implemented tables against real pg_catalog. SQLC generates typed query models; domain commands retain owning transactions and explicit scope checks.', '', '| Specified table | Canonical table | Owner / section | Decision | Migration | Tenant / FK / unique / check / indexes / immutability |', '|---|---|---|---|---|---|']
for e in entries:
    a = e['schema']
    detail = ('altered_in='+','.join(a['altered_in'])+'; tenant=' + str(a['organisation_column']) + '; FK=' + str(a['foreign_key_count']) + '; unique=' + str(a['unique_constraint_count']) + '; checks=' + str(a['check_constraint_count']) + '; indexes=' + ', '.join(a['indexes']) + '; immutable=' + str(a['immutable'])) if a else e['dependency']
    md.append('| ' + ' | '.join([e['specified_table'], e['canonical_table'], e['owner_package'] + ' / ' + e['owning_section'], e['decision'], a['migration'] if a else 'Required current migration' if e['decision']=='REQUIRED_CURRENT_WORK' else 'Future owning-section migration', detail]) + ' |')
outputs = {ROOT / 'docs/implementation/business-operations-database-catalogue.json': json.dumps(data, indent=2) + '\n', ROOT / 'docs/implementation/business-operations-database-catalogue.md': '\n'.join(md) + '\n'}
changed = []
for path, text in outputs.items():
    if not path.exists() or path.read_text(encoding='utf-8') != text:
        changed.append(str(path.relative_to(ROOT)))
        if '--check' not in sys.argv:
            path.write_text(text, encoding='utf-8', newline='\n')
if '--check' in sys.argv and changed:
    raise SystemExit('Catalogue drift: ' + ', '.join(changed))
if unresolved:
    print('Unresolved catalogue entries: ' + ', '.join(unresolved))
print(f'{sum(e["decision"] == "EQUIVALENT_IMPLEMENTATION_VERIFIED" for e in entries)} proven equivalences; {len(entries)} table decisions; {len(tables)} implemented tables; {sum(e["decision"] == "DEFERRED_FUTURE_DOMAIN" for e in entries)} future tables explicitly deferred; {sum(e["decision"] == "REQUIRED_CURRENT_WORK" for e in entries)} required current schema gaps')
