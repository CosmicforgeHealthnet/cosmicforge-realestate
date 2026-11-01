# Section 9 reservation implementation checkpoint

Status: **PARTIAL — implementation continues**. This report records a logical reservation-core milestone, not acceptance of the full Sections 8–11 assignment.

## Baseline and scope

Repository `CosmicforgeHealthnet/cosmicforge-realestate`; branch `staging` only. Starting and re-fetched remote baseline: `19eb01f456d3cd40217e4ecd0e70a7a8073e1307`. No main, production database, VPS or SSH operation was performed. Existing Individual Owner web changes, master-plan document/generator/lock file and `README_FIRST.md` are excluded from this milestone.

The authoritative specification and master Sections 8–11 request govern the work. The baseline had Goose 7, 283 routes, 214 typed schemas and 40 Business event contracts. All five exact-baseline workflows were successful. Baseline evidence is in `business-operations-sections-08-11-audit.md`.

## Implemented checkpoint

- Canonical pending reservations and inquiry context, independently approved acceptance/guarantee policies, immutable accepted prices/taxes/fees/local-time policies, live confirmation, cancellation, whole-line partial cancellation, per-line no-show deadlines, dispute/review and expiry.
- Real inventory hold conversion and release, immutable committed-night provenance, allotment conservation, ordered locks, failed multi-line rollback and concurrent acceptance safety.
- Approved pre-arrival amendment, immutable financial-credit lineage, whole-line split/merge, group references and typed source guest associations.
- Expiring waitlist offers backed by real holds and single-use acceptance.
- Signed Marketplace-side intake/status, connection-scoped external version dedupe, durable technical receipts, savepoint rollback, source cancellation and independently approved exact source-change reconciliation.
- Scoped reservation search and command-center arrivals/departures/pending/groups/cancellation/no-show/modification/conflict/waitlist/exception views. Scope filtering precedes pagination.
- Encrypted durable Payment collection/readback jobs, lease fencing, exact policy/quote/payer binding, lost-response recovery, session-revocation recovery, current remedy restrictions, fresh backing checks and late-collection refund-review obligations.
- Protective cancellation under organisation restriction without expanding other authority; immutable security/audit history and transactional reservation events; real broker outage/restart/redelivery integration tests.

## Database and contracts

Goose `00008_canonical_reservations.sql` adds 25 reservation/waitlist/supporting tables. The first checkpoint catalogue contained 147 implemented tables and 150 explicit future-table decisions; the overbooking milestone below records the updated catalogue. Tenant/property composite references, UUIDv7 IDs, immutable accepted terms and histories, monotonic versions, financial-credit conservation and transactional root-history/outbox guards are enforced. Runtime role tests execute the actual least-privilege grant script against an isolated disposable database/role.

`sql/queries/reservations.sql` and generated SQLC output provide canonical root/source/locking/expiry queries alongside typed domain repositories. Normal `sqlc diff` verifies drift; a temporary-output atomic replacement was used for generation around Windows mapped-file write restrictions.

The first pushed checkpoint (`1d8558cd00e0a8b72d4276559e1cc18c892c9485`) generated **319 routes, 246 typed schemas, 43 Business event schemas**. Its follow-up adds waitlist offer rejection and lifecycle events, bringing that follow-up catalogue to **320 routes, 248 typed schemas, 44 Business event schemas**; the overbooking milestone below records the current totals. Only registered executable routes are documented. OpenAPI, route manifest and the existing `03 Business Operations` Postman collection are generated together; example schemas are validated. RMS/PMS/CRM folders reserve the requested handoff structure and are not represented as completed APIs. Public base remains `/api/v1/business`; source status is signed internal-only. See `BUSINESS_OPERATIONS_CANONICAL_RESERVATIONS.md` for transaction, source and financial contracts.

Payment's existing receipt has two additive fields, generated into its contracts: `remedy_version` and `remedy_review_required`. Real Payment repository tests prove successful original collection remains restricted after refund. No collection, refund, ledger or provider authority was moved into Business.

## Executed evidence

The earlier full Business module checkpoint passed **167 tests, zero failure/skip** with real disposable PostgreSQL and embedded disk-backed NATS. Subsequent targeted suites cover late collection after expiry, lost collection after session revocation, refunded backing denial, command-center scope/pagination, restricted protective exit and reviewed source amendment binding. The later full-module regression passed **172 tests, zero failure/skip** in 502.985 seconds. The final financial-projection change additionally passed all **8 targeted integration tests** in 52.271 seconds, including split-root remedy invalidation. The earlier full run predates that last projection change; exact-SHA Linux CI must provide final full/race certification.

Commands use `CF_TEST_POSTGRES_URL` pointing at a verified task-owned disposable PostgreSQL instance and `CF_REQUIRE_INTEGRATION=1`. No staging/production data or real customer information is used. Provider fixtures are explicitly disposable signed contract fixtures, not live provider certification.

Additional executed checks: `go vet ./...` and `staticcheck ./...` passed in Business and Payment; Gateway `go test ./... -count=1` passed; normal Business `sqlc diff`, architecture/ownership checks, generated contract/example validation, event/catalogue drift and scoped diff checks passed. Gitleaks scanned both changed services with no leak. Govulncheck found zero reachable vulnerabilities and zero vulnerable imported packages in each service; three non-called module advisories remain reported by the scanner.

Payment's full local module run recorded **175 passed and 1 failed**. `TestActualOwnerFinancialCoreConsumer` invokes a Go race subprocess and failed because this Windows host lacks CGO (`go: -race requires cgo`); this is an environment limitation, not a passing test. The unchanged test must execute in Linux CI. The focused real PostgreSQL Payment receipt/refund/privacy suite passed (2 tests, 5.270 seconds). Live-provider-tagged suites were not executed.

