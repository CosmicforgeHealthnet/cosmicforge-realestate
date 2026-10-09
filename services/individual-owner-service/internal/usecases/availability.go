package usecases

import (
	"context"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Availability and freshness (§13.9). Long-term listings use the §7 state
// set with effective dates; short-let date inventory lives in
// availability_calendar. Published listings need periodic confirmation:
// reminders first, then a system pause with reason; Marketplace receives
// availability/freshness events.

// Owner-domain notification templates (seeded in notification-service).
const (
	TemplateListingFreshness = "owner.listing_freshness_reminder" // variables: listing_title, due_at
	TemplateListingPaused    = "owner.listing_paused"             // variables: listing_title, reason
)

// notifyOwner queues a durable notification to the property owner of record.
func (s *Service) notifyOwner(ctx context.Context, accountID ids.ID, template string, vars map[string]string) error {
	tx := mustTx(ctx)
	acc, err := s.q.GetAccount(ctx, tx, accountID)
	if err != nil {
		return err
	}
	channels := []string{notification.ChannelPush}
	var email *string
	if acc.EmailVerifiedAt != nil {
		email = &acc.Email
		channels = append(channels, notification.ChannelEmail)
	}
	return s.q.InsertDispatch(ctx, tx, db.InsertDispatchParams{DispatchID: ids.New(), AccountID: accountID, TemplateKey: template, Channels: channels,
		Variables: jsonb(vars), DestinationEmail: email, NextAttemptAt: s.now()})
}

// GetAvailability returns a listing's availability.
func (s *Service) GetAvailability(ctx context.Context, subj authz.Subject, listingID ids.ID) (AvailabilityView, error) {
	l, _, err := s.listingFor(ctx, subj, listingID, authz.Read(authz.CapAvailability))
	if err != nil {
		return AvailabilityView{}, err
	}
	a, err := s.q.GetAvailability(ctx, s.dbq(ctx), l.ID)
	if err != nil {
		return AvailabilityView{}, err
	}
	return *availabilityView(a), nil
}

// AvailabilityInput sets the owner-controlled availability.
type AvailabilityInput struct {
	Status         string  `json:"status" v:"required,enum=AVAILABLE|TEMP_UNAVAILABLE|OFF_MARKET|OCCUPIED|SOLD" doc:"RESERVED/APPLICATION_UNDER_REVIEW/OFFER_ACCEPTED are set by applications and offers; OCCUPIED with a future available_from keeps the listing live (re-letting, occupant consent gates viewings)"`
	AvailableFrom  *string `json:"available_from,omitempty" v:"format=date"`
	AvailableUntil *string `json:"available_until,omitempty" v:"format=date"`
	Note           *string `json:"note,omitempty" v:"max=500"`
}

// PutAvailability changes availability (If-Match on the availability
// version). It counts as a confirmation; a status a published listing cannot
// be offered in pauses the listing.
func (s *Service) PutAvailability(ctx context.Context, subj authz.Subject, listingID ids.ID, expected int64, in AvailabilityInput) (AvailabilityView, error) {
	if err := schema.Validate(in); err != nil {
		return AvailabilityView{}, err
	}
	from, until := schema.ParseDate(in.AvailableFrom), schema.ParseDate(in.AvailableUntil)
	if from != nil && until != nil && until.Before(*from) {
		return AvailabilityView{}, apperrors.InvalidRequest("available_until must not be before available_from.")
	}
	var out AvailabilityView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, d, err := s.listingForUpdate(ctx, subj, listingID, authz.Do(authz.CapAvailability))
		if err != nil {
			return err
		}
		if in.Status == "SOLD" && l.TransactionType != "SALE" {
			return apperrors.InvalidRequest("SOLD applies to sale listings only.")
		}
		if l.Status == "WITHDRAWN" || l.Status == "ARCHIVED" {
			return conflict("LISTING_CLOSED", "The listing is "+l.Status+".")
		}
		tx := mustTx(ctx)
		a, err := s.q.GetAvailability(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		if err := checkVersion(max(expected, 1), a.Version); err != nil {
			return err
		}
		if a.Status != in.Status {
			if _, _, err := s.transition(ctx, domain.Availability, l.ID, lifecycle.Change{To: in.Status, Command: "owner_set", ExpectedVersion: a.Version}); err != nil {
				return err
			}
		}
		if _, err := s.q.SetAvailabilityWindow(ctx, tx, db.SetAvailabilityWindowParams{ID: l.ID, AvailableFrom: from, AvailableUntil: until, Note: in.Note,
			UpdatedAt: s.now()}); err != nil {
			return err
		}
		// An occupied property may be re-let ahead of move-out when a future
		// available_from is given; otherwise these states take it off the market.
		reletting := in.Status == "OCCUPIED" && from != nil && from.After(s.now())
		if l.Status == "PUBLISHED" && slices.Contains([]string{"OFF_MARKET", "OCCUPIED", "SOLD"}, in.Status) && !reletting {
			if err := s.pauseTx(ctx, l, "SYSTEM", "AVAILABILITY_"+in.Status); err != nil {
				return err
			}
		}
		if err := s.confirmTx(ctx, subj, l, "AVAILABILITY_CHANGED"); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, l.PropertyID, "AVAILABILITY_SET", AggListing, l.ID, in.Status); err != nil {
			return err
		}
		na, err := s.q.GetAvailability(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		out = *availabilityView(na)
		return nil
	})
	return out, err
}

