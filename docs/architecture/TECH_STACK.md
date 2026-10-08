# Technology Stack

## Backend
- Go
- Chi
- pgx
- sqlc
- Goose
- OpenAPI 3.1
- Domain-owned authentication using shared Go security/auth libraries

## Mobile
- Flutter / Dart
- Riverpod
- GoRouter
- Dio
- Freezed / json_serializable where useful

## Web
- Next.js
- React
- TypeScript
- TanStack Query
- React Hook Form
- Zod
- Tailwind CSS

## Platform
- PostgreSQL: authoritative transactional databases, one database per service
- Redis: cache, rate limiting, ephemeral coordination where justified
- NATS JetStream: durable asynchronous events/jobs
- Firebase: FCM, Crashlytics, Analytics, App Distribution
- CF Meet: messaging, calling, video, privacy relay
- Paystack: payment rail via payment-service only
- S3-compatible object storage: files/media

## Authentication model
- No central identity microservice
- Each of the seven user-domain services owns its own accounts, credentials, sessions, roles and permissions
- Shared Go packages provide JWT, password hashing, session, permissions and security primitives
- API Gateway validates configured domain-issued tokens
- Internal service authentication is separate from end-user authentication

## Deployment
- Docker
- Docker Compose
- GitHub Container Registry
- Nginx
- Terraform
- Cloudflare
- GitHub Actions

Docker is for deployment; local development does not require Docker.

## Observability
- OpenTelemetry
- Prometheus
- Grafana
- Loki
