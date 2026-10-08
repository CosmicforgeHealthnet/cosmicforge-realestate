# notification-service

Type: **shared platform service** · Database authority: **notification_db** ·
Module: `github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service`

System-to-user notifications for every domain over **PUSH**, **EMAIL** and **SMS**.
Human-to-human messaging, voice and video belong to CF Meet and are out of scope.

## Providers (provider-neutral by design)

| Channel | Interface | Adapters | Status |
|---|---|---|---|
| PUSH | `channels.PushProvider` | `fcm` (Firebase Cloud Messaging HTTP v1), `dev` capture | **FCM is the selected push provider.** Firebase is a push transport only — never a database. |
| EMAIL | `channels.EmailProvider` | `dev` capture, `smtp` (optional) | **No production email provider selected.** SMTP is one optional adapter, not an assumed final choice. |
| SMS | `channels.SMSProvider` | `dev` capture, `twilio` (optional Twilio-compatible) | **No production SMS provider selected.** Twilio is never a default and never required. |

- Selection is configuration-driven per channel (`NOTIFY_PUSH_PROVIDER`,
  `NOTIFY_EMAIL_PROVIDER`, `NOTIFY_SMS_PROVIDER`). Defaults: `dev` capture for every channel in
  `development`/`test` (no external credentials needed); in `staging`/`production` push
  defaults to `fcm` and email/SMS to `disabled` (requests report `CHANNEL_NOT_CONFIGURED`).
- Adapter settings are read and validated **only when that adapter is selected**; no
  optional-adapter environment variable is ever required for startup.
- Use cases depend only on `channels.Sender`, `Message`, `Result` and classified errors
  (transient / permanent / invalid destination). A test fails the build if business logic
  references a concrete adapter.
- Adding a provider: implement the channel interface in `internal/channels`, add its settings
  to `internal/config`, register a factory in `internal/app/providers.go`. Notification logic,
  retries and contracts do not change.

## Boundaries

- Only this service reads/writes `notification_db`; it never reads another domain's users.
  Recipients are domain-aware references (`recipient_domain`, `recipient_id`); callers supply
  contact destinations (email, E.164 phone). PUSH uses device endpoints registered here.
- Recipient isolation: a user-domain service addresses only its own domain's users;
  `admin-service`, `payment-service` and `verification-service` may address any domain;
  end users (via the gateway) manage only their own devices and preferences.
- Provider calls never run inside a database transaction.

## Delivery model

```text
QUEUED -> DISPATCHING -> SENT | RETRY_SCHEDULED | FAILED_FINAL
RETRY_SCHEDULED -> DISPATCHING
```

- One delivery per channel destination (one per active push endpoint for PUSH; FCM v1 has no
  multicast, so per-token requests give exact per-device error handling).
- The worker claims due deliveries with `FOR UPDATE SKIP LOCKED` and a lease
  (`NOTIFY_DISPATCH_LEASE`), marks attempt *n* `DISPATCHING`, calls the provider outside the
  transaction, then records the outcome (`delivery_attempts`, `provider_responses`).
- Bounded retries: PUSH 5, EMAIL 6, SMS 4 attempts; exponential backoff from
  `NOTIFY_RETRY_BASE` to `NOTIFY_RETRY_MAX` with jitter, honouring provider `Retry-After`;
  never past the template TTL (expired deliveries fail with `EXPIRED` and are never sent).
- **At-least-once, never exactly-once.** If a worker dies after the provider accepted a message
  but before recording it, the lease expires and the attempt is recorded as `UNKNOWN`
  (`OUTCOME_UNKNOWN_LEASE_EXPIRED`) and retried — a rare duplicate is possible and preferred
  over a silently lost security notification.
- `SENT` means provider acceptance. User delivery/read receipts are tracked separately
  (`receipt_status`, currently `NONE`) and never conflated with `SENT`.

### Error classification (per adapter; business logic sees only the classification)

| Provider result | Classification | Effect |
|---|---|---|
| FCM `UNREGISTERED`, `SENDER_ID_MISMATCH`, invalid registration token | invalid destination | `FAILED_FINAL`, endpoint marked `INVALID` |
| FCM `UNAVAILABLE`, `INTERNAL`, `QUOTA_EXCEEDED`, HTTP 401/429/5xx, network | transient | retry with backoff / `Retry-After` |
| FCM `INVALID_ARGUMENT` (payload), `THIRD_PARTY_AUTH_ERROR` | permanent | `FAILED_FINAL` |
| Optional SMTP adapter: 4xx / connection errors | transient | retry |
| Optional SMTP adapter: 5xx (550–553 on RCPT = invalid recipient) | permanent / invalid destination | `FAILED_FINAL` |
| Optional Twilio-compatible adapter: 21211/21610/21612/21614 | invalid destination | `FAILED_FINAL` |
| Optional Twilio-compatible adapter: 429/5xx | transient | retry |

## Policy

- Categories: `SECURITY` (critical — ignores opt-outs and quiet hours), `ACCOUNT`,
  `TRANSACTIONAL`, `ACTIVITY` (per-channel opt-out).
- Quiet hours (`HH:MM`–`HH:MM`, IANA timezone, may cross midnight) defer non-critical PUSH and SMS.
- Suppressed channels are reported, never silently dropped: `PREFERENCE_DISABLED`,
  `NO_DESTINATION`, `NO_ACTIVE_PUSH_ENDPOINT`, `CHANNEL_NOT_CONFIGURED`.
- Content: security content (codes, alerts) is cleared as soon as a delivery is final; other
  rendered content is purged after `NOTIFY_CONTENT_RETENTION`. Events never contain content
  or destinations. Logs never contain message content, tokens or credentials.

## Templates

