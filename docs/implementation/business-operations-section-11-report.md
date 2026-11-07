## 7 November 2026 closure pass — Section 11 CRM

Status: **PARTIAL**. Implemented (migrations 00025/00026): service recovery cases/actions/compensation with exact approval and verified outcomes (refunds stay pending on the Admin-owned contract), loyalty programmes/rules/rewards/accounts/ledger/redemptions/entitlements/reversals, privacy requests with authorised export, legal holds enforced in the database, retention/anonymisation worker, VIP classifications, closed source-backed segments with consent-following marketing eligibility, and CRM reporting. All 16 former `REQUIRED_CURRENT_WORK` catalogue tables are implemented and tested. Remaining internal work: guest identity merge workflow and CRM-triggered Notification dispatch/readback. See `business-operations-sections-08-11-final-closure.md` for the requirement matrix, tests and CI evidence of the 7 November 2026 closure pass.

## Current schema18 stay-history implementation

Status remains **PARTIAL for full Section11**. The new owned projection implements requirement5.6 and the history portion of5.14: `internal/crm/history.go`, `history_projector.go`, `internal/http/operational_crm_history.go`, migration00018 and SQLC queries. It records actual reservation source, corporate reference, accepted room type where authoritative, physical arrival/departure, unique civil nights, cancellation/no-show state and current signed Payment-backed folio obligations. Unfinished/in-house nights are null; promised nights never become actual guest nights. Actual completed visits remain factual when later monetary evidence expires or reverses; spend becomes unknown. No net ledger revenue or provider success is invented.

`GET /api/v1/business/guests/{id}/stay-history` requires current guest, reservation and folio read capabilities in the validated property, audits the read, returns no-store and reloads current facts rather than stale cached money. Pending/source-review counts expose incomplete projection work. `POST .../stay-history/rebuild` requires current guest version, guest write, reservation read, recent MFA and idempotency. Rebuilds retain immutable history. The existing shared worker consumes real reservation/stay/folio events with event-ID deduplication, reloads authoritative rows and reconciles due roots after restart/retention. Source errors roll back through a savepoint while retaining the organisation fence, back off without starving other guests and enter quarantine after twelve failures; an authorized rebuild clears repaired sources.

Executed focused certification: **23 tests passed, zero failed/skipped**, covering existing CRM identity/corporate/upgrade, current PMS closure/revocation, new history and DST calculations, real PostgreSQL migrations/catalogue, real disk-backed JetStream outage/recovery, duplicate deliveries, forged producer/payload handling, immutable histories, current financial expiry/reversal, source-permission failure/quarantine and authenticated rebuild recovery. The complete local schema18 module passed **224 tests, zero failed/skipped**; two final PostgreSQL/JetStream security and quarantine tests passed after the final source-review refinement. Gofmt, vet, staticcheck, tidy-diff, exact SQLC output, contract/examples/event/catalogue drift, architecture and service gitleaks checks passed. Exact schema18 pushed-SHA CI remains pending at this report update. Current generated counts: Goose18, 426 routes, 385 typed schemas, 74 events, 203 tables. The separate schema17 certificate records actual prior CI; it is not substituted for this candidate.

Remaining internal CRM work: full identity merge/VIP/segments, privacy/legal-hold/export/retention, optional loyalty, recovery/compensation, consent-aware Notification dispatch/readback, cohort metrics and the remaining cross-domain journeys. PMS/reservation/RMS gaps remain tracked separately. These are not external blockers.

Current verification (November 5): fresh disposable PostgreSQL18 full Business module run **207 passed, zero failed/skipped**; local quality, contracts, SQLC, architecture and service secret scan passed. Linux race/container and exact-SHA CI/deployment status require subsequent verification.

# Section 11 identity and corporate foundation

Status: **PARTIAL — implementation continues**. Migration 00012 and secured API commands now implement domain-local guest identity, protected preferences/contacts, purpose consents, source assertions and corporate contract eligibility. This does not certify full CRM/loyalty/recovery.

Starting/fetched staging SHA: `13801be24a431b8ef33201c3c2ddeb7b16167b2e`. Current owned implementation: `internal/crm/identity.go`, `booking.go`, `internal/http/operational_crm_*.go`, `sql/queries/crm.sql` and generated SQLC repositories.

