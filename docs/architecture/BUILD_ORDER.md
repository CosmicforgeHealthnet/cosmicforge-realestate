# Backend Build Order

## Foundation Block — build first

### F1 Shared Go foundation
Implement:
- IDs / UUIDv7 helpers
- request context
- error envelope
- JSON/http helpers
- validation
- auth/JWT primitives
- password hashing
- session/refresh-token primitives
- permissions helpers
- service auth
- idempotency primitives
- event envelope
- outbox worker primitives
- logging/observability
- test helpers

Proof:
- package tests
- crypto/session tests
- service-token validation tests
- outbox lease/recovery tests

### F2 API Gateway
Implement:
- routing
- trusted issuer validation
- user JWT verification
- service routing
- rate limiting
- CORS/security headers
- request/trace context
- standard error handling

Proof:
- route matrix tests
- trusted/untrusted issuer tests
- expired/wrong-audience token tests
- request context propagation

### F3 Verification Service
Implement:
- verification cases
- subject references
- verification types
- provider adapter contract
- manual review state
- evidence references
- expiry/reverification
- internal APIs/events

Proof:
- state-machine tests
- idempotent submission
- provider callback/read-back recovery
- admin decision authorization

### F4 Media & Document Service
Implement:
- upload sessions
- signed upload/download
- object metadata
- checksums
- scanning hooks
- access policies
- retention/deletion
- internal APIs/events

Proof:
- unauthorized access denied
- upload completion idempotency
- checksum/scan transitions
- delete/retention rules

### F5 Notification Service
Implement:
- recipient domain references
- templates
- push/email/SMS adapters
- Firebase FCM
- preferences
- retries
- delivery history
- internal APIs/events

Proof:
- duplicate command handling
- retry/final-failure behavior
- preference enforcement
- provider failure mapping

## Next domain block

After foundation is green:
1. individual-owner-service
2. business-operations-service
3. property-manager-service
4. artisan-service
5. professional-services-service
6. marketplace-service
7. payment-service full implementation
8. admin-service cross-domain operations

Payment API/state contracts are defined before domains depend on them even if the full service is delivered later.

## Domain authentication

When each user-domain service begins, it implements its local account/auth subsystem using shared Go packages. No shared identity database/service is introduced.
