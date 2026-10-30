-- +goose Up
CREATE TABLE outbox_events (
    event_id uuid PRIMARY KEY,
    event_type text NOT NULL,
    producer text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    subject text NOT NULL,
    trace_id text,
    actor_domain text,
    actor_id uuid,
    metadata jsonb,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_expires_at timestamptz,
    published_at timestamptz,
    dead_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX outbox_events_pending_idx ON outbox_events (next_attempt_at, occurred_at)
    WHERE published_at IS NULL AND dead_at IS NULL;
CREATE INDEX outbox_events_aggregate_idx ON outbox_events (aggregate_type, aggregate_id, occurred_at);

-- +goose Down
DROP TABLE outbox_events;
