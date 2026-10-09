# Individual Owner Backend — Implementation Coverage Register (Sections 5–13)

| | |
|---|---|
| Service | `services/individual-owner-service` · database `individual_owner_db` |
| Documented foundation baseline | F1–F5 certified at `54425cb5df86fbb239c4faab624d786f98fb343e` |
| Implementation commits | Section 5: `31ef5fc`, `606341e`, `127fcba`, `ecf5ee4` · Sections 6–13: `2592eff` (ownership/property/banking), `41202cc` (delegation/listings/pricing/availability), `5e61cbc` (leads/viewings/applications/offers), `5c7aaec` (agreements/tenancy/obligations/utilities), `f802392` (commercial/land/sale), `6e986f2` (short-let), `4f55d6a` (maintenance/professionals), `3848d8c` (disputes/privacy/reports/insights/dashboard), `d297990` (financing/insurance/smart access), `659d43e` (consumers, templates, domain E2E) · Final corrections: `9dfe6a7` (spec path aliases), `27511c9` (tenant/guest recipient contract), `42ff86b` (sale-linked ownership transfer), `d19746d` (jurisdiction legal policy), `c5bb657` (event compatibility), `e0a05ec` (migration preflight), `deb5c93` (capture-file hardening) |
| Specification | `CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.pdf` (this folder) |
| Last updated | 2026-10-09 |

## Specification

The authoritative *Individual Property Owner Platform — Full Backend Implementation Specification
v1.0* is stored in this folder (`CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.pdf`, text extraction alongside;
added 2026-10-09). Section 5 was implemented **before** the document was available, from the
implementation directive and `docs/architecture/*`; it has since been reconciled against the
specification below. Sections 6–12 items marked *spec* are no longer blocked on the document and
are scheduled by the specification's workstreams (§23, IO-2…IO-16).

### Section 5 reconciliation against spec v1.0

| Spec item | Implementation | Assessment |
|---|---|---|
| §5 principals, §5.1 capabilities, account states, §7 Account lifecycle | as recorded in the Section 5 table below | Conforms. Spec lists RESTRICTED/SUSPENDED → ACTIVE "only through authorized remediation/appeal" — implemented (resolution reference). |
| §6 table names `owner_credentials`, `owner_sessions`, `owner_devices`, `owner_mfa_methods`, `security_events` | `owner_accounts.password_hash`, `auth_sessions` + `auth_refresh_tokens` (shared `sessions` schema reference), `auth_devices`, `mfa_totp_credentials` + `mfa_recovery_codes`, `account_security_events`, plus `auth_challenges`, `account_status_history`, `notification_dispatches` | Logical-model mapping (§6 lists *logical* tables). Physical names follow the certified shared session schema; no rename planned. `owner_credentials` (OIDC credential metadata) lands with the OAuth adapter. |
| §8 auth paths `/auth/forgot-password`, `/auth/reset-password`, `/auth/mfa/enrol`, `/auth/mfa/verify`, `/sessions`, `/sessions/{id}`, `/profile`, `/account/closure` | normalized paths kept (`/auth/password/forgot`, `/me/sessions`, …) **and** spec-named aliases on the same handlers (`9dfe6a7`) | PASS (LOCAL). Every Section 8 operation of the stored spec text is routed (`TestSpecificationSection8PathsAreRouted`). |
| §13.2 `/account/status`, `/verification`; IDENTITY case creation | implemented (`2592eff`) | PASS (LOCAL). |
| §13.1 `/dashboard`, `/calendar` | implemented (`3848d8c`) | PASS (dashboard CONTRACT via E2E). |
| §7 Account closure lifecycle `REQUESTED → BLOCKERS_CHECK → CLOSURE_PENDING → CLOSED` | CLOSURE_PENDING with cooling-off; closure-holds registry (open tenancies and sales, unsettled deposits, upcoming bookings) blocks finalization | PASS (LOCAL). |
| §19 config names `OWNER_AUTH_PRIVATE_KEY[_FILE]`, `POLICY_CONFIG_VERSION` | `OWNER_ACCESS_TOKEN_PRIVATE_KEY[_FILE]`, `OWNER_SECURITY_POLICY_VERSION` | Naming deviation; accept the spec names as aliases in IO-2. |
| §16 step-up for payout changes, sensitive document grants, property transfer | step-up rules on payout/tax writes, transfers and privacy verification | PASS (LOCAL). |
| §23 IO-1 Service bootstrapping & local auth | `31ef5fc` … `ecf5ee4` | **IO-1: PASS (LOCAL/CONTRACT)** except OAuth adapters (BLOCKED, Appendix D "DOMAIN IMPLEMENTATION": provider selection pending). |

## Final corrections (2026-10-09)