// ConfirmAvailabilityInput optionally refreshes the start date.
type ConfirmAvailabilityInput struct {
	AvailableFrom *string `json:"available_from,omitempty" v:"format=date"`
}

// ConfirmAvailability confirms a listing is still available (freshness). A
// listing paused only for missed freshness resumes when it is publishable.
func (s *Service) ConfirmAvailability(ctx context.Context, subj authz.Subject, listingID ids.ID, in ConfirmAvailabilityInput) (ListingView, error) {
	if err := schema.Validate(in); err != nil {
		return ListingView{}, err
	}
	var out ListingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, d, err := s.listingForUpdate(ctx, subj, listingID, authz.Do(authz.CapAvailability))
		if err != nil {
			return err
		}
		if !slices.Contains([]string{"PUBLISHED", "PAUSED", "READY_FOR_REVIEW", "DRAFT"}, l.Status) {
			return conflict("LISTING_CLOSED", "The listing is "+l.Status+".")
		}
		tx := mustTx(ctx)
		a, err := s.q.GetAvailability(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		if a.Status != "AVAILABLE" && a.Status != "TEMP_UNAVAILABLE" {
			return conflict("NOT_AVAILABLE", "Availability is "+a.Status+"; update it instead of confirming.")
		}
		if in.AvailableFrom != nil {
			if _, err := s.q.SetAvailabilityWindow(ctx, tx, db.SetAvailabilityWindowParams{ID: l.ID, AvailableFrom: schema.ParseDate(in.AvailableFrom),
				AvailableUntil: a.AvailableUntil, Note: a.Note, UpdatedAt: s.now()}); err != nil {
				return err
			}
		}
		if err := s.confirmTx(ctx, subj, l, "CONFIRMED"); err != nil {
			return err
		}
		if l.Status == "PAUSED" && deref(l.PauseSource) == "SYSTEM" && deref(l.PauseReason) == "FRESHNESS_EXPIRED" {
			if problems, err := s.readiness(ctx, l); err != nil {
				return err
			} else if len(problems) == 0 {
				if _, _, err := s.transition(ctx, domain.Listing, l.ID, lifecycle.Change{To: "PUBLISHED", Command: "resume_after_confirmation"}); err != nil {
					return err
				}
				pl, err := s.q.GetListingForUpdate(ctx, tx, l.ID)
				if err != nil {
					return err
				}
				nl, snap, err := s.writeVersion(ctx, subj, pl, "RESUME", "RESUMED")
				if err != nil {
					return err
				}
				if err := s.scheduleFreshness(ctx, nl, "SCHEDULED", nil); err != nil {
					return err
				}
				if err := s.emitListing(ctx, "listing.published.v1", nl, snap, "RESUME"); err != nil {
					return err
				}
			}
		}
		if err := s.noteDelegateActivity(ctx, d, l.PropertyID, "AVAILABILITY_CONFIRMED", AggListing, l.ID, l.Title); err != nil {
			return err
		}
		nl, err := s.q.GetListing(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		out, err = s.listingView(ctx, nl)
		return err
	})
	return out, err
}

