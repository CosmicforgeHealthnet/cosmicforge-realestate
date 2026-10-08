# CosmicForge Real Estate

CosmicForge Real Estate is a microservice-based real-estate platform built around seven distinct product domains and five shared platform services.

## Locked backend architecture

### Seven user-domain services
1. `marketplace-service`
2. `individual-owner-service`
3. `business-operations-service`
4. `property-manager-service`
5. `artisan-service`
6. `professional-services-service`
7. `admin-service`

### Five shared platform services
8. `identity-service`
9. `verification-service`
10. `payment-service`
11. `notification-service`
12. `media-document-service`

### Edge
- `api-gateway` — no business database.

There are **12 database-owning microservices + 1 API gateway**.

## Core rules

- Every service owns its own PostgreSQL database and credentials.
- No service may read or write another service's database.
- Synchronous cross-service communication uses authenticated internal APIs.
- Durable asynchronous propagation uses NATS JetStream and transactional outbox patterns.
- Marketplace aggregates searchable/public projections from the originating domains; it never becomes the source of truth for those originating records.
- CF Meet owns human-to-human messaging, voice, video, virtual viewing and privacy relay.
- Paystack is reachable only through `payment-service`.
- Firebase is used for FCM, Crashlytics, Analytics and App Distribution; it is not the primary business database.
- Docker is used for deployment, not required for local development.
- Staging and production Docker deployments are separated.
- Terraform provisions infrastructure; Docker Compose deploys application containers initially.
- Kubernetes is intentionally not part of the initial architecture.

## Technology stack

**Backend:** Go, Chi, pgx, sqlc, Goose, OpenAPI 3.1  
**Mobile:** Flutter/Dart, Riverpod, GoRouter, Dio  
**Web:** Next.js/React/TypeScript, TanStack Query, React Hook Form, Zod, Tailwind CSS  
**Data:** PostgreSQL, Redis  
**Events:** NATS JetStream + transactional outbox  
**Communications:** CF Meet  
**Payments:** Paystack through Payment Service  
**Notifications/mobile platform:** Firebase  
**Files:** S3-compatible object storage through Media & Document Service  
**Deployment:** Docker, Docker Compose, GHCR, Nginx  
**Infrastructure:** Terraform, Cloudflare  
**CI/CD:** GitHub Actions  
**Observability:** OpenTelemetry, Prometheus, Grafana, Loki

## Branch flow

`feature/* -> development -> staging -> main`
