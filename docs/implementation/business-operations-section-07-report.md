# Section 7 — inventory, rate plans and restrictions

Implementation baseline and delivery evidence: [combined certificate](business-operations-sections-05-07-certification.md). Scope is canonical commercial supply and exact offers; Section 8 autopilot, Section 9 reservation confirmation and payment execution remain with their owning future/external contexts.

## Inventory and availability

`internal/inventory/{model,repository}.go` owns physical-stock projections and ordered multi-day locking. `internal/http/commercial.go` and `commercial_stock.go` expose tenant/property-scoped products, pools, calendars, availability, holds and allotments. Unit/room stock is one physical identity; room-type stock is the explicitly selected distinct real room set; facility stock is bounded by canonical physical capacity. Products share their pool rather than creating stock. Common areas/non-sellable nodes cannot be commercialized. Current graph/source/member versions, active business, authoritative verification/capacity, compliance holds, rate readiness and channel eligibility are checked on commercial use.

Calendar writes use all expected day versions and preserve holds/allotments/commitments. Hold creation locks civil dates deterministically in the owning transaction, validates an immutable quote, and writes the hold, days, history, audit and outbox atomically. Cancellation/extension are versioned; expired holds and allotments cease consuming stock immediately even if maintenance is stopped. Cleanup persists explicit expiry and deltas. Allotments reserve existing free capacity for a certified channel; releases return capacity and cannot manufacture stock. Handoff returns an explicit unavailable dependency until Section 9 can accept; it never confirms a reservation.

## Pricing and restrictions

`internal/revenue/{pricing,repository,offers}.go`, `commercial_rates.go`, `commercial_quotes.go`, `commercial_fiscal.go` and `commercial_offers.go` implement immutable effective rate versions, standard/derived/occupancy/negotiated/packaged kinds, fixed and basis-point adjustments, season/day/channel rules, adult/child and length-of-stay pricing. Derived parents are pinned immutable versions, same-property/currency, cycle checked and depth bounded. Parent hard restrictions remain live and cannot be evaded by a derived child.

Hard restrictions intersect property/pool/product/rate/channel scopes, date/weekday, stop-sell, CTA/CTD, LOS, guest counts, lead windows and property-local cutoff. Checkout-day CTD is checked separately. Contradictory overlapping rules are rejected. Relaxation requires independent exact approval. Precedence is canonical compliance/physical capacity → all hard restrictions → immutable rate/occupancy/LOS/channel rules → eligible promotions → approved package adjustment → add-ons → approved fiscal policy. Lower layers cannot widen hard limits.

Money uses the shared integer ISO currency primitive, overflow checks and exact ratio rounding (HALF_AWAY_FROM_ZERO). No implicit FX or jurisdiction tax assumption occurs. Currency policy and source-backed tax/fee records require reviewed approvals. Inclusive/exclusive tax/fees conserve exact totals. A changing per-stay fee definition across the stay is rejected without an authoritative allocation policy. Nightly fees apply per civil night; per-stay fees once. Quotes include product/pool/rate/policy versions, parent provenance, occupancy, dates, exact components, fiscal IDs, discounts, cancellation/prepayment references, expiry and `is_reservation=false`. Existing snapshots are immutable; new holds/extension recheck live eligibility and versions.

Promotions, packages and add-ons have persisted draft/lifecycle controls, effective eligibility, stacking/exclusivity and bounds. Activation and price changes require independent capability/amount/currency-bound approval. Packages reference real products and add-ons; they do not create hospitality fulfilment or extra physical inventory. Channel markup/markdown uses approved canonical rate rules; field overrides cannot invent separate unmanaged prices. Commission inputs are explicitly contractual estimates until reconciled source facts arrive.

## Bulk commands and security

`commercial_bulk_rates.go` implements bounded date, product, plan and branch/property selection; preview has no price mutation. Immutable snapshots contain expected versions, proposed exact definitions, digest, conflicts and server-computed approval exposure. Apply consumes an exact independent approval, queues a durable job, and revalidates the original live session, all scopes, current versions and authority before one atomic batch. Competing workers cannot apply twice. Stale/revoked work is quarantined with immutable audit. Rollback creates a new preview/approval and appends versions only if no intervening change exists.

The established session/membership evaluator supplies tenant and hierarchy scope, explicit grants, effective dates, denials, approval limits and recent MFA. No client organisation header can override it. Commercial approvals exclude onboarding bootstrap and require distinct approvers; break-glass cannot grant commercial mutation/approval authority.

## Executed journeys

Real PostgreSQL tests cover commercializing canonical units, real room-type membership, inherited derived pricing/occupancy/LOS, last-capacity concurrency, stop-sell/stale quote rejection, cancellation/expiry, allotment release/expiry, immutable fiscal/offer snapshots, promotion/package precedence, branch-wide atomic batches, stale/revoked bulk jobs and cross-tenant channels/rates/pools/mappings. Pure tests cover DST civil dates, overflow/currencies, restriction windows and exact tax arithmetic. See the combined certificate for exact test names/counts and Linux race evidence.

API inventory is generated from actual registered handlers in `contracts/routes.json`, OpenAPI and official Postman folder **06 Inventory & Availability** / **07 Rate Plans, Restrictions & Promotions**. Events include inventory.changed.v1 and rate_plan.changed.v1, transactionally relayed on the existing Business namespace.

Repository status: implemented and tested on disposable infrastructure. **Live commercial activation is fail-closed while the real CapacityAuthority operation remains unavailable; this is not live verification/capacity certification.**
