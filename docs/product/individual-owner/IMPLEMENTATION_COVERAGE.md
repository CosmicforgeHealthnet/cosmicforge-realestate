# Individual Owner Backend — Implementation Coverage Register (Sections 5–12)

| | |
|---|---|
| Service | `services/individual-owner-service` · database `individual_owner_db` |
| Documented foundation baseline | F1–F5 certified at `54425cb5df86fbb239c4faab624d786f98fb343e` |
| Implementation commits | `31ef5fc` (Section 5 service), `606341e` (gateway prefix, Postman, e2e), `127fcba` (register), `ecf5ee4` (x/net security bump) |
| Specification | `INDIVIDUAL_OWNER_BACKEND_SPEC_v1.0.pdf` (this folder) |
| Last updated | 2026-10-09 |

## Specification

The authoritative *Individual Property Owner Platform — Full Backend Implementation Specification
v1.0* is stored in this folder (`INDIVIDUAL_OWNER_BACKEND_SPEC_v1.0.pdf`, text extraction alongside;
added 2026-10-09). Section 5 was implemented **before** the document was available, from the
implementation directive and `docs/architecture/*`; it has since been reconciled against the
specification below. Sections 6–12 items marked *spec* are no longer blocked on the document and
are scheduled by the specification's workstreams (§23, IO-2…IO-16).

### Section 5 reconciliation against spec v1.0

| Spec item | Implementation | Assessment |
|---|---|---|
| §5 principals, §5.1 capabilities, account states, §7 Account lifecycle | as recorded in the Section 5 table below | Conforms. Spec lists RESTRICTED/SUSPENDED → ACTIVE "only through authorized remediation/appeal" — implemented (resolution reference). |
| §6 table names `owner_credentials`, `owner_sessions`, `owner_devices`, `owner_mfa_methods`, `security_events` | `owner_accounts.password_hash`, `auth_sessions` + `auth_refresh_tokens` (shared `sessions` schema reference), `auth_devices`, `mfa_totp_credentials` + `mfa_recovery_codes`, `account_security_events`, plus `auth_challenges`, `account_status_history`, `notification_dispatches` | Logical-model mapping (§6 lists *logical* tables). Physical names follow the certified shared session schema; no rename planned. `owner_credentials` (OIDC credential metadata) lands with the OAuth adapter. |
| §8 auth paths `/auth/forgot-password`, `/auth/reset-password`, `/auth/mfa/enrol`, `/auth/mfa/verify`, `/sessions`, `/sessions/{id}`, `/profile`, `/account/closure` | `/auth/password/forgot`, `/auth/password/reset`, `/me/mfa/totp/setup`+`/confirm`, `/auth/mfa/verify`, `/me/sessions`, `/me/sessions/{id}`, `/me`, `/me/closure` | §8 permits path normalization; OpenAPI/Postman document the implemented paths. Open decision: add spec-named aliases or keep normalized paths (frontend has not integrated yet). |
| §13.2 `/account/status`, `/verification`; IDENTITY case creation | not implemented | IO-2/IO-4 (Verification Service integration). |
| §13.1 `/dashboard`, `/calendar` | not implemented | depend on business read models (IO-3+). |
| §7 Account closure lifecycle `REQUESTED → BLOCKERS_CHECK → CLOSURE_PENDING → CLOSED` | account status CLOSURE_PENDING with cooling-off; blockers checker interface with no business holds yet | PARTIAL (unchanged). |
| §19 config names `OWNER_AUTH_PRIVATE_KEY[_FILE]`, `POLICY_CONFIG_VERSION` | `OWNER_ACCESS_TOKEN_PRIVATE_KEY[_FILE]`, `OWNER_SECURITY_POLICY_VERSION` | Naming deviation; accept the spec names as aliases in IO-2. |
| §16 step-up for payout changes, sensitive document grants, property transfer | step-up mechanism and rule flag exist | Rules are registered when those commands exist (IO-2/IO-3). |
| §23 IO-1 Service bootstrapping & local auth | `31ef5fc` … `ecf5ee4` | **IO-1: PASS (LOCAL/CONTRACT)** except OAuth adapters (BLOCKED, Appendix D "DOMAIN IMPLEMENTATION": provider selection pending). |

## Status and certification levels

`PASS` — code exists, is functional, tests pass at the stated level. `PARTIAL` — some of the
requirement is implemented and tested; the rest is listed. `BLOCKED` — cannot be completed without a
named dependency. `NOT STARTED` — no code. `FAIL` — implemented but failing (none).

