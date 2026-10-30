<!-- Faithful text/table transcription of the user-supplied v1.0 DOCX. Source SHA256: 4446e8a7a69c88c2dff9deae396d0f377d0ec5f357450b150d0e0903bf3b7490. Historical baseline statements are preserved as source text, not current certification. -->

COSMICFORGE REAL ESTATE

Business Property Operations Platform
Full Backend Implementation Specification v1.0 (for Product v6.0)

Research benchmark date: 29 October 2026

| Document control | Value |
| --- | --- |
| Target service | services/business-operations-service |
| Database | business_operations_db |
| Active engineering branch | staging (development branch no longer exists remotely) |
| Current staging baseline | 54dd980bf7497fd61ac1a248c3a10697b80d7e33 |
| Current service status | Scaffold only; implementation packages/migrations/tests are placeholders |
| Foundation reuse | shared/go, API Gateway, Verification, Media & Document, Notification |
| Product source | Business Property Operations v5.0 + v6.0 competitor-benchmarked upgrade |

| Implementation mandate<br>Build `business-operations-service` as the canonical enterprise supply operating system. It owns organisation/staff/property/inventory/rental/sales/development/hospitality business state. It must not become Marketplace, payment ledger, verification authority, media store or human chat system. |
| --- |

## 1. Architecture boundaries

Unified Marketplace / Business web+mobile clients
                 |
             API Gateway
                 |
        business-operations-service
       /      |       |        \
 Verification Media Notification Payment*
      |         |        |       |
      +--- CF Meet / Manager / Artisan / Professional
      +--- Channel connectors / Maps / eSign / Smart access / Accounting
                 |
        business_operations_db
                 |
        transactional outbox
                 |
            NATS JetStream

* payment-service is the only authority allowed to execute provider money movement.

| Concern | Authority | Business-service responsibility |
| --- | --- | --- |
| Organisation/staff auth | business-operations-service | Accounts, credentials, sessions, SSO linkage, staff memberships, RBAC, approvals. |
| Enterprise properties/inventory | business-operations-service | Canonical source for business-owned/developed/operated/authorised inventory. |
| Verification truth | verification-service | Create/link KYB/property/professional cases; enforce results. |
| Media bytes/access lifecycle | media-document-service | Store media/document IDs and business-purpose relationships. |
| Payments/ledger/payouts | payment-service | Create business obligations, request execution, consume financial truth. |
| System notifications | notification-service | Request push/email/SMS; no direct provider calls. |
| Human messaging/voice/video | CF Meet | Create business context and authorization; no local chat store. |
| Customer discovery/booking channel | marketplace-service | Receive published projections; business service remains authoritative supply source. |
| External OTA/channel | business-operations-service connector modules | Map/sync approved external channels without giving them DB authority. |
| Property managers | property-manager-service | Business owns assignment/mandate policy; manager domain owns manager workspace/profile. |
| Artisans/providers | artisan-service | Business owns work request/approval/history; provider domain owns execution profile. |
| Professionals | professional-services-service | Business owns appointment/transaction linkage; professional domain owns provider work. |

## 2. Bounded-context package layout

services/business-operations-service/
├── cmd/{api,worker,migrate}/
├── internal/
│   ├── auth/                    # business/staff accounts, SSO, MFA
│   ├── organisation/            # company, branches, departments, teams
│   ├── workforce/               # staff, roles, scopes, shifts, handover
│   ├── approvals/               # policy engine, multi-level approvals, SoD
│   ├── finance/                 # business obligations, coding, beneficiaries refs
│   ├── portfolio/               # developments/estates/buildings/units/parcels/rooms
│   ├── authority/               # capacity, ownership, operator/manager authority
│   ├── content/                 # listings, media links, quality score, versions
│   ├── inventory/               # sellable units, holds, availability
│   ├── distribution/            # channel manager, mappings, sync health
│   ├── revenue/                 # rate plans, restrictions, RMS/dynamic pricing
│   ├── crm/                     # leads, accounts, guest/customer CRM
│   ├── reservations/            # canonical booking engine
│   ├── residential/
│   ├── commercial/
│   ├── land/
│   ├── sales/
│   ├── development/
│   ├── shortlet/
│   ├── hospitality/             # PMS/front desk/folio/night audit/group sales
│   ├── housekeeping/
│   ├── maintenance/
│   ├── delegation/
│   ├── professional/
│   ├── reputation/
│   ├── support/
│   ├── analytics/
│   ├── automation/
│   ├── risk/
│   ├── privacy/
│   ├── financing/
│   ├── insurance/
│   ├── access/
│   ├── integrations/
│   ├── events/
│   └── workers/
├── migrations/
├── sql/queries/
├── tests/{unit,integration,contract,e2e}/
└── Dockerfile

## 3. Account, organisation, RBAC and approvals

• Business domain owns all business/staff accounts; no central identity service.

• Use shared Go Ed25519 access JWTs, Argon2id, rotating refresh tokens, reuse detection, MFA, device/session management and service-auth primitives.

• Organisation scopes: company → region → branch → department → portfolio → property/development → unit/facility.

• Permission model combines role template + explicit capabilities + scope + effective dates + approval limit.

• Separation-of-duties rules prevent the same actor from initiating and self-approving configured high-risk actions.

• Approval policies are versioned and evaluated from amount, action, property scope, channel, staff role and risk class.

• Emergency break-glass requires reason, step-up authentication, expiry and mandatory audit/notification.

• SSO/SAML/OIDC and SCIM-style provisioning are adapter-ready but do not replace the domain account authority.

| Core table | Purpose |
| --- | --- |
| business_accounts | User/staff account root and status. |
| business_credentials | Password/OIDC/SSO credential metadata. |
| business_sessions | Refresh-token families and device session state. |
| business_devices | Trusted devices/risk/security events linkage. |
| business_mfa_methods | TOTP/SMS/email MFA configuration. |
| organisations | Legal organisation profile and verified capacity. |
| branches | Branch/region hierarchy and operating timezone/currency. |
| departments / teams | Operational structure. |
| staff_memberships | Account→organisation employment/contractor membership. |
| role_templates / permissions | Versioned role capabilities. |
| staff_scope_grants | Branch/portfolio/property/unit scoped grants. |
| approval_policies | Versioned multi-level approval rules. |
| approval_requests / approval_steps | Approval workflow and decisions. |
| delegation_grants | Temporary approval/permission delegation. |
| security_events / audit_events | Immutable security/business audit. |

## 4. Enterprise inventory graph

Use one canonical graph with typed parent/child rules rather than separate disconnected property tables. Every node has stable UUIDv7 ID, organisation owner, operational branch, timezone, currency/policy context, lifecycle and verification projection.

| Entity | Examples / rules |
| --- | --- |
| portfolio | Fund, owner entity, region, operating collection. |
| development | Project, estate, phase. |
| parcel | Land plot/parcel with geometry/survey refs. |
| property | Hotel, apartment block, office building, estate asset. |
| building / block / floor | Physical hierarchy. |
| unit | Apartment/shop/office/villa/standalone sellable or occupiable unit. |
| room_type | Sellable category for hospitality/short-let. |
| room | Physical room instance. |
| facility | Hall, spa, restaurant, pool, conference room, recreation. |
| common_area | Shared non-sellable operational space. |
| asset | Equipment/appliance/device tied to any inventory node. |

• Hierarchy changes are audited and cannot silently invalidate active reservations/leases/sales.

• Use external IDs and mapping tables for imported/channel objects rather than replacing internal IDs.

• Bulk import is asynchronous, resumable and idempotent with preview, validation report, dedupe and rollback plan.

• Digital-twin/3D floor-plan assets are references to media service artifacts plus structured room/space graph metadata.

## 5. Database catalogue

