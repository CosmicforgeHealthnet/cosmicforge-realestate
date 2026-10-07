# trust-safety-service

Status: scaffold only.

This service is part of the CosmicForge Real Estate monorepo. Domain ownership and public contracts must be defined before implementation.

## Rules
- Backend language: Go.
- Owns its own persistence boundary.
- Must not query another service's database directly.
- Cross-service communication uses versioned APIs and/or events.
- Money-moving operations require idempotency and durable auditability.
