package usecases

import (
	"context"
	"encoding/json"
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

// Residential applications and tenant selection (§13.12). The applicant
// record originates in Marketplace; this service stores the domain
// application with typed applicant references and the minimum necessary
// data. Evidence is accessed through purpose- and time-limited grants on
// media-service. Screening uses a versioned policy of configured, lawful
// criteria captured at decision time; automated flags are recorded but a
// human always makes the final decision, with audited reasons.

// AggApplication / AggOffer are event aggregates.
const (
	AggApplication = "rental_application"
	AggOffer       = "rental_offer"
)

// Criterion is one screening criterion (configured types only).
type Criterion struct {
	Type      string `json:"type" v:"required,enum=MIN_INCOME_MULTIPLE|MAX_OCCUPANTS|PETS_ALLOWED|REFERENCES_REQUIRED|GUARANTOR_REQUIRED|INCOME_EVIDENCE_REQUIRED|IDENTITY_VERIFICATION_REQUIRED"`
	IntValue  *int32 `json:"int_value,omitempty" v:"min=0,max=100000" doc:"MIN_INCOME_MULTIPLE in basis points of monthly rent (25000 = 2.5x); MAX_OCCUPANTS count"`
	BoolValue *bool  `json:"bool_value,omitempty"`
}

// ScreeningPolicyInput creates a new screening policy version.
type ScreeningPolicyInput struct {
	Criteria []Criterion `json:"criteria" v:"required,max=10"`
}

// ScreeningPolicyView is a screening policy version.
type ScreeningPolicyView struct {
	ID            ids.ID      `json:"id"`
	PropertyID    ids.ID      `json:"property_id"`
	VersionNo     int32       `json:"version_no"`
	Criteria      []Criterion `json:"criteria"`
	PolicyVersion string      `json:"policy_version"`
	CreatedAt     time.Time   `json:"created_at"`
}

func screeningView(p db.ScreeningPolicy) ScreeningPolicyView {
	v := ScreeningPolicyView{ID: p.ID, PropertyID: p.PropertyID, VersionNo: p.VersionNo, Criteria: []Criterion{}, PolicyVersion: p.PolicyVersion, CreatedAt: p.CreatedAt}
	_ = json.Unmarshal(p.Criteria, &v.Criteria)
	return v
}

// CreateScreeningPolicy appends a new immutable policy version.
func (s *Service) CreateScreeningPolicy(ctx context.Context, subj authz.Subject, propertyID ids.ID, in ScreeningPolicyInput) (ScreeningPolicyView, error) {
	if err := schema.Validate(in); err != nil {
		return ScreeningPolicyView{}, err
	}
	seen := map[string]bool{}
	for _, c := range in.Criteria {
		if seen[c.Type] {
			return ScreeningPolicyView{}, apperrors.InvalidRequest("Duplicate criterion " + c.Type + ".")
		}
		seen[c.Type] = true
		switch c.Type {
		case "MIN_INCOME_MULTIPLE", "MAX_OCCUPANTS":
			if c.IntValue == nil || *c.IntValue <= 0 {
				return ScreeningPolicyView{}, apperrors.InvalidRequest(c.Type + " needs a positive int_value.")
			}
		default:
			if c.BoolValue == nil {
				return ScreeningPolicyView{}, apperrors.InvalidRequest(c.Type + " needs bool_value.")
			}
		}
	}
	var out ScreeningPolicyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapApplicationDecide), propertyID); err != nil {
			return err
		}
		raw, _ := json.Marshal(in.Criteria)
		dom, actor := s.actor(ctx, subj, propertyID)
		p, err := s.q.InsertScreeningPolicy(ctx, mustTx(ctx), db.InsertScreeningPolicyParams{ID: ids.New(), PropertyID: propertyID, Criteria: raw,
			PolicyVersion: s.Policy.Version, CreatedByActorDomain: dom, CreatedByActorID: actor, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = screeningView(p)
		return s.record(ctx, "screening.policy.create", "screening_policy", p.ID, map[string]any{"version_no": p.VersionNo, "criteria": in.Criteria}, nil)
	})
	return out, err
}

// ListScreeningPolicies lists a property's policy versions.
func (s *Service) ListScreeningPolicies(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]ScreeningPolicyView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapApplicationReview), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListScreeningPolicies(ctx, s.dbq(ctx), propertyID)
	out := make([]ScreeningPolicyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, screeningView(r))
	}
	return out, err
}

// ApplicationView is the owner-facing application.
type ApplicationView struct {
	ID                  ids.ID                `json:"id"`
	PropertyID          ids.ID                `json:"property_id"`
	ListingID           ids.ID                `json:"listing_id"`
	UnitID              *ids.ID               `json:"unit_id"`
	InquiryID           *ids.ID               `json:"inquiry_id"`
	CustomerRefID       ids.ID                `json:"customer_ref_id"`
	Status              string                `json:"status"`
	DesiredMoveIn       *string               `json:"desired_move_in"`
	TermMonths          *int32                `json:"term_months"`
	Occupants           *int32                `json:"occupants"`
	HasPets             *bool                 `json:"has_pets"`
	ScreeningPolicy     *ScreeningPolicyView  `json:"screening_policy"`
	Participants        []ParticipantView     `json:"participants"`
	Evidence            []EvidenceGrantView   `json:"evidence"`
	ScreeningResults    []ScreeningResultView `json:"screening_results"`
	Decisions           []DecisionView        `json:"decisions"`
	Offers              []OfferView           `json:"offers"`
	SubmittedAt         *time.Time            `json:"submitted_at"`
	ExpiresAt           *time.Time            `json:"expires_at"`
	AllowedNextStatuses []string              `json:"allowed_next_statuses"`
	Version             int64                 `json:"version"`
	CreatedAt           time.Time             `json:"created_at"`
}

// ParticipantView is an applicant/co-applicant/guarantor/occupant.
type ParticipantView struct {
	ID                         ids.ID  `json:"id"`
	Role                       string  `json:"role"`
	CustomerRefID              *ids.ID `json:"customer_ref_id"`
	DisplayName                string  `json:"display_name"`
	Relationship               *string `json:"relationship"`
	EmploymentStatus           *string `json:"employment_status"`
	DeclaredMonthlyIncomeMinor *int64  `json:"declared_monthly_income_minor"`
	IncomeCurrency             *string `json:"income_currency"`
	IdentityVerified           bool    `json:"identity_verification_linked"`
}