| # | Correction | Status | Level | Evidence | Commit |
|---|---|---|---|---|---|
| 1 | Public API compatibility: Section 8 / §13 paths vs 313 operations; 13 aliases (Section 5, property-scoped short-let, damage claim) on the same handlers; OpenAPI (`x-alias-of`), Postman, parity | PASS | LOCAL | `specpaths_test.go` (`TestSpecificationSection8PathsAreRouted`, `TestSpecificationPathAliases`); contract/Postman parity tests | `9dfe6a7` |
| 2 | Tenant/guest notification recipient contract via the recipient's domain and notification-service (identity, consent, purpose, preferred channel, sender authority, rate limit, relationship checks) | PASS (contract) · BLOCKED (live) | LOCAL | `announcements_test.go`; `CUSTOMER_NOTIFY_SERVICE_URL` refused until marketplace-service exists | `27511c9` |
| 3 | Ownership transfer after sale: SALE change case (authorization, step-up, identity gate, co-owner consent, sale COMPLETED + milestones, buyer acceptance, evidence, platform review); sale completion never replaces the owner; retries idempotent, conflicts 409 | PASS | LOCAL | `sale_transfer_test.go` | `42ff86b` |
| 4 | Legal-policy defaults: inventory, silence acceptance of damage claims removed, versioned jurisdiction policy, consequential automation disabled without approval, manual review paths | PASS (engineering) · approval pending | LOCAL | `legal_policy_test.go` (4 tests), `TestRentArrearsLateFeesAndWaivers`; [`LEGAL_POLICY.md`](LEGAL_POLICY.md) | `d19746d` |
| 5 | Consumed-event compatibility: matrix, producer identity, version allowlists, `schema_version`, duplicates, out-of-order (media terminal states, payment watermark) | PASS (verification, media) · BLOCKED (5 families) | CONTRACT (real-process for verification/media) / LOCAL | `event_compat_test.go` (mutation-checked), `tests/e2e/owner_events_test.go`; [`EVENT_COMPATIBILITY.md`](EVENT_COMPATIBILITY.md) | `c5bb657` |
| 6 | `btree_gist` prerequisite (migration 00009, not 00012): non-destructive preflight, documented administrator step, clear refusal | PASS | LOCAL (real PostgreSQL 16, restricted role) | `shared/go/migrate/preflight_test.go`, `migrate_preflight_test.go`; [`DATABASE_PREREQUISITES.md`](DATABASE_PREREQUISITES.md) | `e0a05ec` |
| 7 | `NOTIFY_DEV_CAPTURE_FILE`: dev/test only (staging and production refuse), absolute path, requires a dev provider, no symlinks, regular file owned by the process user, 0600, validated at startup, bodies never logged | PASS | LOCAL | notification-service `channels_test.go`, `config_test.go` | `deb5c93` |

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
| S5-C03 | Property authority: ownership, co-owner consent, representative, manager delegation, verification status, property/transaction state | PASS | LOCAL | engine semantics + DB authority resolver over ownership records/parties, mandates and manager assignments (`property_access` view) | `authz_test.go` TestPropertyAuthority; property, delegation and maintenance integration tests (manager limits, revocation) | — |
| S5-C04 | Principals OWNER, CO_OWNER, AUTHORIZED_REPRESENTATIVE, WITNESS/SIGNATORY, SERVICE_PRINCIPAL, GATEWAY_DELEGATED_ACTOR | PASS | LOCAL | modelled in authz and backed by ownership/delegation records; provider actors (artisan, professional) required for provider callbacks | authz_test; property2/listing/maintenance integration tests | — |
| S5-D01 | Account lifecycle with authorized remediation | PASS | CONTRACT | `domain/account.go` FSM; resolution reference required | `account_test.go`; Admin…; Closure… | — |
| S5-E01 | Required Section 5 tests | PASS | LOCAL | — | correct/invalid login, expired/malformed/wrong-issuer/audience, cross-domain, reuse, concurrent refresh, revoked sessions, MFA failure/recovery, step-up, escalation, co-owner, expired representative, service-principal restriction, gateway actor forgery | — |

## Section 6 — Database and data model

