# Docker deployment

Docker is used for deployment only; it is not required for local development.

| Path | Purpose |
|---|---|
| `staging/docker-compose.yml` | Staging stack (`APP_ENV=staging`) |
| `production/docker-compose.yml` | Production stack (`APP_ENV=production`) |
| `*/.env.example` | Compose-level variables: image digests, `SECRETS_DIR`, broker/cache credentials |
| `service-env/*.env.example` | Per-service secret env file templates (`${SECRETS_DIR}/<service>.env`) |

## Images and promotion

- `.github/workflows/images.yml` builds one multi-stage, distroless, non-root image per
  component (`api-gateway`, `verification-service`, `media-document-service`,
  `notification-service`) on pushes to `development`, `staging` and `main`, pushes it to
  `ghcr.io/cosmicforgehealthnet/cosmicforge-realestate/<component>`, scans it with Trivy and
  publishes the digests (job summary + `image-digests` artifact).
- Compose files reference images **only by digest** (`<image>@sha256:...`); a missing digest
  fails `docker compose` immediately.
- `.github/workflows/promote.yml` (manual, `production` environment approval) verifies that
  each digest exists, tags it `production-<run>` and emits the production `.env` digest block.
  Production therefore runs the exact bytes certified in staging; nothing is rebuilt.

## Process model

Each service image contains `/app/api`, `/app/worker` and `/app/migrate`
(`docker run IMAGE /app/migrate up`). Compose runs `<svc>-migrate` once (it must succeed
before `api`/`worker` start), then the API and worker. Workers are horizontally scalable
(`FOR UPDATE SKIP LOCKED` leases). Containers run read-only, as non-root, with all
capabilities dropped and `no-new-privileges`.

## Not run by Compose

- PostgreSQL: one managed database per service with its own credentials
  (`verification_db`, `media_document_db`, `notification_db`); no cross-database access.
- S3-compatible object storage for media.
- TLS termination: the gateway is published on `127.0.0.1` for the host reverse proxy / load
  balancer.

## Secrets

Never committed. Provision `${SECRETS_DIR}/<service>.env` (mode 0600) from secrets
management using the templates in `service-env/`. Generate service keys with
`go run ./shared/go/cmd/cf-keygen -service <name> -kid <name>-YYYY-MM`.
