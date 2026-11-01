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
