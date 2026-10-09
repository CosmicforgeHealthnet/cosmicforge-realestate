# ADR-0011: Individual Owner Public API Prefix `/api/v1/owner/*`

**Status:** Accepted (implementation instruction, 2026-10-09)

The API Gateway originally reserved `/api/v1/individual-owner/` for the Individual Owner domain as an
unmounted placeholder. The Individual Owner backend implementation instruction mandates the public
contract `/api/v1/owner/*`.

Decision:

- The gateway mounts the individual-owner-service public surface at `/api/v1/owner/`.
- The `/api/v1/owner/auth/` sub-tree uses the `auth-sensitive` rate-limit category.
- Only owner-domain tokens are accepted on this prefix (cross-domain tokens are rejected at the edge).

No client depended on the placeholder: it was mounted only when an upstream URL was configured and
no individual-owner-service existed. Other domains keep their reserved prefixes.
