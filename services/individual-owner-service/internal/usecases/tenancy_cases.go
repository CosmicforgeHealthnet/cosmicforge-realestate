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
)

// Renewal, notices and move-out (§13.15).
//
// A renewal is a separate versioned case (owner proposal ↔ tenant counter);
// accepted terms generate a RENEWAL_ADDENDUM agreement that is signed like
// any agreement and, once EFFECTIVE, updates the tenancy. Notices use
// configured legal templates; the legal effective date is computed from
// policy (issue date + template minimum), and delivery and acknowledgement
// are recorded separately. The property only returns to the market after
// move-out completion and the owner's confirmation.

const (
	AggRenewal = "renewal_case"
	AggNotice  = "notice"
	AggMoveOut = "move_out_case"
)

// RenewalTermsView is one round of renewal terms.
type RenewalTermsView struct {
	RoundNo           int32           `json:"round_no"`
	ProposedBy        string          `json:"proposed_by"`
	RentMinor         int64           `json:"rent_minor"`
	Currency          string          `json:"currency"`
	TermMonths        int32           `json:"term_months"`
	StartDate         string          `json:"start_date"`
	ChangeExplanation *string         `json:"change_explanation"`
	MarketContext     json.RawMessage `json:"market_context,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

// RenewalView is a renewal case.
type RenewalView struct {
	ID                  ids.ID             `json:"id"`
	TenancyID           ids.ID             `json:"tenancy_id"`
	Status              string             `json:"status"`
	CurrentRound        int32              `json:"current_round"`
	RespondBy           time.Time          `json:"respond_by"`
	AddendumAgreementID *ids.ID            `json:"addendum_agreement_id"`
	Rounds              []RenewalTermsView `json:"rounds"`
	Version             int64              `json:"version"`
	CreatedAt           time.Time          `json:"created_at"`
}

func (s *Service) renewalView(ctx context.Context, c db.RenewalCase) (RenewalView, error) {
	v := RenewalView{ID: c.ID, TenancyID: c.TenancyID, Status: c.Status, CurrentRound: c.CurrentRound, RespondBy: c.RespondBy,
		AddendumAgreementID: c.AddendumAgreementID, Version: c.Version, CreatedAt: c.CreatedAt, Rounds: []RenewalTermsView{}}
	rs, err := s.q.ListRenewalTerms(ctx, s.dbq(ctx), c.ID)
	for _, r := range rs {
		v.Rounds = append(v.Rounds, RenewalTermsView{RoundNo: r.RoundNo, ProposedBy: r.ProposedBy, RentMinor: r.RentMinor, Currency: r.Currency,
			TermMonths: r.TermMonths, StartDate: r.StartDate.Format(time.DateOnly), ChangeExplanation: r.ChangeExplanation, MarketContext: r.MarketContext,
			CreatedAt: r.CreatedAt})
	}
	return v, err
}

// RenewalTermsInput proposes renewal terms.
type RenewalTermsInput struct {
	RentMinor         int64           `json:"rent_minor" v:"required,min=1" doc:"Cycle rent for the renewed term"`
	TermMonths        int32           `json:"term_months" v:"required,min=1,max=600"`
	StartDate         *string         `json:"start_date,omitempty" v:"format=date" doc:"Must be the day after the current end date (the default)"`
	ChangeExplanation *string         `json:"change_explanation,omitempty" v:"max=2000" doc:"Required when the rent changes"`
	MarketContext     json.RawMessage `json:"market_context,omitempty" doc:"Advisory market context shown to the tenant (e.g. a market-insight snapshot)"`
}

func (s *Service) insertRenewalTerms(ctx context.Context, c db.RenewalCase, t db.Tenancy, round int32, by string, in RenewalTermsInput) error {
	start := t.EndDate.AddDate(0, 0, 1)
	if in.StartDate != nil && !schema.ParseDate(in.StartDate).Equal(start) {
		return precondition("RENEWAL_START_MISMATCH", "A renewal starts the day after the current term ends ("+start.Format(time.DateOnly)+").")
	}
	if m := domain.CycleMonths(t.Frequency); m > 0 && in.TermMonths%m != 0 {
		return precondition("TERM_NOT_WHOLE_CYCLES", "The renewal term must be a whole number of payment cycles.")
	}
	if in.RentMinor != t.RentMinor && (in.ChangeExplanation == nil || len(*in.ChangeExplanation) < 10) {
		return precondition("RENT_CHANGE_EXPLANATION_REQUIRED", "Explain the rent change to the tenant (at least 10 characters).")
	}
	if len(in.MarketContext) > 8192 {
		return apperrors.InvalidRequest("market_context is too large.")
	}
	var dom *string
	var actor *ids.ID
	if a := actorFromCtx(ctx); a != nil {
		dom, actor = &a.Domain, &a.ID
	}
	var mc []byte
	if len(in.MarketContext) > 0 {
		if !json.Valid(in.MarketContext) {
			return apperrors.InvalidRequest("market_context must be JSON.")
		}
		mc = in.MarketContext
	}
	_, err := s.q.InsertRenewalTerms(ctx, mustTx(ctx), db.InsertRenewalTermsParams{ID: ids.New(), CaseID: c.ID, RoundNo: round, ProposedBy: by,
		RentMinor: in.RentMinor, Currency: t.Currency, TermMonths: in.TermMonths, StartDate: start, ChangeExplanation: in.ChangeExplanation,
		MarketContext: mc, ActorDomain: dom, ActorID: actor, CreatedAt: s.now()})
	return err
}

// ProposeRenewal opens a renewal case (tenancy ACTIVE → RENEWAL_PENDING).
func (s *Service) ProposeRenewal(ctx context.Context, subj authz.Subject, tenancyID ids.ID, in RenewalTermsInput) (RenewalView, error) {
	if err := schema.Validate(in); err != nil {
		return RenewalView{}, err
	}
	var out RenewalView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Do(authz.CapTenancyManage))
		if err != nil {
			return err
		}
		if t.Status != "ACTIVE" {
			return apperrors.InvalidStateTransition(t.Status, "RENEWAL_PENDING")
		}
		if t.EndDate == nil {
			return precondition("TENANCY_OPEN_ENDED", "Open-ended tenancies have no renewal.")
		}
		tx, now := mustTx(ctx), s.now()
		c, err := s.q.InsertRenewalCase(ctx, tx, db.InsertRenewalCaseParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, Status: "PROPOSED",
			RespondBy: now.Add(s.Policy.Domain.RenewalResponseTTL), CreatedAt: now})
		if isUnique(err, "renewal_cases_open_uniq") {
			return conflict("RENEWAL_ALREADY_OPEN", "A renewal is already in progress.")
		}
		if err != nil {
			return err
		}
		if err := domain.RenewalCase.RecordCreation(ctx, tx, c.ID, &c.PropertyID, "PROPOSED", now); err != nil {
			return err
		}
		if err := s.insertRenewalTerms(ctx, c, t, 1, "OWNER", in); err != nil {
			return err
		}
		if err := s.tenancyTransition(ctx, t.ID, "RENEWAL_PENDING", "renewal_proposed", ""); err != nil {
			return err
		}
		if err := s.record(ctx, "renewal.propose", AggRenewal, c.ID, map[string]any{"rent_minor": in.RentMinor, "term_months": in.TermMonths}, nil); err != nil {
			return err
		}
		out, err = s.renewalView(ctx, c)
		return err
	})
	return out, err
}

// ListRenewals lists a tenancy's renewal cases.
func (s *Service) ListRenewals(ctx context.Context, subj authz.Subject, tenancyID ids.ID) ([]RenewalView, error) {
	if _, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Read(authz.CapTenancyManage)); err != nil {
		return nil, err
	}
	cs, err := s.q.ListRenewalCases(ctx, s.dbq(ctx), tenancyID)
	if err != nil {
		return nil, err
	}
	out := make([]RenewalView, 0, len(cs))
	for _, c := range cs {
		v, err := s.renewalView(ctx, c)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// RenewalResponse answers the other side's latest terms.
type RenewalResponse struct {
	Decision string             `json:"decision" v:"required,enum=ACCEPT|COUNTER|DECLINE|WITHDRAW" doc:"WITHDRAW: owner only"`
	Counter  *RenewalTermsInput `json:"counter,omitempty" doc:"Required for COUNTER"`
}

// closeRenewal ends a case and returns the tenancy to ACTIVE.
func (s *Service) closeRenewal(ctx context.Context, c db.RenewalCase, to, reason string) error {
	if _, _, err := s.transition(ctx, domain.RenewalCase, c.ID, lifecycle.Change{To: to, Command: lower(to), Reason: reason}); err != nil {
		return err
	}
	t, err := s.q.GetTenancyForUpdate(ctx, mustTx(ctx), c.TenancyID)
	if err != nil {
		return err
	}
	if t.Status == "RENEWAL_PENDING" {
		return s.tenancyTransition(ctx, t.ID, "ACTIVE", "renewal_closed", reason)
	}
	return nil
}

// respondRenewal applies a decision by party (OWNER | TENANT).
func (s *Service) respondRenewal(ctx context.Context, c db.RenewalCase, t db.Tenancy, party string, in RenewalResponse) error {
	awaiting := map[string]string{"PROPOSED": "TENANT", "COUNTERED": "OWNER"}[c.Status]
	if in.Decision == "WITHDRAW" {
		if party != "OWNER" || (c.Status != "PROPOSED" && c.Status != "COUNTERED") {
			return conflict("RENEWAL_NOT_WITHDRAWABLE", "Only the owner can withdraw an open renewal proposal.")
		}
		return s.closeRenewal(ctx, c, "WITHDRAWN", "OWNER_WITHDREW")
	}
	if awaiting != party {
		return conflict("RENEWAL_NOT_AWAITING_YOU", "The renewal is not awaiting your response.")
	}
	if !s.now().Before(c.RespondBy) {
		return conflict("RENEWAL_EXPIRED", "The response deadline has passed.")
	}
	tx := mustTx(ctx)
	switch in.Decision {
	case "DECLINE":
		return s.closeRenewal(ctx, c, "DECLINED", party+"_DECLINED")
	case "COUNTER":
		if in.Counter == nil {
			return apperrors.InvalidRequest("counter terms are required.")
		}
		if err := schema.Validate(*in.Counter); err != nil {
			return err
		}
		next := map[string]string{"TENANT": "COUNTERED", "OWNER": "PROPOSED"}[party]
		if err := s.insertRenewalTerms(ctx, c, t, c.CurrentRound+1, party, *in.Counter); err != nil {
			return err
		}
		if _, _, err := s.transition(ctx, domain.RenewalCase, c.ID, lifecycle.Change{To: next, Command: "counter"}); err != nil {
			return err
		}
		return s.q.SetRenewalRound(ctx, tx, db.SetRenewalRoundParams{ID: c.ID, CurrentRound: c.CurrentRound + 1,
			RespondBy: s.now().Add(s.Policy.Domain.RenewalResponseTTL), UpdatedAt: s.now()})
	default: // ACCEPT → addendum agreement
		if _, _, err := s.transition(ctx, domain.RenewalCase, c.ID, lifecycle.Change{To: "ACCEPTED", Command: "accept"}); err != nil {
			return err
		}
		return s.createAddendum(ctx, c, t)
	}
}

// createAddendum prepares the RENEWAL_ADDENDUM agreement from the accepted terms.
func (s *Service) createAddendum(ctx context.Context, c db.RenewalCase, t db.Tenancy) error {
	tx, now := mustTx(ctx), s.now()
	last, err := s.q.LatestRenewalTerms(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	parent, err := s.q.GetAgreement(ctx, tx, t.AgreementID)
	if err != nil {
		return err
	}
	pv, err := s.q.GetAgreementVersion(ctx, tx, db.GetAgreementVersionParams{AgreementID: parent.ID, VersionNo: parent.CurrentVersionNo})
	if err != nil {
		return err
	}
	terms := parseTerms(pv.Terms)
	terms.StartDate, terms.TermMonths, terms.RentMinor = last.StartDate.Format(time.DateOnly), last.TermMonths, last.RentMinor
	terms.Clauses = []string{}
	a, err := s.q.InsertAgreement(ctx, tx, db.InsertAgreementParams{ID: ids.New(), PropertyID: t.PropertyID, UnitID: t.UnitID, ListingID: t.ListingID,
		ParentAgreementID: &parent.ID, RenewalCaseID: &c.ID, TenancyID: &t.ID, AgreementKind: "RENEWAL_ADDENDUM", Source: "RENEWAL",
		CustomerRefID: t.CustomerRefID, CreatedAt: now})
	if err != nil {
		return err
	}
	if err := domain.Agreement.RecordCreation(ctx, tx, a.ID, &a.PropertyID, "DRAFT", now); err != nil {
		return err
	}
	if _, err := s.newAgreementVersion(ctx, a, terms, nil, nil, "Renewal terms accepted (round "+itoa(int(last.RoundNo))+")"); err != nil {
		return err
	}
	ps, err := s.q.ListAgreementParticipants(ctx, tx, parent.ID)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if _, err := s.q.InsertAgreementParticipant(ctx, tx, db.InsertAgreementParticipantParams{ID: ids.New(), AgreementID: a.ID, Role: p.Role,
			PartyDomain: p.PartyDomain, PartyRefID: p.PartyRefID, DisplayName: p.DisplayName, SigningEmail: p.SigningEmail, SignRequired: p.SignRequired,
			CreatedAt: now}); err != nil {
			return err
		}
	}
	return s.q.SetRenewalAddendum(ctx, tx, db.SetRenewalAddendumParams{ID: c.ID, AddendumAgreementID: &a.ID, UpdatedAt: now})
}

// applyRenewalAddendum updates the tenancy when its addendum becomes effective.
func (s *Service) applyRenewalAddendum(ctx context.Context, a db.Agreement, t AgreementTerms) error {
	if a.TenancyID == nil || a.RenewalCaseID == nil {
		return precondition("ADDENDUM_WITHOUT_TENANCY", "The addendum is not linked to a tenancy.")
	}
	tx, now := mustTx(ctx), s.now()
	tn, err := s.q.GetTenancyForUpdate(ctx, tx, *a.TenancyID)
	if err != nil {
		return err
	}
	if tn.Status != "RENEWAL_PENDING" {
		return conflict("TENANCY_NOT_RENEWING", "The tenancy is no longer awaiting renewal ("+tn.Status+").")
	}
	rt := t.rentTerms()
	end := rt.EndDate()
	if !end.After(deref(tn.EndDate)) {
		return precondition("RENEWAL_DOES_NOT_EXTEND", "The renewed term must end after the current end date.")
	}
	if tn.RentCollection == "PLATFORM" {
		// Close out the original term on its own terms before re-anchoring.
		for _, p := range domain.RentSchedule(tenancyRentTerms(tn), *tn.EndDate) {
			if err := s.insertObligation(ctx, tn, p); err != nil {
				return err
			}
		}
	}
	var escAfter, escBps *int32
	if t.EscalationMonths > 0 {
		escAfter, escBps = &t.EscalationMonths, &t.EscalationBps
	}
	if err := s.q.SetTenancyTerms(ctx, tx, db.SetTenancyTermsParams{ID: tn.ID, EndDate: &end, RentMinor: t.RentMinor, RecurringFeesMinor: t.RecurringFeesMinor,
		EscalationAfterMonths: escAfter, EscalationBps: escBps, ScheduleAnchor: &rt.Start, UpdatedAt: now}); err != nil {
		return err
	}
	if tn.RentCollection == "PLATFORM" {
		renewed, err := s.q.GetTenancy(ctx, tx, tn.ID)
		if err != nil {
			return err
		}
		if err := s.materializeSchedule(ctx, renewed); err != nil {
			return err
		}
	}
	rc, err := s.q.GetRenewalCaseForUpdate(ctx, tx, *a.RenewalCaseID)
	if err != nil {
		return err
	}
	if _, _, err := s.transition(ctx, domain.RenewalCase, rc.ID, lifecycle.Change{To: "COMPLETED", Command: "addendum_effective"}); err != nil {
		return err
	}
	if err := s.tenancyTransition(ctx, tn.ID, "ACTIVE", "renewed", ""); err != nil {
		return err
	}
	return s.propertyHistory(ctx, tn.PropertyID, "TENANCY_RENEWED", "Tenancy renewed until "+end.Format(time.DateOnly), AggTenancy, tn.ID)
}

// RespondRenewal is the owner's response to a tenant counter (or withdrawal).
func (s *Service) RespondRenewal(ctx context.Context, subj authz.Subject, caseID ids.ID, in RenewalResponse) (RenewalView, error) {
	if err := schema.Validate(in); err != nil {
		return RenewalView{}, err
	}
	var out RenewalView
	err := s.InTx(ctx, func(ctx context.Context) error {
		c, err := s.q.GetRenewalCase(ctx, mustTx(ctx), caseID)
		if isNoRows(err) {
			return notFound(AggRenewal)
		}
		if err != nil {
			return err
		}
		t, _, err := s.tenancyFor(ctx, subj, c.TenancyID, authz.Do(authz.CapTenancyManage))
		if err != nil {
			return err
		}
		if c, err = s.q.GetRenewalCaseForUpdate(ctx, mustTx(ctx), caseID); err != nil {
			return err
		}
		if err := s.respondRenewal(ctx, c, t, "OWNER", in); err != nil {
			return err
		}
		if err := s.record(ctx, "renewal.owner_response", AggRenewal, caseID, map[string]any{"decision": in.Decision}, nil); err != nil {
			return err
		}
		n, err := s.q.GetRenewalCase(ctx, mustTx(ctx), caseID)
		if err != nil {
			return err
		}
		out, err = s.renewalView(ctx, n)
		return err
	})
	return out, err
}

// CustomerRespondRenewal is the tenant's response (marketplace).
func (s *Service) CustomerRespondRenewal(ctx context.Context, subj authz.Subject, caseID ids.ID, in RenewalResponse) (RenewalView, error) {
	if err := schema.Validate(in); err != nil {
		return RenewalView{}, err
	}
	if in.Decision == "WITHDRAW" {
		return RenewalView{}, apperrors.InvalidRequest("Tenants decline instead of withdrawing.")
	}
	var out RenewalView
	err := s.InTx(ctx, func(ctx context.Context) error {
		c, err := s.q.GetRenewalCase(ctx, mustTx(ctx), caseID)
		if isNoRows(err) {
			return notFound(AggRenewal)
		}
		if err != nil {
			return err
		}
		t, _, err := s.customerTenancy(ctx, subj, c.TenancyID, true)
		if err != nil {
			if apperrors.CodeOf(err) == "TENANCY_NOT_FOUND" {
				return notFound(AggRenewal)
			}
			return err
		}
		if c, err = s.q.GetRenewalCaseForUpdate(ctx, mustTx(ctx), caseID); err != nil {
			return err
		}
		if err := s.respondRenewal(ctx, c, t, "TENANT", in); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, mustTx(ctx), t.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateTenantResponse, map[string]string{"property_title": p.Title, "subject": "renewal " + lower(in.Decision)}); err != nil {
			return err
		}
		if err := s.record(ctx, "renewal.tenant_response", AggRenewal, caseID, map[string]any{"decision": in.Decision}, nil); err != nil {
			return err
		}
		n, err := s.q.GetRenewalCase(ctx, mustTx(ctx), caseID)
		if err != nil {
			return err
		}
		out, err = s.renewalView(ctx, n)
		return err
	})
	return out, err
}

// RunRenewals expires unanswered renewal rounds and reminds owners of
// upcoming term ends (§11 renewal/notices).
func (s *Service) RunRenewals(ctx context.Context, batch int32) error {
	now := s.now()
	due, err := s.q.DueRenewalExpiries(ctx, s.Pool, db.DueRenewalExpiriesParams{RespondBy: now, Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range due {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			c, err := s.q.GetRenewalCaseForUpdate(ctx, mustTx(ctx), id)
			if err != nil {
				return err
			}
			return s.closeRenewal(ctx, c, "EXPIRED", "DEADLINE_PASSED")
		}); err != nil && !benign(err) {
			return err
		}
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		ts, err := s.q.TenanciesForRenewalReminder(ctx, mustTx(ctx), db.TenanciesForRenewalReminderParams{Now: now,
			WindowEnd: now.Add(s.Policy.Domain.RenewalReminderLead), MaxRows: batch})
		if err != nil {
			return err
		}
		for _, t := range ts {
			p, err := s.q.GetProperty(ctx, mustTx(ctx), t.PropertyID)
			if err != nil {
				return err
			}
			if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateRenewalDue, map[string]string{"property_title": p.Title,
				"end_date": t.EndDate.Format(time.DateOnly)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- notices ----

// NoticeView is a notice.
type NoticeView struct {
	ID                      ids.ID     `json:"id"`
	TenancyID               ids.ID     `json:"tenancy_id"`
	IssuedBy                string     `json:"issued_by"`
	NoticeType              string     `json:"notice_type"`
	TemplateID              *ids.ID    `json:"template_id"`
	Status                  string     `json:"status"`
	Summary                 string     `json:"summary"`
	DocumentMediaID         *ids.ID    `json:"document_media_id"`
	RequestedEffectiveDate  *string    `json:"requested_effective_date"`
	EffectiveDate           *string    `json:"effective_date" doc:"Policy-computed legal effective date (not moved by acknowledgement)"`
	DeliveryChannel         string     `json:"delivery_channel"`
	IssuedAt                *time.Time `json:"issued_at"`
	DeliveredAt             *time.Time `json:"delivered_at"`
	DeliveryEvidenceMediaID *ids.ID    `json:"delivery_evidence_media_id"`
	DeliveryRef             *string    `json:"delivery_ref"`
	AcknowledgedAt          *time.Time `json:"acknowledged_at"`
	DisputedAt              *time.Time `json:"disputed_at"`
	DisputeReason           *string    `json:"dispute_reason"`
	FailureReason           *string    `json:"failure_reason"`
	PolicyVersion           *string    `json:"policy_version"`
	Version                 int64      `json:"version"`
	CreatedAt               time.Time  `json:"created_at"`
}

func noticeView(n db.Notice) NoticeView {
	return NoticeView{ID: n.ID, TenancyID: n.TenancyID, IssuedBy: n.IssuedBy, NoticeType: n.NoticeType, TemplateID: n.TemplateID, Status: n.Status,
		Summary: n.Summary, DocumentMediaID: n.DocumentMediaID, RequestedEffectiveDate: dateStr(n.RequestedEffectiveDate), EffectiveDate: dateStr(n.EffectiveDate),
		DeliveryChannel: n.DeliveryChannel, IssuedAt: n.IssuedAt, DeliveredAt: n.DeliveredAt, DeliveryEvidenceMediaID: n.DeliveryEvidenceMediaID,
		DeliveryRef: n.DeliveryRef, AcknowledgedAt: n.AcknowledgedAt, DisputedAt: n.DisputedAt, DisputeReason: n.DisputeReason, FailureReason: n.FailureReason,
		PolicyVersion: n.PolicyVersion, Version: n.Version, CreatedAt: n.CreatedAt}
}

// NoticeInput drafts a notice.
type NoticeInput struct {
	NoticeType             string  `json:"notice_type" v:"required,enum=RENT_REMINDER|ARREARS|RENT_REVIEW|RENEWAL|TERMINATION|ENTRY|BREACH|GENERAL"`
	TemplateID             *ids.ID `json:"template_id,omitempty" doc:"Required for notice types that policy reserves to legal templates"`
	Summary                string  `json:"summary" v:"required,max=2000"`
	DocumentMediaID        *ids.ID `json:"document_media_id,omitempty"`
	RequestedEffectiveDate *string `json:"requested_effective_date,omitempty" v:"format=date"`
	DeliveryChannel        string  `json:"delivery_channel" v:"required,enum=PLATFORM|EMAIL|POST|HAND_DELIVERY|COURIER"`
}

// MediaRefs lists referenced media.
func (in NoticeInput) MediaRefs() []ids.ID { return optIDs(in.DocumentMediaID) }

// CreateNotice drafts a notice for a tenancy.
func (s *Service) CreateNotice(ctx context.Context, subj authz.Subject, tenancyID ids.ID, in NoticeInput, ev Evidence) (NoticeView, error) {
	if err := schema.Validate(in); err != nil {
		return NoticeView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return NoticeView{}, err
	}
	var out NoticeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Do(authz.CapNoticesIssue))
		if err != nil {
			return err
		}
		if t.Status == "ENDED" || t.Status == "PENDING_MOVE_IN" {
			return conflict("TENANCY_NOT_RUNNING", "Notices apply to running tenancies.")
		}
		if err := s.checkNoticeTemplate(ctx, t, in.NoticeType, in.TemplateID); err != nil {
			return err
		}
		if in.DeliveryChannel == "PLATFORM" && t.CustomerRefID == nil {
			return precondition("TENANT_NOT_ON_PLATFORM", "Off-platform tenants must be served by email, post, courier or hand delivery.")
		}
		n, err := s.q.InsertNotice(ctx, mustTx(ctx), db.InsertNoticeParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, IssuedBy: "OWNER",
			NoticeType: in.NoticeType, TemplateID: in.TemplateID, Summary: in.Summary, DocumentMediaID: in.DocumentMediaID,
			RequestedEffectiveDate: schema.ParseDate(in.RequestedEffectiveDate), DeliveryChannel: in.DeliveryChannel, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		if err := domain.Notice.RecordCreation(ctx, mustTx(ctx), n.ID, &n.PropertyID, "DRAFT", s.now()); err != nil {
			return err
		}
		out = noticeView(n)
		return s.record(ctx, "notice.create", AggNotice, n.ID, map[string]any{"notice_type": in.NoticeType}, nil)
	})
	return out, err
}

func (s *Service) checkNoticeTemplate(ctx context.Context, t db.Tenancy, noticeType string, templateID *ids.ID) error {
	if templateID == nil {
		if slices.Contains(s.Policy.Domain.NoticeTemplateRequired, noticeType) {
			return precondition("NOTICE_TEMPLATE_REQUIRED", noticeType+" notices must use a configured legal template.")
		}
		return nil
	}
	nt, err := s.q.GetNoticeTemplate(ctx, s.dbq(ctx), *templateID)
	if isNoRows(err) {
		return precondition("NOTICE_TEMPLATE_NOT_FOUND", "The notice template does not exist.")
	}
	if err != nil {
		return err
	}
	addr, err := s.q.GetPropertyAddress(ctx, s.dbq(ctx), t.PropertyID)
	if err != nil {
		return err
	}
	if nt.Status != "ACTIVE" || nt.NoticeType != noticeType || nt.Country != addr.CountryCode {
		return precondition("NOTICE_TEMPLATE_MISMATCH", "The template is retired or does not apply to this notice type or jurisdiction.")
	}
	return nil
}

// ListNotices lists a tenancy's notices.
func (s *Service) ListNotices(ctx context.Context, subj authz.Subject, tenancyID ids.ID) ([]NoticeView, error) {
	if _, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Read(authz.CapNoticesIssue)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListNotices(ctx, s.dbq(ctx), tenancyID)
	out := make([]NoticeView, 0, len(rows))
	for _, r := range rows {
		out = append(out, noticeView(r))
	}
	return out, err
}

func (s *Service) noticeFor(ctx context.Context, subj authz.Subject, id ids.ID) (db.Notice, db.Tenancy, error) {
	n, err := s.q.GetNotice(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return n, db.Tenancy{}, notFound(AggNotice)
	}
	if err != nil {
		return n, db.Tenancy{}, err
	}
	t, _, err := s.tenancyFor(ctx, subj, n.TenancyID, authz.Do(authz.CapNoticesIssue))
	if err != nil {
		if apperrors.CodeOf(err) == "TENANCY_NOT_FOUND" {
			err = notFound(AggNotice)
		}
		return n, t, err
	}
	n, err = s.q.GetNoticeForUpdate(ctx, mustTx(ctx), id)
	return n, t, err
}

// issueEffects applies the tenancy consequence of an issued notice.
func (s *Service) issueEffects(ctx context.Context, n db.Notice, t db.Tenancy, eff time.Time) error {
	if n.NoticeType != "TERMINATION" {
		return nil
	}
	tx := mustTx(ctx)
	if t.Status == "RENEWAL_PENDING" {
		cs, err := s.q.ListRenewalCases(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if c.Status == "PROPOSED" || c.Status == "COUNTERED" || c.Status == "ACCEPTED" {
				if _, _, err := s.transition(ctx, domain.RenewalCase, c.ID, lifecycle.Change{To: "WITHDRAWN", Command: "termination_notice"}); err != nil {
					return err
				}
			}
		}
	}
	if t.Status == "ACTIVE" || t.Status == "RENEWAL_PENDING" {
		if err := s.tenancyTransition(ctx, t.ID, "TERMINATION_PENDING", "termination_notice", n.IssuedBy); err != nil {
			return err
		}
	}
	return s.q.SetTenancyTermination(ctx, tx, db.SetTenancyTerminationParams{ID: t.ID, TerminationEffectiveDate: &eff, UpdatedAt: s.now()})
}

func (s *Service) noticeMinDays(ctx context.Context, n db.Notice, t db.Tenancy) (int32, error) {
	if n.TemplateID != nil {
		nt, err := s.q.GetNoticeTemplate(ctx, mustTx(ctx), *n.TemplateID)
		if err != nil {
			return 0, err
		}
		if nt.Status != "ACTIVE" {
			return 0, precondition("NOTICE_TEMPLATE_RETIRED", "The notice template was retired; redraft with the current version.")
		}
		return nt.MinNoticeDays, nil
	}
	if n.NoticeType != "TERMINATION" {
		return 0, nil
	}
	// Tenant termination notices follow the jurisdiction's active template.
	addr, err := s.q.GetPropertyAddress(ctx, mustTx(ctx), t.PropertyID)
	if err != nil {
		return 0, err
	}
	ts, err := s.q.ListNoticeTemplates(ctx, mustTx(ctx), db.ListNoticeTemplatesParams{Country: &addr.CountryCode, NoticeType: ptr("TERMINATION")})
	if err != nil || len(ts) == 0 {
		return 0, err
	}
	return ts[0].MinNoticeDays, nil
}

// IssueNotice serves a drafted notice; the legal effective date is fixed now.
func (s *Service) IssueNotice(ctx context.Context, subj authz.Subject, id ids.ID) (NoticeView, error) {
	var out NoticeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		n, t, err := s.noticeFor(ctx, subj, id)
		if err != nil {
			return err
		}
		if n.Status != "DRAFT" && n.Status != "DELIVERY_FAILED" {
			return apperrors.InvalidStateTransition(n.Status, "ISSUED")
		}
		if n.NoticeType == "TERMINATION" && t.Status != "ACTIVE" && t.Status != "RENEWAL_PENDING" && t.Status != "TERMINATION_PENDING" {
			return conflict("TENANCY_NOT_RUNNING", "The tenancy is "+t.Status+".")
		}
		days, err := s.noticeMinDays(ctx, n, t)
		if err != nil {
			return err
		}
		now := s.now()
		eff := domain.NoticeEffectiveDate(now, days, n.RequestedEffectiveDate)
		if err := s.q.SetNoticeIssued(ctx, mustTx(ctx), db.SetNoticeIssuedParams{ID: id, IssuedAt: &now, EffectiveDate: &eff, PolicyVersion: &s.Policy.Version}); err != nil {
			return err
		}
		if _, _, err := s.transition(ctx, domain.Notice, id, lifecycle.Change{To: "ISSUED", Command: "issue"}); err != nil {
			return err
		}
		if err := s.issueEffects(ctx, n, t, eff); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, n.PropertyID, "NOTICE_ISSUED", n.NoticeType+" notice issued (effective "+eff.Format(time.DateOnly)+")", AggNotice, id); err != nil {
			return err
		}
		if err := s.record(ctx, "notice.issue", AggNotice, id, map[string]any{"effective_date": eff.Format(time.DateOnly), "min_notice_days": days}, nil); err != nil {
			return err
		}
		cur, err := s.q.GetNotice(ctx, mustTx(ctx), id)
		out = noticeView(cur)
		return err
	})
	return out, err
}

// NoticeDeliveryInput records service of a notice outside the platform.
type NoticeDeliveryInput struct {
	Outcome         string  `json:"outcome" v:"required,enum=DELIVERED|FAILED"`
	EvidenceMediaID *ids.ID `json:"evidence_media_id,omitempty" doc:"Proof of service (courier receipt, photo, signed copy)"`
	DeliveryRef     *string `json:"delivery_ref,omitempty" v:"max=200"`
	FailureReason   *string `json:"failure_reason,omitempty" v:"max=500"`
}

// MediaRefs lists referenced media.
func (in NoticeDeliveryInput) MediaRefs() []ids.ID { return optIDs(in.EvidenceMediaID) }

// RecordNoticeDelivery records delivery or failure of an off-platform notice.
func (s *Service) RecordNoticeDelivery(ctx context.Context, subj authz.Subject, id ids.ID, in NoticeDeliveryInput, ev Evidence) (NoticeView, error) {
	if err := schema.Validate(in); err != nil {
		return NoticeView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return NoticeView{}, err
	}
	var out NoticeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		n, _, err := s.noticeFor(ctx, subj, id)
		if err != nil {
			return err
		}
		if n.DeliveryChannel == "PLATFORM" {
			return conflict("PLATFORM_DELIVERY", "Platform notices are delivered when marketplace-service shows them to the tenant.")
		}
		tx, now := mustTx(ctx), s.now()
		if in.Outcome == "DELIVERED" {
			if in.EvidenceMediaID == nil && in.DeliveryRef == nil {
				return precondition("DELIVERY_EVIDENCE_REQUIRED", "Provide proof of service or a delivery reference.")
			}
			if _, _, err := s.transition(ctx, domain.Notice, id, lifecycle.Change{To: "DELIVERED", Command: "delivered"}); err != nil {
				return err
			}
			err = s.q.SetNoticeDelivered(ctx, tx, db.SetNoticeDeliveredParams{ID: id, DeliveredAt: &now, DeliveryEvidenceMediaID: in.EvidenceMediaID, DeliveryRef: in.DeliveryRef})
		} else {
			if _, _, err := s.transition(ctx, domain.Notice, id, lifecycle.Change{To: "DELIVERY_FAILED", Command: "delivery_failed"}); err != nil {
				return err
			}
			err = s.q.SetNoticeFailed(ctx, tx, db.SetNoticeFailedParams{ID: id, FailureReason: in.FailureReason, UpdatedAt: now})
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "notice.delivery", AggNotice, id, map[string]any{"outcome": in.Outcome}, nil); err != nil {
			return err
		}
		cur, err := s.q.GetNotice(ctx, tx, id)
		out = noticeView(cur)
		return err
	})
	return out, err
}

// AcknowledgeTenantNotice acknowledges a notice given by the tenant.
func (s *Service) AcknowledgeTenantNotice(ctx context.Context, subj authz.Subject, id ids.ID) (NoticeView, error) {
	var out NoticeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		n, _, err := s.noticeFor(ctx, subj, id)
		if err != nil {
			return err
		}
		if n.IssuedBy != "TENANT" {
			return conflict("NOT_A_TENANT_NOTICE", "Only notices given by the tenant are acknowledged by the owner.")
		}
		if _, _, err := s.transition(ctx, domain.Notice, id, lifecycle.Change{To: "ACKNOWLEDGED", Command: "owner_acknowledge"}); err != nil {
			return err
		}
		if err := s.q.SetNoticeAcknowledged(ctx, mustTx(ctx), db.SetNoticeAcknowledgedParams{ID: id, AcknowledgedAt: ptr(s.now())}); err != nil {
			return err
		}
		if err := s.record(ctx, "notice.acknowledge", AggNotice, id, nil, nil); err != nil {
			return err
		}
		cur, err := s.q.GetNotice(ctx, mustTx(ctx), id)
		out = noticeView(cur)
		return err
	})
	return out, err
}

// CustomerNoticeAction is the tenant's receipt, acknowledgement or dispute.
type CustomerNoticeAction struct {
	Action string  `json:"action" v:"required,enum=RECEIVED|ACKNOWLEDGE|DISPUTE" doc:"RECEIVED is sent by marketplace-service when the notice is shown to the tenant"`
	Reason *string `json:"reason,omitempty" v:"max=1000"`
}

// CustomerActOnNotice records delivery/acknowledgement/dispute by the tenant.
func (s *Service) CustomerActOnNotice(ctx context.Context, subj authz.Subject, id ids.ID, in CustomerNoticeAction) (NoticeView, error) {
	if err := schema.Validate(in); err != nil {
		return NoticeView{}, err
	}
	var out NoticeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		n, err := s.q.GetNotice(ctx, mustTx(ctx), id)
		if isNoRows(err) {
			return notFound(AggNotice)
		}
		if err != nil {
			return err
		}
		t, _, err := s.customerTenancy(ctx, subj, n.TenancyID, true)
		if err != nil {
			if apperrors.CodeOf(err) == "TENANCY_NOT_FOUND" {
				return notFound(AggNotice)
			}
			return err
		}
		if n.IssuedBy != "OWNER" || n.Status == "DRAFT" {
			return notFound(AggNotice)
		}
		tx, now := mustTx(ctx), s.now()
		switch in.Action {
		case "RECEIVED":
			if n.Status != "ISSUED" {
				break // idempotent
			}
			if _, _, err := s.transition(ctx, domain.Notice, id, lifecycle.Change{To: "DELIVERED", Command: "platform_delivered"}); err != nil {
				return err
			}
			err = s.q.SetNoticeDelivered(ctx, tx, db.SetNoticeDeliveredParams{ID: id, DeliveredAt: &now, DeliveryRef: ptr("marketplace")})
		case "ACKNOWLEDGE":
			if n.Status == "ISSUED" && n.DeliveryChannel == "PLATFORM" {
				if _, _, err := s.transition(ctx, domain.Notice, id, lifecycle.Change{To: "DELIVERED", Command: "platform_delivered"}); err != nil {
					return err
				}
				if err := s.q.SetNoticeDelivered(ctx, tx, db.SetNoticeDeliveredParams{ID: id, DeliveredAt: &now, DeliveryRef: ptr("marketplace")}); err != nil {
					return err
				}
			}
			if _, _, err := s.transition(ctx, domain.Notice, id, lifecycle.Change{To: "ACKNOWLEDGED", Command: "tenant_acknowledge"}); err != nil {
				return err
			}
			err = s.q.SetNoticeAcknowledged(ctx, tx, db.SetNoticeAcknowledgedParams{ID: id, AcknowledgedAt: &now})
		case "DISPUTE":
			if in.Reason == nil {
				return apperrors.InvalidRequest("A dispute needs a reason.")
			}
			if _, _, err := s.transition(ctx, domain.Notice, id, lifecycle.Change{To: "DISPUTED", Command: "tenant_dispute"}); err != nil {
				return err
			}
			if err := s.q.SetNoticeDisputed(ctx, tx, db.SetNoticeDisputedParams{ID: id, DisputedAt: &now, DisputeReason: in.Reason}); err != nil {
				return err
			}
			err = s.q.SetTenancyDisputeFlag(ctx, tx, db.SetTenancyDisputeFlagParams{ID: t.ID, DisputeFlag: true, UpdatedAt: now})
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "notice.tenant_action", AggNotice, id, map[string]any{"action": in.Action}, nil); err != nil {
			return err
		}
		cur, err := s.q.GetNotice(ctx, tx, id)
		out = noticeView(cur)
		return err
	})
	return out, err
}

// CustomerNoticeInput is a notice given by the tenant (marketplace).
type CustomerNoticeInput struct {
	NoticeType             string  `json:"notice_type" v:"required,enum=TERMINATION|GENERAL"`
	Summary                string  `json:"summary" v:"required,max=2000"`
	RequestedEffectiveDate *string `json:"requested_effective_date,omitempty" v:"format=date"`
}

// CustomerGiveNotice records a tenant's notice (delivered on the platform).
func (s *Service) CustomerGiveNotice(ctx context.Context, subj authz.Subject, tenancyID ids.ID, in CustomerNoticeInput) (NoticeView, error) {
	if err := schema.Validate(in); err != nil {
		return NoticeView{}, err
	}
	var out NoticeView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.customerTenancy(ctx, subj, tenancyID, true)
		if err != nil {
			return err
		}
		if t.Status != "ACTIVE" && t.Status != "RENEWAL_PENDING" {
			return conflict("TENANCY_NOT_RUNNING", "The tenancy is "+t.Status+".")
		}
		tx, now := mustTx(ctx), s.now()
		n, err := s.q.InsertNotice(ctx, tx, db.InsertNoticeParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, IssuedBy: "TENANT",
			NoticeType: in.NoticeType, Summary: in.Summary, RequestedEffectiveDate: schema.ParseDate(in.RequestedEffectiveDate), DeliveryChannel: "PLATFORM",
			CreatedAt: now})
		if err != nil {
			return err
		}
		if err := domain.Notice.RecordCreation(ctx, tx, n.ID, &n.PropertyID, "DRAFT", now); err != nil {
			return err
		}
		days, err := s.noticeMinDays(ctx, n, t)
		if err != nil {
			return err
		}
		eff := domain.NoticeEffectiveDate(now, days, n.RequestedEffectiveDate)
		if err := s.q.SetNoticeIssued(ctx, tx, db.SetNoticeIssuedParams{ID: n.ID, IssuedAt: &now, EffectiveDate: &eff, PolicyVersion: &s.Policy.Version}); err != nil {
			return err
		}
		for _, to := range []string{"ISSUED", "DELIVERED"} {
			if _, _, err := s.transition(ctx, domain.Notice, n.ID, lifecycle.Change{To: to, Command: "tenant_notice"}); err != nil {
				return err
			}
		}
		if err := s.q.SetNoticeDelivered(ctx, tx, db.SetNoticeDeliveredParams{ID: n.ID, DeliveredAt: &now, DeliveryRef: ptr("marketplace")}); err != nil {
			return err
		}
		if err := s.issueEffects(ctx, n, t, eff); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, t.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateTenantNotice, map[string]string{"property_title": p.Title, "notice_type": lower(in.NoticeType)}); err != nil {
			return err
		}
		if err := s.record(ctx, "notice.tenant_give", AggNotice, n.ID, map[string]any{"notice_type": in.NoticeType, "effective_date": eff.Format(time.DateOnly)}, nil); err != nil {
			return err
		}
		cur, err := s.q.GetNotice(ctx, tx, n.ID)
		out = noticeView(cur)
		return err
	})
	return out, err
}

// ---- move-out ----

// MoveOutView is a move-out case.
type MoveOutView struct {
	ID               ids.ID     `json:"id"`
	TenancyID        ids.ID     `json:"tenancy_id"`
	Status           string     `json:"status"`
	InitiatedBy      string     `json:"initiated_by"`
	NoticeID         *ids.ID    `json:"notice_id"`
	MoveOutDate      string     `json:"move_out_date"`
	InspectionAt     *time.Time `json:"inspection_at"`
	InspectedAt      *time.Time `json:"inspected_at"`
	KeysReturnedAt   *time.Time `json:"keys_returned_at"`
	OwnerConfirmedAt *time.Time `json:"owner_confirmed_at"`
	ReturnToMarket   *bool      `json:"return_to_market"`
	CompletedAt      *time.Time `json:"completed_at"`
	Version          int64      `json:"version"`
}

func moveOutView(m db.MoveOutCase) MoveOutView {
	return MoveOutView{ID: m.ID, TenancyID: m.TenancyID, Status: m.Status, InitiatedBy: m.InitiatedBy, NoticeID: m.NoticeID,
		MoveOutDate: m.MoveOutDate.Format(time.DateOnly), InspectionAt: m.InspectionAt, InspectedAt: m.InspectedAt, KeysReturnedAt: m.KeysReturnedAt,
		OwnerConfirmedAt: m.OwnerConfirmedAt, ReturnToMarket: m.ReturnToMarket, CompletedAt: m.CompletedAt, Version: m.Version}
}

// GetMoveOut returns a tenancy's move-out case.
func (s *Service) GetMoveOut(ctx context.Context, subj authz.Subject, tenancyID ids.ID) (MoveOutView, error) {
	if _, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Read(authz.CapTenancyManage)); err != nil {
		return MoveOutView{}, err
	}
	m, err := s.q.GetMoveOutByTenancy(ctx, s.dbq(ctx), tenancyID)
	if isNoRows(err) {
		return MoveOutView{}, notFound(AggMoveOut)
	}
	return moveOutView(m), err
}

// MoveOutInput starts or advances a move-out.
type MoveOutInput struct {
	Action         string     `json:"action" v:"required,enum=START|SCHEDULE_INSPECTION|COMPLETE_INSPECTION|KEYS_RETURNED|COMPLETE"`
	MoveOutDate    *string    `json:"move_out_date,omitempty" v:"format=date" doc:"START: the agreed move-out date"`
	NoticeID       *ids.ID    `json:"notice_id,omitempty"`
	InspectionAt   *time.Time `json:"inspection_at,omitempty"`
	ReturnToMarket *bool      `json:"return_to_market,omitempty" doc:"COMPLETE: the owner's confirmation that the property may return to the market"`
}

// MoveOut drives the move-out case (tenancy TERMINATION_PENDING →
// MOVE_OUT_PENDING → ENDED). Completion needs the move-out inspection, the
// platform deposit settled (or its dispute isolated) and the owner's explicit
// confirmation; only then can the property become AVAILABLE.
func (s *Service) MoveOut(ctx context.Context, subj authz.Subject, tenancyID ids.ID, in MoveOutInput) (MoveOutView, error) {
	if err := schema.Validate(in); err != nil {
		return MoveOutView{}, err
	}
	var out MoveOutView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, d, err := s.tenancyFor(ctx, subj, tenancyID, authz.Do(authz.CapTenancyManage))
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if in.Action == "START" {
			if in.MoveOutDate == nil {
				return apperrors.InvalidRequest("move_out_date is required.")
			}
			date := *schema.ParseDate(in.MoveOutDate)
			switch {
			case t.Status == "TERMINATION_PENDING":
			case (t.Status == "ACTIVE" || t.Status == "RENEWAL_PENDING") && t.EndDate != nil && !date.Before(*t.EndDate):
				if t.Status == "RENEWAL_PENDING" {
					return conflict("RENEWAL_IN_PROGRESS", "Close the open renewal before ending the tenancy at term end.")
				}
				if err := s.tenancyTransition(ctx, t.ID, "TERMINATION_PENDING", "term_end", "TERM_ENDED"); err != nil {
					return err
				}
				if err := s.q.SetTenancyTermination(ctx, tx, db.SetTenancyTerminationParams{ID: t.ID, TerminationEffectiveDate: t.EndDate, UpdatedAt: now}); err != nil {
					return err
				}
			default:
				return conflict("TERMINATION_REQUIRED", "Serve a termination notice (or wait for the term end) before starting move-out.")
			}
			initiated := "OWNER"
			if in.NoticeID != nil {
				n, err := s.q.GetNotice(ctx, tx, *in.NoticeID)
				if isNoRows(err) || (err == nil && n.TenancyID != t.ID) {
					return precondition("NOTICE_NOT_FOUND", "The notice does not belong to this tenancy.")
				}
				if err != nil {
					return err
				}
				initiated = n.IssuedBy
			}
			m, err := s.q.InsertMoveOutCase(ctx, tx, db.InsertMoveOutCaseParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, InitiatedBy: initiated,
				NoticeID: in.NoticeID, MoveOutDate: date, CreatedAt: now})
			if isUnique(err, "move_out_cases_tenancy_id_key") {
				return conflict("MOVE_OUT_ALREADY_STARTED", "Move-out has already started.")
			}
			if err != nil {
				return err
			}
			if err := domain.MoveOut.RecordCreation(ctx, tx, m.ID, &m.PropertyID, "OPEN", now); err != nil {
				return err
			}
			if err := s.tenancyTransition(ctx, t.ID, "MOVE_OUT_PENDING", "move_out_started", ""); err != nil {
				return err
			}
			if _, err := s.q.InsertChecklist(ctx, tx, db.InsertChecklistParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, Phase: "MOVE_OUT",
				CreatedAt: now}); err != nil && !isNoRows(err) {
				return err
			}
		} else {
			m, err := s.q.GetMoveOutForUpdate(ctx, tx, t.ID)
			if isNoRows(err) {
				return conflict("MOVE_OUT_NOT_STARTED", "Start the move-out first.")
			}
			if err != nil {
				return err
			}
			if err := s.advanceMoveOut(ctx, t, m, d, in); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "tenancy.move_out."+lower(in.Action), AggTenancy, tenancyID, nil, nil); err != nil {
			return err
		}
		m, err := s.q.GetMoveOutByTenancy(ctx, tx, tenancyID)
		out = moveOutView(m)
		return err
	})
	return out, err
}

func (s *Service) advanceMoveOut(ctx context.Context, t db.Tenancy, m db.MoveOutCase, d authz.Decision, in MoveOutInput) error {
	tx, now := mustTx(ctx), s.now()
	move := func(to string) error {
		_, _, err := s.transition(ctx, domain.MoveOut, m.ID, lifecycle.Change{To: to, Command: lower(in.Action)})
		return err
	}
	switch in.Action {
	case "SCHEDULE_INSPECTION":
		if in.InspectionAt == nil {
			return apperrors.InvalidRequest("inspection_at is required.")
		}
		if m.Status == "OPEN" {
			if err := move("INSPECTION_SCHEDULED"); err != nil {
				return err
			}
		} else if m.Status != "INSPECTION_SCHEDULED" {
			return apperrors.InvalidStateTransition(m.Status, "INSPECTION_SCHEDULED")
		}
		return s.q.SetMoveOutInspection(ctx, tx, db.SetMoveOutInspectionParams{ID: m.ID, InspectionAt: in.InspectionAt, UpdatedAt: now})
	case "COMPLETE_INSPECTION":
		c, err := s.q.GetChecklist(ctx, tx, db.GetChecklistParams{TenancyID: t.ID, Phase: "MOVE_OUT"})
		if err != nil {
			return err
		}
		if c.OwnerAckAt == nil {
			return precondition("MOVE_OUT_CHECKLIST_REQUIRED", "Record and submit the move-out condition checklist first.")
		}
		if err := move("INSPECTED"); err != nil {
			return err
		}
		return s.q.SetMoveOutInspected(ctx, tx, db.SetMoveOutInspectedParams{ID: m.ID, InspectedAt: &now})
	case "KEYS_RETURNED":
		n, err := s.q.UnreturnedKeyItems(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			return precondition("KEYS_OUTSTANDING", "Record the return of every key/access item first.")
		}
		return s.q.SetMoveOutKeysReturned(ctx, tx, db.SetMoveOutKeysReturnedParams{ID: m.ID, KeysReturnedAt: &now})
	default: // COMPLETE
		if in.ReturnToMarket == nil {
			return precondition("OWNER_CONFIRMATION_REQUIRED", "Confirm whether the property returns to the market.")
		}
		if dep, err := s.q.GetDepositByTenancy(ctx, tx, t.ID); err == nil {
			if !slices.Contains([]string{"RELEASED", "PARTIAL_RELEASED", "DISPUTED"}, dep.Status) || (dep.Status == "DISPUTED" && dep.ReleasedMinor == 0) {
				return precondition("DEPOSIT_SETTLEMENT_PENDING", "The deposit settlement has not been completed by payment-service.")
			}
		} else if !isNoRows(err) {
			return err
		} else if m.Status == "INSPECTED" {
			if err := move("SETTLEMENT_PENDING"); err != nil {
				return err
			}
		}
		if err := move("COMPLETED"); err != nil {
			return err
		}
		if err := s.q.SetMoveOutCompleted(ctx, tx, db.SetMoveOutCompletedParams{ID: m.ID, OwnerConfirmedAt: &now, ReturnToMarket: in.ReturnToMarket}); err != nil {
			return err
		}
		return s.endTenancy(ctx, t, d, *in.ReturnToMarket)
	}
}

// endTenancy ends occupancy: future schedule waived, tenancy.ended.v1, and
// availability AVAILABLE (owner confirmed) or OFF_MARKET.
func (s *Service) endTenancy(ctx context.Context, t db.Tenancy, d authz.Decision, returnToMarket bool) error {
	tx, now := mustTx(ctx), s.now()
	end := dayOf(now)
	if t.TerminationEffectiveDate != nil && t.TerminationEffectiveDate.After(end) {
		end = *t.TerminationEffectiveDate
	}
	future, err := s.q.FutureObligationsAfter(ctx, tx, db.FutureObligationsAfterParams{TenancyID: t.ID, PeriodStart: end})
	if err != nil {
		return err
	}
	for _, o := range future {
		if err := s.obligationTo(ctx, o, "WAIVED", "SYSTEM", "TENANCY_ENDED", nil, nil); err != nil {
			return err
		}
	}
	if err := s.tenancyTransition(ctx, t.ID, "ENDED", "move_out_completed", ""); err != nil {
		return err
	}
	if err := s.q.SetTenancyEnded(ctx, tx, db.SetTenancyEndedParams{ID: t.ID, EndedAt: &now, EndReason: ptr("MOVED_OUT")}); err != nil {
		return err
	}
	if t.ListingID != nil {
		av, err := s.q.GetAvailability(ctx, tx, *t.ListingID)
		if err == nil && av.Status == "OCCUPIED" {
			to := "OFF_MARKET"
			if returnToMarket {
				to = "AVAILABLE"
			}
			if _, _, err := s.transition(ctx, domain.Availability, *t.ListingID, lifecycle.Change{To: to, Command: "tenancy_ended",
				ExpectedVersion: av.Version, Metadata: map[string]any{"tenancy_id": t.ID}}); err != nil {
				return err
			}
		} else if err != nil && !isNoRows(err) {
			return err
		}
	}
	if err := s.noteDelegateActivity(ctx, d, t.PropertyID, "TENANCY_ENDED", AggTenancy, t.ID, "Tenancy ended after move-out"); err != nil {
		return err
	}
	if err := s.propertyHistory(ctx, t.PropertyID, "TENANCY_ENDED", "Tenancy ended after move-out", AggTenancy, t.ID); err != nil {
		return err
	}
	return s.emitTenancy(ctx, "tenancy.ended.v1", t.ID, &returnToMarket)
}
