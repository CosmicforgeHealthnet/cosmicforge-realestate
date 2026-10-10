# payment-service

Type: **shared platform service**  
Database authority: **payment_db**

Shared payment, ledger, refund, payout, settlement and reconciliation authority.

## Non-negotiable boundaries

- This service is the only application service allowed to read/write `payment_db`.
- It must never query another service's database.
- Immediate cross-service needs use authenticated `/internal/v1/*` APIs.
- Durable asynchronous propagation uses versioned events over NATS JetStream with an outbox where state and event publication must be atomic.
- Public/client routes and internal service routes remain separate.
- Service-to-service calls carry calling service identity, request ID, trace ID, and actor/organization context where applicable.
- Deployment packaging uses Docker; Docker is not required for local development.

## Implemented scope

Master blueprint v1.0 Sections 1–3: authenticated role wallets, immutable
double-entry accounting, seven balance views, safe intents/provider routing,
Paystack test-mode collection/customer/Titan-DVA/settlement adapters, durable
inbox/outbox and reconciliation/reference/restriction foundations. Live financial
activation is disabled. Splits, milestone release, full withdrawals/refunds and
autopay require a separate later implementation.

API: `go run ./cmd/api`; worker: `go run ./cmd/worker`; migration:
`go run ./cmd/migrate up`. Supply owned `DATABASE_URL` and standard service
signing/trusted keys; the worker also requires `NATS_URL`. Provider config may
be `[]`: registration wallets do not require Paystack. The API health defaults
to :8080 and worker health to :8081. The independent module and Docker image
build api/worker/migrate binaries.

The [implementation register](../../docs/product/payments/IMPLEMENTATION_COVERAGE.md),
[architecture/auth matrix](../../docs/product/payments/ARCHITECTURE_AND_AUTHORIZATION.md),
[schema](../../docs/product/payments/FINANCIAL_SCHEMA_AND_INVARIANTS.md),
[provider and recovery guide](../../docs/product/payments/PROVIDERS_AND_OPERATIONS.md),
[test evidence](../../docs/product/payments/TEST_EVIDENCE.md),
[external dependencies/handoff](../../docs/product/payments/EXTERNAL_DEPENDENCIES_AND_HANDOFF.md)
and [closure record](../../docs/product/payments/CLOSURE_REPORT.md) state the actual
implemented behavior and certification limits. No real sandbox, staging or
production approval is implied by local passing tests.
