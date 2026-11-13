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
