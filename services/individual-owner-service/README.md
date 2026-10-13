# individual-owner-service

Type: **user-domain service**  
Database authority: **individual_owner_db**  
Public prefix: `/api/v1/owner/*` (ADR-0011) · Internal prefix: `/internal/v1/owner/*`

Individual Property Owner. Implementation status, requirement-by-requirement, is tracked in
[`docs/product/individual-owner/IMPLEMENTATION_COVERAGE.md`](../../docs/product/individual-owner/IMPLEMENTATION_COVERAGE.md).
**Implemented:** Section 5 (authentication, roles, authorization) and the Sections 6–13 business domains —
ownership, properties, listings, pricing, availability, leads, viewings, applications, agreements, tenancy,
rent/deposits, utilities, commercial/land/sale, short-let, delegation, maintenance, professionals, disputes,
privacy, reports/insights/dashboard, financing, insurance and smart access. Features that depend on services
or providers that do not exist yet (e-signature, calendar sync, smart locks, financing/
insurance partners, CF Meet, maps) answer `503 FEATURE_UNAVAILABLE`; see the coverage register for the
exact status and certification level of every item.
The authoritative specification is
[`docs/product/individual-owner/CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.pdf`](../../docs/product/individual-owner/CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.pdf).

## Current Financial Core integration

`PAYMENT_WALLET_SERVICE_URL` reuses the signed wallet client for accepted financial
workflows, original receipt/resource readback, verified beneficiaries, owned history,
payout preferences and independently approved withdrawals. `PAYMENT_SERVICE_URL`
remains refused because its generic legacy commands lack accepted quote/party/consent
context. Source business truth stays here; balances, allocation, funding, release,
refund execution, provider reconciliation and financial approvals stay in Payment Core.
See the [current Sections 12–13 closure](../../docs/product/individual-owner/SECTIONS_12_13_INTEGRATION_CLOSURE.md)
for supported workflows, exact evidence and external prerequisites. A test fixture is
not production custody, bank verification or a real tenant/beneficiary approval.

## Account and authentication ownership

This service owns the accounts used by its product/domain, including the domain's user records,
credentials, sessions, devices, MFA and security history. There is no central Real Estate identity
microservice. Reusable primitives come from `shared/go` (`auth`, `jwt`, `passwords`, `sessions`,
`serviceauth`, `idempotency`, `outbox`, `audit`); persistence and authority stay in `individual_owner_db`.

| Capability | Implementation |
|---|---|
| Passwords | Argon2id (shared), rehash on login, policy-checked, never logged |
| Access tokens | EdDSA `at+jwt`, issuer `individual-owner-service`, 15 min, `kid`, `jti`, `sid`; JWKS at `/.well-known/jwks.json` |
| Refresh tokens | opaque `cfrt_`, SHA-256 at rest, rotated per use, family revoked on reuse (shared `sessions.Manager` over `auth_sessions`/`auth_refresh_tokens`) |
| Request authentication | gateway service JWT (`X-Service-Authorization`) **and** the owner token, re-verified here, bound to the gateway-asserted actor, live session + account state checked per request |
| MFA | RFC 6238 TOTP (replay-protected, AES-256-GCM sealed seeds), 10 hashed recovery codes, SMS/EMAIL codes via notification-service |
| Step-up | per-session freshness (default 5 min); MFA accounts must use a second factor |
| Abuse controls | account lockout (5 failures / 15 min), bounded code attempts, per-purpose send limits, timing equalization, no account enumeration on OTP/recovery; edge rate limits at the gateway |
| Devices | hashed client device keys, suspicious (unrecognized) device alerts, trust/revoke |
| Lifecycle | PENDING_VERIFICATION → ACTIVE → RESTRICTED / SUSPENDED → CLOSURE_PENDING → CLOSED (guarded FSM, history, audit, outbox) |
| Authorization | `internal/authz` policy engine for owner sessions, service principals and workers, including property-scoped owner / co-owner / representative / signatory / manager evaluation |

## Binaries

`/app/api` (HTTP), `/app/worker` (outbox relay to NATS JetStream, §10.1 event consumers, security-alert
dispatch, closure finalization, housekeeping and the Section 11 domain jobs listed by
`usecases.DomainJobs`), `/app/migrate up|down-to|status|version`.

`/app/api readinesscheck` and `/app/worker readinesscheck` probe `/ready` before
loading process secrets; container dependencies use this command so database,
schema, required broker/consumer or shutdown failures cannot appear ready.
`healthcheck` retains the existing `/health` liveness probe. Container execution
and SIGTERM certification remain independent release gates.

## Configuration

