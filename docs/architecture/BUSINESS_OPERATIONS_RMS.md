# Revenue management ownership and evidence

Business Operations extends `internal/revenue` and the Section 7 canonical rate commands. RMS owns retained observations, forecasts, operator policy versions, proposed changes, simulations and immutable decisions. It does not own payment execution, search attribution, external-market authority or another service database.

## Observation and model boundaries

Demand snapshots contain actual configured calendar rows and unreleased canonical confirmed reservation nights, plus source/provenance hashes. Missing historical dates remain unknown. Accepted accommodation revenue is contractual and is not a claim about settled cash or realized stays. The baseline estimates same-weekday capacity-weighted committed demand from observations ending before the source's local civil day. Coverage is observed history coverage, not calibrated probability. Fixed-point thousandths preserve fractional demand. No model output increases physical capacity.

## Policy and recommendation boundaries

An RMS policy is an immutable version scoped to the owning organisation, branch, property, physical pool and rate plan. Activating it requires recent MFA and independent `rms:approve` authorization through `COMMERCIAL_RMS_POLICY`. A pause creates a new policy version without requiring approval. Recommendations pinned to any prior version become unusable immediately. Branch/currency changes invalidate authorization. A rate shared with another pool is rejected until an aggregate mandate covers every affected pool.

All four modes are executable. MANUAL and RECOMMEND_ONLY never apply anything automatically. An APPROVAL_AUTOMATION or AUTOPILOT_WITH_LIMITS policy version is only a mandate: execution additionally requires an `rms_automation_activations` record created by a sponsor holding live `rms:write` and `rates:write` (plus `rms:autopilot` for autopilot), recent MFA and an independent `COMMERCIAL_RMS_AUTOMATION_ACTIVATION` approval (`rms:approve`) bound to the exact limits and their maximum exposure. Limits can only narrow the bound policy (floor, ceiling, daily/cumulative movement, approval threshold, blackouts, compliance direction, minimum lead time, required operating capacity, source age, eligible model versions, coverage/comparable-day criteria, exposure, horizon, cadence, apply/outcome delays, drift threshold). Limits are immutable; pause is a kill switch, resume needs a new independent approval, revoke is final. See "Automation" below.

The baseline recommendation is an operator-configured occupancy feedback rule; target and gain are explicit policy values. It does not claim measured elasticity. Staff can retain an explicit amount scenario instead. Canonical floors/ceilings, blackout dates and channel coverage are independent hard bounds. Complex derived/conditional-rate scenarios fail closed until they can be evaluated using the owning canonical scenario adapter. No second rate engine is introduced.

Every recommendation retains its model/source, exact policy version, target plan version, per-day effective rate version, observed calendar version, proposed definition, expiry, explanation and digest. Changes to the proposal create a new identity; OVERRIDE links that replacement without editing the old snapshot. The state machine is DRAFT → PENDING_APPROVAL → APPROVED → APPLIED, with explicit rejection, override and expiry paths. ROLLED_BACK follows APPLIED only. Decision rows are append-only; PostgreSQL guards state/version/approval/application lineage.

## Application and rollback

SUBMIT does not approve a proposal. APPROVE consumes an independently completed `COMMERCIAL_RMS_RATE_CHANGE` approval containing:

```json
{"recommendation_id":"UUIDv7","change_digest":"SHA256","policy_id":"UUIDv7","forecast_id":"UUIDv7","target_plan_version":1,"rollback":false}
```

The approval scope is the owning PROPERTY, channel is empty (the entire plan is changed), currency is the canonical policy currency, and `amount_minor` is the maximum canonical ceiling of the affected definitions. The policy requires independent `rates:approve` capability and MFA. A requester cannot self-approve through the existing approval engine.

APPLY rechecks live `rms:write` and `rates:write`, MFA, property authority/capacity/compliance, policy freshness/pause, mandatory inputs, source age, exact plan/effective-rate/calendar versions and the original consumed approval's actor/state/deadline. Unresolved overbooking blocks application. Daily and cumulative budgets count absolute canonical movements, including manual changes and reversals, so repeated up/down changes cannot reset a net-change cap. Mixed-currency/unsupported derived history cannot authorize a budget.

The owning tenant fence serializes application; expected versions and immutable decisions prevent duplicate execution. `writeRateVersion` appends canonical versions and updates the plan once. Audit, commercial history and `rate_plan.changed.v1` outbox commit in the same transaction. Existing channel reconciliation observes commercial history and queues ARI deltas under its own live publication authority. External delivery remains unconfirmed until owning provider readback; no network side effect runs in the rate transaction.

ROLLBACK requires a separate `COMMERCIAL_RMS_RATE_ROLLBACK` approval using the same exact operation with `rollback:true` and the prior definitions' ceiling exposure. It checks the applied plan/effective versions and appends compensating versions. An intervening canonical change blocks rollback. A paused policy does not prevent approved restoration of the exact prior definitions; current property/compliance authority is still required. Historical versions and accepted reservation prices are never rewritten.

