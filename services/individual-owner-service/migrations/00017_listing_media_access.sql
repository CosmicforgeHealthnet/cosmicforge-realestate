-- Marketplace read access to published listing media (§13.7).
--
-- media-document-service keeps listing photos PRIVATE (owner domain only)
-- unless the owning domain's service changes the access policy. While a
-- listing that uses a file is live (PUBLISHED or PAUSED) the owner service
-- grants marketplace-service read access to it; once no live listing uses
-- the file the grant is removed. This table records the last policy the
-- worker applied per file so the reconciliation is idempotent.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE media_marketplace_access (
    media_id    uuid        PRIMARY KEY,
    granted     boolean     NOT NULL,
    synced_at   timestamptz NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS media_marketplace_access;
-- +goose StatementEnd