// EvidenceGrantView is an evidence request/grant (no document content).
type EvidenceGrantView struct {
	ID            ids.ID     `json:"id"`
	ParticipantID *ids.ID    `json:"participant_id"`
	EvidenceType  string     `json:"evidence_type"`
	Purpose       string     `json:"purpose"`
	Status        string     `json:"status"`
	RequestNote   *string    `json:"request_note"`
	DueAt         *time.Time `json:"due_at"`
	GrantedAt     *time.Time `json:"granted_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	AccessCount   int32      `json:"access_count"`
}

// ScreeningResultView is one recorded screening outcome.
type ScreeningResultView struct {
	Criterion  string    `json:"criterion"`
	Outcome    string    `json:"outcome"`
	Source     string    `json:"source"`
	Note       *string   `json:"note"`
	RecordedAt time.Time `json:"recorded_at"`
}

// DecisionView is one human decision.
type DecisionView struct {
	Decision          string    `json:"decision"`
	ReasonCodes       []string  `json:"reason_codes"`
	Conditions        []string  `json:"conditions"`
	Note              *string   `json:"note"`
	ScreeningPolicyID *ids.ID   `json:"screening_policy_id"`
	PolicyVersion     string    `json:"policy_version"`
	HumanReview       bool      `json:"human_review"`
	DecidedAt         time.Time `json:"decided_at"`
}

func (s *Service) applicationView(ctx context.Context, a db.RentalApplication) (ApplicationView, error) {
	q := s.dbq(ctx)
	v := ApplicationView{ID: a.ID, PropertyID: a.PropertyID, ListingID: a.ListingID, UnitID: a.UnitID, InquiryID: a.InquiryID, CustomerRefID: a.CustomerRefID,
		Status: a.Status, DesiredMoveIn: dateStr(a.DesiredMoveIn), TermMonths: a.TermMonths, Occupants: a.Occupants, HasPets: a.HasPets,
		Participants: []ParticipantView{}, Evidence: []EvidenceGrantView{}, ScreeningResults: []ScreeningResultView{}, Decisions: []DecisionView{},
		Offers: []OfferView{}, SubmittedAt: a.SubmittedAt, ExpiresAt: a.ExpiresAt, AllowedNextStatuses: nonNil(domain.Application.Targets(a.Status)),
		Version: a.Version, CreatedAt: a.CreatedAt}
	if a.ScreeningPolicyID != nil {
		if p, err := s.q.GetScreeningPolicy(ctx, q, *a.ScreeningPolicyID); err == nil {
			sv := screeningView(p)
			v.ScreeningPolicy = &sv
		}
	}
	parts, err := s.q.ListParticipants(ctx, q, a.ID)
	if err != nil {
		return v, err
	}
	for _, p := range parts {
		v.Participants = append(v.Participants, ParticipantView{ID: p.ID, Role: p.Role, CustomerRefID: p.CustomerRefID, DisplayName: p.DisplayName,
			Relationship: p.Relationship, EmploymentStatus: p.EmploymentStatus, DeclaredMonthlyIncomeMinor: p.DeclaredMonthlyIncomeMinor,
			IncomeCurrency: p.IncomeCurrency, IdentityVerified: p.IdentityVerificationID != nil})
	}
	grants, err := s.q.ListEvidenceGrants(ctx, q, a.ID)
	if err != nil {
		return v, err
	}
	for _, g := range grants {
		v.Evidence = append(v.Evidence, evidenceView(g))
	}
	results, err := s.q.ListScreeningResults(ctx, q, a.ID)
	if err != nil {
		return v, err
	}
	for _, r := range results {
		v.ScreeningResults = append(v.ScreeningResults, ScreeningResultView{Criterion: r.Criterion, Outcome: r.Outcome, Source: r.Source, Note: r.Note, RecordedAt: r.RecordedAt})
	}
	decs, err := s.q.ListApplicationDecisions(ctx, q, a.ID)
	if err != nil {
		return v, err
	}
	for _, d := range decs {
		v.Decisions = append(v.Decisions, DecisionView{Decision: d.Decision, ReasonCodes: nonNil(d.ReasonCodes), Conditions: nonNil(d.Conditions), Note: d.Note,
			ScreeningPolicyID: d.ScreeningPolicyID, PolicyVersion: d.PolicyVersion, HumanReview: d.HumanReview, DecidedAt: d.DecidedAt})
	}
	offers, err := s.q.ListOffersForApplication(ctx, q, a.ID)
	if err != nil {
		return v, err
	}
	for _, o := range offers {
		v.Offers = append(v.Offers, offerView(o))
	}
	return v, nil
}

func evidenceView(g db.ApplicationEvidenceGrant) EvidenceGrantView {
	return EvidenceGrantView{ID: g.ID, ParticipantID: g.ParticipantID, EvidenceType: g.EvidenceType, Purpose: g.Purpose, Status: g.Status,
		RequestNote: g.RequestNote, DueAt: g.DueAt, GrantedAt: g.GrantedAt, ExpiresAt: g.ExpiresAt, AccessCount: g.AccessCount}
}

func (s *Service) applicationFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.RentalApplication, authz.Decision, error) {
	a, err := s.q.GetApplication(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return a, authz.Decision{}, notFound("application")
	}
	if err != nil {
		return a, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, a.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound("application")
	}
	if err == nil && action != authz.Read(authz.CapApplicationReview) {
		a, err = s.q.GetApplicationForUpdate(ctx, mustTx(ctx), id)
	}
	return a, d, err
}

// ApplicationEvent is application.updated.v1 (customer-facing state only:
// never screening results, notes or evidence).
type ApplicationEvent struct {
	SchemaVersion        int       `json:"schema_version"`
	ApplicationID        ids.ID    `json:"application_id"`
	ListingID            ids.ID    `json:"listing_id"`
	PropertyID           ids.ID    `json:"property_id"`
	CustomerRefID        ids.ID    `json:"customer_ref_id"`
	Status               string    `json:"status"`
	PreviousStatus       string    `json:"previous_status"`
	OpenEvidenceRequests int64     `json:"open_evidence_requests"`
	ApplicationVersion   int64     `json:"application_version"`
	OccurredAt           time.Time `json:"occurred_at"`
}

func (s *Service) emitApplication(ctx context.Context, id ids.ID, previous string) error {
	tx := mustTx(ctx)
	a, err := s.q.GetApplication(ctx, tx, id)
	if err != nil {
		return err
	}
	open, err := s.q.CountOpenEvidenceRequests(ctx, tx, id)
	if err != nil {
		return err
	}
	return s.publish(ctx, "application.updated.v1", AggApplication, id, ApplicationEvent{SchemaVersion: 1, ApplicationID: id, ListingID: a.ListingID,
		PropertyID: a.PropertyID, CustomerRefID: a.CustomerRefID, Status: a.Status, PreviousStatus: previous, OpenEvidenceRequests: open,
		ApplicationVersion: a.Version, OccurredAt: s.now()})
}

func (s *Service) applicationTransition(ctx context.Context, a db.RentalApplication, to, cmd, reason string) error {
	if _, _, err := s.transition(ctx, domain.Application, a.ID, lifecycle.Change{To: to, Command: cmd, Reason: reason}); err != nil {
		return err
	}
	return s.emitApplication(ctx, a.ID, a.Status)
}

// GetApplication returns the full application for review.
func (s *Service) GetApplication(ctx context.Context, subj authz.Subject, id ids.ID) (ApplicationView, error) {
	a, _, err := s.applicationFor(ctx, subj, id, authz.Read(authz.CapApplicationReview))
	if err != nil {
		return ApplicationView{}, err
	}
	return s.applicationView(ctx, a)
}

// ListApplications pages applications across the caller's properties.
func (s *Service) ListApplications(ctx context.Context, subj authz.Subject, listingID ids.ID, status, cursor string, limit int) (pagination.Response[ApplicationView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[ApplicationView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[ApplicationView]{}, err
	}
	rows, err := s.q.ListApplicationsForAccount(ctx, s.dbq(ctx), db.ListApplicationsForAccountParams{AccountID: accountOf(subj), Now: s.now(),
		ListingID: idp(listingID), Status: strp(status), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[ApplicationView]{}, err
	}
	out := make([]ApplicationView, 0, len(rows))
	for _, r := range rows {
		v, err := s.applicationView(ctx, r)
		if err != nil {
			return pagination.Response[ApplicationView]{}, err
		}
		out = append(out, v)
	}
	return pagination.Build(out, limit, func(v ApplicationView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

// StartReview moves a submitted/waitlisted/appealed application into review.
func (s *Service) StartReview(ctx context.Context, subj authz.Subject, id ids.ID) (ApplicationView, error) {
	var out ApplicationView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, _, err := s.applicationFor(ctx, subj, id, authz.Do(authz.CapApplicationReview))
		if err != nil {
			return err
		}
		to := "UNDER_REVIEW"
		if a.Status == "APPEAL" {
			to = "HUMAN_REVIEW"
		}
		if err := s.applicationTransition(ctx, a, to, "start_review", ""); err != nil {
			return err
		}
		na, err := s.q.GetApplication(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.applicationView(ctx, na)
		return err
	})
	return out, err
}

// ensureReview moves SUBMITTED/RECEIVED into UNDER_REVIEW implicitly.
func (s *Service) ensureReview(ctx context.Context, a db.RentalApplication) (db.RentalApplication, error) {
	if a.Status != "SUBMITTED" && a.Status != "RECEIVED" {
		return a, nil
	}
	if err := s.applicationTransition(ctx, a, "UNDER_REVIEW", "start_review", ""); err != nil {
		return a, err
	}
	return s.q.GetApplicationForUpdate(ctx, mustTx(ctx), a.ID)
}

// EvidenceRequestItem asks for one piece of evidence.
type EvidenceRequestItem struct {
	ParticipantID *ids.ID `json:"participant_id,omitempty"`
	EvidenceType  string  `json:"evidence_type" v:"required,enum=PROOF_OF_INCOME|EMPLOYMENT_LETTER|BANK_STATEMENT|ID_DOCUMENT|REFERENCE_LETTER|GUARANTOR_FORM|PREVIOUS_LANDLORD_REFERENCE|OTHER_ALTERNATIVE_EVIDENCE"`
	Note          *string `json:"note,omitempty" v:"max=1000" doc:"What is needed; alternative evidence is always acceptable"`
	DueInDays     int32   `json:"due_in_days,omitempty" v:"min=1,max=30"`
}

// RequestInfoInput requests additional information.
type RequestInfoInput struct {
	Items []EvidenceRequestItem `json:"items" v:"required,max=10"`
}

// RequestInfo opens evidence requests (UNDER_REVIEW → INFO_REQUESTED).
func (s *Service) RequestInfo(ctx context.Context, subj authz.Subject, id ids.ID, in RequestInfoInput) (ApplicationView, error) {
	if err := schema.Validate(in); err != nil {
		return ApplicationView{}, err
	}
	var out ApplicationView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, d, err := s.applicationFor(ctx, subj, id, authz.Do(authz.CapApplicationReview))
		if err != nil {
			return err
		}
		if a, err = s.ensureReview(ctx, a); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		parts, err := s.q.ListParticipants(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, it := range in.Items {
			if it.ParticipantID != nil && !slices.ContainsFunc(parts, func(p db.ApplicationParticipant) bool { return p.ID == *it.ParticipantID }) {
				return apperrors.InvalidRequest("participant_id does not belong to the application.")
			}
			days := it.DueInDays
			if days == 0 {
				days = 7
			}
			due := now.Add(time.Duration(days) * 24 * time.Hour)
			if _, err := s.q.InsertEvidenceGrant(ctx, tx, db.InsertEvidenceGrantParams{ID: ids.New(), ApplicationID: id, ParticipantID: it.ParticipantID,
				EvidenceType: it.EvidenceType, Purpose: "application-review:" + id.String(), Status: "REQUESTED", RequestNote: it.Note, DueAt: &due,
				CreatedAt: now}); err != nil {
				return err
			}
		}
		if err := s.applicationTransition(ctx, a, "INFO_REQUESTED", "request_info", ""); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, a.PropertyID, "APPLICATION_INFO_REQUESTED", "application", id, itoa(len(in.Items))+" items"); err != nil {
			return err
		}
		if err := s.record(ctx, "application.request_info", "application", id, map[string]any{"items": len(in.Items)}, nil); err != nil {
			return err
		}
		na, err := s.q.GetApplication(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.applicationView(ctx, na)
		return err
	})
	return out, err
}

// ScreeningInput records the owner's assessment of criteria.
type ScreeningInput struct {
	Results []struct {
		Criterion string  `json:"criterion" v:"required,enum=MIN_INCOME_MULTIPLE|MAX_OCCUPANTS|PETS_ALLOWED|REFERENCES_REQUIRED|GUARANTOR_REQUIRED|INCOME_EVIDENCE_REQUIRED|IDENTITY_VERIFICATION_REQUIRED"`
		Outcome   string  `json:"outcome" v:"required,enum=MET|NOT_MET|UNKNOWN|WAIVED"`
		Source    string  `json:"source" v:"required,enum=APPLICANT_DECLARED|EVIDENCE_REVIEWED|OWNER_ASSESSED"`
		Note      *string `json:"note,omitempty" v:"max=500"`
	} `json:"results" v:"required,max=10"`
}

// RecordScreening appends screening results against the application's policy.
func (s *Service) RecordScreening(ctx context.Context, subj authz.Subject, id ids.ID, in ScreeningInput) (ApplicationView, error) {
	if err := schema.Validate(in); err != nil {
		return ApplicationView{}, err
	}
	var out ApplicationView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, _, err := s.applicationFor(ctx, subj, id, authz.Do(authz.CapApplicationReview))
		if err != nil {
			return err
		}
		if a.ScreeningPolicyID == nil {
			return precondition("NO_SCREENING_POLICY", "The application was submitted without a screening policy.")
		}
		if a, err = s.ensureReview(ctx, a); err != nil {
			return err
		}
		pol, err := s.q.GetScreeningPolicy(ctx, mustTx(ctx), *a.ScreeningPolicyID)
		if err != nil {
			return err
		}
		crit := screeningView(pol).Criteria
		dom, actor := s.actor(ctx, subj, a.PropertyID)
		for _, r := range in.Results {
			if !slices.ContainsFunc(crit, func(c Criterion) bool { return c.Type == r.Criterion }) {
				return apperrors.InvalidRequest("Criterion " + r.Criterion + " is not part of the application's screening policy.")
			}
			if err := s.q.InsertScreeningResult(ctx, mustTx(ctx), db.InsertScreeningResultParams{ID: ids.New(), ApplicationID: id, PolicyID: pol.ID,
				Criterion: r.Criterion, Outcome: r.Outcome, Source: r.Source, Note: r.Note, ActorDomain: &dom, ActorID: &actor, RecordedAt: s.now()}); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "application.screening", "application", id, map[string]any{"results": len(in.Results)}, nil); err != nil {
			return err
		}
		na, err := s.q.GetApplication(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.applicationView(ctx, na)
		return err
	})
	return out, err
}

// DecisionInput is a human decision.
type DecisionInput struct {
	Decision    string   `json:"decision" v:"required,enum=APPROVED|CONDITIONAL|DECLINED|WAITLISTED"`
	ReasonCodes []string `json:"reason_codes,omitempty" v:"max=5" doc:"Required for DECLINED: configured lawful reasons only"`
	Conditions  []string `json:"conditions,omitempty" v:"max=10" doc:"Required for CONDITIONAL"`
	Note        *string  `json:"note,omitempty" v:"max=2000" doc:"Required with OTHER_LAWFUL_REASON"`
}

// DecideApplication records a human decision. Automated screening flags never
// decide; the decision captures the screening policy version in force.
func (s *Service) DecideApplication(ctx context.Context, subj authz.Subject, id ids.ID, in DecisionInput) (ApplicationView, error) {
	if err := schema.Validate(in); err != nil {
		return ApplicationView{}, err
	}
	switch in.Decision {
	case "DECLINED":
		if len(in.ReasonCodes) == 0 {
			return ApplicationView{}, apperrors.InvalidRequest("A decline needs at least one reason code.")
		}
		for _, r := range in.ReasonCodes {
			if !slices.Contains(s.Policy.Domain.DeclineReasons, r) {
				return ApplicationView{}, apperrors.Unprocessable("DECLINE_REASON_NOT_CONFIGURED", "Decline reason "+r+" is not a configured lawful reason.")
			}
			if r == "OTHER_LAWFUL_REASON" && (in.Note == nil || len(*in.Note) < 10) {
				return ApplicationView{}, apperrors.InvalidRequest("OTHER_LAWFUL_REASON needs an explanatory note.")
			}
		}
	case "CONDITIONAL":
		if len(in.Conditions) == 0 {
			return ApplicationView{}, apperrors.InvalidRequest("A conditional approval needs conditions.")
		}
	}
	var out ApplicationView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, d, err := s.applicationFor(ctx, subj, id, authz.Do(authz.CapApplicationDecide))
		if err != nil {
			return err
		}
		if a, err = s.ensureReview(ctx, a); err != nil {
			return err
		}
		human := a.Status == "HUMAN_REVIEW"
		dom, actor := s.actor(ctx, subj, a.PropertyID)
		if _, err := s.q.InsertApplicationDecision(ctx, mustTx(ctx), db.InsertApplicationDecisionParams{ID: ids.New(), ApplicationID: id, Decision: in.Decision,
			ReasonCodes: nonNil(in.ReasonCodes), Conditions: nonNil(in.Conditions), Note: in.Note, ScreeningPolicyID: a.ScreeningPolicyID,
			PolicyVersion: s.Policy.Version, HumanReview: human, ActorDomain: dom, ActorID: actor, DecidedAt: s.now()}); err != nil {
			return err
		}
		if err := s.applicationTransition(ctx, a, in.Decision, "decide", firstOr(in.ReasonCodes, "")); err != nil {
			return err
		}
		if in.Decision == "DECLINED" {
			if err := s.q.RevokeApplicationGrants(ctx, mustTx(ctx), db.RevokeApplicationGrantsParams{ApplicationID: id, RevokedAt: ptr(s.now())}); err != nil {
				return err
			}
		}
		if err := s.noteDelegateActivity(ctx, d, a.PropertyID, "APPLICATION_"+in.Decision, "application", id, firstOr(in.ReasonCodes, in.Decision)); err != nil {
			return err
		}
		if err := s.record(ctx, "application.decide", "application", id, map[string]any{"decision": in.Decision, "reason_codes": in.ReasonCodes,
			"screening_policy_id": a.ScreeningPolicyID, "human_review": human}, nil); err != nil {
			return err
		}
		na, err := s.q.GetApplication(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.applicationView(ctx, na)
		return err
	})
	return out, err
}

func firstOr(ss []string, def string) string {
	if len(ss) > 0 {
		return ss[0]
	}
	return def
}

// EvidenceDownload is a short-lived media-service URL for one evidence grant.
type EvidenceDownload struct {
	GrantID   ids.ID    `json:"grant_id"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// EvidenceDownloadURL issues a media-service download URL while the grant is
// valid; every access is counted and audited.
func (s *Service) EvidenceDownloadURL(ctx context.Context, subj authz.Subject, appID, grantID ids.ID) (EvidenceDownload, error) {
	if s.Media == nil {
		return EvidenceDownload{}, unavailable("Evidence access", "media-document-service")
	}
	var g db.ApplicationEvidenceGrant
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, _, err := s.applicationFor(ctx, subj, appID, authz.Do(authz.CapApplicationReview)); err != nil {
			return err
		}
		var err error
		g, err = s.q.GetEvidenceGrantForUpdate(ctx, mustTx(ctx), db.GetEvidenceGrantForUpdateParams{ID: grantID, ApplicationID: appID})
		if isNoRows(err) {
			return notFound("evidence_grant")
		}
		if err != nil {
			return err
		}
		if g.Status != "GRANTED" || g.MediaID == nil || (g.ExpiresAt != nil && !g.ExpiresAt.After(s.now())) {
			return conflict("EVIDENCE_NOT_ACCESSIBLE", "The evidence grant is not active.")
		}
		if err := s.q.TouchEvidenceGrant(ctx, mustTx(ctx), db.TouchEvidenceGrantParams{ID: grantID, LastAccessedAt: ptr(s.now())}); err != nil {
			return err
		}
		return s.record(ctx, "application.evidence.access", "application", appID, map[string]any{"grant_id": grantID, "evidence_type": g.EvidenceType},
			map[string]any{"privileged_read": true})
	})
	if err != nil {
		return EvidenceDownload{}, err
	}
	dl, err := s.Media.DownloadURL(ctx, *g.MediaID, g.Purpose)
	if err != nil {
		return EvidenceDownload{}, downstream("media-document-service", err)
	}
	return EvidenceDownload{GrantID: grantID, URL: dl.URL, ExpiresAt: dl.ExpiresAt}, nil
}