Certification levels: **LOCAL PASS** (unit/integration tests against real PostgreSQL 16, embedded
NATS JetStream, fakes only for external services), **CONTRACT PASS** (OpenAPI/JSON-Schema/Postman
parity and real-process e2e with real gateway + notification-service), **STAGING PASS** and
**PRODUCTION PASS** — **not claimed for anything** (no staging deployment was performed).

Test evidence paths are relative to `services/individual-owner-service/` unless noted.

## Section 5 — Authentication, owner roles and authorization

| ID | Requirement | Status | Level | Implementation | Tests | Blocker / outstanding |
|---|---|---|---|---|---|---|
| S5-A01 | Owner registration | PASS | CONTRACT | `usecases/accounts.go` Register; `POST /api/v1/owner/auth/register` | `auth_test.go` Registration…, ConcurrentRegistration; `tests/e2e/owner_test.go` | — |
| S5-A02 | Email/password login | PASS | CONTRACT | `usecases/login.go` Login | LoginInvalidCredentialsAndLockout; e2e | — |
| S5-A03 | Phone/OTP login | PASS | LOCAL | RequestLoginOTP / VerifyLoginOTP (no enumeration, background SMS) | `mfa_test.go` PhoneVerificationAndOTPLogin | Production SMS provider is notification-service's (Appendix D) |
| S5-A04 | Email verification | PASS | CONTRACT | StartEmailVerification / ConfirmContact | Registration…; CodeAttemptsAreBounded; e2e (real notification-service acceptance) | — |
| S5-A05 | Phone verification | PASS | LOCAL | StartPhoneVerification / ConfirmContact (verified-phone uniqueness) | PhoneVerificationAndOTPLogin | — |
| S5-A06 | Password reset & recovery | PASS | LOCAL | ForgotPassword / ResetPassword (all sessions revoked) | PasswordChangeForgotResetAndNoEnumeration | — |
| S5-A07 | Session issuance | PASS | CONTRACT | startSessionTx via shared `sessions.Manager` | all auth tests; e2e | — |
| S5-A08 | Refresh-token rotation | PASS | CONTRACT | Refresh (shared manager, CAS rotate in tx) | RefreshRotationReuseAndRevocation; e2e | — |
| S5-A09 | Logout | PASS | CONTRACT | Logout | LogoutRemoteRevocationAndIDOR; e2e | — |
| S5-A10 | Logout-all | PASS | LOCAL | LogoutAll | LogoutRemoteRevocationAndIDOR | — |
| S5-A11 | Device and session management | PASS | LOCAL | `selfservice.go` sessions/devices | Logout…IDOR; SuspiciousDevice… | — |
| S5-A12 | Suspicious device detection | PARTIAL | LOCAL | Rule: unknown/revoked/unidentified device once known devices exist → security event + alert | SuspiciousDeviceDetectionAndAlertDispatch | Richer signals (geo, IP reputation, impossible travel) need product policy (*spec*) |
| S5-A13 | Account activation & restriction handling | PASS | CONTRACT | `lifecycle.go`; activation rule versioned (`OWNER_ACTIVATION_REQUIRES`, default EMAIL) | AdminRestrictSuspendReinstate…; e2e | Default activation rule is an assumption pending *spec* |
| S5-A14 | Security event history | PASS | LOCAL | `account_security_events`; cursor API | SuspiciousDevice… (pagination) | — |
| S5-A15 | OAuth/OIDC adapter contracts | BLOCKED | LOCAL | `integrations/oauth` contract; routes answer 503 `OAUTH_PROVIDER_UNAVAILABLE`; config refuses `OWNER_OAUTH_PROVIDERS` | OAuthIsExplicitlyUnavailable; config_test | Provider choice, client registration, credentials (Appendix D) |
| S5-B01 | Argon2id hashing | PASS | LOCAL | shared `passwords` (rehash on login) | Login tests | — |
| S5-B02 | Ed25519 access JWT, 15 min | PASS | CONTRACT | shared `auth.Issuer`; JWKS `/.well-known/jwks.json` | AccessTokenValidation; JWKSInteroperatesWithGatewayTrustConfig; e2e | — |
| S5-B03 | Hashed refresh tokens | PASS | LOCAL | `auth_refresh_tokens.token_hash` (SHA-256) | Refresh tests | — |
| S5-B04 | Family rotation + reuse detection | PASS | CONTRACT | shared manager; reuse revocation committed | RefreshRotationReuse…; ConcurrentRefreshAllowsExactlyOneRotation; e2e | — |
| S5-B05 | Remote session revocation | PASS | CONTRACT | live session check per request | Logout…IDOR; admin suspend; e2e | — |
| S5-B06 | Trusted device records | PASS | LOCAL | `auth_devices` trust/revoke (trust never bypasses MFA) | SuspiciousDevice… | — |
| S5-B07 | TOTP MFA | PASS | LOCAL | `credentials` RFC 6238 (vectors), replay CAS, AES-GCM seeds | `credentials_test.go`; TOTPEnrolmentMFALogin… | — |
| S5-B08 | Recovery codes | PASS | LOCAL | hashed, single-use, regeneration | TOTPEnrolment… | — |
| S5-B09 | SMS/email second factor via Notification Service | PASS | CONTRACT | MFA_CODE / STEP_UP_CODE challenges; `integrations/notification` | TOTPEnrolment… (EMAIL); client_test; e2e (real contract) | Production email/SMS providers (Appendix D) |
| S5-B10 | Step-up for sensitive commands | PASS | LOCAL | `authz` StepUp rules; `POST /auth/step-up` | TOTPEnrolment…; Closure…; EmailChange… | — |
| S5-B11 | Audit for auth & recovery | PASS | LOCAL | `audit_events` in the same tx | Registration…; Admin… (privileged read) | — |
| S5-B12 | Abuse / rate limiting | PASS | CONTRACT | edge limits (gateway auth-sensitive 10/min/IP); lockout; code attempt & send limits | Lockout…; CodeAttemptsAreBounded; gateway tests | — |
| S5-B13 | No raw biometrics | PASS | — | nothing biometric is accepted or stored | schema review | Mobile local unlock is a client concern |
| S5-C01 | Reusable policy-enforcement component | PASS | LOCAL | `internal/authz` used by HTTP, internal routes and workers | `authz_test.go` | — |
| S5-C02 | Actor, role, step-up, account status, admin restriction checks | PASS | LOCAL | authz Account rules | authz_test; integration | — |
| S5-C03 | Property authority: ownership, co-owner consent, representative, manager delegation, verification status, property/transaction state | PARTIAL | LOCAL | engine semantics (roles, capability, validity window, revocation, consent obligation, verified authority, admin restriction) | `authz_test.go` TestPropertyAuthority | Persistence resolver needs ownership/delegation tables (*spec* §6 ctx 04, 21); fails closed today |
| S5-C04 | Principals OWNER, CO_OWNER, AUTHORIZED_REPRESENTATIVE, WITNESS/SIGNATORY, SERVICE_PRINCIPAL, GATEWAY_DELEGATED_ACTOR | PARTIAL | LOCAL | modelled in authz; owner/service/gateway enforced end to end | authz_test | Co-owner/representative/signatory records need *spec* §6 |
| S5-D01 | Account lifecycle with authorized remediation | PASS | CONTRACT | `domain/account.go` FSM; resolution reference required | `account_test.go`; Admin…; Closure… | — |
| S5-E01 | Required Section 5 tests | PASS | LOCAL | — | correct/invalid login, expired/malformed/wrong-issuer/audience, cross-domain, reuse, concurrent refresh, revoked sessions, MFA failure/recovery, step-up, escalation, co-owner, expired representative, service-principal restriction, gateway actor forgery | — |

