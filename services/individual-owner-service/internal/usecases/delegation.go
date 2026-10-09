package usecases

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Property manager delegation (§13.21). The owner remains the authority:
// property-manager-service owns manager profiles, search and the manager
// workspace; this service stores the owner's grant — property-specific,
// explicitly capability-scoped, versioned, time-limited and revocable. The
// authz engine reads live assignments on every request, so revocation and
// expiry take effect immediately.

// AggAssignment is the event aggregate of manager assignments.
const AggAssignment = "manager_assignment"

// roleCapabilities is the explicit capability matrix per role type.
var roleCapabilities = map[string][]authz.Capability{
	"FULL_MANAGEMENT": {authz.CapPropertyView, authz.CapPropertyEdit, authz.CapPropertyMedia, authz.CapUnitsManage, authz.CapAssetsManage,
		authz.CapListingManage, authz.CapListingPublish, authz.CapListingPause, authz.CapPricingManage, authz.CapAvailability, authz.CapLeadsManage,
		authz.CapViewingsManage, authz.CapApplicationReview, authz.CapApplicationDecide, authz.CapOffersManage, authz.CapAgreementsManage,
		authz.CapTenancyManage, authz.CapNoticesIssue, authz.CapRentView, authz.CapUtilitiesManage, authz.CapServiceCharges,
		authz.CapMaintenanceReq, authz.CapMaintenanceApprove, authz.CapProfessionals, authz.CapReportsView, authz.CapDisputesManage,
		authz.CapShortletManage, authz.CapShortletBookings, authz.CapAccessGrant},
	"LETTING_ONLY": {authz.CapPropertyView, authz.CapPropertyMedia, authz.CapListingManage, authz.CapListingPublish, authz.CapListingPause,
		authz.CapAvailability, authz.CapLeadsManage, authz.CapViewingsManage, authz.CapApplicationReview, authz.CapOffersManage, authz.CapReportsView},
	"MAINTENANCE_ONLY": {authz.CapPropertyView, authz.CapAssetsManage, authz.CapMaintenanceReq, authz.CapMaintenanceApprove,
		authz.CapProfessionals, authz.CapReportsView},
	"SHORTLET_OPERATIONS": {authz.CapPropertyView, authz.CapPropertyMedia, authz.CapListingManage, authz.CapListingPause, authz.CapAvailability,
		authz.CapShortletManage, authz.CapShortletBookings, authz.CapLeadsManage, authz.CapMaintenanceReq, authz.CapReportsView},
	"INSPECTION_ONLY": {authz.CapPropertyView, authz.CapAssetsManage, authz.CapReportsView},
}

// RoleCapabilities exposes the matrix (documentation and tests).
func RoleCapabilities() map[string][]authz.Capability { return roleCapabilities }

