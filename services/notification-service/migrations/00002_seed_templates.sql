-- Platform baseline templates (version 1, English). New versions are added
-- through the admin-only template API; existing versions are immutable.

-- +goose Up
INSERT INTO notification_templates (template_id, template_key, version, locale, category, channels, required_variables,
    push_title, push_body, email_subject, email_text, email_html, sms_text, ttl_seconds, status, created_by_service, created_at) VALUES
('01a11d00-0000-7000-8000-000000000001', 'account.security_alert', 1, 'en', 'SECURITY', ARRAY['PUSH','EMAIL','SMS'], ARRAY['event'],
 'Security alert', 'New security activity on your account: {{event}}.',
 'Security alert on your CosmicForge account',
 'We noticed new security activity on your account: {{event}}. If this was not you, secure your account immediately.',
 '<p>We noticed new security activity on your account: <strong>{{event}}</strong>.</p><p>If this was not you, secure your account immediately.</p>',
 'CosmicForge security alert: {{event}}. If this was not you, secure your account.', 3600, 'ACTIVE', 'notification-service', now()),
('01a11d00-0000-7000-8000-000000000002', 'account.verification_code', 1, 'en', 'SECURITY', ARRAY['EMAIL','SMS'], ARRAY['code'],
 NULL, NULL,
 'Your CosmicForge verification code',
 'Your verification code is {{code}}. It expires in 10 minutes. Never share this code.',
 '<p>Your verification code is <strong>{{code}}</strong>.</p><p>It expires in 10 minutes. Never share this code.</p>',
 'CosmicForge code: {{code}}. Expires in 10 minutes. Never share it.', 600, 'ACTIVE', 'notification-service', now()),
('01a11d00-0000-7000-8000-000000000003', 'verification.status_changed', 1, 'en', 'ACCOUNT', ARRAY['PUSH','EMAIL'], ARRAY['status'],
 'Verification update', 'Your verification is now {{status}}.',
 'Your verification status changed',
 'Your verification is now {{status}}. Open the app for details.',
 '<p>Your verification is now <strong>{{status}}</strong>.</p><p>Open the app for details.</p>',
 NULL, 86400, 'ACTIVE', 'notification-service', now()),
('01a11d00-0000-7000-8000-000000000004', 'activity.generic_update', 1, 'en', 'ACTIVITY', ARRAY['PUSH','EMAIL','SMS'], ARRAY['title','message'],
 '{{title}}', '{{message}}', '{{title}}', '{{message}}', '<p>{{message}}</p>', '{{message}}', 86400, 'ACTIVE', 'notification-service', now());

-- +goose Down
DELETE FROM notification_templates WHERE template_id IN (
 '01a11d00-0000-7000-8000-000000000001', '01a11d00-0000-7000-8000-000000000002',
 '01a11d00-0000-7000-8000-000000000003', '01a11d00-0000-7000-8000-000000000004');
