package usecases

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Stay extensions (§13.20). A checked-in guest (through marketplace) or the
// owner proposes a later check-out; the other side accepts. The booking's
// accepted price snapshot never changes: each extension carries its own
// immutable snapshot for the added nights from the same cost engine (per-
// stay fees and the deposit are not charged again). The added nights and
// the moved turnover buffer are claimed on the calendar under the
// extension's id while it is open, under the same per-listing lock as
// booking holds, so an extension can never overlap another booking. On
// platform collection the extension is applied only once payment-service
// reports the added amount paid; off-platform it is applied on acceptance.
// Pre-arrival date changes go through cancellation and a new booking.

const aggExtension = "shortlet_booking_extension"

// ExtensionInput asks for a later check-out.
type ExtensionInput struct {
	NewCheckOut string `json:"new_check_out" v:"required,format=date" doc:"Later than the current check-out"`
}

// ExtensionDecision answers an open extension.
type ExtensionDecision struct {
	Decision   string  `json:"decision" v:"required,enum=ACCEPT|DECLINE"`
	ReasonCode *string `json:"reason_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}" doc:"Required to decline"`
}

// ExtensionView is a stay extension.
type ExtensionView struct {
	ID                ids.ID          `json:"id"`
	BookingID         ids.ID          `json:"booking_id"`
	RequestedBy       string          `json:"requested_by" doc:"GUEST or OWNER"`
	Awaiting          *string         `json:"awaiting" doc:"Who must answer: GUEST or OWNER (null once answered)"`
	Status            string          `json:"status" doc:"REQUESTED, PAYMENT_PENDING, APPLIED, DECLINED, EXPIRED, CANCELLED"`
	PreviousCheckOut  string          `json:"previous_check_out"`
	NewCheckOut       string          `json:"new_check_out"`
	PricingVersionID  ids.ID          `json:"pricing_version_id"`
	Cost              json.RawMessage `json:"cost" doc:"Immutable price snapshot of the added nights"`
	AmountMinor       int64           `json:"amount_minor"`
	Currency          string          `json:"currency"`
	PaymentCollection string          `json:"payment_collection"`
	PaidMinor         int64           `json:"paid_minor" doc:"As reported by payment-service"`
	RespondBy         time.Time       `json:"respond_by"`
	DeclineReason     *string         `json:"decline_reason"`
	AppliedAt         *time.Time      `json:"applied_at"`
	Version           int64           `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
}

func extensionView(e db.ShortletBookingExtension) ExtensionView {
	return ExtensionView{ID: e.ID, BookingID: e.BookingID, RequestedBy: e.RequestedBy, Awaiting: e.Awaiting, Status: e.Status,
		PreviousCheckOut: e.PreviousCheckOut.Format(time.DateOnly), NewCheckOut: e.NewCheckOut.Format(time.DateOnly), PricingVersionID: e.PricingVersionID,
		Cost: e.CostSnapshot, AmountMinor: e.AmountMinor, Currency: e.Currency, PaymentCollection: e.PaymentCollection, PaidMinor: e.PaidMinor,
		RespondBy: e.RespondBy, DeclineReason: e.DeclineReason, AppliedAt: e.AppliedAt, Version: e.Version, CreatedAt: e.CreatedAt}
}

// RequestExtension (owner side) proposes a later check-out to the guest.
func (s *Service) RequestExtension(ctx context.Context, subj authz.Subject, bookingID ids.ID, in ExtensionInput) (ExtensionView, error) {
	if err := schema.Validate(in); err != nil {
		return ExtensionView{}, err
	}
	var out ExtensionView
	err := s.InTx(ctx, func(ctx context.Context) error {
		b, d, err := s.bookingFor(ctx, subj, bookingID, authz.Do(authz.CapShortletBookings))
		if err != nil {
			return err
		}
		e, err := s.openExtension(ctx, b, "OWNER", *schema.ParseDate(&in.NewCheckOut))
		if err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, b.PropertyID, "BOOKING_EXTENSION_PROPOSED", aggExtension, e.ID, "Stay extension proposed"); err != nil {
			return err
		}
		out = extensionView(e)
		return nil
	})
	return out, err
}

// CustomerRequestExtension (guest, through marketplace) asks the owner for
// a later check-out.
func (s *Service) CustomerRequestExtension(ctx context.Context, subj authz.Subject, bookingID ids.ID, in ExtensionInput) (ExtensionView, error) {
	if err := schema.Validate(in); err != nil {
		return ExtensionView{}, err
	}
	var out ExtensionView
	err := s.InTx(ctx, func(ctx context.Context) error {
		b, err := s.guestBooking(ctx, subj, bookingID, true)
		if err != nil {
			return err
		}
		e, err := s.openExtension(ctx, b, "GUEST", *schema.ParseDate(&in.NewCheckOut))
		if err != nil {
			return err
		}
		l, err := s.q.GetListing(ctx, mustTx(ctx), b.ListingID)
		if err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, mustTx(ctx), b.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateBookingRequested, map[string]string{"listing_title": l.Title,
			"check_in": e.PreviousCheckOut.Format(time.DateOnly)}); err != nil {
			return err
		}
		out = extensionView(e)
		return nil
	})
	return out, err
}

// openExtension validates and prices the extension and claims the days.
func (s *Service) openExtension(ctx context.Context, b db.ShortletBooking, by string, newOut time.Time) (db.ShortletBookingExtension, error) {
	tx, now := mustTx(ctx), s.now()
	if b.Status != "CHECKED_IN" {
		return db.ShortletBookingExtension{}, precondition("EXTENSION_NOT_ALLOWED", "Stays are extended while the guest is checked in; change dates before arrival by cancelling and rebooking.")
	}
	if !newOut.After(b.CheckOut) {
		return db.ShortletBookingExtension{}, apperrors.InvalidRequest("new_check_out must be after the current check-out.")
	}
	st, err := s.q.LockShortletSettings(ctx, tx, b.ListingID)
	if err != nil {
		return db.ShortletBookingExtension{}, err
	}
	if total := int32(newOut.Sub(b.CheckIn).Hours() / 24); total > st.MaxNights {
		return db.ShortletBookingExtension{}, precondition("STAY_LENGTH_NOT_ALLOWED", "The extended stay would exceed the listing's maximum nights.")
	}
	open, err := s.q.OpenBookingExtensions(ctx, tx, b.ID)
	if err != nil {
		return db.ShortletBookingExtension{}, err
	}
	if len(open) > 0 {
		return db.ShortletBookingExtension{}, conflict("EXTENSION_ALREADY_OPEN", "An extension of this stay is already open.")
	}
	l, err := s.q.GetListing(ctx, tx, b.ListingID)
	if err != nil {
		return db.ShortletBookingExtension{}, err
	}
	from := b.CheckOut
	cost, err := s.costPreview(ctx, l, CostPreviewInput{CheckIn: &from, CheckOut: &newOut, extension: true})
	if err != nil {
		return db.ShortletBookingExtension{}, err
	}
	if cost.Cost.Currency != b.Currency {
		return db.ShortletBookingExtension{}, conflict("CURRENCY_MISMATCH", "The listing is now priced in another currency; extend through a new booking.")
	}
	buf := int(st.TurnoverBufferDays)
	bufferEnd, prevBufferEnd := newOut.AddDate(0, 0, buf-1), b.CheckOut.AddDate(0, 0, buf-1)
	if buf == 0 {
		bufferEnd = newOut.AddDate(0, 0, -1)
	}
	lp, err := s.legalPolicy(ctx, b.PropertyID)
	if err != nil {
		return db.ShortletBookingExtension{}, err
	}
	awaiting := "OWNER"
	if by == "OWNER" {
		awaiting = "GUEST"
	}
	raw, _ := json.Marshal(cost.Cost)
	id := ids.New()
	n, err := s.q.ClaimExtensionDays(ctx, tx, db.ClaimExtensionDaysParams{ListingID: b.ListingID, NewCheckOut: newOut, ExtensionID: id, Now: now,
		FromDay: b.CheckOut, ToDay: bufferEnd, BookingID: b.ID})
	if err != nil {
		return db.ShortletBookingExtension{}, err
	}
	if n != int64(bufferEnd.Sub(b.CheckOut).Hours()/24)+1 {
		return db.ShortletBookingExtension{}, conflict("DATES_UNAVAILABLE", "The added nights (or the turnover days after them) are not available.")
	}
	e, err := s.q.InsertBookingExtension(ctx, tx, db.InsertBookingExtensionParams{ID: id, BookingID: b.ID, ListingID: b.ListingID, PropertyID: b.PropertyID,
		RequestedBy: by, Awaiting: &awaiting, PreviousCheckOut: b.CheckOut, NewCheckOut: newOut, BufferEnd: bufferEnd, PreviousBufferEnd: prevBufferEnd,
		PricingVersionID: cost.PricingVersionID, CostSnapshot: raw, AmountMinor: cost.Cost.UpfrontTotal, Currency: cost.Cost.Currency,
		PaymentCollection: b.PaymentCollection, RespondBy: now.Add(time.Duration(lp.Rules.ShortletApprovalHours) * time.Hour), CreatedAt: now})
	if isUnique(err, "shortlet_booking_extensions_open_uniq") {
		return e, conflict("EXTENSION_ALREADY_OPEN", "An extension of this stay is already open.")
	}
	if err != nil {
		return e, err
	}
	if err := s.emitAvailability(ctx, l, "BOOKING", []AvailabilityRange{{From: b.CheckOut.Format(time.DateOnly), To: bufferEnd.Format(time.DateOnly), Status: "HOLD"}}); err != nil {
		return e, err
	}
	return e, s.record(ctx, "shortlet.extension.request", AggBooking, b.ID, map[string]any{"extension_id": e.ID, "requested_by": by,
		"new_check_out": newOut.Format(time.DateOnly), "amount_minor": e.AmountMinor}, nil)
}

// ListExtensions lists a booking's extensions (owner side).
func (s *Service) ListExtensions(ctx context.Context, subj authz.Subject, bookingID ids.ID) ([]ExtensionView, error) {
	b, _, err := s.bookingFor(ctx, subj, bookingID, authz.Read(authz.CapShortletBookings))
	if err != nil {
		return nil, err
	}
	return s.extensionViews(ctx, b.ID)
}

// CustomerListExtensions lists the guest's booking extensions.
func (s *Service) CustomerListExtensions(ctx context.Context, subj authz.Subject, bookingID ids.ID) ([]ExtensionView, error) {
	b, err := s.guestBooking(ctx, subj, bookingID, false)
	if err != nil {
		return nil, err
	}
	return s.extensionViews(ctx, b.ID)
}

func (s *Service) extensionViews(ctx context.Context, bookingID ids.ID) ([]ExtensionView, error) {
	rows, err := s.q.ListBookingExtensions(ctx, s.dbq(ctx), bookingID)
	out := make([]ExtensionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, extensionView(r))
	}
	return out, err
}

// DecideExtension answers a guest's extension request (owner side).
func (s *Service) DecideExtension(ctx context.Context, subj authz.Subject, extensionID ids.ID, in ExtensionDecision) (ExtensionView, error) {
	if err := schema.Validate(in); err != nil {
		return ExtensionView{}, err
	}
	var out ExtensionView
	err := s.InTx(ctx, func(ctx context.Context) error {
		e, err := s.q.GetBookingExtension(ctx, mustTx(ctx), extensionID)
		if isNoRows(err) {
			return notFound(aggExtension)
		}
		if err != nil {
			return err
		}
		b, d, err := s.bookingFor(ctx, subj, e.BookingID, authz.Do(authz.CapShortletBookings))
		if apperrors.CodeOf(err) == "BOOKING_NOT_FOUND" {
			return notFound(aggExtension)
		}
		if err != nil {
			return err
		}
		if out, err = s.answerExtension(ctx, b, extensionID, "OWNER", in); err != nil {
			return err
		}
		return s.noteDelegateActivity(ctx, d, b.PropertyID, "BOOKING_EXTENSION_"+in.Decision, aggExtension, extensionID, "Stay extension "+lower(in.Decision))
	})
	return out, err
}

// CustomerRespondExtension answers the owner's extension proposal (guest).
func (s *Service) CustomerRespondExtension(ctx context.Context, subj authz.Subject, extensionID ids.ID, in ExtensionDecision) (ExtensionView, error) {
	if err := schema.Validate(in); err != nil {
		return ExtensionView{}, err
	}
	var out ExtensionView
	err := s.InTx(ctx, func(ctx context.Context) error {
		e, err := s.q.GetBookingExtension(ctx, mustTx(ctx), extensionID)
		if isNoRows(err) {
			return notFound(aggExtension)
		}
		if err != nil {
			return err
		}
		b, err := s.guestBooking(ctx, subj, e.BookingID, true)
		if apperrors.CodeOf(err) == "BOOKING_NOT_FOUND" {
			return notFound(aggExtension)
		}
		if err != nil {
			return err
		}
		out, err = s.answerExtension(ctx, b, extensionID, "GUEST", in)
		return err
	})
	return out, err
}

func (s *Service) answerExtension(ctx context.Context, b db.ShortletBooking, id ids.ID, side string, in ExtensionDecision) (ExtensionView, error) {
	tx, now := mustTx(ctx), s.now()
	e, err := s.q.GetBookingExtensionForUpdate(ctx, tx, id)
	if err != nil {
		return ExtensionView{}, err
	}
	if e.Status != "REQUESTED" || deref(e.Awaiting) != side {
		return ExtensionView{}, apperrors.InvalidStateTransition(e.Status, in.Decision)
	}
	if !e.RespondBy.After(now) {
		return ExtensionView{}, conflict("EXTENSION_EXPIRED", "The extension expired before it was answered.")
	}
	if in.Decision == "DECLINE" {
		if in.ReasonCode == nil {
			return ExtensionView{}, apperrors.InvalidRequest("reason_code is required to decline.")
		}
		e, err = s.closeExtension(ctx, e, "DECLINED", in.ReasonCode)
		if err != nil {
			return ExtensionView{}, err
		}
		return extensionView(e), s.record(ctx, "shortlet.extension.decline", AggBooking, b.ID, map[string]any{"extension_id": id, "by": side}, nil)
	}
	if b.Status != "CHECKED_IN" {
		return ExtensionView{}, precondition("EXTENSION_NOT_ALLOWED", "The guest is no longer checked in.")
	}
	if e.PaymentCollection == "PLATFORM" && e.AmountMinor > 0 {
		if s.Payment == nil {
			return ExtensionView{}, unavailable("Platform payment for stays", "payment-service")
		}
		lp, err := s.legalPolicy(ctx, b.PropertyID)
		if err != nil {
			return ExtensionView{}, err
		}
		if e, err = s.q.SetBookingExtensionStatus(ctx, tx, db.SetBookingExtensionStatusParams{ID: id, Status: "PAYMENT_PENDING", RespondBy: now.Add(lp.Rules.paymentHold()),
			UpdatedAt: now}); err != nil {
			return ExtensionView{}, err
		}
	} else if e, err = s.applyExtension(ctx, e); err != nil {
		return ExtensionView{}, err
	}
	return extensionView(e), s.record(ctx, "shortlet.extension.accept", AggBooking, b.ID, map[string]any{"extension_id": id, "by": side, "status": e.Status}, nil)
}

// applyExtension moves the check-out: the added nights become the booking's
// and the shortlet_bookings exclusion constraint re-checks the longer stay.
func (s *Service) applyExtension(ctx context.Context, e db.ShortletBookingExtension) (db.ShortletBookingExtension, error) {
	tx, now := mustTx(ctx), s.now()
	b, err := s.q.GetBookingForUpdate(ctx, tx, e.BookingID)
	if err != nil {
		return e, err
	}
	if b.Status != "CHECKED_IN" || !b.CheckOut.Equal(e.PreviousCheckOut) {
		return s.closeExtension(ctx, e, "CANCELLED", ptr("STAY_CHANGED"))
	}
	if err := s.q.ExtendBookingStay(ctx, tx, db.ExtendBookingStayParams{ID: b.ID, CheckOut: e.NewCheckOut, UpdatedAt: now}); err != nil {
		if isExclusion(err) {
			return e, conflict("DATES_UNAVAILABLE", "The added nights are no longer available.")
		}
		return e, err
	}
	if err := s.q.ApplyExtensionDays(ctx, tx, db.ApplyExtensionDaysParams{BookingID: b.ID, Now: now, ExtensionID: e.ID}); err != nil {
		return e, err
	}
	e, err = s.q.SetBookingExtensionStatus(ctx, tx, db.SetBookingExtensionStatusParams{ID: e.ID, Status: "APPLIED", AppliedAt: &now, RespondBy: e.RespondBy, UpdatedAt: now})
	if err != nil {
		return e, err
	}
	if err := s.emitBooking(ctx, b.ID, b.Status); err != nil {
		return e, err
	}
	l, err := s.q.GetListing(ctx, tx, b.ListingID)
	if err != nil {
		return e, err
	}
	if err := s.emitAvailability(ctx, l, "BOOKING", []AvailabilityRange{{From: e.PreviousCheckOut.Format(time.DateOnly),
		To: e.NewCheckOut.AddDate(0, 0, -1).Format(time.DateOnly), Status: "BOOKED"}}); err != nil {
		return e, err
	}
	return e, s.record(ctx, "shortlet.extension.apply", AggBooking, b.ID, map[string]any{"extension_id": e.ID, "check_out": e.NewCheckOut.Format(time.DateOnly)}, nil)
}

// closeExtension ends an unapplied extension and gives its days back.
func (s *Service) closeExtension(ctx context.Context, e db.ShortletBookingExtension, to string, reason *string) (db.ShortletBookingExtension, error) {
	tx, now := mustTx(ctx), s.now()
	if err := s.q.RestoreExtensionDays(ctx, tx, db.RestoreExtensionDaysParams{PreviousBufferEnd: e.PreviousBufferEnd, BookingID: e.BookingID, Now: now,
		ExtensionID: e.ID}); err != nil {
		return e, err
	}
	n, err := s.q.SetBookingExtensionStatus(ctx, tx, db.SetBookingExtensionStatusParams{ID: e.ID, Status: to, DeclineReason: reason, RespondBy: e.RespondBy, UpdatedAt: now})
	if err != nil {
		return e, err
	}
	l, err := s.q.GetListing(ctx, tx, e.ListingID)
	if err != nil {
		return n, err
	}
	return n, s.emitAvailability(ctx, l, "BOOKING", []AvailabilityRange{{From: e.NewCheckOut.Format(time.DateOnly), To: e.BufferEnd.Format(time.DateOnly), Status: "AVAILABLE"}})
}

// cancelOpenExtensions closes open extensions when the stay ends.
func (s *Service) cancelOpenExtensions(ctx context.Context, bookingID ids.ID, reason string) error {
	open, err := s.q.OpenBookingExtensions(ctx, mustTx(ctx), bookingID)
	if err != nil {
		return err
	}
	for _, e := range open {
		if _, err := s.closeExtension(ctx, e, "CANCELLED", &reason); err != nil {
			return err
		}
	}
	return nil
}

// runExtensionJobs expires unanswered/unpaid extensions and requests and
// reconciles extension payments.
func (s *Service) runExtensionJobs(ctx context.Context, batch int32) error {
	now := s.now()
	expired, err := s.q.ExpiredBookingExtensions(ctx, s.Pool, db.ExpiredBookingExtensionsParams{RespondBy: now, Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range expired {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			e, err := s.q.GetBookingExtensionForUpdate(ctx, mustTx(ctx), id)
			if err != nil || (e.Status != "REQUESTED" && e.Status != "PAYMENT_PENDING") || e.RespondBy.After(now) {
				return err
			}
			if _, err := s.closeExtension(ctx, e, "EXPIRED", nil); err != nil {
				return err
			}
			return s.record(ctx, "shortlet.extension.expire", AggBooking, e.BookingID, map[string]any{"extension_id": id, "status": e.Status}, nil)
		}); err != nil && !benign(err) {
			return err
		}
	}
	if s.Payment == nil {
		return nil
	}
	need, err := s.q.ExtensionsNeedingIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range need {
		if err := s.requestExtensionIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "extension payment intent deferred", "extension_id", id, "error", err)
		}
	}
	recon, err := s.q.ExtensionsToReconcile(ctx, s.Pool, db.ExtensionsToReconcileParams{CheckedBefore: now.Add(-s.Policy.Domain.PaymentReconcileAfter), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range recon {
		e, err := s.q.GetBookingExtension(ctx, s.Pool, id)
		if err != nil {
			return err
		}
		in, err := s.Payment.GetIntent(ctx, *e.PaymentIntentRef)
		if err != nil {
			s.Logger.WarnContext(ctx, "extension payment read-back deferred", "extension_id", id, "error", err)
			continue
		}
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.projectExtensionIntent(ctx, id, in) }); err != nil && !benign(err) {
			return err
		}
	}
	return nil
}

func (s *Service) requestExtensionIntent(ctx context.Context, id ids.ID) error {
	e, err := s.q.GetBookingExtension(ctx, s.Pool, id)
	if err != nil || e.PaymentIntentRef != nil {
		return err
	}
	b, err := s.q.GetBooking(ctx, s.Pool, e.BookingID)
	if err != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, s.Pool, e.PropertyID)
	if err != nil {
		return err
	}
	// The idempotency key is the extension id: a retry after an unknown
	// outcome returns the same intent instead of charging twice.
	in, err := s.Payment.CreateIntent(ctx, payment.IntentRequest{Purpose: payment.PurposeShortletStay, ReferenceType: aggExtension, ReferenceID: e.ID,
		PayeeOwnerAccountID: p.OwnerAccountID, PayerDomain: string(platform.DomainMarketplace), PayerRefID: &b.CustomerRefID, AmountMinor: e.AmountMinor,
		Currency: e.Currency, DueDate: e.RespondBy, Escrow: true}, aggExtension+":"+e.ID.String())
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		if err := s.q.SetExtensionIntent(ctx, mustTx(ctx), db.SetExtensionIntentParams{ID: id, PaymentIntentRef: &in.Ref, LastPaymentStatus: &in.Status,
			LastCheckedAt: ptr(s.now())}); err != nil {
			return err
		}
		return s.projectExtensionIntent(ctx, id, in)
	})
}

// projectExtensionIntent applies payment truth: the extension is applied
// only when payment-service reports the added amount paid (or held).
func (s *Service) projectExtensionIntent(ctx context.Context, id ids.ID, in payment.Intent) error {
	if fresh, err := s.intentIsFresh(ctx, in); !fresh || err != nil {
		return err
	}
	tx, now := mustTx(ctx), s.now()
	e, err := s.q.GetBookingExtensionForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := s.q.SetExtensionPayment(ctx, tx, db.SetExtensionPaymentParams{ID: id, PaidMinor: in.PaidMinor, LastPaymentStatus: &in.Status, LastCheckedAt: &now}); err != nil {
		return err
	}
	paid := (in.Status == payment.IntentSucceeded || in.Status == payment.IntentHeld) && in.PaidMinor >= e.AmountMinor
	if !paid || e.Status != "PAYMENT_PENDING" {
		if paid && e.Status != "APPLIED" {
			// Money for an extension that has ended meanwhile is surfaced for
			// refund through payment-service; never applied silently.
			return s.record(ctx, "shortlet.extension.payment_after_close", AggBooking, e.BookingID, map[string]any{"extension_id": id,
				"amount_minor": in.PaidMinor}, map[string]any{"review": "MANUAL"})
		}
		return nil
	}
	_, err = s.applyExtension(ctx, e)
	return err
}
