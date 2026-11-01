# Sections 8–11 baseline audit and execution matrix

Current progress: canonical reservation/overbooking checkpoint `67557e69c3177d2166d4a8d86cf52ac1da19486a` passed all five exact-SHA workflows. Subsequent RMS observations/forecasts are implemented and passed 188 Business tests with real disposable PostgreSQL/JetStream; see [Section 8 evidence](business-operations-section-08-report.md). The Sections 8–11 task remains incomplete; pending RMS governance, PMS and CRM work is not external-provider certification.

Baseline/fetched origin/staging: `19eb01f456d3cd40217e4ecd0e70a7a8073e1307`. Branch staging only. Verified current generated contracts: **283 routes, 214 schemas, 40 event schemas**, Goose **7**. Previous exact-SHA all five workflows passed (Backend 41210542440, Repository structure 41210542393, Contracts 41210542384, Security 41210542509, Images 41210542453). Business race artifact: 152 passed, no failure/skip. Baseline image: `sha256:2dcad286fba7ddffa4d182d6db506b9067a2e5dade131e21f5793fc28849f184`.

Preserved unrelated work: Owner web .gitignore/next-env.d.ts, README_FIRST.md, Owner master-plan document/generator and its Word lock file. No main/production/VPS operation authorised. All new migrations follow 00007; previously published files remain unchanged.

The complete authoritative specification (Sections 1–27, Annexes D–J) and the new master request were read before implementation. Earlier scaffold statements in the original specification are historical. Existing Sections 1–7 remain the foundation rather than replacement systems.

## Dependency audit

| Dependency | Classification | Evidence and required integration |
|---|---|---|
| Inventory/quotes/holds/rates | Existing and working | internal/inventory, revenue and commercial HTTP commands; extend atomic hold conversion and commitments, retain canonical graph/quotes |
| Marketplace reservation command/status | Missing owning integration | Marketplace remains scaffold; implement Business signed command/readback and explicit disposable provider contracts |
| Channel reservation receipts | Existing but requiring integration | signed receipt/delivery-key pipeline currently QUARANTINED/DEPENDENCY_BLOCKED; wire canonical reservation transaction, preserve unresolved receipt history |
| Payment locked quote/intent/status | Existing but requiring integration | payment /internal/v1/locked-quotes and /payment-intents; HOTEL/SHORT_LET policy families; business-domain caller permitted; bind payer/legal consent/quote/policy/amount, verify signed readback |
| Payment refunds | Missing Business-authorised submission operation | RequestRefund requires AdminService; persist approved Business remedy/refund reference and route through actual authorised Admin contract when supplied; never impersonate Admin or report fake success |
| Payment settlement/provider collection | Credential/configuration and live certification unavailable | Existing service currently guards production capabilities and uses eligible configured test providers; preserve pending/unknown and verify authoritative readback |
| Notification | Existing but requiring integration | existing protected durable notification jobs, shared signed acceptance contract; new reservation/CRM notices use it, not providers |
| KYB/property authority | Existing but requiring integration | existing verified projections/readback and immediate expiry restrictions |
| Operating capacity | Missing owning-service operation | existing typed CapacityAuthority fails closed; test explicit owning fixtures; do not approve declarations automatically |
| Smart access | Credential/configuration and live certification unavailable | typed grant/revoke/status boundary, durable commands and unknown outcomes; no invented code delivery |
| Customer identity | Missing owning-service linking/readback operations | typed Marketplace/OTA/direct/corporate refs; never email/phone-only merge; signed source assertions and approved explicit local linking |
| Corporate billing | Future/owning financial dependency | Business owns contract/obligation references; actual credit/collection remains Payment-owned |
| External market/search data | Credential/configuration unavailable | source-backed optional adapters and signed inputs; explicit missingness; deterministic baseline requires no external AI |
| Staging rollout | No authorised workflow/transport/secrets | previous and current readiness audit retained; recheck final GitHub metadata; Images publication is not deployment |

## Requirement matrix (in progress; checkpoint evidence)

Rows record current checkpoint status, not final certification. Existing foundations are reused for every row. Execution order: reservations → RMS → PMS → CRM → complete certification. Final per-requirement results and evidence belong in the combined implementation report.

