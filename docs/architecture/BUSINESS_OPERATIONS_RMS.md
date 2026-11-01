# Revenue management ownership and evidence

Business Operations extends `internal/revenue` and the Section 7 canonical rate commands. RMS owns retained observations, forecasts, operator policy versions, proposed changes, simulations and immutable decisions. It does not own payment execution, search attribution, external-market authority or another service database.

## Observation and model boundaries

Demand snapshots contain actual configured calendar rows and unreleased canonical confirmed reservation nights, plus source/provenance hashes. Missing historical dates remain unknown. Accepted accommodation revenue is contractual and is not a claim about settled cash or realized stays. The baseline estimates same-weekday capacity-weighted committed demand from observations ending before the source's local civil day. Coverage is observed history coverage, not calibrated probability. Fixed-point thousandths preserve fractional demand. No model output increases physical capacity.

## Policy and recommendation boundaries

An RMS policy is an immutable version scoped to the owning organisation, branch, property, physical pool and rate plan. Activating it requires recent MFA and independent `rms:approve` authorization through `COMMERCIAL_RMS_POLICY`. A pause creates a new policy version without requiring approval. Recommendations pinned to any prior version become unusable immediately. Branch/currency changes invalidate authorization. A rate shared with another pool is rejected until an aggregate mandate covers every affected pool.

Supported application modes at this checkpoint are MANUAL and RECOMMEND_ONLY. Paused configurations can retain APPROVAL_AUTOMATION or AUTOPILOT_WITH_LIMITS, but activation returns 503 until the explicit bounded worker authority is composed. A mode label grants no permission and creates no background application. This is an unfinished Section 8 requirement, not an unavailable external-provider operation.

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
