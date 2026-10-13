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
  `notification-service`, `individual-owner-service`, `payment-service`) on pushes to `development`, `staging` and `main`, pushes it to
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
  `individual_owner_db` needs the
  `btree_gist` extension: an administrator runs `CREATE EXTENSION IF NOT EXISTS btree_gist;` in
  it once, or the migration role must be allowed to create it. `/app/migrate up` refuses to start
  (exit 1, nothing applied) when it is missing, and `/app/migrate preflight` runs only that check —
  see `docs/product/individual-owner/DATABASE_PREREQUISITES.md`.
- S3-compatible object storage for media.
- TLS termination: the gateway is published on `127.0.0.1` for the host reverse proxy / load
  balancer.

## Secrets

Never committed. Provision `${SECRETS_DIR}/<service>.env` (mode 0600) from secrets
management using the templates in `service-env/`. Generate service keys with
`go run ./shared/go/cmd/cf-keygen -service <name> -kid <name>-YYYY-MM`.

## Pending Individual Owner deployment

`staging/individual-owner.pending.yml` and `production/individual-owner.pending.yml`
are prepared overlays, excluded from the active stacks. The Owner specification
requires green affected CI and a certified image before adding the service. No Owner
digest is supplied or certified by these files. Only after the relevant gates pass,
an operator can select the base Compose file and the matching pending overlay with
`-f`, supply `INDIVIDUAL_OWNER_SERVICE_DIGEST=sha256:<64 hex characters>`, and review
the rendered configuration before approved execution. Never use a floating tag.

Provision `individual_owner_db`, required extensions, distinct migration/runtime
roles, and `${SECRETS_DIR}/individual-owner-{api,worker,migrate}.env`. Use the new
`service-env/individual-owner-service.env.example` for API/worker; migration needs
only its database secret and deployment environment. Mount only the Owner secret
directory at `/run/secrets/owner`, read-only. API/worker may use separate service
key IDs under the same `individual-owner-service` identity. Trusted peer registries
must contain both public keys during rotation. Provision files readable by the
non-root container UID/group; mode 0400/0440 with correct ownership is suitable.
Windows file ACLs and deployment Unix permissions require independent review.

The overlays separate migration/API/worker, wait for migration success, publish
no Owner ports, preserve the internal backend network, use readiness probes,
bounded resources and 30-second stop grace. Gateway owns Redis rate limiting;
Owner has no new Redis dependency. Only `PAYMENT_WALLET_SERVICE_URL` can enable
the signed Financial Core adapter after the separately certified Payment stack
and approvals exist. Future domain/provider endpoints remain empty.

Before staging: retain schema backup and source migration hashes, run preflight
and populated upgrade, inspect exact image metadata/non-root execution, run process
E2E/readiness/dependency recovery and restore drills. Roll back the application by
the prior certified digest only when schema-compatible. Do not run `down-to` over
protected history; preserve 00022/00023 retention guards. Before production, obtain
staging E2E/migration/backup/rollback evidence, telemetry/alert/security approval
and applicable financial/legal authorizations. The current promotion workflow
does not yet promote Owner digests; extending that release automation remains a
release-gated task after Owner image/staging certification, not authorization here.
