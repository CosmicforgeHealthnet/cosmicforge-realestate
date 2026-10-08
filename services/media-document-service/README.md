# media-document-service

Type: **shared platform service** · Database authority: **media_document_db** ·
Module: `github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service`

Owns file storage, access and lifecycle for every domain. Raw bytes live only in
S3-compatible object storage; PostgreSQL holds metadata, access policy and lifecycle
state. Domain services own the *business relationship* to a file (which listing uses
which photo); this service owns the file itself.

## Lifecycle

```text
INITIATED -> UPLOAD_PENDING -> UPLOADED -> SCANNING -> READY | REJECTED
READY -> DELETE_PENDING -> DELETED
REJECTED -> DELETE_PENDING -> DELETED
```

A file is never usable because storage accepted an upload. `READY` requires a CLEAN
malware verdict **and** a matching size, SHA-256 and sniffed content type. Executables,
archives and type/extension mismatches are rejected.

## Classes and policy

| Class | MIME types | Default max | Classifications (default) |
|---|---|---|---|
| IMAGE | jpeg, png, webp, heic | 15 MiB | PUBLIC, PRIVATE, DOMAIN_RESTRICTED (PRIVATE) |
| VIDEO | mp4 | 200 MiB | PUBLIC, PRIVATE, DOMAIN_RESTRICTED (PRIVATE) |
| DOCUMENT | pdf, jpeg, png | 25 MiB | PRIVATE, DOMAIN_RESTRICTED, ADMIN_RESTRICTED (PRIVATE) |
| VERIFICATION_DOCUMENT | pdf, jpeg, png | 15 MiB | DOMAIN_RESTRICTED, ADMIN_RESTRICTED (ADMIN_RESTRICTED) |

Size limits are configurable (`MEDIA_MAX_BYTES_<CLASS>`). Storage keys are generated
server-side (`<domain>/<class>/<yyyy>/<mm>/<media_id>`); filenames are sanitized display
names only.

## Access

| Classification | Download allowed for |
|---|---|
| PUBLIC | any authenticated principal |
| PRIVATE | owning user (via gateway), owner domain service, admin, explicit grants |
| DOMAIN_RESTRICTED | owner domain service, admin, explicit grants (verification-service for verification documents) |
| ADMIN_RESTRICTED | admin (admin-service + admin actor), explicit service grants, verification-service for verification documents |

Authorization happens before a signed URL is issued (uploads 15 min, downloads 5 min).
Every issued URL is recorded in `download_grants`; privileged downloads and denials and
all access-policy changes are audited.

## Retention and deletion

`retention_rules`: verification documents are retained 1825 days after acceptance; rejected
files are purged after 7 days. Deletion requests during retention return 409
`RETENTION_PERIOD_ACTIVE`. The worker deletes the object first (idempotent) and only then
records `DELETED`.

## Public endpoints (via API Gateway)

`POST /api/v1/media/uploads`, `POST /api/v1/media/{id}/complete`, `GET /api/v1/media/{id}`,
`POST /api/v1/media/{id}/download-url` — owner is always the signed-in user.
Contract: `contracts/public/openapi/media.yaml`.

## Internal endpoints

`POST /internal/v1/media/uploads`, `GET /internal/v1/media?owner_*`, `GET /internal/v1/media/{id}`,
`GET /internal/v1/media/{id}/status`, `POST /internal/v1/media/{id}/complete`,
`POST /internal/v1/media/{id}/download-url`, `PUT /internal/v1/media/{id}/access-policy`,
`POST /internal/v1/media/{id}/deletion`. Contract: `contracts/internal/openapi/media-document-service.yaml`.

## Events published

`media.upload_completed.v1`, `media.scanning.v1`, `media.ready.v1`, `media.rejected.v1`,
`media.deleted.v1` (outbox → JetStream `REALESTATE_MEDIA`). Schemas: `contracts/events/media/`.

## Events consumed

None.

## External providers

- Object storage: any S3-compatible service (AWS S3, Cloudflare R2, MinIO, Spaces) via
  `internal/storage` (`CreateUploadURL`, `CreateDownloadURL`, `HeadObject`, `GetObject`,
  `DeleteObject`, `CopyObject`). Signed PUTs bind Content-Type, Content-Length and
  `x-amz-checksum-sha256`.
- Malware scanning: ClamAV `clamd` (INSTREAM) in staging/production; `dev` scanner
  (EICAR only) for development/test — refused when `APP_ENV=production`.

## Configuration

| Variable | Notes |
|---|---|
| `DATABASE_URL[_FILE]`, `DB_MAX_CONNS` | media_document_db only |
| `SERVICE_AUTH_KEY_ID`, `SERVICE_AUTH_PRIVATE_KEY[_FILE]`, `SERVICE_AUTH_TRUSTED_KEYS[_FILE]` | service identity |
| `STORAGE_ENDPOINT`, `STORAGE_PUBLIC_ENDPOINT`, `STORAGE_REGION`, `STORAGE_BUCKET`, `STORAGE_PATH_STYLE` | |
| `STORAGE_ACCESS_KEY_ID[_FILE]`, `STORAGE_SECRET_ACCESS_KEY[_FILE]` | never sent to clients |
| `MEDIA_SCANNER` (`clamav`/`dev`), `CLAMAV_ADDR` | |
| `MEDIA_UPLOAD_URL_TTL`, `MEDIA_DOWNLOAD_URL_TTL`, `MEDIA_MAX_BYTES_<CLASS>`, `MEDIA_SCAN_LEASE`, `MEDIA_MAX_SCAN_TRIES` | |
| `NATS_URL`, `NATS_SUBJECT_PREFIX`, `NATS_ENSURE_STREAM` | worker |
| `OUTBOX_POLL_INTERVAL`, `SCAN_POLL_INTERVAL`, `DELETE_POLL_INTERVAL`, `PURGE_INTERVAL` | worker |

## Migrations / tests / deployment

```bash
DATABASE_URL=... go run ./cmd/migrate up
sqlc generate -f sql/sqlc.yaml
CF_TEST_POSTGRES_URL=postgres://... go test -race ./...
```

Integration tests use real PostgreSQL, an in-process S3-compatible server (real signed URL
uploads/downloads) and embedded JetStream. Covered: migrations, signed upload/download,
MIME/extension/size/classification policy, checksum/size/MIME/executable/malware
rejection, completion idempotency and concurrent duplicate finalization, storage outage,
session expiry, access control matrix, privileged-download audit, policy changes,
deletion/retention/purge, scanner outage and lease recovery, bounded scan failure, public
gateway flow, event relay with broker outage and schema validation, OpenAPI drift.

Image: one distroless non-root image with `/app/api`, `/app/worker`, `/app/migrate`.