// ---- rental offers ----

// OfferView is a rental offer.
type OfferView struct {
	ID               ids.ID          `json:"id"`
	ApplicationID    ids.ID          `json:"application_id"`
	ListingID        ids.ID          `json:"listing_id"`
	CustomerRefID    ids.ID          `json:"customer_ref_id"`
	Status           string          `json:"status"`
	PricingVersionID ids.ID          `json:"pricing_version_id" doc:"Frozen when accepted"`
	Cost             json.RawMessage `json:"cost" doc:"All-in cost computed by the shared calculator"`
	StartDate        string          `json:"start_date"`
	TermMonths       int32           `json:"term_months"`
	Conditions       []string        `json:"conditions"`
	ExpiresAt        time.Time       `json:"expires_at"`
	SentAt           *time.Time      `json:"sent_at"`
	RespondedAt      *time.Time      `json:"responded_at"`
	Version          int64           `json:"version"`
	CreatedAt        time.Time       `json:"created_at"`
}

func offerView(o db.RentalOffer) OfferView {
	return OfferView{ID: o.ID, ApplicationID: o.ApplicationID, ListingID: o.ListingID, CustomerRefID: o.CustomerRefID, Status: o.Status,
		PricingVersionID: o.PricingVersionID, Cost: o.CostSnapshot, StartDate: o.StartDate.Format(time.DateOnly), TermMonths: o.TermMonths,
		Conditions: nonNil(o.Conditions), ExpiresAt: o.ExpiresAt, SentAt: o.SentAt, RespondedAt: o.RespondedAt, Version: o.Version, CreatedAt: o.CreatedAt}
}

