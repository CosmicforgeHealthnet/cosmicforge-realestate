# Business Operations Section 3 implementation and certification

## A. Baseline and delivery identity

Repository: `CosmicforgeHealthnet/cosmicforge-realestate`; branch: **staging only**. Starting remote commit: `065b8e133bd19e6500fe67bcd31bfb61f2cf6b54`, fetched and compared before changes. Historical commits `cdc76f4`, `72ef535` and `065b8e1` were audited before reuse.

The authoritative Word source was read in full, Sections 1–27 and Annexes D–J. Its faithful paragraph/table transcription and source SHA256 are in [the specification](../specifications/CosmicForge_Business_Property_Operations_Full_Backend_Implementation_Spec_v1.0.md). This delivery implements Section 3 and applicable annex requirements; later domain workflows remain future work.

History: `c84ffa1`/`0d61f5e` foundation, `f57eb47` domain rules, `1c26b26` accounts/sessions/MFA/KYB, `da1dc34` effective database/event ownership, followed by final implementation commit **0e4e7926291ea8cb6f23397c9facad6fffc3c6b7** and this evidence-only documentation update. Final SHA is recorded outside its own content; CI evidence is tied to a SHA below. Original dirty Individual Owner frontend files and user documents remain preserved and unstaged. Main and production were not changed. No VPS access was used.

## B. Sections 1–2 remediation

| Previous blocker | Root cause/remediation | Evidence/status |
|---|---|---|
| Backend failure at 065b8e1 | Job 123830771080 reports hosted runner lost communication. Detailed log blob unavailable. Same-SHA failed-job rerun passed as job 123963506900. No failing assertion or Owner regression demonstrated. | Controlled real-PG Owner reproduction: 180 tests/subtests PASS, no skips. No Owner implementation changed. Full CI green at 1c26b26; delivery SHA evaluated separately. |
| NATS/outbox unproven | Real shared relay/JetStream exercised; business subjects added preserving legacy. Fixed shared consumer timeout when AckWait <=1s. | Commit/rollback, outage/restart, republish, deduplication, leases/fences, poison quarantine and shutdown PASS. |
| Gateway path mismatch | Canonical `/api/v1/business/`; explicit retained `/api/v1/business-operations/` alias with identical security/handlers. | Signed identity/JWT forwarding, domain denial, request/trace IDs, errors/timeouts, internal/unknown routes tested. |
| Internal contracts deferred | Typed signed Verification/Notification clients composed into API/worker. | Signed contract, outage, idempotency and durable workflow tests PASS; live providers separate. |
| Staging database | Effective driver DB and actual current_database enforced; owner/migrator/runtime scripts and separate credentials. | Isolated provisioning twice, up twice/status/version 4, runtime privileges PASS. Actual staging credentials/grants/connectivity unverified. |
| Deployment absent | Existing Images/Compose retained; separate migration/API/worker secrets and fail-safe migration dependencies. | Repository preparation complete. No preauthorised transport/runner/usable staging secrets; no invented SSH deployment. Live PARTIAL. |

See [foundation incident evidence](business-operations-foundation-remediation.md), [updated original report](business-operations-sections-01-02-report.md) and [staging operator runbook](business-operations-staging-readiness.md).

## C. Section 3 requirements matrix

Paths are relative to `services/business-operations-service`. PASS means implemented and tested on the stated infrastructure; it does not certify live providers/staging.

