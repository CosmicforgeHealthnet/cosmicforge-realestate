# Business Operations Service

Business Operations is the canonical authority for business-managed supply and operating state. Its database is `business_operations_db`; no other service database is an integration surface.

## Boundaries

- Business account identity belongs to this domain; there is no central Real Estate identity service. Reusable authentication cryptography and session primitives live in shared/go, while business account persistence and authority belong here and in business_operations_db. Section 3 implements the business account flows.
- Organisation, staff, portfolio, inventory, listings, reservations and operational records belong here as their Sections 3–27 workflows are implemented.
- Verification decisions remain authoritative in Verification Service. Media and document bytes remain in Media & Document Service. Payment execution and ledger state remain in Payment Service. Notification delivery remains in Notification Service. Marketplace owns customer discovery and booking journeys. Manager, artisan and professional provider profiles remain in their respective services. CF Meet remains the human messaging and call history authority.
- Synchronous integrations use authenticated internal contracts. Durable events use the shared versioned envelope and transactional outbox. This service never queries another service's database.
- Canonical public routes use `/api/v1/business/`. The retained `/api/v1/business-operations/` prefix has identical security and handler behavior. Every business request requires the Gateway's signed service identity; protected operations also re-verify the business account JWT and live session. No `/internal/v1/*` operation is currently exposed.

## Layout and ownership

`internal/` is organized by bounded context. Context directories that contain only `.gitkeep` are intentional future ownership boundaries, not implemented functionality. Shared bootstrap lives in `internal/app`, configuration in `internal/config`, and stable cross-service adapters belong under `internal/integrations` only when their contracts are implemented. Reusable platform primitives are imported from `shared/go`.

The per-context responsibility and external authority mapping is in [Business Operations bounded-context boundaries](../../docs/architecture/BUSINESS_OPERATIONS_BOUNDARIES.md).

The service owns its Goose migrations and SQLC output. Migration 00001 creates the durable outbox; 00002 adds Section 3 accounts, sessions, organisation/workforce, role/scope, approval/delegation and immutable audit storage. Table presence does not certify the corresponding workflow.

## Run locally

Copy `.env.example` to a local environment file and provide a PostgreSQL URL whose database path is exactly `/business_operations_db`. Apply migrations with `go run ./cmd/migrate up`, then run the API with `go run ./cmd/api`. Start a worker with `go run ./cmd/worker` and `NATS_URL` set; the worker relays committed outbox rows to JetStream and serves health endpoints on port 8081 by default. All processes respond to SIGINT/SIGTERM and close resources cleanly.

The API and worker use the same required database configuration. The API additionally requires the standard SERVICE_AUTH identity, BUSINESS_ACCESS signing key, BUSINESS_CODE_PEPPER (at least 32 bytes), BUSINESS_MFA AES key (32 bytes encoded as base64), key IDs and Verification/Notification service URLs. Secrets support the shared `_FILE` mechanism. Retired access public keys and MFA encryption keys can be supplied as JSON key-ID maps. See the staging service environment example. NATS is required only by the worker. Optional telemetry is enabled by `OTEL_EXPORTER_OTLP_ENDPOINT`.

## Current Section 3 routes

Implemented: registration; login; rotating refresh; logout and logout-all; session/device listing and revocation; organisation context selection; encrypted TOTP enrolment, confirmation, login challenge, step-up and removal; one-time recovery codes; organisation profile read/update; KYB create, attach evidence, submit and authoritative status refresh; capacity projection listing. The API process requires identity configuration before serving any of these routes.

KYB never approves locally. The Verification Service remains the decision authority. A registration creates a DRAFT organisation. The capacity catalogue/eligibility authority and remaining workforce/approval management workflows are still being implemented; the full Section 3 system is not yet certified.

## Checks

From this directory, run `go test ./...`, `go vet ./...`, `go build ./cmd/api ./cmd/worker ./cmd/migrate`, and `sqlc diff -f sql/sqlc.yaml`. Set `CF_REQUIRE_INTEGRATION=1` and `CF_TEST_POSTGRES_URL` to a disposable test PostgreSQL admin connection for mandatory database tests. The test kit starts a real isolated JetStream server. From the repository root, run `scripts/ci/check-architecture.sh`. SQLC generates outbox and account/session queries.

## Deployment

Build from repository root using `docker build -f services/business-operations-service/Dockerfile .`. The image contains API, worker and migration binaries and runs as distroless non-root. Staging must provision a least-privilege `business_operations_db` role and its secret independently. No production deployment or production configuration is part of this scaffold.

## Deferred scope

Section 3 remains in progress; consult its implementation report and executable tests. All domain workflows in Sections 4–27 remain deferred. Live staging database/provider/deployment certification requires the authorized operator prerequisites documented in the foundation remediation report.
