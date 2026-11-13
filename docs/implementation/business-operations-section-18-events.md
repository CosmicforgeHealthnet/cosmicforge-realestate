# Business Operations Section 18 — Event Catalogue

* Schemas: **155** JSON Schemas in `contracts/events/business` (105 before this delivery), generated and drift-checked by `scripts/contracts/generate_business_events.py --check` from producer literals in the Go source.
* Envelope: shared `events.New` (UUIDv7 id, schema/event type, producer, aggregate type/id, occurred_at, trace id, actor) with payload `organisation_id`, optional `property_id`, `target_id`, `operation`, `state`, `version`, `snapshot`.
* Atomicity: every new producer writes `operations_history`, `audit_events` and the outbox row in the owning transaction (`events.OperationCommit`). Rollback leaves no event. The existing outbox relay (`outbox.NewWorker`) publishes to the existing Business stream subject namespace (`<prefix>.business.<event_type>`).
* Validation: every integration test validates every stored Business outbox event against its schema (`validateStoredBusinessEvents`).

## Section 18 mandatory events

| Event | Producer |
|---|---|
| business.verified.v1, business.staff.updated.v1 | organisation projection / workforce (existing) |
| property.created.v1, property.updated.v1 | portfolio (existing) |
| listing.published/updated/withdrawn.v1, inventory.changed.v1, rate_plan.changed.v1 | commercial (existing) |
| channel.connection.changed.v1, channel.sync.failed.v1 | distribution (existing) |
| reservation.created/modified/cancelled/checked_in/checked_out.v1 | reservations/PMS (existing) |
| room.status.changed.v1, night_audit.closed.v1 | hospitality (existing) |
| guest.service_recovery.created.v1 | **new**: recovery case creation and approved-overbooking recovery |
| review.received.v1 | **new**: review intake/update |
| pricing.recommendation.created.v1, pricing.applied.v1 | RMS (existing) |
| manager.assignment.updated.v1 | **new**: manager delegation domain |
| maintenance.work_order.updated.v1 | **new**: work orders |
| development.milestone.updated.v1 | **new**: construction milestones |
| access.grant.updated.v1 | **new**: business access grants |

## Additional new event families

communication.context.updated, inbox.thread.updated, inbox.reply.updated, ai.suggestion.updated, automation.rule.updated, automation.action.proposed, messaging.connection.updated, support.case.updated, review.response.updated, review.dispute.updated, quality.task.updated, reputation.snapshot.ready, lead.updated, campaign.updated, storefront.updated, finance.obligation/budget/expense/payout_change/revenue_split/settlement/exception.updated, housekeeping.task.updated, maintenance.request.updated, professional.appointment.updated, data_room.updated, data_room.grant.updated, rental.application.updated, tenancy.updated, commercial.deal.updated, land.transaction.updated, sale.transaction.updated, sale.offer.updated, development.allocation/snag/warranty.updated, risk.case.updated, dispute.updated, financing.status.updated, insurance.policy/claim.updated, access.device.alert, integration.health.changed, analytics.snapshot.ready.

## Consumer guarantees

Existing consumers (notification relay, CRM history projector, verification) remain idempotent with quarantine (`OnFailure`), bounded `MaxDeliver` and durable consumers; their broker-outage, duplicate and poison tests are unchanged (`outbox_test.go`, `TestJourney09PaymentAndBrokerOutageRecoverWithoutDuplicateEffects`). New Sections 12–16 behaviour is state-driven by workers reading committed state, so redelivery never repeats an effect.
