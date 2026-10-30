# Business Operations Service

Business Operations is the canonical authority for business-managed supply and operating state. Its database is `business_operations_db`; no other service database is an integration surface.

## Boundaries

- Business account identity belongs to this domain; there is no central Real Estate identity service. Reusable authentication cryptography and session primitives live in shared/go, while business account persistence and authority belong here and in business_operations_db. Section 3 implements the business account flows.
- Organisation, staff, portfolio, inventory, listings, reservations and operational records belong here as their Sections 3–27 workflows are implemented.
- Verification decisions remain authoritative in Verification Service. Media and document bytes remain in Media & Document Service. Payment execution and ledger state remain in Payment Service. Notification delivery remains in Notification Service. Marketplace owns customer discovery and booking journeys. Manager, artisan and professional provider profiles remain in their respective services. CF Meet remains the human messaging and call history authority.
- Synchronous integrations use authenticated internal contracts. Durable events use the shared versioned envelope and transactional outbox. This service never queries another service's database.
- The API Gateway reserves `/api/v1/business-operations/`; it only mounts that prefix when its upstream is configured. The service currently exposes only `/health`, `/ready` and `/version`; business routes return a not-found envelope until implemented. No `/internal/v1/*` operation is currently exposed.

## Layout and ownership

`internal/` is organized by bounded context. Context directories that contain only `.gitkeep` are intentional future ownership boundaries, not implemented functionality. Shared bootstrap lives in `internal/app`, configuration in `internal/config`, and stable cross-service adapters belong under `internal/integrations` only when their contracts are implemented. Reusable platform primitives are imported from `shared/go`.

The per-context responsibility and external authority mapping is in [Business Operations bounded-context boundaries](../../docs/architecture/BUSINESS_OPERATIONS_BOUNDARIES.md).

The service owns its Goose migrations and SQLC output. The initial migration creates only the durable outbox table. No business table is added before its domain section.

## Run locally

Copy `.env.example` to a local environment file and provide a PostgreSQL URL whose database path is exactly `/business_operations_db`. Apply migrations with `go run ./cmd/migrate up`, then run the API with `go run ./cmd/api`. Start a worker with `go run ./cmd/worker` and `NATS_URL` set; the worker relays committed outbox rows to JetStream and serves health endpoints on port 8081 by default. All processes respond to SIGINT/SIGTERM and close resources cleanly.

The API and worker use the same required database configuration. NATS is required only by the worker, so broker availability does not prevent the API from starting. Optional telemetry is enabled by `OTEL_EXPORTER_OTLP_ENDPOINT`.

## Checks

From this directory, run `go test ./...`, `go vet ./...`, `go build ./cmd/api ./cmd/worker ./cmd/migrate`, and `sqlc diff -f sql/sqlc.yaml`. From the repository root, run `scripts/ci/check-architecture.sh`. SQLC currently generates the foundational outbox backlog query; domain queries are deferred until their sections are implemented.

## Deployment

Build from repository root using `docker build -f services/business-operations-service/Dockerfile .`. The image contains API, worker and migration binaries and runs as distroless non-root. Staging must provision a least-privilege `business_operations_db` role and its secret independently. No production deployment or production configuration is part of this scaffold.

## Deferred scope

Account, organisation, RBAC and approvals are Section 3 work. All domain workflows in the remaining Sections 4–27 are deferred. Package presence and health endpoints do not certify any business feature.
