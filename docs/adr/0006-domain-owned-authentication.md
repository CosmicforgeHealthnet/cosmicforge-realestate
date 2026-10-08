# ADR-0006: Domain-Owned Authentication

**Status:** Accepted

There is no central Real Estate identity service.

Each user-domain service owns its accounts, credentials, sessions, roles and permissions.

Common cryptographic and security implementation is shared through reusable Go packages only.

User tokens are domain-issued asymmetrically signed JWTs. The API Gateway validates configured trusted issuers.

Service-to-service authentication uses separate service identities/tokens.