func capsFor(role string, requested []string) ([]string, error) {
	allowed := roleCapabilities[role]
	if len(requested) == 0 {
		out := make([]string, 0, len(allowed))
		for _, c := range allowed {
			out = append(out, string(c))
		}
		return out, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range requested {
		if !authz.IsDelegable(authz.Capability(c)) {
			return nil, apperrors.InvalidRequest("Capability "+c+" cannot be delegated.", apperrors.Detail{Field: "capabilities", Code: "NOT_DELEGABLE", Message: c})
		}
		if !slices.Contains(allowed, authz.Capability(c)) {
			return nil, apperrors.InvalidRequest("Capability "+c+" is outside the "+role+" role.", apperrors.Detail{Field: "capabilities", Code: "OUTSIDE_ROLE", Message: c})
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ProposalView is an owner-originated management proposal.
type ProposalView struct {
	ID                 ids.ID    `json:"id"`
	PropertyID         ids.ID    `json:"property_id"`
	ManagerDomain      string    `json:"manager_domain"`
	ManagerRefID       ids.ID    `json:"manager_ref_id"`
	ManagerDisplayName string    `json:"manager_display_name"`
	RoleType           string    `json:"role_type"`
	Capabilities       []string  `json:"capabilities"`
	FeeType            string    `json:"fee_type"`
	FeeBps             *int32    `json:"fee_bps"`
	FeeAmountMinor     *int64    `json:"fee_amount_minor"`
	FeeCurrency        *string   `json:"fee_currency"`
	Status             string    `json:"status"`
	Message            *string   `json:"message"`
	ExpiresAt          time.Time `json:"expires_at"`
	CreatedAt          time.Time `json:"created_at"`
}

func proposalView(p db.ManagerProposal) ProposalView {
	return ProposalView{ID: p.ID, PropertyID: p.PropertyID, ManagerDomain: p.ManagerDomain, ManagerRefID: p.ManagerRefID, ManagerDisplayName: p.ManagerDisplayName,
		RoleType: p.RoleType, Capabilities: nonNil(p.Capabilities), FeeType: p.FeeType, FeeBps: p.FeeBps, FeeAmountMinor: p.FeeAmountMinor,
		FeeCurrency: p.FeeCurrency, Status: p.Status, Message: p.Message, ExpiresAt: p.ExpiresAt, CreatedAt: p.CreatedAt}
}

// ProposalInput proposes a mandate to a manager found in property-manager-service.
type ProposalInput struct {
	PropertyID         ids.ID   `json:"property_id" v:"required"`
	ManagerDomain      string   `json:"manager_domain" v:"required,enum=property-manager|business-operations"`
	ManagerRefID       ids.ID   `json:"manager_ref_id" v:"required" doc:"Manager/organization ID in the manager's own domain"`
	ManagerDisplayName string   `json:"manager_display_name" v:"required,max=160"`
	RoleType           string   `json:"role_type" v:"required,enum=FULL_MANAGEMENT|LETTING_ONLY|MAINTENANCE_ONLY|SHORTLET_OPERATIONS|INSPECTION_ONLY"`
	Capabilities       []string `json:"capabilities,omitempty" v:"max=30" doc:"Subset of the role's capability matrix; omitted = the whole role set"`
	FeeType            string   `json:"fee_type" v:"required,enum=PERCENT_OF_RENT|FIXED_MONTHLY|PER_TASK|NONE"`
	FeeBps             *int32   `json:"fee_bps,omitempty" v:"min=0,max=10000"`
	FeeAmountMinor     *int64   `json:"fee_amount_minor,omitempty" v:"min=0"`
	FeeCurrency        *string  `json:"fee_currency,omitempty" v:"format=currency"`
	Message            *string  `json:"message,omitempty" v:"max=2000"`
	ExpiresInDays      int32    `json:"expires_in_days,omitempty" v:"min=1,max=30" doc:"Default 14"`
}

// CreateProposal records an owner proposal.
func (s *Service) CreateProposal(ctx context.Context, subj authz.Subject, in ProposalInput) (ProposalView, error) {
	if err := schema.Validate(in); err != nil {
		return ProposalView{}, err
	}
	caps, err := capsFor(in.RoleType, in.Capabilities)
	if err != nil {
		return ProposalView{}, err
	}
	switch in.FeeType {
	case "PERCENT_OF_RENT":
		if in.FeeBps == nil {
			return ProposalView{}, apperrors.InvalidRequest("fee_bps is required for PERCENT_OF_RENT.")
		}
	case "FIXED_MONTHLY", "PER_TASK":
		if in.FeeAmountMinor == nil || in.FeeCurrency == nil {
			return ProposalView{}, apperrors.InvalidRequest("fee_amount_minor and fee_currency are required for fixed fees.")
		}
	}
	days := in.ExpiresInDays
	if days == 0 {
		days = 14
	}
	var out ProposalView
	err = s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapManagerAssign), in.PropertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, in.PropertyID); err != nil {
			return err
		}
		now := s.now()
		p, err := s.q.InsertManagerProposal(ctx, mustTx(ctx), db.InsertManagerProposalParams{ID: ids.New(), PropertyID: in.PropertyID, ManagerDomain: in.ManagerDomain,
			ManagerRefID: in.ManagerRefID, ManagerDisplayName: strings.TrimSpace(in.ManagerDisplayName), RoleType: in.RoleType, Capabilities: caps,
			FeeType: in.FeeType, FeeBps: in.FeeBps, FeeAmountMinor: in.FeeAmountMinor, FeeCurrency: in.FeeCurrency, Message: in.Message,
			ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour), CreatedAt: now})
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, in.PropertyID, "MANAGER_PROPOSED", "Management proposed to "+p.ManagerDisplayName, "manager_proposal", p.ID); err != nil {
			return err
		}
		out = proposalView(p)
		return s.record(ctx, "manager.proposal.create", "manager_proposal", p.ID, map[string]any{"role_type": p.RoleType, "capabilities": caps}, nil)
	})
	return out, err
}

// ListProposals lists a property's proposals.
func (s *Service) ListProposals(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]ProposalView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapManagerAssign), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListManagerProposals(ctx, s.dbq(ctx), propertyID)
	out := make([]ProposalView, 0, len(rows))
	for _, r := range rows {
		out = append(out, proposalView(r))
	}
	return out, err
}

// WithdrawProposal withdraws an open proposal.
func (s *Service) WithdrawProposal(ctx context.Context, subj authz.Subject, id ids.ID) (ProposalView, error) {
	var out ProposalView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		p, err := s.q.GetManagerProposalForUpdate(ctx, tx, id)
		if isNoRows(err) {
			return notFound("manager_proposal")
		}
		if err != nil {
			return err
		}
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapManagerAssign), p.PropertyID); err != nil {
			return err
		}
		n, err := s.q.SetManagerProposalStatus(ctx, tx, db.SetManagerProposalStatusParams{ID: id, Status: "SUBMITTED", Status_2: "WITHDRAWN", UpdatedAt: s.now()})
		if err != nil {
			return err
		}
		if n == 0 {
			return conflict("PROPOSAL_NOT_OPEN", "The proposal is no longer open.")
		}
		p.Status = "WITHDRAWN"
		out = proposalView(p)
		return s.record(ctx, "manager.proposal.withdraw", "manager_proposal", id, nil, nil)
	})
	return out, err
}

// PermissionView is one immutable permission version.
type PermissionView struct {
	VersionNo           int32     `json:"version_no"`
	Capabilities        []string  `json:"capabilities"`
	SpendLimitMinor     int64     `json:"spend_limit_minor"`
	EmergencyLimitMinor int64     `json:"emergency_limit_minor"`
	Currency            string    `json:"currency"`
	EmergencyRule       string    `json:"emergency_rule"`
	CreatedAt           time.Time `json:"created_at"`
}

