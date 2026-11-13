# Business Operations Section 20 — State Machines

New in this delivery: a single generic database guard `business_ops_state_guard(edges)` (migration 00031) enforces, for every Sections 12–16 governed table: immutable identity/tenant/scope, `version = version + 1` on every update, and only the declared FROM>TO edges. Terminal states have no outgoing edge. `TestBusinessDomainStateGuardsMatchGeneratedTransitions` asserts the guards exist; tests attempt illegal direct SQL transitions (support case RESOLVED→OPEN, housekeeping DONE→PLANNED) and assert rejection.

| Section 20 machine | Enforcement | Evidence |
|---|---|---|
| Organisation | existing organisation guard/lifecycle (Section 3) | existing organisation tests |
| Staff membership | existing membership history/guards | existing staff tests |
| Approval request | existing approval engine (DRAFT→PENDING→APPROVED/REJECTED/EXPIRED/CANCELLED), exact consumption | existing approvals tests; consumed by every new exact approval |
| Authority | existing verification links/inventory authority | existing capacity/verification tests |
| Listing | existing listing guard (DRAFT→REVIEW→APPROVED→PUBLISHED→PAUSED/WITHDRAWN/ARCHIVED) | existing commercial tests |
| Channel connection, sync job | existing distribution guards | existing channel tests |
| Reservation | existing reservation transition commit guard | existing reservation tests |
| Room status | existing PMS room state | existing PMS tests |
| Folio, night audit | existing PMS guards | existing PMS/night audit tests |
| Pricing recommendation | existing RMS states | existing RMS tests |
| Housekeeping task | **new** generated guard: PLANNED→ASSIGNED→IN_PROGRESS→INSPECTION→DONE / REWORK / CANCELLED; PASS/FAIL require `room_status:inspect` and a different person; room-ready gate blocks release while a room-blocking work order is open | `TestBusinessHousekeepingRoomReadyGateInspectionSeparationAndMaintenanceBlock`, `TestJourneyJ24CheckoutTurnoverInspectionReadyRoom` |
| Development allocation | **new**: AVAILABLE→HELD→RESERVED→ALLOCATED→CONTRACTED→HANDED_OVER, RELEASED/CANCELLED exits; one live allocation per unit; ALLOCATE needs exact approval + MFA and freezes the milestone schedule; handover blocked by open snags; held allocations auto-release | `TestBusinessDevelopmentAllocationMilestonesSnagsAndHandover` |
| Manager assignment | **new**: PROPOSED→ACTIVE→SUSPENDED/EXPIRING→REVOKED/EXPIRED/TERMINATED; activation needs Annex F approval + MFA | `TestBusinessManagerDelegationScopedExpiringRevocableAndEnforced` |

Additional governed machines (all database-guarded): messaging connections, communication contexts, inbox threads, scheduled replies, AI suggestions, action cards, automation rules, support cases, reviews, review responses, topic signals, quality issues/tasks, review disputes, lead sources, leads, campaigns, storefronts, upsell offers/acceptances, fiscal periods, budgets, obligations, expense requests, payout destinations/changes, revenue splits, channel settlements, financial exceptions, report jobs, and the 26 Section 16 domains.

Every transition: current authorization (live session membership/grants), version check under row lock, Idempotency-Key replay, outbox event, immutable `operations_history` + `audit_events`.

## Addendum — migration 00038 state machines

Each table below has a `business_ops_state_guard` trigger with exactly these edges (`TestAnnexDDatabaseGuardsMatchEveryGeneratedDomain`).

