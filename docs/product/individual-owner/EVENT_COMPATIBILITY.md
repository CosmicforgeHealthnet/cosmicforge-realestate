# Individual Owner — consumed event compatibility matrix

This matrix covers the seven event families that individual-owner-service
consumes (§10.1). It states which producer is authoritative, which contract
version each consumer accepts, and what evidence exists at each level. Fake
adapters and in-process fixtures never count as live integration.

Levels: **LOCAL** = in-process against real PostgreSQL; **CONTRACT** = fixture
validated against the producer's published JSON Schema; **REAL-PROCESS** =
the real producer binary publishes over JetStream and the owner worker
consumes it (tests/e2e). Staging and production are not certified.

## Matrix

| # | Family (durable consumer) | Producer | Accepted types (v1 only) | Producer schema | Consumer contract | Status |
|---|---|---|---|---|---|---|
| 1 | `verification.*` (`individual-owner-service--verification`) | verification-service (implemented) | `submitted`, `processing`, `manual_review_required`, `verified`, `rejected`, `expired` | `contracts/events/verification/*.v1.schema.json` | `verificationPayload` | LOCAL PASS · CONTRACT PASS · REAL-PROCESS PASS (`verified`) |
| 2 | `media.*` (`individual-owner-service--media`) | media-document-service (implemented) | `ready`, `rejected`, `deleted` | `contracts/events/media/*.v1.schema.json` | `ApplyMediaEvent` payload | LOCAL PASS · CONTRACT PASS · REAL-PROCESS PASS (`deleted`) |
| 3 | canonical `finance.*`, historical `payment.*` (`individual-owner-service--payment`) | payment-service (implemented Financial Core) | 17 supported canonical v1 types; legacy `intent.updated`/`refund.updated` only for historical bindings | `contracts/events/payments/*.v1.schema.json` (34 published schemas) | Signed Core truth readback, original binding/aggregate, lease/retry, immutable resource IDs; no legacy updates to Core-bound intents | LOCAL / CONTRACT; actual process + JetStream collection/allocation/recovery evidence; provider/production gates remain |
| 4 | `manager.*` (`individual-owner-service--property-manager`) | property-manager-service (**not implemented**) | `mandate.responded`, `activity.recorded` | none published | `ManagerResponseEvent`, `ManagerActivityEvent` | LOCAL PASS · live **BLOCKED** |
| 5 | `artisan.*` (`individual-owner-service--artisan`) | artisan-service (**not implemented**) | `quote.submitted`, `job.event_recorded` | none published | `ArtisanQuoteEvent`, `ArtisanJobEvent` | LOCAL PASS · live **BLOCKED** |
| 6 | `professional.*` (`individual-owner-service--professional-services`) | professional-services-service (**not implemented**) | `engagement.updated` | none published | `ProfessionalUpdateEvent` | LOCAL PASS · live **BLOCKED** |
| 7 | `marketplace.*` (`individual-owner-service--marketplace`) | marketplace-service (**not implemented**) | `listing.reported` | none published | `ListingReportedEvent` | LOCAL PASS · live **BLOCKED** |

The current payment consumer accepts only the canonical types documented in
[the consolidated integration closure](SECTIONS_12_13_INTEGRATION_CLOSURE.md).
Collection is a hint for signed original Core readback, never a paid/settled
business outcome. Customer wallet spend adopts its existing Core intent.
Reconciliation exceptions support both published aggregates: custody pool and
payment intent. Payout results retain original wallet/provider/pool correlation.
Duplicate and stale events use fresh truth; confirmed refund IDs are recomputed,
not incremented, and expired observation claims recover through the existing worker.

Real source backends for manager/artisan/professional/marketplace remain absent.
Their signed simulators and payloads certify local boundary behavior only. Real
notification/verification/media evidence is retained at its original source SHA.
Historical generic `payment.*` payloads remain reserved; Core is not asked to
publish invented legacy events. Unknown references retry until their source binding
is committed; wrong source, aggregate or version is rejected.

## Safety rules (all families)

| Threat | Guard | Evidence |
|---|---|---|
| Invalid producer identity | Envelope `producer` must equal the authoritative service. Verification and media also check `aggregate_type`. Violations are terminated as poison and never applied. | `TestUpstreamEventsCannotCorruptState`, `TestEventConsumersApplyOnceThroughUseCases` |
| Unknown schema version | Each consumer has a type allowlist (v1 only); an unknown type or `.vN` is terminated. A payload `schema_version` other than 1 is terminated. A payload status that disagrees with its type (verification, media) is terminated. | `TestUpstreamEventsCannotCorruptState` |
| Duplicate delivery | `processed_events (consumer, event_id)` is written in the same transaction as the effect. | All consumer tests, plus real-process `processed_events` = 1 |
| Out-of-order delivery | Verification: status rank, never backwards; `EXPIRED` only after `VERIFIED`. Media: `DELETED` is terminal and `REJECTED` never becomes `READY` (SQL guard). Payment: per-intent `updated_at` watermark (`payment_intent_watermarks`, migration 00016) on events and read-backs; `updated_at` is required. Unknown references are retried, never dropped. | `TestUpstreamEventsCannotCorruptState` (mutation-checked: removing the media guard or the payment watermark makes it fail) |
| Business rejection | 4xx outcomes other than 404/503 are terminated and logged. 404 is retried as possibly not yet committed. | `consumerOutcome` |

## Defect found by the real-process run

The owner service checks that identity evidence belongs to the caller by
reading the file's metadata from media-document-service. Media classifies
`VERIFICATION_DOCUMENT` as `ADMIN_RESTRICTED` by default, which hides it from
the owner service, so the real stack returned `422 MEDIA_NOT_FOUND`. The fake
media adapter had masked this. verification-service only checks the owner
*domain*, so removing the owner-side check would let one owner submit another
owner's document. The client contract is now documented on
`POST /verification`: upload identity evidence with
`access_classification: DOMAIN_RESTRICTED`, which media policy lets only the
owning domain's service, admins and verification-service read. The E2E
exercises this path.

## Remaining to certify

- Actual producer implementations and usable schemas for families 4–7. Until they exist,
  live integration stays BLOCKED.
- Real-process coverage of the other verification and media types, such as
  `rejected` and `expired`. These have CONTRACT PASS (schema plus consumer) but
  not REAL-PROCESS evidence.
- Staging: JetStream stream and consumer provisioning, redelivery limits, and a
  dead-letter alert on terminated messages.
