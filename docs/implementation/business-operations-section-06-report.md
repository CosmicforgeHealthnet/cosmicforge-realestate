# Section 6 — distribution and Channel Manager

See the [combined certificate](business-operations-sections-05-07-certification.md) for baseline, exact test/CI evidence and delivery SHA. The Channel Manager is Business Operations' supply producer; external channels never own canonical property, capacity, rates or reservations.

## Architecture and provider readiness

`internal/distribution` supplies typed Connection, Authorization, MappingReference/Proof, Command, Acknowledgement, normalized Receipt and PollResult boundaries. Adapter operations cover validation, inspected mapping, content/ARI/stop commands, readback and bounded polling. Provider/capability records expose 17 operation-specific states, supported mechanisms/object types, contract version, quotas and operator certification references. Optional messaging/reviews/promotions/payment-metadata/reporting capabilities remain disabled until a real owning provider operation exists; registry availability is not an implementation claim.

The first-party Marketplace client uses the existing Ed25519 service signer/shared HTTP client, Marketplace audience, delegated organisation, correlation, bounded timeouts/responses and no redirects. URLs come only from deployment configuration. Durable retry belongs to this service's sync jobs. Live Marketplace currently lacks the supply consumer/readback endpoints; signed disposable HTTP consumers verify the real wire/authentication/idempotency/readback contract without rewriting Marketplace.

Booking.com and Airbnb remain explicit disabled adapters. Real partner credentials, property access, exact API/signature operations and provider certification are unavailable. No fabricated API call, token, reservation or certification is implemented. Current primary provider references reviewed for readiness:

