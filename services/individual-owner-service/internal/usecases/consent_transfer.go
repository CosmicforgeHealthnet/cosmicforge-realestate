package usecases

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

// Co-owner consent (§13.4): action-specific consent from the other
// principals (owner / co-owners with linked accounts). Decided consents are
// immutable (database trigger); approvals are valid until they expire.

// Consent action types.
const (
	ConsentListingPublish    = "LISTING_PUBLISH"
	ConsentSale              = "SALE"
	ConsentManagerAssignment = "MANAGER_ASSIGNMENT"
	ConsentOwnershipTransfer = "OWNERSHIP_TRANSFER"
	ConsentPriceChange       = "PRICE_CHANGE"
	ConsentLease             = "LEASE"
)

// ConsentView is one consent request/decision.
type ConsentView struct {
	ID           ids.ID     `json:"id"`
	PropertyID   ids.ID     `json:"property_id"`
	PartyID      ids.ID     `json:"party_id"`
	ActionType   string     `json:"action_type"`
	TargetType   string     `json:"target_type"`
	TargetID     ids.ID     `json:"target_id"`
	Decision     string     `json:"decision"`
	DecisionNote *string    `json:"decision_note"`
	DecidedAt    *time.Time `json:"decided_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

func consentView(c db.OwnershipConsent) ConsentView {
	return ConsentView{ID: c.ID, PropertyID: c.PropertyID, PartyID: c.PartyID, ActionType: c.ActionType, TargetType: c.TargetType, TargetID: c.TargetID,
		Decision: c.Decision, DecisionNote: c.DecisionNote, DecidedAt: c.DecidedAt, ExpiresAt: c.ExpiresAt, CreatedAt: c.CreatedAt}
}

// ConsentStatus summarizes consent for one action/target.
type ConsentStatus struct {
	ActionType string        `json:"action_type"`
	TargetID   ids.ID        `json:"target_id"`
	Satisfied  bool          `json:"satisfied"`
	Threshold  string        `json:"threshold"`
	Consents   []ConsentView `json:"consents"`
}

// RequestConsentInput asks the other principals to consent.
type RequestConsentInput struct {
	ActionType string `json:"action_type" v:"required,enum=LISTING_PUBLISH|SALE|MANAGER_ASSIGNMENT|OWNERSHIP_TRANSFER|PRICE_CHANGE|LEASE"`
	TargetType string `json:"target_type" v:"required,enum=property|listing|sale_transaction|manager_assignment|agreement|price_schedule"`
	TargetID   ids.ID `json:"target_id" v:"required"`
}

// RequestConsent creates PENDING consents for every other principal.
func (s *Service) RequestConsent(ctx context.Context, subj authz.Subject, propertyID ids.ID, in RequestConsentInput) (ConsentStatus, error) {
	if err := schema.Validate(in); err != nil {
		return ConsentStatus{}, err
	}
	var out ConsentStatus
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipManage), propertyID); err != nil {
			return err
		}
		if in.TargetType == "property" && in.TargetID != propertyID {
			return apperrors.InvalidRequest("target_id must be the property for target_type property.")
		}
		tx, now := mustTx(ctx), s.now()
		principals, err := s.q.ActivePrincipalParties(ctx, tx, propertyID)
		if err != nil {
			return err
		}
		requester := s.actingAccount(ctx, subj, propertyID)
		n := 0
		for _, p := range principals {
			if p.AccountID == nil || *p.AccountID == requester {
				continue
			}
			n++
			if _, err := s.q.InsertOwnershipConsent(ctx, tx, db.InsertOwnershipConsentParams{ID: ids.New(), PropertyID: propertyID, PartyID: p.ID,
				ActionType: in.ActionType, TargetType: in.TargetType, TargetID: in.TargetID, RequestedByAccount: requester,
				ExpiresAt: now.Add(s.Policy.Domain.ConsentTTL), CreatedAt: now}); err != nil && !isNoRows(err) {
				return err
			}
		}
		if n == 0 {
			return precondition("NO_CONSENT_REQUIRED", "There are no other principals whose consent is required.")
		}
		if err := s.record(ctx, "ownership.consent.request", AggProperty, propertyID, map[string]any{"action_type": in.ActionType, "target_id": in.TargetID}, nil); err != nil {
			return err
		}
		out, err = s.consentStatus(ctx, propertyID, in.ActionType, in.TargetID, requester)
		return err
	})
	return out, err
}

func (s *Service) consentStatus(ctx context.Context, propertyID ids.ID, action string, target, requester ids.ID) (ConsentStatus, error) {
	ok, rows, err := s.consentSatisfied(ctx, propertyID, action, target, requester)
	if err != nil {
		return ConsentStatus{}, err
	}
	v := ConsentStatus{ActionType: action, TargetID: target, Satisfied: ok, Threshold: s.Policy.Domain.CoOwnerConsentThreshold, Consents: []ConsentView{}}
	for _, r := range rows {
		v.Consents = append(v.Consents, ConsentView{ID: r.ID, PropertyID: r.PropertyID, PartyID: r.PartyID, ActionType: r.ActionType, TargetType: r.TargetType,
			TargetID: r.TargetID, Decision: r.Decision, DecisionNote: r.DecisionNote, DecidedAt: r.DecidedAt, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt})
	}
	return v, nil
}

// consentSatisfied evaluates the configured threshold for action/target.
func (s *Service) consentSatisfied(ctx context.Context, propertyID ids.ID, action string, target, requester ids.ID) (bool, []db.ListConsentsForTargetRow, error) {
	q, now := s.dbq(ctx), s.now()
	principals, err := s.q.ActivePrincipalParties(ctx, q, propertyID)
	if err != nil {
		return false, nil, err
	}
	rows, err := s.q.ListConsentsForTarget(ctx, q, db.ListConsentsForTargetParams{PropertyID: propertyID, ActionType: action, TargetID: target})
	if err != nil {
		return false, nil, err
	}
	latest := map[ids.ID]db.ListConsentsForTargetRow{} // newest decision per party (rows are newest first)
	for _, r := range rows {
		if _, seen := latest[r.PartyID]; !seen {
			latest[r.PartyID] = r
		}
	}
	var others, approved int
	var approvedShare int64
	for _, p := range principals {
		share := int64(deref(p.ShareBps))
		if p.AccountID != nil && *p.AccountID == requester {
			approvedShare += share
			continue
		}
		others++
		c, ok := latest[p.ID]
		if ok && c.Decision == "REJECTED" {
			return false, rows, nil
		}
		if ok && c.Decision == "APPROVED" && c.ExpiresAt.After(now) {
			approved++
			approvedShare += share
		}
	}
	if others == 0 {
		return true, rows, nil
	}
	if s.Policy.Domain.CoOwnerConsentThreshold == "MAJORITY_SHARE" {
		return approvedShare > 5000, rows, nil
	}
	return approved == others, rows, nil
}

// requireConsent enforces the co-owner consent obligation returned by authz.
func (s *Service) requireConsent(ctx context.Context, d authz.Decision, propertyID ids.ID, action string, target, requester ids.ID) error {
	if !slices.Contains(d.Obligations, authz.ObligationCoOwnerConsent) {
		return nil
	}
	ok, rows, err := s.consentSatisfied(ctx, propertyID, action, target, requester)
	if err != nil || ok {
		return err
	}
	for _, r := range rows {
		if r.Decision == "REJECTED" {
			return conflict("CO_OWNER_CONSENT_REJECTED", "A co-owner rejected this action.")
		}
	}
	return apperrors.Conflict("CO_OWNER_CONSENT_REQUIRED", "Co-owner consent is required before this action ("+action+").").
		WithDetails(apperrors.Detail{Field: "target_id", Code: action, Message: target.String()})
}

// ListPropertyConsents lists a property's consent history.
func (s *Service) ListPropertyConsents(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]ConsentView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapOwnershipManage), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPropertyConsents(ctx, s.dbq(ctx), propertyID)
	out := make([]ConsentView, 0, len(rows))
	for _, r := range rows {
		out = append(out, consentView(r))
	}
	return out, err
}

// PendingConsentView is a consent awaiting the caller's decision.
type PendingConsentView struct {
	ConsentView
	PropertyTitle string `json:"property_title"`
}

// ListMyConsents lists consents awaiting the caller.
func (s *Service) ListMyConsents(ctx context.Context, subj authz.Subject) ([]PendingConsentView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActProfileRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPendingConsentsForAccount(ctx, s.dbq(ctx), db.ListPendingConsentsForAccountParams{AccountID: ptr(accountOf(subj)), ExpiresAt: s.now()})
	out := make([]PendingConsentView, 0, len(rows))
	for _, r := range rows {
		out = append(out, PendingConsentView{PropertyTitle: r.PropertyTitle, ConsentView: ConsentView{ID: r.ID, PropertyID: r.PropertyID, PartyID: r.PartyID,
			ActionType: r.ActionType, TargetType: r.TargetType, TargetID: r.TargetID, Decision: r.Decision, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt}})
	}
	return out, err
}

// ConsentDecisionInput decides a consent.
type ConsentDecisionInput struct {
	Decision string  `json:"decision" v:"required,enum=APPROVED|REJECTED"`
	Note     *string `json:"note,omitempty" v:"max=1000"`
}

// DecideConsent records the caller's decision (immutable afterwards).
func (s *Service) DecideConsent(ctx context.Context, subj authz.Subject, consentID ids.ID, in ConsentDecisionInput) (ConsentView, error) {
	if err := schema.Validate(in); err != nil {
		return ConsentView{}, err
	}
	var out ConsentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActProfileWrite, ids.Nil); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		c, err := s.q.GetOwnershipConsentForUpdate(ctx, tx, consentID)
		if isNoRows(err) || (err == nil && (c.PartyAccountID == nil || *c.PartyAccountID != accountOf(subj))) {
			return notFound("consent")
		}
		if err != nil {
			return err
		}
		if c.Decision != "PENDING" {
			return conflict("CONSENT_ALREADY_DECIDED", "This consent was already decided.")
		}
		if !c.ExpiresAt.After(now) {
			return conflict("CONSENT_EXPIRED", "This consent request has expired.")
		}
		d, err := s.q.DecideOwnershipConsent(ctx, tx, db.DecideOwnershipConsentParams{ID: consentID, Decision: in.Decision, DecisionNote: in.Note, DecidedAt: &now})
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, c.PropertyID, "CONSENT_"+in.Decision, c.ActionType+" consent "+lower(in.Decision), "ownership_consent", consentID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.consent.decide", "ownership_consent", consentID, map[string]any{"decision": in.Decision, "action_type": c.ActionType}, nil); err != nil {
			return err
		}
		out = consentView(d)
		return nil
	})
	return out, err
}

// TransferView is an ownership change case (§13.4 guarded transfer).
type TransferView struct {
	ID           ids.ID    `json:"id"`
	PropertyID   ids.ID    `json:"property_id"`
	CaseType     string    `json:"case_type"`
	Status       string    `json:"status"`
	ToEmail      *string   `json:"to_email"`
	RecipientSet bool      `json:"recipient_accepted"`
	Reason       *string   `json:"reason"`
	SaleID       *ids.ID   `json:"sale_id" doc:"Completed sale this transfer follows (SALE cases)"`
	DecisionNote *string   `json:"decision_note"`
	Version      int64     `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func transferView(c db.OwnershipChangeCase) TransferView {
	return TransferView{ID: c.ID, PropertyID: c.PropertyID, CaseType: c.CaseType, Status: c.Status, ToEmail: c.ToEmail, RecipientSet: c.ToAccountID != nil,
		Reason: c.Reason, SaleID: c.SaleID, DecisionNote: c.DecisionNote, Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

// TransferInput opens a guarded ownership change.
type TransferInput struct {
	CaseType string  `json:"case_type" v:"required,enum=TRANSFER|SUCCESSION|INCAPACITY"`
	ToEmail  string  `json:"to_email" v:"required,format=email" doc:"Recipient's sign-in email; the recipient must accept"`
	Reason   *string `json:"reason,omitempty" v:"max=2000"`
}

// OpenTransfer starts an ownership transfer: step-up, verified authority,
// identity gate and co-owner consent (OWNERSHIP_TRANSFER, target = property).
func (s *Service) OpenTransfer(ctx context.Context, subj authz.Subject, propertyID ids.ID, in TransferInput) (TransferView, error) {
	if err := schema.Validate(in); err != nil {
		return TransferView{}, err
	}
	var out TransferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		c, err := s.openChangeCase(ctx, subj, propertyID, in.CaseType, in.ToEmail, in.Reason, nil)
		out = transferView(c)
		return err
	})
	return out, err
}

// SaleTransferInput opens the ownership transfer that follows a completed sale.
type SaleTransferInput struct {
	ToEmail string  `json:"to_email" v:"required,format=email" doc:"Buyer's owner-account sign-in email; the buyer must accept, pass the identity gate and the case needs platform review"`
	Reason  *string `json:"reason,omitempty" v:"max=2000"`
}

// OpenSaleTransfer opens the SALE ownership-change case for a completed sale.
// Sale completion itself never changes the owner: authority moves only when
// this case is accepted by the buyer, evidenced and approved by platform
// review. The case carries every OpenTransfer guard plus the transaction
// prerequisites (sale COMPLETED, every required closing milestone met).
// Retrying with the same recipient returns the existing case; a different
// recipient while one is live is a conflict.
func (s *Service) OpenSaleTransfer(ctx context.Context, subj authz.Subject, saleID ids.ID, in SaleTransferInput) (TransferView, error) {
	if err := schema.Validate(in); err != nil {
		return TransferView{}, err
	}
	var out TransferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.saleFor(ctx, subj, saleID, authz.Do(authz.CapOwnershipTransfer))
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		if t.Status != "COMPLETED" {
			return conflict("SALE_NOT_COMPLETED", "Ownership can only be transferred after the sale is completed.")
		}
		if t.UnitID != nil {
			return conflict("UNIT_SALE_TRANSFER_UNSUPPORTED", "This sale covers a single unit; the property ownership workflow transfers whole properties only.")
		}
		unmet, err := s.q.UnmetRequiredMilestones(ctx, tx, saleID)
		if err != nil {
			return err
		}
		if len(unmet) > 0 {
			return apperrors.Unprocessable("MILESTONES_INCOMPLETE", "Required milestones are not satisfied.").
				WithDetails(apperrors.Detail{Field: "milestones", Code: "UNMET", Message: joinCodes(unmet)})
		}
		to := strings.ToLower(strings.TrimSpace(in.ToEmail))
		if ex, err := s.q.GetLiveSaleChangeCase(ctx, tx, &saleID); err == nil {
			if ex.ToEmail != nil && *ex.ToEmail == to {
				out = transferView(ex)
				return nil
			}
			return conflict("SALE_TRANSFER_EXISTS", "This sale already has an ownership transfer for a different recipient.")
		} else if !isNoRows(err) {
			return err
		}
		c, err := s.openChangeCase(ctx, subj, t.PropertyID, "SALE", to, in.Reason, &saleID)
		out = transferView(c)
		return err
	})
	return out, err
}