| Variable | Required | Notes |
|---|---|---|
| `DATABASE_URL[_FILE]` | yes | `individual_owner_db` only |
| `SERVICE_AUTH_KEY_ID`, `SERVICE_AUTH_PRIVATE_KEY[_FILE]`, `SERVICE_AUTH_TRUSTED_KEYS[_FILE]` | yes | service identity; trust `api-gateway`, `admin-service`, peers |
| `OWNER_ACCESS_TOKEN_KEY_ID`, `OWNER_ACCESS_TOKEN_PRIVATE_KEY[_FILE]` | yes | Ed25519 signing key |
| `OWNER_ACCESS_TOKEN_RETIRED_KEYS` | no | JSON `[{kid, public_key}]` still accepted during rotation |
| `OWNER_CODE_PEPPER[_FILE]` | yes | ≥ 32 bytes; HMAC key for codes, tokens, device keys, recovery codes |
| `OWNER_MFA_ENCRYPTION_KEY_ID`, `OWNER_MFA_ENCRYPTION_KEY[_FILE]` | yes | 32-byte base64 AES key; `OWNER_MFA_RETIRED_KEYS` for rotation |
| `NOTIFICATION_SERVICE_URL` | yes | one-time codes and security alerts |
| `NATS_URL[_FILE]` | worker | JetStream (`REALESTATE_OWNER`, `realestate.owner.>`) |
| `OWNER_SECURITY_POLICY_VERSION` and `OWNER_*` policy knobs | no | lockout, code TTL/attempts/limits, MFA fallbacks, activation rule (`EMAIL` default), closure cooling-off (30 days) |
| `OWNER_OAUTH_PROVIDERS` | must be empty | no OAuth/OIDC adapter is certified; startup fails otherwise |
| `VERIFICATION_SERVICE_URL`, `MEDIA_SERVICE_URL` | recommended | identity/authority verification and evidence/media; dependent features answer 503 when unset |
| `PAYMENT_SERVICE_URL`, `ESIGN_PROVIDER`, `EXTERNAL_CALENDAR_PROVIDER`, `FINANCING_PROVIDER`, `INSURANCE_PROVIDER`, `SMART_ACCESS_PROVIDER` | must be empty | no implemented contract/adapter yet; startup fails otherwise so nobody believes the feature is live |
| `DOMAIN_WORKER_INTERVAL`, `PROPERTY_IMPORT_LEASE` | no | Section 11 job cadence and import lease |

Section 19 delivery validation additionally bounds `DB_MAX_CONNS` to 1–1000 before
conversion, validates `HTTP_ADDR` port 1–65535, positive worker/security durations,
HTTP(S) dependency URLs, literal NATS subjects, broker URL schemes and finite OTEL
sampling 0–1. `OTEL_EXPORTER_OTLP_ENDPOINT` is collector `host:port`. Optional
dependency absence retains existing degradation; absent-provider selectors and
endpoint variables refuse activation. Owner does not use Redis; Gateway budgets do.

`OWNER_AUTH_PRIVATE_KEY[_FILE]` aliases the established access-signing variable.
The established name takes precedence only when both definitions parse to the same
key; conflicting or invalid pairs fail startup. Direct and `_FILE` for one secret
must not coexist. Retired access keys now support `_FILE`, reject duplicate IDs and
retain the existing rotation protocol. Issuer/audience overrides must match the
Gateway's explicitly provisioned trust configuration; defaults remain
`individual-owner-service` / `cosmicforge-realestate-api`. Wrong claims remain denied.

On Unix staging/production, secret files must be regular files with no world access
or group write; file ownership/group must allow the distroless process to read them.
Windows ACL enforcement is a deployment prerequisite, not a POSIX-mode claim.
Safe startup errors name variables, never supplied values or file paths. Add new
public keys to peer trust before switching signing key IDs; retain old user/MFA
keys until tokens/encrypted seeds expire or migrate. Rotating the code pepper
invalidates pending HMAC-bound codes/device keys; coordinate that operational change.
Provision actual secrets through approved storage using
`infrastructure/docker/service-env/individual-owner-service.env.example`.

## Consumed events (consumer contract)

Payloads this service accepts from producers that do not publish yet. Each delivery must carry the
producer's service name in the envelope `producer`; handlers are idempotent per `event_id`. Only the
listed `.v1` types are applied; other types or versions, and payloads with a `schema_version` other
than 1, are terminated without effect. `payment.intent.updated.v1` requires `updated_at`: older or
equal updates per intent are ignored. Status per family:
[`docs/product/individual-owner/EVENT_COMPATIBILITY.md`](../../docs/product/individual-owner/EVENT_COMPATIBILITY.md).