// OfferInput creates or edits a DRAFT offer.
type OfferInput struct {
	StartDate     string   `json:"start_date" v:"required,format=date"`
	TermMonths    int32    `json:"term_months" v:"required,min=1,max=600"`
	Conditions    []string `json:"conditions,omitempty" v:"max=10"`
	ExpiresInDays int32    `json:"expires_in_days,omitempty" v:"min=1,max=30" doc:"Default per policy (7)"`
}

func (s *Service) offerCost(ctx context.Context, l db.Listing, term int32) (ids.ID, []byte, error) {
	c, err := s.costPreview(ctx, l, CostPreviewInput{TermMonths: term})
	if err != nil {
		return ids.Nil, nil, err
	}
	raw, _ := json.Marshal(c.Cost)
	return c.PricingVersionID, raw, nil
}

// CreateOffer drafts an offer for an approved application using the active price.
func (s *Service) CreateOffer(ctx context.Context, subj authz.Subject, appID ids.ID, in OfferInput) (OfferView, error) {
	if err := schema.Validate(in); err != nil {
		return OfferView{}, err
	}
	var out OfferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, _, err := s.applicationFor(ctx, subj, appID, authz.Do(authz.CapOffersManage))
		if err != nil {
			return err
		}
		if a.Status != "APPROVED" && a.Status != "CONDITIONAL" {
			return conflict("APPLICATION_NOT_APPROVED", "Offers follow an approved (or conditionally approved) application.")
		}
		tx, now := mustTx(ctx), s.now()
		l, err := s.q.GetListing(ctx, tx, a.ListingID)
		if err != nil {
			return err
		}
		pvID, cost, err := s.offerCost(ctx, l, in.TermMonths)
		if err != nil {
			return err
		}
		days := in.ExpiresInDays
		exp := now.Add(s.Policy.Domain.OfferTTL)
		if days > 0 {
			exp = now.Add(time.Duration(days) * 24 * time.Hour)
		}
		o, err := s.q.InsertRentalOffer(ctx, tx, db.InsertRentalOfferParams{ID: ids.New(), ApplicationID: appID, PropertyID: a.PropertyID, ListingID: a.ListingID,
			UnitID: a.UnitID, CustomerRefID: a.CustomerRefID, PricingVersionID: pvID, CostSnapshot: cost, StartDate: *schema.ParseDate(&in.StartDate),
			TermMonths: in.TermMonths, Conditions: nonNil(in.Conditions), ExpiresAt: exp, CreatedAt: now})
		if isUnique(err, "rental_offers_live_uniq") {
			return conflict("OFFER_ALREADY_OPEN", "The application already has an open offer.")
		}
		if err != nil {
			return err
		}
		if err := domain.RentalOffer.RecordCreation(ctx, tx, o.ID, &o.PropertyID, "DRAFT", now); err != nil {
			return err
		}
		out = offerView(o)
		return s.record(ctx, "offer.create", AggOffer, o.ID, map[string]any{"pricing_version_id": pvID, "term_months": in.TermMonths}, nil)
	})
	return out, err
}

