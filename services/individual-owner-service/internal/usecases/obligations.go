package usecases

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Rent and deposit obligations (§13.14). The owner domain owns the business
// due schedule; payment-service owns money truth. PAID, PARTIAL, FUNDED,
// HELD, RELEASED and REFUNDED are only ever set from payment-service
// outcomes (read-back here, events in the payment consumer). The owner may
// waive or dispute obligations — business decisions — but never mark money
// as received.

const (
	AggRentObligation = "rent_obligation"
	AggDeposit        = "deposit"
)

// RentObligationView is one obligation.
type RentObligationView struct {
	ID                ids.ID                 `json:"id"`
	TenancyID         ids.ID                 `json:"tenancy_id"`
	Kind              string                 `json:"kind"`
	PeriodStart       string                 `json:"period_start"`
	PeriodEnd         string                 `json:"period_end"`
	DueDate           string                 `json:"due_date"`
	AmountMinor       int64                  `json:"amount_minor"`
	Currency          string                 `json:"currency"`
	Status            string                 `json:"status"`
	PaidMinor         int64                  `json:"paid_minor" doc:"As reported by payment-service"`
	RefundedMinor     int64                  `json:"refunded_minor"`
	PaymentIntentRef  *string                `json:"payment_intent_ref"`
	LastPaymentStatus *string                `json:"last_payment_status"`
	StatusReason      *string                `json:"status_reason"`
	History           []ObligationStatusView `json:"history,omitempty"`
	Version           int64                  `json:"version"`
}

// ObligationStatusView is one projected status change.
type ObligationStatusView struct {
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Source     string    `json:"source"`
	PaymentRef *string   `json:"payment_ref"`
	PaidMinor  *int64    `json:"paid_minor"`
	Reason     *string   `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
}

func obligationView(o db.RentObligation) RentObligationView {
	return RentObligationView{ID: o.ID, TenancyID: o.TenancyID, Kind: o.Kind, PeriodStart: o.PeriodStart.Format(time.DateOnly),
		PeriodEnd: o.PeriodEnd.Format(time.DateOnly), DueDate: o.DueDate.Format(time.DateOnly), AmountMinor: o.AmountMinor, Currency: o.Currency,
		Status: o.Status, PaidMinor: o.PaidMinor, RefundedMinor: o.RefundedMinor, PaymentIntentRef: o.PaymentIntentRef,
		LastPaymentStatus: o.LastPaymentStatus, StatusReason: o.StatusReason, Version: o.Version}
}

// ListRentObligations lists a tenancy's obligations.
func (s *Service) ListRentObligations(ctx context.Context, subj authz.Subject, tenancyID ids.ID, status string) ([]RentObligationView, error) {
	if _, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Read(authz.CapRentView)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListRentObligations(ctx, s.dbq(ctx), db.ListRentObligationsParams{TenancyID: tenancyID, Status: strp(status)})
	out := make([]RentObligationView, 0, len(rows))
	for _, r := range rows {
		out = append(out, obligationView(r))
	}
	return out, err
}

// GetRentObligation returns one obligation with its status history.
func (s *Service) GetRentObligation(ctx context.Context, subj authz.Subject, id ids.ID) (RentObligationView, error) {
	o, err := s.q.GetRentObligation(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return RentObligationView{}, notFound(AggRentObligation)
	}
	if err != nil {
		return RentObligationView{}, err
	}
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapRentView), o.PropertyID); err != nil {
		if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
			err = notFound(AggRentObligation)
		}
		return RentObligationView{}, err
	}
	v := obligationView(o)
	hs, err := s.q.ListObligationHistory(ctx, s.dbq(ctx), id)
	v.History = []ObligationStatusView{}
	for _, h := range hs {
		v.History = append(v.History, ObligationStatusView{FromStatus: h.FromStatus, ToStatus: h.ToStatus, Source: h.Source, PaymentRef: h.PaymentRef,
			PaidMinor: h.PaidMinor, Reason: h.Reason, OccurredAt: h.OccurredAt})
	}
	return v, err
}

// obligationTo transitions an obligation and appends its projection history.
func (s *Service) obligationTo(ctx context.Context, id ids.ID, to, source, reason string, paymentRef *string, paid *int64) error {
	from, _, err := s.transition(ctx, domain.RentObligation, id, lifecycle.Change{To: to, Command: lower(source), Reason: reason})
	if err != nil {
		return err
	}
	tx, now := mustTx(ctx), s.now()
	if reason != "" {
		if err := s.q.SetObligationReason(ctx, tx, db.SetObligationReasonParams{ID: id, StatusReason: &reason, UpdatedAt: now}); err != nil {
			return err
		}
	}
	return s.q.InsertObligationHistory(ctx, tx, db.InsertObligationHistoryParams{ID: ids.New(), ObligationID: id, FromStatus: from.Status, ToStatus: to,
		Source: source, PaymentRef: paymentRef, PaidMinor: paid, Reason: strp(reason), OccurredAt: now})
}

func (s *Service) obligationFor(ctx context.Context, subj authz.Subject, id ids.ID) (db.RentObligation, error) {
	o, err := s.q.GetRentObligation(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return o, notFound(AggRentObligation)
	}
	if err != nil {
		return o, err
	}
	if _, err := s.authorize(ctx, subj, authz.Do(authz.CapTenancyManage), o.PropertyID); err != nil {
		if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
			err = notFound(AggRentObligation)
		}
		return o, err
	}
	return s.q.GetRentObligationForUpdate(ctx, mustTx(ctx), id)
}

// ObligationDecisionInput waives, disputes or resolves an obligation.
type ObligationDecisionInput struct {
	Action     string `json:"action" v:"required,enum=WAIVE|DISPUTE|REINSTATE"`
	ReasonCode string `json:"reason_code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
}

