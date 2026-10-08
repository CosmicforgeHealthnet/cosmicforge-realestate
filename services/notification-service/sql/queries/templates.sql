-- name: GetActiveTemplate :one
SELECT * FROM notification_templates
 WHERE template_key = $1 AND locale = $2 AND status = 'ACTIVE'
 ORDER BY version DESC LIMIT 1;

-- name: GetTemplateVersion :one
SELECT * FROM notification_templates WHERE template_key = $1 AND locale = $2 AND version = $3;

-- name: NextTemplateVersion :one
SELECT coalesce(max(version), 0)::int + 1 AS next FROM notification_templates WHERE template_key = $1 AND locale = $2;

-- name: InsertTemplate :one
INSERT INTO notification_templates (template_id, template_key, version, locale, category, channels, required_variables,
    push_title, push_body, email_subject, email_text, email_html, sms_text, ttl_seconds, status, created_by_service, created_by_actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'ACTIVE', $15, $16, $17)
RETURNING *;

-- name: ListTemplates :many
SELECT * FROM notification_templates WHERE template_key = $1 ORDER BY locale, version DESC;