func (s *Service) offerFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.RentalOffer, authz.Decision, error) {
	o, err := s.q.GetRentalOffer(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return o, authz.Decision{}, notFound(AggOffer)
	}
	if err != nil {
		return o, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, o.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggOffer)
	}
	if err == nil && action != authz.Read(authz.CapOffersManage) {
		o, err = s.q.GetRentalOfferForUpdate(ctx, mustTx(ctx), id)
	}
	return o, d, err
}

// GetOffer returns an offer.
func (s *Service) GetOffer(ctx context.Context, subj authz.Subject, id ids.ID) (OfferView, error) {
	o, _, err := s.offerFor(ctx, subj, id, authz.Read(authz.CapOffersManage))
	return offerView(o), err
}

// UpdateOffer edits a DRAFT offer (If-Match); terms are re-priced.
func (s *Service) UpdateOffer(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in OfferInput) (OfferView, error) {
	if err := schema.Validate(in); err != nil {
		return OfferView{}, err
	}
	var out OfferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		o, _, err := s.offerFor(ctx, subj, id, authz.Do(authz.CapOffersManage))
		if err != nil {
			return err
		}
		if o.Status != "DRAFT" {
			return conflict("OFFER_NOT_EDITABLE", "Only draft offers can be edited.")
		}
		l, err := s.q.GetListing(ctx, mustTx(ctx), o.ListingID)
		if err != nil {
			return err
		}
		_, cost, err := s.offerCost(ctx, l, in.TermMonths)
		if err != nil {
			return err
		}
		exp := o.ExpiresAt
		if in.ExpiresInDays > 0 {
			exp = s.now().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		}
		no, err := s.q.UpdateRentalOfferTerms(ctx, mustTx(ctx), db.UpdateRentalOfferTermsParams{ID: id, Version: expected, StartDate: *schema.ParseDate(&in.StartDate),
			TermMonths: in.TermMonths, Conditions: nonNil(in.Conditions), ExpiresAt: exp, CostSnapshot: cost, UpdatedAt: s.now()})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		out = offerView(no)
		return s.record(ctx, "offer.update", AggOffer, id, map[string]any{"term_months": in.TermMonths}, nil)
	})
	return out, err
}

// RentalOfferEvent is rental_offer.updated.v1.
type RentalOfferEvent struct {
	SchemaVersion    int             `json:"schema_version"`
	OfferID          ids.ID          `json:"offer_id"`
	ApplicationID    ids.ID          `json:"application_id"`
	ListingID        ids.ID          `json:"listing_id"`
	PropertyID       ids.ID          `json:"property_id"`
	CustomerRefID    ids.ID          `json:"customer_ref_id"`
	Status           string          `json:"status"`
	PreviousStatus   string          `json:"previous_status"`
	PricingVersionID ids.ID          `json:"pricing_version_id"`
	Cost             json.RawMessage `json:"cost"`
	StartDate        string          `json:"start_date"`
	TermMonths       int32           `json:"term_months"`
	Conditions       []string        `json:"conditions"`
	ExpiresAt        time.Time       `json:"expires_at"`
	OfferVersion     int64           `json:"offer_version"`
	OccurredAt       time.Time       `json:"occurred_at"`
}

func (s *Service) offerTransition(ctx context.Context, o db.RentalOffer, to, cmd, reason string) error {
	if _, _, err := s.transition(ctx, domain.RentalOffer, o.ID, lifecycle.Change{To: to, Command: cmd, Reason: reason}); err != nil {
		return err
	}
	n, err := s.q.GetRentalOffer(ctx, mustTx(ctx), o.ID)
	if err != nil {
		return err
	}
	return s.publish(ctx, "rental_offer.updated.v1", AggOffer, o.ID, RentalOfferEvent{SchemaVersion: 1, OfferID: n.ID, ApplicationID: n.ApplicationID,
		ListingID: n.ListingID, PropertyID: n.PropertyID, CustomerRefID: n.CustomerRefID, Status: n.Status, PreviousStatus: o.Status,
		PricingVersionID: n.PricingVersionID, Cost: n.CostSnapshot, StartDate: n.StartDate.Format(time.DateOnly), TermMonths: n.TermMonths,
		Conditions: nonNil(n.Conditions), ExpiresAt: n.ExpiresAt, OfferVersion: n.Version, OccurredAt: s.now()})
}

