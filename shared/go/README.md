# shared/go — CosmicForge Real Estate Go foundation

Module: `github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go`

Reusable platform primitives for every Go component. This module **owns no
persistence**: reusable PostgreSQL helpers (idempotency, outbox, audit,
processed events) operate on tables that live in the *consuming service's own
database*, created by that service's Goose migrations from the canonical
`SchemaSQL` constants. There are no shared users, accounts or sessions.

| Package (dir) | Import name | Purpose |
|---|---|---|
| `ids` | `ids` | UUIDv7 generation (monotonic per process), parsing, JSON/SQL |
| `money` | `money` | `amount_minor` int64 + ISO 4217 currency; overflow/mixed-currency safe |
| `errors` | `apperrors` | Platform error envelope, stable codes, status mapping, outcome classes |
| `requestcontext` | `requestcontext` | request_id / trace_id / calling_service / actor / organization / idempotency_key |
| `http` | `httpx` | JSON envelopes, strict decoding, recoverer, access log, graceful server, internal client |
| `validation` | `validation` | Field-level validation → `INVALID_REQUEST` details |
| `jwt` | `jwt` | EdDSA-only JWS/JWT core, kid key resolution, JWKS, key rotation |
| `auth` | `auth` | User access tokens: issuer, trusted-issuer verifier, actor normalisation |
| `passwords` | `passwords` | Argon2id (PHC encoded, versioned params, rehash detection, bounded concurrency) |
| `sessions` | `sessions` | Opaque rotating refresh tokens, hash-at-rest, reuse detection, revocation |
| `permissions` | `permissions` | Domain-local role → permission helpers |
| `serviceauth` | `serviceauth` | Service JWT signer/verifier, trusted registry, capability policy, middleware |
| `idempotency` | `idempotency` | DB-backed Idempotency-Key handling with lease + fencing |
| `events` | `events` | Canonical event envelope, publisher/consumer contracts, consumer de-dup |
| `events/jetstream` | `jetstream` | NATS JetStream transport (PubAck, Msg-Id de-dup, durable consumers) |
| `outbox` | `outbox` | Transactional outbox store and relay worker (SKIP LOCKED leasing) |
| `audit` | `audit` | Append-only audit events written in the business transaction |
| `database` | `database` | pgx pool, short transactions with serialization/deadlock retry |
| `migrate` | `migrate` | Goose runner for embedded migrations |
| `health` | `health` | `/health`, `/ready`, `/version` |
| `logging` | `logging` | slog JSON with request context and secret redaction |
| `observability` | `observability` | OpenTelemetry setup, HTTP middleware, vendor-neutral metrics |
| `security` | `security` | Secret wrapper, random tokens, HMAC, Ed25519 key parsing, security headers |
| `config` | `config` | Fail-fast env config loading (`*_FILE` secrets, values never echoed) |
| `pagination` | `pagination` | Cursor pagination contract |
| `fsm` | `fsm` | Declarative state-transition tables |
| `platform` | `platform` | The 12 component names and 7 user domains |
| `buildinfo` | `buildinfo` | Link-time version/commit metadata |
| `testing` | `testkit` | Ephemeral PostgreSQL databases, embedded NATS JetStream, helpers |
| `cmd/cf-keygen` | — | Ed25519 key generation for service identities / domain issuers |

## Tests

Unit tests need nothing. Integration tests need a reachable PostgreSQL (no
Docker required locally — any PostgreSQL works); NATS is embedded.

```bash
export CF_TEST_POSTGRES_URL='postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable'
go test -race ./...
```

Without `CF_TEST_POSTGRES_URL` integration tests skip; CI sets
`CF_REQUIRE_INTEGRATION=1` so a missing database fails instead of skipping.

## Rules

- Services import this module only through its module path; services never
  import each other (enforced by `scripts/ci/check-architecture.sh`).
- Delivery is at-least-once everywhere; consumers use `events.MarkProcessed`.
- Never pass raw passwords, refresh tokens, JWTs, signing keys or provider
  secrets to a logger; `logging` redacts sensitive keys as a safety net only.
