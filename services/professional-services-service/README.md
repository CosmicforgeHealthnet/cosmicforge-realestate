# professional-services-service

Type: **user-domain service**  
Database authority: **professional_services_db**

Professional Property Services.

## Account and authentication ownership

This service owns the accounts used by its product/domain, including the domain's user records, credentials, sessions, devices, roles and permissions. There is no central Real Estate identity microservice.

Reusable cryptographic/auth/session primitives live under `shared/go/`, but persistence and account authority stay inside this service and `professional_services_db`.

## Non-negotiable boundaries

- This service is the only application service allowed to read/write `professional_services_db`.
- It must never query another service's database.
- Immediate cross-service needs use authenticated `/internal/v1/*` APIs.
- Durable asynchronous propagation uses versioned events over NATS JetStream with an outbox where state and event publication must be atomic.
- Public/client routes and internal service routes remain separate.
- Service-to-service calls carry calling service identity, request ID, trace ID, and actor/domain context where applicable.
- Deployment packaging uses Docker; Docker is not required for local development.