| Requirement | Result | Files/evidence |
|---|---|---|
| 2.1 Registration | PASS | `internal/auth/service.go`, SQLC auth repository: DRAFT organisation, explicit representative role/grants, duplicate prevention, peppered retry fingerprint, audit/outbox; `auth_test.go`. |
| 2.2 Authentication | PASS | Shared bounded Argon2id/Ed25519/session primitives; key rotation, safe failures, rate limits, refresh-family reuse invalidation; auth/security concurrency tests. |
| 2.3 Sessions/devices | PASS | Current server-side session/account checks, rotation locks/CAS, logout/all, password invalidation, device version/retirement and untrusted risk metadata; auth/concurrency tests. |
| 2.4 MFA | PASS workflows; live delivery PARTIAL | `auth/mfa*.go`: protected TOTP, EMAIL/SMS Notification jobs, confirmation, bound login/step-up codes, expiry/attempts/replay, one-time recovery/removal; auth and `mfa_delivery_test.go`. |
| 2.5 Organisation | PASS local; external changes PARTIAL | Profile/FSM, independent admin restrictions, fresh KYB to resume, draft closure; `http/lifecycle.go`, organisation/annex/final-guard tests. Activated closure/legal representative change fail closed pending owning assertions. |
| 2.6 Capacities/packs | PASS framework; provider PARTIAL | Declaration/status/restriction/expiry and typed capacity/pack evaluation; `http/capacities.go`, `capacity_verification_test.go`. Unsupported/unverified capacity never authorizes use. |
| 2.7 KYB | PASS contract/workflows; live PARTIAL | Existing BUSINESS create/read/evidence/submit/reverify, immutable lineage, signed read-back of broker hints, stale update/forgery rejection, expiry/restriction; organisation/renewal/event tests. |
| 2.8 Hierarchy | PASS | Regions/branches/departments/teams CRUD/lifecycle, timezone/currency, tenant ancestry/version checks; `http/hierarchy.go`, hierarchy tests. |
| 2.9 Workforce | PASS | Hashed single-use invitations, encrypted delivery, provisional accounts/existing-account proof, membership lifecycle/history, acknowledged handover; staff/annex/notification suppression tests. |
| 2.10 RBAC | PASS | Immutable versioned roles/mappings, capability+scope+dates+denies+MFA+amount checks, direct authority ceiling and self-escalation prevention; workforce/hierarchy/temporary/concurrency tests. |
| 2.11 Hierarchical scopes | PASS | Trusted signed session organisation plus live membership, company/region/branch/department/team ancestry, cross-tenant denial; `http/tenant.go`. Future graph references fail closed until Section 4 resolution exists. |
| 2.12 Policies | PASS | Immutable versioned policy, risk/action/channel/actor-role/scope/amount/currency, ambiguity denial, exact operation digest; policy/approval tests. |
| 2.13 Multi-level/SoD | PASS | Ordered quorum, distinct actor and delegated authority provenance, frozen version, deadlines/cancel/draft-submit, evidence, concurrent idempotent decisions; HTTP/concurrency/annex tests. |
| 2.14 Delegation | PASS | <=30-day grants, scope/capability/MFA/amount ceiling, no chain, source authority rechecked, immediate revocation/expiry; temporary/annex tests. |
| 2.15 Break-glass | PASS | Senior eligibility, recent MFA, written reason, independent reviewer, narrow read scope, 30–900 seconds, audit/alert/review, deny/tenant boundaries preserved; temporary tests. |
| 2.16 High-risk controls | PASS Section 3 | Annex F action/capability contracts; exact approved role/scope/lifecycle/representative/emergency commands. `approvals/actions.go`, `finance/authorization.go`; no future-domain/payment execution. |
| 2.17 Federation/provisioning | PASS adapter requirement; configured provider N/A | `auth/federation`: OIDC/SAML challenge/assertion and SCIM-style provisioning boundaries, mapping schema; issuer/audience/state/nonce/expiry/replay tests. No implicit email linking/IdP role authority. |
| 2.18 Audit/security | PASS | Transactional audit/security/outbox and immutable database history/privileges; sensitive material excluded; actual stored event schemas validated by HTTP fixtures. |

Bootstrap privilege configuration requires the primary representative, recent MFA and never-activated DRAFT/KYB_PENDING state. Creating any policy permanently seals its action's exception; deactivation never reopens it. Activation seals all remaining bootstrap authority. Configure independent approvers before activation. Later privilege commands consume an exact unused/unexpired approved operation; requester/proxy authority cannot satisfy separation of duties.

