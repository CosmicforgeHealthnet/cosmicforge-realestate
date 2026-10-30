-- name: OutboxBacklog :one
SELECT
    count(*) FILTER (WHERE published_at IS NULL AND dead_at IS NULL)::bigint AS pending,
    count(*) FILTER (WHERE dead_at IS NOT NULL)::bigint AS dead
FROM outbox_events;
