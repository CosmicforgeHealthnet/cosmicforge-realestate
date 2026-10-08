# ADR-0001: Monorepo and Service Model

**Status:** Accepted

The platform uses one monorepo with seven independent user-domain services, five shared platform services and one API Gateway.

Each database-owning service has an isolated PostgreSQL database. Cross-service database access is forbidden. Internal APIs and versioned durable events are the integration mechanisms.