- [Booking.com authentication](https://developers.booking.com/connectivity/docs/authentication), [going live](https://developers.booking.com/connectivity/docs/going_live), [mapping connections](https://developers.booking.com/connectivity/docs/connections-api/mapping-connections) and [endpoint access](https://developers.booking.com/connectivity/docs/managing-endpoint-access).
- [Airbnb PMS software](https://www.airbnb.com/help/article/3418) and [software partners](https://www.airbnb.com/software-partners).

Those sources establish external onboarding/certification requirements, not CosmicForge approval. No historical onboarding pause is asserted as a current fact.

## Connections, mappings and authority

Connections are tenant/property scoped. A frontend supplies only an opaque operator-managed credential reference, never a provider URL or raw credential. Authenticated provider validation occurs outside the mutation transaction; version, provider account/property, proof, expiry, capability and independent approval are then checked before CONNECTED. Disconnect/remap requires actual confirmed provider stop/withdrawal and no conflicting channel holds. Unconfirmed sellability remains UNKNOWN.

Mapping preview inspects the actual remote property/product/rate object and freezes canonical/current mapping versions, proofs, connection and digest for ten minutes. Confirm rechecks all references under the organisation lock and consumes independent approval. Duplicate external/canonical identities and conflicting existing bindings are rejected. Confirmed mappings retain provenance, validation and last-sync evidence; no canonical ID is replaced by a provider ID.

Field authority supports INTERNAL, CHANNEL_OVERRIDE and expiring MANUAL_OVERRIDE. Approved content title/description/amenities/rules and stricter stop/minimum-stay controls apply to actual queued payloads and local sellability. Listing overrides retain canonical public Media references. Expired/changed authority fails closed. Price changes use approved canonical rate versions; physical capacity/compliance/tenant identity cannot be overridden. Last-sync timestamps update only after matching acknowledgement and readback, without mutating configuration versions.

## Publication, ARI and recovery

`commercial_content.go` implements templates, immutable listing versions, Media references, quality/readiness, review and desired publication state. Publish validates current business/KYB/capacity/property/product/pool/rate/fiscal/source versions, independent SoD/MFA/amount-bound approval and fresh signed READY/PUBLIC Media metadata. A 202 response means durably queued intent. PUBLISHED is recorded only after durable matching provider acknowledgement and readback.

`commercial_channel_worker.go` claims bounded jobs with SKIP LOCKED, lease ownership/generation, provider-wide dispatch serialization/quota and stable command keys. Network operations occur outside transactions. Current actor session/grants, canonical versions, stock, checkout restrictions, fiscal context, override versions and Media are rechecked before dispatch. Timeouts/ambiguous outcomes trigger readback before retry; only authoritative not-applied permits replay. Unknown remains UNKNOWN; bounded retry/backoff, durable failures/conflicts and safety pause preserve evidence. Worker restart recovers expired leases without losing jobs.

The existing maintenance loop consumes increasing immutable commercial-history sequence checkpoints into real current ARI deltas, including deduplicated pricing provenance and all checkout-aware restrictions. It does not replace an in-flight initial content publication. Connections rotate by maintenance time rather than success, avoiding starvation. Quiet publications receive periodic readback without another mutation. Canonical revocation queues a narrow system safety pause even when the old publishing actor is revoked; provider outage cannot be misreported as a confirmed stop.

## Inbound events and conflicts

`/internal/v1/business-supply/channel-receipts` requires signed Marketplace identity, correct audience and matching organisation delegation/connection. The isolated Gateway-forwarded callback namespace uses the specific versioned Marketplace HMAC contract over raw body, path and bounded timestamp. Keys are AES-protected with tenant/connection AAD and never returned/logged; changes require a fingerprint-bound independent approval. OTA signatures remain unavailable rather than borrowing an invented contract.

Provider delivery IDs and external object/version identities are durably deduplicated; equivalent redeliveries retain all delivery keys. Conflicting facts create immutable collision/poison evidence. Poll receipt commit and cursor advancement are atomic; poison pages retain the cursor for reviewed recovery. Valid signed unknown product events persist in quarantine. Reservation refs remain DEPENDENCY_BLOCKED because Section 9 acceptance does not exist; responses explicitly state canonical_reservation_accepted=false. No canonical reservation/capacity overwrite occurs.

Conflicts have immutable decisions and independent scope/MFA/SoD approval for RECONCILE, REJECT or PAUSE. Reconcile queues actual provider readback; an operator cannot manually declare provider sellability resolved. API history, queued/running/retry/failure counts, last success/failure, backlog lag, open conflicts, stale/unmapped products, confirmed/unknown publications and measured latency use persisted facts. Occupancy and conversion remain null until Section 9 sources exist.

## Profitability and boundaries

Signed Payment/Admin-only source inputs carry immutable identity/hash, exact amounts/currency, period, provenance, observed/expiry times and reconciliation state. Replayed changed facts conflict. Contribution is gross minus commission/payment/promotion/operating cost only when every component is current and reconciled; missing/stale/unreconciled components and net stay explicitly unknown. Contractual commission assumptions are not settlement facts. No charges, refunds, payouts, ledger, messages or Media bytes are executed/stored here.

## Evidence and status

Signed disposable consumer tests prove content publication, ambiguous commit/readback, idempotency, confirmed pause/disconnect, actual ARI outage/recovery/override/last-sync, periodic readback, cursor poison/replay, reviewed rejection, mapping collisions, tenant isolation, callback signature/key protection, reservation fail-closed intake and signed financial missingness/exactness. JetStream recovery/dedupe tests preserve the Owner namespace.

**Repository/disposable contract certification is distinct from live Channel Manager certification.** Real capacity assertion, Marketplace consumer, external OTA operations, Section 9 reservation acceptance and staging rollout remain externally blocked and PARTIAL. No live channel or deployment success is claimed.

Connection authority hardening: forward-only migration `00007_channel_connection_authority.sql` persists the operation list from the signed provider credential proof. Every capability requires both provider certification and a grant to that particular connection. Existing connections default to no grants until real revalidation. `TestBusinessCommercialProviderCertificationCannotExpandCredentialAuthority` proves registry certification cannot authorize polling/webhook intake with a narrower credential. Current Goose schema version is **7**; migration 00006 remains unchanged.

Certified runtime evidence: `cd03e9e19886ae762451334961279428a2870b62`; all five workflows passed, all 20 Backend jobs passed, Business race suite 152/152 with zero skips/failures, all 8 Images jobs passed. Runtime image `sha256:23a740645bbe1be8017aa84ca86404003d9caaf4def0224041fac9cce5bfdf09`. See the combined certificate/machine evidence and final delivery response for the separately checked latest handoff SHA. This does not certify a live deployment or unavailable provider operation.
