# Foundation State Machines

## Verification case

```text
DRAFT
  -> SUBMITTED
  -> PROCESSING
      -> VERIFIED
      -> REJECTED
      -> MANUAL_REVIEW
           -> VERIFIED
           -> REJECTED

VERIFIED -> EXPIRED
```

Rules:
- every transition is audited;
- provider ambiguity remains PROCESSING/UNKNOWN_PROVIDER_RESULT until read-back resolves it;
- expiry and re-verification are explicit, not silent record mutation.

Verification types initially:
- IDENTITY
- BUSINESS
- PROPERTY_AUTHORITY
- PROFESSIONAL_CREDENTIAL

## Media/document

```text
INITIATED
  -> UPLOAD_PENDING
  -> UPLOADED
  -> SCANNING
      -> READY
      -> REJECTED

READY -> DELETE_PENDING -> DELETED
REJECTED -> DELETE_PENDING -> DELETED
```

Access classifications:
- PUBLIC
- PRIVATE
- DOMAIN_RESTRICTED
- ADMIN_RESTRICTED

Media-document-service stores metadata/access state; raw bytes live in object storage.

## Notification

```text
QUEUED
  -> DISPATCHING
      -> SENT
      -> RETRY_SCHEDULED
          -> DISPATCHING
      -> FAILED_FINAL
```

Delivery acknowledgement, where supported, is separate from provider acceptance.

Retry policy is bounded and provider-aware. Permanent failures are not retried indefinitely.

Channels:
- PUSH via Firebase FCM
- EMAIL
- SMS

Human-to-human messages are not notification-service data; they use CF Meet.

## Payment contract — reserved before implementation

```text
CREATED
  -> PENDING
  -> PROCESSING
      -> CONFIRMED
      -> FAILED
      -> UNKNOWN

UNKNOWN -> CONFIRMED | FAILED

CONFIRMED
  -> PARTIALLY_REFUNDED
  -> REFUNDED
```

Unknown provider outcomes require authoritative read-back/reconciliation before repeating the money-moving request.

Ledger entries are append-only accounting records; business status changes do not rewrite financial history.
