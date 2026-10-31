# Business Operations Service

Business Operations owns business accounts, organisation membership, scopes and enterprise approvals in `business_operations_db`. Sections 3–4 are implemented; live provider and staging certification are reported separately from executable test evidence.

## Architecture and public contract

- Existing shared Go configuration, HTTP, Argon2id, Ed25519 JWT, service authentication, sessions, PostgreSQL, idempotency, outbox and JetStream primitives are reused.
- Verification owns verification truth; Notification owns delivery; Payment owns all money movement. Media owns document bytes; CF Meet owns human messages. No cross-service SQL or provider delivery/execution SDK is introduced.
- Canonical public prefix: `/api/v1/business/`. The existing `/api/v1/business-operations/` prefix is a tested compatibility alias with the same handlers and security.
- Annex D profile/verification shorthand has explicit `/profile` and `/verification` aliases of the canonical organisation handlers. `/capabilities` returns effective capacity eligibility, while `/organisation/capacities` and `/organisation/capability-packs` manage declarations/configuration.
- The Gateway signs its service identity and forwards the business access JWT. The service verifies both and checks current account, session, selected organisation, membership, role, grants and expiry. Client organisation headers never override that context. No Business Operations internal/admin/webhook bypass is exposed.
- [OpenAPI](../../contracts/public/openapi/business-operations.yaml), [route inventory](contracts/routes.json) and the official `03 Business Operations` Postman folder cover 188 method/path pairs. Regenerate with `python scripts/contracts/generate_business_operations.py` from repository root.

## Implemented Section 3

Registration creates a DRAFT organisation and explicit initial owner capabilities; it never grants KYB approval. Authentication includes rotating refresh families, reuse invalidation, current-session checks, logout/all, password change, device labels/revocation, rate limits and protected TOTP with confirmation, expiring login challenges, step-up, replay prevention and one-time recovery codes. TOTP, email and SMS methods are implemented. Email/SMS challenges use encrypted Notification jobs and protected contact data, eight-digit challenge-bound codes, five-minute expiry, five attempts and single-use recovery. One active/pending method per account prevents silent method replacement. Actual provider delivery and enterprise federation remain separate configuration/certification concerns.

Organisation profile/lifecycle, branches/regions/departments/teams, staff invitations and membership, versioned roles, explicit scope grants, deny policies, amount limits, effective dates, handover/termination, versioned multi-level approvals, approval delegation and emergency access are executable. Immutable decisions retain both acting accounts and delegated authority provenance. Policy snapshots include action, risk, amount/currency, scope, actor-role and channel context.

Initial privilege configuration is limited to the primary representative with recent MFA while the organisation has never been activated and is DRAFT/KYB_PENDING. Creating any policy permanently seals its action’s bootstrap exception, even if later deactivated. First activation permanently seals all remaining exceptions. Later role/scope changes bind an independently approved exact operation digest; no self-escalation is permitted. Initial onboarding must configure independent approvers before activation.

Delegation cannot exceed direct authority or be chained. Its source account/role/grants are rechecked on every use. Delegated approvals cannot approve the source actor's request or reuse one authority across independent approval requirements. Emergency access requires a designated senior role, an independent reviewer, recent MFA, a written reason, a branch/department/team scope and a 30–900 second lifetime. Only explicit read capabilities are permitted, existing denies remain effective, and every emergency use is audited.

The Verification client uses existing authenticated BUSINESS case create/read/evidence/submit/reverify contracts. Renewal preserves immutable case lineage and requires a new authoritative decision. Broker events are hints: a signed API read-back is always required. Reconciliation rejects stale updates and cannot clear an independent administrative restriction. Capacity-specific and legal-representative assertions have typed fail-closed boundaries because those provider operations do not exist yet. Previously active organisation closure also requires an external readiness assertion rather than fabricated closure safety.

## Persistence and worker

Goose migrations 00001–00005 add the complete required catalogue and supporting records. SQLC generates account/session/outbox queries and models; domain commands use typed parameters and owning transactions. Tenant FKs, unique invitation constraints, row locks, optimistic versions and immutable audit/decision/policy/role definitions protect concurrency. Application IDs use shared UUIDv7 generation; monetary values use integer minor units and currency.

