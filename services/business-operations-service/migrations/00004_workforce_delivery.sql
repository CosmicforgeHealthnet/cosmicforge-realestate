-- +goose Up
ALTER TABLE business_accounts DROP CONSTRAINT business_accounts_status_check;
ALTER TABLE business_accounts ADD CONSTRAINT business_accounts_status_check CHECK(status IN ('INVITED','ACTIVE','RESTRICTED','DISABLED','CLOSED'));
ALTER TABLE organisations ADD COLUMN authorization_version bigint NOT NULL DEFAULT 1;
ALTER TABLE regions ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE staff_membership_history ALTER COLUMN actor_id DROP NOT NULL;
ALTER TABLE staff_invitations ADD COLUMN account_id uuid REFERENCES business_accounts(id);
ALTER TABLE staff_invitations ADD COLUMN authorization_version bigint NOT NULL DEFAULT 1;
ALTER TABLE staff_memberships ADD COLUMN scope_kind text NOT NULL DEFAULT 'COMPANY';
ALTER TABLE staff_memberships ADD COLUMN scope_id uuid;
UPDATE staff_memberships SET scope_id=organisation_id WHERE scope_id IS NULL;
ALTER TABLE staff_memberships ALTER COLUMN scope_id SET NOT NULL;
ALTER TABLE staff_invitations ADD COLUMN membership_id uuid;
ALTER TABLE staff_invitations ADD CONSTRAINT staff_invitation_member_fk FOREIGN KEY(organisation_id,membership_id) REFERENCES staff_memberships(organisation_id,id);
ALTER TABLE approval_requests ADD COLUMN channel text NOT NULL DEFAULT '';
ALTER TABLE approval_requests ADD COLUMN operation_digest text NOT NULL DEFAULT '';
ALTER TABLE approval_requests ADD COLUMN operation_context jsonb NOT NULL DEFAULT '{}';
ALTER TABLE approval_requests ADD COLUMN consumed_at timestamptz;
ALTER TABLE approval_requests ADD COLUMN consumed_by_account_id uuid REFERENCES business_accounts(id);
ALTER TABLE approval_decisions DROP CONSTRAINT approval_decisions_request_id_level_actor_id_key;
CREATE UNIQUE INDEX approval_decisions_actor_step_idx ON approval_decisions(request_id,level,actor_id) WHERE outcome IN ('APPROVED','REJECTED');
CREATE TABLE event_failures (
 id uuid PRIMARY KEY,consumer text NOT NULL,stream text NOT NULL,sequence bigint NOT NULL,
 event_id text NOT NULL DEFAULT '',digest text NOT NULL,failure_class text NOT NULL,attempt bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(consumer,stream,sequence)
);
CREATE TABLE notification_acceptances (
 job_id uuid PRIMARY KEY,event_id uuid NOT NULL UNIQUE,notification_id uuid NOT NULL,accepted_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementBegin
CREATE FUNCTION business_approval_snapshot_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.organisation_id,NEW.policy_id,NEW.requester_id,NEW.action,NEW.scope_kind,NEW.scope_id,NEW.amount_minor,NEW.currency,NEW.policy_snapshot,NEW.deadline,NEW.operation_digest,NEW.operation_context,NEW.reason,NEW.channel)
 IS DISTINCT FROM
 (OLD.organisation_id,OLD.policy_id,OLD.requester_id,OLD.action,OLD.scope_kind,OLD.scope_id,OLD.amount_minor,OLD.currency,OLD.policy_snapshot,OLD.deadline,OLD.operation_digest,OLD.operation_context,OLD.reason,OLD.channel)
 THEN RAISE EXCEPTION 'Approval governing context is immutable' USING ERRCODE='42501'; END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER approval_request_snapshot_immutable BEFORE UPDATE ON approval_requests FOR EACH ROW EXECUTE FUNCTION business_approval_snapshot_immutable();