## D. Database implementation

Goose 00001–00004 are additive/forward-only; published 00002 was not rewritten. Required names retained: business_accounts, business_credentials, business_sessions, business_devices, business_mfa_methods, organisations, organisation_capacities, capability_packs, branches, departments, teams, staff_memberships, role_templates, permissions, role_permissions, staff_scope_grants, approval_policies, approval_requests, approval_steps, delegation_grants, verification_links, security_events, audit_events.

Supporting tables cover refresh tokens/families, MFA challenges/recovery/delivery codes, regions, invitations/history/handover, approval decisions, emergency grants/review, external identity mappings, verification lineage, shared idempotency, outbox/processed/quarantine/notification acceptance. Tenant composite FKs, unique credentials/pending invitations/MFA/decisions/versions and expiry/list indexes protect integrity. Application IDs use shared UUIDv7; money uses int64 amount_minor plus currency.

SQLC auth/session/outbox queries and generated models are current. Domain commands use typed Go input and parameterized owning transactions. Organisation serialization, row locks, optimistic versions and shared idempotency protect retries. Audit/decisions/role mappings/policy definitions/approval snapshots/completed handover/verification lineage are immutable. Runtime cannot create objects, mutate audit or read Goose metadata. Password/token hashes, peppered codes and protected MFA/delivery ciphertext are stored without logging secrets.

Disposable PostgreSQL tests cover fresh/repeated migrations/status/version 4, concurrency, duplicates and immutability. Provisioning/grants executed twice locally; schema CREATE and audit UPDATE false for runtime. This does not certify actual staging ownership/connectivity.

## E. API / OpenAPI / Postman

Complete [96-route inventory](../../services/business-operations-service/contracts/routes.json): account/auth/password/MFA/recovery/sessions/devices/organisation selection; profile/lifecycle/closure/representative/KYB/evidence/renewal/capacities/packs; regions/branches/departments/teams; staff/invitations/handover/scopes; roles/catalogue; policies/evaluation/version/activation; approvals/decisions; delegation/emergency/review/audit.

Canonical `/api/v1/business/...` preserves Annex E paths. Legacy prefix and Annex D profile/verification aliases share security. Internal provider/broker operations have no frontend bypass. Auth exceptions are documented; every request requires trusted Gateway identity. Updates use current versions/locks; side-effect retries use stable idempotency where required; lists use bounded pagination/filters; shared errors and request correlation are preserved.

[OpenAPI](../../contracts/public/openapi/business-operations.yaml) and official Postman `03 Business Operations` contain 78 referenced schemas, examples, success/validation/auth/forbidden/conflict responses, headers/permissions/idempotency and representative tests/ID capture. Required 00/01/02 folders and separate _Internal/_Webhooks/_Admin folders are present; empty restricted folders explain that no public bypass exists.

`generate_business_operations.py --check --validate` verifies outputs/examples. `TestBusinessRegisteredRoutesMatchOpenAPIAndOfficialPostman` compares actual chi registrations against both contracts. Event generator verifies 25 producer/schema contracts; HTTP journeys validate stored outbox envelopes.

## F. Security

Executed coverage includes correct/wrong/disabled login, expiry/signature/key rotation, refresh expiry/reuse/concurrency, logout/revocation/password invalidation, Argon2id, rate limits, MFA replay/expiry/attempts/recovery; tenant/branch/membership isolation, untrusted organisation header denial, revoked/expired role/grants, dates/denies/amount/MFA, self-escalation; ordered/immutable/concurrent approval, self/proxy authority denial; delegation ceiling/source/expiry/revocation; emergency controls/alerts/review; forged verification producer/case/subject and signed read-back; Notification outage/retry/duplicates/poison and outbox rollback/recovery.

No production secrets/customer data used. Shared crypto/key rotation/service auth reused. Worker does not load account JWT private signing key. Local race/Docker/Gitleaks tools were unavailable; Linux Actions on 0e4e792 executed race tests, full-history Gitleaks, container smoke and Trivy successfully. No failing check was disabled.