## Section 6 — Database and data model

| ID | Bounded context | Status | Notes |
|---|---|---|---|
| S6-01 | Account and security | PASS (LOCAL) | `migrations/00002_account_security.sql`: `owner_accounts`, `account_status_history`, `auth_devices`, `auth_sessions`, `auth_refresh_tokens`, `auth_challenges`, `mfa_totp_credentials`, `mfa_recovery_codes`, `account_security_events`, `notification_dispatches`; CHECK/unique/partial indexes; round-trip tested (`TestMigrationsRoundTrip`); sqlc drift clean |
| S6-02 | Owner profile and contacts | PARTIAL | display name, locale, verified email/phone only; full profile catalogue is *spec* |
| S6-03 … S6-28 | Banking/payouts/tax, ownership & authority, properties/units/parcels/assets, verification linkage, listings, pricing, availability, leads, viewings, applications, agreements, tenancy, deposits, utilities, commercial, land, sale, short-let, manager delegation, maintenance, professional services, disputes & privacy, reports, financing, insurance, smart access | NOT STARTED | blocked on *spec* (exact table catalogue) |
| S6-29 | Idempotency, outbox, processed events | PASS (LOCAL) | `00001_platform.sql` (shared canonical definitions); `processed_events` awaits the first consumer |
| S6-30 | Audit history | PASS (LOCAL) | append-only `audit_events` (trigger), lifecycle history, security events |