// AssignmentView is a manager assignment with its current permissions.
type AssignmentView struct {
	ID                  ids.ID           `json:"id"`
	PropertyID          ids.ID           `json:"property_id"`
	ProposalID          *ids.ID          `json:"proposal_id"`
	ManagerDomain       string           `json:"manager_domain"`
	ManagerRefID        ids.ID           `json:"manager_ref_id"`
	ManagerDisplayName  string           `json:"manager_display_name"`
	RoleType            string           `json:"role_type"`
	Status              string           `json:"status"`
	StartsAt            time.Time        `json:"starts_at"`
	EndsAt              time.Time        `json:"ends_at"`
	ReportSchedule      string           `json:"report_schedule"`
	Permissions         PermissionView   `json:"permissions"`
	PermissionHistory   []PermissionView `json:"permission_history"`
	AllowedNextStatuses []string         `json:"allowed_next_statuses"`
	RevokedAt           *time.Time       `json:"revoked_at"`
	RevokeReason        *string          `json:"revoke_reason"`
	Version             int64            `json:"version"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
}

func permView(p db.ManagerPermission) PermissionView {
	return PermissionView{VersionNo: p.VersionNo, Capabilities: nonNil(p.Capabilities), SpendLimitMinor: p.SpendLimitMinor,
		EmergencyLimitMinor: p.EmergencyLimitMinor, Currency: p.Currency, EmergencyRule: p.EmergencyRule, CreatedAt: p.CreatedAt}
}

func (s *Service) assignmentView(ctx context.Context, a db.ManagerAssignment, history bool) (AssignmentView, error) {
	v := AssignmentView{ID: a.ID, PropertyID: a.PropertyID, ProposalID: a.ProposalID, ManagerDomain: a.ManagerDomain, ManagerRefID: a.ManagerRefID,
		ManagerDisplayName: a.ManagerDisplayName, RoleType: a.RoleType, Status: a.Status, StartsAt: a.StartsAt, EndsAt: a.EndsAt,
		ReportSchedule: a.ReportSchedule, PermissionHistory: []PermissionView{}, AllowedNextStatuses: nonNil(domain.ManagerAssignment.Targets(a.Status)),
		RevokedAt: a.RevokedAt, RevokeReason: a.RevokeReason, Version: a.Version, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	q := s.dbq(ctx)
	p, err := s.q.GetManagerPermission(ctx, q, db.GetManagerPermissionParams{AssignmentID: a.ID, VersionNo: a.CurrentPermissionVersion})
	if err != nil {
		return v, err
	}
	v.Permissions = permView(p)
	if history {
		all, err := s.q.ListManagerPermissions(ctx, q, a.ID)
		if err != nil {
			return v, err
		}
		for _, h := range all {
			v.PermissionHistory = append(v.PermissionHistory, permView(h))
		}
	}
	return v, nil
}

// AssignmentEvent is the payload of manager.assignment.* events.
type AssignmentEvent struct {
	SchemaVersion       int       `json:"schema_version"`
	AssignmentID        ids.ID    `json:"assignment_id"`
	PropertyID          ids.ID    `json:"property_id"`
	ManagerDomain       string    `json:"manager_domain"`
	ManagerRefID        ids.ID    `json:"manager_ref_id"`
	RoleType            string    `json:"role_type"`
	Status              string    `json:"status"`
	PreviousStatus      string    `json:"previous_status"`
	Capabilities        []string  `json:"capabilities"`
	SpendLimitMinor     int64     `json:"spend_limit_minor"`
	EmergencyLimitMinor int64     `json:"emergency_limit_minor"`
	Currency            string    `json:"currency"`
	EmergencyRule       string    `json:"emergency_rule"`
	PermissionVersion   int32     `json:"permission_version"`
	StartsAt            time.Time `json:"starts_at"`
	EndsAt              time.Time `json:"ends_at"`
	ReportSchedule      string    `json:"report_schedule"`
	ReasonCode          *string   `json:"reason_code"`
	AssignmentVersion   int64     `json:"assignment_version" doc:"Monotonic; consumers discard stale versions"`
	OccurredAt          time.Time `json:"occurred_at"`
}

func (s *Service) emitAssignment(ctx context.Context, eventType string, id ids.ID, previous string, reason *string) error {
	tx := mustTx(ctx)
	a, err := s.q.GetManagerAssignment(ctx, tx, id)
	if err != nil {
		return err
	}
	p, err := s.q.GetManagerPermission(ctx, tx, db.GetManagerPermissionParams{AssignmentID: id, VersionNo: a.CurrentPermissionVersion})
	if err != nil {
		return err
	}
	return s.publish(ctx, eventType, AggAssignment, id, AssignmentEvent{SchemaVersion: 1, AssignmentID: id, PropertyID: a.PropertyID,
		ManagerDomain: a.ManagerDomain, ManagerRefID: a.ManagerRefID, RoleType: a.RoleType, Status: a.Status, PreviousStatus: previous,
		Capabilities: nonNil(p.Capabilities), SpendLimitMinor: p.SpendLimitMinor, EmergencyLimitMinor: p.EmergencyLimitMinor, Currency: p.Currency,
		EmergencyRule: p.EmergencyRule, PermissionVersion: p.VersionNo, StartsAt: a.StartsAt, EndsAt: a.EndsAt, ReportSchedule: a.ReportSchedule,
		ReasonCode: reason, AssignmentVersion: a.Version, OccurredAt: s.now()})
}

// AssignmentInput turns an open proposal into an assignment.
type AssignmentInput struct {
	ProposalID          ids.ID    `json:"proposal_id" v:"required"`
	StartsAt            time.Time `json:"starts_at" v:"required"`
	EndsAt              time.Time `json:"ends_at" v:"required"`
	Capabilities        []string  `json:"capabilities,omitempty" v:"max=30" doc:"Optional narrowing of the proposal's capabilities"`
	SpendLimitMinor     int64     `json:"spend_limit_minor" v:"min=0" doc:"Per-job maintenance approval limit"`
	EmergencyLimitMinor int64     `json:"emergency_limit_minor" v:"min=0"`
	Currency            string    `json:"currency" v:"required,format=currency"`
	EmergencyRule       string    `json:"emergency_rule,omitempty" v:"enum=NOTIFY_OWNER|OWNER_APPROVAL|ACT_WITHIN_LIMIT"`
	ReportSchedule      string    `json:"report_schedule,omitempty" v:"enum=WEEKLY|MONTHLY|QUARTERLY"`
}

const maxMandate = 3 * 366 * 24 * time.Hour

// CreateAssignment creates a PROPOSED assignment from an open proposal.
// Co-owner consent MANAGER_ASSIGNMENT targets the proposal.
func (s *Service) CreateAssignment(ctx context.Context, subj authz.Subject, in AssignmentInput) (AssignmentView, error) {
	if err := schema.Validate(in); err != nil {
		return AssignmentView{}, err
	}
	now := s.now()
	switch {
	case !in.EndsAt.After(in.StartsAt):
		return AssignmentView{}, apperrors.InvalidRequest("ends_at must be after starts_at.")
	case in.StartsAt.Before(now.Add(-5 * time.Minute)):
		return AssignmentView{}, apperrors.InvalidRequest("starts_at cannot be in the past.")
	case in.EndsAt.Sub(in.StartsAt) > maxMandate:
		return AssignmentView{}, apperrors.InvalidRequest("A mandate cannot exceed 3 years.")
	case in.EmergencyLimitMinor > in.SpendLimitMinor && in.SpendLimitMinor > 0:
		return AssignmentView{}, apperrors.InvalidRequest("emergency_limit_minor cannot exceed spend_limit_minor.")
	}
	var out AssignmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		p, err := s.q.GetManagerProposalForUpdate(ctx, tx, in.ProposalID)
		if isNoRows(err) {
			return notFound("manager_proposal")
		}
		if err != nil {
			return err
		}
		d, err := s.authorize(ctx, subj, authz.Do(authz.CapManagerAssign), p.PropertyID)
		if err != nil {
			return err
		}
		if err := s.requireConsent(ctx, d, p.PropertyID, ConsentManagerAssignment, p.ID, accountOf(subj)); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, p.PropertyID); err != nil {
			return err
		}
		if p.Status != "SUBMITTED" || !p.ExpiresAt.After(now) {
			return conflict("PROPOSAL_NOT_OPEN", "The proposal is no longer open.")
		}
		caps := p.Capabilities
		if len(in.Capabilities) > 0 {
			for _, c := range in.Capabilities {
				if !slices.Contains(p.Capabilities, c) {
					return apperrors.InvalidRequest("Capabilities may only narrow the proposal.", apperrors.Detail{Field: "capabilities", Code: "NOT_IN_PROPOSAL", Message: c})
				}
			}
			caps = in.Capabilities
			slices.Sort(caps)
		}
		sched := in.ReportSchedule
		if sched == "" {
			sched = "MONTHLY"
		}
		rule := in.EmergencyRule
		if rule == "" {
			rule = "NOTIFY_OWNER"
		}
		creator := s.actingAccount(ctx, subj, p.PropertyID)
		a, err := s.q.InsertManagerAssignment(ctx, tx, db.InsertManagerAssignmentParams{ID: ids.New(), PropertyID: p.PropertyID, ProposalID: &p.ID,
			ManagerDomain: p.ManagerDomain, ManagerRefID: p.ManagerRefID, ManagerDisplayName: p.ManagerDisplayName, RoleType: p.RoleType,
			StartsAt: in.StartsAt.UTC(), EndsAt: in.EndsAt.UTC(), ReportSchedule: sched, CreatedByAccount: creator, CreatedAt: now})
		if isUnique(err, "manager_assignments_live_uniq") {
			return conflict("MANAGER_ALREADY_ASSIGNED", "This manager already holds a live assignment for the property.")
		}
		if err != nil {
			return err
		}
		if _, err := s.q.InsertManagerPermission(ctx, tx, db.InsertManagerPermissionParams{ID: ids.New(), AssignmentID: a.ID, VersionNo: 1, Capabilities: caps,
			SpendLimitMinor: in.SpendLimitMinor, EmergencyLimitMinor: in.EmergencyLimitMinor, Currency: in.Currency, EmergencyRule: rule,
			CreatedByAccount: creator, CreatedAt: now}); err != nil {
			return err
		}
		if _, err := s.q.SetManagerProposalStatus(ctx, tx, db.SetManagerProposalStatusParams{ID: p.ID, Status: "SUBMITTED", Status_2: "ACCEPTED", UpdatedAt: now}); err != nil {
			return err
		}
		if err := domain.ManagerAssignment.RecordCreation(ctx, tx, a.ID, &a.PropertyID, "PROPOSED", now); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, a.PropertyID, "MANAGER_ASSIGNED", "Manager assignment created for "+a.ManagerDisplayName, AggAssignment, a.ID); err != nil {
			return err
		}
		if err := s.record(ctx, "manager.assignment.create", AggAssignment, a.ID, map[string]any{"capabilities": caps, "spend_limit_minor": in.SpendLimitMinor}, nil); err != nil {
			return err
		}
		if err := s.emitAssignment(ctx, "manager.assignment.created.v1", a.ID, "", nil); err != nil {
			return err
		}
		out, err = s.assignmentView(ctx, a, true)
		return err
	})
	return out, err
}

// loadAssignment locks an assignment and authorizes action on its property.
func (s *Service) loadAssignment(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.ManagerAssignment, authz.Decision, error) {
	a, err := s.q.GetManagerAssignmentForUpdate(ctx, mustTx(ctx), id)
	if isNoRows(err) {
		return a, authz.Decision{}, notFound(AggAssignment)
	}
	if err != nil {
		return a, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, a.PropertyID)
	return a, d, err
}

// GetAssignment returns an assignment with its permission history.
func (s *Service) GetAssignment(ctx context.Context, subj authz.Subject, id ids.ID) (AssignmentView, error) {
	var out AssignmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, _, err := s.loadAssignment(ctx, subj, id, authz.Read(authz.CapManagerAssign))
		if err != nil {
			return err
		}
		out, err = s.assignmentView(ctx, a, true)
		return err
	})
	return out, err
}

// ListAssignments lists assignments across the caller's properties.
func (s *Service) ListAssignments(ctx context.Context, subj authz.Subject, status, cursor string, limit int) (pagination.Response[AssignmentView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[AssignmentView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[AssignmentView]{}, err
	}
	rows, err := s.q.ListAssignmentsForAccount(ctx, s.dbq(ctx), db.ListAssignmentsForAccountParams{AccountID: accountOf(subj), Status: strp(status),
		CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[AssignmentView]{}, err
	}
	out := make([]AssignmentView, 0, len(rows))
	for _, r := range rows {
		v, err := s.assignmentView(ctx, r, false)
		if err != nil {
			return pagination.Response[AssignmentView]{}, err
		}
		out = append(out, v)
	}
	return pagination.Build(out, limit, func(v AssignmentView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

// AssignmentPatch changes terms; capability/limit changes create a new
// immutable permission version. Widening requires co-owner consent.
type AssignmentPatch struct {
	Capabilities        []string   `json:"capabilities,omitempty" v:"max=30"`
	SpendLimitMinor     *int64     `json:"spend_limit_minor,omitempty" v:"min=0"`
	EmergencyLimitMinor *int64     `json:"emergency_limit_minor,omitempty" v:"min=0"`
	EmergencyRule       *string    `json:"emergency_rule,omitempty" v:"enum=NOTIFY_OWNER|OWNER_APPROVAL|ACT_WITHIN_LIMIT"`
	EndsAt              *time.Time `json:"ends_at,omitempty"`
	ReportSchedule      *string    `json:"report_schedule,omitempty" v:"enum=WEEKLY|MONTHLY|QUARTERLY"`
}

var liveAssignment = []string{"PROPOSED", "ACCEPTED", "ACTIVE", "SUSPENDED", "EXPIRING"}

// UpdateAssignment applies a patch with If-Match.
func (s *Service) UpdateAssignment(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in AssignmentPatch) (AssignmentView, error) {
	if err := schema.Validate(in); err != nil {
		return AssignmentView{}, err
	}
	var out AssignmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, d, err := s.loadAssignment(ctx, subj, id, authz.Do(authz.CapManagerAssign))
		if err != nil {
			return err
		}
		if err := checkVersion(expected, a.Version); err != nil {
			return err
		}
		if !slices.Contains(liveAssignment, a.Status) {
			return conflict("ASSIGNMENT_NOT_LIVE", "The assignment has ended.")
		}
		tx, now := mustTx(ctx), s.now()
		cur, err := s.q.GetManagerPermission(ctx, tx, db.GetManagerPermissionParams{AssignmentID: id, VersionNo: a.CurrentPermissionVersion})
		if err != nil {
			return err
		}
		next := cur
		if in.Capabilities != nil {
			if next.Capabilities, err = capsFor(a.RoleType, in.Capabilities); err != nil {
				return err
			}
		}
		if in.SpendLimitMinor != nil {
			next.SpendLimitMinor = *in.SpendLimitMinor
		}
		if in.EmergencyLimitMinor != nil {
			next.EmergencyLimitMinor = *in.EmergencyLimitMinor
		}
		if in.EmergencyRule != nil {
			next.EmergencyRule = *in.EmergencyRule
		}
		widened := next.SpendLimitMinor > cur.SpendLimitMinor || next.EmergencyLimitMinor > cur.EmergencyLimitMinor
		for _, c := range next.Capabilities {
			widened = widened || !slices.Contains(cur.Capabilities, c)
		}
		if widened {
			if err := s.requireConsent(ctx, d, a.PropertyID, ConsentManagerAssignment, id, accountOf(subj)); err != nil {
				return err
			}
		}
		version := a.CurrentPermissionVersion
		changed := !slices.Equal(next.Capabilities, cur.Capabilities) || next.SpendLimitMinor != cur.SpendLimitMinor ||
			next.EmergencyLimitMinor != cur.EmergencyLimitMinor || next.EmergencyRule != cur.EmergencyRule
		if changed {
			version++
			if _, err := s.q.InsertManagerPermission(ctx, tx, db.InsertManagerPermissionParams{ID: ids.New(), AssignmentID: id, VersionNo: version,
				Capabilities: next.Capabilities, SpendLimitMinor: next.SpendLimitMinor, EmergencyLimitMinor: next.EmergencyLimitMinor,
				Currency: cur.Currency, EmergencyRule: next.EmergencyRule, CreatedByAccount: s.actingAccount(ctx, subj, a.PropertyID), CreatedAt: now}); err != nil {
				return err
			}
		}
		ends, sched := a.EndsAt, a.ReportSchedule
		if in.EndsAt != nil {
			if !in.EndsAt.After(now) || !in.EndsAt.After(a.StartsAt) || in.EndsAt.Sub(a.StartsAt) > maxMandate {
				return apperrors.InvalidRequest("ends_at must be in the future, after starts_at and within 3 years of it.")
			}
			ends = in.EndsAt.UTC()
		}
		if in.ReportSchedule != nil {
			sched = *in.ReportSchedule
		}
		na, err := s.q.UpdateAssignmentTerms(ctx, tx, db.UpdateAssignmentTermsParams{ID: id, Version: a.Version, EndsAt: ends, ReportSchedule: sched,
			CurrentPermissionVersion: version, UpdatedAt: now})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		if na.Status == "EXPIRING" && na.EndsAt.After(now.Add(expiringWindow)) {
			if _, _, err := s.transition(ctx, domain.ManagerAssignment, id, lifecycle.Change{To: "ACTIVE", Command: "extend"}); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "manager.assignment.update", AggAssignment, id, map[string]any{"permission_version": version, "capabilities": next.Capabilities,
			"ends_at": ends}, nil); err != nil {
			return err
		}
		if err := s.emitAssignment(ctx, "manager.assignment.updated.v1", id, a.Status, nil); err != nil {
			return err
		}
		na, err = s.q.GetManagerAssignment(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.assignmentView(ctx, na, true)
		return err
	})
	return out, err
}

const expiringWindow = 14 * 24 * time.Hour

// ReasonCodeInput carries a mandatory reason code.
type ReasonCodeInput struct {
	ReasonCode string  `json:"reason_code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
	Note       *string `json:"note,omitempty" v:"max=1000"`
}

