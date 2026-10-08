# Database Ownership

Every service owns exactly one PostgreSQL database boundary.

| Service | Database |
|---|---|
| marketplace-service | marketplace_db |
| individual-owner-service | individual_owner_db |
| business-operations-service | business_operations_db |
| property-manager-service | property_manager_db |
| artisan-service | artisan_db |
| professional-services-service | professional_services_db |
| admin-service | admin_db |
| identity-service | identity_db |
| verification-service | verification_db |
| payment-service | payment_db |
| notification-service | notification_db |
| media-document-service | media_document_db |

The API Gateway owns no business database.

## Rules

1. A service receives credentials only for its own database.
2. Cross-database reads/writes are prohibited.
3. Cross-service reads/actions use authenticated internal APIs.
4. Asynchronous propagation uses versioned NATS events.
5. Database schemas are private implementation details, not integration contracts.