ALTER TABLE organisations ADD COLUMN restriction_source text NOT NULL DEFAULT '' CHECK(restriction_source IN ('','KYB','ADMIN'));
CREATE INDEX invitations_expiry_idx ON staff_invitations(expires_at,id) WHERE status='PENDING';
CREATE INDEX approvals_expiry_idx ON approval_requests(deadline,id) WHERE state IN ('PENDING','DRAFT');
CREATE INDEX membership_expiry_idx ON staff_memberships(expires_at,id) WHERE status='ACTIVE';
CREATE INDEX capacities_expiry_idx ON organisation_capacities(expires_at,id) WHERE status IN ('ACTIVE','PENDING','DECLARED');
CREATE INDEX break_glass_expiry_idx ON break_glass_grants(expires_at,id) WHERE revoked_at IS NULL;
-- +goose StatementBegin
CREATE FUNCTION business_emergency_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.organisation_id,NEW.membership_id,NEW.scope_kind,NEW.scope_id,NEW.capabilities,NEW.reason,NEW.created_at,NEW.expires_at,NEW.mfa_at) IS DISTINCT FROM (OLD.organisation_id,OLD.membership_id,OLD.scope_kind,OLD.scope_id,OLD.capabilities,OLD.reason,OLD.created_at,OLD.expires_at,OLD.mfa_at)
 OR OLD.reviewed_at IS NOT NULL AND (NEW.reviewed_at,NEW.reviewed_by,NEW.review_reason) IS DISTINCT FROM (OLD.reviewed_at,OLD.reviewed_by,OLD.review_reason)
 THEN RAISE EXCEPTION 'Emergency authority and completed review are immutable' USING ERRCODE='42501'; END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER break_glass_immutable BEFORE UPDATE ON break_glass_grants FOR EACH ROW EXECUTE FUNCTION business_emergency_immutable();
ALTER TABLE business_devices ADD COLUMN version bigint NOT NULL DEFAULT 1;
ALTER TABLE business_devices ADD COLUMN retired_at timestamptz;
ALTER TABLE approval_decisions ADD COLUMN authority_account_ids uuid[] NOT NULL DEFAULT '{}';
CREATE TABLE staff_handovers (
 id uuid PRIMARY KEY,organisation_id uuid NOT NULL,membership_id uuid NOT NULL,successor_membership_id uuid NOT NULL,
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','ACKNOWLEDGED','COMPLETED','CANCELLED')),
 initiated_by uuid NOT NULL REFERENCES business_accounts(id),reason text NOT NULL,evidence_ids uuid[] NOT NULL DEFAULT '{}',
 acknowledged_by uuid REFERENCES business_accounts(id),acknowledged_at timestamptz,acknowledgement_reason text,
 completed_by uuid REFERENCES business_accounts(id),completed_at timestamptz,completion_reason text,
 cancelled_by uuid REFERENCES business_accounts(id),cancelled_at timestamptz,
 version bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,membership_id) REFERENCES staff_memberships(organisation_id,id),
 FOREIGN KEY(organisation_id,successor_membership_id) REFERENCES staff_memberships(organisation_id,id),
 CHECK(membership_id<>successor_membership_id)
);
CREATE UNIQUE INDEX staff_handover_open_idx ON staff_handovers(organisation_id,membership_id) WHERE status IN ('PENDING','ACKNOWLEDGED');
-- +goose StatementBegin
CREATE FUNCTION business_handover_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status IN ('COMPLETED','CANCELLED') OR (NEW.organisation_id,NEW.membership_id,NEW.successor_membership_id,NEW.initiated_by,NEW.reason,NEW.evidence_ids,NEW.created_at) IS DISTINCT FROM (OLD.organisation_id,OLD.membership_id,OLD.successor_membership_id,OLD.initiated_by,OLD.reason,OLD.evidence_ids,OLD.created_at)
 THEN RAISE EXCEPTION 'Handover evidence and completed history are immutable' USING ERRCODE='42501'; END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER staff_handover_immutable BEFORE UPDATE ON staff_handovers FOR EACH ROW EXECUTE FUNCTION business_handover_immutable();
INSERT INTO permissions(code,description) VALUES
 ('finance:approve','Approve referenced financial actions; Payment Service executes money movement'),
 ('refund:approve','Approve a high-value refund reference'),('payout:approve','Approve payout destination intent'),
 ('listing:publish','Publish verified inventory in its owning future context'),('listing:approve','Approve listing publication'),
 ('revenue:approve','Approve out-of-band revenue changes'),('inventory:override','Approve inventory conflict override'),
 ('inventory:overbook','Approve explicitly bounded overbooking'),('reservations:override','Approve exceptional check-in'),
 ('night_audit:approve','Approve a business-day close exception'),('manager:grant','Approve external manager access'),
 ('professional:grant','Approve purpose-limited professional access'),('sales:approve','Approve high-value disposal/sale')
 ON CONFLICT DO NOTHING;