| Bounded context | Primary tables |
| --- | --- |
| Organisation & security | business_accounts, business_credentials, business_sessions, business_devices, business_mfa_methods, organisations, organisation_capacities, branches, departments, teams, staff_memberships, role_templates, permissions, staff_scope_grants, approval_policies, approval_requests, approval_steps, delegation_grants, security_events |
| Finance & coding | financial_profiles, payout_destination_refs, payout_change_requests, cost_centres, budgets, budget_lines, revenue_split_rules, tax_profiles, fiscal_periods, accounting_connection_refs, financial_exception_cases |
| Portfolio & authority | portfolios, developments, development_phases, parcels, properties, buildings, blocks, floors, units, room_types, rooms, facilities, common_areas, assets, inventory_edges, authority_records, authority_parties, authority_documents, verification_links, compliance_holds |
| Content & listings | listing_templates, listings, listing_versions, listing_media_links, content_quality_scores, listing_approvals, listing_publication, channel_content_overrides |
| Inventory & rate products | sellable_products, inventory_pools, inventory_calendar, inventory_holds, rate_plans, rate_plan_versions, rate_relations, rate_rules, rate_restrictions, promotions, packages, add_ons |
| Distribution/channel manager | channel_providers, channel_connections, channel_capabilities, channel_property_mappings, channel_product_mappings, channel_rate_mappings, channel_field_authority, channel_sync_jobs, channel_sync_cursors, channel_sync_failures, channel_conflicts, channel_reservation_refs, channel_commission_rules |
| CRM/marketing/support | customer_accounts_projection, guest_profiles, corporate_customer_profiles, lead_sources, leads, lead_assignments, lead_activities, segments, campaigns, campaign_members, support_cases, service_recovery_cases, consents |
| Reservations & hospitality | reservations, reservation_guests, reservation_rooms, reservation_modifications, reservation_holds, waitlist_entries, room_assignments, stay_records, folios, folio_items, cashier_shifts, cash_drawers, night_audits, business_day_closes, guest_requests, group_blocks, rooming_lists, corporate_rate_contracts, event_bookings |
| Residential/commercial/land | rental_applications, rental_offers, agreements, tenancies, rent_obligations, deposit_obligations, renewal_cases, notices, move_out_cases, commercial_terms, commercial_lois, land_terms, land_allocations, utility_accounts, service_charge_budgets |
| Sales/development | sale_transactions, sale_offers, sale_allocations, data_rooms, data_room_grants, due_diligence_items, closing_milestones, construction_milestones, progress_certifications, project_variations, buyer_allocations, snag_items, warranty_cases |
| Short-let/housekeeping | shortlet_settings, turnover_jobs, housekeeping_tasks, housekeeping_checklists, room_status_history, linen_stock_refs, lost_found_items, incident_cases |
| Maintenance/vendors | maintenance_requests, work_orders, vendor_quote_refs, maintenance_approvals, provider_assignment_refs, work_evidence_refs, warranties, preventive_schedules, asset_service_history, service_contract_refs |
| Managers/professionals | manager_assignment_refs, manager_permissions, manager_performance_refs, professional_appointments, professional_findings, professional_access_grants |
| Reviews/reputation | review_refs, review_responses, reputation_metrics, quality_issues, review_action_tasks |
| Analytics/RMS/AI | demand_snapshots, market_benchmarks, booking_pace_snapshots, forecast_runs, pricing_recommendations, pricing_decisions, experiments, analytics_snapshots, ai_recommendations, automation_rules, automation_runs |
| Risk/privacy/access | risk_signals, risk_cases, disputes, dispute_evidence_refs, privacy_requests, legal_holds, data_retention_jobs, access_devices, access_grants, access_events, insurance_policy_refs, insurance_claim_refs, finance_offer_refs |
| Platform | idempotency_records, outbox_events, processed_events, audit_events |

## 6. Distribution and Channel Manager Hub

| Strategic upgrade<br>This is the largest missing backend capability in V5. The Business service must become the canonical source that can distribute to CosmicForge Marketplace and approved external channels without allowing any external channel to become the internal system of record. |
| --- |

• Connector interface: authenticate, discover capabilities, map property/products/rates, push content, push ARI, pull/receive reservations, push reservation changes where supported, messaging, reviews, promotions, payments metadata and reporting.

• Per-channel capability registry and certification status; feature must be disabled if provider access/certification is unavailable.

• Field authority map: INTERNAL, CHANNEL_OVERRIDE, MANUAL_OVERRIDE with provenance and last-sync timestamps.

• Canonical mapping: internal property/product/rate IDs to external IDs; never use external IDs as primary keys.

• Delta queue/outbox for outbound sync; webhook and polling adapters for inbound changes.

• Sync cursor/checkpoint, retry with backoff, dead-letter/quarantine for persistent conflicts and operator reconciliation UI.

• Reservation ingestion must be idempotent on provider + external reservation/version.

• Availability decrement/hold is atomic before acknowledgement to minimize overbooking.

• Channel health: last success, lag, failed object count, unmapped products/rates, stale availability, provider outage.

• Channel profitability: gross revenue - commissions - payment fees - promotion cost - operational costs by source.

6.1 External connector reality

• Booking.com integration must be isolated behind an optional connector because its portal currently says onboarding new connectivity providers is paused. Build contracts/tests now; do not make launch depend on approval.

• Airbnb integration must be isolated behind an optional connector/API partner adapter. The internal channel engine is valuable even before access is granted.

• CosmicForge Marketplace connector is first-party and should be implemented first; it provides direct distribution independence.

## 7. Inventory, rate-plan and restriction engine

| Concept | Backend definition |
| --- | --- |
| Sellable product | Combination of inventory type + rate plan + occupancy/guest rules + policy version. |
| Rate plan | Refundable/non-refundable/corporate/package/etc. with meal/add-on/cancellation/prepayment policies. |
| Derived rate | Child plan calculated from parent by amount/percentage rule. |
| Occupancy pricing | Price varies by number/type of occupants. |
| LOS pricing | Price/restrictions depend on length of stay. |
| Booking window | Min/max advance booking. |
| Min/max LOS | Arrival/stay restrictions. |
| CTA/CTD | Closed to arrival / closed to departure. |
| Stop sell | Close a product/date/channel while preserving canonical inventory. |
| Allotment/release | Reserved channel/corporate inventory returned after release deadline. |
| Restriction conflict | Validator detects impossible/overlapping rules before publication. |

• All pricing uses amount_minor + currency. Tax/fee display mode is policy-driven.

• Accepted reservation stores immutable price/policy/rate snapshots.

• Bulk rate updates use versioned commands and dry-run preview.

• External channel pricing is derived from canonical plans plus channel-specific commission/strategy overrides—not duplicated unmanaged calendars.

## 8. Revenue Management System (RMS)

• Inputs: historical bookings, searches/conversion from Marketplace where available, occupancy, booking pace, pickup, cancellations/no-shows, events/holidays, channel source, competitor/peer/market datasets where legally/provider available.

• Outputs: demand forecast, recommended rate range, min/max stay, availability-window, last-minute/early-booker/LOS promotions, channel allocation and overbooking recommendation.

• Modes: MANUAL, RECOMMEND_ONLY, APPROVAL_AUTOMATION, AUTOPILOT_WITH_LIMITS.

• Autopilot is constrained by branch/property policy, floor/ceiling, maximum daily % change, approval threshold and blackout rules.

• Every recommendation stores model/version/features/provenance/confidence/explanation and operator decision.

• Pricing simulator estimates occupancy, ADR, RevPAR/revenue and channel-margin impact before applying.

• Performance metrics: ADR, RevPAR, occupancy, length of stay, lead time, pickup, pace, conversion, cancellation/no-show, net revenue and contribution margin.

| State | Meaning |
| --- | --- |
| DRAFT | Recommendation calculated, not exposed. |
| PENDING_APPROVAL | Requires configured manager/finance approval. |
| APPROVED | May be applied. |
| APPLIED | Canonical rate/restriction version updated and queued to channels. |
| OVERRIDDEN | Human replaced recommendation. |
| EXPIRED | Recommendation is stale. |
| ROLLED_BACK | Applied change reverted to prior known-good version. |

## 9. Canonical reservation engine

INQUIRY / HOLD
      |
 PENDING_PAYMENT / PENDING_APPROVAL
      |
  CONFIRMED
  /   |   \
MODIFIED ROOM_MOVED SPLIT/MERGED
  |
CHECKED_IN
  |
CHECKED_OUT
  |
COMPLETED

Side exits: CANCELLED | NO_SHOW | REJECTED | EXPIRED | DISPUTED

• Reservation source may be CosmicForge Marketplace, direct/business, external channel, walk-in, corporate/group or imported legacy booking.

• Inventory hold uses transactional/serializable locking or explicit inventory-version fencing to prevent double booking.

• Modification is append-only version history, never silent overwrite.

• Support multi-room, group, room change, extension, early departure, partial cancellation, no-show and waitlist.

• Store source/channel payment responsibility and immutable price/tax/fee/policy snapshots.

• External-channel updates are acknowledged only after durable local commit.

• Conflict queue handles cases where provider and local state disagree.

## 10. Hotel PMS, front desk, folio and business-day operations

• Arrivals/departures board, walk-in, reservation lookup, room assignment/move, registration/check-in, key/access issue, in-house guest state, extension and checkout.

• Room status is reconciled across front desk, housekeeping and maintenance: VACANT_CLEAN, VACANT_DIRTY, OCCUPIED, INSPECTED, BLOCKED, OUT_OF_ORDER.

• Folio supports accommodation, tax, deposit, meals, minibar, spa, laundry, transfer, event, damage and other charge postings; payment execution still goes through payment-service.

• Cashier shift: open/close, expected vs actual, variance, approval, handover and immutable report.

• Night audit: unresolved arrivals/departures/folio imbalances, room-status exceptions, posting cutoff, business-day rollover and close report.

• Offline-tolerant clients queue idempotent front-desk operations; server rejects stale conflicting commands and returns reconciliation tasks.

## 11. Guest CRM, loyalty and service recovery

• Domain-local CRM projection keyed by typed Marketplace/customer/channel guest reference; do not infer cross-domain identity from email/phone alone.

• Preferences, accessibility, language, stay history, company affiliation, VIP tags, consent, issues, recovery actions and repeat-stay metrics.

• Service recovery case can issue approved benefit/credit through policy + payment/Marketplace integration; all compensation auditable.

• Loyalty/rewards rules are optional and policy-driven; Marketplace remains customer-facing account authority for Marketplace users.

• Corporate account contracts support negotiated rate plans, traveler policy and billing references.

## 12. Unified communication and AI operations inbox

• CF Meet is the human conversation authority for CosmicForge-native communications.

• External channel messaging adapters mirror supported channel threads into a unified operational inbox while respecting provider policy and retention.

• Quick replies, scheduled replies, templates, read state (where available), priority/SLA and assignment.

• AI auto-replies only for safe factual answers derived from approved property/reservation content; clearly attributed and reversible.