// openChangeCase applies the guards shared by every ownership change and
// opens the case. It must run inside a transaction.
func (s *Service) openChangeCase(ctx context.Context, subj authz.Subject, propertyID ids.ID, caseType, toEmail string, reason *string, saleID *ids.ID) (db.OwnershipChangeCase, error) {
	d, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipTransfer), propertyID)
	if err != nil {
		return db.OwnershipChangeCase{}, err
	}
	acc := accountOf(subj)
	if err := s.requireIdentity(ctx, acc, GateTransfer); err != nil {
		return db.OwnershipChangeCase{}, err
	}
	if err := s.requireConsent(ctx, d, propertyID, ConsentOwnershipTransfer, propertyID, acc); err != nil {
		return db.OwnershipChangeCase{}, err
	}
	tx, now := mustTx(ctx), s.now()
	p, err := s.q.GetPropertyForUpdate(ctx, tx, propertyID)
	if err != nil {
		return db.OwnershipChangeCase{}, err
	}
	if p.Status != "ACTIVE" {
		return db.OwnershipChangeCase{}, conflict("PROPERTY_NOT_TRANSFERABLE", "Only ACTIVE properties can be transferred.")
	}
	rec, err := s.q.GetPrimaryOwnershipRecordForUpdate(ctx, tx, propertyID)
	if err != nil {
		return db.OwnershipChangeCase{}, err
	}
	to := strings.ToLower(strings.TrimSpace(toEmail))
	if me, err := s.q.GetAccount(ctx, tx, acc); err == nil && me.Email == to {
		return db.OwnershipChangeCase{}, precondition("CANNOT_TRANSFER_TO_SELF", "The recipient must be another person.")
	}
	var c db.OwnershipChangeCase
	if saleID != nil {
		c, err = s.q.InsertSaleChangeCase(ctx, tx, db.InsertSaleChangeCaseParams{ID: ids.New(), PropertyID: propertyID, FromRecordID: rec.ID, ToEmail: &to,
			RequestedByAccount: acc, Reason: reason, SaleID: saleID, CreatedAt: now})
	} else {
		c, err = s.q.InsertChangeCase(ctx, tx, db.InsertChangeCaseParams{ID: ids.New(), PropertyID: propertyID, CaseType: caseType, FromRecordID: rec.ID,
			ToEmail: &to, RequestedByAccount: acc, Reason: reason, CreatedAt: now})
	}
	if isUnique(err, "ownership_change_cases_open_uniq") || isUnique(err, "ownership_change_cases_sale_uniq") {
		return db.OwnershipChangeCase{}, conflict("TRANSFER_ALREADY_OPEN", "An ownership change is already in progress.")
	}
	if err != nil {
		return db.OwnershipChangeCase{}, err
	}
	if err := domain.OwnershipChange.RecordCreation(ctx, tx, c.ID, &propertyID, "OPEN", now); err != nil {
		return c, err
	}
	if _, _, err := s.transition(ctx, domain.Ownership, rec.ID, lifecycle.Change{To: "TRANSFER_PENDING", Command: "open_transfer"}); err != nil {
		return c, err
	}
	if _, _, err := s.transition(ctx, domain.Property, propertyID, lifecycle.Change{To: "TRANSFER_PENDING", Command: "open_transfer"}); err != nil {
		return c, err
	}
	if err := s.afterTransferLock(ctx, propertyID); err != nil {
		return c, err
	}
	if err := s.propertyHistory(ctx, propertyID, "TRANSFER_OPENED", caseType+" opened", "ownership_change_case", c.ID); err != nil {
		return c, err
	}
	meta := map[string]any{"case_type": caseType}
	if saleID != nil {
		meta["sale_id"] = saleID.String()
	}
	if err := s.record(ctx, "ownership.transfer.open", "ownership_change_case", c.ID, meta, nil); err != nil {
		return c, err
	}
	if p, err = s.q.GetProperty(ctx, tx, propertyID); err != nil {
		return c, err
	}
	return c, s.emitProperty(ctx, "property.updated.v1", p, []string{"status"})
}

