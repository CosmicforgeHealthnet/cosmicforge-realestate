# Section 10 PMS status

Status: **FAIL — required domain workflows remain unimplemented**. This is an internal implementation gap, not an external infrastructure blocker. Existing reservation and inventory functionality is not a substitute for PMS certification.

| Requirement | Current evidence | Remaining action |
|---|---|---|
| PMS data model/front desk | Canonical reservations and property-local command centre exist; no PMS stay/business-day model | Implement owning PMS persistence and dashboard |
| Room assignment/status/readiness | Existing physical inventory IDs and `reservations.RequirePhysicalBacking` reject outstanding overbooking | Implement unique physical room/date assignments, status/readiness transitions and inspection authority |
| Walk-in/check-in/out/in-house operations | Canonical WALK_IN source accepted under reservation policy; checked-in/out lifecycle is not exposed as a completed PMS command | Implement actual stay, readiness, risk, guest, financial and approval prerequisites atomically |
| Folios/payment reconciliation | Signed reservation Payment readback exists; no hotel folio workflow | Implement immutable obligations/adjustments and actual Payment references without a second ledger |
| Cashier shifts/drawers | Not implemented | Implement counted custody/variance controls and independent approvals |
| Night audit/business-day close | Not implemented | Implement property timezone/business-day locking, reconciliation, exception approvals and irreversible close history |
| Offline operations | Not implemented | Implement authenticated idempotent command replay, conflict/version handling and explicit offline authority |
| Group/corporate front desk | Whole-line canonical groups/split/merge exist | Implement rooming/arrival/stay/corporate workflows using real CRM contracts |
| Workers, APIs, events, OpenAPI/Postman | No PMS endpoints documented as implemented | Implement and test owning workflows before adding their published contracts |

No Section 10 migration is claimed. Goose 00010 and 00011 belong to RMS. No PMS workflow/security/concurrency/E2E test is certified. Existing reservation/inventory regression and image smoke results certify their actual scope only. Future smart-access operations require an authenticated owning contract/provider certification; that external dependency does not excuse the unfinished internal PMS work. The full requirement matrix is in the Sections 8–11 audit and combined certificate.