• AI suggested-action cards can open reservation change, maintenance, refund, access or rate workflows but do not execute high-impact actions without permission/approval.

• Message content is never copied into notification-service; system push/email/SMS only alerts users back to the business context.

## 13. Reviews, reputation and quality loop

• Import/reference channel reviews where permitted, receive Marketplace verified-stay reviews, and allow authorized replies.

• Store source, external review ID, property/unit/booking reference, rating dimensions, response state and moderation status.

• Sentiment/topic extraction creates quality signals, not public review rewriting.

• Repeated issues create quality tasks for housekeeping/maintenance/front desk/operations.

• Fake/abusive review disputes route to channel/provider/admin process; original evidence/history remains immutable.

• Reputation dashboards by branch/property/channel and correlation to conversion, cancellation and repeat stays.

## 14. Marketing, direct commerce and conversion

• Leads and campaigns are attributed by source/campaign/listing/property/channel.

• CosmicForge Marketplace is the primary owned direct commerce channel; Business emits brand/property/package/inventory projections.

• Support packages, add-ons and upsells: breakfast, airport transfer, spa, events, early check-in, late checkout, cleaning and other lawful services.

• Campaign consent/unsubscribe is explicit. Notification-service sends system/broadcast messages only under approved category/consent rules.

• Conversion funnel: impression/search exposure (from Marketplace/channel where provided) → listing view → inquiry → hold → reservation → check-in → completed stay → review/repeat.

## 15. Enterprise finance integration

| Business service owns | Payment service owns |
| --- | --- |
| Revenue/expense obligation context, cost-centre/project/property coding, approval state. | Transaction execution, payment intents, ledger/journals, money movement. |
| Payout/revenue-split configuration intent. | Beneficiary validation, split execution/settlement and reconciliation. |
| Channel settlement/remittance expected amounts and source metadata. | Provider/channel payment ledger entries and financial truth where platform processes/reconciles. |
| Guest/customer folio business charges. | Actual collection/refund/chargeback/payment status. |
| Budget and spend approval. | Provider payment/payout execution. |

| Critical dependency<br>Full production certification of booking payments, refunds, payouts, channel VCC/settlement and financial close requires the full payment-service implementation. Business Operations must be coded against stable interfaces and fakes until that service is ready. |
| --- |

## 16. API surface