// DecideObligation records an owner business decision on an obligation.
// WAIVE forgoes an unpaid amount (an open payment request is then cancelled
// with payment-service by the worker);
// DISPUTE flags it (lifecycle of the tenancy is unchanged); REINSTATE ends a
// dispute and re-derives the state from payment truth and the due date.
func (s *Service) DecideObligation(ctx context.Context, subj authz.Subject, id ids.ID, in ObligationDecisionInput) (RentObligationView, error) {
	if err := schema.Validate(in); err != nil {
		return RentObligationView{}, err
	}
	var out RentObligationView
	err := s.InTx(ctx, func(ctx context.Context) error {
		o, err := s.obligationFor(ctx, subj, id)
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		switch in.Action {
		case "WAIVE":
			if o.PaidMinor > 0 {
				return conflict("OBLIGATION_PARTLY_PAID", "Money was received for this obligation; resolve it with payment-service (refund) instead.")
			}
			err = s.obligationTo(ctx, id, "WAIVED", "OWNER", in.ReasonCode, nil, nil)
		case "DISPUTE":
			if err = s.obligationTo(ctx, id, "DISPUTED", "OWNER", in.ReasonCode, nil, nil); err == nil {
				err = s.q.SetTenancyDisputeFlag(ctx, tx, db.SetTenancyDisputeFlagParams{ID: o.TenancyID, DisputeFlag: true, UpdatedAt: s.now()})
			}
		case "REINSTATE":
			if o.Status != "DISPUTED" {
				return apperrors.InvalidStateTransition(o.Status, "DUE")
			}
			err = s.obligationTo(ctx, id, s.derivedStatus(ctx, o), "OWNER", in.ReasonCode, nil, nil)
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "rent_obligation."+lower(in.Action), AggRentObligation, id, map[string]any{"reason_code": in.ReasonCode}, nil); err != nil {
			return err
		}
		n, err := s.q.GetRentObligation(ctx, tx, id)
		out = obligationView(n)
		return err
	})
	return out, err
}

// derivedStatus is the state an undisputed obligation has from payment truth
// and its due date.
func (s *Service) derivedStatus(ctx context.Context, o db.RentObligation) string {
	switch {
	case o.PaidMinor >= o.AmountMinor:
		return "PAID"
	case o.PaidMinor > 0:
		return "PARTIAL"
	}
	t, err := s.q.GetTenancy(ctx, mustTx(ctx), o.TenancyID)
	today := dayOf(s.now())
	if err == nil && o.DueDate.AddDate(0, 0, int(t.LateFeeGraceDays)).Before(today) {
		return "OVERDUE"
	}
	return "DUE"
}

func dayOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// projectRentIntent applies payment-service's view of an obligation's intent.
func (s *Service) projectRentIntent(ctx context.Context, o db.RentObligation, in payment.Intent, source string, eventID *ids.ID) error {
	tx, now := mustTx(ctx), s.now()
	if err := s.q.SetObligationPayment(ctx, tx, db.SetObligationPaymentParams{ID: o.ID, PaidMinor: in.PaidMinor, RefundedMinor: in.RefundedMinor,
		LastPaymentStatus: &in.Status, LastCheckedAt: &now}); err != nil {
		return err
	}
	o.PaidMinor, o.RefundedMinor = in.PaidMinor, in.RefundedMinor
	target := ""
	switch in.Status {
	case payment.IntentSucceeded, payment.IntentPartiallyPaid:
		target = "PARTIAL"
		if in.PaidMinor >= o.AmountMinor {
			target = "PAID"
		}
		if in.PaidMinor == 0 {
			target = ""
		}
	case payment.IntentRefundPending:
		target = "REFUND_PENDING"
	case payment.IntentRefunded:
		target = "REFUNDED"
	}
	if target == "" || target == o.Status || o.Status == "DISPUTED" && target != "PAID" {
		return nil
	}
	if !domain.RentObligation.Can(o.Status, target) {
		s.Logger.WarnContext(ctx, "payment outcome not applicable to obligation state", "obligation_id", o.ID, "status", o.Status, "payment_status", in.Status)
		return nil
	}
	if err := s.obligationToWithEvent(ctx, o.ID, target, source, in, eventID); err != nil {
		return err
	}
	return nil
}

func (s *Service) obligationToWithEvent(ctx context.Context, id ids.ID, to, source string, in payment.Intent, eventID *ids.ID) error {
	from, _, err := s.transition(ctx, domain.RentObligation, id, lifecycle.Change{To: to, Command: lower(source)})
	if err != nil {
		return err
	}
	return s.q.InsertObligationHistory(ctx, mustTx(ctx), db.InsertObligationHistoryParams{ID: ids.New(), ObligationID: id, FromStatus: from.Status, ToStatus: to,
		Source: source, PaymentRef: &in.Ref, SourceEventID: eventID, PaidMinor: &in.PaidMinor, OccurredAt: s.now()})
}

// ---- deposits ----

