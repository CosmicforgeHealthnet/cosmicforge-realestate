package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// CRMRecoveryCaseCreate opens a service recovery case for an actual guest in
// the property scope. Reservation and stay references must belong to that guest.
type CRMRecoveryCaseCreate struct {
	PropertyID        ids.ID  `json:"property_id"`
	GuestID           ids.ID  `json:"guest_id"`
	ReservationID     *ids.ID `json:"reservation_id,omitempty"`
	StayID            *ids.ID `json:"stay_id,omitempty"`
	IssueType         string  `json:"issue_type"`
	Severity          string  `json:"severity"`
	Summary           string  `json:"summary"`
	EvidenceReference string  `json:"evidence_reference"`
	Reason            string  `json:"reason"`
}

// CRMRecoveryTransition moves a case through its governed lifecycle.
type CRMRecoveryTransition struct {
	Version                int64      `json:"version"`
	Action                 string     `json:"action"`
	AssigneeID             *ids.ID    `json:"assignee_id,omitempty"`
	EvidenceReference      string     `json:"evidence_reference,omitempty"`
	Outcome                string     `json:"outcome,omitempty"`
	CommunicationReference string     `json:"communication_reference,omitempty"`
	FollowUpAt             *time.Time `json:"follow_up_at,omitempty"`
	Reason                 string     `json:"reason"`
}

type CRMRecoveryRemedyItem struct {
	Kind        string `json:"kind"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency,omitempty"`
	Points      int64  `json:"points"`
	Description string `json:"description"`
}

// CRMRecoveryRemedyCommand proposes the exact remedy that approval binds to.
type CRMRecoveryRemedyCommand struct {
	Version  int64                   `json:"version"`
	Remedies []CRMRecoveryRemedyItem `json:"remedies"`
	Reason   string                  `json:"reason"`
}

// CRMRecoveryApproval approves the current remedy digest. Monetary and points
// remedies consume an exact independent approval; others need a distinct approver.
type CRMRecoveryApproval struct {
	Version      int64  `json:"version"`
	RemedyDigest string `json:"remedy_digest"`
	ApprovalID   ids.ID `json:"approval_id,omitempty"`
	Reason       string `json:"reason"`
}

// CRMRecoveryExecution records the verified outcome of one approved remedy.
type CRMRecoveryExecution struct {
	Version            int64   `json:"version"`
	Decline            bool    `json:"decline"`
	ExecutionReference *ids.ID `json:"execution_reference,omitempty"`
	EvidenceReference  string  `json:"evidence_reference,omitempty"`
	Reason             string  `json:"reason"`
}

type CRMRecoveryCase struct {
	ID                     ids.ID          `json:"id"`
	PropertyID             ids.ID          `json:"property_id"`
	GuestID                ids.ID          `json:"guest_id"`
	ReservationID          *ids.ID         `json:"reservation_id"`
	StayID                 *ids.ID         `json:"stay_id"`
	IssueType              string          `json:"issue_type"`
	Severity               string          `json:"severity"`
	State                  string          `json:"state"`
	Summary                string          `json:"summary"`
	EvidenceReference      string          `json:"evidence_reference"`
	AssignedTo             *ids.ID         `json:"assigned_to"`
	SLADueAt               time.Time       `json:"sla_due_at"`
	SLABreached            bool            `json:"sla_breached"`
	Remedy                 json.RawMessage `json:"remedy"`
	RemedyDigest           *string         `json:"remedy_digest"`
	RemedyAmountMinor      int64           `json:"remedy_amount_minor"`
	Currency               *string         `json:"currency"`
	ApprovalID             *ids.ID         `json:"approval_id"`
	Outcome                *string         `json:"outcome"`
	CommunicationReference *string         `json:"communication_reference"`
	FollowUpAt             *time.Time      `json:"follow_up_at"`
	ResolvedAt             *time.Time      `json:"resolved_at"`
	ClosedAt               *time.Time      `json:"closed_at"`
	ReopenedCount          int             `json:"reopened_count"`
	Version                int64           `json:"version"`
	CreatedBy              ids.ID          `json:"created_by"`
	CreatedAt              time.Time       `json:"created_at"`
}