// ChangeAssignmentStatus suspends, resumes or revokes an assignment.
func (s *Service) ChangeAssignmentStatus(ctx context.Context, subj authz.Subject, id ids.ID, command string, in ReasonCodeInput) (AssignmentView, error) {
	if err := schema.Validate(in); err != nil {
		return AssignmentView{}, err
	}
	var out AssignmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, _, err := s.loadAssignment(ctx, subj, id, authz.Do(authz.CapManagerAssign))
		if err != nil {
			return err
		}
		to, event := "", "manager.assignment.updated.v1"
		switch command {
		case "suspend":
			to = "SUSPENDED"
		case "resume":
			to = "ACTIVE"
		case "revoke":
			to, event = "REVOKED", "manager.assignment.revoked.v1"
			if a.Status == "PROPOSED" {
				to = "TERMINATED"
			}
		}
		if _, _, err := s.transition(ctx, domain.ManagerAssignment, id, lifecycle.Change{To: to, Command: command, Reason: in.ReasonCode}); err != nil {
			return err
		}
		if command == "revoke" || command == "suspend" {
			// Revocation/suspension ends the manager's access immediately (§14.5).
			if err := s.endRelationGrants(ctx, "manager_assignment", id, "MANDATE_"+strings.ToUpper(command)+"D"); err != nil {
				return err
			}
		}
		if command == "revoke" {
			if err := s.q.SetAssignmentRevocation(ctx, mustTx(ctx), db.SetAssignmentRevocationParams{ID: id, RevokedAt: ptr(s.now()), RevokeReason: &in.ReasonCode}); err != nil {
				return err
			}
		}
		if err := s.propertyHistory(ctx, a.PropertyID, "MANAGER_"+strings.ToUpper(command), "Manager assignment "+lower(to)+": "+a.ManagerDisplayName, AggAssignment, id); err != nil {
			return err
		}
		if err := s.record(ctx, "manager.assignment."+command, AggAssignment, id, map[string]any{"status": to, "reason_code": in.ReasonCode}, nil); err != nil {
			return err
		}
		if err := s.emitAssignment(ctx, event, id, a.Status, &in.ReasonCode); err != nil {
			return err
		}
		na, err := s.q.GetManagerAssignment(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.assignmentView(ctx, na, false)
		return err
	})
	return out, err
}