// SendOffer sends a draft offer to the applicant.
func (s *Service) SendOffer(ctx context.Context, subj authz.Subject, id ids.ID) (OfferView, error) {
	var out OfferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		o, d, err := s.offerFor(ctx, subj, id, authz.Do(authz.CapOffersManage))
		if err != nil {
			return err
		}
		if !o.ExpiresAt.After(s.now()) {
			return conflict("OFFER_EXPIRED", "Set a new expiry before sending.")
		}
		// The price must still be the one the offer was computed from.
		l, err := s.q.GetListing(ctx, mustTx(ctx), o.ListingID)
		if err != nil {
			return err
		}
		if pv, _, err := s.activePricing(ctx, l); err != nil {
			return err
		} else if pv == nil || pv.PricingVersionID != o.PricingVersionID {
			return conflict("OFFER_PRICE_STALE", "The listing price changed; edit the offer to re-price it.")
		}
		if err := s.offerTransition(ctx, o, "SENT", "send", ""); err != nil {
			return err
		}
		if err := s.q.SetRentalOfferTimes(ctx, mustTx(ctx), db.SetRentalOfferTimesParams{ID: id, SentAt: ptr(s.now()), UpdatedAt: s.now()}); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, o.PropertyID, "OFFER_SENT", AggOffer, id, "Rental offer sent"); err != nil {
			return err
		}
		a, err := s.q.GetApplication(ctx, mustTx(ctx), o.ApplicationID)
		if err == nil {
			err = s.advanceInquiry(ctx, a.InquiryID, "OFFER")
		}
		if err != nil {
			return err
		}
		n, err := s.q.GetRentalOffer(ctx, mustTx(ctx), id)
		out = offerView(n)
		if err != nil {
			return err
		}
		return s.record(ctx, "offer.send", AggOffer, id, nil, nil)
	})
	return out, err
}

// WithdrawOffer withdraws a draft or sent offer.
func (s *Service) WithdrawOffer(ctx context.Context, subj authz.Subject, id ids.ID, in ReasonCodeInput) (OfferView, error) {
	if err := schema.Validate(in); err != nil {
		return OfferView{}, err
	}
	var out OfferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		o, _, err := s.offerFor(ctx, subj, id, authz.Do(authz.CapOffersManage))
		if err != nil {
			return err
		}
		if err := s.offerTransition(ctx, o, "WITHDRAWN", "withdraw", in.ReasonCode); err != nil {
			return err
		}
		if err := s.q.SetRentalOfferTimes(ctx, mustTx(ctx), db.SetRentalOfferTimesParams{ID: id, WithdrawReason: &in.ReasonCode, UpdatedAt: s.now()}); err != nil {
			return err
		}
		n, err := s.q.GetRentalOffer(ctx, mustTx(ctx), id)
		out = offerView(n)
		if err != nil {
			return err
		}
		return s.record(ctx, "offer.withdraw", AggOffer, id, map[string]any{"reason_code": in.ReasonCode}, nil)
	})
	return out, err
}

// offerAcceptedHooks let later contexts react (e.g. prepare the agreement).
var offerAcceptedHooks []func(s *Service, ctx context.Context, o db.RentalOffer) error

// ---- marketplace (customer actor) ----

// CustomerApplicationInput submits an application for a published listing.
type CustomerApplicationInput struct {
	ListingID     ids.ID  `json:"listing_id" v:"required"`
	InquiryID     *ids.ID `json:"inquiry_id,omitempty"`
	DesiredMoveIn *string `json:"desired_move_in,omitempty" v:"format=date"`
	TermMonths    *int32  `json:"term_months,omitempty" v:"min=1,max=600"`
	Occupants     *int32  `json:"occupants,omitempty" v:"min=1,max=100"`
	HasPets       *bool   `json:"has_pets,omitempty"`
	Participants  []struct {
		Role                       string  `json:"role" v:"required,enum=PRIMARY|CO_APPLICANT|GUARANTOR|OCCUPANT"`
		CustomerRefID              *ids.ID `json:"customer_ref_id,omitempty"`
		DisplayName                string  `json:"display_name" v:"required,max=120"`
		Relationship               *string `json:"relationship,omitempty" v:"enum=SELF|SPOUSE_PARTNER|FAMILY|FLATMATE|EMPLOYER|GUARANTOR|OTHER"`
		EmploymentStatus           *string `json:"employment_status,omitempty" v:"enum=EMPLOYED|SELF_EMPLOYED|STUDENT|RETIRED|UNEMPLOYED|OTHER"`
		DeclaredMonthlyIncomeMinor *int64  `json:"declared_monthly_income_minor,omitempty" v:"min=0"`
		IncomeCurrency             *string `json:"income_currency,omitempty" v:"format=currency"`
		IdentityVerificationID     *ids.ID `json:"identity_verification_id,omitempty" doc:"Customer-domain verification reference (never the document)"`
	} `json:"participants" v:"required,max=10"`
	Consent bool `json:"consent" doc:"Applicant consent to processing for this application (required)"`
}

// CustomerSubmitApplication records a marketplace application.
func (s *Service) CustomerSubmitApplication(ctx context.Context, subj authz.Subject, in CustomerApplicationInput) (ApplicationView, error) {
	if err := schema.Validate(in); err != nil {
		return ApplicationView{}, err
	}
	if !in.Consent {
		return ApplicationView{}, precondition("CONSENT_REQUIRED", "Applicant consent is required.")
	}
	primaries := 0
	for _, p := range in.Participants {
		if p.Role == "PRIMARY" {
			primaries++
		}
	}
	if primaries != 1 {
		return ApplicationView{}, apperrors.InvalidRequest("Exactly one PRIMARY participant is required.")
	}
	var out ApplicationView
	err := s.InTx(ctx, func(ctx context.Context) error {
		customer, err := s.requireCustomer(ctx, subj)
		if err != nil {
			return err
		}
		l, err := s.bookableListing(ctx, in.ListingID, "RENT_LONG_TERM", "COMMERCIAL_LEASE", "LAND_LEASE")
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		var policyID *ids.ID
		if p, err := s.q.LatestScreeningPolicy(ctx, tx, l.PropertyID); err == nil {
			policyID = &p.ID
		} else if !isNoRows(err) {
			return err
		}
		exp := now.Add(s.Policy.Domain.ApplicationTTL)
		a, err := s.q.InsertApplication(ctx, tx, db.InsertApplicationParams{ID: ids.New(), PropertyID: l.PropertyID, ListingID: l.ID, UnitID: l.UnitID,
			InquiryID: in.InquiryID, CustomerRefID: customer, DesiredMoveIn: schema.ParseDate(in.DesiredMoveIn), TermMonths: in.TermMonths, Occupants: in.Occupants,
			HasPets: in.HasPets, ScreeningPolicyID: policyID, SubmittedAt: &now, ExpiresAt: &exp})
		if isUnique(err, "rental_applications_open_uniq") {
			return conflict("APPLICATION_ALREADY_OPEN", "You already have an open application for this listing.")
		}
		if err != nil {
			return err
		}
		var incomeTotal int64
		for _, p := range in.Participants {
			if _, err := s.q.InsertParticipant(ctx, tx, db.InsertParticipantParams{ID: ids.New(), ApplicationID: a.ID, Role: p.Role, CustomerRefID: p.CustomerRefID,
				DisplayName: p.DisplayName, Relationship: p.Relationship, EmploymentStatus: p.EmploymentStatus,
				DeclaredMonthlyIncomeMinor: p.DeclaredMonthlyIncomeMinor, IncomeCurrency: p.IncomeCurrency, IdentityVerificationID: p.IdentityVerificationID,
				CreatedAt: now}); err != nil {
				return err
			}
			if p.Role != "GUARANTOR" && p.Role != "OCCUPANT" {
				incomeTotal += deref(p.DeclaredMonthlyIncomeMinor)
			}
		}
		if err := domain.Application.RecordCreation(ctx, tx, a.ID, &a.PropertyID, "SUBMITTED", now); err != nil {
			return err
		}
		if policyID != nil {
			if err := s.automatedFlags(ctx, a, *policyID, l, incomeTotal); err != nil {
				return err
			}
		}
		if err := s.advanceInquiry(ctx, in.InquiryID, "APPLICATION"); err != nil {
			return err
		}
		if err := s.emitApplication(ctx, a.ID, ""); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateApplicationReceived, map[string]string{"listing_title": l.Title}); err != nil {
			return err
		}
		if err := s.record(ctx, "application.submit", "application", a.ID, map[string]any{"listing_id": l.ID, "participants": len(in.Participants)}, nil); err != nil {
			return err
		}
		out, err = s.applicationView(ctx, a)
		return err
	})
	return out, err
}