// confirmTx resolves the open freshness check, schedules the next one and
// emits listing.availability_confirmed.v1 for live listings.
func (s *Service) confirmTx(ctx context.Context, subj authz.Subject, l db.Listing, reason string) error {
	tx, now := mustTx(ctx), s.now()
	if _, err := s.q.ResolveFreshnessChecks(ctx, tx, db.ResolveFreshnessChecksParams{ListingID: l.ID, Status: "CONFIRMED", ResolvedAt: &now}); err != nil {
		return err
	}
	next := now.Add(s.freshnessInterval(l.TransactionType))
	if _, err := s.q.SetAvailabilityConfirmation(ctx, tx, db.SetAvailabilityConfirmationParams{ID: l.ID, LastConfirmedAt: &now, NextConfirmationDue: &next}); err != nil {
		return err
	}
	if l.Status != "PUBLISHED" && l.Status != "PAUSED" {
		return nil
	}
	if _, err := s.q.InsertFreshnessCheck(ctx, tx, db.InsertFreshnessCheckParams{ID: ids.New(), ListingID: l.ID, Trigger: "SCHEDULED", DueAt: next, CreatedAt: now}); err != nil {
		return err
	}
	if err := s.q.InsertListingPublication(ctx, tx, db.InsertListingPublicationParams{ID: ids.New(), ListingID: l.ID, VersionNo: &l.CurrentVersionNo,
		Action: "AVAILABILITY_CONFIRMED", Source: sourceOf(subj), Reason: &reason, OccurredAt: now}); err != nil {
		return err
	}
	nl, err := s.q.GetListing(ctx, tx, l.ID)
	if err != nil {
		return err
	}
	return s.emitListingState(ctx, "listing.availability_confirmed.v1", nl, sourceOf(subj), &reason)
}

func (s *Service) freshnessInterval(txn string) time.Duration {
	if d, ok := s.Policy.Domain.FreshnessInterval[txn]; ok && d > 0 {
		return d
	}
	return 14 * 24 * time.Hour
}

// scheduleFreshness opens the next freshness check for a published listing.
func (s *Service) scheduleFreshness(ctx context.Context, l db.Listing, trigger string, report *string) error {
	tx, now := mustTx(ctx), s.now()
	due := now.Add(s.freshnessInterval(l.TransactionType))
	if trigger == "REPORT" {
		due = now
	}
	if _, err := s.q.InsertFreshnessCheck(ctx, tx, db.InsertFreshnessCheckParams{ID: ids.New(), ListingID: l.ID, Trigger: trigger, DueAt: due,
		ReportType: report, CreatedAt: now}); err != nil {
		return err
	}
	if trigger == "SCHEDULED" {
		_, err := s.q.SetAvailabilityConfirmation(ctx, tx, db.SetAvailabilityConfirmationParams{ID: l.ID, LastConfirmedAt: &now, NextConfirmationDue: &due})
		return err
	}
	return nil
}

// RunFreshness sends reminders before due dates and system-pauses published
// listings whose confirmation is overdue past the grace period (§11).
func (s *Service) RunFreshness(ctx context.Context, batch int32) (reminded, paused int, err error) {
	ctx = requestcontext.Job(ctx, "owner-freshness")
	err = s.InTx(ctx, func(ctx context.Context) error {
		tx, now := mustTx(ctx), s.now()
		due, err := s.q.DueFreshnessReminders(ctx, tx, db.DueFreshnessRemindersParams{Now: now, RemindBefore: now.Add(s.Policy.Domain.FreshnessReminderLead), MaxRows: batch})
		if err != nil {
			return err
		}
		for _, f := range due {
			l, err := s.q.GetListing(ctx, tx, f.ListingID)
			if err != nil {
				return err
			}
			p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
			if err != nil {
				return err
			}
			if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateListingFreshness, map[string]string{"listing_title": l.Title,
				"due_at": f.DueAt.Format(time.RFC3339)}); err != nil {
				return err
			}
			if err := s.propertyHistory(ctx, l.PropertyID, "FRESHNESS_REMINDER", "Availability confirmation requested", AggListing, l.ID); err != nil {
				return err
			}
			reminded++
		}
		misses, err := s.q.DueFreshnessMisses(ctx, tx, db.DueFreshnessMissesParams{Overdue: now.Add(-s.Policy.Domain.FreshnessGrace), MaxRows: batch})
		if err != nil {
			return err
		}
		for _, f := range misses {
			if _, err := s.q.ResolveFreshnessChecks(ctx, tx, db.ResolveFreshnessChecksParams{ListingID: f.ListingID, Status: "MISSED", ResolvedAt: &now}); err != nil {
				return err
			}
			l, err := s.q.GetListingForUpdate(ctx, tx, f.ListingID)
			if err != nil {
				return err
			}
			if l.Status != "PUBLISHED" {
				continue
			}
			if err := s.pauseTx(ctx, l, "SYSTEM", "FRESHNESS_EXPIRED"); err != nil {
				return err
			}
			p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
			if err != nil {
				return err
			}
			if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateListingPaused, map[string]string{"listing_title": l.Title, "reason": "availability not confirmed"}); err != nil {
				return err
			}
			paused++
		}
		return nil
	})
	return reminded, paused, err
}