Read-only GitHub environment inventory returned no configured environments and no deployment workflow was found (only the existing Images publication workflow). This is evidence of missing repository deployment readiness, not proof about live host database availability.

## Outstanding Section 9 work

1. Complete customer-recovery composition through Section 11. Overbooking policy/permit acceptance, actual excess-demand accounting, cancellation, physical reconciliation and signed channel safety pause are now executable/tested; this does not certify the unimplemented CRM.
2. Quantity-level fragmentation for partial cancellation/split; current tested operations transfer whole accepted room lines.
3. In-house extension/early departure, room assignment/room-level arrival and completion lifecycle, folio and operational outcomes through Section 10.
4. Corporate contract and canonical guest identity validation through Section 11; corporate bindings currently fail closed.
5. Release-triggered waitlist offering and Notification-owned guest delivery workflow. Versioned offer rejection and atomic waitlist lifecycle audit/outbox events are implemented in the follow-up.
6. Additional operational reconciliation coverage. Cross-source merge guards now forbid combining independent external booking/connection identities while preserving legitimate split-family merges.
7. Actual authorised Business-to-Admin refund handoff and refund outcome references; Payment's existing submission requires Admin identity. No fake refund success is supplied.
8. Live Marketplace/OTA/Payment-provider integration and authorised automated staging deployment certification. Disposable tests do not certify these systems.

RMS (Section 8), PMS (Section 10) and CRM/loyalty/recovery (Section 11) remain unimplemented in this checkpoint. Their matrix rows retain FAIL/PARTIAL accurately. The broader assignment remains active.

## CI and deployment

The milestone must be committed/pushed exclusively to `origin/staging` and every required workflow checked for that exact SHA. Local Windows tests do not substitute for Linux race/image/container checks. Images publication alone is not deployment. No preauthorised live staging deployment path or credentials have been established by this checkpoint; live deployment remains unverified.


## Follow-up validation and CI repair

The first checkpoint's Images run `41287808897`, Business image job `124792582869`, failed at the real container migration/recovery smoke step: `unexpected migration version 8`. The image migrated successfully to 8; the smoke script still hard-coded 7. The script now derives the required version from the checked-out migration filenames, fails on missing/invalid source, and compares the actual image version to it. This preserves the migration check and catches stale images. The corrected smoke program builds locally; actual Docker execution remains a Linux CI check.

Follow-up integration evidence: both waitlist tests passed in 9.238 seconds, including concurrent offer rejection (one accepted/one conflict), preserved waiting request, released physical stock and exactly one audit/event transition. Three source/split/financial tests passed in 10.369 seconds, including denial of a merge across independent provider bookings and conservation of legitimate split/merge. No migration already pushed in the first checkpoint was edited.

Architecture structure, Contracts and Security passed on `1d8558c`. Backend run `41287808918` completed successfully, including all service lint/race tests and process E2E jobs. This also executes the Payment cross-service race consumer that could not run on the local Windows host. Images remained failed on that earlier SHA until the correction is pushed. Required workflows must be checked again for the follow-up SHA, not inferred from these earlier checks.


## Overbooking implementation milestone (in progress)

Previous exact pushed SHA `a07a562de324808fc89fea2571200e0347ffd91a` has all five required workflows green: Backend `41288459031`, Images `41288459073`, Contracts `41288459005`, Security `41288458931`, Architecture `41288458953`. The image migration/recovery correction passed its actual Docker workflow. This is repository/container certification, not live staging deployment.

Goose `00009_reservation_overbooking.sql` implements separate excess-demand accounting without changing physical capacity, one active pool policy, immutable accepted policy/permit references, a conversion/cancellation journal and property/room/date/quantity integrity guards. Previously pushed migrations were not modified. New policy/quote/accept/reconcile handlers require the live scope, MFA, exact independent approval and real capacity/financial/risk prerequisites. Expired policy authority cannot accept new exposure; mitigation can reduce an existing accepted exposure. Read-only reservation detail identifies outstanding excess nights. A necessary physical-assignment guard is available for the forthcoming PMS. Customer recovery remains a real open case; no downstream operation or refund success is fabricated.

Local evidence: fresh/repeated Goose migration/version **9** passed in 5.635 seconds. Overbooking policy math/security and reservation/reconciliation tests passed; concurrent requests produced one acceptance and one quota conflict, stale MFA and mismatched approval were denied, cancellation conserved original stock, expired policy was denied, and journal/counter mutation attempts failed. The full Business module run passed **178 tests, zero failure/skip**, in 214.190 seconds. A subsequently added signed-channel publication→overbook→safety-pause/readback test passed in 8.491 seconds; it was not part of that earlier full count. Exact-SHA Linux CI must certify the combined final source.

Current generated contracts: **325 routes, 254 typed schemas, 45 Business event schemas**; catalogue **148 tables, 150 explicit future decisions**. Normal SQLC drift, contract/example/event/catalogue drift, vet, staticcheck, ownership checks and service secret scan passed locally. SQLC generation used a verified isolated temporary output and atomic replacement to avoid Windows mapped-file write restrictions.

Section 9 remains PARTIAL for quantity fragmentation, release-triggered waitlist automation, guest/corporate/notification composition and PMS-owned operations. Sections 8, 10 and 11 remain unimplemented; these are open implementation requirements, not external blockers. Live service/provider and staging deployment claims remain unverified.

Canonical guest IDs fail closed until the actual Section 11 ownership contract is composed; callers may supply only typed source references in the interim. This prevents arbitrary/other-tenant canonical guest UUIDs from being persisted as trusted relationships. Corporate canonical bindings retain the same explicit missing-composition boundary.