// transferLockHooks run when a property becomes TRANSFER_PENDING (e.g. pause listings).
var transferLockHooks []func(s *Service, ctx context.Context, propertyID ids.ID) error

func (s *Service) afterTransferLock(ctx context.Context, propertyID ids.ID) error {
	for _, h := range transferLockHooks {
		if err := h(s, ctx, propertyID); err != nil {
			return err
		}
	}
	return nil
}

// GetTransfer returns the property's latest ownership change case.
func (s *Service) GetTransfer(ctx context.Context, subj authz.Subject, propertyID ids.ID) (TransferView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapOwnershipManage), propertyID); err != nil {
		return TransferView{}, err
	}
	c, err := s.q.GetLatestChangeCase(ctx, s.dbq(ctx), propertyID)
	if isNoRows(err) {
		return TransferView{}, notFound("ownership_transfer")
	}
	return transferView(c), err
}

// CancelTransfer cancels an open transfer and restores the property.
func (s *Service) CancelTransfer(ctx context.Context, subj authz.Subject, propertyID ids.ID) (TransferView, error) {
	var out TransferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActTransferCancel, propertyID); err != nil {
			return err
		}
		tx := mustTx(ctx)
		c, err := s.q.GetOpenChangeCase(ctx, tx, propertyID)
		if isNoRows(err) {
			return notFound("ownership_transfer")
		}
		if err != nil {
			return err
		}
		if c.Status == "APPROVED" {
			return conflict("TRANSFER_APPROVED", "An approved transfer can no longer be cancelled.")
		}
		if _, _, err := s.transition(ctx, domain.OwnershipChange, c.ID, lifecycle.Change{To: "CANCELLED", Command: "cancel"}); err != nil {
			return err
		}
		if err := s.restoreAfterTransfer(ctx, propertyID, c.FromRecordID, "transfer_cancelled"); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "TRANSFER_CANCELLED", "Ownership transfer cancelled", "ownership_change_case", c.ID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.transfer.cancel", "ownership_change_case", c.ID, nil, nil); err != nil {
			return err
		}
		c, err = s.q.GetChangeCase(ctx, tx, c.ID)
		out = transferView(c)
		return err
	})
	return out, err
}

