-- Operator/CI use only, against the authorised PostgreSQL administrative
-- endpoint. No password, host or connection string is embedded here.
-- psql -v ON_ERROR_STOP=1 -f business-operations-provision.sql ADMIN_DSN
-- Run before migrations, then business-operations-runtime-grants.sql after.
\set ON_ERROR_STOP on
SELECT 'CREATE ROLE cf_business_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION'
WHERE NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='cf_business_owner') \gexec
SELECT 'CREATE ROLE cf_business_migrator LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION'
WHERE NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='cf_business_migrator') \gexec
SELECT 'CREATE ROLE cf_business_runtime LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION'
WHERE NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='cf_business_runtime') \gexec
GRANT cf_business_owner TO cf_business_migrator;
SELECT 'CREATE DATABASE business_operations_db OWNER cf_business_owner'
WHERE NOT EXISTS(SELECT 1 FROM pg_database WHERE datname='business_operations_db') \gexec
\connect business_operations_db
DO $$ BEGIN
 IF (SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=current_database())<>'cf_business_owner'
 OR EXISTS(SELECT 1 FROM pg_roles WHERE rolname IN ('cf_business_owner','cf_business_migrator','cf_business_runtime') AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls))
 OR pg_has_role('cf_business_runtime','cf_business_owner','MEMBER')
 THEN RAISE EXCEPTION 'Existing database/role ownership conflicts; operator review required'; END IF;
END $$;
REVOKE CONNECT,TEMPORARY ON DATABASE business_operations_db FROM PUBLIC;
GRANT CONNECT ON DATABASE business_operations_db TO cf_business_runtime,cf_business_migrator;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO cf_business_owner;
GRANT USAGE ON SCHEMA public TO cf_business_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE cf_business_owner IN SCHEMA public
GRANT SELECT,INSERT,UPDATE,DELETE ON TABLES TO cf_business_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE cf_business_owner IN SCHEMA public
GRANT USAGE,SELECT ON SEQUENCES TO cf_business_runtime;
-- Configure migrator credentials via the authorised secret manager; its DSN
-- must SET ROLE cf_business_owner (options=-c role=cf_business_owner).
-- Configure the runtime role password via the secret manager. This script
-- deliberately does not set, reveal or rotate any credential.