func recoverySLA(severity string) time.Duration {
	switch severity {
	case "CRITICAL":
		return 4 * time.Hour
	case "HIGH":
		return 24 * time.Hour
	case "MEDIUM":
		return 72 * time.Hour
	}
	return 168 * time.Hour
}

func (h *Handler) registerRecoveryCRM(r chi.Router) {
	r.Get("/service-recovery/cases", h.crmRecoveryCases)
	r.Post("/service-recovery/cases", h.crmRecoveryCaseCreate)
	r.Get("/service-recovery/cases/{id}", h.crmRecoveryCaseGet)
	r.Post("/service-recovery/cases/{id}/transitions", h.crmRecoveryCaseTransition)
	r.Post("/service-recovery/cases/{id}/remedy", h.crmRecoveryRemedy)
	r.Post("/service-recovery/cases/{id}/approval", h.crmRecoveryApprove)
	r.Post("/service-recovery/cases/{id}/compensations/{compensation_id}/outcome", h.crmRecoveryCompensationOutcome)
}

func loadRecoveryCase(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, org, id ids.ID, now time.Time, lock bool) (CRMRecoveryCase, error) {
	var out CRMRecoveryCase
	var raw []byte
	query := `SELECT to_jsonb(c)||jsonb_build_object('sla_breached',c.state NOT IN ('RESOLVED','CLOSED','REJECTED','CANCELLED') AND c.sla_due_at<$3) FROM service_recovery_cases c WHERE organisation_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	e := q.QueryRow(ctx, query, org, id, now).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, apperrors.NotFound("RECOVERY_CASE_NOT_FOUND", "Service recovery case not found.")
	}
	if e != nil {
		return out, e
	}
	return out, json.Unmarshal(raw, &out)
}

func (h *Handler) recoveryTarget(r *http.Request, capability string, mfa bool) (tenant, CRMRecoveryCase, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, CRMRecoveryCase{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, CRMRecoveryCase{}, e
	}
	c, e := loadRecoveryCase(r.Context(), h.Auth.Service.Pool, t.Organisation, id, h.Auth.Service.Now(), false)
	if e != nil {
		return t, c, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", c.PropertyID, mfa)
	return t, c, e
}

// recoveryCommit appends the action row, audit, history and outbox event for
// one case version transition.
func (h *Handler) recoveryCommit(ctx context.Context, tx pgx.Tx, t tenant, c CRMRecoveryCase, version int64, action, from, to, evidence string, details map[string]any) error {
	var ev any
	if evidence != "" {
		ev = evidence
	}
	if _, e := tx.Exec(ctx, `INSERT INTO service_recovery_actions(id,organisation_id,property_id,case_id,case_version,action,from_state,to_state,evidence_reference,details,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, ids.New(), t.Organisation, c.PropertyID, c.ID, version, action, from, to, ev, marshalCommercial(details), t.Account); e != nil {
		return e
	}
	snapshot := map[string]any{"case_id": c.ID, "guest_id": c.GuestID, "action": action, "from_state": from, "state": to, "details": details}
	return h.commercialCommit(ctx, tx, t, c.PropertyID, c.ID, "service_recovery_case", "SERVICE_RECOVERY_"+action, "service_recovery.updated.v1", version, snapshot)
}

