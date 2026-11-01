# Section 8 RMS implementation evidence

Branch: staging. Foundation checkpoint: `67557e69c3177d2166d4a8d86cf52ac1da19486a`; its Backend, Images, Contracts, Security and Architecture workflows all succeeded. This report covers the next observation/forecast checkpoint; Section 8 remains **PARTIAL**.

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

Local Docker and race execution are unavailable; those require the exact-SHA Linux workflows. Their result must be recorded after this checkpoint is pushed. No deployment certification follows from image publication.

## Outstanding Section 8 implementation

Versioned RMS policies, governed pricing recommendations/decisions, simulations, approved application/rollback, all four operating modes and worker automation, broader feature/cohort/aggregation support, performance monitoring and channel redistribution remain unfinished at this checkpoint. External market/search inputs and live staging rollout also need actual configured owning contracts/infrastructure. Sections 10–11 remain unfinished. No scaffold is certified as a completed workflow.