// ActivityView is one manager activity reference.
type ActivityView struct {
	ID           ids.ID    `json:"id"`
	ActivityType string    `json:"activity_type"`
	RefService   string    `json:"ref_service"`
	RefType      *string   `json:"ref_type"`
	RefID        *ids.ID   `json:"ref_id"`
	Summary      string    `json:"summary"`
	ActorDomain  *string   `json:"actor_domain"`
	ActorID      *ids.ID   `json:"actor_id"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// ListActivity pages an assignment's activity.
func (s *Service) ListActivity(ctx context.Context, subj authz.Subject, id ids.ID, cursor string, limit int) (pagination.Response[ActivityView], error) {
	a, err := s.q.GetManagerAssignment(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return pagination.Response[ActivityView]{}, notFound(AggAssignment)
	}
	if err != nil {
		return pagination.Response[ActivityView]{}, err
	}
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapManagerAssign), a.PropertyID); err != nil {
		return pagination.Response[ActivityView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[ActivityView]{}, err
	}
	rows, err := s.q.ListManagerActivity(ctx, s.dbq(ctx), db.ListManagerActivityParams{AssignmentID: id, CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[ActivityView]{}, err
	}
	out := make([]ActivityView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ActivityView{ID: r.ID, ActivityType: r.ActivityType, RefService: r.RefService, RefType: r.RefType, RefID: r.RefID,
			Summary: r.Summary, ActorDomain: r.ActorDomain, ActorID: r.ActorID, OccurredAt: r.OccurredAt})
	}
	return pagination.Build(out, limit, func(v ActivityView) any { return timeCursor{At: v.OccurredAt, ID: v.ID} })
}

// SearchManagers: discovery is owned by property-manager-service (§13.21),
// which has no implemented contract yet.
func (s *Service) SearchManagers(ctx context.Context, subj authz.Subject) ([]ProposalView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActManagerSearch, ids.Nil); err != nil {
		return nil, err
	}
	return nil, unavailable("Manager search", "property-manager-service")
}

// ---- internal (property-manager-service / business-operations-service) ----

func (s *Service) managerAssignment(ctx context.Context, subj authz.Subject, action authz.Action, id ids.ID) (db.ManagerAssignment, error) {
	if _, err := s.Authz.Require(ctx, subj, action, authz.Resource{}); err != nil {
		return db.ManagerAssignment{}, err
	}
	a, err := s.q.GetManagerAssignmentForUpdate(ctx, mustTx(ctx), id)
	// A manager only ever sees its own assignments.
	if isNoRows(err) || (err == nil && (a.ManagerDomain != string(subj.ActorDomain) || a.ManagerRefID != subj.ActorID)) {
		return db.ManagerAssignment{}, notFound(AggAssignment)
	}
	return a, err
}

// ManagerGetAssignment lets the assigned manager read its mandate.
func (s *Service) ManagerGetAssignment(ctx context.Context, subj authz.Subject, id ids.ID) (AssignmentView, error) {
	var out AssignmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, err := s.managerAssignment(ctx, subj, authz.ActInternalManagerRespond, id)
		if err != nil {
			return err
		}
		out, err = s.assignmentView(ctx, a, true)
		return err
	})
	return out, err
}

// ManagerResponseInput accepts or declines a proposed assignment.
type ManagerResponseInput struct {
	Decision string `json:"decision" v:"required,enum=ACCEPT|DECLINE"`
}

// ManagerRespond records the manager's acceptance (ACTIVE once started) or decline.
func (s *Service) ManagerRespond(ctx context.Context, subj authz.Subject, id ids.ID, in ManagerResponseInput) (AssignmentView, error) {
	if err := schema.Validate(in); err != nil {
		return AssignmentView{}, err
	}
	var out AssignmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, err := s.managerAssignment(ctx, subj, authz.ActInternalManagerRespond, id)
		if err != nil {
			return err
		}
		if a.Status != "PROPOSED" {
			return conflict("ASSIGNMENT_NOT_PROPOSED", "The assignment is not awaiting the manager's response.")
		}
		to := "TERMINATED"
		if in.Decision == "ACCEPT" {
			to = "ACCEPTED"
		}
		if _, _, err := s.transition(ctx, domain.ManagerAssignment, id, lifecycle.Change{To: to, Command: "manager_" + lower(in.Decision)}); err != nil {
			return err
		}
		if to == "ACCEPTED" && !a.StartsAt.After(s.now()) {
			if _, _, err := s.transition(ctx, domain.ManagerAssignment, id, lifecycle.Change{To: "ACTIVE", Command: "start"}); err != nil {
				return err
			}
		}
		if err := s.propertyHistory(ctx, a.PropertyID, "MANAGER_"+in.Decision+"ED", a.ManagerDisplayName+" responded: "+lower(in.Decision), AggAssignment, id); err != nil {
			return err
		}
		if err := s.record(ctx, "manager.assignment.respond", AggAssignment, id, map[string]any{"decision": in.Decision}, nil); err != nil {
			return err
		}
		if err := s.emitAssignment(ctx, "manager.assignment.updated.v1", id, a.Status, nil); err != nil {
			return err
		}
		na, err := s.q.GetManagerAssignment(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.assignmentView(ctx, na, false)
		return err
	})
	return out, err
}

// ActivityInput records manager activity performed in the manager workspace.
type ActivityInput struct {
	ActivityType string  `json:"activity_type" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}" ex:"INSPECTION_COMPLETED"`
	RefType      *string `json:"ref_type,omitempty" v:"max=60"`
	RefID        *ids.ID `json:"ref_id,omitempty"`
	Summary      string  `json:"summary" v:"required,max=1000"`
}

