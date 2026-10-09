package usecases

import (
	"context"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

// Fraud, privacy, safety and disputes (§13.26) and privacy rights (§13.27).
// Owner dispute cases target an owner-domain record the owner can act on
// and are worked by operators through the admin contract (admin-service is
// told through dispute.case.updated.v1); evidence is append-only. Privacy
// consents are append-only decisions. Data-subject requests require a
// step-up-verified requester; access/export requests are fulfilled with a
// generated PDF export, other rights are decided by an operator with the
// categories retained under legal/business holds.

const (
	AggDispute = "domain_dispute"
	AggDSR     = "data_subject_request"
)

// Owner notification templates.
const (
	TemplateDisputeUpdate = "owner.dispute_update" // status
	TemplatePrivacyUpdate = "owner.privacy_update" // request_type, status
)

// disputeTargets maps a target type to the SQL that resolves its property
// (property-scoped targets) — account-scoped targets are handled separately.
var disputeTargets = map[string]string{
	"property":                 `SELECT id FROM properties WHERE id = $1`,
	"ownership_record":         `SELECT property_id FROM ownership_records WHERE id = $1`,
	"listing":                  `SELECT property_id FROM listings WHERE id = $1`,
	"manager_assignment":       `SELECT property_id FROM manager_assignments WHERE id = $1`,
	"tenancy":                  `SELECT property_id FROM tenancies WHERE id = $1`,
	"rent_obligation":          `SELECT t.property_id FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id WHERE o.id = $1`,
	"deposit_obligation":       `SELECT property_id FROM deposit_obligations WHERE id = $1`,
	"agreement":                `SELECT property_id FROM agreements WHERE id = $1`,
	"sale_transaction":         `SELECT property_id FROM sale_transactions WHERE id = $1`,
	"shortlet_booking":         `SELECT property_id FROM shortlet_bookings WHERE id = $1`,
	"damage_claim":             `SELECT property_id FROM damage_claims WHERE id = $1`,
	"maintenance_request":      `SELECT property_id FROM maintenance_requests WHERE id = $1`,
	"professional_appointment": `SELECT coalesce(a.property_id, s.property_id) FROM professional_appointments a LEFT JOIN sale_transactions s ON s.id = a.sale_id WHERE a.id = $1`,
}

// DisputeInput raises a dispute.
type DisputeInput struct {
	TargetType       string   `json:"target_type" v:"required,enum=property|ownership_record|listing|manager_assignment|tenancy|rent_obligation|deposit_obligation|agreement|sale_transaction|shortlet_booking|damage_claim|maintenance_request|professional_appointment|payout_account|account"`
	TargetID         ids.ID   `json:"target_id" v:"required"`
	Category         string   `json:"category" v:"required,enum=UNAUTHORIZED_MANAGER|FORGED_DOCUMENT|FALSE_OWNERSHIP|DUPLICATE_PROPERTY|PAYMENT_DIVERSION|ABUSE|PRIVACY|SERVICE_QUALITY|BILLING|SAFETY|OTHER"`
	Summary          string   `json:"summary" v:"required,max=4000"`
	DesiredOutcome   *string  `json:"desired_outcome,omitempty" v:"max=1000"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=20"`
}

// MediaRefs lists referenced media.
func (in DisputeInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// DisputeEvidenceView is one evidence record.
type DisputeEvidenceView struct {
	MediaID     *ids.ID   `json:"media_id"`
	Note        *string   `json:"note"`
	SubmittedBy string    `json:"submitted_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// DisputeView is a dispute case.
type DisputeView struct {
	ID                 ids.ID                `json:"id"`
	PropertyID         *ids.ID               `json:"property_id"`
	TargetType         string                `json:"target_type"`
	TargetID           ids.ID                `json:"target_id"`
	Category           string                `json:"category"`
	Status             string                `json:"status"`
	Summary            string                `json:"summary"`
	DesiredOutcome     *string               `json:"desired_outcome"`
	Urgent             bool                  `json:"urgent" doc:"Fraud/safety categories are prioritized for review"`
	AdminCaseRef       *string               `json:"admin_case_ref"`
	InformationRequest *string               `json:"information_request"`
	ResolutionCode     *string               `json:"resolution_code"`
	ResolutionNote     *string               `json:"resolution_note"`
	ResolvedAt         *time.Time            `json:"resolved_at"`
	Evidence           []DisputeEvidenceView `json:"evidence"`
	Version            int64                 `json:"version"`
	CreatedAt          time.Time             `json:"created_at"`
}

func disputeBase(d db.DomainDispute) DisputeView {
	return DisputeView{ID: d.ID, PropertyID: d.PropertyID, TargetType: d.TargetType, TargetID: d.TargetID, Category: d.Category, Status: d.Status,
		Summary: d.Summary, DesiredOutcome: d.DesiredOutcome, Urgent: d.Urgent, AdminCaseRef: d.AdminCaseRef, InformationRequest: d.InformationRequest,
		ResolutionCode: d.ResolutionCode, ResolutionNote: d.ResolutionNote, ResolvedAt: d.ResolvedAt, Version: d.Version, CreatedAt: d.CreatedAt,
		Evidence: []DisputeEvidenceView{}}
}

func (s *Service) disputeView(ctx context.Context, d db.DomainDispute) (DisputeView, error) {
	v := disputeBase(d)
	evs, err := s.q.ListDisputeEvidence(ctx, s.dbq(ctx), d.ID)
	for _, e := range evs {
		v.Evidence = append(v.Evidence, DisputeEvidenceView{MediaID: e.MediaID, Note: e.Note, SubmittedBy: e.SubmittedBy, CreatedAt: e.CreatedAt})
	}
	return v, err
}

// DisputeEvent is dispute.case.updated.v1 (engineering addition: admin
// operations discover and track owner dispute cases).
type DisputeEvent struct {
	SchemaVersion  int       `json:"schema_version"`
	DisputeID      ids.ID    `json:"dispute_id"`
	AccountID      ids.ID    `json:"account_id"`
	PropertyID     *ids.ID   `json:"property_id"`
	TargetType     string    `json:"target_type"`
	TargetID       ids.ID    `json:"target_id"`
	Category       string    `json:"category"`
	Status         string    `json:"status"`
	Urgent         bool      `json:"urgent"`
	DisputeVersion int64     `json:"dispute_version"`
	OccurredAt     time.Time `json:"occurred_at"`
}

func (s *Service) emitDispute(ctx context.Context, d db.DomainDispute) error {
	return s.publish(ctx, "dispute.case.updated.v1", AggDispute, d.ID, DisputeEvent{SchemaVersion: 1, DisputeID: d.ID, AccountID: d.AccountID,
		PropertyID: d.PropertyID, TargetType: d.TargetType, TargetID: d.TargetID, Category: d.Category, Status: d.Status, Urgent: d.Urgent,
		DisputeVersion: d.Version, OccurredAt: s.now()})
}

var urgentCategories = []string{"UNAUTHORIZED_MANAGER", "FORGED_DOCUMENT", "FALSE_OWNERSHIP", "PAYMENT_DIVERSION", "SAFETY"}

// disputeProperty authorizes the caller on the dispute target and returns
// its property (nil for account-scoped targets).
func (s *Service) disputeProperty(ctx context.Context, subj authz.Subject, targetType string, targetID ids.ID) (*ids.ID, error) {
	acc := accountOf(subj)
	switch targetType {
	case "account":
		if _, err := s.authorize(ctx, subj, authz.ActAccountDispute, ids.Nil); err != nil {
			return nil, err
		}
		if targetID != acc {
			return nil, notFound("dispute_target")
		}
		return nil, nil
	case "payout_account":
		if _, err := s.authorize(ctx, subj, authz.ActAccountDispute, ids.Nil); err != nil {
			return nil, err
		}
		var owner ids.ID
		err := s.dbq(ctx).QueryRow(ctx, `SELECT account_id FROM payout_accounts WHERE id = $1`, targetID).Scan(&owner)
		if isNoRows(err) || (err == nil && owner != acc) {
			return nil, notFound("dispute_target")
		}
		return nil, err
	}
	var pid ids.ID
	err := s.dbq(ctx).QueryRow(ctx, disputeTargets[targetType], targetID).Scan(&pid)
	if isNoRows(err) {
		return nil, notFound("dispute_target")
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.authorize(ctx, subj, authz.Do(authz.CapDisputesManage), pid); err != nil {
		if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
			return nil, notFound("dispute_target")
		}
		return nil, err
	}
	return &pid, nil
}

// RaiseDispute opens a dispute case (one open case per target and category).
func (s *Service) RaiseDispute(ctx context.Context, subj authz.Subject, in DisputeInput, ev Evidence) (DisputeView, error) {
	if err := schema.Validate(in); err != nil {
		return DisputeView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return DisputeView{}, err
	}
	var out DisputeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		pid, err := s.disputeProperty(ctx, subj, in.TargetType, in.TargetID)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		d, err := s.q.InsertDispute(ctx, tx, db.InsertDisputeParams{ID: ids.New(), AccountID: accountOf(subj), PropertyID: pid, TargetType: in.TargetType,
			TargetID: in.TargetID, Category: in.Category, Summary: in.Summary, DesiredOutcome: in.DesiredOutcome,
			Urgent: slices.Contains(urgentCategories, in.Category), CreatedAt: now})
		if isUnique(err, "domain_disputes_open_uniq") {
			return conflict("DISPUTE_ALREADY_OPEN", "A dispute about this record and category is already open.")
		}
		if err != nil {
			return err
		}
		for _, m := range in.EvidenceMediaIDs {
			if err := s.q.InsertDisputeEvidence(ctx, tx, db.InsertDisputeEvidenceParams{ID: ids.New(), DisputeID: d.ID, MediaID: &m, SubmittedBy: "OWNER", CreatedAt: now}); err != nil {
				return err
			}
		}
		if pid != nil {
			if err := s.propertyHistory(ctx, *pid, "DISPUTE_RAISED", in.Category, AggDispute, d.ID); err != nil {
				return err
			}
		}
		if err := s.emitDispute(ctx, d); err != nil {
			return err
		}
		if err := s.record(ctx, "dispute.raise", AggDispute, d.ID, map[string]any{"target_type": in.TargetType, "target_id": in.TargetID, "category": in.Category}, nil); err != nil {
			return err
		}
		out, err = s.disputeView(ctx, d)
		return err
	})
	return out, err
}

func (s *Service) ownDispute(ctx context.Context, subj authz.Subject, id ids.ID, lock bool) (db.DomainDispute, error) {
	if _, err := s.authorize(ctx, subj, authz.ActAccountDispute, ids.Nil); err != nil {
		return db.DomainDispute{}, err
	}
	get := s.q.GetDispute
	if lock {
		get = s.q.GetDisputeForUpdate
	}
	d, err := get(ctx, s.dbq(ctx), id)
	if isNoRows(err) || (err == nil && d.AccountID != accountOf(subj)) {
		return d, notFound(AggDispute)
	}
	return d, err
}

// GetDispute returns one of the caller's disputes.
func (s *Service) GetDispute(ctx context.Context, subj authz.Subject, id ids.ID) (DisputeView, error) {
	d, err := s.ownDispute(ctx, subj, id, false)
	if err != nil {
		return DisputeView{}, err
	}
	return s.disputeView(ctx, d)
}

// ListDisputes lists the caller's disputes.
func (s *Service) ListDisputes(ctx context.Context, subj authz.Subject, status string) ([]DisputeView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActAccountDispute, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListDisputesForAccount(ctx, s.dbq(ctx), db.ListDisputesForAccountParams{AccountID: accountOf(subj), Status: strp(status)})
	out := make([]DisputeView, 0, len(rows))
	for _, r := range rows {
		out = append(out, disputeBase(r))
	}
	return out, err
}

// DisputeUpdateInput adds information/evidence or withdraws the dispute.
type DisputeUpdateInput struct {
	Action           string   `json:"action" v:"required,enum=ADD_INFORMATION|WITHDRAW"`
	Note             *string  `json:"note,omitempty" v:"max=2000"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=20"`
}

// MediaRefs lists referenced media.
func (in DisputeUpdateInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// UpdateDispute appends information (answering an operator's request moves
// the case back to UNDER_REVIEW) or withdraws it.
func (s *Service) UpdateDispute(ctx context.Context, subj authz.Subject, id ids.ID, in DisputeUpdateInput, ev Evidence) (DisputeView, error) {
	if err := schema.Validate(in); err != nil {
		return DisputeView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return DisputeView{}, err
	}
	var out DisputeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		d, err := s.ownDispute(ctx, subj, id, true)
		if err != nil {
			return err
		}
		if !slices.Contains([]string{"OPEN", "UNDER_REVIEW", "AWAITING_INFORMATION"}, d.Status) {
			return conflict("DISPUTE_CLOSED", "The dispute is "+lower(d.Status)+".")
		}
		tx, now := mustTx(ctx), s.now()
		status := d.Status
		if in.Action == "WITHDRAW" {
			status = "WITHDRAWN"
		} else {
			if in.Note == nil && len(in.EvidenceMediaIDs) == 0 {
				return apperrors.InvalidRequest("Add a note or evidence.")
			}
			if in.Note != nil {
				if err := s.q.InsertDisputeEvidence(ctx, tx, db.InsertDisputeEvidenceParams{ID: ids.New(), DisputeID: id, Note: in.Note, SubmittedBy: "OWNER", CreatedAt: now}); err != nil {
					return err
				}
			}
			for _, m := range in.EvidenceMediaIDs {
				if err := s.q.InsertDisputeEvidence(ctx, tx, db.InsertDisputeEvidenceParams{ID: ids.New(), DisputeID: id, MediaID: &m, SubmittedBy: "OWNER", CreatedAt: now}); err != nil {
					return err
				}
			}
			if d.Status == "AWAITING_INFORMATION" {
				status = "UNDER_REVIEW"
			}
		}
		var resolved *time.Time
		if status == "WITHDRAWN" {
			resolved = &now
		}
		n, err := s.q.UpdateDisputeStatus(ctx, tx, db.UpdateDisputeStatusParams{ID: id, Status: status, InformationRequest: d.InformationRequest,
			ResolvedAt: resolved, Now: now})
		if err != nil {
			return err
		}
		if err := s.emitDispute(ctx, n); err != nil {
			return err
		}
		if err := s.record(ctx, "dispute."+lower(in.Action), AggDispute, id, map[string]any{"evidence": len(in.EvidenceMediaIDs), "status": status}, nil); err != nil {
			return err
		}
		out, err = s.disputeView(ctx, n)
		return err
	})
	return out, err
}

// AdminDisputeAction is an operator's step on a dispute.
type AdminDisputeAction struct {
	Status             string  `json:"status" v:"required,enum=UNDER_REVIEW|AWAITING_INFORMATION|RESOLVED|REJECTED"`
	AdminCaseRef       *string `json:"admin_case_ref,omitempty" v:"max=128"`
	InformationRequest *string `json:"information_request,omitempty" v:"max=2000"`
	ResolutionCode     *string `json:"resolution_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
	ResolutionNote     *string `json:"resolution_note,omitempty" v:"max=2000"`
}

// AdminListDisputes pages disputes for operators (urgent first).
func (s *Service) AdminListDisputes(ctx context.Context, subj authz.Subject, status, cursor string, limit int) (pagination.Response[DisputeView], error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminDispute, authz.Resource{}); err != nil {
		return pagination.Response[DisputeView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[DisputeView]{}, err
	}
	rows, err := s.q.ListDisputesForAdmin(ctx, s.dbq(ctx), db.ListDisputesForAdminParams{Status: strp(status), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[DisputeView]{}, err
	}
	out := make([]DisputeView, 0, len(rows))
	for _, r := range rows {
		out = append(out, disputeBase(r))
	}
	return pagination.Build(out, limit, func(v DisputeView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

// AdminGetDispute returns a dispute with evidence (privileged read, audited).
func (s *Service) AdminGetDispute(ctx context.Context, subj authz.Subject, id ids.ID) (DisputeView, error) {
	var out DisputeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminDispute, authz.Resource{}); err != nil {
			return err
		}
		d, err := s.q.GetDispute(ctx, mustTx(ctx), id)
		if isNoRows(err) {
			return notFound(AggDispute)
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "dispute.privileged_read", AggDispute, id, nil, nil); err != nil {
			return err
		}
		out, err = s.disputeView(ctx, d)
		return err
	})
	return out, err
}

// AdminUpdateDispute moves a dispute through review (audited; the owner is notified).
func (s *Service) AdminUpdateDispute(ctx context.Context, subj authz.Subject, id ids.ID, in AdminDisputeAction) (DisputeView, error) {
	if err := schema.Validate(in); err != nil {
		return DisputeView{}, err
	}
	var out DisputeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminDispute, authz.Resource{}); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		d, err := s.q.GetDisputeForUpdate(ctx, tx, id)
		if isNoRows(err) {
			return notFound(AggDispute)
		}
		if err != nil {
			return err
		}
		if !slices.Contains([]string{"OPEN", "UNDER_REVIEW", "AWAITING_INFORMATION"}, d.Status) {
			return conflict("DISPUTE_CLOSED", "The dispute is "+lower(d.Status)+".")
		}
		final := in.Status == "RESOLVED" || in.Status == "REJECTED"
		switch {
		case in.Status == "AWAITING_INFORMATION" && in.InformationRequest == nil:
			return apperrors.InvalidRequest("information_request is required.")
		case final && (in.ResolutionCode == nil || in.ResolutionNote == nil):
			return apperrors.InvalidRequest("resolution_code and resolution_note are required.")
		}
		var resolved *time.Time
		if final {
			resolved = &now
		}
		n, err := s.q.UpdateDisputeStatus(ctx, tx, db.UpdateDisputeStatusParams{ID: id, Status: in.Status, AdminCaseRef: in.AdminCaseRef,
			InformationRequest: in.InformationRequest, ResolutionCode: in.ResolutionCode, ResolutionNote: in.ResolutionNote, ResolvedAt: resolved, Now: now})
		if err != nil {
			return err
		}
		if err := s.emitDispute(ctx, n); err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, d.AccountID, TemplateDisputeUpdate, map[string]string{"status": lower(in.Status)}); err != nil {
			return err
		}
		if err := s.record(ctx, "dispute.admin_update", AggDispute, id, map[string]any{"status": in.Status, "resolution_code": in.ResolutionCode}, nil); err != nil {
			return err
		}
		out, err = s.disputeView(ctx, n)
		return err
	})
	return out, err
}

// ---- consents ----

// PrivacyConsentInput records a privacy consent decision.
type PrivacyConsentInput struct {
	Purpose       string     `json:"purpose" v:"required,enum=MARKETING_EMAIL|MARKETING_SMS|MARKETING_PUSH|PRODUCT_ANALYTICS|AI_INSIGHTS|PARTNER_FINANCING|PARTNER_INSURANCE|PUBLIC_OWNER_PROFILE"`
	Decision      string     `json:"decision" v:"required,enum=GRANTED|WITHDRAWN"`
	PolicyVersion string     `json:"policy_version" v:"required,max=40" doc:"Version of the privacy notice shown"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// PrivacyConsentView is the current decision for a purpose.
type PrivacyConsentView struct {
	Purpose       string     `json:"purpose"`
	Decision      string     `json:"decision" doc:"EXPIRED when a granted consent passed its expiry"`
	PolicyVersion string     `json:"policy_version"`
	ExpiresAt     *time.Time `json:"expires_at"`
	DecidedAt     time.Time  `json:"decided_at"`
}

func (s *Service) consentView(c db.Consent) PrivacyConsentView {
	v := PrivacyConsentView{Purpose: c.Purpose, Decision: c.Decision, PolicyVersion: c.PolicyVersion, ExpiresAt: c.ExpiresAt, DecidedAt: c.CreatedAt}
	if c.Decision == "GRANTED" && c.ExpiresAt != nil && !c.ExpiresAt.After(s.now()) {
		v.Decision = "EXPIRED"
	}
	return v
}

// ListPrivacyConsents returns the current decision per purpose.
func (s *Service) ListPrivacyConsents(ctx context.Context, subj authz.Subject) ([]PrivacyConsentView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPrivacyRequest, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.CurrentConsents(ctx, s.dbq(ctx), accountOf(subj))
	out := make([]PrivacyConsentView, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.consentView(r))
	}
	return out, err
}

// RecordPrivacyConsent appends a consent decision.
func (s *Service) RecordPrivacyConsent(ctx context.Context, subj authz.Subject, in PrivacyConsentInput) (PrivacyConsentView, error) {
	if err := schema.Validate(in); err != nil {
		return PrivacyConsentView{}, err
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.now()) {
		return PrivacyConsentView{}, apperrors.InvalidRequest("expires_at must be in the future.")
	}
	var out PrivacyConsentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActPrivacyRequest, ids.Nil); err != nil {
			return err
		}
		c, err := s.q.InsertConsent(ctx, mustTx(ctx), db.InsertConsentParams{ID: ids.New(), AccountID: accountOf(subj), Purpose: in.Purpose, Decision: in.Decision,
			PolicyVersion: in.PolicyVersion, ExpiresAt: in.ExpiresAt, Source: "OWNER", ActorID: idp(accountOf(subj)), CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = s.consentView(c)
		return s.record(ctx, "privacy.consent", "owner_account", accountOf(subj), map[string]any{"purpose": in.Purpose, "decision": in.Decision,
			"policy_version": in.PolicyVersion}, nil)
	})
	return out, err
}

