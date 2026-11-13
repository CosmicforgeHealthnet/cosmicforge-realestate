# marketplace-service

Type: **user-domain service**  
Database authority: **marketplace_db**

Unified Property Marketplace.

## Account and authentication ownership

This service owns the accounts used by its product/domain, including the domain's user records, credentials, sessions, devices, roles and permissions. There is no central Real Estate identity microservice.

Reusable cryptographic/auth/session primitives live under `shared/go/`, but persistence and account authority stay inside this service and `marketplace_db`.

## Non-negotiable boundaries

- This service is the only application service allowed to read/write `marketplace_db`.
- It must never query another service's database.
- Immediate cross-service needs use authenticated `/internal/v1/*` APIs.
- Durable asynchronous propagation uses versioned events over NATS JetStream with an outbox where state and event publication must be atomic.
- Public/client routes and internal service routes remain separate.
- Service-to-service calls carry calling service identity, request ID, trace ID, and actor/domain context where applicable.
- Deployment packaging uses Docker; Docker is not required for local development.

## Implemented capability (narrow, Business supply receiver)

This service currently implements only the Marketplace-owned receiving side
of the Business Operations supply contract `business-supply.v1`, as consumed
by `business-operations-service/internal/distribution.MarketplaceClient`:

| Method | Path | Behaviour |
|---|---|---|
| POST | `/internal/v1/business-supply/connections/validate` | Registers/re-validates a first-party seller connection bound to the signed organisation, property and provider account |
| POST | `/internal/v1/business-supply/commands` | Durable exactly-once acceptance (Idempotency-Key), canonical payload-hash verification, monotonic canonical version, listing projection update |
| GET | `/internal/v1/business-supply/commands/{key}` | Authoritative readback; unknown commands are 404 |
| GET | `/internal/v1/business-supply/connections/{id}/events` | Cursor feed of Marketplace customer events for Business intake |
| POST | `/internal/v1/business-supply/connections/{id}/mapping-inspection` | Mapping proof for property/product/rate objects |
| GET | `/internal/v1/business-supply/connections/{id}/listing` | Storefront projection readback |

Contract: `contracts/internal/openapi/marketplace-service.yaml`. Database:
`marketplace_db` migration `00001_business_supply.sql`. Tests:
`tests/integration/supply_test.go` (real PostgreSQL) and
`tests/e2e/marketplace_supply_test.go` (real migrate + api processes).

Not yet implemented (requires Marketplace consumer product functionality —
customer accounts, discovery, checkout): producers of customer reservation
events into `supply_events`, verified-stay reviews and funnel observations.
Business intake of these remains fail-closed until they exist.
