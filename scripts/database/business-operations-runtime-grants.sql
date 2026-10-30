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
-- Assertions fail before an incorrectly privileged role can be certified.
DO $$ BEGIN
 IF has_schema_privilege('cf_business_runtime','public','CREATE')
 OR has_table_privilege('cf_business_runtime','audit_events','UPDATE')
 OR has_table_privilege('cf_business_runtime','goose_db_version','SELECT')
 THEN RAISE EXCEPTION 'Runtime least-privilege check failed'; END IF;
END $$;
