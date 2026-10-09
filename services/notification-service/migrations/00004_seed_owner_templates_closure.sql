-- Individual-owner templates added at Section 13 closure (version 1,
-- English): statutory/tax calendar reminders (§13.25) and assisted-use
-- notices (§13.27). Template versions are immutable once seeded.

-- +goose Up
INSERT INTO notification_templates (template_id, template_key, version, locale, category, channels, required_variables,
    push_title, push_body, email_subject, email_text, email_html, sms_text, ttl_seconds, status, created_by_service, created_at) VALUES
('01a11d00-0000-7000-8000-000000000126', 'owner.statutory_due', 1, 'en', 'ACTIVITY', ARRAY['PUSH','EMAIL'], ARRAY['title','due_on'],
 'Upcoming due date', '{{title}} is due on {{due_on}}.',
 'Upcoming due date - CosmicForge',
 '{{title}} is due on {{due_on}}. Open the CosmicForge owner app for details.',
 '<p>{{title}} is due on {{due_on}}.</p><p>Open the CosmicForge owner app for details.</p>',
 NULL, 86400, 'ACTIVE', 'notification-service', now()),
('01a11d00-0000-7000-8000-000000000127', 'owner.assisted_activity', 1, 'en', 'SECURITY', ARRAY['PUSH','EMAIL'], ARRAY['activity'],
 'Support assisted you', 'CosmicForge support recorded: {{activity}}. Review or revoke assistance in the app.',
 'Support activity on your account - CosmicForge',
 'CosmicForge support recorded: {{activity}}. Review the activity or revoke assistance in the CosmicForge owner app.',
 '<p>CosmicForge support recorded: {{activity}}.</p><p>Review the activity or revoke assistance in the CosmicForge owner app.</p>',
 NULL, 86400, 'ACTIVE', 'notification-service', now());

-- +goose Down
DELETE FROM notification_templates WHERE template_id IN (
 '01a11d00-0000-7000-8000-000000000126',
 '01a11d00-0000-7000-8000-000000000127');