| Table | Edges |
|---|---|
| rental_offers | DRAFT>ISSUED, ISSUED>ACCEPTED, ISSUED>DECLINED, DRAFT>WITHDRAWN, ISSUED>WITHDRAWN, ISSUED>EXPIRED, ACCEPTED>CONVERTED |
| agreements | DRAFT>ISSUED, ISSUED>SIGNED, SIGNED>SUPERSEDED, DRAFT>VOIDED, ISSUED>VOIDED |
| rent_obligations | SCHEDULED>LINKED, DUE>LINKED, OVERDUE>LINKED, LINKED>SETTLED, OVERDUE>SETTLED, SCHEDULED>WAIVED, DUE>WAIVED, OVERDUE>WAIVED, SCHEDULED>CANCELLED, SCHEDULED>DUE, DUE>OVERDUE, LINKED>OVERDUE |
| deposit_obligations | REQUESTED>COLLECTION_PENDING, COLLECTION_PENDING>HELD, HELD>RELEASE_REQUESTED, DISPUTED>RELEASE_REQUESTED, HELD>DISPUTED, RELEASE_REQUESTED>DISPUTED, RELEASE_REQUESTED>RELEASED, REQUESTED>CANCELLED |
| notices | DRAFT>SERVED, SERVED>ACKNOWLEDGED, SERVED>DISPUTED, ACKNOWLEDGED>DISPUTED, DRAFT>WITHDRAWN, SERVED>WITHDRAWN, ACKNOWLEDGED>WITHDRAWN, DISPUTED>WITHDRAWN, SERVED>EFFECTIVE, ACKNOWLEDGED>EFFECTIVE |
| renewal_cases | OPEN>OFFERED, OFFERED>ACCEPTED, OFFERED>DECLINED, ACCEPTED>COMPLETED, OPEN>CANCELLED, OFFERED>CANCELLED, OPEN>LAPSED, OFFERED>LAPSED |
| move_out_cases | SCHEDULED>INSPECTED, INSPECTED>SETTLEMENT_PROPOSED, DISPUTED>SETTLEMENT_PROPOSED, SETTLEMENT_PROPOSED>DISPUTED, SETTLEMENT_PROPOSED>SETTLED, SETTLED>CLOSED, SCHEDULED>CANCELLED |
| utility_accounts | ACTIVE>TRANSFER_PENDING, TRANSFER_PENDING>TRANSFERRED, TRANSFER_PENDING>ACTIVE, ACTIVE>CLOSED |
| commercial_lois | DRAFT>ISSUED, ISSUED>COUNTERSIGNED, COUNTERSIGNED>SUPERSEDED, DRAFT>WITHDRAWN, ISSUED>WITHDRAWN, ISSUED>EXPIRED |
| commercial_terms | PROPOSED>APPROVED, PROPOSED>REJECTED, APPROVED>SUPERSEDED |
| service_charge_budgets | DRAFT>APPROVED, APPROVED>RECONCILED, RECONCILED>CLOSED, DRAFT>CANCELLED |
| land_terms | PROPOSED>APPROVED, PROPOSED>REJECTED, APPROVED>SUPERSEDED |
| land_allocations | PROVISIONAL>CONFIRMED, CONFIRMED>PERFECTED, PROVISIONAL>DISPUTED, CONFIRMED>DISPUTED, PERFECTED>DISPUTED, DISPUTED>CONFIRMED, PROVISIONAL>REVOKED, CONFIRMED>REVOKED, DISPUTED>REVOKED, PROVISIONAL>RELEASED |
| due_diligence_items | OPEN>IN_REVIEW, IN_REVIEW>SATISFIED, IN_REVIEW>FAILED, OPEN>WAIVED, IN_REVIEW>WAIVED, FAILED>WAIVED, FAILED>OPEN |
| closing_milestones | PENDING>EVIDENCED, REJECTED>EVIDENCED, EVIDENCED>CONFIRMED, EVIDENCED>REJECTED, PENDING>WAIVED |
| sale_allocations | HELD>CONVERTED, HELD>RELEASED, CONVERTED>RELEASED, HELD>EXPIRED |
| progress_certifications | SUBMITTED>PROFESSIONAL_REVIEW, PROFESSIONAL_REVIEW>CERTIFIED, SUBMITTED>REJECTED, PROFESSIONAL_REVIEW>REJECTED, SUBMITTED>WITHDRAWN |
| project_variations | PROPOSED>COSTED, COSTED>APPROVED, APPROVED>IMPLEMENTED, PROPOSED>REJECTED, COSTED>REJECTED, PROPOSED>WITHDRAWN, COSTED>WITHDRAWN |
| vendor_quote_refs | REQUESTED>RECEIVED, RECEIVED>ACCEPTED, RECEIVED>REJECTED, REQUESTED>WITHDRAWN, RECEIVED>WITHDRAWN |
| provider_assignment_refs | REQUESTED>ACCEPTED, ACCEPTED>ON_SITE, ON_SITE>COMPLETED, REQUESTED>DECLINED, REQUESTED>CANCELLED, ACCEPTED>CANCELLED |
| warranties | ACTIVE>CLAIMED, CLAIMED>ACTIVE, ACTIVE>VOIDED, ACTIVE>EXPIRED, CLAIMED>EXPIRED |
| service_contract_refs | ACTIVE>TERMINATED, EXPIRING>TERMINATED, EXPIRING>ACTIVE, ACTIVE>EXPIRING, ACTIVE>EXPIRED, EXPIRING>EXPIRED |
| manager_performance_refs | DRAFT>PUBLISHED, PUBLISHED>ACKNOWLEDGED, PUBLISHED>DISPUTED, DISPUTED>ACKNOWLEDGED |
| professional_findings | RECORDED>ACCEPTED, ACCEPTED>LINKED_TO_CONDITION, ACCEPTED>RESOLVED, LINKED_TO_CONDITION>RESOLVED, RECORDED>DISPUTED, ACCEPTED>DISPUTED, RECORDED>REJECTED, DISPUTED>REJECTED |
| risk_signals | OBSERVED>LINKED, OBSERVED>DISMISSED, LINKED>DISMISSED, OBSERVED>EXPIRED |
| protection_case_refs | REQUESTED>OPEN, OPEN>RESOLVED, OPEN>REJECTED, REQUESTED>WITHDRAWN, OPEN>WITHDRAWN |
| incident_cases | OPEN>INVESTIGATING, INVESTIGATING>RESOLVED, OPEN>ESCALATED, INVESTIGATING>ESCALATED, RESOLVED>CLOSED, ESCALATED>CLOSED |
| access_events | PENDING>CONFIRMED, UNKNOWN>CONFIRMED, PENDING>FAILED, UNKNOWN>FAILED, PENDING>UNKNOWN, UNKNOWN>MANUALLY_RESOLVED, FAILED>MANUALLY_RESOLVED |
| device_health_snapshots | RECORDED>ACKNOWLEDGED, RECORDED>RESOLVED, ACKNOWLEDGED>RESOLVED |
| financing_consents | GRANTED>REVOKED, GRANTED>EXPIRED |
| insurance_compliance_checks | NON_COMPLIANT>REMEDIATED, NON_COMPLIANT>WAIVED, PENDING>COMPLIANT, PENDING>NON_COMPLIANT |
| api_credentials | ACTIVE>ROTATING, ROTATING>ACTIVE, ACTIVE>REVOKED, ROTATING>REVOKED, ACTIVE>EXPIRED, ROTATING>EXPIRED |
| webhook_subscriptions | PENDING_VERIFICATION>ACTIVE, ACTIVE>PAUSED, PAUSED>ACTIVE, PENDING_VERIFICATION>REVOKED, ACTIVE>REVOKED, PAUSED>REVOKED |
| fx_rate_refs | RECORDED>ACTIVE, RECORDED>RETRACTED, ACTIVE>RETRACTED, ACTIVE>STALE, ACTIVE>SUPERSEDED |
| organisation_change_cases | DRAFT>SUBMITTED, SUBMITTED>VERIFIED, VERIFIED>APPROVED, APPROVED>APPLIED, SUBMITTED>REJECTED, VERIFIED>REJECTED, DRAFT>WITHDRAWN, SUBMITTED>WITHDRAWN |
| closure_cases | DRAFT>OBLIGATIONS_REVIEW, OBLIGATIONS_REVIEW>APPROVED, APPROVED>RETENTION, RETENTION>CLOSED, DRAFT>CANCELLED, OBLIGATIONS_REVIEW>CANCELLED, APPROVED>CANCELLED |
| housekeeping_checklists | DRAFT>ACTIVE, DRAFT>RETIRED, ACTIVE>RETIRED |
| lost_found_items | FOUND>STORED, STORED>CLAIMED, CLAIMED>RETURNED, STORED>DISPOSED |
| linen_stock_refs | ACTIVE>ACTIVE, ACTIVE>RETIRED |
| shortlet_settings | DRAFT>ACTIVE, ACTIVE>SUPERSEDED, DRAFT>DISCARDED |
| property_service_catalogue | DRAFT>ACTIVE, SUSPENDED>ACTIVE, ACTIVE>SUSPENDED, DRAFT>RETIRED, ACTIVE>RETIRED, SUSPENDED>RETIRED |
| branch_incidents | OPEN>MITIGATING, OPEN>RESOLVED, MITIGATING>RESOLVED, RESOLVED>CLOSED |
| content_suggestions | SUGGESTED>ACCEPTED, SUGGESTED>REJECTED, ACCEPTED>PUBLISHED, SUGGESTED>EXPIRED |
| experiments | DRAFT>RUNNING, RUNNING>ABORTED, RUNNING>COMPLETED |
| market_benchmarks | RECORDED>ACTIVE, RECORDED>RETRACTED, ACTIVE>RETRACTED, ACTIVE>STALE |
