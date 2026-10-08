# Final Architecture Baseline

## Count

- 7 user-domain services
- 4 shared platform services
- 1 API Gateway
- 11 service-owned PostgreSQL databases
- 12 deployable backend components

## User-domain services

1. marketplace-service
2. individual-owner-service
3. business-operations-service
4. property-manager-service
5. artisan-service
6. professional-services-service
7. admin-service

Each maps to its own product/application domain. Individual owners are not merged with business property operators. Artisans are not merged with professional property-service providers.

Each user-domain service owns the accounts, credentials, sessions, roles and permissions for its own product. There is no central Real Estate identity microservice.

## Shared services

1. verification-service
2. payment-service
3. notification-service
4. media-document-service

## Edge

api-gateway is an edge/routing component and owns no business database.

The gateway trusts configured domain token issuers/signing keys and validates domain-issued authentication tokens. It is not an account authority.

## External/shared platforms

- CF Meet: messaging, voice, video, virtual viewing, communication context, privacy relay
- Paystack: payment rail accessed only through payment-service
- Firebase: FCM, Crashlytics, Analytics, App Distribution
- S3-compatible object storage
- Maps and verification/registry providers
