# verification-service

Type: **shared platform service** · Database authority: **verification_db** ·
Module: `github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service`

Owns verification truth for KYC/KYB, property authority and professional
credentials across all seven user domains. It does not own users or accounts
and never reads another service's database: subjects are referenced as
`(subject_type, subject_domain, subject_id)` and evidence as media-document-service `media_id`s.

## Model

Verification types: `IDENTITY`, `BUSINESS`, `PROPERTY_AUTHORITY`, `PROFESSIONAL_CREDENTIAL`.

State machine (exactly as approved):

```text
DRAFT -> SUBMITTED -> PROCESSING -> VERIFIED | REJECTED | MANUAL_REVIEW
MANUAL_REVIEW -> VERIFIED | REJECTED
VERIFIED -> EXPIRED
```

Ambiguous provider outcomes (timeouts, 5xx) keep the case `PROCESSING` with internal
substatus `UNKNOWN_PROVIDER_RESULT`; the next attempt starts with a **read-back** by our
client reference and only re-submits when the provider confirms it never received the
request. Provider failures that cannot produce a decision escalate to `MANUAL_REVIEW`
(never an automatic `REJECTED`). Retries are bounded.

Tables: `verification_cases`, `verification_evidence_refs`, `verification_attempts`
(leased work items), `provider_requests` (immutable call log, no PII), `provider_results`
(unique per provider event → duplicate callbacks are no-ops), `manual_reviews`,
`verification_decisions` (one final decision per case), `verification_history`,
`idempotency_records`, `outbox_events`, `audit_events` (append-only).
Database constraints enforce one open case per subject/type and valid states.

## Public endpoints (via API Gateway)

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/verification/cases` | The signed-in user's own cases |
| GET | `/api/v1/verification/cases/{verification_id}` | One of the user's own cases |

Requires the gateway's service assertion (`X-Service-Authorization`) with a delegated actor.
Contract: `contracts/public/openapi/verification.yaml`.

## Internal endpoints (`Authorization: Bearer <service JWT>`)

| Method | Path | Callers |
|---|---|---|
| POST | `/internal/v1/verifications` | domain services (own-domain subjects, approved types) |
| GET | `/internal/v1/verifications?subject_*` | subject's domain service, admin-service |
| GET | `/internal/v1/verifications/{id}`, `/result` | subject's domain, requester, admin-service |
| POST | `/internal/v1/verifications/{id}/evidence` | requester (DRAFT only) |
| POST | `/internal/v1/verifications/{id}/submit` | requester |
| POST | `/internal/v1/verifications/{id}/process` | requester, admin-service |
| POST | `/internal/v1/verifications/{id}/reverify` | subject's domain service |
| POST | `/internal/v1/verifications/{id}/expire` | admin-service + admin actor |
| GET | `/internal/v1/manual-reviews` | admin-service + admin actor |
| POST | `/internal/v1/verifications/{id}/manual-review/assign` | admin-service + admin actor |
| POST | `/internal/v1/verifications/{id}/manual-review/decision` | admin-service + admin actor |

All commands require `Idempotency-Key`. Contract: `contracts/internal/openapi/verification-service.yaml`.

## Webhooks

`POST /webhooks/v1/{provider}/callbacks` — via the gateway; authenticated by the provider
adapter's signature. Contract: `contracts/webhooks/verification-providers.yaml`.

## Events published (transactional outbox → NATS JetStream `REALESTATE_VERIFICATION`)

`verification.submitted.v1`, `verification.processing.v1`, `verification.manual_review_required.v1`,
`verification.verified.v1`, `verification.rejected.v1`, `verification.expired.v1` —
schemas in `contracts/events/verification/`. Subject: `realestate.<event_type>`.

## Events consumed

None. Evidence readiness is checked synchronously against media-document-service.

## External providers

Provider adapters implement `Submit`, `ReadStatus`, `VerifyCallback`, `FetchResult`
(`internal/integrations/providers`). Only `sandbox-kyc` (deterministic development/test
adapter; refused when `APP_ENV=production`) ships today. A production KYC/KYB adapter must
be added before production use — see Known limitations.

## Configuration

| Variable | Process | Notes |
|---|---|---|
| `APP_ENV`, `LOG_LEVEL`, `HTTP_ADDR` | both | api `:8080`, worker `:8081` (health) |
| `DATABASE_URL[_FILE]`, `DB_MAX_CONNS` | both | verification_db credentials only |
| `SERVICE_AUTH_KEY_ID`, `SERVICE_AUTH_PRIVATE_KEY[_FILE]`, `SERVICE_AUTH_TRUSTED_KEYS[_FILE]` | both | service identity + trusted peers |
| `MEDIA_SERVICE_URL`, `MEDIA_SERVICE_TIMEOUT` | both | evidence checks |
| `NATS_URL`, `NATS_SUBJECT_PREFIX`, `NATS_ENSURE_STREAM` | worker | |
| `VERIFICATION_PROVIDERS`, `VERIFICATION_DEFAULT_PROVIDER`, `SANDBOX_KYC_CALLBACK_SECRET[_FILE]` | both | |
| `VERIFICATION_VALIDITY_<TYPE>` | both | e.g. `8760h` |
| `PROVIDER_TIMEOUT`, `PROVIDER_MAX_SUBMIT_TRIES`, `PROVIDER_MAX_POLL_TRIES`, `PROVIDER_POLL_INTERVAL` | worker | |
| `OUTBOX_POLL_INTERVAL`, `DISPATCH_POLL_INTERVAL`, `EXPIRY_POLL_INTERVAL` | worker | |
| `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_TRACES_SAMPLER_RATIO` | both | |

## Migrations

```bash
DATABASE_URL=... go run ./cmd/migrate up        # or: down-to <v> | status | version
# in deployment: docker run ... <image> /app/migrate up
sqlc generate -f sql/sqlc.yaml                    # regenerate internal/repositories/db
```

## Tests

```bash
export CF_TEST_POSTGRES_URL='postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable'
go test -race ./...
```

Integration tests use real PostgreSQL (fresh database per test), embedded NATS JetStream and a
media-document-service stand-in that verifies service JWTs. Covered: migrations up/down/up,
state machine and invalid transitions, service authentication, caller allow-lists, concurrent
idempotent creation, idempotent submission replay, evidence cross-service checks and
dependency outage, duplicate/forged callbacks, provider timeout → read-back without
resubmission, not-found → safe resubmission, retryable/permanent/persistent failures, manual
review authorization and attribution, expiry and re-verification, audit trail, outbox relay
with broker outage and event contract validation, worker graceful stop, public visibility
rules, OpenAPI drift, health/readiness/version.

## Deployment

`services/verification-service/Dockerfile` builds `/app/api`, `/app/worker`, `/app/migrate`
into one distroless non-root image. Run `migrate up` as a one-off before rolling `api` and
`worker`. Both expose `/health`, `/ready`, `/version`.

## Known limitations

- No production KYC/KYB provider adapter yet (no credentials/vendor selected). Production
  configuration refuses the sandbox, so the service cannot be enabled in production until an
  adapter is added behind the existing interface.
- Provider document delivery uses `EvidenceSource` (short-lived media URLs); the sandbox does
  not fetch documents.