## Simulation, maintenance and contracts

Simulation retains the exact proposal/forecast digest and holds forecast demand constant. Outputs are explicitly projected base accommodation scenarios. Commission, promotion cost, payment fees, operating costs and net contribution stay null without authoritative scoped inputs. Simulation does not change rates or stock.

The established worker scheduler expires unapplied recommendations transactionally. It records the Business Operations service identity, immutable decision, security audit and outbox without borrowing the creator's account. Concurrent/repeated sweeps cannot create duplicate decisions. Expired recommendations are ineligible immediately even if the worker is stopped.

Public routes use `/api/v1/business/revenue/`. OpenAPI, Postman, event schemas and SQLC are generated and drift checked. Runtime roles cannot mutate policy versions, decisions, simulations, source observations or model records. See the Section 8 implementation report for executed evidence and remaining functionality; these controls do not certify all Section 8 workflows.

The specification's `pricing.recommendation.created.v1` and `pricing.applied.v1` are emitted transactionally for their exact operations. The additive `revenue.recommendation.changed.v1` lifecycle events are retained. `rate_plan.changed.v1` still drives canonical commercial deltas. Additional canonical publication reuses the same shared envelope/outbox path; it does not duplicate audit/history or introduce another broker publisher.

## Automation (Section 8 D1–D3, D7)

`ProcessRMSAutomation` runs on the existing worker loop. It schedules due activations and processes durable `rms_automation_jobs` one at a time. A claim increments `lease_generation`; processing reloads the job under the organisation lock and stops unless it still owns the current lease, so duplicate replicas, restarts and delayed workers cannot double-apply. Expired leases are taken over. Each run is a staged record (`rms_automation_runs`, append-only `rms_automation_run_stages`) with at most one open run per activation.

Pipeline: CAPTURE_DEMAND (demand + pace snapshot) → GENERATE_FORECAST (registered model version, exact source digest) → GENERATE_RECOMMENDATION (bounded by policy ∩ limits) → deterministic `EvaluateAutomation`. Hard violations block in every mode and record a POLICY_EXCEPTION finding. APPROVAL_AUTOMATION always files an approval request (SUBMIT_APPROVAL) whose requester is the sponsor and whose operation is marked `submitted_by_service`; the sponsor cannot decide it and the automation never decides approvals. MONITOR_APPROVAL observes the independent decision. AUTOPILOT_WITH_LIMITS applies changes within the approval threshold under the standing activation approval; larger changes escalate to the approval path. APPLY_RATE re-checks activation state, sponsor authority, policy currency, verified capacity, compliance holds, source freshness, plan/calendar/effective versions, movement budgets and approval validity, locks the rate plan, then writes through `writeRateVersion`. Rate versions, proposals and decisions record the service identity (`created_by_service`), never a human account.

Monitoring jobs: MEASURE_OUTCOME (forecast error, actual vs predicted occupancy, post-application ADR/revenue, cancellations, no-shows, channel contribution; booking evidence only, not cash), DETECT_DRIFT (mean absolute error in bps of capacity), CHECK_FRESHNESS (source age, model eligibility), MONITOR_LIMITS (sponsor authority, policy currency, movement budgets) and RECONCILE_PUBLICATION (channel cursors, publication delivery and failed sync jobs; unknown never becomes success). Drift, authority loss, model ineligibility and limit breaches pause the activation automatically and queue review. Rollback stays the authorized human decision (`COMMERCIAL_RMS_RATE_ROLLBACK`) that appends compensating versions and refuses to overwrite an intervening change.

## Analytics and advanced recommendations (D4–D6)

`POST /revenue/analytics` builds a reproducible snapshot (`canonical-pace-cancellation-v1`) for a property, branch or portfolio from canonical calendars, reservation lines and governed `rms_market_inputs` (holidays, events, competitor rates, demand indices, channel commissions and operating costs, each with source and licence references). It reports KPIs per property/branch/portfolio, product and channel analysis, lead time, pickup, cancellation and no-show rates, LOS, weekday and month effects, comparable period (UNAVAILABLE without history) and a cancellation-adjusted forecast. `input_digest` and `result_digest` make it reproducible. Missing sources stay null.

`POST /revenue/scenario-simulations` compares pricing, promotion, LOS and channel-mix alternatives against a retained forecast without writing anything. Commission, net revenue, operating cost and contribution stay null unless governed inputs cover them.

`POST /revenue/advanced-recommendations` supports RATE_RANGE and DERIVED_RATE (canonical rate version), MIN_LOS, MAX_LOS and BOOKING_WINDOW (canonical restriction), EARLY_BOOKER, LAST_MINUTE, LONG_STAY and discount CHANNEL_PRICING (canonical promotion scoped to the rate plan), and CHANNEL_ALLOCATION, OVERBOOKING, channel markups and non-derived DERIVED_RATE as ADVISORY with an explicit reason. Application needs an independent `COMMERCIAL_RMS_ADVANCED_CHANGE` approval (`pricing:approve`) consumed by the applying actor and refuses superseded rate versions.
