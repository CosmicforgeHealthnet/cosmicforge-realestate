# ADR-0001: Monorepo and Service Model

**Status:** Accepted

The platform uses one monorepo with seven independent user-domain services, four shared platform services and one API Gateway.

Each database-owning service has an isolated PostgreSQL database. Cross-service database access is forbidden. Internal APIs and versioned durable events are the integration mechanisms.

There is no central identity microservice. Each user-domain service owns its own accounts, credentials, sessions, roles and permissions while reusing shared Go security/authentication libraries.
