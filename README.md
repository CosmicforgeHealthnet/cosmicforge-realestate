# CosmicForge Real Estate

CosmicForge Real Estate is a microservice-based real-estate platform built around seven distinct product domains and four shared platform services.

## Locked backend architecture

### Seven user-domain services
1. `marketplace-service`
2. `individual-owner-service`
3. `business-operations-service`
4. `property-manager-service`
5. `artisan-service`
6. `professional-services-service`
7. `admin-service`

Each user-domain service owns its own accounts, credentials, sessions, roles and permissions inside its own database. There is **no central identity service**.

### Four shared platform services
8. `verification-service`
9. `payment-service`
10. `notification-service`
11. `media-document-service`

### Edge
- `api-gateway` — no business database.

There are **11 database-owning microservices + 1 API gateway = 12 deployable backend components**.

## Core rules

- Every service owns its own PostgreSQL database and credentials.
- No service may read or write another service's database.
- Each of the seven user-domain services owns its own account/authentication data.
- Shared Go packages provide reusable auth/security primitives without creating shared identity persistence.
- UUIDv7 is the canonical persistent entity ID.
- Synchronous cross-service communication uses authenticated internal REST APIs.
- Durable asynchronous propagation uses NATS JetStream and transactional outbox patterns.
- Marketplace aggregates searchable/public projections from originating domains; it never becomes the source of truth for those records.
- CF Meet owns human-to-human messaging, voice, video, virtual viewing and privacy relay.
- Paystack is reachable only through `payment-service`.
- Firebase is used for FCM, Crashlytics, Analytics and App Distribution.
- Docker is used for deployment, not required for local development.
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

## Backend readiness

The accepted pre-coding architecture and implementation rules are indexed in `docs/architecture/README.md`. The master baseline is `docs/architecture/BACKEND_FOUNDATION_BLUEPRINT.md`.

## Branch flow

`feature/* -> development -> staging -> main`