func (s *Service) restoreAfterTransfer(ctx context.Context, propertyID, recordID ids.ID, cmd string) error {
	if _, _, err := s.transition(ctx, domain.Ownership, recordID, lifecycle.Change{To: "VERIFIED", Command: cmd}); err != nil {
		return err
	}
	if _, _, err := s.transition(ctx, domain.Property, propertyID, lifecycle.Change{To: "ACTIVE", Command: cmd}); err != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, mustTx(ctx), propertyID)
	if err != nil {
		return err
	}
	return s.emitProperty(ctx, "property.updated.v1", p, []string{"status"})
}

// ListIncomingTransfers lists transfers addressed to the caller's verified email.
func (s *Service) ListIncomingTransfers(ctx context.Context, subj authz.Subject) ([]TransferView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActProfileRead, ids.Nil); err != nil {
		return nil, err
	}
	email, err := s.verifiedEmail(ctx, accountOf(subj))
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListTransfersForEmail(ctx, s.dbq(ctx), &email)
	out := make([]TransferView, 0, len(rows))
	for _, r := range rows {
		out = append(out, transferView(r))
	}
	return out, err
}

// AcceptTransfer: the recipient (verified email, ACTIVE, identity-verified per policy) accepts.
func (s *Service) AcceptTransfer(ctx context.Context, subj authz.Subject, caseID ids.ID) (TransferView, error) {
	var out TransferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActProfileWrite, ids.Nil); err != nil {
			return err
		}
		email, err := s.verifiedEmail(ctx, accountOf(subj))
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		c, err := s.q.GetChangeCase(ctx, tx, caseID)
		if isNoRows(err) || (err == nil && (c.ToEmail == nil || *c.ToEmail != email)) {
			return notFound("ownership_transfer")
		}
		if err != nil {
			return err
		}
		acc, err := s.q.GetAccount(ctx, tx, accountOf(subj))
		if err != nil {
			return err
		}
		if acc.Status != "ACTIVE" {
			return precondition("ACCOUNT_NOT_ACTIVE", "Activate your account before accepting a transfer.")
		}
		if err := s.requireIdentity(ctx, acc.AccountID, GateTransfer); err != nil {
			return err
		}
		if !c.CreatedAt.Add(s.Policy.Domain.TransferInviteTTL).After(s.now()) {
			return conflict("TRANSFER_INVITATION_EXPIRED", "The transfer invitation has expired.")
		}
		if _, _, err := s.transition(ctx, domain.OwnershipChange, caseID, lifecycle.Change{To: "EVIDENCE_PENDING", Command: "recipient_accept"}); err != nil {
			return err
		}
		if err := s.q.SetChangeCaseRecipient(ctx, tx, db.SetChangeCaseRecipientParams{ID: caseID, ToAccountID: &acc.AccountID, UpdatedAt: s.now()}); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, c.PropertyID, "TRANSFER_ACCEPTED", "Transfer recipient accepted", "ownership_change_case", caseID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.transfer.accept", "ownership_change_case", caseID, nil, nil); err != nil {
			return err
		}
		c, err = s.q.GetChangeCase(ctx, tx, caseID)
		out = transferView(c)
		return err
	})
	return out, err
}

