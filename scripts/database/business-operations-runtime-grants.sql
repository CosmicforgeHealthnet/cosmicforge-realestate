\set ON_ERROR_STOP on
-- Run as the authorised owner/operator after successful migrations.
DO $$ BEGIN
 IF current_database()<>'business_operations_db' THEN RAISE EXCEPTION 'Wrong service database'; END IF;
END $$;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO cf_business_runtime;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO cf_business_runtime;
REVOKE ALL ON goose_db_version FROM cf_business_runtime;
REVOKE UPDATE,DELETE,TRUNCATE ON audit_events,security_events,approval_decisions,staff_membership_history,role_permissions,verification_link_history FROM cf_business_runtime;
REVOKE DELETE,TRUNCATE ON approval_policies,role_templates,approval_requests,staff_handovers,break_glass_grants FROM cf_business_runtime;
REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON inventory_edge_rules FROM cf_business_runtime;
REVOKE UPDATE,DELETE,TRUNCATE ON inventory_history,inventory_edge_history,space_graphs,digital_twin_refs FROM cf_business_runtime;
REVOKE UPDATE,DELETE,TRUNCATE ON portfolios,developments,development_phases,parcels,properties,buildings,blocks,floors,units,room_types,rooms,facilities,common_areas,assets FROM cf_business_runtime;
REVOKE DELETE,TRUNCATE ON inventory_nodes,inventory_import_jobs,inventory_import_records,inventory_external_mappings FROM cf_business_runtime;
-- Sections 5–7: provider certification is operator-controlled, never a frontend claim.
REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON channel_providers,channel_capabilities FROM cf_business_runtime;
REVOKE UPDATE,DELETE,TRUNCATE ON commercial_history,bulk_rate_scope_links,channel_mapping_previews,channel_event_delivery_keys,channel_commission_rules,content_quality_scores,rate_plan_versions,rate_relations,rate_rules,price_quotes,tax_profiles,listing_versions,listing_media_links,listing_approvals,channel_sync_failures,channel_conflict_decisions,channel_financial_inputs,channel_metrics_snapshots FROM cf_business_runtime;
REVOKE DELETE,TRUNCATE ON channel_property_mappings,channel_product_mappings,channel_rate_mappings,channel_sync_cursors,compliance_holds,currency_policies,package_components,channel_provider_dispatch,channel_field_authority,channel_content_overrides,listing_templates,inventory_pools,sellable_products,inventory_calendar,inventory_holds,inventory_hold_days,inventory_allotments,inventory_allotment_days,rate_plans,rate_restrictions,promotions,packages,add_ons,bulk_rate_commands,listings,channel_connections,channel_sync_jobs,channel_event_receipts,channel_reservation_refs,channel_conflicts FROM cf_business_runtime;
-- Section 9: immutable accepted obligations and source/decision history.
REVOKE UPDATE,DELETE,TRUNCATE ON reservation_versions,reservation_price_snapshots,reservation_source_refs,reservation_holds,reservation_modifications,reservation_lineage,reservation_financial_credits,reservation_cancellation_obligations,reservation_payment_bindings,reservation_payment_projections,reservation_conflict_decisions,reservation_overbooking_permits FROM cf_business_runtime;
REVOKE DELETE,TRUNCATE ON reservation_policies,reservation_groups,reservations,reservation_financial_groups,reservation_rooms,reservation_inventory_days,reservation_financial_requests,reservation_risk_projection,reservation_conflicts,reservation_overbooking_policies,waitlist_entries,waitlist_offers FROM cf_business_runtime;
-- Assertions fail before an incorrectly privileged role can be certified.
DO $$ BEGIN
 IF has_schema_privilege('cf_business_runtime','public','CREATE')
 OR has_table_privilege('cf_business_runtime','audit_events','UPDATE')
 OR has_table_privilege('cf_business_runtime','goose_db_version','SELECT')
 OR has_table_privilege('cf_business_runtime','inventory_edge_rules','INSERT')
 OR has_table_privilege('cf_business_runtime','inventory_history','UPDATE')
 OR has_table_privilege('cf_business_runtime','reservation_financial_credits','UPDATE')
 OR has_table_privilege('cf_business_runtime','reservation_versions','TRUNCATE')
 THEN RAISE EXCEPTION 'Runtime least-privilege check failed'; END IF;
END $$;