| API group | Required public routes |
| --- | --- |
| Auth / organisation | /api/v1/business/auth/*; /organisations; /branches; /departments; /staff; /roles; /permissions; /approvals |
| Portfolio / authority | /portfolios; /developments; /properties; /units; /parcels; /rooms; /facilities; /authority; /verification |
| Content / listings | /listings; /listing-templates; /content-quality; /bulk-imports; /publication |
| Inventory / rates | /inventory; /calendar; /rate-plans; /restrictions; /promotions; /packages; /availability |
| Distribution | /channels; /channel-connections; /mappings; /sync-health; /conflicts; /channel-actions |
| Revenue | /revenue/forecast; /revenue/recommendations; /revenue/simulations; /revenue/decisions |
| Reservations / front desk | /reservations; /holds; /waitlist; /arrivals; /departures; /room-assignments; /folios; /cashier-shifts; /night-audit |
| CRM / marketing | /guests; /corporate-customers; /leads; /segments; /campaigns; /service-recovery |
| Residential / commercial / land | /rentals; /tenancies; /commercial; /land; /sales |
| Development | /projects; /phases; /construction-milestones; /allocations; /snagging; /warranties |
| Housekeeping / maintenance | /housekeeping; /room-status; /maintenance; /work-orders; /assets; /preventive-maintenance |
| Managers / professionals | /manager-assignments; /professional-appointments; /data-rooms |
| Support / reputation | /support-cases; /reviews; /review-responses; /quality-tasks |
| Analytics / automation | /dashboards; /reports; /benchmarks; /ai-recommendations; /automation-rules |
| Risk / privacy | /risk-cases; /disputes; /privacy-requests; /legal-holds |
| Insurance / finance / access | /financing; /insurance; /access-devices; /access-grants |

• All side-effecting operations use `Idempotency-Key` when retries could duplicate publication, reservation, approval, payment, external sync or access issuance.

• Use optimistic version/If-Match or row locking for concurrent business objects.

• OpenAPI is the machine contract; Postman folder `03 Business Operations` is the official human/frontend handoff.

• Internal service routes live under `/internal/v1/*`; channel provider callbacks under `/webhooks/v1/<provider>/*` and are never exposed as frontend routes.

## 17. Internal API dependencies

| Dependency | Key internal contract |
| --- | --- |
| verification-service | KYB, property authority, licence/professional verification case create/read/status. |
| media-document-service | Signed upload/download, media metadata/status/access grants. |
| notification-service | System notifications, preferences, push endpoints. |
| payment-service | Payment intent, collection, refund, payout, beneficiary, ledger/reconciliation. |
| marketplace-service | Publish/read-back Marketplace projections, direct channel reservation/customer context. |
| property-manager-service | Manager search/proposals/assignment execution/status. |
| artisan-service | Provider discovery, quote/work execution/status. |
| professional-services-service | Professional search/appointment/report status. |
| admin-service | Moderation, manual review, disputes, restrictions, support operator actions. |
| CF Meet | Conversation/call/video contexts and participant authorization. |

## 18. Event catalogue

| Event | Purpose |
| --- | --- |
| business.verified.v1 | Organisation verification ready. |
| business.staff.updated.v1 | Staff membership/permission-relevant change. |
| property.created.v1 | Canonical business property created. |
| property.updated.v1 | Public/operational property fields changed. |
| listing.published.v1 | Publish to Marketplace/channel projection. |
| listing.updated.v1 | Listing version/public fields changed. |
| listing.withdrawn.v1 | Stop active discovery. |
| inventory.changed.v1 | Canonical availability/inventory changed. |
| rate_plan.changed.v1 | Canonical sellable rate product changed. |
| channel.connection.changed.v1 | Connector lifecycle/capability changed. |
| channel.sync.failed.v1 | Persistent sync issue needs operator attention. |
| reservation.created.v1 | Canonical reservation committed. |
| reservation.modified.v1 | Reservation version changed. |
| reservation.cancelled.v1 | Cancellation committed. |
| reservation.checked_in.v1 | Stay begins. |
| reservation.checked_out.v1 | Stay ends. |
| room.status.changed.v1 | Operational room status. |
| guest.service_recovery.created.v1 | Recovery case created. |
| review.received.v1 | Review/reputation input. |
| pricing.recommendation.created.v1 | RMS recommendation ready. |
| pricing.applied.v1 | Rate/restriction change committed. |
| manager.assignment.updated.v1 | Delegation changed. |
| maintenance.work_order.updated.v1 | Property maintenance state. |
| development.milestone.updated.v1 | Construction/off-plan progress. |
| access.grant.updated.v1 | Digital/physical access lifecycle. |
| night_audit.closed.v1 | Business day close completed. |

• Every business event uses UUIDv7 envelope, producer, aggregate type/id, occurred_at, trace_id and actor context.

• State + outbox are committed atomically.

• Consumers are idempotent; at-least-once delivery is assumed.

## 19. Background workers

| Worker | Responsibility |
| --- | --- |
| Outbox relay | Publish durable business events. |
| Channel outbound dispatcher | Push content/ARI/promotions/messaging actions. |
| Channel inbound poller/webhook processor | Reservations/messages/reviews/payments metadata where provider supports. |
| Sync reconciliation | Compare canonical state vs channel snapshots and quarantine drift. |
| Reservation hold expiry | Release expired holds and inventory. |
| Availability/freshness | Stale inventory warnings and safe pause. |
| Revenue forecast | Generate demand/pace forecasts and recommendations. |
| Pricing publisher | Apply approved recommendations and distribute deltas. |
| Housekeeping scheduler | Create turnover/cleaning/inspection tasks. |
| Night audit | Validate and close business day. |
| Cashier reconciliation | Flag expected/actual variances. |
| Maintenance preventive scheduler | Create recurring asset/service work. |
| Verification/licence expiry | Restrict affected capacity/listings. |
| Manager/staff access expiry | Revoke time-limited grants. |
| Privacy retention/legal hold | Delete/anonymize when eligible. |
| Analytics snapshots | Materialize command-center metrics. |
| External import/migration | Resumable bulk migration jobs. |
| Smart access expiry/health | Revoke expired codes and raise device alerts. |

## 20. Key state machines

Organisation

DRAFT → KYB_PENDING → ACTIVE | RESTRICTED | SUSPENDED → CLOSURE_PENDING → CLOSED

Staff membership

INVITED → ACTIVE → SUSPENDED | HANDOVER_PENDING → TERMINATED

Approval request

DRAFT → PENDING → APPROVED | REJECTED | EXPIRED | CANCELLED

Authority

DRAFT → EVIDENCE_PENDING → VERIFYING → VERIFIED | REJECTED | CONFLICT; VERIFIED → EXPIRED/REVOKED/HOLD

Listing

DRAFT → REVIEW → APPROVED → PUBLISHED → PAUSED | WITHDRAWN | ARCHIVED

Channel connection

DISCONNECTED → AUTHORIZING → CONNECTED → DEGRADED | SUSPENDED → DISCONNECTED

Sync job

QUEUED → RUNNING → SUCCEEDED | RETRY | QUARANTINED | FAILED_FINAL

Reservation

HOLD/PENDING → CONFIRMED → CHECKED_IN → CHECKED_OUT → COMPLETED; side exits CANCELLED/NO_SHOW/DISPUTED

Room status

VACANT_DIRTY → CLEANING → INSPECTION → VACANT_CLEAN → OCCUPIED; BLOCKED/OUT_OF_ORDER side states

Folio

OPEN → SETTLEMENT_PENDING → SETTLED → CLOSED; may be DISPUTED before close

Night audit

OPEN → VALIDATING → EXCEPTIONS | READY → CLOSED

Pricing recommendation

DRAFT → PENDING_APPROVAL → APPROVED → APPLIED | OVERRIDDEN | EXPIRED | ROLLED_BACK

Housekeeping task

PLANNED → ASSIGNED → IN_PROGRESS → INSPECTION → DONE | REWORK | CANCELLED

Development allocation

AVAILABLE → HELD → RESERVED → ALLOCATED → CONTRACTED → HANDED_OVER; CANCELLED/RELEASED side exits

Manager assignment

PROPOSED → ACTIVE → SUSPENDED | EXPIRING → REVOKED | EXPIRED | TERMINATED

## 21. Security, privacy and enterprise controls

• Step-up MFA for bank/payout changes, high-value refunds, sale approval, business ownership change, privileged document grants and break-glass access.

• Field/row authorization evaluates organisation, branch, department, portfolio/property scope, action, approval level, delegation and compliance hold.

• Provider/channel credentials are secret-managed; never stored in Postman or logs.

• Sensitive customer documents are media-service references with purpose/time-limited grants.

• Channel webhooks require provider signature/authentication and replay protection.

• Staff activity, approval, pricing, payout, access, channel mapping and reservation overrides are append-only audited.

• Fraud/risk decisions that materially restrict a user/business require review/appeal path where appropriate.

• Data retention honors active transaction, dispute, tax/accounting and legal-hold requirements.

## 22. Resilience and concurrency

• Canonical inventory updates use fencing/version checks so concurrent channels/staff cannot oversell the same unit/room.

• Outbound channel operations use idempotency keys/provider request IDs where supported; ambiguous outcomes are read back before retry.

• Webhook/poll events are deduped by provider event/reservation version.

• External network calls occur outside DB transactions unless impossible; authoritative state is committed before durable work is queued.

• Connector/provider outages degrade only affected channel; CosmicForge Marketplace and internal operations remain available.

• Front desk offline commands carry client operation IDs and expected versions; server returns conflicts for human reconciliation.

• Dead-letter/quarantine queues never silently drop reservations or payment-related events.

## 23. Postman and OpenAPI handoff

• Populate `03 Business Operations` in the existing CosmicForge Postman collection.

• Folders: Auth & Organisation; Staff/RBAC; Approvals; Portfolio; Listings; Inventory; Rates; Channels; Revenue; Reservations; Front Desk; Housekeeping; CRM; Rental; Commercial; Land; Sales; Development; Maintenance; Managers; Professionals; Reviews; Analytics; Risk; Privacy; Access.

• Each implemented frontend route includes auth, examples, relevant errors and tests; create commands capture IDs into environment variables.

• `_Internal`, `_Webhooks`, `_Admin` remain separate from frontend folders.

• CI compares route methods/paths across handlers, OpenAPI and Postman.

## 24. CI, migration and certification gates

| Gate | Required proof |
| --- | --- |
| Go quality | gofmt, go vet, staticcheck, tidy-clean, architecture import check. |
| Database | Goose empty→current; safe down/up; sqlc diff; real PostgreSQL integration. |
| Auth/RBAC | cross-org/branch/property isolation, SoD, approvals, delegated/expired permission, step-up actions. |
| Inventory | atomic holds, concurrent booking, oversell prevention, hierarchy constraints. |
| Channels | mapping, webhook dedup, polling cursor, retry, outage, conflict quarantine, sync reconciliation. |
| Reservations | create/modify/cancel/no-show/room move/split/merge/group; source idempotency. |
| RMS | pricing math, floors/ceilings, approval bands, rollback, stale recommendation. |
| PMS | front-desk flows, room-status reconciliation, folio/cashier/night audit. |
| Housekeeping | task lease/recovery, room-ready gate, maintenance block. |
| Finance contracts | payment-service mocks/contracts, unknown outcomes, no direct provider/payment calls. |
| Events | outbox recovery, broker outage, duplicate/poison events. |
| Privacy/security | gitleaks, govulncheck, Trivy, secrets/logs, document access, legal holds. |
| Contracts | OpenAPI valid, event JSON schemas, Postman coverage/drift. |
| Docker | distroless non-root image, health/readiness/version, graceful SIGTERM. |
| E2E | organisation onboarding; portfolio publish; Marketplace reservation; external-channel simulated reservation; hotel check-in/out/night audit; RMS rate update; development allocation. |

## 25. Implementation workstreams

| Workstream | Scope |
| --- | --- |
| BO-1 Bootstrap + business auth/KYB | Module, config, DB, org accounts, sessions/MFA, business verification links. |
| BO-2 Organisation/RBAC/approvals | Branches/departments/staff/scopes/SoD/approval engine. |
| BO-3 Portfolio/inventory graph | Developments, properties, units, rooms, facilities, bulk import/dedupe. |
| BO-4 Authority/verification/content | Authority records, listing/content versions, media, readiness/quality. |
| BO-5 Inventory/rate-plan engine | Sellable products, availability, holds, rate plans/restrictions/promotions. |
| BO-6 Channel Manager Hub | Connector contracts, mappings, sync queues, Marketplace first-party connector, simulated external connector. |
| BO-7 Revenue Management System | Forecasts, comp-set adapters, recommendations, simulator, approval/autopilot. |
| BO-8 CRM/marketing/support/reputation | Leads, guest CRM, campaigns, service recovery, reviews. |
| BO-9 Residential/commercial/land | Rental, commercial lease/sale and parcel workflows. |
| BO-10 Sales/development/off-plan | Sale pipeline, data rooms, allocations, milestones, snagging/warranty. |
| BO-11 Short-let canonical booking | Calendars, bookings, guest ops, turnover, damage/claims. |
| BO-12 Hotel PMS/front desk | Room assignment, folios, walk-ins, group/corporate bookings. |
| BO-13 Housekeeping/night audit/cashier | Operational boards, close procedures, shift accountability. |
| BO-14 Maintenance/vendors/assets | Work orders, SLA, provider integration, lifecycle/PM. |
| BO-15 Managers/professionals/data rooms | Delegation, access grants, findings. |
| BO-16 Analytics/AI/automation | Command center, forecasts, action cards, rules, provenance. |
| BO-17 Risk/privacy/insurance/financing/access | Risk cases, DSR, partner refs, smart access. |
| BO-18 Full certification | Contracts/Postman/events, E2E, security, image, staging deploy readiness. |

## 26. Production blockers / deferred external dependencies

• Full payment-service is required before real collections, refunds, payouts, ledger/settlement and channel-payment reconciliation can be production-certified.

• Production KYB/property verification adapters must exist behind verification-service; sandbox-only verification is insufficient for live operation.

• Booking.com connector activation depends on Booking.com connectivity-partner access/certification; their portal currently states new connectivity provider integrations are paused.

• Airbnb connector activation depends on Airbnb software/API partner access and provider review.

• Production email/SMS/WhatsApp providers remain configuration/partner decisions; Notification Service is provider-neutral.

• Electronic signature, smart-lock, accounting/POS, market-data, financing and insurance provider selection remain adapters, not hard-coded dependencies.

## 27. Competitive implementation acceptance

☐ Multi-channel inventory and rate sync works without external channels becoming source of truth.

☐ CosmicForge Marketplace operates as a first-party direct channel.

☐ Dynamic pricing/revenue recommendations match or exceed current Airbnb/Booking host insight capabilities in explainability and enterprise control.

☐ Hotel/short-let operations include front desk, folio, housekeeping, maintenance, smart access and night audit—well beyond OTA partner dashboards.

☐ Enterprise RBAC/approvals exceed co-host/team permission models.

☐ Messaging includes automation/AI/action suggestions while keeping CF Meet as native human communication authority.

☐ Review/reputation and service recovery are first-class operational workflows.

☐ Channel profitability and settlement visibility expose true net revenue by source.

☐ Land, commercial and development/off-plan remain native—not bolted-on accommodation features.

☐ Every public endpoint is handed to frontend/QA through Postman and backed by OpenAPI.

## Appendix A. Research benchmark sources

| Source | Used for | URL |
| --- | --- | --- |
| Booking.com Connectivity APIs | Content, rooms/rates, availability, reservations, reporting, messaging, reviews, promotions, performance, payments onboarding | https://developers.booking.com/connectivity/docs |
| Booking.com Rates & Availability | Inventory, restrictions, Standard/RLO/OBP/LOS pricing | https://developers.booking.com/connectivity/docs/ari |
| Booking.com Room Type & Rate Plan | Room types, rate plans, roomrates and conditions | https://developers.booking.com/connectivity/docs/room-type-and-rate-plan-management/understanding-room-types-and-rate-plans |
| Booking.com Messaging API | Guest conversations, attachments, structured requests, historical messages | https://developers.booking.com/connectivity/docs/messaging-api/understanding-the-messaging-api |
| Booking.com Market Insights | Booking window, pace, sales statistics, peer/competitive-set/market comparisons | https://developers.booking.com/connectivity/docs/market-insights-api/understanding-the-market-insights-api |
| Booking.com Payments API | Payout and VCC-related payment information | https://developers.booking.com/connectivity/docs/payments-api/understanding-the-payments-api |
| Booking.com Guest Review API | Retrieve reviews/scores and post property replies | https://developers.booking.com/connectivity/docs/review-api |
| Airbnb Professional Hosting Tools | Multi-calendar, rule-sets, fees, tasks and insights | https://www.airbnb.com/help/article/2499 |
| Airbnb 2026 Host Tools | Dynamic pricing, multi-listing calendar, AI earnings insights, 3D floor plans | https://www.airbnb.com/resources/hosting-homes/a/new-tools-to-host-smarter-not-harder-776 |
| Airbnb Dynamic Pricing | Demand-based daily pricing, competitiveness and 52-week forecasting | https://www.airbnb.com/resources/hosting-homes/a/introducing-dynamic-pricing-777 |
| Airbnb Messaging 2026 | AI auto-replies, suggested actions and priority messages | https://www.airbnb.com/resources/hosting-homes/a/respond-faster-in-the-messages-tab-770 |
| Airbnb Reservations 2026 | Reservation actions, notes, alerts, cancellation cutoffs and earnings breakdown | https://www.airbnb.com/resources/hosting-homes/a/reservations-redesigned-765 |
| Airbnb Co-host Permissions | Listing/calendar/reservation/damage-management permissions | https://www.airbnb.com/help/article/1534 |
| Airbnb Smart Locks | Automatic time-limited guest codes and lock status | https://www.airbnb.com/help/article/3478 |
| Airbnb Software Partners | PMS/channel-management ecosystem for professional operators | https://www.airbnb.com/software-partners |
| Airbnb PMS Connectivity | Software-connected listing and calendar management | https://www.airbnb.com/help/article/2683 |

## Appendix B. Current repository baseline

• Remote branches observed: `staging` and `main`; `development` is no longer present.

• `staging` HEAD during this audit: 54dd980bf7497fd61ac1a248c3a10697b80d7e33.

• `business-operations-service` currently contains the approved scaffold only (`cmd`, `internal`, migrations/sql/tests placeholders) and is ready for implementation.

• The architecture remains 7 user-domain services + 4 shared platform services + API Gateway, one database per service, no cross-service DB access.

• Postman remains the official human/frontend API handoff; OpenAPI remains the machine contract.

| Final backend target<br>When BO-1 through BO-18 are complete, Business Property Operations should function as CosmicForge's enterprise supply operating system: PMS + channel manager + RMS + CRM + deep real-estate operations, with the Unified Marketplace as the owned demand layer and external OTAs as optional distribution channels. |
| --- |

## Implementation Annex D. Detailed V6 feature-to-backend map

This annex closes the gap between the upgraded product definition and actual code. Each product section has an owning bounded context, database scope, API family, integrations/events and a concrete certification target. These are implementation requirements, not optional examples.

### D.1 Business Types and Operating Capacities

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | organisation, auth, policy |
| Primary tables | organisations, organisation_capacities, capability_packs |
| Public/API family | /business/capabilities; /business/profile |
| Internal/external integrations | verification-service |
| Events | business.capacity.changed.v1 |
| Acceptance focus | Unsupported or expired capacity disables prohibited routes/publication immediately. |

### D.2 Business Registration, Security and Verification

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | auth, organisation, risk |
| Primary tables | business_accounts, credentials, sessions, devices, mfa_methods, verification_links, security_events |
| Public/API family | /auth/*; /business/verification; /security/* |
| Internal/external integrations | verification-service, notification-service |
| Events | business.verified.v1; business.restricted.v1 |
| Acceptance focus | KYB/licence expiry, representative replacement, MFA/session/device controls tested. |

### D.3 Organisation, Branches, Departments and Staff

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | organisation, workforce, approvals |
| Primary tables | branches, departments, teams, staff_memberships, role_templates, staff_scope_grants, approval_policies |
| Public/API family | /branches; /departments; /staff; /roles; /approvals |
| Internal/external integrations | notification-service |
| Events | staff.updated.v1; approval.completed.v1 |
| Acceptance focus | Cross-branch/property isolation, SoD, temporary delegation and termination handover. |

### D.4 Enterprise Banking, Finance and Approval Controls

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | finance, approvals |
| Primary tables | financial_profiles, payout_destination_refs, budgets, cost_centres, revenue_split_rules, financial_exception_cases |
| Public/API family | /finance/*; /budgets; /payout-destinations; /approval-policies |
| Internal/external integrations | payment-service, accounting adapters |
| Events | finance.approval.completed.v1 |
| Acceptance focus | No direct money-provider calls; high-risk actions require configured approvals/step-up. |

### D.5 Enterprise Property Identity, Portfolio and Unit Hierarchy

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | portfolio, inventory |
| Primary tables | portfolios, developments, phases, parcels, properties, buildings, blocks, floors, units, room_types, rooms, facilities, assets, inventory_edges |
| Public/API family | /portfolios; /developments; /properties; /units; /rooms; /facilities; /imports |
| Internal/external integrations | media-document-service, maps |
| Events | property.created.v1; property.updated.v1 |
| Acceptance focus | Hierarchy constraints, bulk import idempotency, dedupe and stable external IDs. |

### D.6 Corporate Ownership, Authority and Property Verification

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | authority, policy |
| Primary tables | authority_records, authority_parties, authority_documents, verification_links, compliance_holds |
| Public/API family | /authority; /verification; /compliance-holds |
| Internal/external integrations | verification-service, media-document-service, admin-service |
| Events | property.authority.changed.v1 |
| Acceptance focus | Listing/transaction operations cannot exceed verified active capacity. |

### D.7 Bulk Listing and Inventory Management

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | content, listing |
| Primary tables | listing_templates, listings, listing_versions, listing_media_links, listing_approvals, content_quality_scores |
| Public/API family | /listings; /listing-templates; /bulk-listings; /content-quality |
| Internal/external integrations | media-document-service, marketplace-service |
| Events | listing.published.v1; listing.updated.v1; listing.withdrawn.v1 |
| Acceptance focus | Dry-run/preview, approval, quality rules, immutable published versions. |

### D.8 Marketing, Customer Engagement and Lead Management

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | crm, marketing |
| Primary tables | lead_sources, leads, assignments, activities, segments, campaigns, campaign_members, consents |
| Public/API family | /leads; /segments; /campaigns |
| Internal/external integrations | marketplace-service, notification-service, CF Meet |
| Events | lead.updated.v1; campaign.updated.v1 |
| Acceptance focus | Consent/unsubscribe, attribution, SLA routing and duplicate merge. |

### D.9 Residential Rental Operations

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | residential |
| Primary tables | rental_applications, rental_offers, agreements, tenancies, rent_obligations, deposit_obligations, notices, renewal_cases, move_out_cases |
| Public/API family | /rentals; /applications; /agreements; /tenancies |
| Internal/external integrations | payment-service, media, notification, CF Meet |
| Events | tenancy.started.v1; tenancy.ended.v1 |
| Acceptance focus | Complete rental lifecycle with payment projections and data-minimisation. |

### D.10 Commercial Property Operations

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | commercial |
| Primary tables | commercial_terms, commercial_lois, agreements, tenancies, service_charge_budgets |
| Public/API family | /commercial/*; /lois; /commercial-tenancies |
| Internal/external integrations | verification, payment, professionals |
| Events | commercial.transaction.updated.v1 |
| Acceptance focus | Corporate applicant/terms/fit-out/obligation lifecycle. |

### D.11 Land Rent, Lease, Sale and Parcel Operations

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | land, sales |
| Primary tables | parcels, land_terms, land_allocations, due_diligence_items, closing_milestones |
| Public/API family | /land/*; /parcels; /land-transactions |
| Internal/external integrations | maps/GIS, professional-services, verification, payment |
| Events | land.transaction.updated.v1 |
| Acceptance focus | Geometry/title/lease/sale/perfection tracking with professional evidence. |

### D.12 Property Sales and Buyer Management

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | sales, crm |
| Primary tables | sale_transactions, sale_offers, sale_allocations, data_rooms, due_diligence_items, closing_milestones |
| Public/API family | /sales/*; /buyers; /data-rooms |
| Internal/external integrations | marketplace, payment, professional-services, media |
| Events | sale.transaction.updated.v1 |
| Acceptance focus | Offer→conditions→payment milestones→closing with unit holds and audit. |

### D.13 Developer and Off-Plan Project Operations

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | development, sales |
| Primary tables | construction_milestones, progress_certifications, project_variations, buyer_allocations, snag_items, warranty_cases |
| Public/API family | /development/*; /allocations; /progress; /snagging |
| Internal/external integrations | payment, media, professional-services, notification |
| Events | development.milestone.updated.v1 |
| Acceptance focus | No overselling; certified progress and versioned payment plans. |

### D.14 Short-Let Business Operations

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | shortlet, reservations, distribution |
| Primary tables | shortlet_settings, inventory_calendar, rate_plans, reservations, turnover_jobs, incident_cases |
| Public/API family | /shortlet/*; /reservations |
| Internal/external integrations | marketplace, channel connectors, payment, CF Meet, access |
| Events | reservation.*; inventory.changed.v1 |
| Acceptance focus | Atomic date inventory, cross-channel booking, turnover/damage/service recovery. |

### D.15 Hotel and Resort Property Structure

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | portfolio, inventory |
| Primary tables | room_types, rooms, facilities, sellable_products, property_service_catalogue |
| Public/API family | /hotel/room-types; /rooms; /facilities; /sellable-products |
| Internal/external integrations | media, marketplace, channels |
| Events | hotel.inventory.changed.v1 |
| Acceptance focus | Room/facility structure and sellable products remain canonical. |

### D.16 Hotel and Resort Reservations and Front Desk

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | reservations, hospitality |
| Primary tables | reservations, reservation_rooms, modifications, holds, waitlist_entries, room_assignments, stay_records, folios |
| Public/API family | /reservations; /front-desk/*; /folios |
| Internal/external integrations | payment, CF Meet, notification, access |
| Events | reservation.created/modified/cancelled/checked_in/checked_out |
| Acceptance focus | Walk-in/online/group, room move, checkout, incidentals and conflict-safe inventory. |

### D.17 Housekeeping, Guest Service and Hospitality Maintenance

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | housekeeping, maintenance |
| Primary tables | housekeeping_tasks, checklists, room_status_history, guest_requests, lost_found_items, maintenance_requests |
| Public/API family | /housekeeping; /room-status; /guest-requests |
| Internal/external integrations | artisan, notification |
| Events | room.status.changed.v1; housekeeping.task.updated.v1 |
| Acceptance focus | Room-ready gate, supervisor inspection, maintenance blocking and SLA. |

### D.18 Hospitality Channel, Pricing and Revenue Management

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | revenue, inventory, distribution |
| Primary tables | rate_plans, rate_relations, rate_rules, restrictions, promotions, demand_snapshots, forecast_runs, pricing_recommendations |
| Public/API family | /rates; /restrictions; /promotions; /revenue/* |
| Internal/external integrations | marketplace, channels, market-data adapters |
| Events | pricing.recommendation.created.v1; pricing.applied.v1 |
| Acceptance focus | RMS math, approval/autopilot limits, rollback and channel propagation. |

### D.19 Property Manager and Management Company Delegation

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | delegation, approvals |
| Primary tables | manager_assignment_refs, manager_permissions, manager_performance_refs |
| Public/API family | /manager-assignments; /manager-permissions |
| Internal/external integrations | property-manager-service |
| Events | manager.assignment.updated.v1 |
| Acceptance focus | Fine-grained, scoped, expiring grants; revocation immediate. |

### D.20 Maintenance, Vendors, Service Companies and Assets

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | maintenance, assets |
| Primary tables | maintenance_requests, work_orders, vendor_quote_refs, maintenance_approvals, warranties, preventive_schedules, asset_service_history |
| Public/API family | /maintenance; /work-orders; /assets |
| Internal/external integrations | artisan-service, payment-service |
| Events | maintenance.work_order.updated.v1 |
| Acceptance focus | Quote→approval→work→evidence→rework/warranty with SLA. |

### D.21 Professional Services and Secure Data Rooms

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | professional, data room |
| Primary tables | professional_appointments, professional_findings, data_rooms, data_room_grants |
| Public/API family | /professional-appointments; /data-rooms |
| Internal/external integrations | professional-services, media, payment |
| Events | professional.appointment.updated.v1 |
| Acceptance focus | Purpose/time-limited access and finding-to-condition workflow. |

### D.22 Customer Support, Communication and Notifications

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | support, communications |
| Primary tables | support_cases, service_recovery_cases, communication_context_refs |
| Public/API family | /support; /communication-contexts |
| Internal/external integrations | CF Meet, notification, channel messaging adapters |
| Events | support.case.updated.v1 |
| Acceptance focus | Unified inbox metadata, SLA, AI suggestions and provider-safe separation. |

### D.23 Business Reporting and Analytics

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | analytics, reporting |
| Primary tables | analytics_snapshots, market_benchmarks, report_jobs, ai_recommendations |
| Public/API family | /dashboards; /reports; /benchmarks |
| Internal/external integrations | marketplace, payment, channel/market data |
| Events | analytics.snapshot.ready.v1 |
| Acceptance focus | KPI definitions, source provenance, materialised snapshots and asynchronous exports. |

### D.24 Privacy, Security, Fraud and Disputes

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | risk, privacy |
| Primary tables | risk_signals, risk_cases, disputes, dispute_evidence_refs, privacy_requests, legal_holds |
| Public/API family | /risk; /disputes; /privacy |
| Internal/external integrations | admin, media, verification |
| Events | risk.case.updated.v1; dispute.updated.v1 |
| Acceptance focus | Manual review, appeal, privileged-access audit and legal holds. |

### D.25 Accessibility, Assisted Migration and Business Closure

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | migration, organisation |
| Primary tables | migration_jobs, migration_errors, organisation_change_cases, closure_cases |
| Public/API family | /migrations; /business-changes; /closure |
| Internal/external integrations | admin, media |
| Events | migration.completed.v1; business.closure.updated.v1 |
| Acceptance focus | Resumable imports, M&A/name/beneficial-owner changes and record continuity. |

### D.26 Financing, Mortgage and Customer Payment Plans

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | financing |
| Primary tables | finance_offer_refs, financing_consents, financing_status_projection |
| Public/API family | /financing/* |
| Internal/external integrations | financing partners, payment-service |
| Events | financing.status.updated.v1 |
| Acceptance focus | Consent/data-scope and total-cost transparency. |

### D.27 Enterprise Insurance and Protection

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | insurance, risk |
| Primary tables | insurance_policy_refs, insurance_claim_refs, insurance_compliance_checks |
| Public/API family | /insurance/* |
| Internal/external integrations | insurance partners, media, payment |
| Events | insurance.claim.updated.v1 |
| Acceptance focus | Coverage/compliance monitoring and claim evidence. |

### D.28 Smart Access, Keys and Property Devices

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | access, IoT |
| Primary tables | access_devices, access_grants, access_events, device_health_snapshots |
| Public/API family | /access/devices; /access/grants |
| Internal/external integrations | smart-access adapters, notification |
| Events | access.grant.updated.v1; access.device.alert.v1 |
| Acceptance focus | Time-bounded codes, health/offline backup and automatic expiry. |

### D.29 Distribution and Channel Manager Hub

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | distribution |
| Primary tables | channel_connections, capabilities, property/product/rate mappings, field_authority, sync_jobs, cursors, failures, conflicts |
| Public/API family | /channels; /mappings; /sync-health; /conflicts |
| Internal/external integrations | Marketplace + optional OTA adapters |
| Events | channel.connection.changed.v1; channel.sync.failed.v1 |
| Acceptance focus | Delta sync, idempotent inbound reservations, quarantine and reconciliation. |

### D.30 Advanced Revenue Management System

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | revenue |
| Primary tables | demand_snapshots, market_benchmarks, booking_pace_snapshots, forecast_runs, pricing_recommendations, pricing_decisions |
| Public/API family | /revenue/forecast; /recommendations; /simulations |
| Internal/external integrations | market data, marketplace, channel performance |
| Events | pricing.recommendation.created.v1 |
| Acceptance focus | Explainable forecasts, floors/ceilings, approval and bounded autopilot. |

### D.31 Unified Reservation Command Center

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | reservations |
| Primary tables | reservations, reservation_modifications, holds, waitlist, source_refs |
| Public/API family | /reservation-command-center; /reservations |
| Internal/external integrations | Marketplace/channels, payment, notification |
| Events | reservation.* |
| Acceptance focus | Canonical reservation lifecycle across every source. |

### D.32 Guest CRM, Loyalty and Service Recovery

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | crm |
| Primary tables | guest_profiles, corporate_customer_profiles, stay_history_projection, loyalty_accounts, service_recovery_cases |
| Public/API family | /guests; /corporate-customers; /loyalty; /service-recovery |
| Internal/external integrations | marketplace, notification, payment |
| Events | guest.profile.updated.v1; service_recovery.updated.v1 |
| Acceptance focus | Consent-aware CRM, benefits and repeat-stay lifecycle. |

### D.33 Reputation and Review Operations

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | reputation |
| Primary tables | review_refs, review_responses, reputation_metrics, quality_issues, review_action_tasks |
| Public/API family | /reviews; /reputation; /quality-tasks |
| Internal/external integrations | marketplace, channel review adapters |
| Events | review.received.v1 |
| Acceptance focus | Reply/moderation/service-recovery loop and provider constraints. |

### D.34 AI Operations and Automation Center

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | automation, analytics |
| Primary tables | ai_recommendations, automation_rules, automation_runs, action_cards |
| Public/API family | /ai/recommendations; /automation-rules; /action-cards |
| Internal/external integrations | all domain modules via use-case interfaces |
| Events | automation.action.proposed.v1 |
| Acceptance focus | Provenance/confidence, approval boundaries and no hidden high-impact automation. |

### D.35 Direct Commerce and Brand Control

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | content, marketing |
| Primary tables | brand_profiles, storefront_collections, packages, add_ons, promotion_codes |
| Public/API family | /brand; /storefront; /packages; /add-ons |
| Internal/external integrations | marketplace-service |
| Events | storefront.updated.v1 |
| Acceptance focus | Business controls supply/brand; Marketplace controls customer discovery/checkout. |

### D.36 Integration and Partner Platform

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | integrations |
| Primary tables | integration_clients, api_credentials, webhook_subscriptions, integration_health |
| Public/API family | /integrations; /webhooks/config; /api-clients |
| Internal/external integrations | external partners |
| Events | integration.health.changed.v1 |
| Acceptance focus | Scoped credentials, rate limits, sandbox and no DB access. |

### D.37 Enterprise Tax, Currency and Fiscal Controls

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | finance, policy |
| Primary tables | tax_profiles, tax_rules, currency_policies, fx_rate_refs, fiscal_periods |
| Public/API family | /tax; /currency; /fiscal-periods |
| Internal/external integrations | payment, accounting, tax adapters |
| Events | fiscal.policy.updated.v1 |
| Acceptance focus | Jurisdictional policy packs and price display/settlement consistency. |

### D.38 Night Audit, Cashier and Business-Day Close

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | hospitality, finance |
| Primary tables | cashier_shifts, cash_drawers, night_audits, business_day_closes, close_exceptions |
| Public/API family | /cashier; /night-audit; /business-day |
| Internal/external integrations | payment-service |
| Events | night_audit.closed.v1 |
| Acceptance focus | Cannot close with unresolved configured exceptions; immutable report. |

### D.39 Group, Corporate, Conference and Event Sales

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | hospitality, sales, crm |
| Primary tables | group_blocks, rooming_lists, corporate_rate_contracts, event_bookings, master_folios |
| Public/API family | /groups; /corporate-rates; /events |
| Internal/external integrations | payment, notification, CF Meet |
| Events | group.booking.updated.v1 |
| Acceptance focus | Blocks/allotments, group billing and facility inventory. |

### D.40 Content Intelligence, Digital Twin and 3D

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | content, portfolio |
| Primary tables | digital_twin_refs, space_graphs, content_quality_scores, content_suggestions |
| Public/API family | /digital-twin; /content-quality |
| Internal/external integrations | media, maps/3D provider |
| Events | content.quality.changed.v1 |
| Acceptance focus | Operator confirms extracted/suggested content; drift tracked. |

### D.41 Offline Continuity and Disaster Recovery

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | platform/reservations/front desk |
| Primary tables | offline_operation_receipts, reconciliation_tasks, branch_incidents |
| Public/API family | /offline-sync; /reconciliation |
| Internal/external integrations | none required for core replay |
| Events | reconciliation.required.v1 |
| Acceptance focus | Idempotent replay, expected versions and deterministic conflict response. |

### D.42 Trust, Reservation Risk and Protection

| Implementation field | Requirement |
| --- | --- |
| Owning bounded context | risk, reservations |
| Primary tables | reservation_risk_projection, risk_signals, protection_case_refs, incident_cases |
| Public/API family | /reservation-risk; /protection-cases |
| Internal/external integrations | verification, marketplace, admin, insurance |
| Events | reservation.risk.changed.v1 |
| Acceptance focus | Risk is advisory/reviewable; significant restrictions have human review. |

## Implementation Annex E. Required route catalogue

Authentication & security

• POST /api/v1/business/auth/register

• POST /api/v1/business/auth/login

• POST /api/v1/business/auth/refresh

• POST /api/v1/business/auth/logout

• POST /api/v1/business/auth/mfa/enrol

• POST /api/v1/business/auth/mfa/verify

• GET /api/v1/business/sessions

• DELETE /api/v1/business/sessions/{session_id}

Organisation & workforce

• GET/PATCH /api/v1/business/organisation

• GET/POST /api/v1/business/branches

• GET/POST /api/v1/business/departments

• GET/POST /api/v1/business/teams

• GET/POST /api/v1/business/staff

• PATCH /api/v1/business/staff/{staff_id}

• GET/POST /api/v1/business/roles

• GET/POST /api/v1/business/approval-policies

• GET/POST /api/v1/business/approvals

• POST /api/v1/business/approvals/{id}/decision

Portfolio & inventory hierarchy

• GET/POST /api/v1/business/portfolios

• GET/POST /api/v1/business/developments

• GET/POST /api/v1/business/properties

• GET/POST /api/v1/business/properties/{id}/units

• GET/POST /api/v1/business/properties/{id}/room-types

• GET/POST /api/v1/business/properties/{id}/rooms

• GET/POST /api/v1/business/properties/{id}/facilities

• POST /api/v1/business/imports

• GET /api/v1/business/imports/{id}

Authority & verification

• GET/POST /api/v1/business/authority

• POST /api/v1/business/authority/{id}/verification

• GET /api/v1/business/verification

• GET/POST /api/v1/business/compliance-holds

Listings/content

• GET/POST /api/v1/business/listings

• GET/PATCH /api/v1/business/listings/{id}

• POST /api/v1/business/listings/{id}/preview

• POST /api/v1/business/listings/{id}/submit-approval

• POST /api/v1/business/listings/{id}/publish

• POST /api/v1/business/listings/{id}/pause

• POST /api/v1/business/listings/{id}/withdraw

• GET /api/v1/business/listings/{id}/quality

Inventory/rates/restrictions

• GET/PUT /api/v1/business/inventory

• GET/POST /api/v1/business/rate-plans

• GET/PATCH /api/v1/business/rate-plans/{id}

• GET/POST /api/v1/business/restrictions

• GET/POST /api/v1/business/promotions

• POST /api/v1/business/rates/bulk-preview

• POST /api/v1/business/rates/bulk-apply

Channels

• GET/POST /api/v1/business/channels/connections

• POST /api/v1/business/channels/{id}/authorize

• GET/POST /api/v1/business/channels/{id}/mappings

• GET /api/v1/business/channels/{id}/sync-health

• POST /api/v1/business/channels/{id}/resync

• GET /api/v1/business/channel-conflicts

• POST /api/v1/business/channel-conflicts/{id}/resolve

Revenue management

• GET /api/v1/business/revenue/dashboard

• POST /api/v1/business/revenue/forecast

• GET /api/v1/business/revenue/recommendations

• POST /api/v1/business/revenue/recommendations/{id}/approve

• POST /api/v1/business/revenue/recommendations/{id}/override

• POST /api/v1/business/revenue/simulations

Reservations/front desk

• GET/POST /api/v1/business/reservations

• GET/PATCH /api/v1/business/reservations/{id}

• POST /api/v1/business/reservations/{id}/cancel

• POST /api/v1/business/reservations/{id}/check-in

• POST /api/v1/business/reservations/{id}/room-move

• POST /api/v1/business/reservations/{id}/extend

• POST /api/v1/business/reservations/{id}/check-out

• GET /api/v1/business/front-desk/arrivals

• GET /api/v1/business/front-desk/departures

• GET/POST /api/v1/business/waitlist

Folio/cashier/night audit

• GET/POST /api/v1/business/folios

• POST /api/v1/business/folios/{id}/items

• POST /api/v1/business/cashier-shifts/open

• POST /api/v1/business/cashier-shifts/{id}/close

• GET /api/v1/business/night-audit

• POST /api/v1/business/night-audit/validate

• POST /api/v1/business/night-audit/close

Housekeeping/maintenance

• GET/POST /api/v1/business/housekeeping/tasks

• PATCH /api/v1/business/housekeeping/tasks/{id}

• GET/PUT /api/v1/business/room-status/{room_id}

• GET/POST /api/v1/business/maintenance

• GET/POST /api/v1/business/work-orders

• POST /api/v1/business/work-orders/{id}/approve

CRM/marketing/support

• GET/POST /api/v1/business/leads

• PATCH /api/v1/business/leads/{id}

• GET/POST /api/v1/business/guests

• GET/POST /api/v1/business/corporate-customers

• GET/POST /api/v1/business/segments

• GET/POST /api/v1/business/campaigns

• GET/POST /api/v1/business/support-cases

• GET/POST /api/v1/business/service-recovery

Residential/commercial/land/sales

• GET/POST /api/v1/business/rental-applications

• GET/POST /api/v1/business/tenancies

• GET/POST /api/v1/business/commercial/deals

• GET/POST /api/v1/business/land/transactions

• GET/POST /api/v1/business/sales

• POST /api/v1/business/sales/{id}/offers/{offer_id}/decision

• GET/POST /api/v1/business/data-rooms

Development/off-plan

• GET/POST /api/v1/business/projects

• GET/POST /api/v1/business/projects/{id}/phases

• GET/POST /api/v1/business/projects/{id}/milestones

• GET/POST /api/v1/business/projects/{id}/allocations

• GET/POST /api/v1/business/projects/{id}/snagging

• GET/POST /api/v1/business/projects/{id}/warranties

Managers/professionals

• GET/POST /api/v1/business/manager-assignments

• PATCH /api/v1/business/manager-assignments/{id}

• POST /api/v1/business/manager-assignments/{id}/revoke

• GET/POST /api/v1/business/professional-appointments

• GET/POST /api/v1/business/professional-appointments/{id}/data-room-grants

Reviews/reputation

• GET /api/v1/business/reviews

• POST /api/v1/business/reviews/{id}/reply

• GET /api/v1/business/reputation

• GET/POST /api/v1/business/quality-tasks

Analytics/automation

• GET /api/v1/business/dashboards

• POST /api/v1/business/reports

• GET /api/v1/business/benchmarks

• GET /api/v1/business/ai/recommendations

• GET/POST /api/v1/business/automation-rules

• POST /api/v1/business/automation-rules/{id}/enable

Risk/privacy/access

• GET/POST /api/v1/business/risk-cases

• GET/POST /api/v1/business/disputes

• GET/POST /api/v1/business/privacy-requests

• GET/POST /api/v1/business/access/devices

• GET/POST /api/v1/business/access/grants

• POST /api/v1/business/access/grants/{id}/revoke

Financing/insurance/fiscal

• GET/POST /api/v1/business/financing

• GET/POST /api/v1/business/insurance/policies

• GET/POST /api/v1/business/insurance/claims

• GET/PUT /api/v1/business/tax-policy

• GET/PUT /api/v1/business/currency-policy

• GET/POST /api/v1/business/fiscal-periods

Groups/events

• GET/POST /api/v1/business/groups

• GET/POST /api/v1/business/groups/{id}/rooming-list

• GET/POST /api/v1/business/corporate-rates

• GET/POST /api/v1/business/events

Integrations/offline

• GET/POST /api/v1/business/integrations

• GET /api/v1/business/integrations/{id}/health

• POST /api/v1/business/offline-sync

• GET /api/v1/business/reconciliation-tasks

• POST /api/v1/business/reconciliation-tasks/{id}/resolve

## Implementation Annex F. High-risk permission and approval matrix

| Action | Minimum authority | Additional controls |
| --- | --- | --- |
| Change business payout destination | Finance capability + organisation scope | Step-up MFA; dual/multi approval; cooling-off; notification. |
| Publish property/listing | Listing capability + verified property capacity | Listing approval policy; verification/compliance gate. |
| High-value refund | Finance/refund capability | Amount threshold approval; payment-service idempotency. |
| Change rate outside policy band | Revenue capability | Revenue/finance approval depending deviation. |
| Override inventory conflict | Reservations/inventory supervisor | Reason required; audit; channel reconciliation. |
| Overbook inventory | Explicit overbooking capability | Property policy; limit; manager approval. |
| Check in without configured prerequisite | Front desk supervisor | Override reason; risk/payment exception audit. |
| Close night audit with exception | Night-audit supervisor | Exception-specific approval; immutable close report. |
| Grant external manager access | Organisation/property admin | Verified mandate; scoped capabilities; expiry. |
| Grant professional data-room access | Transaction owner | Purpose/expiry; watermark/download policy. |
| Change staff role/scope | Organisation admin | SoD validation; no self-escalation. |
| Break-glass access | Designated senior role | Recent MFA; reason; short expiry; immediate security notification. |
| Bulk disposal / sale approval | Sales executive + board/signatory policy | Authority verification; approval chain. |

## Implementation Annex G. Critical sequence contracts

### G.1 Publish inventory to Marketplace and channels

Business user -> business-operations-service: publish listing/rate/inventory command
business-operations-service: verify capacity + approval + content quality + inventory
business-operations-service: commit canonical version + outbox atomically
outbox -> NATS -> marketplace-service: listing/inventory projection
channel dispatcher -> approved external connectors: delta content/ARI
connector: provider acknowledgement/read-back
business-operations-service: update sync state; conflicts -> reconciliation queue

### G.2 Inbound external reservation

Channel webhook/poller -> connector adapter
adapter: verify signature/auth + normalize provider version
business-operations-service: dedupe(provider, external_reservation_id, version)
DB transaction: lock inventory pool/date; validate capacity; create reservation; decrement/hold inventory; outbox
commit
connector: acknowledge provider
outbox -> Marketplace/Notification/analytics as relevant
if inventory conflict -> quarantine + operator alert, never silently overwrite

### G.3 CosmicForge Marketplace reservation

Marketplace -> internal authenticated reservation command
business-operations-service: verify sellable product + price snapshot + inventory
DB transaction: atomic hold/confirm + reservation + outbox
payment-service: payment intent/collection (outside DB transaction)
payment outcome event/read-back -> reservation state
business-operations-service -> Marketplace: authoritative status

### G.4 Dynamic pricing application

forecast worker -> demand/pace/market inputs
RMS -> pricing recommendation + explanation/confidence
policy engine -> manual / approval / autopilot threshold
approval -> create immutable rate version
DB commit + outbox
Marketplace + channel dispatchers receive rate delta
post-apply monitoring -> conversion/ADR/RevPAR/net-margin
rollback if operator or policy trigger

### G.5 Hotel check-in and checkout

Front desk -> reservation readiness
verify room status + identity/risk/payment prerequisites
check-in -> room OCCUPIED + stay ACTIVE + access grant
during stay -> folio postings + guest requests + maintenance
checkout -> final folio review -> payment-service settlement/refund
room -> VACANT_DIRTY -> housekeeping -> INSPECTION -> VACANT_CLEAN
access automatically revoked

### G.6 Night audit

night-audit worker/operator -> validate business day
check unresolved arrivals/departures, open folios, cashier variances, room-state inconsistencies
if exceptions -> EXCEPTIONS state + assigned queue
after resolution/authorized overrides -> READY
close -> immutable audit report + fiscal/business-day rollover + event

### G.7 Off-plan allocation

Buyer/Marketplace -> unit hold
business service -> atomic availability hold
verification/payment prerequisites
approval -> reservation/allocation
milestone schedule version frozen
construction progress certifications update project state
payment-service events update milestone projections
snagging -> correction -> final inspection -> handover/warranty

## Implementation Annex H. KPI definitions for the command center

| KPI | Definition / use |
| --- | --- |
| Occupancy | Occupied/sold room nights ÷ available room nights for configured inventory scope. |
| ADR | Accommodation room revenue ÷ sold room nights. |
| RevPAR | Accommodation room revenue ÷ available room nights. |
| Net RevPAR / contribution | Net channel/accommodation contribution ÷ available room nights after configured commissions/fees. |
| Booking lead time | Reservation creation to check-in. |
| Pickup | Change in booked room nights/revenue for a future stay period between two observation dates. |
| Pace | Current future bookings/revenue versus prior comparable observation or target. |
| Cancellation rate | Cancelled reservations ÷ gross reservations, segmented by source/rate plan. |
| No-show rate | No-show reservations ÷ confirmed due arrivals. |
| Conversion | Search/listing/inquiry-to-booking stages when upstream data is available. |
| Channel margin | Revenue less channel commission, payment fee, promotion and attributable variable cost. |
| Response SLA | Time from inquiry/support/message arrival to first qualifying response. |
| Housekeeping turnaround | Checkout/dirty timestamp to inspected/clean timestamp. |
| Maintenance SLA | Open/triage/assigned/completed cycle time by severity. |
| Guest recovery rate | Service-recovery cases resolved within target and subsequent satisfaction/repeat outcome. |
| Inventory sync freshness | Age of last successful channel update/read-back per mapped product. |
| Content quality | Weighted completeness/quality score across required content/media/accuracy. |

## Implementation Annex I. Postman folder contract

03 Business Operations
├── 00 Auth & Security
├── 01 Organisation & Staff
├── 02 Roles, Permissions & Approvals
├── 03 Portfolio & Property Hierarchy
├── 04 Authority & Verification
├── 05 Listings & Content
├── 06 Inventory & Availability
├── 07 Rate Plans, Restrictions & Promotions
├── 08 Channel Manager
├── 09 Revenue Management
├── 10 Reservations
├── 11 Front Desk
├── 12 Folio, Cashier & Night Audit
├── 13 Guest CRM & Marketing
├── 14 Residential
├── 15 Commercial
├── 16 Land
├── 17 Sales & Buyer Management
├── 18 Development & Off-Plan
├── 19 Short-Let
├── 20 Housekeeping
├── 21 Maintenance & Assets
├── 22 Managers
├── 23 Professional Services & Data Rooms
├── 24 Support & Service Recovery
├── 25 Reviews & Reputation
├── 26 Analytics & AI
├── 27 Risk, Privacy & Disputes
├── 28 Financing & Insurance
├── 29 Smart Access
├── 30 Group / Corporate / Event Sales
├── 31 Integrations & Offline Sync
├── _Internal
├── _Webhooks
└── _Admin

• Every frontend-facing implemented route must exist in this collection and OpenAPI.

• Create operations capture IDs into environment variables.

• Provider webhooks and internal service JWT calls are never mixed into frontend folders.

• Collection tests validate status, envelope, required identifiers and representative state transitions.

## Implementation Annex J. Final service closure criteria

☐ BO-1 through BO-18 each have truthful PASS/PARTIAL/FAIL closure reports and commits.

☐ `business_operations_db` migrations are reproducible from empty database and sqlc output is current.

☐ All route, event and Postman/OpenAPI drift tests pass.

☐ Marketplace first-party channel integration is working before optional OTA connectors are treated as complete.

☐ Channel simulation tests prove reservation deduplication, inventory fencing, sync recovery and conflict quarantine.

☐ RMS recommendation/apply/rollback and approval policies pass race/concurrency tests.

☐ Hotel front desk, housekeeping, folio, cashier and night audit E2E pass.

☐ Residential/commercial/land/development flows preserve service boundaries and payment/verification/media authority.

☐ No production flow directly calls a payment provider from business-operations-service.

☐ No raw media/document bytes or human chat history are stored in business_operations_db.

☐ All security scans and race tests pass with required integration infrastructure; no required test is skipped.

☐ Image is distroless/non-root, health/readiness/version endpoints pass and SIGTERM is graceful.

☐ Staging deployment migration/readiness/E2E/rollback proof is green before any main promotion.