// SubmitTransfer attaches transfer evidence and sends the case to review.
func (s *Service) SubmitTransfer(ctx context.Context, subj authz.Subject, propertyID ids.ID, in AddEvidenceInput, checked CheckedMedia) (TransferView, error) {
	if err := schema.Validate(in); err != nil {
		return TransferView{}, err
	}
	var out TransferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipTransfer), propertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		c, err := s.q.GetOpenChangeCase(ctx, tx, propertyID)
		if isNoRows(err) {
			return notFound("ownership_transfer")
		}
		if err != nil {
			return err
		}
		if c.Status != "EVIDENCE_PENDING" {
			return conflict("TRANSFER_NOT_READY", "The recipient must accept before the transfer is submitted.")
		}
		for _, d := range in.Documents {
			m, ok := checked[d.MediaID]
			if !ok {
				return precondition("MEDIA_NOT_CHECKED", "Media must be validated before attaching.")
			}
			if _, err := s.q.InsertAuthorityDocument(ctx, tx, db.InsertAuthorityDocumentParams{ID: ids.New(), OwnershipRecordID: c.FromRecordID, PropertyID: propertyID,
				MediaID: d.MediaID, DocumentType: d.DocumentType, MediaStatus: media.Projection(m.Status), UploadedByAccount: accountOf(subj), CreatedAt: now}); err != nil && !isNoRows(err) {
				return err
			}
		}
		if _, _, err := s.transition(ctx, domain.OwnershipChange, c.ID, lifecycle.Change{To: "UNDER_REVIEW", Command: "submit"}); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "TRANSFER_SUBMITTED", "Transfer submitted for review", "ownership_change_case", c.ID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.transfer.submit", "ownership_change_case", c.ID, map[string]any{"documents": len(in.Documents)}, nil); err != nil {
			return err
		}
		c, err = s.q.GetChangeCase(ctx, tx, c.ID)
		out = transferView(c)
		return err
	})
	return out, err
}

