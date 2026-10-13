-- +goose Up
ALTER TABLE owner_external_operations DROP CONSTRAINT owner_external_operations_kind_check;
ALTER TABLE owner_external_operations ADD CONSTRAINT owner_external_operations_kind_check CHECK(kind IN ('SMART_ACCESS_PROVISION','ESIGN_ENVELOPE','FINANCING_DECISION'));
-- Reference-only quarantine. Original content remains in the producer stream;
-- hash + immutable stream sequence permit controlled, attributable replay.
CREATE TABLE owner_event_review (
 id uuid PRIMARY KEY,
 consumer text NOT NULL,
 stream text NOT NULL,
 stream_sequence bigint NOT NULL CHECK (stream_sequence > 0),
 event_id text NOT NULL,
 payload_sha256 text NOT NULL CHECK (length(payload_sha256)=64),
 failure_class text NOT NULL CHECK (failure_class IN ('MALFORMED','PERMANENT','EXHAUSTED')),
 attempt bigint NOT NULL CHECK (attempt>0),
 observed_at timestamptz NOT NULL,
 UNIQUE(consumer,stream,stream_sequence)
);
-- +goose StatementBegin
CREATE FUNCTION owner_event_review_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Owner event review history is immutable'; END;
$$;
-- +goose StatementEnd
CREATE TRIGGER owner_event_review_no_mutation BEFORE UPDATE OR DELETE OR TRUNCATE ON owner_event_review
 FOR EACH STATEMENT EXECUTE FUNCTION owner_event_review_immutable();

CREATE TABLE owner_event_review_decisions (
 id uuid PRIMARY KEY,
 review_id uuid NOT NULL UNIQUE REFERENCES owner_event_review(id),
 decision text NOT NULL CHECK(decision IN ('REPLAY_APPROVED','NO_ACTION')),
 reason_code text NOT NULL CHECK(reason_code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
 evidence_ref uuid NOT NULL,
 actor_id uuid NOT NULL,
 decided_at timestamptz NOT NULL
);
CREATE TRIGGER owner_event_review_decisions_no_mutation BEFORE UPDATE OR DELETE OR TRUNCATE ON owner_event_review_decisions
 FOR EACH STATEMENT EXECUTE FUNCTION owner_event_review_immutable();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM owner_event_review) OR EXISTS(SELECT 1 FROM owner_external_operations WHERE kind='FINANCING_DECISION') THEN
  RAISE EXCEPTION 'Cannot remove populated Owner event review history';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE owner_event_review_decisions;
DROP TABLE owner_event_review;
DROP FUNCTION owner_event_review_immutable();

ALTER TABLE owner_external_operations DROP CONSTRAINT owner_external_operations_kind_check;
ALTER TABLE owner_external_operations ADD CONSTRAINT owner_external_operations_kind_check CHECK(kind IN ('SMART_ACCESS_PROVISION','ESIGN_ENVELOPE'));
