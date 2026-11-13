-- +goose Up
-- Marketplace-owned receiving side of the Business Operations supply
-- contract (business-supply.v1). Marketplace stores its own projection of
-- business supply; it never reads business_operations_db. Accepted commands
-- are append-only; listing projections advance only with canonical version.

CREATE TABLE supply_connections (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),
 organisation_id uuid NOT NULL,
 property_id uuid NOT NULL,
 provider_account_id text NOT NULL CHECK(length(provider_account_id) BETWEEN 3 AND 200),
 credential_reference text NOT NULL CHECK(length(credential_reference) BETWEEN 3 AND 300),
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','SUSPENDED')),
 validation_reference text NOT NULL CHECK(length(validation_reference) BETWEEN 3 AND 300),
 validated_at timestamptz NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX supply_connections_org ON supply_connections(organisation_id,property_id);

CREATE TABLE supply_commands (
 connection_id uuid NOT NULL REFERENCES supply_connections(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 300),
 job_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('CONTENT','ARI','WITHDRAW','PAUSE','RECONCILE')),
 canonical_version bigint NOT NULL CHECK(canonical_version>0),
 payload_hash text NOT NULL CHECK(payload_hash ~ '^[a-f0-9]{64}$'),
 payload jsonb NOT NULL,
 provider_request_id text NOT NULL UNIQUE CHECK(length(provider_request_id) BETWEEN 8 AND 200),
 state text NOT NULL CHECK(state IN ('PUBLISHED','SYNCHRONIZED','WITHDRAWN','PAUSED')),
 accepted_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(connection_id,idempotency_key)
);

-- +goose StatementBegin
CREATE FUNCTION supply_commands_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'supply commands are append-only' USING ERRCODE='23514';
END $$;
-- +goose StatementEnd
CREATE TRIGGER supply_commands_immutable BEFORE UPDATE OR DELETE ON supply_commands FOR EACH ROW EXECUTE FUNCTION supply_commands_append_only();

CREATE TABLE listing_projections (
 connection_id uuid NOT NULL REFERENCES supply_connections(id),
 kind text NOT NULL CHECK(kind IN ('CONTENT','ARI')),
 canonical_version bigint NOT NULL CHECK(canonical_version>0),
 payload_hash text NOT NULL CHECK(payload_hash ~ '^[a-f0-9]{64}$'),
 payload jsonb NOT NULL,
 publication_state text NOT NULL CHECK(publication_state IN ('PUBLISHED','PAUSED','WITHDRAWN')),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(connection_id,kind)
);

-- Customer-side events Business polls (reservation requests, changes).
-- Producers are Marketplace customer journeys; Business consumes by cursor.
CREATE TABLE supply_events (
 sequence bigserial PRIMARY KEY,
 connection_id uuid NOT NULL REFERENCES supply_connections(id),
 provider_event_id text NOT NULL UNIQUE CHECK(length(provider_event_id) BETWEEN 3 AND 200),
 receipt jsonb NOT NULL CHECK(jsonb_typeof(receipt)='object'),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX supply_events_connection ON supply_events(connection_id,sequence);

-- +goose Down
SELECT 1;
