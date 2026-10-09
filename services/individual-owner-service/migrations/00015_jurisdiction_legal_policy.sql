-- Versioned jurisdiction legal policy (§12 policy configuration, §13.13–
-- §13.15, §13.20).
--
-- Timelines and automatic decisions with legal effect (deposit-deduction
-- acceptance on tenant silence, automatic late fees, damage-claim response
-- windows, deposit release deadlines, short-let holds) come from a policy
-- version approved for the property's jurisdiction, never from code
-- defaults. A version is drafted, approved against a recorded legal
-- approval reference, and retired when superseded; approved rules are
-- immutable. Without an approved version consequential automatic decisions
-- are disabled and the case goes to explicit manual review.
--
-- Damage claims are never accepted by guest silence: a lapsed response
-- window moves the claim to RESPONSE_OVERDUE for operator review. Prior
-- claims and deposits keep their recorded outcomes.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE jurisdiction_policies (
    id                    uuid PRIMARY KEY,
    country               char(2)     NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    region                text        CHECK (char_length(region) BETWEEN 1 AND 100 AND region = upper(region)),
    version_no            integer     NOT NULL CHECK (version_no > 0),
    status                text        NOT NULL CHECK (status IN ('DRAFT','APPROVED','RETIRED')),
    rules                 jsonb       NOT NULL,
    summary               text        NOT NULL CHECK (char_length(summary) BETWEEN 3 AND 500),
    legal_approval_ref    text        CHECK (char_length(legal_approval_ref) BETWEEN 3 AND 200),
    created_by_actor_id   uuid        NOT NULL,
    approved_by_actor_id  uuid,
    approved_at           timestamptz,
    retired_at            timestamptz,
    created_at            timestamptz NOT NULL,
    CONSTRAINT jurisdiction_policies_approval_chk CHECK (status = 'DRAFT'
        OR (legal_approval_ref IS NOT NULL AND approved_by_actor_id IS NOT NULL AND approved_at IS NOT NULL)),
    CONSTRAINT jurisdiction_policies_retired_chk CHECK ((status = 'RETIRED') = (retired_at IS NOT NULL))
);
CREATE UNIQUE INDEX jurisdiction_policies_version_uniq ON jurisdiction_policies (country, coalesce(region, ''), version_no);
-- At most one approved version per jurisdiction.
CREATE UNIQUE INDEX jurisdiction_policies_approved_uniq ON jurisdiction_policies (country, coalesce(region, '')) WHERE status = 'APPROVED';

-- Drafts are editable and deletable; approval and retirement are the only
-- later changes, and approved rules never change.
CREATE FUNCTION jurisdiction_policy_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status <> 'DRAFT' THEN
            RAISE EXCEPTION 'approved or retired jurisdiction policies are immutable' USING ERRCODE = 'check_violation';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.status = 'DRAFT' THEN
        RETURN NEW;
    END IF;
    IF NOT (OLD.status = 'APPROVED' AND NEW.status = 'RETIRED')
       OR (to_jsonb(NEW) - 'status' - 'retired_at') <> (to_jsonb(OLD) - 'status' - 'retired_at') THEN
        RAISE EXCEPTION 'approved jurisdiction policies can only be retired' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER jurisdiction_policies_guard BEFORE UPDATE OR DELETE ON jurisdiction_policies
    FOR EACH ROW EXECUTE FUNCTION jurisdiction_policy_guard();

ALTER TABLE damage_claims DROP CONSTRAINT damage_claims_status_check;
ALTER TABLE damage_claims ADD CONSTRAINT damage_claims_status_check
    CHECK (status IN ('OPEN','GUEST_ACCEPTED','DISPUTED','RESPONSE_OVERDUE','RESOLVED','WITHDRAWN','SETTLED'));
DROP INDEX damage_claims_open_uniq;
CREATE UNIQUE INDEX damage_claims_open_uniq ON damage_claims (booking_id) WHERE status IN ('OPEN','GUEST_ACCEPTED','DISPUTED','RESPONSE_OVERDUE','RESOLVED');

ALTER TABLE deposit_obligations DROP CONSTRAINT deposit_obligations_tenant_response_check;
ALTER TABLE deposit_obligations ADD CONSTRAINT deposit_obligations_tenant_response_check
    CHECK (tenant_response IN ('ACCEPTED','DISPUTED','NO_RESPONSE'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Rows recorded under the new values block the down migration on purpose:
-- history is never rewritten.
ALTER TABLE deposit_obligations DROP CONSTRAINT deposit_obligations_tenant_response_check;
ALTER TABLE deposit_obligations ADD CONSTRAINT deposit_obligations_tenant_response_check CHECK (tenant_response IN ('ACCEPTED','DISPUTED'));
DROP INDEX damage_claims_open_uniq;
CREATE UNIQUE INDEX damage_claims_open_uniq ON damage_claims (booking_id) WHERE status IN ('OPEN','GUEST_ACCEPTED','DISPUTED','RESOLVED');
ALTER TABLE damage_claims DROP CONSTRAINT damage_claims_status_check;
ALTER TABLE damage_claims ADD CONSTRAINT damage_claims_status_check
    CHECK (status IN ('OPEN','GUEST_ACCEPTED','DISPUTED','RESOLVED','WITHDRAWN','SETTLED'));
DROP TABLE IF EXISTS jurisdiction_policies;
DROP FUNCTION IF EXISTS jurisdiction_policy_guard();
-- +goose StatementEnd
