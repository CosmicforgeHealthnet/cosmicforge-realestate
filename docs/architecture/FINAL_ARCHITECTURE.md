# Final Architecture Baseline

## Count

- 7 user-domain services
- 5 shared platform services
- 1 API Gateway
- 12 service-owned PostgreSQL databases
- 13 deployable backend components

## User-domain services

1. marketplace-service
2. individual-owner-service
3. business-operations-service
4. property-manager-service
5. artisan-service
6. professional-services-service
7. admin-service

Each maps to its own product/application domain. Individual owners are not merged with business property operators. Artisans are not merged with professional property-service providers.

## Shared services

1. identity-service
2. verification-service
3. payment-service
4. notification-service
5. media-document-service

## Edge

api-gateway is an edge/routing component and owns no business database.

## External/shared platforms

- CF Meet: messaging, voice, video, virtual viewing, communication context, privacy relay
- Paystack: payment rail accessed only through payment-service
- Firebase: FCM, Crashlytics, Analytics, App Distribution
- S3-compatible object storage
- Maps and verification/registry providers