-- +goose StatementBegin
CREATE FUNCTION business_version_definition_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='role_templates' THEN
  IF (NEW.organisation_id,NEW.role_key,NEW.name,NEW.version,NEW.senior_emergency_eligible,NEW.created_at) IS DISTINCT FROM (OLD.organisation_id,OLD.role_key,OLD.name,OLD.version,OLD.senior_emergency_eligible,OLD.created_at) THEN RAISE EXCEPTION 'Role version is immutable' USING ERRCODE='42501'; END IF;
 ELSE
  IF (NEW.organisation_id,NEW.policy_key,NEW.version,NEW.action,NEW.risk,NEW.definition,NEW.effective_at,NEW.expires_at,NEW.created_at) IS DISTINCT FROM (OLD.organisation_id,OLD.policy_key,OLD.version,OLD.action,OLD.risk,OLD.definition,OLD.effective_at,OLD.expires_at,OLD.created_at) THEN RAISE EXCEPTION 'Policy version is immutable' USING ERRCODE='42501'; END IF;
 END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER role_version_immutable BEFORE UPDATE ON role_templates FOR EACH ROW EXECUTE FUNCTION business_version_definition_immutable();
CREATE TRIGGER policy_version_immutable BEFORE UPDATE ON approval_policies FOR EACH ROW EXECUTE FUNCTION business_version_definition_immutable();
CREATE TRIGGER role_mapping_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON role_permissions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE verification_link_history (
 id uuid PRIMARY KEY,organisation_id uuid NOT NULL REFERENCES organisations(id),case_id uuid NOT NULL UNIQUE,
 replacement_case_id uuid NOT NULL,provider_status text NOT NULL,expires_at timestamptz,provider_updated_at timestamptz,
 actor_id uuid REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(organisation_id,case_id)
);
CREATE TRIGGER verification_link_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON verification_link_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE business_external_identities (
 id uuid PRIMARY KEY,organisation_id uuid NOT NULL,account_id uuid NOT NULL,provider_id text NOT NULL,issuer text NOT NULL,subject text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),disabled_at timestamptz,
 FOREIGN KEY(organisation_id,account_id) REFERENCES staff_memberships(organisation_id,account_id),
 UNIQUE(organisation_id,provider_id,issuer,subject)
);
-- Preserve the previous account API's company-owner insertion during rollout.
-- Non-company scopes still require an explicit validated reference.
-- +goose StatementBegin
CREATE FUNCTION business_default_company_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.scope_id IS NULL AND NEW.scope_kind='COMPANY' THEN NEW.scope_id:=NEW.organisation_id; END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER staff_default_company_scope BEFORE INSERT ON staff_memberships FOR EACH ROW EXECUTE FUNCTION business_default_company_scope();
ALTER TABLE business_mfa_methods DROP CONSTRAINT business_mfa_methods_kind_check;
ALTER TABLE business_mfa_methods ADD CONSTRAINT business_mfa_methods_kind_check CHECK(kind IN ('TOTP','EMAIL','SMS'));
CREATE UNIQUE INDEX business_mfa_one_live_method_idx ON business_mfa_methods(account_id) WHERE status IN ('PENDING','ACTIVE');
ALTER TABLE business_mfa_methods ADD CONSTRAINT business_mfa_account_method_key UNIQUE(account_id,id);
CREATE TABLE business_mfa_one_time_codes (
 id uuid PRIMARY KEY,account_id uuid NOT NULL,method_id uuid NOT NULL,binding_id uuid NOT NULL,
 purpose text NOT NULL CHECK(purpose IN ('ENROL','LOGIN','STEP_UP')),code_hash text NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),expires_at timestamptz NOT NULL,consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(account_id,method_id) REFERENCES business_mfa_methods(account_id,id)
);
CREATE UNIQUE INDEX mfa_code_live_binding_idx ON business_mfa_one_time_codes(method_id,binding_id,purpose) WHERE consumed_at IS NULL;
-- +goose Down
-- Forward-only; operational rollback uses a compatible immutable image.
SELECT 1;
