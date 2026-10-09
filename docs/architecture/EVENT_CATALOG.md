# Initial Event Catalog

All events use the platform event envelope and UUIDv7 event_id.

## Envelope

```json
{
  "event_id": "019...",
  "event_type": "listing.published.v1",
  "producer": "individual-owner-service",
  "aggregate_type": "listing",
  "aggregate_id": "019...",
  "occurred_at": "2026-10-08T10:00:00Z",
  "trace_id": "...",
  "actor": {
    "domain": "individual-owner",
    "id": "019..."
  },
  "payload": {}
}
```

## Foundation events

### Verification
- verification.submitted.v1
- verification.processing.v1
- verification.manual_review_required.v1
- verification.verified.v1
- verification.rejected.v1
- verification.expired.v1

### Media/document
- media.upload_completed.v1
- media.scanning.v1
- media.ready.v1
- media.rejected.v1
- media.deleted.v1

### Notification
- notification.queued.v1
- notification.sent.v1
- notification.failed.v1

### Payments — contract reserved now
- payment.created.v1
- payment.processing.v1
- payment.confirmed.v1
- payment.failed.v1
- payment.unknown.v1
- payment.refunded.v1
- payout.completed.v1

## Individual Owner account events — implemented

- owner.account.activated.v1
- owner.account.restricted.v1

Schemas: `contracts/events/owners/`. Payloads carry no personal data; consumers order by `account_version`.

## Domain publication events — reserved contract names

- property.created.v1
- property.updated.v1
- listing.published.v1
- listing.updated.v1
- listing.withdrawn.v1
- provider.profile_published.v1
- provider.profile_updated.v1

Marketplace consumes eligible publication events to maintain search/discovery projections.

## Compatibility

Published event versions are immutable.

Breaking payload semantics require a new version suffix.

Consumers must ignore unknown optional fields.