16 goose migrations (`migrations/00001`–`00016`), 131 tables, all in `individual_owner_db`; no
cross-service foreign keys (other services' records are typed UUID references). Every migration is
round-trip tested (`TestMigrationsRoundTrip`) and `sqlc diff` is clean. Append-only tables are
enforced by `forbid_mutation()` triggers; concurrency invariants by partial unique indexes and (short-let)
a `btree_gist` exclusion constraint. No raw KYC documents, card/account numbers, chat messages,
credential secrets or provider keys are stored (media/verification/payment/CF Meet references only).

| ID | Context (spec §6) | Migration | Status | Level |
|---|---|---|---|---|
| S6-01 | Account and security | 00002 | PASS | LOCAL |
| S6-02 | Profile and contacts | 00003 | PASS | LOCAL |
| S6-03 | Banking, payout and tax (references and masked metadata only) | 00003 | PASS | LOCAL |
| S6-04 | Ownership records, parties, consents, authority documents, change cases | 00003 | PASS | LOCAL |
| S6-05 | Properties, addresses, units, parcels, features, media links, assets, history, imports | 00003 | PASS | LOCAL |
| S6-06 | Verification links (projection of verification-service) | 00003 | PASS | LOCAL |
| S6-07 | Dashboard preferences | 00003 | PASS | LOCAL |
| S6-08 | Manager proposals, assignments, versioned permissions, activity | 00004 | PASS | LOCAL |
| S6-09 | Listings, versions, media, publication history | 00005 | PASS | LOCAL |
| S6-10 | Pricing (immutable versions, fees, rules), availability state, calendar, freshness | 00005 | PASS | LOCAL |
| S6-11 | Inquiries, lead activities, viewing slots/requests/attendance | 00006 | PASS | LOCAL |
| S6-12 | Screening policies, applications, participants, evidence grants, screening results, decisions, rental offers | 00006 | PASS | LOCAL |
| S6-13 | Agreement/notice templates, agreements, versions, participants, signature requests | 00007 | PASS | LOCAL |
| S6-14 | Tenancies, rent obligations (+history), deposits, checklists, conditions, key inventory, renewals, notices, move-out | 00007 | PASS | LOCAL |
| S6-15 | Utilities, allocation rules, meter readings, bills, service-charge budgets/items | 00007 | PASS | LOCAL |
| S6-16 | Commercial terms, land terms, LOIs and rounds | 00008 | PASS | LOCAL |
| S6-17 | Sale transactions, offers, data rooms (documents, grants, access log), due diligence, closing milestones | 00008 | PASS | LOCAL |
| S6-18 | Short-let settings, rates, bookings, guests, turnover jobs, damage claims | 00009 | PASS | LOCAL |
| S6-19 | Maintenance requests, quotes, approvals, jobs, job events, warranties, preventive schedules | 00010 | PASS | LOCAL |
| S6-20 | Professional appointments and findings | 00010 | PASS | LOCAL |
| S6-21 | Domain disputes and evidence, consents, data-subject requests | 00011 | PASS | LOCAL |
| S6-22 | Report snapshots, market insights | 00011 | PASS | LOCAL |
| S6-23 | Finance offers, insurance policies and claims | 00012 | PASS | LOCAL |
| S6-24 | Access devices, grants, access events | 00012 | PASS | LOCAL |
| S6-25 | Idempotency, outbox, processed events, audit, lifecycle transitions | 00001/00002 | PASS | LOCAL |
| S6-26 | Owner announcements and customer (tenant/guest) notification requests | 00013 | PASS | LOCAL |
| S6-27 | SALE ownership-change case linked to the completed sale (one live/completed case per sale) | 00014 | PASS | LOCAL |
| S6-28 | Versioned jurisdiction legal policies (immutable once approved); `RESPONSE_OVERDUE` damage claims; `NO_RESPONSE` deposit responses | 00015 | PASS | LOCAL |
| S6-29 | Payment-intent ordering watermarks | 00016 | PASS | LOCAL |

Deployment prerequisite: migration 00009 (and no other) needs `btree_gist`. `migrate up` runs a
non-destructive preflight and refuses (exit 1, nothing applied) when the role cannot create it;
`migrate preflight` runs only the check. Administrator step and evidence:
[`DATABASE_PREREQUISITES.md`](DATABASE_PREREQUISITES.md) (`e0a05ec`; PASS LOCAL on PostgreSQL 16 with a
restricted role; staging role not verified).

## Section 7 — Canonical state machines

All 24 machines are table-driven in `internal/domain/machines.go` and applied by the shared lifecycle
engine (`internal/lifecycle`): compare-and-set on `version`, invalid edges → `INVALID_STATE_TRANSITION`,
stale versions → `VERSION_CONFLICT`, a `lifecycle_transitions` history row per change, audit and outbox in
the same transaction.

| ID | Machine | Status | Level | Notes |
|---|---|---|---|---|
| S7-01 | Account | PASS | CONTRACT | e2e |
| S7-02 | Ownership record, ownership change case | PASS | LOCAL | SALE case type (`42ff86b`): sale completion never changes the owner |
| S7-03 | Property | PASS | LOCAL | ACTIVE property ≠ PUBLISHED listing |
| S7-04 | Listing, availability | PASS | LOCAL | publication gates: verified authority, media, pricing, availability |
| S7-05 | Inquiry, viewing | PASS | LOCAL | |
| S7-06 | Application, rental offer | PASS | LOCAL | accepted offers freeze pricing |
| S7-07 | Agreement | PASS | LOCAL | signed versions immutable |
| S7-08 | Tenancy, rent obligation, deposit, notice, renewal case, move-out | PASS | LOCAL | money states only from payment projections |
| S7-09 | Sale, commercial LOI | PASS | LOCAL | completion needs required closing milestones |
| S7-10 | Short-let booking | PASS | LOCAL | external calendars never overwrite bookings |
| S7-11 | Manager assignment | PASS | LOCAL | expiry/revocation removes authority and access grants immediately |
| S7-12 | Maintenance request, professional appointment | PASS | LOCAL | |
| S7-13 | Data-subject request | PASS | LOCAL | |
| S7-14 | Smart-access grant | PASS | LOCAL | revoked/expired grants never authorize |
| S7-21 | Account closure | PASS | LOCAL | closure holds registry (open tenancies and sales, unsettled deposits, upcoming bookings) blocks finalization; records retained |

## Section 8 — Public REST API (`/api/v1/owner/*`)

331 public operations: 43 account/auth (Section 5, incl. 6 spec-path aliases) + 288 domain (incl. 7
spec-path aliases). Every operation is in OpenAPI
(`contracts/public/openapi/individual-owner.yaml`, `individual-owner-domain.yaml`, generated by
`cmd/apidocgen`, checked with `-check`) and Postman (`postman/CosmicForge_RealEstate.postman_collection.json`);
route/contract parity is tested. Commands use `Idempotency-Key`; versioned updates use `If-Match`;
errors use the platform envelope. CONTRACT level is claimed only where a real-process E2E exists.

| ID | Family (OpenAPI tag) | Ops | Status | Level |
|---|---|---|---|---|
| S8-A | Authentication & account (Section 5) | 43 | PASS | CONTRACT (aliases LOCAL) |
| S8-B | Account & profile, identity verification | 16 | PASS | LOCAL |
| S8-C | Banking & tax | 10 | PASS (payout activation needs payment-service) | LOCAL |
| S8-D | Ownership & verification | 14 | PASS | LOCAL |
| S8-E | Properties (units, parcels, assets, media, imports, history) | 20 | PASS | CONTRACT (create via gateway in E2E) |
| S8-F | Listings, pricing, availability | 18 | PASS | LOCAL |
| S8-G | Leads, viewings | 20 | PASS | LOCAL |
| S8-H | Applications & offers | 14 | PASS | LOCAL |
| S8-I | Agreements (e-sign 503 without provider; OFFLINE signing works) | 14 | PASS | LOCAL |
| S8-J | Tenancy, rent, deposits, renewals, notices, move-out | 29 | PASS (PLATFORM money 503 without payment-service) | LOCAL |
| S8-K | Utilities & service charges | 15 | PASS | LOCAL |
| S8-L | Commercial & land, sales | 23 | PASS | LOCAL |
| S8-M | Short-let | 15 | PASS | LOCAL |
| S8-N | Delegation | 12 | PASS | LOCAL |
| S8-O | Maintenance, professionals | 20 | PASS | CONTRACT (maintenance via gateway in E2E) |
| S8-P | Reports & insights, dashboard, calendar, audit activity | 9 | PASS | CONTRACT (dashboard via gateway in E2E) |
| S8-Q | Disputes & privacy | 10 | PASS | LOCAL |
| S8-R | Financing & insurance | 10 | PARTIAL — financing/insurance options need partners (503); owner-held insurance PASS | LOCAL |
| S8-S | Smart access | 7 | PARTIAL — smart devices need a provider (503); keys/cards/lockboxes PASS | LOCAL |

Spec-path compatibility (`9dfe6a7`): the normalized paths remain for existing clients and the
specification paths are added as aliases on the same handlers — Section 5 (`/auth/forgot-password`,
`/auth/reset-password`, `/auth/mfa/enrol`, `/sessions`, `/sessions/{id}`, `/account/closure`),
property-scoped short-let `/properties/{id}/shortlet/settings|rates|calendar` (GET/PUT; the property's
single active SHORT_LET listing, or `?listing_id=`; 409 `AMBIGUOUS_SHORTLET_LISTING`) and
`/shortlet/bookings/{id}/damage-claim`. Aliases carry `x-alias-of` in OpenAPI and are in Postman;
`TestSpecificationSection8PathsAreRouted` parses Section 8 of the stored spec and fails on any unrouted
operation. New in the corrections: `/properties/{id}/announcements` (GET/POST),
`/transactions/sales/{id}/ownership-transfer`, `/properties/{id}/legal-policy`,
`/rent-obligations/{id}/late-fee`.

## Section 9 — Internal service API contracts (`/internal/v1/owner/*`)

83 internal operations (6 account + 53 internal + 24 admin), OpenAPI `contracts/internal/openapi/individual-owner-service*.yaml`.

| ID | Contract | Status | Level |
|---|---|---|---|
| S9-01 | Service auth, per-action service/actor grants, negative authorization | PASS | LOCAL |
| S9-02 | Admin: account ops, moderation, ownership review, legal templates, jurisdiction legal policies (draft / second-operator approval / retire), deposit/damage/maintenance dispute resolution, owner disputes, privacy decisions | PASS | LOCAL (account ops CONTRACT) |
| S9-03 | Marketplace: listing/public-summary read-back, cost preview, inquiries, viewings, applications, offers, sale offers, bookings, damage responses, tenant actions, data-room grantee access, listing reports | PASS | LOCAL |
| S9-04 | Property manager: mandate response, activity, managed property/listing/maintenance/access routes | PASS | LOCAL |
| S9-05 | Artisan / professional provider callbacks (delegated provider actor must own the record) | PASS | LOCAL |
| S9-06 | Notification client | PASS | CONTRACT |
| S9-07 | Verification client; media client (get, download URL, access policy, generated-document upload) | PASS | CONTRACT — real-process E2E: IDENTITY case create/evidence/submit/process against the real verification-service, evidence metadata and deletion against the real media-document-service (`c5bb657`); download URL / access policy / generated upload LOCAL |
| S9-08 | Payment client contract (intents, cancel, refunds, deposit releases, beneficiaries) | BLOCKED | contract + fakes only; `PAYMENT_SERVICE_URL` refused — payment-service has no implemented API |
| S9-09 | Customer-notification recipient contract (`integrations/customernotify`): owner domain sends recipient reference, relationship, purpose, sender authority; the recipient's domain applies identity, consent and preferred channel and sends through notification-service | PASS (LOCAL) · live BLOCKED | marketplace-service (recipient domain) not implemented; `CUSTOMER_NOTIFY_SERVICE_URL` refused; requests stay `BLOCKED`/retried, never reported delivered (`27511c9`) |

## Section 10 — Domain events

26 produced event types with JSON Schemas in `contracts/events/owners/` (generated, validated in tests
against real outbox envelopes), relayed by the outbox worker to JetStream (`REALESTATE_OWNER`).

| ID | Events | Status | Level |
|---|---|---|---|
| S10-01 | `owner.account.activated/restricted.v1` | PASS | CONTRACT |
| S10-02 | `property.created/updated/verification_changed.v1` | PASS | LOCAL |
| S10-03 | `listing.published/updated/paused/withdrawn/availability_confirmed.v1` | PASS | LOCAL |
| S10-04 | `viewing.updated.v1`, `application.updated.v1`, `rental_offer.updated.v1` | PASS | LOCAL |
| S10-05 | `tenancy.started/ended.v1` | PASS | LOCAL |
| S10-06 | `manager.assignment.created/updated/revoked.v1` | PASS | LOCAL |
| S10-07 | `maintenance.request.created.v1` (+ `updated.v1`, engineering addition) | PASS | CONTRACT (relayed to JetStream in E2E) |
| S10-08 | `sale.transaction.updated.v1` | PASS | LOCAL |
| S10-09 | `shortlet.availability.updated.v1`, `shortlet.booking.updated.v1` | PASS | LOCAL |
| S10-10 | `access.grant.revoked.v1` | PASS | LOCAL |
| S10-11 | Engineering additions: `professional.appointment.updated.v1`, `dispute.case.updated.v1` | PASS | LOCAL |

Consumers (§10.1): exactly-once via `processed_events` in the handler transaction; producer identity,
v1-only type allowlists and payload `schema_version` enforced (violations terminated, never applied);
out-of-order safe (verification rank, terminal media states, per-intent payment `updated_at` watermark).
Versioned producer/consumer matrix: [`EVENT_COMPATIBILITY.md`](EVENT_COMPATIBILITY.md) (`c5bb657`).

| ID | Consumer | Status | Level |
|---|---|---|---|
| S10-C1 | `verification.*` | PASS | CONTRACT — fixtures validated against verification-service schemas; real-process E2E consumes `verification.verified.v1` from the real service |
| S10-C2 | `media.ready/rejected/deleted.v1` | PASS | CONTRACT — fixtures validated against media schemas; real-process E2E consumes `media.deleted.v1` from the real service |
| S10-C3 | `payment.intent.updated.v1`, `payment.refund.updated.v1` | PASS (LOCAL) · live BLOCKED | consumer contract only; payment-service does not publish (no schema) |
| S10-C4 | `manager.mandate.responded.v1`, `manager.activity.recorded.v1` | PASS (LOCAL) · live BLOCKED | producer not implemented (no schema) |
| S10-C5 | `artisan.quote.submitted.v1`, `artisan.job.event_recorded.v1` | PASS (LOCAL) · live BLOCKED | producer not implemented (no schema) |
| S10-C6 | `professional.engagement.updated.v1` | PASS (LOCAL) · live BLOCKED | producer not implemented (no schema) |
| S10-C7 | `marketplace.listing.reported.v1` | PASS (LOCAL) · live BLOCKED | producer not implemented (no schema) |
| S10-C8 | `notification.*` delivery history | NOT STARTED | optional in spec; notification-service publishes no delivery events |
| S10-C9 | `payout.*` | NOT STARTED | payout outcomes read back through the payment client; no event contract |

Consumed payload shapes are documented in the service README ("Consumed events"); producers must
adopt them (or the consumers be adapted) when those services are built. Fake-adapter tests for
families 3–7 are not live-integration evidence.

## Section 11 — Background workers

| ID | Worker (spec) | Job | Status | Level |
|---|---|---|---|---|
| S11-01 | Outbox relay | relay | PASS | CONTRACT |
| S11-02 | Availability freshness | `listing-freshness` | PASS | LOCAL |
| S11-03 | Verification expiry | `verification-reconciliation`, `ownership-expiry` | PASS | LOCAL |
| S11-04 | Payout-change cooling-off | `payout-cooling-off` | PASS | LOCAL |
| S11-05 | Listing expiry/quality | `listing-freshness` + media consumer (unusable media pauses listings) | PASS | LOCAL |
| S11-06 | Viewing reminders | `viewing-reminders` | PASS | LOCAL |
| S11-07 | Application deadlines | `application-deadlines` | PASS | LOCAL |
| S11-08 | Agreement/signature reconciliation | `signature-reconciliation` | BLOCKED | runs only with an e-sign adapter (none) |
| S11-09 | Rent schedule projection | `rent-schedule` | PASS (payment intents BLOCKED on payment-service) | LOCAL |
| S11-10 | Arrears/reminder | `rent-obligations-arrears` | PASS | LOCAL |
| S11-11 | Renewal/notices | `renewals-and-notices` | PASS | LOCAL |
| S11-12 | Move-out/deposit deadlines | `deposits` | PASS | LOCAL — tenant silence accepts deductions only under an approved jurisdiction policy; otherwise manual review (`d19746d`) |
| S11-13 | Short-let calendar | `shortlet-bookings-calendar-and-claims` | PASS (external sync BLOCKED: no provider) | LOCAL — lapsed damage-claim responses go to `RESPONSE_OVERDUE` operator review, never acceptance |
| S11-14 | Turnover scheduler | cleaning job created at check-out | PARTIAL | pre-check-in inspection scheduling not automated |
| S11-15 | Manager mandate expiry | `manager-mandates`, `consent-and-mandate-expiry` | PASS | LOCAL |
| S11-16 | Preventive maintenance | `maintenance-preventive-and-payments` | PASS | LOCAL |
| S11-17 | Privacy retention | `privacy-requests-and-retention` | PARTIAL | guest anonymization with legal holds; other retention classes need the legal policy pack |
| S11-18 | Report generation | `report-generation` | PASS | LOCAL (media upload contract not exercised against real media-service) |
| S11-19 | Smart access expiry | `smart-access-expiry` | PASS | LOCAL |
| S11-x | Sale offers/milestones, professional fees, insurance renewals, property imports, security alerts, closure, housekeeping | various | PASS | LOCAL |
| S11-20 | Customer (tenant/guest) notification requests | `customer-notifications` | PASS (LOCAL) · live BLOCKED | lease → call outside the transaction → record; ended relationships suppressed; recipient domain not implemented |

## Section 12 — Cross-service and external integrations

| ID | Integration | Status | Level | Notes |
|---|---|---|---|---|
| S12-01 | Verification Service (IDENTITY, PROPERTY_AUTHORITY) | PASS | CONTRACT (IDENTITY) / LOCAL (PROPERTY_AUTHORITY) | real-process E2E: owner → real verification-service (sandbox provider) → `verification.verified.v1` → owner consumer; production KYC provider not selected |
| S12-02 | Media & Document Service | PASS | CONTRACT (metadata, deletion event) / LOCAL (rest) | real-process run exposed that identity evidence must be uploaded `DOMAIN_RESTRICTED` (the class default `ADMIN_RESTRICTED` hides it from the owner's ownership check); contract documented on `POST /verification` |
| S12-03 | Notification Service | PASS | CONTRACT | 25 owner templates seeded (drift-tested); E2E delivery of activation code through the real service |
| S12-04 | Payment Service | BLOCKED | — | contract + fakes; config refuses `PAYMENT_SERVICE_URL`; all PLATFORM money answers 503 |
| S12-05 | Marketplace | PASS (LOCAL) · live BLOCKED | internal APIs + events + customer-notification recipient contract; marketplace-service not built |
| S12-06 | Property Manager | PASS | LOCAL | internal APIs + consumers; producer not built |
| S12-07 | Artisan | PASS | LOCAL | provider callbacks + consumers; producer not built |
| S12-08 | Professional Services | PASS | LOCAL | as above |
| S12-09 | Admin | PASS | LOCAL | admin actor required on every admin route |
| S12-10 | CF Meet | BLOCKED | — | only context IDs stored; CF Meet service/contract not available |
| S12-11 | Maps (geocoding) | BLOCKED | — | no provider selected; owner-supplied coordinates validated, no geocoding adapter |
| S12-12 | Electronic signature | BLOCKED | LOCAL | adapter contract; `ESIGN_PROVIDER` refused; OFFLINE signing with evidence works |
| S12-13 | External marketplace/calendar sync | BLOCKED | LOCAL | adapter contract; import/conflict logic tested with a fake; `EXTERNAL_CALENDAR_PROVIDER` refused |
| S12-14 | Smart access devices | BLOCKED | LOCAL | adapter contract; provisioning/revocation tested with a fake; `SMART_ACCESS_PROVIDER` refused |
| S12-15 | Financing / insurance partners | BLOCKED | LOCAL | consent-gated adapter contracts tested with a fake; `FINANCING_PROVIDER`/`INSURANCE_PROVIDER` refused |
| S12-16 | Tenant / guest notification delivery (via the recipient's domain and notification-service) | BLOCKED | LOCAL | contract and fake-recipient tests (consent suppression, idempotency, relationship checks); no recipient domain exists |

## Section 13 — Product sections

| § | Area | Status | Outstanding |
|---|---|---|---|
| 13.1 | Owner role and dashboard | PASS | — |
| 13.2 | Registration, security, personal verification | PASS | OAuth BLOCKED (provider) |
| 13.3 | Banking, payout, tax | PARTIAL | payout execution BLOCKED (payment-service) |
| 13.4 | Ownership and authority | PASS | — |
| 13.5 | Property and portfolio records | PASS | — |
| 13.6 | Property verification and authenticity | PASS | production KYC provider (verification-service) |
| 13.7 | Listings for every category | PASS | — |
| 13.8 | Price and fee configuration | PASS | — |
| 13.9 | Availability and freshness | PASS | — |
| 13.10 | Marketing, inquiries, leads | PASS | CF Meet messaging BLOCKED |
| 13.11 | Viewings and access | PASS | — |
| 13.12 | Applications and tenant selection | PASS | — |
| 13.13 | Agreements and move-in | PARTIAL | e-sign BLOCKED; OFFLINE signing PASS |
| 13.14 | Rent, deposit, financial management | PARTIAL | PLATFORM collection BLOCKED (payment-service); automatic late fees and deposit silence acceptance need a legally approved jurisdiction policy (manual paths PASS) |
| 13.15 | Renewal, rent review, notices, move-out | PASS | notice periods come from per-country template versions; jurisdiction policy approval pending (business/legal) |
| 13.16 | Utilities and service charge | PARTIAL | tenant/guest announcements: contract PASS (LOCAL), delivery BLOCKED (recipient domain) |
| 13.17 | Commercial rent and sale | PASS | — |
| 13.18 | Land rent, lease, sale | PASS | — |
| 13.19 | Property sale, offers, closing | PARTIAL | PLATFORM money BLOCKED; post-sale ownership transfer PASS (LOCAL, SALE case); unit-level title transfer not supported |
| 13.20 | Personally operated short-let | PARTIAL | external calendar sync BLOCKED; PLATFORM money BLOCKED; refund schedules and claim windows await legal approval |
| 13.21 | Manager marketplace and delegation | PASS | manager search content comes from property-manager-service |
| 13.22 | Maintenance, artisans, preventive care | PASS | provider payment BLOCKED (payment-service) |
| 13.23 | Professional services and due diligence | PASS | fee payment BLOCKED (payment-service) |
| 13.24 | Communication and notifications | PARTIAL | CF Meet BLOCKED; WhatsApp not selected; tenant/guest delivery BLOCKED (recipient domain) |
| 13.25 | Finance, reports, market intelligence | PARTIAL | yield needs valuations; tax/statutory calendar not built; AI vacancy/renewal insights not built (statistical price/trend insight only) |
| 13.26 | Fraud, privacy, safety, disputes | PASS | — |
| 13.27 | Accessibility, assisted use, closure | PARTIAL | assisted (support-actor) flows not built; succession uses ownership change cases |
| 13.28 | Financing, mortgage, payment plans | BLOCKED | partner adapter; consent and offer handling PASS (LOCAL) |
| 13.29 | Insurance and protection | PARTIAL | partner products/claims BLOCKED; owner-held policies/claims PASS |
| 13.30 | Smart access and remote controls | PARTIAL | smart devices BLOCKED; manual access and grant lifecycle PASS |

## Totals (at `deb5c93` + register)

| Measure | Count |
|---|---|
| Migrations / tables | 16 / 131 |
| State machines | 24 |
| Public operations (auth + domain) | 43 + 288 = 331 (13 of them specification-path aliases) |
| Internal operations | 83 |
| Produced event types / consumers | 26 / 7 (2 with real producers, 5 BLOCKED) |
| Worker jobs (domain + platform) | 24 + 4 |
| Integration adapters | 11 (`calendarsync`, `customernotify`, `esign`, `financing`, `insurance`, `media`, `notification`, `oauth`, `payment`, `smartaccess`, `verification`) |
| Test functions (owner module) | 79 integration / 32 unit · E2E journeys: 4 (foundation, owner account, owner domain, owner + real verification/media producers) |
| Notification templates seeded | 25 owner + 4 platform |
| Certification | LOCAL for the domain; CONTRACT where stated; STAGING/PRODUCTION not claimed |

## Open product decisions

1. ~~Spec path aliases vs normalized paths~~ — both served (`9dfe6a7`).
2. Default activation rule (EMAIL) and identity gates for publish/payout/sale/transfer (configurable).
3. ~~Tenant/guest announcements~~ — contract built (`27511c9`); delivery needs marketplace-service to implement the recipient side.
4. ~~Ownership transfer after sale~~ — SALE ownership-change case (`42ff86b`).
5. **Legal approval required:** a jurisdiction policy version per launch jurisdiction (deposit silence acceptance, automatic late fees, windows and caps), and review of the short-let refund schedules — see [`LEGAL_POLICY.md`](LEGAL_POLICY.md). Until then those automatic decisions stay disabled.
6. Producers of payment/manager/artisan/professional/marketplace events must adopt this service's consumer contract ([`EVENT_COMPATIBILITY.md`](EVENT_COMPATIBILITY.md)).
7. Clients must upload identity evidence with `access_classification: DOMAIN_RESTRICTED` (found by the real-process E2E).
8. Unit-level sales: whole-property ownership transfer only; unit title transfer needs a product decision.

## Appendix D gaps

| Gap | Handling | Blocks |
|---|---|---|
| Payment Service API/events | contract + fakes; config refuses the URL; PLATFORM money 503 | rent/deposit/booking/sale/provider/fee collection and payouts |
| Production KYC/liveness | verification-service contract | production authority verification |
| Production email/SMS providers | notification-service dev/disabled providers | production delivery |
| OAuth providers | contract + 503 + config guard | social sign-in |
| E-signature, external calendars, smart locks, financing/insurance partners, maps | adapter contracts + 503 + config guards | those features |
| CF Meet | IDs only | in-app messaging |
| Legal policy packs (notice periods, deposit rules, retention classes) | versioned jurisdiction policy with legal-approval reference and second-operator approval (`d19746d`); **no version approved**; consequential automation disabled, manual review paths in place | automatic deposit/late-fee decisions; jurisdiction certification |

## CI status

Since `2592eff` every GitHub Actions job fails within 1–3 seconds without executing a step (latest:
runs `37907440709` Backend, `37907440571` Security, `37907440570` Contracts, `37907440617` Images,
`37907440551` Structure at `659d43e`). This is an account-level Actions restriction (runner not
assigned, typically billing/spending limit), not a code failure; it cannot be fixed from the
repository. All gates were therefore run locally (below). STAGING/PRODUCTION promotion was not attempted.

## Quality gates run for this register (2026-10-09, local, at `deb5c93`)

A local replica of the Backend, Contracts and Security workflows ran over every Go module
(`shared/go`, `gateway/api-gateway`, verification, media-document, notification and individual-owner
services, `tests/e2e`) with PostgreSQL 16, Redis, NATS (embedded) and MinIO. Result: no failures.

| Gate | Result |
|---|---|
| gofmt, `go mod tidy` drift, `go vet`, staticcheck (all 7 modules) | clean |
| `go test -race -count=1 ./...` with `CF_REQUIRE_INTEGRATION=1` (all 7 modules, incl. owner integration ≈150 s and E2E with real processes) | pass |
| Real-process E2E: foundation, owner account, owner domain, owner + real verification/media producers | pass |
| sqlc diff (4 services) | clean |
| Architecture boundaries (`check-architecture.sh --self-test` and run) | OK |
| Generated API docs (`apidocgen -check`) | current |
| OpenAPI validation, JSON Schema metaschema, Postman JSON | OK |
| actionlint | clean |
| gitleaks (full git history and working tree) | no leaks |
| govulncheck | **BLOCKED** — `vuln.go.dev` is unreachable from this environment (HTTP 403 from the network policy); not run |
| GitHub Actions | not dispatched (account-level restriction, see "CI status") |
| Staging / production | not attempted; nothing here certifies them |

Deployment wiring (staging/production compose entries, `promote.yml` digest input) is intentionally
not added until an owner image digest exists from a working CI and deployment is approved.