func (h *Handler) crmRecoveryCaseCreate(w http.ResponseWriter, r *http.Request) {
	var in CRMRecoveryCaseCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.GuestID.ValidateV7() != nil || in.ReservationID != nil && in.ReservationID.ValidateV7() != nil || in.StayID != nil && in.StayID.ValidateV7() != nil ||
		!slices.Contains([]string{"OVERBOOKING", "ROOM_MOVE", "SERVICE_FAILURE", "BILLING_DISPUTE", "ACCESS_FAILURE", "DELAYED_CHECK_IN", "CLEANLINESS", "MAINTENANCE", "INCORRECT_CHARGE", "OTHER"}, in.IssueType) ||
		!slices.Contains([]string{"LOW", "MEDIUM", "HIGH", "CRITICAL"}, in.Severity) || !crm.Text(in.Summary, 5, 500) || !crm.Text(in.EvidenceReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, guest, supported issue type, severity, bounded summary, evidence and reason required."))
		return
	}
	h.execute(w, r, "crm:recovery:create:"+in.PropertyID.String(), in, "recovery:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "recovery:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		var ok bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_property_scopes s JOIN guest_profiles g ON g.organisation_id=s.organisation_id AND g.id=s.guest_id WHERE s.organisation_id=$1 AND s.guest_id=$2 AND s.property_id=$3 AND s.state='ACTIVE' AND g.state IN ('ACTIVE','RESTRICTED')) AND ($4::uuid IS NULL OR EXISTS(SELECT 1 FROM reservations r JOIN reservation_guests rg ON rg.organisation_id=r.organisation_id AND rg.reservation_id=r.id WHERE r.organisation_id=$1 AND r.property_id=$3 AND r.id=$4 AND rg.guest_id=$2)) AND ($5::uuid IS NULL OR EXISTS(SELECT 1 FROM stay_records s WHERE s.organisation_id=$1 AND s.property_id=$3 AND s.id=$5 AND s.guest_id=$2 AND ($4::uuid IS NULL OR s.reservation_id=$4)))`, t.Organisation, in.GuestID, in.PropertyID, in.ReservationID, in.StayID).Scan(&ok); e != nil {
			return 0, nil, e
		}
		if !ok {
			return 0, nil, apperrors.PermissionDenied("The guest, reservation and stay must be actual records of the same guest in this property scope.")
		}
		now := h.Auth.Service.Now().UTC()
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO service_recovery_cases(id,organisation_id,property_id,guest_id,reservation_id,stay_id,issue_type,severity,summary,evidence_reference,sla_due_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, in.PropertyID, in.GuestID, in.ReservationID, in.StayID, in.IssueType, in.Severity, in.Summary, in.EvidenceReference, now.Add(recoverySLA(in.Severity)), t.Account); e != nil {
			return 0, nil, e
		}
		c, e := loadRecoveryCase(ctx, tx, t.Organisation, id, now, false)
		if e != nil {
			return 0, nil, e
		}
		if e = h.recoveryCommit(ctx, tx, t, c, 1, "OPENED", "OPEN", "OPEN", in.EvidenceReference, map[string]any{"issue_type": in.IssueType, "severity": in.Severity, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 201, c, nil
	})
}

func (h *Handler) crmRecoveryCases(w http.ResponseWriter, r *http.Request) {
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A property scope is required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "recovery:read", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(c)||jsonb_build_object('sla_breached',c.state NOT IN ('RESOLVED','CLOSED','REJECTED','CANCELLED') AND c.sla_due_at<$6) FROM service_recovery_cases c WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) ORDER BY sla_due_at,id LIMIT $4 OFFSET $5`, t.Organisation, property, r.URL.Query().Get("state"), limit, offset, h.Auth.Service.Now())
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}