// automatedFlags records objective, informational screening flags. They are
// never decisions (source AUTOMATED_FLAG).
func (s *Service) automatedFlags(ctx context.Context, a db.RentalApplication, policyID ids.ID, l db.Listing, income int64) error {
	tx := mustTx(ctx)
	pol, err := s.q.GetScreeningPolicy(ctx, tx, policyID)
	if err != nil {
		return err
	}
	flag := func(c, outcome, note string) error {
		return s.q.InsertScreeningResult(ctx, tx, db.InsertScreeningResultParams{ID: ids.New(), ApplicationID: a.ID, PolicyID: policyID, Criterion: c,
			Outcome: outcome, Source: "AUTOMATED_FLAG", Note: strp(note), RecordedAt: s.now()})
	}
	for _, c := range screeningView(pol).Criteria {
		switch c.Type {
		case "MAX_OCCUPANTS":
			if a.Occupants != nil {
				outcome := "MET"
				if *a.Occupants > *c.IntValue {
					outcome = "NOT_MET"
				}
				if err := flag(c.Type, outcome, "declared occupants vs policy"); err != nil {
					return err
				}
			}
		case "PETS_ALLOWED":
			if a.HasPets != nil && *a.HasPets && c.BoolValue != nil && !*c.BoolValue {
				if err := flag(c.Type, "NOT_MET", "applicant declared pets"); err != nil {
					return err
				}
			}
		case "MIN_INCOME_MULTIPLE":
			if income == 0 {
				continue
			}
			pv, m, err := s.activePricing(ctx, l)
			if err != nil || pv == nil {
				return err
			}
			cost, err := domain.Calculate(*m, domain.Scenario{TermMonths: max(deref(a.TermMonths), m.MinTermMonths)})
			if err != nil || cost.CycleCount == 0 {
				continue
			}
			monthly := cost.TermTotal / int64(max(cost.TermMonths, 1))
			outcome := "MET"
			if income*10000 < monthly*int64(*c.IntValue) {
				outcome = "NOT_MET"
			}
			if err := flag(c.Type, outcome, "declared income vs monthly cost"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) customerApplication(ctx context.Context, subj authz.Subject, id ids.ID) (db.RentalApplication, error) {
	customer, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return db.RentalApplication{}, err
	}
	a, err := s.q.GetApplicationForUpdate(ctx, mustTx(ctx), id)
	if isNoRows(err) || (err == nil && a.CustomerRefID != customer) {
		return a, notFound("application")
	}
	return a, err
}

// CustomerEvidenceInput fulfils an evidence request or adds alternative evidence.
type CustomerEvidenceInput struct {
	GrantID       *ids.ID `json:"grant_id,omitempty" doc:"Evidence request being fulfilled"`
	ParticipantID *ids.ID `json:"participant_id,omitempty"`
	EvidenceType  string  `json:"evidence_type,omitempty" v:"enum=PROOF_OF_INCOME|EMPLOYMENT_LETTER|BANK_STATEMENT|ID_DOCUMENT|REFERENCE_LETTER|GUARANTOR_FORM|PREVIOUS_LANDLORD_REFERENCE|OTHER_ALTERNATIVE_EVIDENCE"`
	MediaID       ids.ID  `json:"media_id" v:"required" doc:"Customer-domain media; access is enforced by media-document-service"`
}

// CustomerProvideEvidence records a time-limited evidence grant; when no
// requests remain the application returns to review.
func (s *Service) CustomerProvideEvidence(ctx context.Context, subj authz.Subject, appID ids.ID, in CustomerEvidenceInput) (EvidenceGrantView, error) {
	if err := schema.Validate(in); err != nil {
		return EvidenceGrantView{}, err
	}
	var out EvidenceGrantView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, err := s.customerApplication(ctx, subj, appID)
		if err != nil {
			return err
		}
		if !slices.Contains([]string{"SUBMITTED", "UNDER_REVIEW", "INFO_REQUESTED", "WAITLISTED", "APPEAL", "HUMAN_REVIEW"}, a.Status) {
			return conflict("APPLICATION_CLOSED", "The application is "+a.Status+".")
		}
		tx, now := mustTx(ctx), s.now()
		exp := now.Add(s.Policy.Domain.EvidenceGrantTTL)
		var g db.ApplicationEvidenceGrant
		if in.GrantID != nil {
			g, err = s.q.FulfilEvidenceGrant(ctx, tx, db.FulfilEvidenceGrantParams{ID: *in.GrantID, MediaID: &in.MediaID, GrantedAt: &now, ExpiresAt: &exp})
			if isNoRows(err) {
				return conflict("EVIDENCE_REQUEST_NOT_OPEN", "The evidence request is not open.")
			}
			if err == nil && g.ApplicationID != appID {
				return notFound("evidence_grant")
			}
		} else {
			if in.EvidenceType == "" {
				return apperrors.InvalidRequest("evidence_type is required without grant_id.")
			}
			g, err = s.q.InsertEvidenceGrant(ctx, tx, db.InsertEvidenceGrantParams{ID: ids.New(), ApplicationID: appID, ParticipantID: in.ParticipantID,
				EvidenceType: in.EvidenceType, Purpose: "application-review:" + appID.String(), Status: "GRANTED", MediaID: &in.MediaID, GrantedAt: &now,
				ExpiresAt: &exp, CreatedAt: now})
		}
		if err != nil {
			return err
		}
		open, err := s.q.CountOpenEvidenceRequests(ctx, tx, appID)
		if err != nil {
			return err
		}
		if open == 0 && a.Status == "INFO_REQUESTED" {
			if err := s.applicationTransition(ctx, a, "UNDER_REVIEW", "evidence_complete", ""); err != nil {
				return err
			}
		} else if err := s.emitApplication(ctx, appID, a.Status); err != nil {
			return err
		}
		out = evidenceView(g)
		return s.record(ctx, "application.evidence.grant", "application", appID, map[string]any{"evidence_type": g.EvidenceType, "expires_at": exp}, nil)
	})
	return out, err
}

