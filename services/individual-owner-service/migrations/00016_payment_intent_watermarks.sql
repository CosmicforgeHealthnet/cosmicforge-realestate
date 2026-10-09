-- Payment outcome ordering (§10.1 out-of-order delivery).
--
-- payment-service intent updates (events and read-backs) carry the
-- producer's updated_at. The latest applied value per intent is kept here;
-- an older or equal update is ignored, so a late or replayed delivery can
-- never roll a projection (paid or refunded amounts, status) backwards.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE payment_intent_watermarks (
    intent_ref         text        PRIMARY KEY CHECK (char_length(intent_ref) BETWEEN 1 AND 128),
    source_updated_at  timestamptz NOT NULL,
    recorded_at        timestamptz NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS payment_intent_watermarks;
-- +goose StatementEnd