// ManagerRecordActivity appends an activity reference (live assignment only).
func (s *Service) ManagerRecordActivity(ctx context.Context, subj authz.Subject, id ids.ID, in ActivityInput) (ActivityView, error) {
	if err := schema.Validate(in); err != nil {
		return ActivityView{}, err
	}
	var out ActivityView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, err := s.managerAssignment(ctx, subj, authz.ActInternalManagerActivity, id)
		if err != nil {
			return err
		}
		if a.Status != "ACTIVE" && a.Status != "EXPIRING" {
			return conflict("ASSIGNMENT_NOT_ACTIVE", "Activity can only be recorded on an active assignment.")
		}
		svc, _ := platform.ServiceForDomain(subj.ActorDomain)
		dom := string(subj.ActorDomain)
		r, err := s.q.InsertManagerActivity(ctx, mustTx(ctx), db.InsertManagerActivityParams{ID: ids.New(), AssignmentID: id, PropertyID: a.PropertyID,
			ActivityType: in.ActivityType, RefService: string(svc), RefType: in.RefType, RefID: in.RefID, Summary: in.Summary, ActorDomain: &dom,
			ActorID: &subj.ActorID, OccurredAt: s.now()})
		if err != nil {
			return err
		}
		out = ActivityView{ID: r.ID, ActivityType: r.ActivityType, RefService: r.RefService, RefType: r.RefType, RefID: r.RefID, Summary: r.Summary,
			ActorDomain: r.ActorDomain, ActorID: r.ActorID, OccurredAt: r.OccurredAt}
		return nil
	})
	return out, err
}

