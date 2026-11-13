-- +goose Up
-- When Marketplace last asserted the customer's authenticated identity to
-- Business for this booking (assertions are short-lived and re-issued).
ALTER TABLE checkouts ADD COLUMN guest_asserted_at timestamptz;

-- +goose Down
SELECT 1;