func (h *Handler) crmRecoveryCaseGet(w http.ResponseWriter, r *http.Request) {
	t, c, e := h.recoveryTarget(r, "recovery:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	actions, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(a) FROM service_recovery_actions a WHERE organisation_id=$1 AND case_id=$2 ORDER BY case_version LIMIT 500`, t.Organisation, c.ID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	compensations, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(c) FROM service_recovery_compensation_refs c WHERE organisation_id=$1 AND case_id=$2 ORDER BY created_at,ordinal LIMIT 100`, t.Organisation, c.ID)
	reply(w, r, 200, map[string]any{"case": c, "actions": actions, "compensations": compensations}, e)
}

// recoveryTransitions maps action -> allowed source states -> target state.
var recoveryTransitions = map[string]struct {
	from []string
	to   string
}{
	"ASSIGN":        {[]string{"OPEN", "ASSIGNED", "INVESTIGATING", "ESCALATED"}, "ASSIGNED"},
	"INVESTIGATE":   {[]string{"ASSIGNED", "ESCALATED", "REMEDY_PROPOSED"}, "INVESTIGATING"},
	"ESCALATE":      {[]string{"OPEN", "ASSIGNED", "INVESTIGATING", "PENDING_APPROVAL"}, "ESCALATED"},
	"SUBMIT":        {[]string{"REMEDY_PROPOSED"}, "PENDING_APPROVAL"},
	"REJECT_REMEDY": {[]string{"PENDING_APPROVAL"}, "INVESTIGATING"},
	"RESOLVE":       {[]string{"EXECUTION_PENDING"}, "RESOLVED"},
	"REJECT":        {[]string{"OPEN", "ASSIGNED", "INVESTIGATING", "ESCALATED"}, "REJECTED"},
	"CANCEL":        {[]string{"OPEN", "ASSIGNED", "INVESTIGATING", "ESCALATED", "REMEDY_PROPOSED"}, "CANCELLED"},
	"CLOSE":         {[]string{"RESOLVED", "REJECTED", "CANCELLED"}, "CLOSED"},
	"REOPEN":        {[]string{"RESOLVED", "CLOSED"}, "INVESTIGATING"},
}

func (h *Handler) crmRecoveryCaseTransition(w http.ResponseWriter, r *http.Request) {
	var in CRMRecoveryTransition
	if !decode(w, r, &in) {
		return
	}
	rule, known := recoveryTransitions[in.Action]
	if in.Version < 1 || !known || !validStaffReason(in.Reason) || in.EvidenceReference != "" && !crm.Text(in.EvidenceReference, 5, 300) || (in.Action == "ASSIGN") != (in.AssigneeID != nil) ||
		in.Action == "RESOLVE" && (!crm.Text(in.Outcome, 5, 500) || !crm.Text(in.CommunicationReference, 5, 300)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current case version, supported action and reason required; ASSIGN names an assignee and RESOLVE records the actual outcome and guest communication reference."))
		return
	}
	_, pre, e := h.recoveryTarget(r, "recovery:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:recovery:transition:"+pre.ID.String(), in, "recovery:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "recovery:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		c, e := loadRecoveryCase(ctx, tx, t.Organisation, pre.ID, now, true)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Recovery case changed.")
		}
		if !slices.Contains(rule.from, c.State) {
			return 0, nil, apperrors.InvalidStateTransition(c.State, rule.to)
		}
		if in.AssigneeID != nil {
			var member bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2 AND status='ACTIVE')`, t.Organisation, *in.AssigneeID).Scan(&member); e != nil {
				return 0, nil, e
			}
			if !member {
				return 0, nil, apperrors.PermissionDenied("Assignee must be an active member of this organisation.")
			}
		}
		set := `state=$3,version=version+1,updated_at=now()`
		args := []any{t.Organisation, c.ID, rule.to}
		switch in.Action {
		case "ASSIGN":
			set += `,assigned_to=$4`
			args = append(args, *in.AssigneeID)
		case "RESOLVE":
			set += `,outcome=$4,communication_reference=$5,follow_up_at=$6,resolved_at=now()`
			args = append(args, in.Outcome, in.CommunicationReference, in.FollowUpAt)
		case "CLOSE":
			set += `,closed_at=now()`
		case "REOPEN":
			set += `,reopened_count=reopened_count+1,remedy=NULL,remedy_digest=NULL,remedy_amount_minor=0,currency=NULL,approval_id=NULL,resolved_at=NULL,closed_at=NULL`
		case "REJECT_REMEDY":
			set += `,approval_id=NULL`
		}
		if _, e = tx.Exec(ctx, `UPDATE service_recovery_cases SET `+set+` WHERE organisation_id=$1 AND id=$2`, args...); e != nil {
			var app *apperrors.Error
			if errors.As(e, &app) {
				return 0, nil, e
			}
			if database.SQLState(e) == "23514" {
				return 0, nil, apperrors.Conflict("RECOVERY_REMEDY_OUTCOME_REQUIRED", "Every approved remedy needs a verified final outcome before resolution; pending refunds and external requests keep the case open.")
			}
			return 0, nil, e
		}
		if e = h.recoveryCommit(ctx, tx, t, c, c.Version+1, in.Action, c.State, rule.to, in.EvidenceReference, map[string]any{"assignee_id": in.AssigneeID, "outcome": in.Outcome, "communication_reference": in.CommunicationReference, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := loadRecoveryCase(ctx, tx, t.Organisation, c.ID, now, false)
		return 200, out, e
	})
}

func validRecoveryRemedy(items []CRMRecoveryRemedyItem) (int64, string, bool) {
	if len(items) < 1 || len(items) > 10 {
		return 0, "", false
	}
	var total int64
	currency := ""
	for _, it := range items {
		monetary := slices.Contains([]string{"DISCOUNT", "REFUND_REQUEST", "FINANCIAL_ADJUSTMENT_REQUEST", "FUTURE_BOOKING_CREDIT"}, it.Kind)
		if !slices.Contains([]string{"ALTERNATIVE_ROOM", "SERVICE_REPLACEMENT", "COMPLIMENTARY_SERVICE", "DISCOUNT", "LOYALTY_BENEFIT", "FUTURE_BOOKING_CREDIT", "REFUND_REQUEST", "FINANCIAL_ADJUSTMENT_REQUEST", "OTHER_NONCASH"}, it.Kind) ||
			!crm.Text(it.Description, 5, 300) || it.AmountMinor < 0 || it.AmountMinor > 1000000000000 || it.Points < 0 || it.Points > 100000000 ||
			monetary != (it.AmountMinor > 0) || monetary && len(it.Currency) != 3 || !monetary && it.Currency != "" || (it.Kind == "LOYALTY_BENEFIT") != (it.Points > 0) {
			return 0, "", false
		}
		if monetary {
			if currency != "" && currency != it.Currency {
				return 0, "", false
			}
			currency = it.Currency
			total += it.AmountMinor
		}
	}
	return total, currency, total <= 1000000000000
}

func (h *Handler) crmRecoveryRemedy(w http.ResponseWriter, r *http.Request) {
	var in CRMRecoveryRemedyCommand
	if !decode(w, r, &in) {
		return
	}
	total, currency, ok := validRecoveryRemedy(in.Remedies)
	if in.Version < 1 || !ok || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version and one to ten supported remedies required; monetary remedies share one currency and points apply only to loyalty benefits."))
		return
	}
	_, pre, e := h.recoveryTarget(r, "recovery:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:recovery:remedy:"+pre.ID.String(), in, "recovery:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "recovery:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		c, e := loadRecoveryCase(ctx, tx, t.Organisation, pre.ID, now, true)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != in.Version || c.State != "INVESTIGATING" {
			return 0, nil, apperrors.Conflict("RECOVERY_REMEDY_STATE", "Remedies are proposed from the current investigating case version.")
		}
		remedy := marshalCommercial(in.Remedies)
		digest := hashCommercialPayload(marshalCommercial(map[string]any{"case_id": c.ID, "reopened": c.ReopenedCount, "remedies": in.Remedies}))
		var cur any
		if currency != "" {
			cur = currency
		}
		if _, e = tx.Exec(ctx, `UPDATE service_recovery_cases SET state='REMEDY_PROPOSED',remedy=$3,remedy_digest=$4,remedy_amount_minor=$5,currency=$6,approval_id=NULL,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, c.ID, remedy, digest, total, cur); e != nil {
			return 0, nil, e
		}
		if e = h.recoveryCommit(ctx, tx, t, c, c.Version+1, "REMEDY_PROPOSED", c.State, "REMEDY_PROPOSED", "", map[string]any{"remedy_digest": digest, "remedy_amount_minor": total, "currency": currency, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := loadRecoveryCase(ctx, tx, t.Organisation, c.ID, now, false)
		return 200, out, e
	})
}