| Event (stream) | Payload | Effect |
|---|---|---|
| `payment.intent.updated.v1` (`payment`) | `intent_ref`, `status` (`REQUIRES_PAYMENT`, `PROCESSING`, `PARTIALLY_PAID`, `SUCCEEDED`, `HELD`, `FAILED`, `CANCELLED`, `REFUND_PENDING`, `REFUNDED`), `amount_minor`, `paid_minor`, `refunded_minor`, `updated_at` | projects rent, deposits, sale milestones, bookings, stay extensions, damage claims, maintenance jobs, professional fees |
| `payment.refund.updated.v1` (`payment`) | `refund_ref`, `intent_ref`, `status` (`PENDING`, `COMPLETED`, `FAILED`), `refunded_minor` | short-let refunds |
| `manager.mandate.responded.v1` (`property_manager`) | `assignment_id`, `manager_ref_id`, `decision` (`ACCEPT`/`DECLINE`) | mandate acceptance |
| `manager.activity.recorded.v1` (`property_manager`) | `assignment_id`, `manager_ref_id`, `activity_type`, `ref_type?`, `ref_id?`, `summary` | owner-visible activity |
| `artisan.quote.submitted.v1` (`artisan`) | `request_id`, `provider_ref` + the provider-quote body of `POST /internal/v1/owner/maintenance/{id}/provider-quotes` | quote on a platform request |
| `artisan.job.event_recorded.v1` (`artisan`) | `job_id`, `provider_ref` + the job-event body of `…/maintenance-jobs/{id}/provider-events` | attendance/progress/completion/invoice/payment ack/withdrawal |
| `professional.engagement.updated.v1` (`professional_services`) | `appointment_id`, `professional_ref` + the body of `…/professional-appointments/{id}/provider-updates` | quote/decline/start/signed report |
| `marketplace.listing.reported.v1` (`marketplace`) | `listing_id`, `report_type`, `report_ref?` | freshness check / review |

Consumed subjects must not fall inside the owner stream's subjects (one stream per subject); a test
enforces it.

## Non-negotiable boundaries

- This service is the only application service allowed to read/write `individual_owner_db`.
- It must never query another service's database.
- Immediate cross-service needs use authenticated `/internal/v1/*` APIs.
- Durable asynchronous propagation uses versioned events over NATS JetStream with an outbox where state and event publication must be atomic.
- Public/client routes and internal service routes remain separate.
- Service-to-service calls carry calling service identity, request ID, trace ID, and actor/domain context where applicable.
- Deployment packaging uses Docker; Docker is not required for local development.

## Tests

```bash
export CF_TEST_POSTGRES_URL=postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable
GOWORK=off go test -race ./...          # unit + PostgreSQL integration + NATS + contract parity
sqlc diff -f sql/sqlc.yaml               # generated code is current
go run ./cmd/apidocgen -check            # OpenAPI, Postman and event schemas are current
```

Fakes for payment, e-sign, calendar sync, financing and smart access exist only in
`tests/integration` and never count as certification of those integrations.


## Financial Core consumer (Sections 11–14)

Configure `PAYMENT_WALLET_SERVICE_URL` and the existing financial legal-entity/
currency settings with an approved service JWT trust configuration. Registration
uses the durable source-owned delivery queue; an active authenticated Owner can
lazily read `/api/v1/owner/financial-wallet`. This reads Core's explicit EARNINGS
purpose and ledger buckets and retains historical wallet references. Structural
activation is independent of financial verification; restricted wallets stay readable.

`POST /api/v1/owner/rent-obligations/{obligation_id}/financial-collection` requires
an already accepted locked Core quote, payer email, source-owned business reference
and matching recurring rent/fee total. It cannot add initial one-time charges or
deposits to rent. The signed downstream call precedes the Owner transaction and
uses a stable obligation key. GET reads the canonical Core intent and validates
its original terms. `finance.payment.collected.v1` is consumed exactly once into
a separate immutable-binding observation projection. Collected money does not
mark rent paid or make beneficiary earnings available. Original funding, allocation,
hold/release and payout controls remain Financial Core authority.

The legacy generic `payment.API` is not activated by `PAYMENT_SERVICE_URL`; other
product/refund/release adapters need their own accepted context. Unsupported
methods remain unavailable. This prevents inferred consent/fees and false success.

`go run ./cmd/wallet-backfill --dry-run=true --limit=100` inspects missing source
registration deliveries without provider calls. Use the returned `next_cursor`
with `--after` and explicitly select `--dry-run=false` to enqueue a page. The
normal signed delivery worker retries each record independently; no opening funds
or historical journal changes are created. See the payment Sections 11–14
architecture/operations guide and final closure for exact local evidence and
separate provider/legal/staging/production restrictions.
