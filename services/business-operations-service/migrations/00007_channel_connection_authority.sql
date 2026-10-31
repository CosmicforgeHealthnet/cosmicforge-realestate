-- +goose Up
-- Capability certification is provider-wide; credential grants are per connection.
-- Existing connections intentionally fail closed until authoritative revalidation.
ALTER TABLE channel_connections ADD COLUMN authorized_capabilities text[] NOT NULL DEFAULT '{}' CHECK(cardinality(authorized_capabilities)<=32);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Forward-only connection authority migration'; END $$;
-- +goose StatementEnd
