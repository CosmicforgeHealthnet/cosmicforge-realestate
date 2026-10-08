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

Each domain implements as required:
- registration
- login/logout
- password credentials
- email/phone verification
- password recovery
- sessions and rotating refresh tokens
- device/session management
- MFA
- roles and permissions
- organization/staff memberships where the domain requires them
- security/audit events

## Shared implementation, separate authority

Security primitives are reused through shared/go:
- auth
- jwt
- passwords
- sessions
- permissions
- security
- serviceauth

Shared packages contain reusable implementation only. They must not introduce shared user tables or shared account persistence.

## Token standard

User access tokens:
- JWT
- Ed25519 signatures
- domain-specific issuer
- common Real Estate API audience
- 15-minute default lifetime
- kid for rotation
- jti for traceability/revocation support

Refresh tokens:
- opaque cryptographically random value
- hashed at rest
- rotated on use
- default maximum session 30 days
- reuse detection revokes the session/token family

## Password standard

Argon2id is mandatory.

Hash parameters are centralized and versioned in shared Go code so stronger settings can trigger rehash at successful login.

Raw passwords never leave the owning domain and are never logged.

## Gateway trust model

Each user domain is an authentication issuer.

The API Gateway:
1. accepts only configured trusted issuers/signing keys;
2. validates signature, expiry, issuer and audience;
3. normalizes actor context;
4. routes the request.

The gateway does not create accounts and does not own credentials.

## Cross-domain identity

Accounts are intentionally domain-local.

The same human may have separate accounts in multiple products. Matching email/phone values never automatically link accounts.

Any future global account-linking capability requires a new ADR and explicit consent/security model.