## G. Integrations/events/workers

| Boundary | Result |
|---|---|
| Gateway | Route/server tests PASS; actual Gateway/container smoke configured in Images, latest result recorded after execution. |
| Verification | Signed HTTP/reverify contract and fake-authority journeys PASS; live PARTIAL. |
| Notification | Signed acceptance, encrypted jobs, real-broker retry/duplicate/quarantine PASS; live delivery unverified. |
| NATS/outbox | Real PG/JetStream PASS; canonical outbox recovery, quarantine, at-least-once/idempotent consumers. |
| Payment/other services | Typed references/boundaries only; no payment execution, cross-service SQL, provider SDK or fabricated graph. |
| Database/deployment | Disposable tests/repository preparation PASS; actual staging PARTIAL. |

Naming reconciliation: business.staff.updated.v1 follows main specification instead of Annex staff.updated.v1; business.restricted.v1 follows Annex D.2; business.approval.completed.v1 is terminal completion corresponding to Annex approval.completed.v1. Intermediate decisions use business.approval.decided.v1. Future consumers must filter action; no approval event executes payment. Required business.verified.v1/business.capacity.changed.v1 retained. Shared UUIDv7 envelopes carry producer/aggregate/actor/trace fields via transactional outbox.

One established worker/maintenance loop handles relay, encrypted notices, durable verification consumer, authoritative reconciliation and invitation/member/scope/session/MFA/delegation/emergency/approval/capacity/KYB expiry. Synchronous authorization enforces expiry independently of maintenance. Invalid delayed invitation/MFA jobs are suppressed/audited; poison jobs quarantine; unaccepted canonical outbox jobs remain recoverable. Notification acceptance is not delivery proof.

## H. Testing and CI evidence

- Local full run with CF_REQUIRE_INTEGRATION=1, isolated PG18 and real disk-backed JetStream: `go test -json -count=1 -timeout=15m ./...` **62 tests/subtests PASS, zero failures/skips**, including the final authoritative-resume and invalidated-invitation regressions.
- gofmt, go vet, staticcheck, go mod tidy, sqlc diff PASS. Gateway routing/server and shared JetStream packages PASS.
- Contract generation/example validation: 96 routes/78 typed schemas PASS; 25 event schemas PASS. Container certification tool compiles.
- Isolated provisioning/grants twice, migration up twice/status/version and forbidden runtime privileges PASS. Race, Docker, Trivy and full-history Gitleaks were executed successfully by Linux CI on 0e4e792.
- Checkpoint **1c26b269ff3fae60d740de7e9503e718871e3f43** all green: [Backend](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41032138946), [Architecture](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41032138950), [Contracts](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41032138959), [Security](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41032139599), [Images](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41032138947). These do not certify a later delivery SHA.

Images adds disposable PG/NATS/Redis containers, actual migration/API/worker image and Gateway binary, runtime grants, canonical/legacy routing, signed Notification fake, outbox outage/recovery, restart and SIGTERM. Execution passed at 0e4e792. The final documentation SHA is independently rechecked before delivery. Image publishing is not deployment.

E2E journeys 1–10 map respectively to organisation/renewal, auth/MFA, staff, role revocation, multi-membership hierarchy, approvals, delegation, break-glass, capacity/restriction and refresh-family tests. Provider truth/delivery is explicitly fake in these journeys; PostgreSQL and JetStream are real.

## I. Deferred dependencies

Operator: provision/verify staging DB roles and runtime/migrator secrets, service trust registries, NATS ACLs, Notification providers/invitation frontend, approved staging-only GitHub deployment transport; then actual rollout/readiness/smoke/rollback evidence.

Owning-provider operations absent: capacity-specific eligibility and legal representative assertions. Generic KYB cannot substitute. Previously activated closure needs downstream readiness evidence. Typed interfaces fail closed and tests distinguish fakes. SSO live certification needs configured IdP; adapter readiness is implemented as specified.