// AdminDecisionInput is an operator decision.
type AdminDecisionInput struct {
	Decision string  `json:"decision" v:"required,enum=APPROVE|REJECT"`
	Note     *string `json:"note,omitempty" v:"max=2000"`
}

// ownerChangedHooks run after an approved transfer changes the owner
// (e.g. terminate manager assignments of the previous owner).
var ownerChangedHooks []func(s *Service, ctx context.Context, propertyID, from, to ids.ID) error

// DecideTransfer is the admin decision on a transfer under review. Approval
// moves the property to the recipient through the case (never a direct
// owner update) and opens a fresh DRAFT claim the recipient must verify.
func (s *Service) DecideTransfer(ctx context.Context, subj authz.Subject, caseID ids.ID, in AdminDecisionInput) (TransferView, error) {
	if err := schema.Validate(in); err != nil {
		return TransferView{}, err
	}
	var out TransferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminVerify, authz.Resource{}); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		c, err := s.q.GetChangeCase(ctx, tx, caseID)
		if isNoRows(err) {
			return notFound("ownership_transfer")
		}
		if err != nil {
			return err
		}
		if c.Status != "UNDER_REVIEW" {
			return conflict("TRANSFER_NOT_UNDER_REVIEW", "Only transfers under review can be decided.")
		}
		if in.Decision == "REJECT" {
			if _, _, err := s.transition(ctx, domain.OwnershipChange, caseID, lifecycle.Change{To: "REJECTED", Command: "admin_reject", Reason: deref(in.Note)}); err != nil {
				return err
			}
			if err := s.q.SetChangeCaseOutcome(ctx, tx, db.SetChangeCaseOutcomeParams{ID: caseID, DecisionNote: in.Note, UpdatedAt: now}); err != nil {
				return err
			}
			if err := s.restoreAfterTransfer(ctx, c.PropertyID, c.FromRecordID, "transfer_rejected"); err != nil {
				return err
			}
		} else {
			if c.ToAccountID == nil {
				return conflict("TRANSFER_NO_RECIPIENT", "The transfer has no accepted recipient.")
			}
			if _, _, err := s.transition(ctx, domain.OwnershipChange, caseID, lifecycle.Change{To: "APPROVED", Command: "admin_approve"}); err != nil {
				return err
			}
			prop, err := s.q.GetPropertyForUpdate(ctx, tx, c.PropertyID)
			if err != nil {
				return err
			}
			if _, _, err := s.transition(ctx, domain.Ownership, c.FromRecordID, lifecycle.Change{To: "TRANSFERRED", Command: "transfer_completed"}); err != nil {
				return err
			}
			if err := s.q.DemoteOwnershipRecord(ctx, tx, db.DemoteOwnershipRecordParams{ID: c.FromRecordID, UpdatedAt: now}); err != nil {
				return err
			}
			if err := s.q.EndPrincipalParties(ctx, tx, db.EndPrincipalPartiesParams{OwnershipRecordID: c.FromRecordID, RevokedAt: &now}); err != nil {
				return err
			}
			nrec, err := s.q.InsertOwnershipRecord(ctx, tx, db.InsertOwnershipRecordParams{ID: ids.New(), PropertyID: c.PropertyID, ClaimantAccountID: *c.ToAccountID,
				ClaimType: "SOLE", Status: "DRAFT", IsPrimary: true, CreatedAt: now})
			if err != nil {
				return err
			}
			if err := domain.Ownership.RecordCreation(ctx, tx, nrec.ID, &c.PropertyID, "DRAFT", now); err != nil {
				return err
			}
			if _, err := s.q.InsertOwnershipParty(ctx, tx, db.InsertOwnershipPartyParams{ID: ids.New(), OwnershipRecordID: nrec.ID, PropertyID: c.PropertyID,
				PartyRole: "OWNER", AccountID: c.ToAccountID, DisplayName: "Primary owner", ShareBps: ptr(int32(10000)), Capabilities: []string{},
				Status: "ACTIVE", ValidFrom: &now, CreatedAt: now}); err != nil {
				return err
			}
			if _, err := s.q.SetPropertyOwner(ctx, tx, db.SetPropertyOwnerParams{ID: c.PropertyID, OwnerAccountID: *c.ToAccountID, UpdatedAt: now}); err != nil {
				return err
			}
			if _, _, err := s.transition(ctx, domain.Property, c.PropertyID, lifecycle.Change{To: "ACTIVE", Command: "transfer_completed"}); err != nil {
				return err
			}
			if _, _, err := s.transition(ctx, domain.OwnershipChange, caseID, lifecycle.Change{To: "COMPLETED", Command: "apply_transfer"}); err != nil {
				return err
			}
			if err := s.q.SetChangeCaseOutcome(ctx, tx, db.SetChangeCaseOutcomeParams{ID: caseID, ToRecordID: &nrec.ID, DecisionNote: in.Note, UpdatedAt: now}); err != nil {
				return err
			}
			for _, h := range ownerChangedHooks {
				if err := h(s, ctx, c.PropertyID, prop.OwnerAccountID, *c.ToAccountID); err != nil {
					return err
				}
			}
			p, err := s.q.GetProperty(ctx, tx, c.PropertyID)
			if err != nil {
				return err
			}
			if err := s.emitProperty(ctx, "property.updated.v1", p, []string{"status", "owner"}); err != nil {
				return err
			}
			if err := s.emitVerificationChanged(ctx, c.PropertyID); err != nil {
				return err
			}
		}
		if err := s.propertyHistory(ctx, c.PropertyID, "TRANSFER_"+in.Decision+"D", "Ownership transfer "+lower(in.Decision)+"d by platform review", "ownership_change_case", caseID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.transfer.decide", "ownership_change_case", caseID, map[string]any{"decision": in.Decision}, nil); err != nil {
			return err
		}
		c, err = s.q.GetChangeCase(ctx, tx, caseID)
		out = transferView(c)
		return err
	})
	return out, err
}