// DeductionItem is one itemized deposit deduction with evidence.
type DeductionItem struct {
	Code        string   `json:"code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
	Description string   `json:"description" v:"required,max=500"`
	AmountMinor int64    `json:"amount_minor" v:"required,min=1"`
	MediaIDs    []ids.ID `json:"media_ids" v:"required,max=10" doc:"Evidence (photos, invoices) in media-document-service"`
}

// DepositView is a deposit obligation.
type DepositView struct {
	ID                  ids.ID          `json:"id"`
	AmountMinor         int64           `json:"amount_minor"`
	Currency            string          `json:"currency"`
	Status              string          `json:"status"`
	FundedMinor         int64           `json:"funded_minor" doc:"As reported by payment-service"`
	ReleaseDueDays      int32           `json:"release_due_days"`
	Deductions          []DeductionItem `json:"deductions"`
	DeductionTotalMinor int64           `json:"deduction_total_minor"`
	ProposedAt          *time.Time      `json:"proposed_at"`
	ResponseDueAt       *time.Time      `json:"response_due_at"`
	TenantResponse      *string         `json:"tenant_response"`
	DisputedMinor       int64           `json:"disputed_minor"`
	ReleasedMinor       int64           `json:"released_minor"`
	ResolutionOwner     *int64          `json:"resolution_owner_minor"`
	Version             int64           `json:"version"`
}

func depositView(d db.DepositObligation) DepositView {
	v := DepositView{ID: d.ID, AmountMinor: d.AmountMinor, Currency: d.Currency, Status: d.Status, FundedMinor: d.FundedMinor, ReleaseDueDays: d.ReleaseDueDays,
		DeductionTotalMinor: d.DeductionTotalMinor, ProposedAt: d.ProposedAt, ResponseDueAt: d.ResponseDueAt, TenantResponse: d.TenantResponse,
		DisputedMinor: d.DisputedMinor, ReleasedMinor: d.ReleasedMinor, ResolutionOwner: d.ResolutionOwnerMinor, Version: d.Version, Deductions: []DeductionItem{}}
	_ = json.Unmarshal(d.Deductions, &v.Deductions)
	return v
}

// GetDeposit returns a tenancy's deposit obligation.
func (s *Service) GetDeposit(ctx context.Context, subj authz.Subject, tenancyID ids.ID) (DepositView, error) {
	if _, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Read(authz.CapRentView)); err != nil {
		return DepositView{}, err
	}
	d, err := s.q.GetDepositByTenancy(ctx, s.dbq(ctx), tenancyID)
	if isNoRows(err) {
		return DepositView{}, apperrors.NotFound("DEPOSIT_NOT_FOUND", "The tenancy has no platform-held deposit.")
	}
	return depositView(d), err
}

func (s *Service) depositTo(ctx context.Context, id ids.ID, to, cmd, reason string) error {
	_, _, err := s.transition(ctx, domain.Deposit, id, lifecycle.Change{To: to, Command: cmd, Reason: reason})
	return err
}

// DepositSettlementInput proposes how the held deposit is settled.
type DepositSettlementInput struct {
	Deductions []DeductionItem `json:"deductions" v:"max=50" doc:"Empty = release in full"`
}

// MediaRefs lists referenced media.
func (in DepositSettlementInput) MediaRefs() []ids.ID {
	var out []ids.ID
	for _, d := range in.Deductions {
		out = append(out, d.MediaIDs...)
	}
	return out
}

// ProposeDepositSettlement proposes the deposit settlement after the
// move-out inspection: a full release, or itemized deductions with evidence
// for the tenant to accept or dispute.
func (s *Service) ProposeDepositSettlement(ctx context.Context, subj authz.Subject, tenancyID ids.ID, in DepositSettlementInput, ev Evidence) (DepositView, error) {
	if err := schema.Validate(in); err != nil {
		return DepositView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return DepositView{}, err
	}
	var out DepositView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Do(authz.CapTenancyManage))
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		mo, err := s.q.GetMoveOutForUpdate(ctx, tx, t.ID)
		if isNoRows(err) || (err == nil && mo.Status != "INSPECTED") {
			return conflict("MOVE_OUT_INSPECTION_REQUIRED", "Complete the move-out inspection before settling the deposit.")
		}
		if err != nil {
			return err
		}
		d, err := s.q.GetDepositByTenancy(ctx, tx, t.ID)
		if isNoRows(err) {
			return apperrors.NotFound("DEPOSIT_NOT_FOUND", "The tenancy has no platform-held deposit.")
		}
		if err != nil {
			return err
		}
		if d, err = s.q.GetDepositForUpdate(ctx, tx, d.ID); err != nil {
			return err
		}
		var total int64
		for _, it := range in.Deductions {
			total += it.AmountMinor
		}
		if total > d.AmountMinor {
			return precondition("DEDUCTIONS_EXCEED_DEPOSIT", "Deductions cannot exceed the deposit.")
		}
		to := "RELEASE_PENDING"
		if total > 0 {
			to = "DEDUCTION_PROPOSED"
		}
		if err := s.depositTo(ctx, d.ID, to, "propose_settlement", ""); err != nil {
			return err
		}
		items := in.Deductions
		if items == nil {
			items = []DeductionItem{}
		}
		lp, err := s.legalPolicy(ctx, t.PropertyID)
		if err != nil {
			return err
		}
		due := now.AddDate(0, 0, int(lp.Rules.DepositResponseDays))
		if err := s.q.SetDepositDeductions(ctx, tx, db.SetDepositDeductionsParams{ID: d.ID, Deductions: jsonb(items), DeductionTotalMinor: total,
			ProposedAt: &now, ResponseDueAt: &due}); err != nil {
			return err
		}
		if _, _, err := s.transition(ctx, domain.MoveOut, mo.ID, lifecycle.Change{To: "SETTLEMENT_PENDING", Command: "deposit_proposed"}); err != nil {
			return err
		}
		if err := s.record(ctx, "deposit.propose", AggDeposit, d.ID, map[string]any{"deduction_total_minor": total, "items": len(items)}, lp.auditRef()); err != nil {
			return err
		}
		n, err := s.q.GetDepositByTenancy(ctx, tx, t.ID)
		out = depositView(n)
		return err
	})
	return out, err
}

// CustomerDepositResponse is the tenant's answer to proposed deductions.
type CustomerDepositResponse struct {
	Decision      string  `json:"decision" v:"required,enum=ACCEPT|DISPUTE"`
	DisputedMinor int64   `json:"disputed_minor,omitempty" v:"min=0" doc:"Part of the deductions disputed (default: all)"`
	Reason        *string `json:"reason,omitempty" v:"max=1000"`
}

// CustomerRespondDeposit accepts or disputes deductions. A dispute freezes
// only the disputed amount: the undisputed refund proceeds.
func (s *Service) CustomerRespondDeposit(ctx context.Context, subj authz.Subject, tenancyID ids.ID, in CustomerDepositResponse) (DepositView, error) {
	if err := schema.Validate(in); err != nil {
		return DepositView{}, err
	}
	var out DepositView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.customerTenancy(ctx, subj, tenancyID, true)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		d, err := s.q.GetDepositByTenancy(ctx, tx, t.ID)
		if isNoRows(err) {
			return apperrors.NotFound("DEPOSIT_NOT_FOUND", "The tenancy has no platform-held deposit.")
		}
		if err != nil {
			return err
		}
		if d, err = s.q.GetDepositForUpdate(ctx, tx, d.ID); err != nil {
			return err
		}
		if d.Status != "DEDUCTION_PROPOSED" || d.TenantResponse != nil {
			return conflict("DEPOSIT_NOT_AWAITING_RESPONSE", "No deduction proposal is awaiting your response.")
		}
		disputed := int64(0)
		resp := "ACCEPTED"
		if in.Decision == "DISPUTE" {
			resp, disputed = "DISPUTED", in.DisputedMinor
			if disputed == 0 || disputed > d.DeductionTotalMinor {
				disputed = d.DeductionTotalMinor
			}
			if err := s.depositTo(ctx, d.ID, "DISPUTED", "tenant_dispute", truncate(deref(in.Reason), 200)); err != nil {
				return err
			}
			if err := s.q.SetTenancyDisputeFlag(ctx, tx, db.SetTenancyDisputeFlagParams{ID: t.ID, DisputeFlag: true, UpdatedAt: now}); err != nil {
				return err
			}
		}
		if err := s.q.SetDepositTenantResponse(ctx, tx, db.SetDepositTenantResponseParams{ID: d.ID, TenantResponse: &resp, DisputedMinor: disputed, UpdatedAt: now}); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, t.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateTenantResponse, map[string]string{"property_title": p.Title, "subject": "deposit deductions " + lower(resp)}); err != nil {
			return err
		}
		if err := s.record(ctx, "deposit.tenant_response", AggDeposit, d.ID, map[string]any{"decision": in.Decision, "disputed_minor": disputed}, nil); err != nil {
			return err
		}
		n, err := s.q.GetDepositByTenancy(ctx, tx, t.ID)
		out = depositView(n)
		return err
	})
	return out, err
}

// DepositResolutionInput settles the frozen (disputed) amount.
type DepositResolutionInput struct {
	OwnerAwardedMinor int64   `json:"owner_awarded_minor" v:"min=0" doc:"Part of the disputed amount kept by the owner (the rest is refunded)"`
	Note              *string `json:"note,omitempty" v:"max=1000"`
}

// ConcedeDepositDispute lets the owner give up the disputed deductions; an
// operator decides any other split (admin dispute contract).
func (s *Service) ConcedeDepositDispute(ctx context.Context, subj authz.Subject, tenancyID ids.ID) (DepositView, error) {
	return s.resolveDeposit(ctx, subj, tenancyID, authz.Do(authz.CapDisputesManage), DepositResolutionInput{})
}

// AdminResolveDepositDispute records an operator's decision on a deposit dispute.
func (s *Service) AdminResolveDepositDispute(ctx context.Context, subj authz.Subject, tenancyID ids.ID, in DepositResolutionInput) (DepositView, error) {
	if err := schema.Validate(in); err != nil {
		return DepositView{}, err
	}
	return s.resolveDeposit(ctx, subj, tenancyID, authz.ActInternalAdminDispute, in)
}

func (s *Service) resolveDeposit(ctx context.Context, subj authz.Subject, tenancyID ids.ID, action authz.Action, in DepositResolutionInput) (DepositView, error) {
	var out DepositView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx, now := mustTx(ctx), s.now()
		t, err := s.q.GetTenancy(ctx, tx, tenancyID)
		if isNoRows(err) {
			return notFound(AggTenancy)
		}
		if err != nil {
			return err
		}
		if action == authz.ActInternalAdminDispute {
			_, err = s.Authz.Require(ctx, subj, action, authz.Resource{})
		} else {
			_, err = s.authorize(ctx, subj, action, t.PropertyID)
		}
		if err != nil {
			return err
		}
		d, err := s.q.GetDepositByTenancy(ctx, tx, tenancyID)
		if err == nil {
			d, err = s.q.GetDepositForUpdate(ctx, tx, d.ID)
		}
		if isNoRows(err) {
			return apperrors.NotFound("DEPOSIT_NOT_FOUND", "The tenancy has no platform-held deposit.")
		}
		if err != nil {
			return err
		}
		if d.Status != "DISPUTED" || d.ResolvedAt != nil {
			return conflict("DEPOSIT_NOT_DISPUTED", "The deposit has no open dispute.")
		}
		if d.ReleasedMinor == 0 && d.ReleaseRequestRef != nil {
			return conflict("DEPOSIT_RELEASE_IN_PROGRESS", "The undisputed release is still being processed; retry shortly.")
		}
		if in.OwnerAwardedMinor > d.DisputedMinor {
			return precondition("AWARD_EXCEEDS_DISPUTE", "The award cannot exceed the disputed amount.")
		}
		if err := s.q.SetDepositResolution(ctx, tx, db.SetDepositResolutionParams{ID: d.ID, ResolutionOwnerMinor: &in.OwnerAwardedMinor, ResolvedAt: &now}); err != nil {
			return err
		}
		if err := s.record(ctx, "deposit.resolve", AggDeposit, d.ID, map[string]any{"owner_awarded_minor": in.OwnerAwardedMinor}, map[string]any{"note": deref(in.Note)}); err != nil {
			return err
		}
		n, err := s.q.GetDepositByTenancy(ctx, tx, tenancyID)
		out = depositView(n)
		return err
	})
	return out, err
}

// projectDepositIntent applies payment-service's view of the funding intent.
func (s *Service) projectDepositIntent(ctx context.Context, d db.DepositObligation, in payment.Intent) error {
	now := s.now()
	if err := s.q.SetDepositFunding(ctx, mustTx(ctx), db.SetDepositFundingParams{ID: d.ID, FundedMinor: in.PaidMinor, LastPaymentStatus: &in.Status,
		LastCheckedAt: &now}); err != nil {
		return err
	}
	if in.PaidMinor < d.AmountMinor {
		return nil
	}
	cur := d.Status
	if cur == "REQUIRED" && (in.Status == payment.IntentSucceeded || in.Status == payment.IntentHeld) {
		if err := s.depositTo(ctx, d.ID, "FUNDED", "payment_succeeded", ""); err != nil {
			return err
		}
		cur = "FUNDED"
	}
	if cur == "FUNDED" && in.Status == payment.IntentHeld {
		return s.depositTo(ctx, d.ID, "HELD", "payment_held", "")
	}
	return nil
}

// projectDepositRelease applies a release outcome.
func (s *Service) projectDepositRelease(ctx context.Context, d db.DepositObligation, r payment.Release) error {
	now := s.now()
	tx := mustTx(ctx)
	if r.Status == payment.ReleaseFailed {
		// Release again with a fresh request; the failed one is audited.
		_, err := tx.Exec(ctx, `UPDATE deposit_obligations SET release_request_ref = NULL, last_checked_at = $2 WHERE id = $1`, d.ID, now)
		if err == nil {
			err = s.record(ctx, "deposit.release_failed", AggDeposit, d.ID, map[string]any{"release_ref": r.Ref}, nil)
		}
		return err
	}
	if r.Status != payment.ReleaseCompleted && r.Status != payment.ReleasePartial {
		_, err := tx.Exec(ctx, `UPDATE deposit_obligations SET last_checked_at = $2 WHERE id = $1`, d.ID, now)
		return err
	}
	if err := s.q.SetDepositReleased(ctx, tx, db.SetDepositReleasedParams{ID: d.ID, ReleasedMinor: r.ReleasedMinor, LastCheckedAt: &now}); err != nil {
		return err
	}
	switch {
	case d.Status == "DISPUTED" && d.ResolvedAt == nil:
		return nil // undisputed part released; the dispute keeps the rest frozen
	case d.Status == "RELEASE_PENDING":
		return s.depositTo(ctx, d.ID, "RELEASED", "release_completed", "")
	default:
		ownerKept := d.DeductionTotalMinor - d.DisputedMinor + deref(d.ResolutionOwnerMinor)
		if d.Status == "DEDUCTION_PROPOSED" {
			ownerKept = d.DeductionTotalMinor
		}
		to := "RELEASED"
		if ownerKept > 0 {
			to = "PARTIAL_RELEASED"
		}
		if err := s.depositTo(ctx, d.ID, to, "release_completed", ""); err != nil {
			return err
		}
		if d.Status == "DISPUTED" {
			return s.q.SetTenancyDisputeFlag(ctx, tx, db.SetTenancyDisputeFlagParams{ID: d.TenancyID, DisputeFlag: false, UpdatedAt: now})
		}
		return nil
	}
}

// releaseRequest builds the next release instruction for a deposit.
func releaseRequest(d db.DepositObligation, owner ids.ID) (payment.DepositReleaseRequest, string) {
	r := payment.DepositReleaseRequest{DepositID: d.ID, IntentRef: deref(d.PaymentIntentRef), Currency: d.Currency, OwnerAccountID: owner}
	switch {
	case d.Status == "RELEASE_PENDING":
		r.RefundMinor = d.AmountMinor
		return r, "deposit:" + d.ID.String() + ":release"
	case d.Status == "DISPUTED" && d.ResolvedAt != nil:
		r.DeductionMinor = deref(d.ResolutionOwnerMinor)
		r.RefundMinor = d.DisputedMinor - r.DeductionMinor
		r.DeductionSummary = "dispute resolution"
		return r, "deposit:" + d.ID.String() + ":resolution"
	case d.Status == "DISPUTED":
		r.RefundMinor = d.AmountMinor - d.DeductionTotalMinor
		r.DeductionMinor = d.DeductionTotalMinor - d.DisputedMinor
		r.WithheldMinor = d.DisputedMinor
		r.DeductionSummary = "undisputed deductions"
		return r, "deposit:" + d.ID.String() + ":release"
	default:
		r.RefundMinor = d.AmountMinor - d.DeductionTotalMinor
		r.DeductionMinor = d.DeductionTotalMinor
		r.DeductionSummary = "agreed deductions"
		return r, "deposit:" + d.ID.String() + ":release"
	}
}

// ---- jobs (§11 rent schedule projection, arrears, move-out/deposit deadlines) ----

// RunRentSchedule materializes upcoming obligations for running tenancies.
func (s *Service) RunRentSchedule(ctx context.Context, batch int32) error {
	horizon := s.now().Add(s.Policy.Domain.RentScheduleHorizon)
	ts, err := s.q.TenanciesNeedingSchedule(ctx, s.Pool, db.TenanciesNeedingScheduleParams{Horizon: horizon, MaxRows: batch})
	if err != nil {
		return err
	}
	for _, t := range ts {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			cur, err := s.q.GetTenancyForUpdate(ctx, mustTx(ctx), t.ID)
			if err != nil || cur.Status == "ENDED" {
				return err
			}
			return s.materializeSchedule(ctx, cur)
		}); err != nil {
			return err
		}
	}
	return nil
}

// RunRentObligations opens due obligations, marks arrears (with policy late
// fees and an owner notification), requests payment intents and reconciles
// payment truth by read-back.
func (s *Service) RunRentObligations(ctx context.Context, batch int32) error {
	now := s.now()
	today := dayOf(now)
	open, err := s.q.ObligationsToOpen(ctx, s.Pool, db.ObligationsToOpenParams{OpenUntil: today.Add(s.Policy.Domain.RentIntentLead), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range open {
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.obligationTo(ctx, id, "DUE", "SCHEDULE", "", nil, nil) }); err != nil && !benign(err) {
			return err
		}
	}
	overdue, err := s.q.ObligationsToMarkOverdue(ctx, s.Pool, db.ObligationsToMarkOverdueParams{Today: today, MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range overdue {
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.markOverdue(ctx, id) }); err != nil && !benign(err) {
			return err
		}
	}
	if s.Payment == nil {
		return nil
	}
	need, err := s.q.ObligationsNeedingIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range need {
		if err := s.requestRentIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "payment intent request deferred", "obligation_id", id, "error", err)
		}
	}
	cancel, err := s.q.ObligationsToCancelIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range cancel {
		if err := s.cancelRentIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "payment intent cancellation deferred", "obligation_id", id, "error", err)
		}
	}
	recon, err := s.q.ObligationsToReconcile(ctx, s.Pool, db.ObligationsToReconcileParams{CheckedBefore: now.Add(-s.Policy.Domain.PaymentReconcileAfter), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range recon {
		if err := s.reconcileRent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "payment read-back deferred", "obligation_id", id, "error", err)
		}
	}
	return nil
}

func benign(err error) bool {
	c := apperrors.CodeOf(err)
	return c == apperrors.CodeInvalidStateTransition || c == "VERSION_CONFLICT"
}

func (s *Service) markOverdue(ctx context.Context, id ids.ID) error {
	tx := mustTx(ctx)
	o, err := s.q.GetRentObligationForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := s.obligationTo(ctx, id, "OVERDUE", "SCHEDULE", "PAYMENT_OVERDUE", nil, nil); err != nil {
		return err
	}
	t, err := s.q.GetTenancy(ctx, tx, o.TenancyID)
	if err != nil {
		return err
	}
	if o.Kind == "RENT" && t.LateFeeMinor > 0 {
		// The agreed late fee is charged automatically only under an approved
		// jurisdiction policy that allows it; otherwise the owner decides.
		lp, err := s.legalPolicy(ctx, t.PropertyID)
		if err != nil {
			return err
		}
		if lp.Approved && lp.Rules.AutomaticLateFees && lateFeeWithin(t, lp.Rules) {
			if _, err := s.insertLateFee(ctx, t, o); err != nil {
				return err
			}
			if err := s.record(ctx, "rent.late_fee.assessed", AggRentObligation, o.ID, map[string]any{"amount_minor": t.LateFeeMinor, "by": "POLICY"}, lp.auditRef()); err != nil {
				return err
			}
		} else {
			md := lp.auditRef()
			md["review"] = "MANUAL"
			if err := s.record(ctx, "rent.late_fee.manual_review", AggRentObligation, o.ID, map[string]any{"agreed_minor": t.LateFeeMinor}, md); err != nil {
				return err
			}
		}
	}
	p, err := s.q.GetProperty(ctx, tx, o.PropertyID)
	if err != nil {
		return err
	}
	return s.notifyOwner(ctx, p.OwnerAccountID, TemplateRentOverdue, map[string]string{"property_title": p.Title, "due_date": o.DueDate.Format(time.DateOnly)})
}

func lateFeeWithin(t db.Tenancy, r LegalRules) bool {
	return t.LateFeeMinor <= t.RentMinor*r.MaxLateFeeBps/10000
}

// insertLateFee adds the period's LATE_FEE obligation (at most one).
func (s *Service) insertLateFee(ctx context.Context, t db.Tenancy, o db.RentObligation) (*db.RentObligation, error) {
	tx, now := mustTx(ctx), s.now()
	lf, err := s.q.InsertRentObligation(ctx, tx, db.InsertRentObligationParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, Kind: "LATE_FEE",
		PeriodStart: o.PeriodStart, PeriodEnd: o.PeriodEnd, DueDate: dayOf(now), AmountMinor: t.LateFeeMinor, Currency: t.Currency, CreatedAt: now})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lf, domain.RentObligation.RecordCreation(ctx, tx, lf.ID, &lf.PropertyID, "SCHEDULED", now)
}

// AssessLateFee is the owner's explicit decision to charge the agreed late
// fee on an overdue rent obligation (the manual path where no approved
// jurisdiction policy charges it automatically). The fee must be within the
// policy cap and is charged at most once per period.
func (s *Service) AssessLateFee(ctx context.Context, subj authz.Subject, id ids.ID) (RentObligationView, error) {
	var out RentObligationView
	err := s.InTx(ctx, func(ctx context.Context) error {
		o, err := s.obligationFor(ctx, subj, id)
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		if o.Kind != "RENT" || o.Status != "OVERDUE" {
			return conflict("LATE_FEE_NOT_APPLICABLE", "Late fees apply only to overdue rent.")
		}
		t, err := s.q.GetTenancy(ctx, tx, o.TenancyID)
		if err != nil {
			return err
		}
		if t.LateFeeMinor == 0 {
			return precondition("NO_AGREED_LATE_FEE", "The tenancy agreement has no late fee.")
		}
		lp, err := s.legalPolicy(ctx, t.PropertyID)
		if err != nil {
			return err
		}
		if !lateFeeWithin(t, lp.Rules) {
			return precondition("LATE_FEE_ABOVE_POLICY", "The agreed late fee exceeds the jurisdiction's late fee cap.")
		}
		lf, err := s.insertLateFee(ctx, t, o)
		if err != nil {
			return err
		}
		if lf == nil {
			return conflict("LATE_FEE_ALREADY_ASSESSED", "The late fee for this period was already assessed.")
		}
		if err := s.record(ctx, "rent.late_fee.assessed", AggRentObligation, o.ID, map[string]any{"amount_minor": t.LateFeeMinor, "by": "OWNER"}, lp.auditRef()); err != nil {
			return err
		}
		out = obligationView(*lf)
		return nil
	})
	return out, err
}

func (s *Service) requestRentIntent(ctx context.Context, id ids.ID) error {
	o, err := s.q.GetRentObligation(ctx, s.Pool, id)
	if err != nil || o.PaymentIntentRef != nil {
		return err
	}
	t, err := s.q.GetTenancy(ctx, s.Pool, o.TenancyID)
	if err != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, s.Pool, o.PropertyID)
	if err != nil {
		return err
	}
	purpose := payment.PurposeRent
	if o.Kind == "LATE_FEE" {
		purpose = payment.PurposeLateFee
	}
	in, err := s.Payment.CreateIntent(ctx, payment.IntentRequest{Purpose: purpose, ReferenceType: AggRentObligation, ReferenceID: o.ID,
		PayeeOwnerAccountID: p.OwnerAccountID, PayerDomain: string(platform.DomainMarketplace), PayerRefID: t.CustomerRefID, AmountMinor: o.AmountMinor,
		Currency: o.Currency, DueDate: o.DueDate}, "rent_obligation:"+o.ID.String())
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.q.GetRentObligationForUpdate(ctx, mustTx(ctx), id)
		if err != nil || cur.PaymentIntentRef != nil {
			return err
		}
		if err := s.q.SetObligationIntent(ctx, mustTx(ctx), db.SetObligationIntentParams{ID: id, PaymentIntentRef: &in.Ref, LastPaymentStatus: &in.Status,
			LastCheckedAt: ptr(s.now())}); err != nil {
			return err
		}
		return s.projectRentIntent(ctx, cur, in, "PAYMENT_READBACK", nil)
	})
}

func (s *Service) reconcileRent(ctx context.Context, id ids.ID) error {
	o, err := s.q.GetRentObligation(ctx, s.Pool, id)
	if err != nil || o.PaymentIntentRef == nil {
		return err
	}
	in, err := s.Payment.GetIntent(ctx, *o.PaymentIntentRef)
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.q.GetRentObligationForUpdate(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		return s.projectRentIntent(ctx, cur, in, "PAYMENT_READBACK", nil)
	})
}

// RunDeposits requests escrow intents, reconciles funding and releases, and
// applies the tenant's lapsed response window: deductions proceed only where
// an approved jurisdiction policy makes silence acceptance; otherwise the
// deductions are frozen for manual review (owner concession or operator
// decision) while the undisputed refund proceeds.
func (s *Service) RunDeposits(ctx context.Context, batch int32) error {
	now := s.now()
	lapsed, err := s.q.DepositsAwaitingResponse(ctx, s.Pool, db.DepositsAwaitingResponseParams{ResponseDueAt: &now, Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range lapsed {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			tx := mustTx(ctx)
			d, err := s.q.GetDepositForUpdate(ctx, tx, id)
			if err != nil || d.Status != "DEDUCTION_PROPOSED" || d.TenantResponse != nil {
				return err
			}
			lp, err := s.legalPolicy(ctx, d.PropertyID)
			if err != nil {
				return err
			}
			if lp.Approved && lp.Rules.DepositSilenceAcceptance {
				resp := "ACCEPTED"
				if err := s.q.SetDepositTenantResponse(ctx, tx, db.SetDepositTenantResponseParams{ID: id, TenantResponse: &resp, UpdatedAt: now}); err != nil {
					return err
				}
				return s.record(ctx, "deposit.response_window_lapsed", AggDeposit, id, map[string]any{"outcome": "ACCEPTED_BY_POLICY"}, lp.auditRef())
			}
			resp := "NO_RESPONSE"
			if err := s.depositTo(ctx, id, "DISPUTED", "no_response_manual_review", "NO_RESPONSE"); err != nil {
				return err
			}
			if err := s.q.SetDepositTenantResponse(ctx, tx, db.SetDepositTenantResponseParams{ID: id, TenantResponse: &resp, DisputedMinor: d.DeductionTotalMinor, UpdatedAt: now}); err != nil {
				return err
			}
			md := lp.auditRef()
			md["review"] = "MANUAL"
			return s.record(ctx, "deposit.response_window_lapsed", AggDeposit, id, map[string]any{"outcome": "MANUAL_REVIEW", "frozen_minor": d.DeductionTotalMinor}, md)
		}); err != nil {
			return err
		}
	}
	if s.Payment == nil {
		return nil
	}
	need, err := s.q.DepositsNeedingIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range need {
		if err := s.requestDepositIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "deposit intent request deferred", "deposit_id", id, "error", err)
		}
	}
	stale, err := s.q.DepositsToCancelIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range stale {
		if err := s.cancelDepositIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "deposit intent cancellation deferred", "deposit_id", id, "error", err)
		}
	}
	ready, err := s.q.DepositsReadyForRelease(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range ready {
		if err := s.requestDepositRelease(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "deposit release request deferred", "deposit_id", id, "error", err)
		}
	}
	recon, err := s.q.DepositsToReconcile(ctx, s.Pool, db.DepositsToReconcileParams{CheckedBefore: now.Add(-s.Policy.Domain.PaymentReconcileAfter), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range recon {
		if err := s.reconcileDeposit(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "deposit read-back deferred", "deposit_id", id, "error", err)
		}
	}
	return nil
}

func (s *Service) requestDepositIntent(ctx context.Context, id ids.ID) error {
	var d db.DepositObligation
	var t db.Tenancy
	var p db.Property
	var err error
	if d, err = s.q.GetDepositForUpdate(ctx, s.Pool, id); err != nil || d.PaymentIntentRef != nil {
		return err
	}
	if t, err = s.q.GetTenancy(ctx, s.Pool, d.TenancyID); err != nil {
		return err
	}
	if p, err = s.q.GetProperty(ctx, s.Pool, d.PropertyID); err != nil {
		return err
	}
	in, err := s.Payment.CreateIntent(ctx, payment.IntentRequest{Purpose: payment.PurposeDeposit, ReferenceType: "deposit_obligation", ReferenceID: d.ID,
		PayeeOwnerAccountID: p.OwnerAccountID, PayerDomain: string(platform.DomainMarketplace), PayerRefID: t.CustomerRefID, AmountMinor: d.AmountMinor,
		Currency: d.Currency, DueDate: t.StartDate, Escrow: true}, "deposit:"+d.ID.String())
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.q.GetDepositForUpdate(ctx, mustTx(ctx), id)
		if err != nil || cur.PaymentIntentRef != nil {
			return err
		}
		if err := s.q.SetDepositIntent(ctx, mustTx(ctx), db.SetDepositIntentParams{ID: id, PaymentIntentRef: &in.Ref, LastCheckedAt: ptr(s.now())}); err != nil {
			return err
		}
		return s.projectDepositIntent(ctx, cur, in)
	})
}

func (s *Service) requestDepositRelease(ctx context.Context, id ids.ID) error {
	d, err := s.q.GetDepositForUpdate(ctx, s.Pool, id)
	if err != nil || d.ReleaseRequestRef != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, s.Pool, d.PropertyID)
	if err != nil {
		return err
	}
	req, key := releaseRequest(d, p.OwnerAccountID)
	r, err := s.Payment.RequestDepositRelease(ctx, req, key)
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.q.GetDepositForUpdate(ctx, mustTx(ctx), id)
		if err != nil || cur.ReleaseRequestRef != nil {
			return err
		}
		now := s.now()
		if err := s.q.SetDepositReleaseRequest(ctx, mustTx(ctx), db.SetDepositReleaseRequestParams{ID: id, ReleaseRequestRef: &r.Ref, ReleaseRequestedAt: &now}); err != nil {
			return err
		}
		if err := s.record(ctx, "deposit.release_requested", AggDeposit, id, map[string]any{"refund_minor": req.RefundMinor,
			"deduction_minor": req.DeductionMinor, "withheld_minor": req.WithheldMinor}, nil); err != nil {
			return err
		}
		cur.ReleaseRequestRef = &r.Ref
		return s.projectDepositRelease(ctx, cur, r)
	})
}

func (s *Service) reconcileDeposit(ctx context.Context, id ids.ID) error {
	d, err := s.q.GetDepositForUpdate(ctx, s.Pool, id)
	if err != nil {
		return err
	}
	switch {
	case d.ReleaseRequestRef != nil:
		r, err := s.Payment.GetRelease(ctx, *d.ReleaseRequestRef)
		if err != nil {
			return err
		}
		return s.InTx(ctx, func(ctx context.Context) error {
			cur, err := s.q.GetDepositForUpdate(ctx, mustTx(ctx), id)
			if err != nil || cur.ReleaseRequestRef == nil || *cur.ReleaseRequestRef != r.Ref {
				return err
			}
			return s.projectDepositRelease(ctx, cur, r)
		})
	case d.PaymentIntentRef != nil:
		in, err := s.Payment.GetIntent(ctx, *d.PaymentIntentRef)
		if err != nil {
			return err
		}
		return s.InTx(ctx, func(ctx context.Context) error {
			cur, err := s.q.GetDepositForUpdate(ctx, mustTx(ctx), id)
			if err != nil {
				return err
			}
			return s.projectDepositIntent(ctx, cur, in)
		})
	}
	return nil
}

// cancelRentIntent stops collection of a waived obligation. If payment-
// service reports the money already arrived, the obligation keeps WAIVED and
// the receipt is audited for a refund decision (never silently re-labelled).
func (s *Service) cancelRentIntent(ctx context.Context, id ids.ID) error {
	o, err := s.q.GetRentObligation(ctx, s.Pool, id)
	if err != nil || o.PaymentIntentRef == nil {
		return err
	}
	in, err := s.Payment.CancelIntent(ctx, *o.PaymentIntentRef, "obligation waived: "+deref(o.StatusReason))
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		now := s.now()
		if err := s.q.SetObligationPayment(ctx, mustTx(ctx), db.SetObligationPaymentParams{ID: id, PaidMinor: in.PaidMinor, RefundedMinor: in.RefundedMinor,
			LastPaymentStatus: &in.Status, LastCheckedAt: &now}); err != nil {
			return err
		}
		if in.PaidMinor > 0 {
			return s.record(ctx, "rent_obligation.payment_after_waiver", AggRentObligation, id, map[string]any{"paid_minor": in.PaidMinor},
				map[string]any{"attention": "money received for a waived obligation; refund through payment-service"})
		}
		return nil
	})
}

func (s *Service) cancelDepositIntent(ctx context.Context, id ids.ID) error {
	d, err := s.q.GetDepositForUpdate(ctx, s.Pool, id)
	if err != nil || d.PaymentIntentRef == nil {
		return err
	}
	in, err := s.Payment.CancelIntent(ctx, *d.PaymentIntentRef, "tenancy cancelled before move-in")
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		now := s.now()
		if err := s.q.SetDepositFunding(ctx, mustTx(ctx), db.SetDepositFundingParams{ID: id, FundedMinor: in.PaidMinor, LastPaymentStatus: &in.Status,
			LastCheckedAt: &now}); err != nil {
			return err
		}
		if in.PaidMinor > 0 {
			return s.record(ctx, "deposit.payment_after_cancellation", AggDeposit, id, map[string]any{"paid_minor": in.PaidMinor},
				map[string]any{"attention": "deposit received for a cancelled tenancy; refund through payment-service"})
		}
		return nil
	})
}

// ProjectPaymentIntent applies a payment-service intent outcome delivered by
// event (payment consumer). Unknown references are retried (the intent ref
// may not be committed yet); duplicates are idempotent.
func (s *Service) ProjectPaymentIntent(ctx context.Context, eventID ids.ID, in payment.Intent) error {
	return s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		if o, err := s.q.GetRentObligationByIntent(ctx, tx, &in.Ref); err == nil {
			cur, err := s.q.GetRentObligationForUpdate(ctx, tx, o.ID)
			if err != nil {
				return err
			}
			return s.projectRentIntent(ctx, cur, in, "PAYMENT_EVENT", &eventID)
		} else if !isNoRows(err) {
			return err
		}
		if d, err := s.q.GetDepositByIntent(ctx, tx, &in.Ref); err == nil {
			cur, err := s.q.GetDepositForUpdate(ctx, tx, d.ID)
			if err != nil {
				return err
			}
			return s.projectDepositIntent(ctx, cur, in)
		} else if !isNoRows(err) {
			return err
		}
		if m, err := s.q.GetMilestoneByIntent(ctx, tx, &in.Ref); err == nil {
			return s.projectMilestoneIntent(ctx, m.ID, in)
		} else if !isNoRows(err) {
			return err
		}
		if id, err := s.q.GetBookingByIntent(ctx, tx, &in.Ref); err == nil {
			return s.projectBookingIntent(ctx, id, in)
		} else if !isNoRows(err) {
			return err
		}
		if id, err := s.q.GetClaimByIntent(ctx, tx, &in.Ref); err == nil {
			return s.projectClaimIntent(ctx, id, in)
		} else if !isNoRows(err) {
			return err
		}
		if id, err := s.q.GetJobByIntent(ctx, tx, &in.Ref); err == nil {
			return s.projectJobIntent(ctx, id, in)
		} else if !isNoRows(err) {
			return err
		}
		if id, err := s.q.GetAppointmentByIntent(ctx, tx, &in.Ref); err == nil {
			return s.projectFeeIntent(ctx, id, in)
		} else if !isNoRows(err) {
			return err
		}
		return fmt.Errorf("payment intent %s is not known yet", in.Ref)
	})
}
