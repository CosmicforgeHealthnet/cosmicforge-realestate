# Business commercial supply — Sections 5–7

The existing Business property graph is canonical. A product references a physical node and shared inventory pool; a rate version and immutable quote describe its commercial offer. A listing is reviewed content referencing that product and approved Media IDs. Distribution stores desired remote state and observed acknowledgements separately. It cannot manufacture stock or decide verification/payment/reservation truth.

## Owning transactions and authorization

The API uses the existing shared service/account signature middleware. Trusted organisation comes from current session/membership. Every product/property/channel read or mutation checks explicit effective capability and resource scope; UUIDs and composite tenant foreign keys reinforce this boundary. High-risk changes use immutable Section 3 policies, recent MFA, distinct actor approval and exact operation/amount/currency/channel binding. Bulk commands preserve every selected property's authorization, not just an anchor property. Provider callbacks/service APIs are separate from browser routes.

Calendar and hold mutations lock the organisation and affected dates in order. Deferred database constraints bound committed/blocked/held/allotted stock by real pool capacity; quote generation does not reserve. Domain mutation, immutable history, audit, outbox and idempotent HTTP result commit together. No external network call is made inside a provider dispatch transaction. Outbox follows the existing shared relay. Hold acceptance by a real reservation engine remains a future typed boundary.

## State and acknowledgement

Connection: DISCONNECTED → AUTHORIZING/CONNECTED, with explicit DEGRADED/SUSPENDED; CONNECTED requires operator-certified capability and signed provider authorization with expiry. Credential material is managed by an operator secret reference; callback keys use the established AES sealer and never return after configuration.

Publication has desired PUBLISHED/PAUSED/WITHDRAWN plus delivery PENDING/UNKNOWN/CONFIRMED/QUARANTINED. A 202 response only commits intent. Jobs use immutable JSON-canonical payload hashes, canonical versions, actor/session, request/trace correlation and stable provider idempotency keys. Lease owner/generation and per-provider serialization fence workers. Rate-limit waits do not exhaust delivery attempts. Provider 429/5xx/timeouts retry with bounded jitter; definitive rejection, corrupt/stale authority or exhausted attempts retain quarantine/conflict evidence. A timeout may have committed: read back the original command before replay. Success requires a fresh durable matching connection/version/hash/state observation. Readback never invents acceptance.

Maintenance derives deltas from immutable source-history sequence checkpoints, current physical stock, exact effective rates/fiscal policies and checkout restrictions. It does not replace an unacknowledged first content publication. Periodic readback checks quiet confirmed publications without applying mutations. Canonical eligibility loss queues a narrow system PAUSE; a remote outage remains UNKNOWN until confirmed. Existing scheduler and shared primitives are reused.

## Versioned Marketplace supply contract

Producer identity: business-operations-service. Audience: marketplace-service. Shared service JWT, delegated organisation and request/trace headers are mandatory. URL is trusted deployment configuration `MARKETPLACE_SERVICE_URL`, never browser input. Bounded shared client rejects redirects and owns no automatic mutation retry. Provider consumer operations (not implemented by the current Marketplace scaffold):

| Method/path | Typed request/result | Consumer obligation |
|---|---|---|
| POST /internal/v1/business-supply/connections/validate | Connection → Authorization | Bind provider account/property, approved credential reference, proof/expiry and capabilities |
| POST /internal/v1/business-supply/connections/{id}/mapping-inspection | MappingReference → MappingProof | Prove external object type/account/property; canonical IDs remain Business-owned |
| POST /internal/v1/business-supply/commands | Command → Acknowledgement | Enforce business-supply.v1, signed delegated org, current connection, stable idempotency key, canonical version; atomically persist accepted projection and durable ack |
| GET /internal/v1/business-supply/commands/{idempotency_key}?connection_id={id} | Acknowledgement | Return actual persisted outcome/hash/version/state and fresh observation; authoritative 404 only if not applied |
| GET /internal/v1/business-supply/connections/{id}/events?cursor={opaque} | PollResult | Stable bounded page (≤100), original and next cursor, immutable event/object version identities |

Commands carry canonical property/product/pool/listing and versions, current civil-date ARI, immutable rate/fiscal calculation context, hard restrictions and bounded content/Media references. Price provenance uses exact minor units, currency precision, deterministic rounding and policy references. Provider observations must match and do not authorize canonical state changes. Tests use a signed disposable HTTP consumer with actual shared authentication, durable command map, outage and readback; this is contract certification only.

Business inbound routes:

- POST /internal/v1/business-supply/channel-receipts: only signed Marketplace + matching organisation delegation/provider connection. Durable normalized receipt/object version/delivery-key dedupe; conflicting immutable facts quarantine. Section 9 unavailable means DEPENDENCY_BLOCKED, never canonical reservation accepted.
- POST /internal/v1/business-supply/channel-financial-inputs: signed Payment or Admin; Payment owns gross/commission/payment fee facts, Admin owns promotion/operating cost allocations. Exact period/currency/source identity/freshness/reconciliation required. No ledger or money execution.
- POST /webhooks/v1/business-channels/{provider}/{connection_id}: public Gateway forwards its own service signature; the specific Marketplace business-supply.v1 HMAC additionally signs timestamp/newline/path/newline/raw-body. Check `internal/distribution/webhook.go` for exact byte contract. Timestamp bounds and durable identity replay checks are required. Booking.com/Airbnb signature contracts remain unavailable and disabled. Internal routes are never Gateway-forwarded.

## Events and database

New canonical event types: inventory.changed.v1, rate_plan.changed.v1, listing.published.v1, listing.updated.v1, listing.withdrawn.v1, channel.connection.changed.v1 and channel.sync.failed.v1. Envelope: shared UUIDv7 event ID, producer business-operations-service, aggregate, actor and trace context. These transport as `realestate.business.<canonical event type>` on the existing Business stream. The Owner stream's root inventory/listing/rate_plan/property reservations are preserved. At-least-once consumers use shared inbox IDs in their owning transactions; raw broker redelivery cannot create duplicate effective updates.

Goose 00006 and SQLC add commercial tables only in business_operations_db. The complete catalogue accounts for all future sections without creating unused tables. Runtime grants reserve migration/provider-certification ownership and prohibit immutable evidence updates/deletes. See the three section reports and combined certificate for current test counts and full route matrix.

## External limitations

No actual operating-capacity assertion operation is available in Verification; commercial use fails closed rather than granting authority from a declaration. Live Marketplace consumer, OTA access/certification, Section 9 acceptance and staging deployment require their owning operations. Payment, Media, Notification and CF Meet boundaries remain intact. Profitability without current reconciled source facts returns explicit missingness/null net. This document is not a live rollout certificate.

Per-connection credential grants are persisted by forward-only migration 00007. Provider-wide certification alone cannot grant CONTENT, mapping, polling, webhook or stop authority to a credential. Revalidate existing connections after migration; default grants are empty/fail-closed.

Canonical frontend mapping confirmation is POST `/api/v1/business/channels/{id}/mappings`, after a real `/mappings/preview`. GET `/channels/{id}/sync-history` returns bounded immutable failure/attempt evidence; it shares the connection's trusted tenant/property read scope.