The worker runs the established outbox relay, encrypted Notification jobs, durable verification consumer, authoritative verification reconciliation and one maintenance loop for invitations, memberships/scopes, sessions, MFA challenges, delegations, emergency access, approvals and capacity/KYB expiry. API expiry checks work independently of maintenance availability. Poison jobs retain quarantine metadata; unaccepted notifications can be replayed from retained outbox records. Notification acceptance is not a delivery receipt.

## Configuration and operation

Use `.env.example` and `infrastructure/docker/service-env/business-operations-service.env.example`. `DATABASE_URL` must effectively connect to `business_operations_db`, including after driver query-parameter overrides. Startup also checks `current_database()`.

API configuration requires standard `SERVICE_AUTH_*`, separate `BUSINESS_ACCESS_*` signing keys, `BUSINESS_CODE_PEPPER`, protected `BUSINESS_MFA_*` encryption keys, provider service URLs and an HTTPS `BUSINESS_INVITATION_ACCEPT_URL` for invitation delivery. Secrets support `_FILE`; retired public/encryption keys are configurable. Workers need service identity/protected storage and provider URLs, but do not load account JWT signing keys. NATS is required by the worker. Pool size and graceful shutdown are bounded.

Operator-run provisioning and least-privilege grant scripts are under `scripts/database/business-operations-*.sql`. No credential is committed. Use a separate migrator identity that assumes the NOLOGIN owner role; API/worker use the runtime role. Apply runtime grants after migrations. The scripts refuse conflicting ownership and modify only the Business Operations database/roles.

## Verification and deployment

Run `go test ./...`, `go test -race ./...`, `go vet ./...`, `staticcheck ./...`, `go mod tidy` and `sqlc diff -f sql/sqlc.yaml` in this module. Set `CF_REQUIRE_INTEGRATION=1` and `CF_TEST_POSTGRES_URL` to an isolated test administrator. Tests create/drop unique disposable databases and use real disk-backed JetStream. The Images workflow additionally runs PostgreSQL and NATS containers with the actual migration/API/worker image, runtime database grants, authenticated provider contract fake, broker recovery and SIGTERM checks.

The distroless non-root image includes API, worker and migration binaries. Existing staging Compose uses immutable digests and controlled migration dependencies. There is no preauthorised GitHub-to-staging deployment transport or available staging credential here. No direct VPS access or production deployment is used. See [Section 3 report](../../docs/implementation/business-operations-section-03-report.md) and [staging runbook](../../docs/implementation/business-operations-staging-readiness.md).

Sections 5–27 remain future work. Their directory placeholders are boundaries, not completed workflows.

## Section 4 canonical inventory

`internal/portfolio` owns one graph with 14 typed identity anchors: portfolio,
development, phase, parcel, property, building, block, floor, unit, room type,
room, facility, common area and asset. Physical containment, portfolio membership
and parcel associations are explicit distinct edges. Room categories never act
as physical parents. Common-area responses derive `non_sellable=true` from the
immutable kind. No availability, publication, reservation, lease, sale or payment
operation is added.

All graph APIs use the existing trusted tenant, current RBAC, versioned approvals,
idempotency and transactional audit/outbox. Controlled changes bind node and
subtree revisions plus destination revision. Previously activated structures
require complete downstream dependency evidence; absent providers return 503.
There is no onboarding approval bypass for inventory restructuring or remapping.

The existing worker performs bounded JSON/CSV preview, execution/checkpoint,
resume and safe soft recovery. Preview never creates canonical nodes. Live
initiator/session/role/grants are rechecked per batch; concurrent claims are
fenced. Unexpected failures quarantine a row and pause unless partial execution
was explicitly accepted. Recovery preserves canonical IDs and mapping provenance,
with reconciliation tasks for later changes or acquired dependencies.

Optional `MEDIA_SERVICE_URL` enables authenticated metadata and purpose-bound
access. Only references and confirmed space coordinates are retained; Media
owns bytes. Verification owns property authority; current case read-back and
expiry/version checks control projections. See the [Section 4 data/security/event
contract](../../docs/architecture/BUSINESS_OPERATIONS_SECTION4_GRAPH.md) and
[implementation report](../../docs/implementation/business-operations-section-04-report.md).
