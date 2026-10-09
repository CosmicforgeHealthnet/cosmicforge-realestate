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
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Inquiries and leads (§13.10). Business state only: human message content
// lives in CF Meet (a conversation reference is kept). Qualification uses a
// fixed set of lawful fields — owners cannot add free-form criteria.

// Owner notification templates for customer activity (seeded in notification-service).
const (
	TemplateNewInquiry          = "owner.new_inquiry"          // listing_title
	TemplateViewingRequested    = "owner.viewing_requested"    // listing_title, starts_at
	TemplateViewingReminder     = "owner.viewing_reminder"     // listing_title, starts_at
	TemplateApplicationReceived = "owner.application_received" // listing_title
	TemplateOfferResponded      = "owner.offer_responded"      // listing_title, decision
)

// InquiryView is an owner-facing inquiry.
type InquiryView struct {
	ID                  ids.ID             `json:"id"`
	PropertyID          ids.ID             `json:"property_id"`
	ListingID           ids.ID             `json:"listing_id"`
	CustomerRefID       ids.ID             `json:"customer_ref_id" doc:"Marketplace customer reference (personal data stays in the customer's domain)"`
	CustomerDisplayName string             `json:"customer_display_name"`
	Channel             string             `json:"channel"`
	Status              string             `json:"status"`
	Priority            string             `json:"priority"`
	ConversationRef     *string            `json:"conversation_ref" doc:"CF Meet conversation (message content is not stored here)"`
	IntendedMoveIn      *string            `json:"intended_move_in"`
	Occupants           *int32             `json:"occupants"`
	TermMonths          *int32             `json:"term_months"`
	BudgetWithinRange   *bool              `json:"budget_within_range"`
	HasPets             *bool              `json:"has_pets"`
	FollowUpAt          *time.Time         `json:"follow_up_at"`
	CloseReason         *string            `json:"close_reason"`
	AcknowledgedAt      *time.Time         `json:"acknowledged_at"`
	AllowedNextStatuses []string           `json:"allowed_next_statuses"`
	Activities          []LeadActivityView `json:"activities,omitempty"`
	Version             int64              `json:"version"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

// LeadActivityView is one lead activity (owner notes and pipeline history).
type LeadActivityView struct {
	ID           ids.ID    `json:"id"`
	ActivityType string    `json:"activity_type"`
	Note         *string   `json:"note"`
	RefType      *string   `json:"ref_type"`
	RefID        *ids.ID   `json:"ref_id"`
	ActorDomain  *string   `json:"actor_domain"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func inquiryView(i db.Inquiry) InquiryView {
	return InquiryView{ID: i.ID, PropertyID: i.PropertyID, ListingID: i.ListingID, CustomerRefID: i.CustomerRefID, CustomerDisplayName: i.CustomerDisplayName,
		Channel: i.Channel, Status: i.Status, Priority: i.Priority, ConversationRef: i.ConversationRef, IntendedMoveIn: dateStr(i.IntendedMoveIn),
		Occupants: i.Occupants, TermMonths: i.TermMonths, BudgetWithinRange: i.BudgetWithinRange, HasPets: i.HasPets, FollowUpAt: i.FollowUpAt,
		CloseReason: i.CloseReason, AcknowledgedAt: i.AcknowledgedAt, AllowedNextStatuses: nonNil(domain.Inquiry.Targets(i.Status)), Version: i.Version,
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
}

func (s *Service) inquiryFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.Inquiry, authz.Decision, error) {
	i, err := s.q.GetInquiry(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return i, authz.Decision{}, notFound("inquiry")
	}
	if err != nil {
		return i, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, i.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound("inquiry")
	}
	return i, d, err
}

func (s *Service) leadActivity(ctx context.Context, i db.Inquiry, typ string, note *string, refType string, refID ids.ID) error {
	dom, actor := "", ids.Nil
	if a := actorFromCtx(ctx); a != nil {
		dom, actor = a.Domain, a.ID
	}
	_, err := s.q.InsertLeadActivity(ctx, mustTx(ctx), db.InsertLeadActivityParams{ID: ids.New(), InquiryID: i.ID, PropertyID: i.PropertyID, ActivityType: typ,
		Note: note, RefType: strp(refType), RefID: idp(refID), ActorDomain: strp(dom), ActorID: idp(actor), OccurredAt: s.now()})
	return err
}

// GetInquiry returns an inquiry with its activity history.
func (s *Service) GetInquiry(ctx context.Context, subj authz.Subject, id ids.ID) (InquiryView, error) {
	i, _, err := s.inquiryFor(ctx, subj, id, authz.Read(authz.CapLeadsManage))
	if err != nil {
		return InquiryView{}, err
	}
	v := inquiryView(i)
	acts, err := s.q.ListLeadActivities(ctx, s.dbq(ctx), id)
	v.Activities = []LeadActivityView{}
	for _, a := range acts {
		v.Activities = append(v.Activities, LeadActivityView{ID: a.ID, ActivityType: a.ActivityType, Note: a.Note, RefType: a.RefType, RefID: a.RefID,
			ActorDomain: a.ActorDomain, OccurredAt: a.OccurredAt})
	}
	return v, err
}

// ListInquiries pages inquiries across the caller's properties (pipeline).
func (s *Service) ListInquiries(ctx context.Context, subj authz.Subject, listingID ids.ID, status, cursor string, limit int) (pagination.Response[InquiryView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[InquiryView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[InquiryView]{}, err
	}
	rows, err := s.q.ListInquiriesForAccount(ctx, s.dbq(ctx), db.ListInquiriesForAccountParams{AccountID: accountOf(subj), Now: s.now(), ListingID: idp(listingID),
		Status: strp(status), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[InquiryView]{}, err
	}
	out := make([]InquiryView, 0, len(rows))
	for _, r := range rows {
		out = append(out, inquiryView(r))
	}
	return pagination.Build(out, limit, func(v InquiryView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

// InquiryPatch updates the owner's pipeline fields and adds a note.
type InquiryPatch struct {
	Priority   *string    `json:"priority,omitempty" v:"enum=LOW|NORMAL|HIGH"`
	FollowUpAt *time.Time `json:"follow_up_at,omitempty" doc:"Reminder time"`
	Note       *string    `json:"note,omitempty" v:"max=2000" doc:"Owner note (not a message to the customer)"`
}

// UpdateInquiry patches pipeline fields (If-Match).
func (s *Service) UpdateInquiry(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in InquiryPatch) (InquiryView, error) {
	if err := schema.Validate(in); err != nil {
		return InquiryView{}, err
	}
	var out InquiryView
	err := s.InTx(ctx, func(ctx context.Context) error {
		i, d, err := s.inquiryFor(ctx, subj, id, authz.Do(authz.CapLeadsManage))
		if err != nil {
			return err
		}
		prio, follow := i.Priority, i.FollowUpAt
		if in.Priority != nil {
			prio = *in.Priority
		}
		if in.FollowUpAt != nil {
			follow = in.FollowUpAt
		}
		ni, err := s.q.UpdateInquiryWork(ctx, mustTx(ctx), db.UpdateInquiryWorkParams{ID: id, Version: expected, Priority: prio, FollowUpAt: follow, UpdatedAt: s.now()})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		if in.Note != nil {
			if err := s.leadActivity(ctx, ni, "NOTE", in.Note, "", ids.Nil); err != nil {
				return err
			}
		}
		if in.FollowUpAt != nil {
			if err := s.leadActivity(ctx, ni, "REMINDER", nil, "", ids.Nil); err != nil {
				return err
			}
		}
		if err := s.noteDelegateActivity(ctx, d, i.PropertyID, "LEAD_UPDATED", "inquiry", id, "Lead updated"); err != nil {
			return err
		}
		out = inquiryView(ni)
		return nil
	})
	return out, err
}

func (s *Service) moveInquiry(ctx context.Context, i db.Inquiry, to, cmd string) error {
	if i.Status == to || !domain.Inquiry.Can(i.Status, to) {
		return nil // pipeline advancement is best-effort (never regresses)
	}
	if _, _, err := s.transition(ctx, domain.Inquiry, i.ID, lifecycle.Change{To: to, Command: cmd}); err != nil {
		return err
	}
	return s.leadActivity(ctx, i, "STATUS_CHANGE", strp(i.Status+" -> "+to), "", ids.Nil)
}

// AcknowledgeInquiry marks an inquiry acknowledged.
func (s *Service) AcknowledgeInquiry(ctx context.Context, subj authz.Subject, id ids.ID) (InquiryView, error) {
	var out InquiryView
	err := s.InTx(ctx, func(ctx context.Context) error {
		i, _, err := s.inquiryFor(ctx, subj, id, authz.Do(authz.CapLeadsManage))
		if err != nil {
			return err
		}
		if _, _, err := s.transition(ctx, domain.Inquiry, id, lifecycle.Change{To: "ACKNOWLEDGED", Command: "acknowledge"}); err != nil {
			return err
		}
		if err := s.q.SetInquiryAcknowledged(ctx, mustTx(ctx), db.SetInquiryAcknowledgedParams{ID: id, UpdatedAt: s.now()}); err != nil {
			return err
		}
		if err := s.leadActivity(ctx, i, "STATUS_CHANGE", strp("NEW -> ACKNOWLEDGED"), "", ids.Nil); err != nil {
			return err
		}
		ni, err := s.q.GetInquiry(ctx, mustTx(ctx), id)
		out = inquiryView(ni)
		return err
	})
	return out, err
}

// QualifyInput records lawful qualification fields and the outcome.
type QualifyInput struct {
	Outcome           string  `json:"outcome" v:"required,enum=QUALIFIED|CLOSED_LOST"`
	IntendedMoveIn    *string `json:"intended_move_in,omitempty" v:"format=date"`
	Occupants         *int32  `json:"occupants,omitempty" v:"min=1,max=100"`
	TermMonths        *int32  `json:"term_months,omitempty" v:"min=1,max=600"`
	BudgetWithinRange *bool   `json:"budget_within_range,omitempty"`
	HasPets           *bool   `json:"has_pets,omitempty"`
	ReasonCode        *string `json:"reason_code,omitempty" v:"enum=BUDGET_MISMATCH|DATES_MISMATCH|OCCUPANCY_LIMIT|PETS_NOT_ALLOWED|UNRESPONSIVE|NOT_INTERESTED|LISTING_UNAVAILABLE|DUPLICATE" doc:"Required for CLOSED_LOST; only lawful, configured reasons"`
}

// QualifyInquiry qualifies (or closes) an inquiry.
func (s *Service) QualifyInquiry(ctx context.Context, subj authz.Subject, id ids.ID, in QualifyInput) (InquiryView, error) {
	if err := schema.Validate(in); err != nil {
		return InquiryView{}, err
	}
	if in.Outcome == "CLOSED_LOST" && in.ReasonCode == nil {
		return InquiryView{}, apperrors.InvalidRequest("reason_code is required when closing a lead.")
	}
	var out InquiryView
	err := s.InTx(ctx, func(ctx context.Context) error {
		i, d, err := s.inquiryFor(ctx, subj, id, authz.Do(authz.CapLeadsManage))
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if i.Status == "NEW" && in.Outcome == "QUALIFIED" {
			if _, _, err := s.transition(ctx, domain.Inquiry, id, lifecycle.Change{To: "ACKNOWLEDGED", Command: "acknowledge"}); err != nil {
				return err
			}
			if err := s.q.SetInquiryAcknowledged(ctx, tx, db.SetInquiryAcknowledgedParams{ID: id, UpdatedAt: now}); err != nil {
				return err
			}
		}
		if _, _, err := s.transition(ctx, domain.Inquiry, id, lifecycle.Change{To: in.Outcome, Command: "qualify", Reason: deref(in.ReasonCode)}); err != nil {
			return err
		}
		pick32 := func(cur, nv *int32) *int32 {
			if nv != nil {
				return nv
			}
			return cur
		}
		pickb := func(cur, nv *bool) *bool {
			if nv != nil {
				return nv
			}
			return cur
		}
		move := i.IntendedMoveIn
		if in.IntendedMoveIn != nil {
			move = schema.ParseDate(in.IntendedMoveIn)
		}
		if err := s.q.SetInquiryQualification(ctx, tx, db.SetInquiryQualificationParams{ID: id, IntendedMoveIn: move, Occupants: pick32(i.Occupants, in.Occupants),
			TermMonths: pick32(i.TermMonths, in.TermMonths), BudgetWithinRange: pickb(i.BudgetWithinRange, in.BudgetWithinRange), HasPets: pickb(i.HasPets, in.HasPets),
			UpdatedAt: now}); err != nil {
			return err
		}
		if in.ReasonCode != nil {
			if err := s.q.SetInquiryCloseReason(ctx, tx, db.SetInquiryCloseReasonParams{ID: id, CloseReason: in.ReasonCode, UpdatedAt: now}); err != nil {
				return err
			}
		}
		if err := s.leadActivity(ctx, i, "QUALIFICATION", strp(in.Outcome), "", ids.Nil); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, i.PropertyID, "LEAD_QUALIFIED", "inquiry", id, in.Outcome); err != nil {
			return err
		}
		if err := s.record(ctx, "lead.qualify", "inquiry", id, map[string]any{"outcome": in.Outcome, "reason_code": in.ReasonCode}, nil); err != nil {
			return err
		}
		ni, err := s.q.GetInquiry(ctx, tx, id)
		out = inquiryView(ni)
		return err
	})
	return out, err
}

// CloseInquiryInput closes an inquiry as won or lost.
type CloseInquiryInput struct {
	Outcome    string `json:"outcome" v:"required,enum=CLOSED_WON|CLOSED_LOST"`
	ReasonCode string `json:"reason_code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
}

// CloseInquiry closes the lead.
func (s *Service) CloseInquiry(ctx context.Context, subj authz.Subject, id ids.ID, in CloseInquiryInput) (InquiryView, error) {
	if err := schema.Validate(in); err != nil {
		return InquiryView{}, err
	}
	var out InquiryView
	err := s.InTx(ctx, func(ctx context.Context) error {
		i, _, err := s.inquiryFor(ctx, subj, id, authz.Do(authz.CapLeadsManage))
		if err != nil {
			return err
		}
		if _, _, err := s.transition(ctx, domain.Inquiry, id, lifecycle.Change{To: in.Outcome, Command: "close", Reason: in.ReasonCode}); err != nil {
			return err
		}
		if err := s.q.SetInquiryCloseReason(ctx, mustTx(ctx), db.SetInquiryCloseReasonParams{ID: id, CloseReason: &in.ReasonCode, UpdatedAt: s.now()}); err != nil {
			return err
		}
		if err := s.leadActivity(ctx, i, "STATUS_CHANGE", strp(i.Status+" -> "+in.Outcome), "", ids.Nil); err != nil {
			return err
		}
		ni, err := s.q.GetInquiry(ctx, mustTx(ctx), id)
		out = inquiryView(ni)
		return err
	})
	return out, err
}

// ---- marketplace intake (internal, customer actor) ----

// requireCustomer authorizes marketplace-originated customer intake and
// returns the customer reference (the delegated marketplace actor).
func (s *Service) requireCustomer(ctx context.Context, subj authz.Subject) (ids.ID, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalCustomerIntake, authz.Resource{}); err != nil {
		return ids.Nil, err
	}
	if subj.ActorDomain != platform.DomainMarketplace {
		return ids.Nil, apperrors.PermissionDenied("A marketplace customer actor is required.")
	}
	return subj.ActorID, nil
}

// bookableListing loads a published listing whose property can transact.
func (s *Service) bookableListing(ctx context.Context, id ids.ID, txns ...string) (db.Listing, error) {
	l, err := s.q.GetListing(ctx, s.dbq(ctx), id)
	if isNoRows(err) || (err == nil && l.Status != "PUBLISHED") {
		return l, apperrors.NotFound("LISTING_NOT_AVAILABLE", "The listing is not available.")
	}
	if err != nil {
		return l, err
	}
	if len(txns) > 0 && !slices.Contains(txns, l.TransactionType) {
		return l, precondition("TRANSACTION_TYPE_MISMATCH", "This action does not apply to "+l.TransactionType+" listings.")
	}
	st, err := s.q.PropertyAuthorityState(ctx, s.dbq(ctx), db.PropertyAuthorityStateParams{Now: s.now(), PropertyID: l.PropertyID})
	if err != nil {
		return l, err
	}
	if st.Status != "ACTIVE" || !st.AuthorityVerified {
		return l, apperrors.NotFound("LISTING_NOT_AVAILABLE", "The listing is not available.")
	}
	return l, nil
}

// CustomerInquiryInput is a marketplace-originated inquiry.
type CustomerInquiryInput struct {
	ListingID           ids.ID  `json:"listing_id" v:"required"`
	CustomerDisplayName string  `json:"customer_display_name" v:"required,max=120"`
	Channel             string  `json:"channel,omitempty" v:"enum=MARKETPLACE|CF_MEET|EXTERNAL"`
	ConversationRef     *string `json:"conversation_ref,omitempty" v:"max=128"`
	IntendedMoveIn      *string `json:"intended_move_in,omitempty" v:"format=date"`
	Occupants           *int32  `json:"occupants,omitempty" v:"min=1,max=100"`
	TermMonths          *int32  `json:"term_months,omitempty" v:"min=1,max=600"`
	ExternalRef         *string `json:"external_ref,omitempty" v:"max=128" doc:"Marketplace inquiry reference (idempotent matching)"`
}

// CustomerCreateInquiry records a marketplace inquiry for a published listing.
func (s *Service) CustomerCreateInquiry(ctx context.Context, subj authz.Subject, in CustomerInquiryInput) (InquiryView, error) {
	if err := schema.Validate(in); err != nil {
		return InquiryView{}, err
	}
	var out InquiryView
	err := s.InTx(ctx, func(ctx context.Context) error {
		customer, err := s.requireCustomer(ctx, subj)
		if err != nil {
			return err
		}
		l, err := s.bookableListing(ctx, in.ListingID)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if in.ExternalRef != nil {
			if ex, err := s.q.GetInquiryByExternalRef(ctx, tx, db.GetInquiryByExternalRefParams{ListingID: l.ID, ExternalRef: in.ExternalRef}); err == nil {
				if ex.CustomerRefID != customer {
					return conflict("EXTERNAL_REF_CONFLICT", "external_ref belongs to another inquiry.")
				}
				out = inquiryView(ex)
				return nil
			} else if !isNoRows(err) {
				return err
			}
		}
		ch := in.Channel
		if ch == "" {
			ch = "MARKETPLACE"
		}
		i, err := s.q.InsertInquiry(ctx, tx, db.InsertInquiryParams{ID: ids.New(), PropertyID: l.PropertyID, ListingID: l.ID, CustomerRefID: customer,
			CustomerDisplayName: in.CustomerDisplayName, Channel: ch, ConversationRef: in.ConversationRef, IntendedMoveIn: schema.ParseDate(in.IntendedMoveIn),
			Occupants: in.Occupants, TermMonths: in.TermMonths, ExternalRef: in.ExternalRef, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := domain.Inquiry.RecordCreation(ctx, tx, i.ID, &i.PropertyID, "NEW", now); err != nil {
			return err
		}
		if err := s.leadActivity(ctx, i, "STATUS_CHANGE", strp("created via "+ch), "listing", l.ID); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateNewInquiry, map[string]string{"listing_title": l.Title}); err != nil {
			return err
		}
		out = inquiryView(i)
		return s.record(ctx, "lead.create", "inquiry", i.ID, map[string]any{"channel": ch, "listing_id": l.ID}, nil)
	})
	return out, err
}

// ListCampaigns / CreateCampaign: promoted listings and external campaigns
// reference marketplace commercial capability (and payment-service for
// paid promotion), neither of which has an implemented contract (§13.10).
func (s *Service) Campaigns(ctx context.Context, subj authz.Subject) ([]InquiryView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return nil, err
	}
	return nil, unavailable("Listing campaigns and promotion", "marketplace-service promotion contract")
}

// RunLeadFollowUps records due follow-up reminders on the lead history.
func (s *Service) RunLeadFollowUps(ctx context.Context, batch int32) (int, error) {
	n := 0
	err := s.InTx(ctx, func(ctx context.Context) error {
		due, err := s.q.DueInquiryFollowUps(ctx, mustTx(ctx), db.DueInquiryFollowUpsParams{Now: s.now(), MaxRows: batch})
		if err != nil {
			return err
		}
		for _, i := range due {
			if err := s.leadActivity(ctx, i, "REMINDER", strp("Follow-up due"), "", ids.Nil); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
