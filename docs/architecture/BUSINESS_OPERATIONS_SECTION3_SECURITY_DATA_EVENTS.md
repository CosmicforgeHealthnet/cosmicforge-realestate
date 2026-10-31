# Section 3 security, data and event contracts

## Trusted request and authorization flow

Gateway accepts canonical `/api/v1/business/` and the explicit legacy
`/api/v1/business-operations/` alias. Public account operations have documented
authentication exceptions. Every service request requires a valid Gateway
service assertion. Protected operations also verify the business JWT and match
its actor/organisation with the signed delegation. The selected organisation is
loaded from the live session and active membership, never an arbitrary header.

Authorization combines active role capabilities with explicit grants, trusted
scope ancestry, dates, explicit denies, MFA recency and integer amount/currency
limits. Organisation restrictions and authoritative KYB expiry remain enforced
without a worker. Organisation locks serialize privilege-changing commands;
current session/membership/role state is rechecked inside the transaction.
Future property/development/unit/facility scope identifiers exist as contracts,
but have no fabricated graph authority before Section 4.

Bootstrap is representative-only with MFA in never-activated onboarding. Any
created policy seals its action's bootstrap exception permanently; activation
seals all. Later high-risk changes consume exact approved operation digests.
Policy snapshots and decisions are immutable. Delegated approval provenance
prevents a requester approving through a proxy or one authority satisfying
independent levels. Temporary authority cannot create permanent authority.

## Credentials, sessions and protected storage

Shared Argon2id and Ed25519 primitives are used. Refresh tokens are random,
hashed, rotated under transaction locks and tracked by session family. Reuse
revokes the family and persists the security event even when returning an error.
Access expiry alone does not determine revocation: live server state is checked.
Password change/logout-all invalidate all relevant sessions. Device identifiers
are associations and never proof of trust.

MFA TOTP/contact material is AES-GCM protected with account/method associated
data and key IDs. EMAIL/SMS codes are random eight-digit values, HMAC protected,
bound to enrolment/login/session purpose, five-minute lifetime, five attempts,
single-use. Delivery jobs are encrypted through Notification Service. Recovery
codes are one-time hashes. Secret enrolment material is not returned after
confirmation. Removing/changing MFA requires password and recent step-up and
revokes sessions. Workers do not load account JWT private signing keys.

Invitations have 256-bit random tokens, hashed acceptance lookup, encrypted
delivery, 24-hour maximum lifetime and single-use acceptance. Existing account
holders must authenticate; invitations cannot replace existing credentials.
Role/issuer/organisation epoch changes invalidate acceptance and suppress stale
delivery. Tokens use an HTTPS frontend URL fragment to reduce referrer leakage.

## Data ownership and immutability

Only `business_operations_db` is accepted, including effective driver query
overrides; startup also verifies `current_database()`. Goose migrations are
forward-only. SQLC generates auth/session/outbox repositories and schema models;
domain transactions use parameterized SQL and owning typed commands.

Tenant composite foreign keys, unique credentials/invitations/versions and
expiry/list indexes support isolation and concurrency. UUIDv7 IDs use shared
generation. Amount-dependent policies use int64 minor units plus currency.
Audit/decisions/role mappings/policy definitions/approval governing snapshots,
completed handovers and verification lineage reject mutation. Runtime grants
exclude DDL, Goose metadata and immutable record mutation. Separate NOLOGIN
owner, migrator and runtime identities are prepared by operator/CI scripts.

Audit writes and high-risk state/outbox changes share a transaction; failure
aborts the operation. Context records actor, organisation, target, reason,
correlation and timestamp without passwords, raw tokens, MFA codes or document
bytes. No cross-service foreign key/database access exists.

## Events and recovery

Versioned schemas are in `contracts/events/business/`. Envelopes reuse shared
UUIDv7 IDs, business producer identity, aggregate, actor, trace and timestamp.
`business.staff.updated.v1` is the main-spec canonical equivalent of Annex
`staff.updated.v1`; `business.restricted.v1` follows Annex D.2;
`business.approval.completed.v1` maps terminal Annex `approval.completed.v1`.
Intermediate decisions use `business.approval.decided.v1`. Consumers filter
action/context; completion never executes payment. Required verified/capacity
events retain the documented names.

Database state and outbox commit atomically. Shared JetStream relay leases,
stable event message IDs, durable consumers and processed-event records support
at-least-once delivery. Poison events retain quarantine metadata. Notification
acceptance is recorded, canonical outbox jobs remain available for recovery,
and invalid expired/revoked challenge/invitation delivery is suppressed/audited.
No broker hint alone changes verification truth: an authenticated owning API
read-back validates subject/type/case and ordering. Renewal archives case lineage.

One existing maintenance loop expires temporary authority and reconciles KYB;
request-time checks enforce expiry independently. Graceful cancellation drains
workers; restart resumes durable work. Tests use disposable PostgreSQL and real
disk-backed JetStream, with provider contract fakes explicitly distinguished
from live provider and staging certification.

## External dependencies

Capacity eligibility and legal representative assertions are missing owning
provider operations and fail closed. Activated organisation closure requires
downstream readiness evidence. Actual SSO needs configured IdP, while adapter
contracts are ready. Actual deployment requires the operator-owned staging
transport/secrets and live smoke checks described in the staging runbook.

## Commercial authorization extension

Migration 00006 versions eligible initial Owner roles and adds explicit commercial grants. Pricing, publication, mapping, field override, tax/currency and conflict decisions bind exact operation/scope/channel/amount/currency and require independent recent-MFA approval. There is no onboarding-bootstrap or break-glass write bypass. Worker commands preserve original session and revalidate current grants; a narrow system pause may only reduce remote supply. Marketplace financial/receipt APIs require signed caller plus organisation delegation. Callback keys are sealed with tenant/connection AAD; only reviewed fingerprints enter approval/audit/idempotent responses. See [commercial contracts and events](BUSINESS_OPERATIONS_COMMERCIAL_SUPPLY.md).