Versioned and immutable (`notification_templates`); notifications record the exact version.
Only `{{variable}}` placeholders exist — no expressions or includes — so variable values can
never execute; values are HTML-escaped in email HTML and stripped of control characters.
Every placeholder must be declared and every declared variable used. New versions are created
by `admin-service` only (audited). Seeded: `account.security_alert`, `account.verification_code`,
`verification.status_changed`, `activity.generic_update`.

## Internal endpoints (`Authorization: Bearer <service JWT>`)

| Method | Path | Capability |
|---|---|---|
| POST | `/internal/v1/notifications` (Idempotency-Key) | `notifications.send` |
| GET | `/internal/v1/notifications/{notification_id}` | `notifications.send` (requester/admin only) |
| POST | `/internal/v1/notifications/push-endpoints` (Idempotency-Key) | `notifications.recipients.manage` |
| DELETE | `/internal/v1/notifications/push-endpoints/{endpoint_id}` | `notifications.recipients.manage` |
| GET | `/internal/v1/notifications/recipients/{domain}/{id}/push-endpoints` | `notifications.recipients.manage` |
| GET/PUT | `/internal/v1/notifications/recipients/{domain}/{id}/preferences` | `notifications.recipients.manage` |
| POST | `/internal/v1/notifications/templates` (Idempotency-Key) | `notifications.templates.manage` (admin) |
| GET | `/internal/v1/notifications/templates/{template_key}` | `notifications.templates.read` |

Contract: `contracts/internal/openapi/notification-service.yaml`.

## Public endpoints (via API Gateway, signed-in user)

`POST|GET /api/v1/notifications/push-endpoints`, `DELETE /api/v1/notifications/push-endpoints/{endpoint_id}`,
`GET|PUT /api/v1/notifications/preferences`. Contract: `contracts/public/openapi/notifications.yaml`.

## Events published

`notification.queued.v1`, `notification.sent.v1`, `notification.failed.v1` — one per delivery,
outbox → JetStream `REALESTATE_NOTIFICATION`. Schemas: `contracts/events/notification/`.

## Events consumed

None. Services request notifications explicitly through the internal API.

## Configuration

| Variable | Notes |
|---|---|
| `DATABASE_URL[_FILE]`, `DB_MAX_CONNS` | notification_db only |
| `SERVICE_AUTH_KEY_ID`, `SERVICE_AUTH_PRIVATE_KEY[_FILE]`, `SERVICE_AUTH_TRUSTED_KEYS[_FILE]` | service identity |
| `NOTIFY_PUSH_PROVIDER` | `fcm` \| `dev` \| `disabled`; default `dev` (development/test), `fcm` otherwise |
| `FCM_PROJECT_ID`, `FCM_CREDENTIALS_JSON[_FILE]`, `FCM_BASE_URL` | only when push = `fcm`; service-account JSON from secrets management |
| `NOTIFY_EMAIL_PROVIDER` | `dev` \| `disabled` \| `smtp`; default `dev` (development/test), `disabled` otherwise |
| `SMTP_HOST`, `SMTP_PORT` (587), `SMTP_USERNAME`, `SMTP_PASSWORD[_FILE]`, `SMTP_FROM`, `SMTP_FROM_NAME`, `SMTP_REQUIRE_TLS` (true; required in production) | only when email = `smtp` (optional adapter) |
| `NOTIFY_SMS_PROVIDER` | `dev` \| `disabled` \| `twilio`; default `dev` (development/test), `disabled` otherwise |
| `TWILIO_ACCOUNT_SID`, `TWILIO_AUTH_TOKEN[_FILE]`, `TWILIO_FROM` or `TWILIO_MESSAGING_SERVICE_SID`, `TWILIO_BASE_URL` | only when SMS = `twilio` (optional adapter) |
| `NOTIFY_DISPATCH_LEASE` (2m), `NOTIFY_SEND_TIMEOUT` (15s, ≤ lease/2), `NOTIFY_RETRY_BASE` (30s), `NOTIFY_RETRY_MAX` (1h), `NOTIFY_CONTENT_RETENTION` (168h), `NOTIFY_DEFAULT_LOCALE` (en) | |
| `NATS_URL`, `NATS_SUBJECT_PREFIX`, `NATS_ENSURE_STREAM` | worker |
| `OUTBOX_POLL_INTERVAL`, `DISPATCH_POLL_INTERVAL`, `DISPATCH_BATCH`, `PURGE_INTERVAL` | worker |

`dev` providers capture messages in memory and are refused when `APP_ENV=production`.

## Migrations / tests / deployment

```bash
DATABASE_URL=... go run ./cmd/migrate up
sqlc generate -f sql/sqlc.yaml
CF_TEST_POSTGRES_URL=postgres://... go test -race ./...
```

Unit tests cover the state machine, quiet hours, template rendering/validation, provider
selection, the provider-neutral boundary, and the FCM adapter (including the service-account
OAuth flow) plus the optional SMTP and Twilio-compatible adapters against local fakes (error
classification, Retry-After, no secret leakage) — no real provider credentials are used.
Integration tests (real PostgreSQL, a fake FCM HTTP v1 server, capture email/SMS providers,
embedded JetStream)
cover migrations, queue/dispatch lifecycle, idempotent and concurrent duplicate commands,
recipient isolation and caller policy, push endpoint lifecycle/reassignment/revocation,
preferences, critical override, quiet hours, validation and suppression, transient retry and
exhaustion, permanent failure and token invalidation, lease recovery with UNKNOWN outcomes,
concurrent dispatchers, expiry, content purge, template versioning, event relay with broker
outage and schema validation, OpenAPI drift and health.

Image: one distroless non-root image with `/app/api`, `/app/worker`, `/app/migrate`.