## Section 7 — Canonical state machines

| ID | Lifecycle | Status | Notes |
|---|---|---|---|
| S7-01 | Account | PASS (LOCAL) | table-driven transitions + actors; CAS on version; concurrent/stale commands rejected (VERSION_CONFLICT / INVALID_STATE_TRANSITION) |
| S7-21 | Account closure | PARTIAL | request (step-up) / cancel / finalize after cooling-off (SKIP LOCKED worker); `ClosureHoldChecker` has no business holds yet; retention/anonymization not implemented (S11-17) |
| S7-02 … S7-20, S7-22 | Ownership record … Smart-access grant | NOT STARTED | canonical states are *spec*. Directive acceptance criteria recorded for later: ACTIVE property ≠ PUBLISHED listing; publication needs verified authority, valid media, disclosed pricing, current availability; accepted offers freeze pricing; signed agreement versions immutable; tenancy activation needs agreement + payment evidence; rent/deposit truth from Payment Service; expired manager assignment loses authority immediately (engine already enforces); revoked access grants never authorize; confirmed short-let bookings never overwritten by external calendars; sale completion needs closing evidence |

## Section 8 — Public REST API (`/api/v1/owner/*`)

| ID | Family | Status | Notes |
|---|---|---|---|
| S8-A | Authentication and account (37 routes incl. MFA, sessions, devices, closure) | PASS (CONTRACT) | OpenAPI `contracts/public/openapi/individual-owner.yaml`; Postman `02 Individual Owner/Frontend`; route parity tests (`TestOpenAPIContractsMatchHandlers`, `TestPostmanMatchesHandlers`). Owner profile fields beyond display name/locale are *spec* |
| S8-B … S8-N | Banking/tax … Financing/insurance/smart access | NOT STARTED | endpoint inventory is *spec*; financial routes additionally depend on Payment Service (not implemented) |

## Section 9 — Internal service API contracts

| ID | Contract | Status | Notes |
|---|---|---|---|
| S9-01 | Service authentication, capability policy, negative authorization | PASS (LOCAL) | shared `serviceauth`; admin capability requires admin-service **and** delegated admin actor; tests for wrong service, gateway token, missing/foreign actor |
| S9-02 | Admin account operations (`/internal/v1/owner/accounts/*`) | PASS (CONTRACT) | idempotent, If-Match, audited privileged read; OpenAPI `contracts/internal/openapi/individual-owner-service.yaml` |
| S9-03 | JWKS for gateway trust | PASS (CONTRACT) | e2e: gateway trusts owner tokens only via `jwks_url` |
| S9-04 | `GET /internal/v1/owner/listings/{id}` | NOT STARTED | listing model is *spec* |
| S9-05 | `GET /internal/v1/owner/properties/{id}/public-summary` | NOT STARTED | property model is *spec* |
| S9-06 | Notification Service client | PASS (CONTRACT) | timeouts, `Idempotency-Key` header on every send, definite/retryable/unknown classification |
| S9-07 | Verification, Media, Payment, Marketplace, Property Manager, Artisan, Professional, Admin (outbound), CF Meet clients | NOT STARTED | consumers of these contracts are *spec* business flows; Payment Service and CF Meet are not implemented platform-side |

## Section 10 — Domain events

| ID | Event / consumer | Status | Notes |
|---|---|---|---|
| S10-01 | `owner.account.activated.v1` | PASS (CONTRACT) | schema `contracts/events/owners/`; outbox in-tx; JetStream relay; broker outage recovery; republish de-duplicated; e2e |
| S10-02 | `owner.account.restricted.v1` | PASS (CONTRACT) | as above (RESTRICTED and SUSPENDED) |
| S10-03 … | property.*, listing.*, viewing, application, rental_offer, tenancy, manager.assignment.*, maintenance, sale, shortlet.*, access.grant.revoked | NOT STARTED | payloads/aggregates are *spec* |
| S10-C | Consumers: verification.*, media.*, payment.*, payout.*, refund.*, notification.*, manager.*, provider job/report, marketplace reports | NOT STARTED | `processed_events` table and shared de-duplication are ready; no owner aggregate consumes them yet (*spec*) |

## Section 11 — Background workers