Future Section 4 graph and Sections 5–27 workflows are deliberately absent; scope references, Annex F policy identifiers and Payment reference boundary are ready without fabricating those records/execution.

## J. Certification

| Area | Result |
|---|---|
| Sections 1–2 repository closure | PASS repository certification at 0e4e792; live staging PARTIAL |
| Authentication/sessions/devices | PASS executable tests |
| Organisation/capacities | PASS framework; external assertions/activated closure PARTIAL |
| KYB | PASS contract/fake-authority workflows; live PARTIAL |
| Staff/workforce/RBAC/scopes | PASS |
| Approvals/SoD/delegation/break-glass | PASS |
| MFA | PASS TOTP/Notification-mediated EMAIL/SMS; live delivery PARTIAL |
| Security/audit | PASS local plus 0e4e792 Linux race/Gitleaks/govulncheck/Trivy |
| Database/events/API | PASS disposable tests/contracts; live staging PARTIAL |
| OpenAPI/Postman | PASS |
| Implementation CI/image digest | PASS all five workflows at 0e4e792; digest/evidence below. Final documentation SHA rechecked and recorded in delivery response. |
| Actual staging deployment | PARTIAL / NOT EXECUTED: authorised transport/environment unavailable |

Overall live operational certification remains **PARTIAL**. Repository functionality and executed evidence are distinguished from external authority and rollout prerequisites.

## Executed implementation CI certificate

All required workflows completed successfully for **0e4e7926291ea8cb6f23397c9facad6fffc3c6b7**, observed 2026-10-30 19:42 UTC:

| Workflow | Evidence |
|---|---|
| Backend | [41115860251](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41115860251): every matrix test/lint, architecture, SQLC and process-level E2E job PASS |
| Architecture | [41115860305](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41115860305) PASS |
| Contracts | [41115860249](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41115860249) PASS, including OpenAPI validation, schema metaschemas, Postman JSON and generated drift/examples |
| Security | [41115860499](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41115860499) PASS, full-history Gitleaks, IaC and every configured govulncheck job |
| Images | [41115860326](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41115860326) PASS, all seven components and combined immutable digest artifact |

Business race job [124269792070](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41115860251/job/124269792070), artifact `backend-tests-7`: **62 tests/subtests PASS, 0 FAIL, 0 SKIP**, with real PostgreSQL 16 and disk-backed JetStream. Conditional MinIO startup is correctly inapplicable to Business Operations; no required test job was skipped. The unchanged Individual Owner race job and the final foundation process-level E2E job also passed.

Business image job [124269791945](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41115860326/job/124269791945) executed the actual Gateway/container recovery certificate and Trivy successfully (0 fixable HIGH/CRITICAL findings under the existing policy). Published implementation image:

`ghcr.io/cosmicforgehealthnet/cosmicforge-realestate/business-operations-service@sha256:966bc07a44985e27bd5268481e49148fa1ecd69e0b84c0e4b3f30a2d2523f302`

This digest belongs to 0e4e792. A later documentation build embeds its own commit and produces its own digest; the final delivery response identifies the latest SHA/digest after checking its Actions. The report cannot embed its own future commit hash. Local govulncheck additionally reported zero reachable/imported-package vulnerabilities and three non-reachable module advisories (GO-2026-6866, GO-2026-6860, GO-2026-5932); the latter OpenPGP package is not imported. No vulnerability gate was suppressed.

Actual deployment re-audit: zero staging deployment records for 0e4e792, zero self-hosted runners, no configured GitHub environment, and only the five existing validation/image workflows. No actual staging connection, rollout or live provider approval/delivery is certified. Required operator actions remain in the runbook.

Working tree before the evidence commit contained only these three report updates plus the original two modified Individual Owner frontend files and four untracked user documents. Only the three intended reports were staged. Final delivery preserves those unrelated files.
