# Database Ownership

Every microservice owns exactly one PostgreSQL database boundary.

| Service | Database | Account authority |
|---|---|---|
| marketplace-service | marketplace_db | Marketplace accounts |
| individual-owner-service | individual_owner_db | Individual-owner accounts |
| business-operations-service | business_operations_db | Business/staff accounts |
| property-manager-service | property_manager_db | Property-manager accounts |
| artisan-service | artisan_db | Artisan/provider accounts |
| professional-services-service | professional_services_db | Professional-provider accounts |
| admin-service | admin_db | Platform-admin accounts |
| verification-service | verification_db | None |
| payment-service | payment_db | None |
| notification-service | notification_db | None |
| media-document-service | media_document_db | None |

The API Gateway owns no business database.

There is no shared identity database.

## Rules

1. A service receives credentials only for its own database.
2. Cross-database reads/writes are prohibited.
3. Each user-domain service persists and authorizes its own users/accounts.
4. Shared services receive authenticated actor/domain context through internal API contracts; they do not own user credentials.
5. Cross-service reads/actions use authenticated internal APIs.
6. Asynchronous propagation uses versioned NATS events.
7. Database schemas are private implementation details, not integration contracts.
