package events

import (
	"context"
	"encoding/json"

	shared "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

// Sections 12-24 canonical event names (Section 18 plus required additions).
const (
	ReviewReceived               = "review.received.v1"
	ServiceRecoveryCreated       = "guest.service_recovery.created.v1"
	ManagerAssignmentUpdated     = "manager.assignment.updated.v1"
	MaintenanceWorkOrderUpdated  = "maintenance.work_order.updated.v1"
	DevelopmentMilestoneUpdated  = "development.milestone.updated.v1"
	AccessGrantUpdated           = "access.grant.updated.v1"
	LeadUpdated                  = "lead.updated.v1"
	CampaignUpdated              = "campaign.updated.v1"
	StorefrontUpdated            = "storefront.updated.v1"
	SupportCaseUpdated           = "support.case.updated.v1"
	AutomationActionProposed     = "automation.action.proposed.v1"
	CommunicationContextUpdated  = "communication.context.updated.v1"
	InboxThreadUpdated           = "inbox.thread.updated.v1"
	InboxReplyUpdated            = "inbox.reply.updated.v1"
	AISuggestionUpdated          = "ai.suggestion.updated.v1"
	AutomationRuleUpdated        = "automation.rule.updated.v1"
	MessagingConnectionUpdated   = "messaging.connection.updated.v1"
	ReviewResponseUpdated        = "review.response.updated.v1"
	ReviewDisputeUpdated         = "review.dispute.updated.v1"
	QualityTaskUpdated           = "quality.task.updated.v1"
	ReputationSnapshotReady      = "reputation.snapshot.ready.v1"
	FinanceObligationUpdated     = "finance.obligation.updated.v1"
	FinanceBudgetUpdated         = "finance.budget.updated.v1"
	FinanceExpenseUpdated        = "finance.expense.updated.v1"
	FinancePayoutChangeUpdated   = "finance.payout_change.updated.v1"
	FinanceSplitRuleUpdated      = "finance.revenue_split.updated.v1"
	FinanceSettlementUpdated     = "finance.settlement.updated.v1"
	FinanceExceptionUpdated      = "finance.exception.updated.v1"
	HousekeepingTaskUpdated      = "housekeeping.task.updated.v1"
	DevelopmentAllocationUpdated = "development.allocation.updated.v1"
	ProfessionalAppointment      = "professional.appointment.updated.v1"
	DataRoomGrantUpdated         = "data_room.grant.updated.v1"
	RiskCaseUpdated              = "risk.case.updated.v1"
	DisputeUpdated               = "dispute.updated.v1"
	AnalyticsSnapshotReady       = "analytics.snapshot.ready.v1"
)

// OperationCommit appends history, audit and (optionally) an outbox event in
// the owning transaction. property may be ids.Nil for company-scoped records.
// A zero actor records the explicit service identity set on the transaction.
func OperationCommit(ctx context.Context, tx pgx.Tx, store *outbox.Store, org, property, target, actor ids.ID, kind, action, from, to, eventType string, version int64, details any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	if string(raw) == "null" {
		raw = []byte("{}")
	}
	var actorRef, propertyRef any
	if !actor.IsZero() {
		actorRef = actor
	}
	if !property.IsZero() {
		propertyRef = property
	}
	correlation := requestcontext.From(ctx).RequestID
	if _, err = tx.Exec(ctx, `INSERT INTO operations_history(id,organisation_id,property_id,entity_type,entity_id,entity_version,action,from_state,to_state,actor_id,correlation_id,details) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, ids.New(), org, propertyRef, kind, target, version, action, from, to, actorRef, correlation, raw); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,organisation_id,action,target_type,target_id,correlation_id,change_context) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), actorRef, org, kind+":"+action, kind, target, correlation, raw); err != nil {
		return err
	}
	if eventType == "" {
		return nil
	}
	return PublishOperation(ctx, tx, store, org, property, target, kind, action, to, eventType, version, details)
}

// PublishOperation appends one canonical Business event under the existing
// Business stream subject namespace.
func PublishOperation(ctx context.Context, tx pgx.Tx, store *outbox.Store, org, property, target ids.ID, kind, action, state, eventType string, version int64, details any) error {
	payload := map[string]any{"organisation_id": org, "target_id": target, "operation": action, "state": state, "version": version, "snapshot": details}
	if !property.IsZero() {
		payload["property_id"] = property
	}
	envelope, err := shared.New(ctx, platform.BusinessOperationsService, eventType, kind, target, payload)
	if err != nil {
		return err
	}
	if err = store.Add(ctx, tx, envelope); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE outbox_events SET subject=left(subject,length(subject)-length(event_type))||'business.'||event_type WHERE event_id=$1`, envelope.EventID)
	return err
}