// ConflictView is an ownership conflict awaiting admin review.
type ConflictView struct {
	RecordID       ids.ID    `json:"record_id"`
	PropertyID     ids.ID    `json:"property_id"`
	ConflictWithID *ids.ID   `json:"conflict_with_record_id"`
	ClaimType      string    `json:"claim_type"`
	CreatedAt      time.Time `json:"created_at"`
}

// ListConflicts pages ownership conflicts (admin).
func (s *Service) ListConflicts(ctx context.Context, subj authz.Subject, cursor string, limit int) (pagination.Response[ConflictView], error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminVerify, authz.Resource{}); err != nil {
		return pagination.Response[ConflictView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[ConflictView]{}, err
	}
	rows, err := s.q.ListOwnershipConflicts(ctx, s.dbq(ctx), db.ListOwnershipConflictsParams{CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[ConflictView]{}, err
	}
	out := make([]ConflictView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ConflictView{RecordID: r.ID, PropertyID: r.PropertyID, ConflictWithID: r.ConflictWithRecordID, ClaimType: r.ClaimType, CreatedAt: r.CreatedAt})
	}
	return pagination.Build(out, limit, func(c ConflictView) any { return timeCursor{At: c.CreatedAt, ID: c.RecordID} })
}

// ResolveConflictInput resolves an ownership conflict.
type ResolveConflictInput struct {
	Decision   string  `json:"decision" v:"required,enum=RELEASE|REJECT" doc:"RELEASE lets the claim proceed to verification; REJECT rejects it"`
	ReasonCode string  `json:"reason_code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
	Note       *string `json:"note,omitempty" v:"max=2000"`
}

// ResolveConflict is the admin decision on a CONFLICT claim.
func (s *Service) ResolveConflict(ctx context.Context, subj authz.Subject, recordID ids.ID, in ResolveConflictInput) (RecordView, error) {
	if err := schema.Validate(in); err != nil {
		return RecordView{}, err
	}
	var out RecordView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminVerify, authz.Resource{}); err != nil {
			return err
		}
		to := map[string]string{"RELEASE": "EVIDENCE_PENDING", "REJECT": "REJECTED"}[in.Decision]
		_, next, err := s.transition(ctx, domain.Ownership, recordID, lifecycle.Change{To: to, Command: "admin_" + lower(in.Decision), Reason: in.ReasonCode})
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		if to == "REJECTED" {
			if err := s.q.SetOwnershipDecision(ctx, tx, db.SetOwnershipDecisionParams{ID: recordID, ReasonCode: &in.ReasonCode, UpdatedAt: s.now()}); err != nil {
				return err
			}
		}
		if err := s.propertyHistory(ctx, *next.PropertyID, "OWNERSHIP_CONFLICT_"+in.Decision+"D", "Ownership conflict resolved: "+in.ReasonCode, AggOwnership, recordID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.conflict.resolve", AggOwnership, recordID, map[string]any{"decision": in.Decision, "reason_code": in.ReasonCode},
			map[string]any{"note_present": in.Note != nil}); err != nil {
			return err
		}
		r, err := s.q.GetOwnershipRecord(ctx, tx, recordID)
		if err != nil {
			return err
		}
		docs, err := s.q.ListAuthorityDocuments(ctx, tx, recordID)
		out = recordView(r, docs)
		return err
	})
	return out, err
}

// RestrictInput is an admin restriction of a property.
type RestrictInput struct {
	ReasonCode string `json:"reason_code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
}

