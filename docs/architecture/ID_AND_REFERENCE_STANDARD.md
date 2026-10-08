# ID and Reference Standard

## Canonical entity IDs

UUIDv7 is the default persistent entity identifier.

Examples:
- user_id
- organization_id
- property_id
- listing_id
- tenancy_id
- job_id
- professional_id
- verification_id
- payment_id
- notification_id
- media_id
- audit_id
- event_id

## Ownership

The owning service generates the ID.

Global uniqueness does not imply global authority. The owner of the record remains the only authority.

## Cross-service references

When the entity type can be owned by more than one domain, carry both owner/domain and ID.

Example:

```json
{
  "subject_type": "user",
  "subject_domain": "artisan",
  "subject_id": "019..."
}
```

For properties/listings, include source_service/source_domain when a projection or cross-domain reference is stored.

## Never do this

- infer cross-domain identity from equal email/phone;
- use database auto-increment IDs as public IDs;
- reuse deleted IDs;
- encode authorization decisions into opaque ID prefixes;
- create database foreign keys across service-owned databases.
