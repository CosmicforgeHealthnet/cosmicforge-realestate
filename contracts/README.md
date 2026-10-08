# Contracts

- `public/openapi/` — client-facing API contracts.
- `internal/openapi/` — authenticated service-to-service API contracts.
- `events/` — versioned durable event schemas.
- `webhooks/` — external webhook contracts.
- `schemas/` — shared schema definitions.

A service may depend on another service's contract, never on its database schema.