// noteDelegateActivity records a manager-performed command against the
// manager's assignment (owner-visible activity trail).
func (s *Service) noteDelegateActivity(ctx context.Context, d authz.Decision, propertyID ids.ID, activity, refType string, refID ids.ID, summary string) error {
	if d.Via == nil || d.Via.Role != authz.RoleManager || d.Via.ReferenceID.IsZero() {
		return nil
	}
	_, err := s.q.InsertManagerActivity(ctx, mustTx(ctx), db.InsertManagerActivityParams{ID: ids.New(), AssignmentID: d.Via.ReferenceID, PropertyID: propertyID,
		ActivityType: activity, RefService: string(platform.IndividualOwnerService), RefType: strp(refType), RefID: idp(refID), Summary: summary, OccurredAt: s.now()})
	return err
}

// ---- jobs and hooks ----

// AdvanceAssignments starts accepted mandates, marks those ending soon as
// EXPIRING and expires ended ones (manager mandate expiry worker, §11).
func (s *Service) AdvanceAssignments(ctx context.Context, batch int32) error {
	now := s.now()
	step := func(ids []ids.ID, to, cmd string, event string) error {
		for _, id := range ids {
			err := s.InTx(ctx, func(ctx context.Context) error {
				prev, _, err := s.transition(ctx, domain.ManagerAssignment, id, lifecycle.Change{To: to, Command: cmd})
				if err != nil {
					return err
				}
				return s.emitAssignment(ctx, event, id, prev.Status, nil)
			})
			if err != nil && apperrors.CodeOf(err) != apperrors.CodeInvalidStateTransition && apperrors.CodeOf(err) != "VERSION_CONFLICT" {
				return err
			}
		}
		return nil
	}
	q := s.Pool
	starts, err := s.q.DueAssignmentActivations(ctx, q, db.DueAssignmentActivationsParams{StartsAt: now, Limit: batch})
	if err != nil {
		return err
	}
	if err := step(starts, "ACTIVE", "start", "manager.assignment.updated.v1"); err != nil {
		return err
	}
	ended, err := s.q.DueAssignmentExpired(ctx, q, db.DueAssignmentExpiredParams{EndsAt: now, Limit: batch})
	if err != nil {
		return err
	}
	if err := step(ended, "EXPIRED", "expire", "manager.assignment.revoked.v1"); err != nil {
		return err
	}
	soon, err := s.q.DueAssignmentExpiring(ctx, q, db.DueAssignmentExpiringParams{EndsAt: now.Add(expiringWindow), Limit: batch})
	if err != nil {
		return err
	}
	if err := step(soon, "EXPIRING", "expiring", "manager.assignment.updated.v1"); err != nil {
		return err
	}
	_, err = s.q.ExpireManagerProposals(ctx, q, now)
	return err
}