// CustomerApplicationAction is a customer action on their application.
type CustomerApplicationAction struct {
	Action string `json:"action" v:"required,enum=WITHDRAW|APPEAL"`
}

// CustomerActOnApplication withdraws or appeals (DECLINED → APPEAL) an application.
func (s *Service) CustomerActOnApplication(ctx context.Context, subj authz.Subject, appID ids.ID, in CustomerApplicationAction) (ApplicationEvent, error) {
	if err := schema.Validate(in); err != nil {
		return ApplicationEvent{}, err
	}
	var out ApplicationEvent
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, err := s.customerApplication(ctx, subj, appID)
		if err != nil {
			return err
		}
		to := "WITHDRAWN"
		if in.Action == "APPEAL" {
			to = "APPEAL"
		}
		if err := s.applicationTransition(ctx, a, to, "customer_"+lower(in.Action), ""); err != nil {
			return err
		}
		if to == "WITHDRAWN" {
			if err := s.q.RevokeApplicationGrants(ctx, mustTx(ctx), db.RevokeApplicationGrantsParams{ApplicationID: appID, RevokedAt: ptr(s.now())}); err != nil {
				return err
			}
		}
		na, err := s.q.GetApplication(ctx, mustTx(ctx), appID)
		out = ApplicationEvent{SchemaVersion: 1, ApplicationID: na.ID, ListingID: na.ListingID, PropertyID: na.PropertyID, CustomerRefID: na.CustomerRefID,
			Status: na.Status, PreviousStatus: a.Status, ApplicationVersion: na.Version, OccurredAt: s.now()}
		return err
	})
	return out, err
}

// CustomerOfferResponse accepts or declines a sent offer.
type CustomerOfferResponse struct {
	Decision string `json:"decision" v:"required,enum=ACCEPT|DECLINE"`
}

// CustomerRespondOffer: acceptance freezes the pricing version, moves the
// listing to OFFER_ACCEPTED (pausing it) — concurrent acceptances of offers
// on the same listing are serialized by the availability compare-and-swap.
func (s *Service) CustomerRespondOffer(ctx context.Context, subj authz.Subject, offerID ids.ID, in CustomerOfferResponse) (OfferView, error) {
	if err := schema.Validate(in); err != nil {
		return OfferView{}, err
	}
	var out OfferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		customer, err := s.requireCustomer(ctx, subj)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		o, err := s.q.GetRentalOfferForUpdate(ctx, tx, offerID)
		if isNoRows(err) || (err == nil && o.CustomerRefID != customer) {
			return notFound(AggOffer)
		}
		if err != nil {
			return err
		}
		if o.Status == "SENT" && !o.ExpiresAt.After(now) {
			return conflict("OFFER_EXPIRED", "The offer has expired.")
		}
		if in.Decision == "DECLINE" {
			if err := s.offerTransition(ctx, o, "DECLINED", "customer_decline", ""); err != nil {
				return err
			}
		} else {
			if o.Status != "SENT" {
				return apperrors.InvalidStateTransition(o.Status, "ACCEPTED")
			}
			av, err := s.q.GetAvailability(ctx, tx, o.ListingID)
			if err != nil {
				return err
			}
			if !domain.Availability.Can(av.Status, "OFFER_ACCEPTED") {
				return conflict("LISTING_NO_LONGER_AVAILABLE", "The listing is no longer available ("+av.Status+").")
			}
			if _, _, err := s.transition(ctx, domain.Availability, o.ListingID, lifecycle.Change{To: "OFFER_ACCEPTED", Command: "offer_accepted",
				ExpectedVersion: av.Version, Metadata: map[string]any{"offer_id": o.ID}}); err != nil {
				return err
			}
			if _, err := s.q.AcceptPricingVersion(ctx, tx, db.AcceptPricingVersionParams{ID: o.PricingVersionID, AcceptedAt: &now}); err != nil {
				return err
			}
			if err := s.offerTransition(ctx, o, "ACCEPTED", "customer_accept", ""); err != nil {
				return err
			}
			if l, err := s.q.GetListingForUpdate(ctx, tx, o.ListingID); err != nil {
				return err
			} else if l.Status == "PUBLISHED" {
				if err := s.pauseTx(ctx, l, "SYSTEM", "OFFER_ACCEPTED"); err != nil {
					return err
				}
			}
			for _, h := range offerAcceptedHooks {
				if err := h(s, ctx, o); err != nil {
					return err
				}
			}
		}
		if err := s.q.SetRentalOfferTimes(ctx, tx, db.SetRentalOfferTimesParams{ID: offerID, RespondedAt: &now, UpdatedAt: now}); err != nil {
			return err
		}
		l, err := s.q.GetListing(ctx, tx, o.ListingID)
		if err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, o.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateOfferResponded, map[string]string{"listing_title": l.Title, "decision": lower(in.Decision)}); err != nil {
			return err
		}
		if err := s.record(ctx, "offer.respond", AggOffer, offerID, map[string]any{"decision": in.Decision}, nil); err != nil {
			return err
		}
		n, err := s.q.GetRentalOffer(ctx, tx, offerID)
		out = offerView(n)
		return err
	})
	return out, err
}

// RunApplicationDeadlines expires stale applications, offers and evidence
// grants (§11 application deadlines).
func (s *Service) RunApplicationDeadlines(ctx context.Context, batch int32) error {
	now := s.now()
	apps, err := s.q.DueApplicationExpiries(ctx, s.Pool, db.DueApplicationExpiriesParams{ExpiresAt: &now, Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range apps {
		err := s.InTx(ctx, func(ctx context.Context) error {
			a, err := s.q.GetApplicationForUpdate(ctx, mustTx(ctx), id)
			if err != nil {
				return err
			}
			if err := s.applicationTransition(ctx, a, "EXPIRED", "expire", "DEADLINE_PASSED"); err != nil {
				return err
			}
			return s.q.RevokeApplicationGrants(ctx, mustTx(ctx), db.RevokeApplicationGrantsParams{ApplicationID: id, RevokedAt: &now})
		})
		if err != nil && apperrors.CodeOf(err) != apperrors.CodeInvalidStateTransition && apperrors.CodeOf(err) != "VERSION_CONFLICT" {
			return err
		}
	}
	offers, err := s.q.DueOfferExpiries(ctx, s.Pool, db.DueOfferExpiriesParams{ExpiresAt: now, Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range offers {
		err := s.InTx(ctx, func(ctx context.Context) error {
			o, err := s.q.GetRentalOfferForUpdate(ctx, mustTx(ctx), id)
			if err != nil {
				return err
			}
			return s.offerTransition(ctx, o, "EXPIRED", "expire", "DEADLINE_PASSED")
		})
		if err != nil && apperrors.CodeOf(err) != apperrors.CodeInvalidStateTransition && apperrors.CodeOf(err) != "VERSION_CONFLICT" {
			return err
		}
	}
	_, err = s.q.ExpireEvidenceGrants(ctx, s.Pool, &now)
	return err
}

func init() {
	produces("application.updated.v1", AggApplication, "Customer-facing application state changed (never screening detail or evidence).", "Marketplace, Notification", ApplicationEvent{})
	produces("rental_offer.updated.v1", AggOffer, "Rental offer state, terms and deadline (an accepted offer freezes its pricing version).", "Marketplace, Notification", RentalOfferEvent{})
}