func (h *Handler) crmRecoveryApprove(w http.ResponseWriter, r *http.Request) {
	var in CRMRecoveryApproval
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || len(in.RemedyDigest) != 64 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, exact remedy digest and reason required."))
		return
	}
	_, pre, e := h.recoveryTarget(r, "recovery:approve", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:recovery:approve:"+pre.ID.String(), in, "recovery:approve", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "recovery:approve", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		c, e := loadRecoveryCase(ctx, tx, t.Organisation, pre.ID, now, true)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != in.Version || c.State != "PENDING_APPROVAL" || c.RemedyDigest == nil || *c.RemedyDigest != in.RemedyDigest {
			return 0, nil, apperrors.Conflict("RECOVERY_APPROVAL_STATE", "Approval binds the exact current pending remedy digest.")
		}
		var items []CRMRecoveryRemedyItem
		if e = json.Unmarshal(c.Remedy, &items); e != nil {
			return 0, nil, e
		}
		valued := c.RemedyAmountMinor > 0
		for _, it := range items {
			valued = valued || it.Points > 0
		}
		var approval any
		if valued {
			amount := c.RemedyAmountMinor
			currency := ""
			if c.Currency != nil {
				currency = *c.Currency
			}
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_SERVICE_RECOVERY_REMEDY", target.Scope, in.ApprovalID, map[string]any{"case_id": c.ID, "remedy_digest": in.RemedyDigest, "remedies": items}, "", &amount, currency); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		} else {
			if !in.ApprovalID.IsZero() {
				return 0, nil, apperrors.InvalidRequest("A non-monetary remedy does not consume an approval request.")
			}
			// Separation of duties: the proposer cannot approve their own remedy.
			var proposer ids.ID
			if e = tx.QueryRow(ctx, `SELECT created_by FROM service_recovery_actions WHERE organisation_id=$1 AND case_id=$2 AND action='REMEDY_PROPOSED' ORDER BY case_version DESC LIMIT 1`, t.Organisation, c.ID).Scan(&proposer); e != nil {
				return 0, nil, e
			}
			if proposer == t.Account {
				return 0, nil, apperrors.PermissionDenied("A distinct recovery approver must approve the proposed remedy.")
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE service_recovery_cases SET state='APPROVED',approval_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, c.ID, approval); e != nil {
			return 0, nil, e
		}
		if e = h.recoveryCommit(ctx, tx, t, c, c.Version+1, "APPROVED", c.State, "APPROVED", "", map[string]any{"remedy_digest": in.RemedyDigest, "approval_id": approval, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		for i, it := range items {
			state, dependency := "PENDING_EXECUTION", any(nil)
			if it.Kind == "REFUND_REQUEST" || it.Kind == "FINANCIAL_ADJUSTMENT_REQUEST" || it.Kind == "FUTURE_BOOKING_CREDIT" {
				// Payment refunds/credits are Admin-authorized. Business never
				// impersonates Admin; the approved request waits for that contract.
				state, dependency = "PENDING_EXTERNAL_CONTRACT", "payment-service remedy execution is Admin-authorized; no authorized Business-to-Admin remedy API exists"
			}
			var cur any
			if it.Currency != "" {
				cur = it.Currency
			}
			if _, e = tx.Exec(ctx, `INSERT INTO service_recovery_compensation_refs(id,organisation_id,property_id,case_id,ordinal,kind,amount_minor,currency,points,remedy_digest,approval_id,state,dependency,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, ids.New(), t.Organisation, c.PropertyID, c.ID, i+1, it.Kind, it.AmountMinor, cur, it.Points, in.RemedyDigest, approval, state, dependency, t.Account); e != nil {
				return 0, nil, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE service_recovery_cases SET state='EXECUTION_PENDING',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, c.ID); e != nil {
			return 0, nil, e
		}
		if e = h.recoveryCommit(ctx, tx, t, c, c.Version+2, "EXECUTION_REQUESTED", "APPROVED", "EXECUTION_PENDING", "", map[string]any{"remedy_digest": in.RemedyDigest}); e != nil {
			return 0, nil, e
		}
		out, e := loadRecoveryCase(ctx, tx, t.Organisation, c.ID, now, false)
		return 200, out, e
	})
}

func (h *Handler) crmRecoveryCompensationOutcome(w http.ResponseWriter, r *http.Request) {
	var in CRMRecoveryExecution
	if !decode(w, r, &in) {
		return
	}
	compensation, e := ids.ParseV7(chi.URLParam(r, "compensation_id"))
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) || in.EvidenceReference != "" && !crm.Text(in.EvidenceReference, 5, 300) || in.ExecutionReference != nil && in.ExecutionReference.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current compensation version, verified execution evidence or decline, and reason required."))
		return
	}
	_, pre, e := h.recoveryTarget(r, "recovery:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:recovery:outcome:"+compensation.String(), in, "recovery:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "recovery:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		c, e := loadRecoveryCase(ctx, tx, t.Organisation, pre.ID, now, true)
		if e != nil {
			return 0, nil, e
		}
		var comp struct {
			ID          ids.ID  `json:"id"`
			Kind        string  `json:"kind"`
			AmountMinor int64   `json:"amount_minor"`
			Currency    *string `json:"currency"`
			Points      int64   `json:"points"`
			State       string  `json:"state"`
			Digest      string  `json:"remedy_digest"`
			Version     int64   `json:"version"`
		}
		var raw []byte
		if e = tx.QueryRow(ctx, `SELECT to_jsonb(c) FROM service_recovery_compensation_refs c WHERE organisation_id=$1 AND case_id=$2 AND id=$3 FOR UPDATE`, t.Organisation, c.ID, compensation).Scan(&raw); errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, apperrors.NotFound("RECOVERY_COMPENSATION_NOT_FOUND", "Compensation does not belong to this case.")
		} else if e != nil {
			return 0, nil, e
		}
		if e = json.Unmarshal(raw, &comp); e != nil {
			return 0, nil, e
		}
		if comp.Version != in.Version || c.State != "EXECUTION_PENDING" || c.RemedyDigest == nil || comp.Digest != *c.RemedyDigest {
			return 0, nil, apperrors.Conflict("RECOVERY_COMPENSATION_STATE", "Only a current approved remedy of an executing case records an outcome.")
		}
		state, kind, reference, evidence := "EXECUTED", "", in.ExecutionReference, in.EvidenceReference
		switch {
		case comp.State != "PENDING_EXECUTION" && !(comp.State == "PENDING_EXTERNAL_CONTRACT" && in.Decline):
			return 0, nil, apperrors.Conflict("RECOVERY_EXTERNAL_OUTCOME_REQUIRED", "A pending external financial remedy is never completed from an accepted request; only an authoritative owning-service outcome or the guest's recorded decline closes it.")
		case in.Decline:
			if evidence == "" {
				return 0, nil, apperrors.InvalidRequest("A decline records the guest's actual decision evidence.")
			}
			state = "DECLINED"
		case comp.Kind == "LOYALTY_BENEFIT":
			kind = "LOYALTY_TRANSACTION"
			tid, e := h.recoveryLoyaltyBenefit(ctx, tx, t, c, comp.ID, comp.Points, comp.Digest)
			if e != nil {
				return 0, nil, e
			}
			reference = &tid
		case comp.Kind == "DISCOUNT":
			kind = "FOLIO_ADJUSTMENT"
			if reference == nil || c.StayID == nil {
				return 0, nil, apperrors.InvalidRequest("A discount is verified by the actual compensating adjustment on the case stay's folio.")
			}
			var ok bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folio_items i JOIN folios f ON f.organisation_id=i.organisation_id AND f.id=i.folio_id WHERE i.organisation_id=$1 AND i.id=$2 AND f.stay_id=$3 AND i.kind='ADJUSTMENT' AND i.amount_minor=-$4::bigint AND i.currency=$5) AND NOT EXISTS(SELECT 1 FROM service_recovery_compensation_refs x WHERE x.organisation_id=$1 AND x.execution_reference=$2)`, t.Organisation, *reference, *c.StayID, comp.AmountMinor, comp.Currency).Scan(&ok); e != nil {
				return 0, nil, e
			}
			if !ok {
				return 0, nil, apperrors.Conflict("RECOVERY_DISCOUNT_EVIDENCE_MISMATCH", "The referenced adjustment must exactly match the approved discount on this stay's folio and be used once.")
			}
		default:
			if evidence == "" {
				return 0, nil, apperrors.InvalidRequest("Non-cash remedies record actual operator delivery evidence.")
			}
			kind, reference = "OPERATOR_EVIDENCE", nil
		}
		var ek any
		if kind != "" {
			ek = kind
		}
		var ev any
		if evidence != "" {
			ev = evidence
		}
		if _, e = tx.Exec(ctx, `UPDATE service_recovery_compensation_refs SET state=$3,execution_kind=$4,execution_reference=$5,evidence_reference=$6,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, comp.ID, state, ek, reference, ev); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE service_recovery_cases SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, c.ID); e != nil {
			return 0, nil, e
		}
		if e = h.recoveryCommit(ctx, tx, t, c, c.Version+1, "COMPENSATION_"+state, c.State, c.State, in.EvidenceReference, map[string]any{"compensation_id": comp.ID, "kind": comp.Kind, "execution_kind": kind, "execution_reference": reference, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := loadRecoveryCase(ctx, tx, t.Organisation, c.ID, now, false)
		return 200, map[string]any{"case": out, "compensation_id": comp.ID, "state": state, "execution_reference": reference}, e
	})
}