| Requirement | Implemented evidence | Remaining work |
|---|---|---|
| Typed guest identity | UUIDv7 tenant/property profiles and explicit local registration; unique opaque typed source keys. Marketplace assertions require signed owning service identity, exact delegated organisation and certified source connection. Immutable monotonic observations, source renewal/expiry/revocation and independently approved identity links. | Merge/conflict case workflow, OTA owning-source assertion producer/adapter and full event projection reconciliation. No email/phone merge is performed. |
| Profile/preferences/accessibility | Existing protected store and rotated keys encrypt core profile and property-private fields with separate AAD. Explicit private capability, current purpose, actual reservation context or MFA reviewer; reads are audited and no-store. No contact details in public/idempotent responses or events. | VIP/segments and complete privacy retention/export/anonymization/legal-hold workflow. |
| Consent | Immutable capture/source/evidence/version, separate operational/marketing/preferences/accessibility/loyalty purposes, current-time eligibility and withdrawal. Withdrawal remains possible as a narrow protective action under an organisation restriction without overriding role/session/scope checks. | Dispatch-time consent reconciliation and delivery-specific retention. |
| Corporate contracts | Protected company contacts, explicit approved cross-property associations, independently approved immutable contract versions tied to actual canonical rate plans, source/amount/currency/responsibility/night limits and approved named-traveler policies. Suspension/revocation retain their approved terms. Reservation creation and confirmation recheck current eligibility. | Full corporate group/master-folio/mixed-payer integration. Actual credit and settlement remain Payment-owned. |
| Stay history/loyalty/recovery | Schema18 now implements the idempotent/rebuildable actual guest-stay projection, immutable evidence, real broker recovery and current monetary missingness. | Cohort metrics, optional loyalty/accrual/redemption/reversal, recovery cases/remedies/fulfilment and overbooking composition remain **internal work**. |
| Notifications | Existing signed Notification boundary is retained; no local provider delivery. | Guest-purpose dispatch orchestration, acceptance/delivery readback and complete supported template/provider integration. |

Executed real PostgreSQL tests in `tests/integration/crm_identity_test.go` cover protected-field minimisation, consent withdrawal, cross-tenant/branch denial, concurrent consent, local typed binding, genuine corporate contract→traveler→reservation→confirmation, signed/forged source assertions, source renewal/expiry/revocation and narrow restricted-organisation withdrawal. `crm_upgrade_test.go` builds the genuine schema11 via supported HTTP booking journeys, applies new migrations twice and verifies unchanged accepted price/policy/reservation/stock before creating a real CRM identity. Staticcheck caught and fixed a shadowed SQL error in traveler revocation; failures propagate rather than silently completing.

New APIs and event schemas are generated from functioning handlers. Full module/CI evidence is recorded by the current certification run; mocks are not live Marketplace, Payment, Notification or KYB certification. Live source producer, Notification templates/delivery configuration and Admin-authorized refund handoff/readback remain exact external contracts. Unfinished CRM domain commands above are not classified as external blockers.

---

<details><summary>Historical schema11 checkpoint (superseded by the current status above)</summary>

# Section 11 CRM, loyalty and service recovery status

Status: **FAIL — required domain workflows remain unimplemented**. This is an internal implementation gap, distinct from unavailable external identity/delivery/refund contracts.

| Requirement | Current evidence | Remaining action |
|---|---|---|
| Canonical guest identity/CRM projection | Reservations accept typed source references; arbitrary canonical guest IDs fail closed | Implement owned tenant-scoped canonical profiles, approved source links and authenticated ownership/readback contracts |
| Preferences/accessibility/consent/privacy | Not implemented | Implement protected purpose-scoped fields, consent history, access restrictions and privacy lifecycle |
| Stay history | Not implemented | Consume idempotent authoritative PMS stay outcomes after implementing PMS |
| Corporate customer accounts | Canonical corporate bindings fail closed | Implement real company/contact/contract authority and Payment-owned financial references |
| Loyalty/rewards | Not implemented | Implement auditable accrual/redemption/reversal with idempotent source references and bounded benefits |
| Recovery cases/compensation | Reservation conflict and overbooking recovery references remain explicitly unresolved; no customer-resolution claim | Implement actual owned recovery cases, independent compensation authorization and confirmed fulfilment/outcome references |
| Notifications/service boundaries | Existing signed Notification contract is available; no independent email/SMS implementation | Compose actual guest identity, consent, supported templates, durable acceptance and delivery reconciliation |
| Metrics/workers | Not implemented | Implement real cohort/provenance/outcome calculations and existing-scheduler workers; unknown outcomes remain unknown |
| APIs/events/OpenAPI/Postman | No CRM endpoints documented as implemented | Add contracts only with functioning secured workflows |

No Section 11 migration or workflow/security/concurrency/E2E certification is claimed. Goose 00011 is RMS governance. Missing owning-service operations include source identity linking/readback, eligible Notification templates/delivery configuration and Admin-authorized refund handoff/readback. Business Operations must not merge identities from email/phone alone, impersonate Admin, execute Payment provider operations or mark a conflict/customer recovery resolved from inventory reconciliation. Existing tests prove the fail-closed reservation boundary, not completed CRM functionality. The Sections 8–11 audit and combined certificate retain every high-level requirement and status.

</details>
