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