// AdminRestrictProperty restricts (or lifts the restriction of) a property.
func (s *Service) AdminRestrictProperty(ctx context.Context, subj authz.Subject, propertyID ids.ID, restrict bool, in RestrictInput) (PublicSummary, error) {
	if err := schema.Validate(in); err != nil {
		return PublicSummary{}, err
	}
	var out PublicSummary
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminModerate, authz.Resource{PropertyID: propertyID}); err != nil {
			return err
		}
		to, cmd, reason := "ACTIVE", "admin_lift_restriction", (*string)(nil)
		if restrict {
			to, cmd, reason = "RESTRICTED", "admin_restrict", &in.ReasonCode
		}
		if _, _, err := s.transition(ctx, domain.Property, propertyID, lifecycle.Change{To: to, Command: cmd, Reason: in.ReasonCode}); err != nil {
			return err
		}
		tx := mustTx(ctx)
		p, err := s.q.SetPropertyRestriction(ctx, tx, db.SetPropertyRestrictionParams{ID: propertyID, RestrictionReason: reason, UpdatedAt: s.now()})
		if err != nil {
			return err
		}
		if restrict {
			if err := s.authorityLost(ctx, propertyID, "RESTRICTED"); err != nil {
				return err
			}
		}
		if err := s.propertyHistory(ctx, propertyID, "PROPERTY_"+to, "Platform review: "+in.ReasonCode, AggProperty, propertyID); err != nil {
			return err
		}
		if err := s.record(ctx, "property.admin."+lower(to), AggProperty, propertyID, map[string]any{"status": to, "reason_code": in.ReasonCode}, nil); err != nil {
			return err
		}
		if err := s.emitProperty(ctx, "property.updated.v1", p, []string{"status"}); err != nil {
			return err
		}
		out, err = s.PublicSummaryOf(ctx, propertyID)
		return err
	})
	return out, err
}