// ListingReportInput is a marketplace customer report (§10.1 marketplace
// report events routed back to the authoritative owner service).
type ListingReportInput struct {
	ReportType string  `json:"report_type" v:"required,enum=STALE|UNAVAILABLE|DUPLICATE|MISLEADING|PRICE_MISMATCH|FRAUD_SUSPECTED"`
	ReportRef  *string `json:"report_ref,omitempty" v:"max=128" doc:"Marketplace report reference"`
}

// ReportView acknowledges a report.
type ReportView struct {
	ListingID ids.ID `json:"listing_id"`
	Action    string `json:"action" doc:"FRESHNESS_CHECK_DUE_NOW (owner must re-confirm) or RECORDED_FOR_REVIEW"`
}

// InternalReportListing records a marketplace report: stale/unavailable/price
// reports force an immediate freshness check; others are recorded for review.
func (s *Service) InternalReportListing(ctx context.Context, subj authz.Subject, listingID ids.ID, in ListingReportInput) (ReportView, error) {
	if err := schema.Validate(in); err != nil {
		return ReportView{}, err
	}
	var out ReportView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalListingReport, authz.Resource{}); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		l, err := s.q.GetListingForUpdate(ctx, tx, listingID)
		if isNoRows(err) || (err == nil && l.PublishedVersionNo == nil) {
			return notFound(AggListing)
		}
		if err != nil {
			return err
		}
		out = ReportView{ListingID: l.ID, Action: "RECORDED_FOR_REVIEW"}
		if slices.Contains([]string{"STALE", "UNAVAILABLE", "PRICE_MISMATCH"}, in.ReportType) && l.Status == "PUBLISHED" {
			if err := s.scheduleFreshness(ctx, l, "REPORT", &in.ReportType); err != nil {
				return err
			}
			out.Action = "FRESHNESS_CHECK_DUE_NOW"
		}
		if err := s.q.InsertListingPublication(ctx, tx, db.InsertListingPublicationParams{ID: ids.New(), ListingID: l.ID, VersionNo: &l.CurrentVersionNo,
			Action: "REPORTED", Source: "MARKETPLACE", Reason: &in.ReportType, OccurredAt: now}); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, l.PropertyID, "LISTING_REPORTED", "Marketplace report: "+in.ReportType, AggListing, l.ID); err != nil {
			return err
		}
		return s.record(ctx, "listing.report", AggListing, l.ID, map[string]any{"report_type": in.ReportType}, map[string]any{"report_ref": in.ReportRef})
	})
	return out, err
}

// ModerationInput is an admin moderation action.
type ModerationInput struct {
	Action     string `json:"action" v:"required,enum=PAUSE|WITHDRAW|RELEASE" doc:"RELEASE lifts an admin pause (the listing resumes when publishable)"`
	ReasonCode string `json:"reason_code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
}

// ModerateListing applies an admin moderation action (service API, §9).
func (s *Service) ModerateListing(ctx context.Context, subj authz.Subject, listingID ids.ID, in ModerationInput) (ListingView, error) {
	if err := schema.Validate(in); err != nil {
		return ListingView{}, err
	}
	var out ListingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminModerate, authz.Resource{}); err != nil {
			return err
		}
		tx := mustTx(ctx)
		l, err := s.q.GetListingForUpdate(ctx, tx, listingID)
		if isNoRows(err) {
			return notFound(AggListing)
		}
		if err != nil {
			return err
		}
		switch in.Action {
		case "PAUSE":
			if l.Status != "PUBLISHED" {
				return apperrors.InvalidStateTransition(l.Status, "PAUSED")
			}
			err = s.pauseTx(ctx, l, "ADMIN", in.ReasonCode)
		case "WITHDRAW":
			err = s.closeListingTx(ctx, l, "WITHDRAWN", "ADMIN", in.ReasonCode)
		case "RELEASE":
			if l.Status != "PAUSED" || deref(l.PauseSource) != "ADMIN" {
				return conflict("LISTING_NOT_ADMIN_PAUSED", "Only an admin-paused listing can be released.")
			}
			// The listing stays paused (owner-resumable) until it passes checks.
			src, reason := "OWNER", "ADMIN_RELEASED"
			err = s.q.SetListingPause(ctx, tx, db.SetListingPauseParams{ID: l.ID, PauseSource: &src, PauseReason: &reason, UpdatedAt: s.now()})
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "listing.moderate", AggListing, l.ID, map[string]any{"action": in.Action, "reason_code": in.ReasonCode}, nil); err != nil {
			return err
		}
		nl, err := s.q.GetListing(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		out, err = s.listingView(ctx, nl)
		return err
	})
	return out, err
}