func init() {
	// A new owner never inherits the previous owner's managers.
	ownerChangedHooks = append(ownerChangedHooks, func(s *Service, ctx context.Context, propertyID, _, _ ids.ID) error {
		live, err := s.q.LiveAssignmentsForProperty(ctx, mustTx(ctx), propertyID)
		if err != nil {
			return err
		}
		reason := "OWNERSHIP_TRANSFERRED"
		for _, a := range live {
			if _, _, err := s.transition(ctx, domain.ManagerAssignment, a.ID, lifecycle.Change{To: "TERMINATED", Command: "owner_changed", Reason: reason}); err != nil {
				return err
			}
			if err := s.emitAssignment(ctx, "manager.assignment.revoked.v1", a.ID, a.Status, &reason); err != nil {
				return err
			}
		}
		return nil
	})
	produces("manager.assignment.created.v1", AggAssignment, "An owner mandate was created and is available to the manager service for acceptance.", "property-manager-service", AssignmentEvent{})
	produces("manager.assignment.updated.v1", AggAssignment, "Assignment status, permissions, limits or term changed.", "property-manager-service", AssignmentEvent{})
	produces("manager.assignment.revoked.v1", AggAssignment, "The assignment was revoked, terminated or expired; access ends immediately.", "property-manager-service", AssignmentEvent{})
}
