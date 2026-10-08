# Internal API Rules

## Routing

Client/public APIs and service-to-service APIs are separated:

- `/api/v1/*` — client/public surface as appropriate.
- `/internal/v1/*` — authenticated service-to-service surface.

## User authentication

There is no central identity service.

Each user-domain service owns and authenticates its own accounts. Domain-issued tokens identify their issuer/domain and subject. The API Gateway validates only configured trusted issuers/signing material and forwards normalized actor context.

A user in one domain is not implicitly an account in another domain.

## Service authentication

Every internal request must authenticate the calling service and propagate:
- request_id
- trace_id
- calling_service
- actor_id when acting for a user
- actor_domain / token issuer when acting for a user
- organization_id when relevant

Knowing an internal URL is never sufficient authorization.

## Data access

A service may know another service's IDs and contracts, but never depend on or access its database schema.

## Synchronous vs asynchronous

Use internal APIs when an immediate authoritative answer is required. Use durable events for asynchronous projection/update flows.
