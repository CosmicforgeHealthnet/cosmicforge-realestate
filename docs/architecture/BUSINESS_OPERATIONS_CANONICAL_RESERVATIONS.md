# Canonical reservation transaction and financial authority

This checkpoint extends Sections 1–7. Business Operations owns canonical reservations in `business_operations_db`; Marketplace and OTA receipts reference that authority. It does not implement payment execution, a second availability engine, or an independent guest identity authority.

## Acceptance and inventory

`internal/reservations` and `internal/http/operational_reservation*.go` consume existing products, rate quotes, property capacity assertions, holds, restrictions and physical inventory pools. Registration under `/api/v1/business` uses the existing Gateway and live account/session/membership enforcement. Internal source routes require signed Marketplace identity, delegated organisation, certified connection capabilities and confirmed mappings.

The common organisation fence precedes expected-version reads. Calendar rows lock in date order. Acceptance, immutable accepted prices/policy, hold provenance, reservation history, audit and transactional outbox commit together. Confirmation exchanges the exact live hold for committed inventory days. Committed allotted stock remains charged against its original allotment. Cancel/reject/expiry release only actual allocation provenance. A failed multi-line command rolls back every stock and financial effect.

Accepted room prices cannot be edited. Amendments supersede old room lines and append new accepted snapshots. Immutable financial credits conserve already collected backing without charging the original gross amount again. Whole-line split/merge transfers ownership of the same price, hold and financial references. Quantity fragmentation remains open work.

The database rejects a root version without its matching immutable snapshot, audit and outbox record. Runtime grants separately deny mutation of immutable histories. This is defence in depth; PostgreSQL administrators remain an operator boundary.

## Payment readback

The optional `PAYMENT_SERVICE_URL` composes the existing signed shared HTTP client. Collection jobs encrypt payer email, consent and legal-party references using the configured protected-storage sealer and tenant/job associated data. Responses, operational events and audit exclude these values. The shared idempotency store retains a request fingerprint and redacted result.

The worker uses Payment-owned approved policy, locked quote, intent and receipt APIs. Calls occur outside owning database transactions. A generation-fenced lease and stable provider keys protect restart/retry. A lost POST response is resolved through the original quote receipt. Recovery may record an already executed collection after the original session is revoked; creating a new collection still requires its original current authority, accepted version and hold deadline.

Collection success is historical and distinct from settlement, refunds and present backing. Payment's additive receipt contract supplies `remedy_version=1` and `remedy_review_required` from its own repeatable-read snapshot. Open cases, refund reservations/executed refunds and adverse closed-case decisions restrict reuse of the collection. Business requires the versioned remedy contract and a fresh, unreversed, unrestricted receipt for every actual financial backing ancestor. An older Payment deployment without this contract blocks new collection/confirmation safely. Deploy the corresponding Payment image before enabling Business collection.

Cancellation freezes fee and potential refund obligations at the exit. A later collection never revives an expired/cancelled booking; readback creates one idempotent refund-review request using the original frozen fees. `DEPENDENCY_BLOCKED` explicitly means independent financial approval and the authorised Admin handoff are still required. Existing Payment refund submission is Admin-only. Business never impersonates Admin or reports that a local obligation executed a refund.

## Source conflicts

Webhook technical receipt and canonical acceptance are separate results. Failed acceptance rolls back a savepoint while retaining the authenticated source receipt, payload hash, delivery keys and reconciliation case. Ordinary redelivery cannot create another reservation or acknowledge an unreviewed change.

Conflict decisions require current versions, property-scoped resolution capability, recent MFA, exact independent approval and evidence references. RETRY reruns the same normalized receipt. BIND_REVIEWED requires an independently applied canonical amendment after that receipt and matches its room set, accepted quote IDs, dates, quantities, financial responsibility and typed source guest references. It cannot bind a different booking or a split/merged allocation. Decisions remain immutable. REJECT never acknowledges canonical acceptance. Source status flags lineage reconciliation rather than pretending one child represents an entire external booking.

## Protective exits and operational queries

Only the server's private cancellation context permits scoped protective exit under a restricted/suspended/closing organisation. Live account/session/membership, explicit capability, deny, scope and effective-date rules remain enforced. Requests cannot construct this context. Growth operations remain restricted.

Reservation lists and command-center views resolve permitted properties before pagination. Arrivals/departures use room-line dates and property-local civil days. No-show checks each selected room's frozen local deadline. Confirmation rechecks current product/channel, restrictions, capacity, risk and actual financial backing without repricing accepted history.

## Delivery boundary

The current checkpoint includes the reservation core and authenticated disposable integration tests. It is not full Section 9 or Sections 8–11 certification. Overbooking commands, quantity fragmentation, additional waitlist automation, PMS, RMS and CRM remain tracked in the implementation matrix. Real OTA/Marketplace, Payment-provider and staging deployment certification require their actual supported integrations and authorised deployment infrastructure.
