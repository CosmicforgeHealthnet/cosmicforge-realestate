-- +goose Up
CREATE TABLE business_accounts (
 id uuid PRIMARY KEY, email text NOT NULL UNIQUE CHECK (email=lower(email)),
 display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
 status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','RESTRICTED','DISABLED','CLOSED')),
 version bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE business_credentials (
 account_id uuid PRIMARY KEY REFERENCES business_accounts(id), password_hash text NOT NULL,
 changed_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE business_devices (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES business_accounts(id),
 label text NOT NULL DEFAULT '', platform text NOT NULL DEFAULT '', risk_flags jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT now(), last_seen_at timestamptz NOT NULL DEFAULT now(), UNIQUE(account_id,id)
);
CREATE TABLE organisations (
 id uuid PRIMARY KEY, representative_account_id uuid NOT NULL REFERENCES business_accounts(id),
 name text NOT NULL, registered_name text NOT NULL DEFAULT '', registration_number text NOT NULL DEFAULT '',
 category text NOT NULL DEFAULT '', jurisdiction text NOT NULL DEFAULT '', currency text NOT NULL DEFAULT 'EUR', timezone text NOT NULL DEFAULT 'UTC',
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','KYB_PENDING','ACTIVE','RESTRICTED','SUSPENDED','CLOSURE_PENDING','CLOSED')),
 version bigint NOT NULL DEFAULT 1, activated_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE business_sessions (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES business_accounts(id), device_id uuid,
 organisation_id uuid REFERENCES organisations(id), created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL, last_refreshed_at timestamptz NOT NULL,
 revoked_at timestamptz, revoke_reason text NOT NULL DEFAULT '', mfa_at timestamptz,
 FOREIGN KEY(account_id,device_id) REFERENCES business_devices(account_id,id), CHECK(expires_at>created_at)
);
CREATE INDEX business_sessions_account_idx ON business_sessions(account_id,created_at DESC,id);
CREATE TABLE business_refresh_tokens (
 id uuid PRIMARY KEY, session_id uuid NOT NULL REFERENCES business_sessions(id), token_hash text NOT NULL UNIQUE CHECK(length(token_hash)=64),
 issued_at timestamptz NOT NULL, expires_at timestamptz NOT NULL, consumed_at timestamptz, replaced_by uuid,
 CHECK(expires_at>issued_at)
);
CREATE INDEX business_refresh_session_idx ON business_refresh_tokens(session_id);
CREATE TABLE business_mfa_methods (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES business_accounts(id),
 kind text NOT NULL CHECK(kind IN ('TOTP')), status text NOT NULL CHECK(status IN ('PENDING','ACTIVE','REMOVED')),
 key_id text NOT NULL, encrypted_secret bytea NOT NULL, last_step bigint NOT NULL DEFAULT -1,
 expires_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), confirmed_at timestamptz, removed_at timestamptz
);
CREATE UNIQUE INDEX business_mfa_live_idx ON business_mfa_methods(account_id,kind) WHERE status IN ('PENDING','ACTIVE');
CREATE TABLE business_mfa_recovery_codes (
 id uuid PRIMARY KEY, method_id uuid NOT NULL REFERENCES business_mfa_methods(id), code_hash text NOT NULL UNIQUE,
 consumed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE business_mfa_challenges (
 id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES business_accounts(id), token_hash text NOT NULL UNIQUE,
 purpose text NOT NULL CHECK(purpose IN ('LOGIN','STEP_UP')), session_id uuid REFERENCES business_sessions(id),
 organisation_id uuid REFERENCES organisations(id), device_name text NOT NULL DEFAULT '', device_platform text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0), expires_at timestamptz NOT NULL, consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE business_auth_limits (
 key_hash text PRIMARY KEY, window_start timestamptz NOT NULL, attempts integer NOT NULL CHECK(attempts>0)
);
CREATE TABLE business_registration_keys (
 key_hash text PRIMARY KEY, request_hash text NOT NULL, account_id uuid NOT NULL REFERENCES business_accounts(id),
 organisation_id uuid NOT NULL REFERENCES organisations(id), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE verification_links (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), case_id uuid NOT NULL UNIQUE,
 purpose text NOT NULL, provider_status text NOT NULL, expires_at timestamptz, provider_updated_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organisation_id,id), UNIQUE(organisation_id,purpose)
);
CREATE TABLE organisation_capacities (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), code text NOT NULL,
 status text NOT NULL CHECK(status IN ('DECLARED','PENDING','ACTIVE','RESTRICTED','SUSPENDED','EXPIRED')),
 verification_link_id uuid, effective_at timestamptz NOT NULL, expires_at timestamptz, restricted_at timestamptz,
 version bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,verification_link_id) REFERENCES verification_links(organisation_id,id), UNIQUE(organisation_id,code), UNIQUE(organisation_id,id)
);
CREATE TABLE capability_packs (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), code text NOT NULL,
 required_capacities text[] NOT NULL CHECK(cardinality(required_capacities)>0), enabled boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organisation_id,code)
);
CREATE TABLE regions (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), name text NOT NULL,
 status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','ARCHIVED')), version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organisation_id,id)
);
CREATE TABLE branches (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), region_id uuid,
 name text NOT NULL, timezone text NOT NULL, currency text NOT NULL,
 status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','SUSPENDED','ARCHIVED')), version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organisation_id,id),
 FOREIGN KEY(organisation_id,region_id) REFERENCES regions(organisation_id,id)
);
CREATE TABLE departments (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), branch_id uuid NOT NULL,
 name text NOT NULL, status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','ARCHIVED')), version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organisation_id,id),
 FOREIGN KEY(organisation_id,branch_id) REFERENCES branches(organisation_id,id)
);
CREATE TABLE teams (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), department_id uuid NOT NULL,
 name text NOT NULL, status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','ARCHIVED')), version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organisation_id,id), UNIQUE(organisation_id,department_id,id),
 FOREIGN KEY(organisation_id,department_id) REFERENCES departments(organisation_id,id)
);
CREATE TABLE permissions (code text PRIMARY KEY CHECK(code ~ '^[a-z_]+:[a-z_]+$'), description text NOT NULL);
CREATE TABLE role_templates (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), role_key uuid NOT NULL,
 name text NOT NULL, version bigint NOT NULL CHECK(version>0), active boolean NOT NULL DEFAULT true,
 senior_emergency_eligible boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(organisation_id,role_key,version), UNIQUE(organisation_id,id)
);
CREATE TABLE role_permissions (
 role_id uuid NOT NULL REFERENCES role_templates(id), permission text NOT NULL REFERENCES permissions(code), PRIMARY KEY(role_id,permission)
);
CREATE TABLE staff_memberships (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), account_id uuid NOT NULL REFERENCES business_accounts(id), role_id uuid NOT NULL,
 department_id uuid, team_id uuid, kind text NOT NULL DEFAULT 'EMPLOYEE' CHECK(kind IN ('EMPLOYEE','CONTRACTOR','TEMPORARY','OWNER')),
 status text NOT NULL CHECK(status IN ('INVITED','ACTIVE','SUSPENDED','HANDOVER_PENDING','TERMINATED')),
 effective_at timestamptz NOT NULL, expires_at timestamptz, version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(organisation_id,account_id), UNIQUE(organisation_id,id),
 FOREIGN KEY(organisation_id,role_id) REFERENCES role_templates(organisation_id,id),
 FOREIGN KEY(organisation_id,department_id) REFERENCES departments(organisation_id,id), FOREIGN KEY(organisation_id,department_id,team_id) REFERENCES teams(organisation_id,department_id,id),
 CHECK(team_id IS NULL OR department_id IS NOT NULL),
 CHECK(expires_at IS NULL OR expires_at>effective_at)
);
CREATE INDEX staff_memberships_account_idx ON staff_memberships(account_id,status);
CREATE TABLE staff_invitations (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), email text NOT NULL CHECK(email=lower(email)),
 role_id uuid NOT NULL, token_hash text NOT NULL UNIQUE, encrypted_delivery bytea NOT NULL, delivery_key_id text NOT NULL,
 invited_by uuid NOT NULL REFERENCES business_accounts(id), status text NOT NULL CHECK(status IN ('PENDING','ACCEPTED','REVOKED','EXPIRED')),
 scopes jsonb NOT NULL, expires_at timestamptz NOT NULL, accepted_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,role_id) REFERENCES role_templates(organisation_id,id)
);
CREATE UNIQUE INDEX staff_invitations_pending_idx ON staff_invitations(organisation_id,email) WHERE status='PENDING';
CREATE TABLE staff_membership_history (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL, membership_id uuid NOT NULL,
 actor_id uuid NOT NULL REFERENCES business_accounts(id), from_status text, to_status text NOT NULL, reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), FOREIGN KEY(organisation_id,membership_id) REFERENCES staff_memberships(organisation_id,id)
);
CREATE TABLE staff_scope_grants (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL, membership_id uuid NOT NULL, capability text NOT NULL REFERENCES permissions(code),
 scope_kind text NOT NULL CHECK(scope_kind IN ('COMPANY','REGION','BRANCH','DEPARTMENT','TEAM','PORTFOLIO','PROPERTY','DEVELOPMENT','UNIT','FACILITY')),
 scope_id uuid NOT NULL, inherit boolean NOT NULL DEFAULT false, deny boolean NOT NULL DEFAULT false,
 effective_at timestamptz NOT NULL, expires_at timestamptz, revoked_at timestamptz,
 amount_limit_minor bigint CHECK(amount_limit_minor>=0), currency text, require_mfa boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(), FOREIGN KEY(organisation_id,membership_id) REFERENCES staff_memberships(organisation_id,id),
 CHECK(scope_kind<>'COMPANY' OR scope_id=organisation_id), CHECK(expires_at IS NULL OR expires_at>effective_at), CHECK(amount_limit_minor IS NULL OR currency ~ '^[A-Z]{3}$')
);
CREATE INDEX staff_scope_grants_member_idx ON staff_scope_grants(organisation_id,membership_id) WHERE revoked_at IS NULL;
CREATE TABLE approval_policies (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), policy_key uuid NOT NULL,
 version bigint NOT NULL CHECK(version>0), action text NOT NULL, risk text NOT NULL CHECK(risk IN ('LOW','MEDIUM','HIGH','CRITICAL')),
 active boolean NOT NULL DEFAULT true, definition jsonb NOT NULL,
 effective_at timestamptz NOT NULL, expires_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(organisation_id,policy_key,version), UNIQUE(organisation_id,id), CHECK(expires_at IS NULL OR expires_at>effective_at)
);
CREATE TABLE approval_requests (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL REFERENCES organisations(id), policy_id uuid NOT NULL,
 requester_id uuid NOT NULL REFERENCES business_accounts(id), action text NOT NULL, scope_kind text NOT NULL, scope_id uuid NOT NULL,
 amount_minor bigint CHECK(amount_minor>=0), currency text, policy_snapshot jsonb NOT NULL,
 state text NOT NULL CHECK(state IN ('DRAFT','PENDING','APPROVED','REJECTED','EXPIRED','CANCELLED')),
 current_level integer NOT NULL DEFAULT 0 CHECK(current_level>=0), deadline timestamptz NOT NULL, reason text NOT NULL,
 idempotency_key text NOT NULL, request_hash text NOT NULL, version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organisation_id,id), UNIQUE(organisation_id,requester_id,idempotency_key),
 FOREIGN KEY(organisation_id,policy_id) REFERENCES approval_policies(organisation_id,id), CHECK(amount_minor IS NULL OR currency ~ '^[A-Z]{3}$')
);
CREATE INDEX approval_requests_pending_idx ON approval_requests(organisation_id,state,created_at DESC,id);
CREATE TABLE approval_steps (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL, request_id uuid NOT NULL, level integer NOT NULL CHECK(level>=0),
 capability text NOT NULL REFERENCES permissions(code), required_count integer NOT NULL CHECK(required_count>0),
 require_mfa boolean NOT NULL DEFAULT false, UNIQUE(request_id,level), FOREIGN KEY(organisation_id,request_id) REFERENCES approval_requests(organisation_id,id)
);
CREATE TABLE approval_decisions (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL, request_id uuid NOT NULL, level integer NOT NULL,
 actor_id uuid NOT NULL REFERENCES business_accounts(id), outcome text NOT NULL CHECK(outcome IN ('APPROVED','REJECTED','CANCELLED')),
 reason text NOT NULL, evidence_ids uuid[] NOT NULL DEFAULT '{}', idempotency_key text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(request_id,idempotency_key), UNIQUE(request_id,level,actor_id), FOREIGN KEY(organisation_id,request_id) REFERENCES approval_requests(organisation_id,id)
);
CREATE TABLE delegation_grants (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL, delegator_membership_id uuid NOT NULL, delegate_membership_id uuid NOT NULL,
 capability text NOT NULL REFERENCES permissions(code), scope_kind text NOT NULL, scope_id uuid NOT NULL, inherit boolean NOT NULL DEFAULT false,
 effective_at timestamptz NOT NULL, expires_at timestamptz NOT NULL, revoked_at timestamptz, require_mfa boolean NOT NULL DEFAULT false,
 amount_limit_minor bigint CHECK(amount_limit_minor>=0), currency text, reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,delegator_membership_id) REFERENCES staff_memberships(organisation_id,id), FOREIGN KEY(organisation_id,delegate_membership_id) REFERENCES staff_memberships(organisation_id,id),
 CHECK(delegator_membership_id<>delegate_membership_id), CHECK(expires_at>effective_at), CHECK(expires_at<=effective_at+interval '30 days')
);
CREATE INDEX delegation_grants_delegate_idx ON delegation_grants(organisation_id,delegate_membership_id,expires_at) WHERE revoked_at IS NULL;
CREATE TABLE break_glass_grants (
 id uuid PRIMARY KEY, organisation_id uuid NOT NULL, membership_id uuid NOT NULL, scope_kind text NOT NULL, scope_id uuid NOT NULL,
 capabilities text[] NOT NULL, reason text NOT NULL CHECK(length(reason)>=20), expires_at timestamptz NOT NULL, revoked_at timestamptz,
 mfa_at timestamptz NOT NULL, reviewed_by uuid REFERENCES business_accounts(id), review_reason text, reviewed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), FOREIGN KEY(organisation_id,membership_id) REFERENCES staff_memberships(organisation_id,id),
 CHECK(expires_at>created_at AND expires_at<=created_at+interval '15 minutes')
);
CREATE TABLE security_events (
 id uuid PRIMARY KEY, account_id uuid REFERENCES business_accounts(id), organisation_id uuid REFERENCES organisations(id),
 event_type text NOT NULL, target_id uuid, correlation_id text NOT NULL DEFAULT '', context jsonb NOT NULL DEFAULT '{}', occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_events_account_idx ON security_events(account_id,occurred_at DESC,id);
CREATE TABLE audit_events (
 id uuid PRIMARY KEY, actor_id uuid REFERENCES business_accounts(id), organisation_id uuid NOT NULL REFERENCES organisations(id),
 action text NOT NULL, target_type text NOT NULL, target_id uuid NOT NULL, correlation_id text NOT NULL DEFAULT '',
 change_context jsonb NOT NULL DEFAULT '{}', occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_org_idx ON audit_events(organisation_id,occurred_at DESC,id);
CREATE TABLE processed_events (consumer text NOT NULL,event_id uuid NOT NULL,event_type text NOT NULL,processed_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(consumer,event_id));

-- +goose StatementBegin
CREATE FUNCTION business_immutable_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Business security/audit/decision history is immutable' USING ERRCODE='42501'; END;
$$;
-- +goose StatementEnd
CREATE TRIGGER security_events_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON security_events FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER audit_events_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON audit_events FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER approval_decisions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON approval_decisions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER staff_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON staff_membership_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- +goose Down
-- Forward-only operational migration. Rollback requires an explicit reviewed recovery plan.
SELECT 1;
