# Authentication and Account Ownership

## Decision

CosmicForge Real Estate has no central identity microservice.

Each of the seven user-domain services owns its own account system and stores account/authentication state in its own database:

- marketplace-service -> marketplace_db
- individual-owner-service -> individual_owner_db
- business-operations-service -> business_operations_db
- property-manager-service -> property_manager_db
- artisan-service -> artisan_db
- professional-services-service -> professional_services_db
- admin-service -> admin_db

## Per-domain account capability

Each domain may implement, as required:
- registration
- login/logout
- password credentials
- email/phone verification
- password recovery
- sessions and refresh tokens
- device/session management
- MFA
- roles and permissions
- organization/staff memberships where the domain requires them
- security/audit events

## Shared implementation, separate authority

Security primitives are reused through `shared/go/`, including:
- auth
- jwt
- passwords
- sessions
- permissions
- security
- serviceauth

Shared packages contain reusable implementation only. They must not introduce shared user tables or shared account persistence.

## Gateway trust model

Each domain is an authentication issuer. Tokens carry an issuer/domain and domain-local subject.

The API Gateway:
1. accepts only configured trusted domain issuers/signing keys;
2. validates signature, expiry, issuer and audience;
3. normalizes actor context;
4. routes the request.

The gateway does not create accounts and does not own credentials.

## Cross-domain identity

Accounts are intentionally domain-local. The same human may have separate accounts in multiple Real Estate products. Cross-domain account linking, if ever introduced, requires an explicit future architecture decision and must not be inferred from matching email/phone values.