| ID | Worker | Status | Notes |
|---|---|---|---|
| S11-01 | Outbox relay | PASS (CONTRACT) | shared lease/SKIP LOCKED relay; recovery tests; e2e |
| S11-x1 | Security-alert dispatch (Section 5 support) | PASS (LOCAL) | lease claim, idempotent sends, backoff + jitter, dead-letter on definite failure/after 8 attempts, destinations purged |
| S11-x2 | Account-closure finalization | PARTIAL | see S7-21 |
| S11-x3 | Housekeeping (challenges, ended sessions, idempotency keys, published outbox) | PASS (LOCAL) | |
| S11-02 … S11-16, S11-18, S11-19 | Availability freshness … smart-access expiry | NOT STARTED | depend on *spec* aggregates |
| S11-17 | Privacy retention and anonymization | NOT STARTED | retention/legal-hold policy is *spec* / legal policy pack |

## Section 12 — Cross-service and external integrations

| ID | Integration | Status | Notes |
|---|---|---|---|
| S12-A | Verification Service (IDENTITY, PROPERTY_AUTHORITY) | NOT STARTED | *spec* flows; production KYC provider unresolved (Appendix D) |
| S12-B | Media & Document Service | NOT STARTED | *spec* |
| S12-C | Notification Service | PARTIAL (CONTRACT) | verification codes + security alerts on seeded templates; no direct SMTP/SMS; WhatsApp not used; delivery/read receipts not claimed. Business reminders are *spec* |
| S12-D | Payment Service | BLOCKED | Payment Service not implemented; no owner financial flows built; no ledger in owner service |
| S12-E … S12-O | Marketplace, Property Manager, Artisan, Professional, Admin (beyond account ops), CF Meet, Maps, e-signature, external listings/calendar, smart access, financing/insurance | NOT STARTED / BLOCKED | *spec*; vendor/agreement-dependent items blocked per Appendix D |

## Appendix D gaps (as far as visible without the document)

| Gap | Resolved in repo? | Handling | Blocks |
|---|---|---|---|
| Cross-domain single identity / multi-role | No (ADR-0006: domain-local by design) | not implemented; no email-based linking | optional feature |
| Production KYC / liveness | No | not used by Section 5 | production property authority |
| Notification delivery/read acknowledgements | No | never claimed; acceptance only | optional |
| WhatsApp | No | not implemented | optional |
| Production email/SMS providers | No (notification-service capture providers in non-prod) | contract implemented | production |
| OAuth providers | No | contract + explicit 503 + config guard | optional sign-in |
| Payment Service | No | not implemented | financial Sections 8/11/12 |
| E-signatures, smart locks, external sync, financing/insurance, AI advisory, legal policy packs | No | not started | their features |

## Quality gates run for this register (2026-10-09, local)

| Gate | Command | Result |
|---|---|---|
| gofmt / vet / staticcheck | `gofmt -l .`, `go vet ./...`, `staticcheck ./...` (owner module) | clean |
| go mod tidy | `go mod tidy` + diff | clean |
| sqlc drift | `sqlc diff -f sql/sqlc.yaml` | clean |
| Architecture boundaries | `scripts/ci/check-architecture.sh [--self-test]` | OK |
| Owner unit + PostgreSQL + NATS + contract tests | `CF_REQUIRE_INTEGRATION=1 go test -race -count=1 ./...` | all pass; 78% statement coverage of `internal/...` |
| Gateway regression (real Redis) | `go test -race ./...` in `gateway/api-gateway` | pass |
| E2E (foundation + owner journeys) | `go test -race ./...` in `tests/e2e` | pass |
| OpenAPI / JSON Schema validity | `openapi-spec-validator`, `check-jsonschema --check-metaschema` | OK |
| Docker image | build + non-root + binaries + process smoke (migrate up, /ready, /version, JWKS) | OK |
| gitleaks 8.28.0 | `gitleaks git` and `gitleaks dir` with `--exit-code 1` | no leaks (two reviewed history false positives in `.gitleaksignore`) |
| Trivy 0.57.0 config | `trivy config --severity HIGH,CRITICAL` on the service | no findings (embedded checks; policy download blocked by sandbox proxy) |
| govulncheck | CI `security.yml` | failed on 2026-10-09 for **all** modules on newly published `golang.org/x/net@v0.59.0` advisories (GO-2026-6603, GO-2026-6611, …); fixed by bumping every module to v0.60.0; full regression (all modules, `-race`, real PostgreSQL/Redis/MinIO, e2e) re-run green locally; CI Security/Backend/Images/Contracts/Structure all green at `ecf5ee4` |
| Trivy image scan | CI `images.yml` | passed for `individual-owner-service` at `127fcba` |

Deployment wiring (staging/production compose entries, `promote.yml` digest input) is intentionally
not added until an owner image digest exists and deployment is approved.