| Requirement | Current result | Owning implementation/evidence | Required closure |
|---|---|---|---|
| 4.1 Single authoritative reservation system | PASS | internal/reservations, operational_reservations.go, real acceptance/source/concurrency tests | Preserve the single owning authority |
| 4.2 Reservation lifecycle | PARTIAL | Pending/confirmation/cancellation/no-show/dispute/expiry executable; checked-in/out/completed depend on unfinished PMS | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.3 Reservation persistence | PARTIAL | Goose 00008, SQLC reservations.sql; 25 tables; overbooking tables still lack executable commands | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.4 Reservation creation | PARTIAL | Multi-line immutable quote/hold acceptance, guarantees, signed source intake; corporate/CRM identity composition pending | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.5 Inventory and double-booking prevention | PASS | Real pool/date locks, hold conversion, committed-day provenance, rollback/concurrent acceptance integration tests | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.6 Quote and immutable price snapshots | PASS | Accepted price/terms immutable; append-only amendment snapshots; database negative mutation tests | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.7 Payment Service integration | PARTIAL | Signed typed policy/quote/intent/receipt, encrypted jobs, lost-response recovery, current remedies; Business refund submission remains Admin-owned external dependency | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.8 Marketplace reservation integration | PARTIAL | Executable signed Business intake/readback; disposable owning-service contract tests; live Marketplace remains absent | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.9 External channel reservation ingestion | PARTIAL | Durable receipt dedupe, savepoint atomic acceptance, conflict queue and exact reviewed-change binding; live OTA contracts/certification absent | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.10 Booking modifications | PARTIAL | Approved pre-arrival full replacement, financial credit conservation; in-house extensions/early departure await PMS | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.11 Multi-room and group reservations | PARTIAL | Multi-line groups, guests, room dates and whole-line partial cancellation; quantity fragmentation and PMS rooming/arrival workflows pending | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.12 Split and merge | PARTIAL | Whole-line transfer, conserved stock/price/financial lineage tested; quantity fragmentation pending; independent external-source merges now denied | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.13 Waitlist | PARTIAL | Expiring real offers, atomic single-use acceptance/cancellation/rejection and lifecycle audit/events; automatic release-triggered offers and guest notification workflow pending | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.14 No-show, cancellation and disputed state | PARTIAL | Frozen local policy deadlines, immutable fees, protective restricted-org exit, late payment refund review; live financial remedy execution unavailable | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.15 Overbooking controls | PARTIAL | Goose 9; explicit MFA/SOD pool policy, bounded excess-demand acceptance, immutable provenance, physical reconciliation/cancellation, signed channel pause; concurrency/security tests pass | Compose the actual CRM customer-recovery workflow and verify final exact-SHA CI |
| 4.16 Reservation command center | PARTIAL | Scoped before pagination; search, arrivals/departures/pending/groups/modifications/waitlist/conflicts/exceptions; PMS room-level completion pending | Continue remaining scope; checkpoint is not full Section 9 certification |
| 4.17 Reservation risk | PARTIAL | Versioned manual evidence/SOD/MFA projection and expiry gate; external risk source remains absent | Continue remaining scope; checkpoint is not full Section 9 certification |
| 5.1 RMS architecture | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.2 Database | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.3 RMS input data | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.4 Forecast engine | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.5 Model governance and provenance | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.6 RMS outputs | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.7 Operating modes | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.8 Autopilot restrictions | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.9 Recommendation lifecycle | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.10 Pricing recommendation application | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.11 Pricing simulator | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.12 RMS metrics | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.13 RMS monitoring and rollback | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 5.14 RMS workers | FAIL (domain not yet implemented) | internal/revenue; migration 00009 | Implement owning commands, persistence, API/event and real integration tests |
| 6.1 PMS ownership | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.2 PMS database | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.3 Front-desk dashboard | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.4 Check-in | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.5 Room assignment | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.6 Room statuses | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.7 Walk-in reservations | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.8 In-house stay management | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.9 Folio engine | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.10 Payment and folio reconciliation | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.11 Checkout | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.12 Cashier shifts | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.13 Cash drawer controls | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.14 Night audit | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.15 Business-day close | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.16 Offline-tolerant front desk | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.17 Group/corporate/event operations | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 6.18 PMS workers | FAIL (domain not yet implemented) | internal/hospitality; migration 00010 | Implement owning commands, persistence, API/event and real integration tests |
| 7.1 CRM ownership | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.2 Database | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.3 Canonical guest references | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.4 Guest profile | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.5 Stay history | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.6 Guest consent and privacy | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.7 VIP and segmentation | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.8 Loyalty and rewards | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.9 Corporate customer management | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.10 Service recovery cases | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.11 Compensation and benefits | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.12 Guest communication and notifications | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.13 CRM metrics | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |
| 7.14 CRM workers | FAIL (domain not yet implemented) | internal/crm; migration 00011 | Implement owning commands, persistence, API/event and real integration tests |

## Transaction and authority decisions

Reservation commitments will extend existing calendar/hold accounting, not introduce another availability store. Reservation, immutable price/policy versions, inventory effect, history and outbox share one owning transaction. Network calls occur outside it. External delivery ACK is distinct from canonical business acceptance. Modifications, split/merge and room movement conserve effective stock and append history.

PMS uses existing physical room IDs with unique active room/date assignments and distinct stay records. Dirty/blocked/unavailable rooms cannot become occupied through a client flag. Folios record obligations/adjustments, never provider money movement. Business-day closure and offline replay are version fenced. CRM stores typed identities, purpose-restricted sensitive preferences, independent marketing consent and idempotent stay/loyalty projections. RMS stores versioned inputs/model/policy, explicit insufficient-data results and deterministic guarded changes to the existing canonical rate engine.

Checkpoint: canonical reservation core is implemented in the working milestone. Sections 8, 10 and 11 remain unimplemented; no full Sections 8–11 certification or staging deployment claim is made. See business-operations-section-09-report.md for executed evidence and open work.
