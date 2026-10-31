# api-gateway

Type: **edge component** (Go, Chi) · Module: `github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway`

The single external entry point. It owns **no business database** and is **not an
account authority**: it validates access tokens issued by the seven user-domain
services, normalizes the actor, applies edge policy and forwards requests.

## Responsibilities

| Concern | Behaviour |
|---|---|
| Routing | Longest-prefix registry (`internal/routing`). `/api/v1/verification/*` → verification-service, `/api/v1/media/*` → media-document-service, `/api/v1/notifications/*` → notification-service, `/webhooks/v1/<provider>/*` → configured service. Domain prefixes `/api/v1/<domain>/*` are reserved and mounted only when that domain's upstream URL is configured. |
| Internal isolation | `/internal/*` always returns `404 ROUTE_NOT_FOUND`. Non-canonical paths (`..`, `//`, `%2e`, `%2f`, `%5c`) are rejected before routing. |
| User tokens | EdDSA JWT (`typ: at+jwt`) from configured trusted issuers only; signature by `kid`, audience, expiry (30 s skew), lifetime ≤ 60 min. Domain is taken from the trusted-issuer config, never from a claim. |
| Cross-domain | Domain routes accept only their own domain's tokens (403 otherwise); shared-service routes accept any trusted domain. Authorization stays with the receiving service. |
| Actor propagation | Client-supplied identity headers are stripped. The gateway signs a short-lived service JWT (`X-Service-Authorization`, `iss=api-gateway`, `aud=<service>`) carrying `actor_domain`, `actor_id`, `organization_id`, `original_request_id`. Shared services never receive the user's token; domain services also receive it for re-verification. |
| Rate limiting | Redis GCRA (shared across replicas, Redis server clock). Categories: `public` (IP), `authenticated` (actor), `auth-sensitive` (IP, fail-closed), `upload-initiation` (actor), `webhook` (provider+IP). Redis outage: fail-closed categories return 503; others degrade to a per-process limiter. |
| Edge security | CORS allow-list (no wildcard outside development), security headers (HSTS in staging/production), body limits (1 MiB default, 256 KiB webhooks), trusted-proxy aware client IP, sanitized errors, upstream timeouts → 503. |
| Observability | slog JSON logs with request/trace IDs, OpenTelemetry spans, W3C `traceparent` and `X-Request-ID` propagation. |

## Endpoints

`GET /health`, `GET /ready`, `GET /version` — see `contracts/public/openapi/api-gateway.yaml`.
Proxied operations are documented in each service's own contract.

## Configuration

| Variable | Required | Description |
|---|---|---|
| `APP_ENV` | | development / test / staging / production |
| `HTTP_ADDR` | | default `:8080` |
| `REDIS_ADDR`, `REDIS_PASSWORD[_FILE]`, `REDIS_DB`, `REDIS_TLS` | ✓ addr | rate-limit store |
| `GATEWAY_TRUSTED_ISSUERS[_FILE]` | ✓ | JSON: `[{"issuer","domain","keys":[{"kid","public_key"}]}]` or `jwks_url` per issuer |
| `GATEWAY_TOKEN_AUDIENCE` | | default `cosmicforge-realestate-api` |
| `SERVICE_AUTH_KEY_ID`, `SERVICE_AUTH_PRIVATE_KEY[_FILE]` | ✓ | gateway service identity (Ed25519) |
| `GATEWAY_UPSTREAM_<SERVICE>_URL` | per service | e.g. `GATEWAY_UPSTREAM_VERIFICATION_SERVICE_URL=http://verification-service:8080` |
| `GATEWAY_WEBHOOK_ROUTES` | | `provider=service,...` (shared/payment services; reserved `business-channels=business-operations-service` supply callback only) |
| `GATEWAY_RATE_LIMIT_{PUBLIC,AUTHENTICATED,AUTH_SENSITIVE,UPLOAD_INITIATION,WEBHOOK}` | | e.g. `120/1m`, `10/1m:burst=5` |
| `GATEWAY_CORS_ALLOWED_ORIGINS` | | comma-separated https origins |
| `GATEWAY_TRUSTED_PROXIES` | | CIDRs whose `X-Forwarded-For` is honoured |
| `GATEWAY_MAX_BODY_BYTES`, `GATEWAY_UPSTREAM_TIMEOUT` | | defaults 1 MiB / 30 s |
| `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_INSECURE`, `OTEL_TRACES_SAMPLER_RATIO` | | telemetry |

Startup fails fast on missing/invalid configuration; secret values are never printed.

## Tests

```bash
cd gateway/api-gateway
CF_TEST_REDIS_ADDR=127.0.0.1:6379 go test -race ./...   # real Redis optional; miniredis otherwise
```

Covers route matrix, valid/invalid-signature/wrong-audience/unknown-issuer/expired tokens,
cross-domain misuse, internal-route isolation and path tricks, request-context propagation,
distributed rate limits, Redis outage, upstream timeout/down, body limits, CORS, security
headers, health/ready/version, graceful shutdown and OpenAPI drift.

## Deployment

`gateway/api-gateway/Dockerfile` (build context = repo root): multi-stage, static binary,
distroless non-root runtime, `HEALTHCHECK` via `/app/api healthcheck`, build metadata via
`VERSION`, `COMMIT`, `BUILD_TIME` build args. Deployed by `infrastructure/docker/{staging,production}`.