// ---- data subject requests ----

// PrivacyRequestInput submits a privacy right request.
type PrivacyRequestInput struct {
	RequestType string  `json:"request_type" v:"required,enum=ACCESS|EXPORT|CORRECTION|DELETION|OBJECTION|RESTRICTION"`
	Details     *string `json:"details,omitempty" v:"max=4000"`
}

// PrivacyRequestView is a data-subject request.
type PrivacyRequestView struct {
	ID                 ids.ID     `json:"id"`
	RequestType        string     `json:"request_type"`
	Status             string     `json:"status"`
	Details            *string    `json:"details"`
	VerifiedAt         *time.Time `json:"verified_at"`
	DueAt              time.Time  `json:"due_at"`
	ReportID           *ids.ID    `json:"report_id" doc:"Export document (ACCESS/EXPORT)"`
	DecisionReason     *string    `json:"decision_reason"`
	RetainedCategories []string   `json:"retained_categories" doc:"Records kept under legal/business holds"`
	DecidedAt          *time.Time `json:"decided_at"`
	Version            int64      `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
}

func dsrView(d db.DataSubjectRequest) PrivacyRequestView {
	return PrivacyRequestView{ID: d.ID, RequestType: d.RequestType, Status: d.Status, Details: d.Details, VerifiedAt: d.VerifiedAt, DueAt: d.DueAt,
		ReportID: d.ReportID, DecisionReason: d.DecisionReason, RetainedCategories: nonNil(d.RetainedCategories), DecidedAt: d.DecidedAt, Version: d.Version,
		CreatedAt: d.CreatedAt}
}

const dsrResponseWindow = 30 * 24 * time.Hour

func (s *Service) stepUpFresh(subj authz.Subject) bool {
	a := subj.Account
	return a != nil && a.StepUpAt != nil && s.now().Sub(*a.StepUpAt) <= s.Authz.StepUpWindow()
}

// SubmitPrivacyRequest records a request; a recently stepped-up session
// verifies the requester immediately.
func (s *Service) SubmitPrivacyRequest(ctx context.Context, subj authz.Subject, in PrivacyRequestInput) (PrivacyRequestView, error) {
	if err := schema.Validate(in); err != nil {
		return PrivacyRequestView{}, err
	}
	if slices.Contains([]string{"CORRECTION", "OBJECTION", "RESTRICTION"}, in.RequestType) && in.Details == nil {
		return PrivacyRequestView{}, apperrors.InvalidRequest("Describe the request in details.")
	}
	var out PrivacyRequestView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActPrivacyRequest, ids.Nil); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		d, err := s.q.InsertDataSubjectRequest(ctx, tx, db.InsertDataSubjectRequestParams{ID: ids.New(), AccountID: accountOf(subj), RequestType: in.RequestType,
			Details: in.Details, DueAt: now.Add(dsrResponseWindow), CreatedAt: now})
		if isUnique(err, "data_subject_requests_open_uniq") {
			return conflict("REQUEST_ALREADY_OPEN", "A request of this type is already in progress.")
		}
		if err != nil {
			return err
		}
		if err := domain.DataSubjectRequest.RecordCreation(ctx, tx, d.ID, nil, "SUBMITTED", now); err != nil {
			return err
		}
		if s.stepUpFresh(subj) {
			if err := s.verifyDSR(ctx, d.ID); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "privacy.request.submit", AggDSR, d.ID, map[string]any{"request_type": in.RequestType}, nil); err != nil {
			return err
		}
		n, err := s.q.GetDataSubjectRequest(ctx, tx, d.ID)
		out = dsrView(n)
		return err
	})
	return out, err
}

func (s *Service) verifyDSR(ctx context.Context, id ids.ID) error {
	if err := s.q.SetDataSubjectVerified(ctx, mustTx(ctx), db.SetDataSubjectVerifiedParams{ID: id, VerifiedAt: ptr(s.now())}); err != nil {
		return err
	}
	_, _, err := s.transition(ctx, domain.DataSubjectRequest, id, lifecycle.Change{To: "VERIFIED_REQUESTER", Command: "verify_requester", Reason: "STEP_UP"})
	return err
}

func (s *Service) ownDSR(ctx context.Context, subj authz.Subject, id ids.ID) (db.DataSubjectRequest, error) {
	d, err := s.q.GetDataSubjectRequest(ctx, s.dbq(ctx), id)
	if isNoRows(err) || (err == nil && d.AccountID != accountOf(subj)) {
		return d, notFound(AggDSR)
	}
	return d, err
}

// VerifyPrivacyRequest confirms the requester with a fresh step-up.
func (s *Service) VerifyPrivacyRequest(ctx context.Context, subj authz.Subject, id ids.ID) (PrivacyRequestView, error) {
	var out PrivacyRequestView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActPrivacyVerify, ids.Nil); err != nil {
			return err
		}
		d, err := s.ownDSR(ctx, subj, id)
		if err != nil {
			return err
		}
		if d.Status != "SUBMITTED" {
			return apperrors.InvalidStateTransition(d.Status, "VERIFIED_REQUESTER")
		}
		if err := s.verifyDSR(ctx, id); err != nil {
			return err
		}
		if err := s.record(ctx, "privacy.request.verify", AggDSR, id, nil, nil); err != nil {
			return err
		}
		n, err := s.q.GetDataSubjectRequest(ctx, mustTx(ctx), id)
		out = dsrView(n)
		return err
	})
	return out, err
}

// ListPrivacyRequests lists the caller's requests.
func (s *Service) ListPrivacyRequests(ctx context.Context, subj authz.Subject) ([]PrivacyRequestView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPrivacyRequest, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListDataSubjectRequests(ctx, s.dbq(ctx), accountOf(subj))
	out := make([]PrivacyRequestView, 0, len(rows))
	for _, r := range rows {
		out = append(out, dsrView(r))
	}
	return out, err
}

// GetPrivacyRequest returns one of the caller's requests.
func (s *Service) GetPrivacyRequest(ctx context.Context, subj authz.Subject, id ids.ID) (PrivacyRequestView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPrivacyRequest, ids.Nil); err != nil {
		return PrivacyRequestView{}, err
	}
	d, err := s.ownDSR(ctx, subj, id)
	return dsrView(d), err
}

// PrivacyDecision is an operator's decision on a correction/deletion/
// objection/restriction request.
type PrivacyDecision struct {
	Outcome            string   `json:"outcome" v:"required,enum=FULFILLED|PARTIALLY_FULFILLED|DENIED_WITH_REASON"`
	Reason             string   `json:"reason" v:"required,max=2000"`
	RetainedCategories []string `json:"retained_categories,omitempty" v:"max=20,enum=TENANCY_RECORDS|FINANCIAL_RECORDS|OWNERSHIP_RECORDS|AUDIT_TRAIL|DISPUTE_RECORDS|LEGAL_HOLD|SECURITY_LOGS"`
}

// AdminListPrivacyRequests lists open requests by due date.
func (s *Service) AdminListPrivacyRequests(ctx context.Context, subj authz.Subject) ([]PrivacyRequestView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminPrivacy, authz.Resource{}); err != nil {
		return nil, err
	}
	rows, err := s.q.ListOpenDataSubjectRequests(ctx, s.dbq(ctx))
	out := make([]PrivacyRequestView, 0, len(rows))
	for _, r := range rows {
		out = append(out, dsrView(r))
	}
	return out, err
}

// AdminDecidePrivacyRequest records the operator's decision.
func (s *Service) AdminDecidePrivacyRequest(ctx context.Context, subj authz.Subject, id ids.ID, in PrivacyDecision) (PrivacyRequestView, error) {
	if err := schema.Validate(in); err != nil {
		return PrivacyRequestView{}, err
	}
	var out PrivacyRequestView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminPrivacy, authz.Resource{}); err != nil {
			return err
		}
		tx := mustTx(ctx)
		d, err := s.q.GetDataSubjectRequest(ctx, tx, id)
		if isNoRows(err) {
			return notFound(AggDSR)
		}
		if err != nil {
			return err
		}
		switch {
		case d.Status == "SUBMITTED" && in.Outcome == "DENIED_WITH_REASON":
		case d.Status != "PROCESSING":
			return conflict("REQUEST_NOT_PROCESSING", "Only verified requests in processing are decided.")
		case d.RequestType == "ACCESS" || d.RequestType == "EXPORT":
			return conflict("FULFILLED_AUTOMATICALLY", "Access and export requests are fulfilled with the generated export.")
		case in.Outcome == "PARTIALLY_FULFILLED" && len(in.RetainedCategories) == 0:
			return apperrors.InvalidRequest("Name the retained categories.")
		}
		if err := s.decideDSR(ctx, d, in.Outcome, in.Reason, in.RetainedCategories, nil); err != nil {
			return err
		}
		if err := s.record(ctx, "privacy.request.decide", AggDSR, id, map[string]any{"outcome": in.Outcome, "retained": in.RetainedCategories}, nil); err != nil {
			return err
		}
		n, err := s.q.GetDataSubjectRequest(ctx, tx, id)
		out = dsrView(n)
		return err
	})
	return out, err
}

func (s *Service) decideDSR(ctx context.Context, d db.DataSubjectRequest, outcome, reason string, retained []string, report *ids.ID) error {
	if err := s.q.SetDataSubjectDecision(ctx, mustTx(ctx), db.SetDataSubjectDecisionParams{ID: d.ID, DecisionReason: &reason, RetainedCategories: nonNil(retained),
		ReportID: report, DecidedAt: ptr(s.now())}); err != nil {
		return err
	}
	if _, _, err := s.transition(ctx, domain.DataSubjectRequest, d.ID, lifecycle.Change{To: outcome, Command: "decide"}); err != nil {
		return err
	}
	return s.notifyOwner(ctx, d.AccountID, TemplatePrivacyUpdate, map[string]string{"request_type": lower(d.RequestType), "status": lower(outcome)})
}

// RunPrivacyJobs moves verified requests into processing (queuing exports),
// fulfils exports whose document is ready, closes decided requests after
// 30 days and applies retention: guest details of ended stays older than
// the retention period are anonymized unless a claim or dispute holds them.
func (s *Service) RunPrivacyJobs(ctx context.Context, batch int32) error {
	now := s.now()
	ready, err := s.q.DataSubjectRequestsToProcess(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range ready {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			tx := mustTx(ctx)
			d, err := s.q.GetDataSubjectRequest(ctx, tx, id)
			if err != nil || d.Status != "VERIFIED_REQUESTER" {
				return err
			}
			if _, _, err := s.transition(ctx, domain.DataSubjectRequest, id, lifecycle.Change{To: "PROCESSING", Command: "process"}); err != nil {
				return err
			}
			if d.RequestType != "ACCESS" && d.RequestType != "EXPORT" {
				return nil // decided by an operator
			}
			r, err := s.q.InsertReport(ctx, tx, db.InsertReportParams{ID: ids.New(), AccountID: d.AccountID, ReportType: "PRIVACY_EXPORT", DataSubjectRequestID: &id,
				CreatedAt: now})
			if err != nil {
				return err
			}
			return s.q.SetDataSubjectReport(ctx, tx, db.SetDataSubjectReportParams{ID: id, ReportID: &r.ID, UpdatedAt: now})
		}); err != nil && !benign(err) {
			return err
		}
	}
	awaiting, err := s.q.DataSubjectRequestsAwaitingReport(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, a := range awaiting {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			tx := mustTx(ctx)
			d, err := s.q.GetDataSubjectRequest(ctx, tx, a.ID)
			if err != nil || d.Status != "PROCESSING" {
				return err
			}
			r, err := s.q.GetReport(ctx, tx, *a.ReportID)
			if err != nil {
				return err
			}
			if r.Status == "READY" {
				return s.decideDSR(ctx, d, "FULFILLED", "Export generated.", nil, &r.ID)
			}
			// The export failed repeatedly: queue a fresh attempt.
			n, err := s.q.InsertReport(ctx, tx, db.InsertReportParams{ID: ids.New(), AccountID: d.AccountID, ReportType: "PRIVACY_EXPORT", DataSubjectRequestID: &d.ID,
				CreatedAt: now})
			if err != nil {
				return err
			}
			return s.q.SetDataSubjectReport(ctx, tx, db.SetDataSubjectReportParams{ID: d.ID, ReportID: &n.ID, UpdatedAt: now})
		}); err != nil && !benign(err) {
			return err
		}
	}
	closing, err := s.q.DataSubjectRequestsToClose(ctx, s.Pool, db.DataSubjectRequestsToCloseParams{DecidedAt: ptr(now.Add(-dsrResponseWindow)), Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range closing {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			_, _, err := s.transition(ctx, domain.DataSubjectRequest, id, lifecycle.Change{To: "CLOSED", Command: "close"})
			return err
		}); err != nil && !benign(err) {
			return err
		}
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		n, err := s.q.AnonymizeShortletGuests(ctx, mustTx(ctx), db.AnonymizeShortletGuestsParams{Now: now, Before: dayOf(now).Add(-s.Policy.Domain.GuestDataRetention)})
		if err != nil || n == 0 {
			return err
		}
		return s.record(ctx, "privacy.retention.guest_anonymization", "privacy_retention", ids.Nil, map[string]any{"guests": n},
			map[string]any{"retention": s.Policy.Domain.GuestDataRetention.String(), "holds": "open damage claims and disputes"})
	})
}

func init() {
	produces("dispute.case.updated.v1", AggDispute, "Owner dispute case opened/updated/resolved (engineering addition for admin operations).", "Admin", DisputeEvent{})
}
