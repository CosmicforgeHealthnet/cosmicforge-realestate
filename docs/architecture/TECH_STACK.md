# Technology Stack

## Backend
- Go
- Chi
- pgx
- sqlc
- Goose
- OpenAPI 3.1

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
- PostgreSQL: authoritative transactional databases
- Redis: cache, rate limiting, ephemeral coordination where justified
- NATS JetStream: durable asynchronous events/jobs
- Firebase: FCM, Crashlytics, Analytics, App Distribution
- CF Meet: messaging, calling, video, privacy relay
- Paystack: payment rail via payment-service only
- S3-compatible object storage: files/media

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
