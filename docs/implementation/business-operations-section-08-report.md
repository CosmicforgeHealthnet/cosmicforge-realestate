# Section 8 RMS implementation evidence

Branch: staging. Foundation checkpoint: `67557e69c3177d2166d4a8d86cf52ac1da19486a`; its Backend, Images, Contracts, Security and Architecture workflows all succeeded. Observation/forecast checkpoint: `4560806a1a42003d00f81f9e1db102c543c9159c`. This report also records subsequent RMS governance work; Section 8 remains **PARTIAL**.

## Implemented and executed

- Goose 00010 adds immutable demand snapshots, booking-pace snapshots, model registry and reproducible forecast runs. UUIDv7 and tenant/property/pool foreign keys bind retained inputs. Model/source hashes, civil-date horizon, source timestamp and model version are checked on insertion. The migration also protects accepted accommodation attribution from mutation.
- Pool-scoped capture reads actual configured calendar rows and canonical committed reservation nights. Missing historical calendar rows remain absent. Accepted accommodation is allocated using frozen nightly weights; unknown attribution remains null. No forecasts manufacture historical bookings, verified capacity or financial outcomes.
- Same-weekday capacity-weighted baseline uses retained historical observations ending at the source's local date. Projected nights use thousandths. Coverage measures observed days, explicitly not calibrated probability. Insufficient history returns an explicit status and null estimates. Revisions retain their predecessor and source/model configuration.
- Pickup compares retained observations in the same scope/window. Cancellations can produce negative pickup. Booked occupancy, contractual ADR and contractual RevPAR use documented physical-capacity denominators. Realized stay/financial revenue, conversion and contribution remain unknown until authoritative inputs exist.
- Eight authenticated routes: GET/POST `/revenue/demand-snapshots`, GET `/revenue/demand-snapshots/{id}`, GET `/revenue/demand-snapshots/{id}/pace`, GET `/revenue/metrics`, GET/POST `/revenue/forecasts`, GET `/revenue/forecasts/{id}`, all beneath `/api/v1/business`. Property scope is validated before pagination. Writes use idempotent transactions with audit/outbox.
- OpenAPI/Postman and generated SQLC queries match the handlers. Snapshot/model/forecast source records are immutable and runtime writes are restricted. The contract generator now atomically replaces outputs to avoid Windows mapped-file truncation errors.

## Test evidence

Disposable native PostgreSQL 18 on task-owned loopback port 55447; isolated databases and embedded real JetStream. No live staging connection was used.

| Executed check | Result |
|---|---|
| Business `go test -json -p2 ./...`, integration required | 188 passed, zero failed/skipped; process exit 0 |
| Actual canonical history forecast | Passed; four real API bookings across eight comparable weekdays yield 500 thousandths of a night, without rounding to a fictitious full room |
| Retained capture/pickup/revision/idempotency/immutability | Passed; cancellation yields actual negative pickup; rates remain unchanged |
| Tenant and branch isolation | Passed for capture, detail, metrics, forecasts and list pagination |
| Fresh/repeated Goose migrations and status | Passed at version 10 |
| SQLC diff, contract/example/event/catalogue drift | Passed; 333 routes, 268 schemas, 47 event schemas, 152 implemented tables |
| Go vet and staticcheck | Passed |

Observation checkpoint exact-SHA workflows succeeded: Backend 41295475732, Images 41295475696, Contracts 41295475744, Security 41295475693, Repository structure 41295475671. Published Business image digest: `sha256:611e7471731cd7922631ce254be036e974b544173a9069fcae31cfe049f4e871`. Linux CI supplies real container/race evidence; local Docker/CGO remain unavailable. No deployment certification follows from image publication.

## Subsequent policy/recommendation checkpoint

Goose 00011 adds immutable `rms_policies`, `pricing_decisions`, `pricing_simulations` and version-guarded `pricing_recommendations`. SQLC policy/recommendation reads are used by executable handlers. Ten additional routes provide policy history/save/detail, recommendation create/list/detail/decision/decision history, simulation creation and simulation listing. Current generated totals: 343 routes, 279 schemas, 49 event schemas and 156 implemented tables.

Implemented workflows: independent policy activation; immediate versioned pause; explicit bounded occupancy-feedback or staff-amount proposals; immutable model/source/rate/calendar snapshots; submit; exact independent rate approval; fresh authority/version checks before application; concurrent/double-submission protection; immutable rejection/override/expiry; exact compensating rollback; retained fixed-demand projected simulation; existing scheduler expiry with service identity. See [RMS architecture](../architecture/BUSINESS_OPERATIONS_RMS.md) for approval payloads, hard constraints and truth boundaries.

Targeted real PostgreSQL tests passed for simulation → independent approval → application → compensating rollback, immutable proposal/decisions, stale rate rejection without consuming approval, immediate pause and idempotent service-worker expiry. The complete Business regression passed **192 tests, zero failures/skips**, exit 0. Integration package: 235.487 seconds. The final focused rerun passed five tests, including fresh/repeated Goose 11, concurrent application (one success/one conflict), unknown mandatory inputs, absolute policy bounds and compensating rollback. Exact-SHA CI must additionally certify the pushed policy checkpoint. Local `go mod tidy -diff`, `go vet` and staticcheck passed; Gitleaks found no leaks; govulncheck found zero reachable/imported-package vulnerabilities (three non-called module advisories); generated OpenAPI/Postman examples, event/catalogue drift, SQLC diff and architecture checks passed. Final counts and workflow evidence are recorded in the staging certification follow-up.

## Outstanding Section 8 implementation

APPROVAL_AUTOMATION and AUTOPILOT_WITH_LIMITS execution/activation, automatic source/model/recommendation workers, complex derived-rate scenarios, LOS/promotion/allocation recommendations, broader feature/cohort/branch/portfolio aggregation, sourced channel-cost simulations and outcome/error/drift monitoring remain unfinished. Automatic activation explicitly fails closed; only paused configuration retention is supported for those modes. External market/search inputs and live staging rollout also need actual configured owning contracts/infrastructure. Section 9 still needs remaining quantity-fragmentation/in-house/CRM/refund-handoff/release-triggered waitlist functionality as recorded in its report. Sections 10–11 remain unfinished. No scaffold is certified as a completed workflow.
