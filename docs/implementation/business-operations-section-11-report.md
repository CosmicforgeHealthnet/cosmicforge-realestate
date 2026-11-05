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
| Stay history/loyalty/recovery | PMS now records real owned stays and canonical checked-in/out events. | Idempotent/rebuildable guest stay projection, cohort metrics, optional versioned loyalty/accrual/redemption/reversal, recovery cases/remedies/approval/fulfilment and overbooking composition remain **internal work**. |
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
