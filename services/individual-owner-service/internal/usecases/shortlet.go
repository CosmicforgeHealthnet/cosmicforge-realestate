package usecases

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/calendarsync"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Personally operated short-let (§13.20, §14.4). Date inventory is the
// listing's availability_calendar: a booking claims its nights (HOLD, then
// BOOKED) and its turnover buffer with conditional upserts in the booking
// transaction, and an exclusion constraint on the stay range guarantees no
// overlap under concurrency. The price snapshot is computed by the shared
// calculator (including date rates) and never changes afterwards. Payment,
// refunds and damage settlements are payment-service truth; external
// calendars are best-effort input that never overrides a booking.

const (
	AggBooking = "shortlet_booking"
	// Short-let policy (engineering defaults; configuration in DomainPolicy
	// would follow the same pattern).
	shortletApprovalTTL    = 24 * time.Hour
	shortletPaymentHold    = 30 * time.Minute
	damageResponseWindow   = 72 * time.Hour
	externalCalendarWindow = 365
)

// Owner notification templates of the short-let context.
const (
	TemplateBookingRequested = "owner.booking_requested"  // listing_title, check_in
	TemplateCalendarConflict = "owner.calendar_conflict"  // listing_title, dates
	TemplateDamageResponse   = "owner.damage_claim_reply" // listing_title, decision
)

// AddOn is an optional extra offered with a stay.
type AddOn struct {
	Code        string `json:"code" v:"required,pattern=[a-z][a-z0-9_]{1,39}"`
	Label       string `json:"label" v:"required,max=80"`
	AmountMinor int64  `json:"amount_minor" v:"required,min=1"`
	Per         string `json:"per" v:"required,enum=STAY|NIGHT|GUEST"`
}

// ShortletSettingsInput configures a SHORT_LET listing.
type ShortletSettingsInput struct {
	MaxGuests                 int32    `json:"max_guests" v:"required,min=1,max=100"`
	MinNights                 int32    `json:"min_nights" v:"required,min=1,max=365"`
	MaxNights                 int32    `json:"max_nights" v:"required,min=1,max=365"`
	CheckInTime               string   `json:"check_in_time" v:"required,pattern=([01][0-9]|2[0-3]):[0-5][0-9]"`
	CheckOutTime              string   `json:"check_out_time" v:"required,pattern=([01][0-9]|2[0-3]):[0-5][0-9]"`
	InstantBook               bool     `json:"instant_book"`
	GuestVerificationRequired bool     `json:"guest_verification_required"`
	PaymentCollection         string   `json:"payment_collection" v:"required,enum=PLATFORM|OFF_PLATFORM" doc:"PLATFORM needs payment-service"`
	CancellationPolicy        string   `json:"cancellation_policy" v:"required,enum=FLEXIBLE|MODERATE|STRICT|NON_REFUNDABLE"`
	HouseRules                []string `json:"house_rules,omitempty" v:"max=30"`
	AddOns                    []AddOn  `json:"add_ons,omitempty" v:"max=20"`
	TurnoverBufferDays        int32    `json:"turnover_buffer_days,omitempty" v:"min=0,max=3"`
	DamageClaimWindowHours    int32    `json:"damage_claim_window_hours,omitempty" v:"min=0,max=720" doc:"Default 48"`
	ExternalCalendarRef       *string  `json:"external_calendar_ref,omitempty" v:"max=500" doc:"External calendar feed (needs a calendar-sync adapter)"`
}

// ShortletSettingsView is the configuration of a short-let listing.
type ShortletSettingsView struct {
	ListingID ids.ID                `json:"listing_id"`
	Settings  ShortletSettingsInput `json:"settings"`
	Version   int64                 `json:"version"`
	UpdatedAt time.Time             `json:"updated_at"`
}

func settingsView(s db.ShortletSetting) ShortletSettingsView {
	v := ShortletSettingsView{ListingID: s.ListingID, Version: s.Version, UpdatedAt: s.UpdatedAt, Settings: ShortletSettingsInput{MaxGuests: s.MaxGuests,
		MinNights: s.MinNights, MaxNights: s.MaxNights, CheckInTime: s.CheckInTime, CheckOutTime: s.CheckOutTime, InstantBook: s.InstantBook,
		GuestVerificationRequired: s.GuestVerificationRequired, PaymentCollection: s.PaymentCollection, CancellationPolicy: s.CancellationPolicy,
		HouseRules: nonNil(s.HouseRules), TurnoverBufferDays: s.TurnoverBufferDays, DamageClaimWindowHours: s.DamageClaimWindowHours,
		ExternalCalendarRef: s.ExternalCalendarRef, AddOns: []AddOn{}}}
	_ = json.Unmarshal(s.AddOns, &v.Settings.AddOns)
	return v
}

// shortletListing authorizes an action on a SHORT_LET listing.
func (s *Service) shortletListing(ctx context.Context, subj authz.Subject, listingID ids.ID, action authz.Action) (db.Listing, authz.Decision, error) {
	l, err := s.q.GetListing(ctx, s.dbq(ctx), listingID)
	if isNoRows(err) {
		return l, authz.Decision{}, notFound(AggListing)
	}
	if err != nil {
		return l, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, l.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		return l, d, notFound(AggListing)
	}
	if err == nil && l.TransactionType != "SHORT_LET" {
		err = precondition("NOT_A_SHORT_LET", "This applies to SHORT_LET listings.")
	}
	return l, d, err
}

// GetShortletSettings returns the listing's short-let settings.
func (s *Service) GetShortletSettings(ctx context.Context, subj authz.Subject, listingID ids.ID) (ShortletSettingsView, error) {
	if _, _, err := s.shortletListing(ctx, subj, listingID, authz.Read(authz.CapShortletManage)); err != nil {
		return ShortletSettingsView{}, err
	}
	st, err := s.q.GetShortletSettings(ctx, s.dbq(ctx), listingID)
	if isNoRows(err) {
		return ShortletSettingsView{}, apperrors.NotFound("SHORTLET_NOT_CONFIGURED", "The short-let settings are not configured yet.")
	}
	return settingsView(st), err
}

// PutShortletSettings creates or updates the settings (If-Match; "0" to create).
func (s *Service) PutShortletSettings(ctx context.Context, subj authz.Subject, listingID ids.ID, expected int64, in ShortletSettingsInput) (ShortletSettingsView, error) {
	if err := schema.Validate(in); err != nil {
		return ShortletSettingsView{}, err
	}
	if in.MaxNights < in.MinNights {
		return ShortletSettingsView{}, apperrors.InvalidRequest("max_nights must be at least min_nights.")
	}
	if in.PaymentCollection == "PLATFORM" && s.Payment == nil {
		return ShortletSettingsView{}, unavailable("Platform payment for stays", "payment-service")
	}
	if in.ExternalCalendarRef != nil && s.CalendarSync == nil {
		return ShortletSettingsView{}, unavailable("External calendar sync", "a calendar-sync provider")
	}
	if in.DamageClaimWindowHours == 0 {
		in.DamageClaimWindowHours = 48
	}
	var out ShortletSettingsView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, _, err := s.shortletListing(ctx, subj, listingID, authz.Do(authz.CapShortletManage))
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		if _, err := s.q.GetShortletSettings(ctx, tx, listingID); isNoRows(err) && expected != 0 {
			return lifecycle.ErrVersionConflict()
		}
		addOns := in.AddOns
		if addOns == nil {
			addOns = []AddOn{}
		}
		st, err := s.q.UpsertShortletSettings(ctx, tx, db.UpsertShortletSettingsParams{ListingID: listingID, PropertyID: l.PropertyID, MaxGuests: in.MaxGuests,
			MinNights: in.MinNights, MaxNights: in.MaxNights, CheckInTime: in.CheckInTime, CheckOutTime: in.CheckOutTime, InstantBook: in.InstantBook,
			GuestVerificationRequired: in.GuestVerificationRequired, PaymentCollection: in.PaymentCollection, CancellationPolicy: in.CancellationPolicy,
			HouseRules: nonNil(in.HouseRules), AddOns: jsonb(addOns), TurnoverBufferDays: in.TurnoverBufferDays, DamageClaimWindowHours: in.DamageClaimWindowHours,
			ExternalCalendarRef: in.ExternalCalendarRef, CreatedAt: s.now(), ExpectedVersion: expected})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		out = settingsView(st)
		return s.record(ctx, "shortlet.settings", AggListing, listingID, map[string]any{"version": st.Version, "instant_book": in.InstantBook}, nil)
	})
	return out, err
}

// ---- rates ----

// RateInput is a date-range nightly rate.
type RateInput struct {
	StartDate        string  `json:"start_date" v:"required,format=date"`
	EndDate          string  `json:"end_date" v:"required,format=date" doc:"Inclusive"`
	NightlyRateMinor int64   `json:"nightly_rate_minor" v:"required,min=1"`
	MinNights        int32   `json:"min_nights,omitempty" v:"min=1,max=365"`
	Label            *string `json:"label,omitempty" v:"max=80"`
}

// RatesInput replaces the listing's date rates.
type RatesInput struct {
	Rates []RateInput `json:"rates" v:"max=200"`
}

// RateView is an active date rate.
type RateView struct {
	StartDate        string  `json:"start_date"`
	EndDate          string  `json:"end_date"`
	NightlyRateMinor int64   `json:"nightly_rate_minor"`
	MinNights        *int32  `json:"min_nights"`
	Label            *string `json:"label"`
}

// GetRates lists the active date rates.
func (s *Service) GetRates(ctx context.Context, subj authz.Subject, listingID ids.ID) ([]RateView, error) {
	if _, _, err := s.shortletListing(ctx, subj, listingID, authz.Read(authz.CapPricingManage)); err != nil {
		return nil, err
	}
	rows, err := s.q.ActiveRates(ctx, s.dbq(ctx), listingID)
	out := make([]RateView, 0, len(rows))
	for _, r := range rows {
		out = append(out, RateView{StartDate: r.StartDate.Format(time.DateOnly), EndDate: r.EndDate.Format(time.DateOnly), NightlyRateMinor: r.NightlyRateMinor,
			MinNights: r.MinNights, Label: r.Label})
	}
	return out, err
}

// PutRates replaces the date rates. Confirmed bookings keep their snapshot.
func (s *Service) PutRates(ctx context.Context, subj authz.Subject, listingID ids.ID, in RatesInput) ([]RateView, error) {
	if err := schema.Validate(in); err != nil {
		return nil, err
	}
	type span struct{ a, b time.Time }
	var spans []span
	for _, r := range in.Rates {
		a, b := *schema.ParseDate(&r.StartDate), *schema.ParseDate(&r.EndDate)
		if b.Before(a) {
			return nil, apperrors.InvalidRequest("end_date must not be before start_date.")
		}
		for _, o := range spans {
			if !a.After(o.b) && !b.Before(o.a) {
				return nil, apperrors.InvalidRequest("Rate ranges must not overlap.")
			}
		}
		spans = append(spans, span{a, b})
	}
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, _, err := s.shortletListing(ctx, subj, listingID, authz.Do(authz.CapPricingManage)); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if err := s.q.SupersedeRates(ctx, tx, db.SupersedeRatesParams{ListingID: listingID, SupersededAt: &now}); err != nil {
			return err
		}
		for _, r := range in.Rates {
			var mn *int32
			if r.MinNights > 0 {
				mn = &r.MinNights
			}
			if _, err := s.q.InsertRate(ctx, tx, db.InsertRateParams{ID: ids.New(), ListingID: listingID, StartDate: *schema.ParseDate(&r.StartDate),
				EndDate: *schema.ParseDate(&r.EndDate), NightlyRateMinor: r.NightlyRateMinor, MinNights: mn, Label: r.Label, CreatedAt: now}); err != nil {
				return err
			}
		}
		return s.record(ctx, "shortlet.rates", AggListing, listingID, map[string]any{"rates": len(in.Rates)}, nil)
	})
	if err != nil {
		return nil, err
	}
	return s.GetRates(ctx, subj, listingID)
}

// applyShortletRates adds date overrides (and their minimum stays) to a model.
func (s *Service) applyShortletRates(ctx context.Context, listingID ids.ID, m *domain.PriceModel, checkIn, checkOut time.Time) error {
	rates, err := s.q.ActiveRates(ctx, s.dbq(ctx), listingID)
	if err != nil || len(rates) == 0 {
		return err
	}
	nights := domain.Nights(checkIn, checkOut)
	m.NightlyOverrides = map[string]int64{}
	for _, n := range nights {
		for _, r := range rates {
			if !n.Before(r.StartDate) && !n.After(r.EndDate) {
				m.NightlyOverrides[n.Format(time.DateOnly)] = r.NightlyRateMinor
				if r.MinNights != nil && int(*r.MinNights) > len(nights) {
					return apperrors.Unprocessable("STAY_BELOW_MINIMUM", fmt.Sprintf("Stays including %s need at least %d nights.", n.Format(time.DateOnly), *r.MinNights))
				}
			}
		}
	}
	return nil
}

// ---- calendar ----

// CalendarDay is one day of date inventory.
type CalendarDay struct {
	Day    string  `json:"day"`
	Status string  `json:"status" doc:"AVAILABLE, BLOCKED, HOLD or BOOKED"`
	Source string  `json:"source"`
	RefID  *ids.ID `json:"ref_id,omitempty" doc:"Booking holding the day (owner view only)"`
}

// GetCalendar returns the inventory between two dates (max 366 days).
func (s *Service) GetCalendar(ctx context.Context, subj authz.Subject, listingID ids.ID, from, to time.Time) ([]CalendarDay, error) {
	if _, _, err := s.shortletListing(ctx, subj, listingID, authz.Read(authz.CapShortletManage)); err != nil {
		return nil, err
	}
	return s.calendar(ctx, listingID, from, to, true)
}

func (s *Service) calendar(ctx context.Context, listingID ids.ID, from, to time.Time, withRefs bool) ([]CalendarDay, error) {
	if to.Before(from) || to.Sub(from) > 366*24*time.Hour {
		return nil, apperrors.InvalidRequest("Query at most 366 days, from ≤ to.")
	}
	rows, err := s.q.ListCalendarRange(ctx, s.dbq(ctx), db.ListCalendarRangeParams{ListingID: listingID, FromDay: from, ToDay: to})
	if err != nil {
		return nil, err
	}
	byDay := map[string]db.AvailabilityCalendar{}
	for _, r := range rows {
		byDay[r.Day.Format(time.DateOnly)] = r
	}
	var out []CalendarDay
	for d := dayOf(from); !d.After(dayOf(to)); d = d.AddDate(0, 0, 1) {
		k := d.Format(time.DateOnly)
		c := CalendarDay{Day: k, Status: "AVAILABLE", Source: "SYSTEM"}
		if r, ok := byDay[k]; ok {
			c.Status, c.Source = r.Status, r.Source
			if withRefs {
				c.RefID = r.RefID
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// CalendarRange changes owner availability for [from, to].
type CalendarRange struct {
	From   string `json:"from" v:"required,format=date"`
	To     string `json:"to" v:"required,format=date" doc:"Inclusive"`
	Status string `json:"status" v:"required,enum=AVAILABLE|BLOCKED"`
}

// CalendarInput updates owner blocks.
type CalendarInput struct {
	Ranges []CalendarRange `json:"ranges" v:"required,max=50"`
}

// AvailabilityRange is one changed range of an availability event.
type AvailabilityRange struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status"`
}

// ShortletAvailabilityEvent is shortlet.availability.updated.v1.
type ShortletAvailabilityEvent struct {
	SchemaVersion int                 `json:"schema_version"`
	ListingID     ids.ID              `json:"listing_id"`
	PropertyID    ids.ID              `json:"property_id"`
	Source        string              `json:"source"`
	Ranges        []AvailabilityRange `json:"ranges"`
	OccurredAt    time.Time           `json:"occurred_at"`
}

func (s *Service) emitAvailability(ctx context.Context, l db.Listing, source string, ranges []AvailabilityRange) error {
	return s.publish(ctx, "shortlet.availability.updated.v1", AggListing, l.ID, ShortletAvailabilityEvent{SchemaVersion: 1, ListingID: l.ID,
		PropertyID: l.PropertyID, Source: source, Ranges: ranges, OccurredAt: s.now()})
}

// PutCalendar blocks or frees days. Held and booked days cannot be changed
// (the whole update is refused).
func (s *Service) PutCalendar(ctx context.Context, subj authz.Subject, listingID ids.ID, in CalendarInput) ([]CalendarDay, error) {
	if err := schema.Validate(in); err != nil {
		return nil, err
	}
	var from, to time.Time
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, _, err := s.shortletListing(ctx, subj, listingID, authz.Do(authz.CapAvailability))
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		var ranges []AvailabilityRange
		for i, r := range in.Ranges {
			a, b := *schema.ParseDate(&r.From), *schema.ParseDate(&r.To)
			if b.Before(a) || b.Sub(a) > 366*24*time.Hour {
				return apperrors.InvalidRequest("Each range is from ≤ to and at most 366 days.")
			}
			if i == 0 || a.Before(from) {
				from = a
			}
			if b.After(to) {
				to = b
			}
			n, err := s.q.SetCalendarRange(ctx, tx, db.SetCalendarRangeParams{ListingID: listingID, Status: r.Status, Source: "OWNER", Now: now, FromDay: a, ToDay: b})
			if err != nil {
				return err
			}
			if want := int64(b.Sub(a)/(24*time.Hour)) + 1; n != want {
				return conflict("CALENDAR_CONFLICT", "Some of the days are held or booked by guests and cannot be changed.")
			}
			ranges = append(ranges, AvailabilityRange(r))
		}
		if err := s.emitAvailability(ctx, l, "OWNER", ranges); err != nil {
			return err
		}
		return s.record(ctx, "shortlet.calendar", AggListing, listingID, map[string]any{"ranges": len(ranges)}, nil)
	})
	if err != nil {
		return nil, err
	}
	return s.calendar(ctx, listingID, from, to, true)
}

// ---- bookings ----

// GuestInput is one guest of a booking.
type GuestInput struct {
	DisplayName        string  `json:"display_name" v:"required,max=120"`
	CustomerRefID      *ids.ID `json:"customer_ref_id,omitempty"`
	Primary            bool    `json:"primary"`
	VerificationRef    *string `json:"verification_ref,omitempty" v:"max=128" doc:"Customer-domain verification reference (never documents)"`
	VerificationStatus string  `json:"verification_status,omitempty" v:"enum=NOT_PROVIDED|PENDING|VERIFIED|REJECTED|EXPIRED" doc:"As attested by marketplace-service"`
}

// CustomerBookingInput is a marketplace booking request.
type CustomerBookingInput struct {
	ListingID       ids.ID       `json:"listing_id" v:"required"`
	CheckIn         string       `json:"check_in" v:"required,format=date"`
	CheckOut        string       `json:"check_out" v:"required,format=date"`
	Guests          int32        `json:"guests" v:"required,min=1,max=100"`
	GuestList       []GuestInput `json:"guest_list" v:"required,max=100"`
	ConversationRef *string      `json:"conversation_ref,omitempty" v:"max=128" doc:"CF Meet conversation (messages stay in CF Meet)"`
	ExternalRef     *string      `json:"external_ref,omitempty" v:"max=128" doc:"Marketplace idempotency reference"`
}

// GuestView is a guest of a booking.
type GuestView struct {
	ID                 ids.ID  `json:"id"`
	DisplayName        string  `json:"display_name"`
	CustomerRefID      *ids.ID `json:"customer_ref_id"`
	Primary            bool    `json:"primary"`
	VerificationRef    *string `json:"verification_ref"`
	VerificationStatus string  `json:"verification_status"`
}

// BookingView is a short-let booking.
type BookingView struct {
	ID                ids.ID          `json:"id"`
	ListingID         ids.ID          `json:"listing_id"`
	PropertyID        ids.ID          `json:"property_id"`
	CustomerRefID     ids.ID          `json:"customer_ref_id"`
	Status            string          `json:"status"`
	CheckIn           string          `json:"check_in"`
	CheckOut          string          `json:"check_out"`
	Guests            int32           `json:"guests"`
	PricingVersionID  ids.ID          `json:"pricing_version_id"`
	Cost              json.RawMessage `json:"cost" doc:"Immutable price snapshot"`
	TotalMinor        int64           `json:"total_minor"`
	Currency          string          `json:"currency"`
	PaymentCollection string          `json:"payment_collection"`
	PaidMinor         int64           `json:"paid_minor" doc:"As reported by payment-service"`
	HoldExpiresAt     *time.Time      `json:"hold_expires_at"`
	CancelledBy       *string         `json:"cancelled_by"`
	CancelReason      *string         `json:"cancel_reason"`
	RefundDueMinor    int64           `json:"refund_due_minor"`
	RefundStatus      *string         `json:"refund_status"`
	RefundedMinor     int64           `json:"refunded_minor"`
	AccessGrantRef    *ids.ID         `json:"access_grant_ref"`
	ConversationRef   *string         `json:"conversation_ref"`
	CheckedInAt       *time.Time      `json:"checked_in_at"`
	CheckedOutAt      *time.Time      `json:"checked_out_at"`
	GuestList         []GuestView     `json:"guest_list"`
	Version           int64           `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
}

func bookingBase(b db.ShortletBooking) BookingView {
	return BookingView{ID: b.ID, ListingID: b.ListingID, PropertyID: b.PropertyID, CustomerRefID: b.CustomerRefID, Status: b.Status,
		CheckIn: b.CheckIn.Format(time.DateOnly), CheckOut: b.CheckOut.Format(time.DateOnly), Guests: b.Guests, PricingVersionID: b.PricingVersionID,
		Cost: b.CostSnapshot, TotalMinor: b.TotalMinor, Currency: b.Currency, PaymentCollection: b.PaymentCollection, PaidMinor: b.PaidMinor,
		HoldExpiresAt: b.HoldExpiresAt, CancelledBy: b.CancelledBy, CancelReason: b.CancelReason, RefundDueMinor: b.RefundDueMinor,
		RefundStatus: b.RefundStatus, RefundedMinor: b.RefundedMinor, AccessGrantRef: b.AccessGrantRef, ConversationRef: b.ConversationRef,
		CheckedInAt: b.CheckedInAt, CheckedOutAt: b.CheckedOutAt, Version: b.Version, CreatedAt: b.CreatedAt, GuestList: []GuestView{}}
}

func (s *Service) bookingView(ctx context.Context, b db.ShortletBooking) (BookingView, error) {
	v := bookingBase(b)
	gs, err := s.q.ListGuests(ctx, s.dbq(ctx), b.ID)
	for _, g := range gs {
		v.GuestList = append(v.GuestList, GuestView{ID: g.ID, DisplayName: g.DisplayName, CustomerRefID: g.CustomerRefID, Primary: g.IsPrimary,
			VerificationRef: g.VerificationRef, VerificationStatus: g.VerificationStatus})
	}
	return v, err
}

// ShortletBookingEvent is shortlet.booking.updated.v1.
type ShortletBookingEvent struct {
	SchemaVersion  int        `json:"schema_version"`
	BookingID      ids.ID     `json:"booking_id"`
	ListingID      ids.ID     `json:"listing_id"`
	PropertyID     ids.ID     `json:"property_id"`
	CustomerRefID  ids.ID     `json:"customer_ref_id"`
	Status         string     `json:"status"`
	PreviousStatus string     `json:"previous_status"`
	CheckIn        string     `json:"check_in"`
	CheckOut       string     `json:"check_out"`
	Guests         int32      `json:"guests"`
	TotalMinor     int64      `json:"total_minor"`
	Currency       string     `json:"currency"`
	HoldExpiresAt  *time.Time `json:"hold_expires_at"`
	BookingVersion int64      `json:"booking_version"`
	OccurredAt     time.Time  `json:"occurred_at"`
}

func (s *Service) emitBooking(ctx context.Context, id ids.ID, previous string) error {
	b, err := s.q.GetBooking(ctx, mustTx(ctx), id)
	if err != nil {
		return err
	}
	return s.publish(ctx, "shortlet.booking.updated.v1", AggBooking, id, ShortletBookingEvent{SchemaVersion: 1, BookingID: b.ID, ListingID: b.ListingID,
		PropertyID: b.PropertyID, CustomerRefID: b.CustomerRefID, Status: b.Status, PreviousStatus: previous, CheckIn: b.CheckIn.Format(time.DateOnly),
		CheckOut: b.CheckOut.Format(time.DateOnly), Guests: b.Guests, TotalMinor: b.TotalMinor, Currency: b.Currency, HoldExpiresAt: b.HoldExpiresAt,
		BookingVersion: b.Version, OccurredAt: s.now()})
}

// bookingTo transitions a booking and publishes its state.
func (s *Service) bookingTo(ctx context.Context, id ids.ID, to, cmd, reason string) error {
	prev, _, err := s.transition(ctx, domain.ShortletBooking, id, lifecycle.Change{To: to, Command: cmd, Reason: reason})
	if err != nil {
		return err
	}
	return s.emitBooking(ctx, id, prev.Status)
}

// isExclusion reports an exclusion-constraint violation (the only one on
// shortlet_bookings is the no-overlap constraint).
func isExclusion(err error) bool { return database.SQLState(err) == "23P01" }

// CustomerRequestBooking places a hold on the dates. Instant-book listings
// go straight to payment (or confirmation when paid off-platform);
// otherwise the owner approves within 24h.
func (s *Service) CustomerRequestBooking(ctx context.Context, subj authz.Subject, in CustomerBookingInput) (BookingView, error) {
	if err := schema.Validate(in); err != nil {
		return BookingView{}, err
	}
	checkIn, checkOut := *schema.ParseDate(&in.CheckIn), *schema.ParseDate(&in.CheckOut)
	primaries := 0
	for _, g := range in.GuestList {
		if g.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		return BookingView{}, apperrors.InvalidRequest("Exactly one primary guest is required.")
	}
	var out BookingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		customer, err := s.requireCustomer(ctx, subj)
		if err != nil {
			return err
		}
		l, err := s.bookableListing(ctx, in.ListingID, "SHORT_LET")
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if in.ExternalRef != nil {
			if b, err := s.q.GetBookingByExternalRef(ctx, tx, db.GetBookingByExternalRefParams{ListingID: l.ID, ExternalRef: in.ExternalRef}); err == nil {
				if b.CustomerRefID != customer {
					return conflict("EXTERNAL_REF_IN_USE", "The external reference belongs to another booking.")
				}
				out, err = s.bookingView(ctx, b)
				return err
			} else if !isNoRows(err) {
				return err
			}
		}
		st, err := s.q.GetShortletSettings(ctx, tx, l.ID)
		if isNoRows(err) {
			return conflict("SHORTLET_NOT_CONFIGURED", "The listing is not open for bookings.")
		}
		if err != nil {
			return err
		}
		nights := int32(len(domain.Nights(checkIn, checkOut)))
		switch {
		case checkIn.Before(dayOf(now)):
			return apperrors.InvalidRequest("check_in cannot be in the past.")
		case nights < 1:
			return apperrors.InvalidRequest("check_out must be after check_in.")
		case nights < st.MinNights || nights > st.MaxNights:
			return precondition("STAY_LENGTH_NOT_ALLOWED", fmt.Sprintf("Stays are %d–%d nights.", st.MinNights, st.MaxNights))
		case in.Guests > st.MaxGuests:
			return precondition("TOO_MANY_GUESTS", fmt.Sprintf("At most %d guests.", st.MaxGuests))
		case int(in.Guests) < len(in.GuestList):
			return apperrors.InvalidRequest("guest_list cannot exceed guests.")
		}
		cost, err := s.costPreview(ctx, l, CostPreviewInput{CheckIn: &checkIn, CheckOut: &checkOut})
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(cost.Cost)
		b, err := s.q.InsertBooking(ctx, tx, db.InsertBookingParams{ID: ids.New(), ListingID: l.ID, PropertyID: l.PropertyID, CustomerRefID: customer,
			CheckIn: checkIn, CheckOut: checkOut, Guests: in.Guests, PricingVersionID: cost.PricingVersionID, CostSnapshot: raw,
			TotalMinor: cost.Cost.UpfrontTotal, Currency: cost.Cost.Currency, PaymentCollection: st.PaymentCollection, CancellationPolicy: st.CancellationPolicy,
			HoldExpiresAt: ptr(now.Add(shortletPaymentHold)), ConversationRef: in.ConversationRef, ExternalRef: in.ExternalRef, CreatedAt: now})
		if isExclusion(err) {
			return conflict("DATES_UNAVAILABLE", "Those dates are no longer available.")
		}
		if err != nil {
			return err
		}
		if err := s.claimStay(ctx, b, st, "HOLD"); err != nil {
			return err
		}
		if err := domain.ShortletBooking.RecordCreation(ctx, tx, b.ID, &b.PropertyID, "HOLD", now); err != nil {
			return err
		}
		verified := false
		for _, g := range in.GuestList {
			status := g.VerificationStatus
			if status == "" {
				status = "NOT_PROVIDED"
			}
			if g.Primary && status == "VERIFIED" {
				verified = true
			}
			if err := s.q.InsertGuest(ctx, tx, db.InsertGuestParams{ID: ids.New(), BookingID: b.ID, IsPrimary: g.Primary, CustomerRefID: g.CustomerRefID,
				DisplayName: g.DisplayName, VerificationRef: g.VerificationRef, VerificationStatus: status, CreatedAt: now}); err != nil {
				return err
			}
		}
		next, hold := "PENDING_APPROVAL", now.Add(shortletApprovalTTL)
		if st.InstantBook && (verified || !st.GuestVerificationRequired) {
			next, hold = "PENDING_PAYMENT", now.Add(shortletPaymentHold)
		}
		if err := s.q.SetBookingHold(ctx, tx, db.SetBookingHoldParams{ID: b.ID, HoldExpiresAt: &hold, UpdatedAt: now}); err != nil {
			return err
		}
		if err := s.bookingTo(ctx, b.ID, next, "request", ""); err != nil {
			return err
		}
		if next == "PENDING_PAYMENT" && st.PaymentCollection == "OFF_PLATFORM" {
			if err := s.confirmBooking(ctx, b.ID); err != nil {
				return err
			}
		}
		if err := s.emitAvailability(ctx, l, "BOOKING", []AvailabilityRange{{From: in.CheckIn, To: checkOut.AddDate(0, 0, int(st.TurnoverBufferDays)-1).Format(time.DateOnly), Status: "HOLD"}}); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateBookingRequested, map[string]string{"listing_title": l.Title, "check_in": in.CheckIn}); err != nil {
			return err
		}
		if err := s.record(ctx, "shortlet.booking.request", AggBooking, b.ID, map[string]any{"nights": nights, "guests": in.Guests}, nil); err != nil {
			return err
		}
		n, err := s.q.GetBooking(ctx, tx, b.ID)
		if err != nil {
			return err
		}
		out, err = s.bookingView(ctx, n)
		return err
	})
	return out, err
}

// claimStay claims the nights (and the turnover buffer) for a booking.
func (s *Service) claimStay(ctx context.Context, b db.ShortletBooking, st db.ShortletSetting, status string) error {
	tx, now := mustTx(ctx), s.now()
	last := b.CheckOut.AddDate(0, 0, -1)
	n, err := s.q.SetCalendarRange(ctx, tx, db.SetCalendarRangeParams{ListingID: b.ListingID, Status: status, Source: "BOOKING", RefID: &b.ID, Now: now,
		FromDay: b.CheckIn, ToDay: last})
	if err != nil {
		return err
	}
	if n != int64(len(domain.Nights(b.CheckIn, b.CheckOut))) {
		return conflict("DATES_UNAVAILABLE", "Those dates are no longer available.")
	}
	if st.TurnoverBufferDays > 0 {
		bufEnd := b.CheckOut.AddDate(0, 0, int(st.TurnoverBufferDays)-1)
		n, err := s.q.SetCalendarRange(ctx, tx, db.SetCalendarRangeParams{ListingID: b.ListingID, Status: "BLOCKED", Source: "BOOKING", RefID: &b.ID, Now: now,
			FromDay: b.CheckOut, ToDay: bufEnd})
		if err != nil {
			return err
		}
		if n != int64(st.TurnoverBufferDays) {
			return conflict("DATES_UNAVAILABLE", "The turnover days after the stay are not available.")
		}
	}
	return nil
}

// confirmBooking confirms a booking and books its nights.
func (s *Service) confirmBooking(ctx context.Context, id ids.ID) error {
	tx, now := mustTx(ctx), s.now()
	if err := s.bookingTo(ctx, id, "CONFIRMED", "confirm", ""); err != nil {
		return err
	}
	if err := s.q.ConfirmBookingDays(ctx, tx, db.ConfirmBookingDaysParams{RefID: &id, UpdatedAt: now}); err != nil {
		return err
	}
	return s.q.SetBookingStay(ctx, tx, db.SetBookingStayParams{ID: id, Now: now})
}

// releaseBooking cancels/ends a booking and frees its dates.
func (s *Service) releaseBooking(ctx context.Context, b db.ShortletBooking, to, by, reason string, refund int64) error {
	tx, now := mustTx(ctx), s.now()
	if err := s.bookingTo(ctx, b.ID, to, lower(to), reason); err != nil {
		return err
	}
	if err := s.q.ReleaseBookingDays(ctx, tx, db.ReleaseBookingDaysParams{RefID: &b.ID, UpdatedAt: now}); err != nil {
		return err
	}
	if err := s.q.SetBookingCancelled(ctx, tx, db.SetBookingCancelledParams{ID: b.ID, CancelledBy: &by, CancelReason: &reason, RefundDueMinor: refund, UpdatedAt: now}); err != nil {
		return err
	}
	if err := s.q.CancelBookingTurnover(ctx, tx, db.CancelBookingTurnoverParams{BookingID: &b.ID, UpdatedAt: now}); err != nil {
		return err
	}
	l, err := s.q.GetListing(ctx, tx, b.ListingID)
	if err != nil {
		return err
	}
	return s.emitAvailability(ctx, l, "BOOKING", []AvailabilityRange{{From: b.CheckIn.Format(time.DateOnly), To: b.CheckOut.AddDate(0, 0, -1).Format(time.DateOnly), Status: "AVAILABLE"}})
}

func (s *Service) bookingFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.ShortletBooking, authz.Decision, error) {
	b, err := s.q.GetBooking(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return b, authz.Decision{}, notFound(AggBooking)
	}
	if err != nil {
		return b, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, b.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggBooking)
	}
	if err == nil && !isReadAction(action) {
		b, err = s.q.GetBookingForUpdate(ctx, mustTx(ctx), id)
	}
	return b, d, err
}

// GetBooking returns a booking.
func (s *Service) GetBooking(ctx context.Context, subj authz.Subject, id ids.ID) (BookingView, error) {
	b, _, err := s.bookingFor(ctx, subj, id, authz.Read(authz.CapShortletBookings))
	if err != nil {
		return BookingView{}, err
	}
	return s.bookingView(ctx, b)
}

// ListBookings pages bookings across the caller's short-let listings.
func (s *Service) ListBookings(ctx context.Context, subj authz.Subject, listingID ids.ID, status, cursor string, limit int) (pagination.Response[BookingView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[BookingView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[BookingView]{}, err
	}
	rows, err := s.q.ListBookingsForAccount(ctx, s.dbq(ctx), db.ListBookingsForAccountParams{AccountID: accountOf(subj), Now: s.now(), ListingID: idp(listingID),
		Status: strp(status), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[BookingView]{}, err
	}
	out := make([]BookingView, 0, len(rows))
	for _, r := range rows {
		out = append(out, bookingBase(r))
	}
	return pagination.Build(out, limit, func(v BookingView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

// BookingCommand is an owner action on a booking (PATCH with If-Match).
type BookingCommand struct {
	Action         string  `json:"action" v:"required,enum=APPROVE|DECLINE|CHECK_IN|CHECK_OUT|NO_SHOW|CANCEL"`
	ReasonCode     *string `json:"reason_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
	AccessGrantRef *ids.ID `json:"access_grant_ref,omitempty" doc:"CHECK_IN: smart-access grant for the stay"`
}

// ActOnBooking applies an owner action.
func (s *Service) ActOnBooking(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in BookingCommand) (BookingView, error) {
	if err := schema.Validate(in); err != nil {
		return BookingView{}, err
	}
	var out BookingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		b, d, err := s.bookingFor(ctx, subj, id, authz.Do(authz.CapShortletBookings))
		if err != nil {
			return err
		}
		if err := checkVersion(expected, b.Version); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		today := dayOf(now)
		reason := deref(in.ReasonCode)
		switch in.Action {
		case "APPROVE":
			if b.Status != "PENDING_APPROVAL" {
				return apperrors.InvalidStateTransition(b.Status, "PENDING_PAYMENT")
			}
			if err := s.requireGuestVerification(ctx, b); err != nil {
				return err
			}
			if b.PaymentCollection == "OFF_PLATFORM" {
				if err := s.bookingTo(ctx, id, "PENDING_PAYMENT", "approve", ""); err != nil {
					return err
				}
				err = s.confirmBooking(ctx, id)
			} else {
				if err = s.bookingTo(ctx, id, "PENDING_PAYMENT", "approve", ""); err == nil {
					err = s.q.SetBookingHold(ctx, tx, db.SetBookingHoldParams{ID: id, HoldExpiresAt: ptr(now.Add(shortletPaymentHold)), UpdatedAt: now})
				}
			}
		case "DECLINE":
			if b.Status != "PENDING_APPROVAL" {
				return apperrors.InvalidStateTransition(b.Status, "CANCELLED")
			}
			err = s.releaseBooking(ctx, b, "CANCELLED", "OWNER", firstOr([]string{reason}, "OWNER_DECLINED"), 0)
		case "CANCEL":
			if !slices.Contains([]string{"HOLD", "PENDING_PAYMENT", "CONFIRMED"}, b.Status) {
				return apperrors.InvalidStateTransition(b.Status, "CANCELLED")
			}
			if reason == "" {
				return apperrors.InvalidRequest("reason_code is required to cancel.")
			}
			err = s.releaseBooking(ctx, b, "CANCELLED", "OWNER", reason, b.PaidMinor)
		case "CHECK_IN":
			if b.CheckIn.After(today) {
				return precondition("TOO_EARLY", "Check-in opens on the arrival date.")
			}
			if err := s.requireGuestVerification(ctx, b); err != nil {
				return err
			}
			if err = s.bookingTo(ctx, id, "CHECKED_IN", "check_in", ""); err == nil {
				err = s.q.SetBookingStay(ctx, tx, db.SetBookingStayParams{ID: id, CheckedInAt: &now, AccessGrantRef: in.AccessGrantRef, Now: now})
			}
		case "CHECK_OUT":
			if err = s.bookingTo(ctx, id, "CHECKED_OUT", "check_out", ""); err == nil {
				err = s.q.SetBookingStay(ctx, tx, db.SetBookingStayParams{ID: id, CheckedOutAt: &now, Now: now})
			}
			if err == nil {
				_, err = s.q.InsertTurnoverJob(ctx, tx, db.InsertTurnoverJobParams{ID: ids.New(), ListingID: b.ListingID, PropertyID: b.PropertyID, BookingID: &b.ID,
					JobType: "CLEANING", ScheduledFor: now, AssigneeType: "OWNER", Checklist: []byte("[]"), CreatedAt: now})
				if isNoRows(err) {
					err = nil
				}
			}
		case "NO_SHOW":
			if !b.CheckIn.Before(today) {
				return precondition("TOO_EARLY", "A no-show is recorded after the arrival date.")
			}
			err = s.releaseBooking(ctx, b, "NO_SHOW", "SYSTEM", "GUEST_NO_SHOW", 0)
		}
		if err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, b.PropertyID, "BOOKING_"+in.Action, AggBooking, id, "Booking "+lower(in.Action)); err != nil {
			return err
		}
		if err := s.record(ctx, "shortlet.booking."+lower(in.Action), AggBooking, id, map[string]any{"reason_code": reason}, nil); err != nil {
			return err
		}
		n, err := s.q.GetBooking(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.bookingView(ctx, n)
		return err
	})
	return out, err
}

func (s *Service) requireGuestVerification(ctx context.Context, b db.ShortletBooking) error {
	st, err := s.q.GetShortletSettings(ctx, mustTx(ctx), b.ListingID)
	if err != nil || !st.GuestVerificationRequired {
		return err
	}
	gs, err := s.q.ListGuests(ctx, mustTx(ctx), b.ID)
	if err != nil {
		return err
	}
	for _, g := range gs {
		if g.IsPrimary && g.VerificationStatus == "VERIFIED" {
			return nil
		}
	}
	return precondition("GUEST_VERIFICATION_REQUIRED", "The primary guest must be verified first.")
}

// ---- marketplace (guest) ----

func (s *Service) guestBooking(ctx context.Context, subj authz.Subject, id ids.ID, lock bool) (db.ShortletBooking, error) {
	customer, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return db.ShortletBooking{}, err
	}
	b, err := s.q.GetBooking(ctx, s.dbq(ctx), id)
	if isNoRows(err) || (err == nil && b.CustomerRefID != customer) {
		return b, notFound(AggBooking)
	}
	if err == nil && lock {
		b, err = s.q.GetBookingForUpdate(ctx, mustTx(ctx), id)
	}
	return b, err
}

// CustomerGetBooking returns the guest's booking.
func (s *Service) CustomerGetBooking(ctx context.Context, subj authz.Subject, id ids.ID) (BookingView, error) {
	b, err := s.guestBooking(ctx, subj, id, false)
	if err != nil {
		return BookingView{}, err
	}
	return s.bookingView(ctx, b)
}

// CustomerBookingAction is a guest action.
type CustomerBookingAction struct {
	Action             string  `json:"action" v:"required,enum=CANCEL|UPDATE_VERIFICATION"`
	GuestID            *ids.ID `json:"guest_id,omitempty"`
	VerificationRef    *string `json:"verification_ref,omitempty" v:"max=128"`
	VerificationStatus *string `json:"verification_status,omitempty" v:"enum=PENDING|VERIFIED|REJECTED|EXPIRED"`
}

// CustomerActOnBooking cancels (refund by policy) or updates a guest's
// verification projection.
func (s *Service) CustomerActOnBooking(ctx context.Context, subj authz.Subject, id ids.ID, in CustomerBookingAction) (BookingView, error) {
	if err := schema.Validate(in); err != nil {
		return BookingView{}, err
	}
	var out BookingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		b, err := s.guestBooking(ctx, subj, id, true)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if in.Action == "CANCEL" {
			if !slices.Contains([]string{"HOLD", "PENDING_APPROVAL", "PENDING_PAYMENT", "CONFIRMED"}, b.Status) {
				return apperrors.InvalidStateTransition(b.Status, "CANCELLED")
			}
			refund := domain.Refund(b.PaidMinor, domain.RefundBps(b.CancellationPolicy, false, now, b.CheckIn))
			if err := s.releaseBooking(ctx, b, "CANCELLED", "GUEST", "GUEST_CANCELLED", refund); err != nil {
				return err
			}
		} else {
			if in.GuestID == nil || in.VerificationStatus == nil {
				return apperrors.InvalidRequest("guest_id and verification_status are required.")
			}
			n, err := s.q.SetGuestVerification(ctx, tx, db.SetGuestVerificationParams{ID: *in.GuestID, BookingID: id, VerificationRef: in.VerificationRef,
				VerificationStatus: *in.VerificationStatus, UpdatedAt: now})
			if err != nil {
				return err
			}
			if n == 0 {
				return notFound("guest")
			}
		}
		if err := s.record(ctx, "shortlet.booking.guest_"+lower(in.Action), AggBooking, id, nil, nil); err != nil {
			return err
		}
		n, err := s.q.GetBooking(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.bookingView(ctx, n)
		return err
	})
	return out, err
}

// CustomerListBookings lists the guest's bookings.
func (s *Service) CustomerListBookings(ctx context.Context, subj authz.Subject) ([]BookingView, error) {
	customer, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.CustomerBookings(ctx, s.dbq(ctx), customer)
	out := make([]BookingView, 0, len(rows))
	for _, r := range rows {
		out = append(out, bookingBase(r))
	}
	return out, err
}

// CustomerShortletCalendar is the public inventory (no booking references).
func (s *Service) CustomerShortletCalendar(ctx context.Context, subj authz.Subject, listingID ids.ID, from, to time.Time) ([]CalendarDay, error) {
	if _, err := s.requireCustomer(ctx, subj); err != nil {
		return nil, err
	}
	if _, err := s.bookableListing(ctx, listingID, "SHORT_LET"); err != nil {
		return nil, err
	}
	return s.calendar(ctx, listingID, from, to, false)
}

// ---- turnover ----

// TurnoverInput schedules a turnover job.
type TurnoverInput struct {
	JobType       string     `json:"job_type" v:"required,enum=CLEANING|INSPECTION|RESTOCK|MAINTENANCE_CHECK"`
	ScheduledFor  time.Time  `json:"scheduled_for" v:"required"`
	AssigneeType  string     `json:"assignee_type" v:"required,enum=OWNER|MANAGER|ARTISAN"`
	AssigneeRefID *ids.ID    `json:"assignee_ref_id,omitempty" doc:"Manager assignment or artisan job reference"`
	Checklist     []string   `json:"checklist,omitempty" v:"max=50"`
	Notes         *string    `json:"notes,omitempty" v:"max=2000"`
	_             *time.Time // reserved
}

// TurnoverView is a turnover job.
type TurnoverView struct {
	ID               ids.ID     `json:"id"`
	ListingID        ids.ID     `json:"listing_id"`
	BookingID        *ids.ID    `json:"booking_id"`
	JobType          string     `json:"job_type"`
	Status           string     `json:"status"`
	ScheduledFor     time.Time  `json:"scheduled_for"`
	AssigneeType     string     `json:"assignee_type"`
	AssigneeRefID    *ids.ID    `json:"assignee_ref_id"`
	Checklist        []string   `json:"checklist"`
	Notes            *string    `json:"notes"`
	EvidenceMediaIDs []ids.ID   `json:"evidence_media_ids"`
	CompletedAt      *time.Time `json:"completed_at"`
	Version          int64      `json:"version"`
}

func turnoverView(j db.TurnoverJob) TurnoverView {
	v := TurnoverView{ID: j.ID, ListingID: j.ListingID, BookingID: j.BookingID, JobType: j.JobType, Status: j.Status, ScheduledFor: j.ScheduledFor,
		AssigneeType: j.AssigneeType, AssigneeRefID: j.AssigneeRefID, Notes: j.Notes, EvidenceMediaIDs: nonNilIDs(j.EvidenceMediaIds),
		CompletedAt: j.CompletedAt, Version: j.Version, Checklist: []string{}}
	_ = json.Unmarshal(j.Checklist, &v.Checklist)
	return v
}

// ScheduleTurnover creates a turnover job for a booking.
func (s *Service) ScheduleTurnover(ctx context.Context, subj authz.Subject, bookingID ids.ID, in TurnoverInput) (TurnoverView, error) {
	if err := schema.Validate(in); err != nil {
		return TurnoverView{}, err
	}
	var out TurnoverView
	err := s.InTx(ctx, func(ctx context.Context) error {
		b, _, err := s.bookingFor(ctx, subj, bookingID, authz.Do(authz.CapShortletManage))
		if err != nil {
			return err
		}
		if slices.Contains([]string{"CANCELLED", "NO_SHOW"}, b.Status) {
			return conflict("BOOKING_ENDED", "The booking was cancelled.")
		}
		cl := in.Checklist
		if cl == nil {
			cl = []string{}
		}
		j, err := s.q.InsertTurnoverJob(ctx, mustTx(ctx), db.InsertTurnoverJobParams{ID: ids.New(), ListingID: b.ListingID, PropertyID: b.PropertyID, BookingID: &b.ID,
			JobType: in.JobType, ScheduledFor: in.ScheduledFor, AssigneeType: in.AssigneeType, AssigneeRefID: in.AssigneeRefID, Checklist: jsonb(cl),
			Notes: in.Notes, CreatedAt: s.now()})
		if isNoRows(err) {
			return conflict("TURNOVER_EXISTS", "A "+in.JobType+" job is already scheduled for this booking.")
		}
		if err != nil {
			return err
		}
		out = turnoverView(j)
		return s.record(ctx, "shortlet.turnover.schedule", AggBooking, bookingID, map[string]any{"job_type": in.JobType}, nil)
	})
	return out, err
}

// ListTurnover lists a listing's turnover jobs.
func (s *Service) ListTurnover(ctx context.Context, subj authz.Subject, listingID ids.ID, bookingID ids.ID) ([]TurnoverView, error) {
	if _, _, err := s.shortletListing(ctx, subj, listingID, authz.Read(authz.CapShortletManage)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListTurnoverJobs(ctx, s.dbq(ctx), db.ListTurnoverJobsParams{ListingID: listingID, BookingID: idp(bookingID)})
	out := make([]TurnoverView, 0, len(rows))
	for _, r := range rows {
		out = append(out, turnoverView(r))
	}
	return out, err
}

// TurnoverUpdate progresses a turnover job (If-Match).
type TurnoverUpdate struct {
	Status           string   `json:"status" v:"required,enum=SCHEDULED|IN_PROGRESS|COMPLETED|ISSUE_REPORTED|CANCELLED"`
	Notes            *string  `json:"notes,omitempty" v:"max=2000"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=20" doc:"Added to the job's evidence"`
	Checklist        []string `json:"checklist,omitempty" v:"max=50"`
}

// MediaRefs lists referenced media.
func (in TurnoverUpdate) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// UpdateTurnover updates a turnover job; completion needs evidence.
func (s *Service) UpdateTurnover(ctx context.Context, subj authz.Subject, jobID ids.ID, expected int64, in TurnoverUpdate, ev Evidence) (TurnoverView, error) {
	if err := schema.Validate(in); err != nil {
		return TurnoverView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return TurnoverView{}, err
	}
	var out TurnoverView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		j, err := s.q.GetTurnoverJob(ctx, tx, jobID)
		if isNoRows(err) {
			return notFound("turnover_job")
		}
		if err != nil {
			return err
		}
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapShortletManage), j.PropertyID); err != nil {
			return err
		}
		if j.Status == "COMPLETED" || j.Status == "CANCELLED" {
			return conflict("TURNOVER_FINAL", "The job is "+j.Status+".")
		}
		media := append(nonNilIDs(j.EvidenceMediaIds), in.EvidenceMediaIDs...)
		if in.Status == "COMPLETED" && len(media) == 0 {
			return precondition("EVIDENCE_REQUIRED", "Attach photos of the finished turnover.")
		}
		cl := j.Checklist
		if in.Checklist != nil {
			cl = jsonb(in.Checklist)
		}
		var done *time.Time
		if in.Status == "COMPLETED" {
			done = ptr(s.now())
		}
		n, err := s.q.UpdateTurnoverJob(ctx, tx, db.UpdateTurnoverJobParams{ID: jobID, Version: expected, Status: in.Status, Notes: firstPtr(in.Notes, j.Notes),
			EvidenceMediaIds: media, Checklist: cl, CompletedAt: done, UpdatedAt: s.now()})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		out = turnoverView(n)
		return s.record(ctx, "shortlet.turnover.update", "turnover_job", jobID, map[string]any{"status": in.Status}, nil)
	})
	return out, err
}

func firstPtr[T any](a, b *T) *T {
	if a != nil {
		return a
	}
	return b
}

// ---- damage claims ----

// DamageItem is one itemized damage with evidence.
type DamageItem struct {
	Description string   `json:"description" v:"required,max=500"`
	AmountMinor int64    `json:"amount_minor" v:"required,min=1"`
	MediaIDs    []ids.ID `json:"media_ids" v:"required,max=10"`
}

// DamageClaimInput raises a claim after check-out.
type DamageClaimInput struct {
	Items []DamageItem `json:"items" v:"required,max=30"`
}

// MediaRefs lists referenced media.
func (in DamageClaimInput) MediaRefs() []ids.ID {
	var out []ids.ID
	for _, i := range in.Items {
		out = append(out, i.MediaIDs...)
	}
	return out
}

// DamageClaimView is a damage claim.
type DamageClaimView struct {
	ID                    ids.ID       `json:"id"`
	BookingID             ids.ID       `json:"booking_id"`
	Status                string       `json:"status"`
	Items                 []DamageItem `json:"items"`
	ClaimedMinor          int64        `json:"claimed_minor"`
	Currency              string       `json:"currency"`
	RespondBy             time.Time    `json:"respond_by"`
	GuestResponseNote     *string      `json:"guest_response_note"`
	GuestEvidenceMediaIDs []ids.ID     `json:"guest_evidence_media_ids"`
	AwardedMinor          *int64       `json:"awarded_minor"`
	Settlement            *string      `json:"settlement"`
	PaidMinor             int64        `json:"paid_minor"`
	SettledAt             *time.Time   `json:"settled_at"`
	Version               int64        `json:"version"`
}

func claimView(c db.DamageClaim) DamageClaimView {
	v := DamageClaimView{ID: c.ID, BookingID: c.BookingID, Status: c.Status, ClaimedMinor: c.ClaimedMinor, Currency: c.Currency, RespondBy: c.RespondBy,
		GuestResponseNote: c.GuestResponseNote, GuestEvidenceMediaIDs: nonNilIDs(c.GuestEvidenceMediaIds), AwardedMinor: c.AwardedMinor,
		Settlement: c.Settlement, PaidMinor: c.PaidMinor, SettledAt: c.SettledAt, Version: c.Version, Items: []DamageItem{}}
	_ = json.Unmarshal(c.Items, &v.Items)
	return v
}

// RaiseDamageClaim raises an evidence-backed claim within the claim window.
func (s *Service) RaiseDamageClaim(ctx context.Context, subj authz.Subject, bookingID ids.ID, in DamageClaimInput, ev Evidence) (DamageClaimView, error) {
	if err := schema.Validate(in); err != nil {
		return DamageClaimView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return DamageClaimView{}, err
	}
	var out DamageClaimView
	err := s.InTx(ctx, func(ctx context.Context) error {
		b, _, err := s.bookingFor(ctx, subj, bookingID, authz.Do(authz.CapShortletBookings))
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		st, err := s.q.GetShortletSettings(ctx, tx, b.ListingID)
		if err != nil {
			return err
		}
		if b.Status != "CHECKED_OUT" || b.CheckedOutAt == nil || now.After(b.CheckedOutAt.Add(time.Duration(st.DamageClaimWindowHours)*time.Hour)) {
			return conflict("CLAIM_WINDOW_CLOSED", "Damage claims are raised after check-out, within the claim window.")
		}
		var total int64
		for _, i := range in.Items {
			total += i.AmountMinor
		}
		c, err := s.q.InsertDamageClaim(ctx, tx, db.InsertDamageClaimParams{ID: ids.New(), BookingID: b.ID, PropertyID: b.PropertyID, Items: jsonb(in.Items),
			ClaimedMinor: total, Currency: b.Currency, RespondBy: now.Add(damageResponseWindow), CreatedAt: now})
		if isUnique(err, "damage_claims_open_uniq") {
			return conflict("CLAIM_ALREADY_OPEN", "A damage claim is already open for this booking.")
		}
		if err != nil {
			return err
		}
		out = claimView(c)
		return s.record(ctx, "shortlet.damage_claim", AggBooking, bookingID, map[string]any{"claimed_minor": total, "items": len(in.Items)}, nil)
	})
	return out, err
}

// ListDamageClaims lists a booking's claims.
func (s *Service) ListDamageClaims(ctx context.Context, subj authz.Subject, bookingID ids.ID) ([]DamageClaimView, error) {
	if _, _, err := s.bookingFor(ctx, subj, bookingID, authz.Read(authz.CapShortletBookings)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListDamageClaims(ctx, s.dbq(ctx), bookingID)
	out := make([]DamageClaimView, 0, len(rows))
	for _, r := range rows {
		out = append(out, claimView(r))
	}
	return out, err
}

func (s *Service) updateClaim(ctx context.Context, c db.DamageClaim, status string, note *string, guestMedia []ids.ID, award *int64, settlement *string, settled *time.Time) (db.DamageClaim, error) {
	return s.q.UpdateDamageClaim(ctx, mustTx(ctx), db.UpdateDamageClaimParams{ID: c.ID, Status: status, GuestResponseNote: note,
		GuestEvidenceMediaIds: append(nonNilIDs(c.GuestEvidenceMediaIds), guestMedia...), AwardedMinor: award, Settlement: settlement, SettledAt: settled,
		UpdatedAt: s.now()})
}

// agreeClaim moves a claim to an agreed amount and its settlement path.
func (s *Service) agreeClaim(ctx context.Context, c db.DamageClaim, status string, amount int64, guestMedia []ids.ID, note *string) (db.DamageClaim, error) {
	b, err := s.q.GetBooking(ctx, mustTx(ctx), c.BookingID)
	if err != nil {
		return c, err
	}
	settlement := b.PaymentCollection
	if amount == 0 {
		return s.updateClaim(ctx, c, "SETTLED", note, guestMedia, &amount, &settlement, ptr(s.now()))
	}
	return s.updateClaim(ctx, c, status, note, guestMedia, &amount, &settlement, nil)
}

// CustomerDamageResponse is the guest's answer to a claim.
type CustomerDamageResponse struct {
	Decision         string   `json:"decision" v:"required,enum=ACCEPT|DISPUTE"`
	Note             *string  `json:"note,omitempty" v:"max=2000"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=10"`
}

// CustomerMediaRefs lists referenced customer media.
func (in CustomerDamageResponse) CustomerMediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// CustomerRespondDamageClaim records the guest's acceptance or dispute.
func (s *Service) CustomerRespondDamageClaim(ctx context.Context, subj authz.Subject, claimID ids.ID, in CustomerDamageResponse, ev Evidence) (DamageClaimView, error) {
	if err := schema.Validate(in); err != nil {
		return DamageClaimView{}, err
	}
	if err := ev.require(in.CustomerMediaRefs()...); err != nil {
		return DamageClaimView{}, err
	}
	var out DamageClaimView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		c, err := s.q.GetDamageClaimForUpdate(ctx, tx, claimID)
		if isNoRows(err) {
			return notFound("damage_claim")
		}
		if err != nil {
			return err
		}
		b, err := s.guestBooking(ctx, subj, c.BookingID, true)
		if err != nil {
			return notFound("damage_claim")
		}
		if c.Status != "OPEN" {
			return conflict("CLAIM_NOT_OPEN", "The claim is "+c.Status+".")
		}
		if in.Decision == "ACCEPT" {
			c, err = s.agreeClaim(ctx, c, "GUEST_ACCEPTED", c.ClaimedMinor, in.EvidenceMediaIDs, in.Note)
		} else {
			if c, err = s.updateClaim(ctx, c, "DISPUTED", in.Note, in.EvidenceMediaIDs, nil, nil, nil); err == nil {
				err = s.bookingTo(ctx, b.ID, "DISPUTED", "damage_dispute", "")
			}
		}
		if err != nil {
			return err
		}
		l, err := s.q.GetListing(ctx, tx, b.ListingID)
		if err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, b.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateDamageResponse, map[string]string{"listing_title": l.Title, "decision": lower(in.Decision)}); err != nil {
			return err
		}
		out = claimView(c)
		return s.record(ctx, "shortlet.damage_claim.guest", AggBooking, b.ID, map[string]any{"decision": in.Decision}, nil)
	})
	return out, err
}

// DamageClaimAction is an owner action on a claim.
type DamageClaimAction struct {
	Action           string   `json:"action" v:"required,enum=WITHDRAW|RECORD_SETTLEMENT"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=10" doc:"RECORD_SETTLEMENT (off-platform): proof of the guest's payment"`
}

// MediaRefs lists referenced media.
func (in DamageClaimAction) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// ActOnDamageClaim withdraws a claim or records an off-platform settlement
// of an agreed/awarded amount (platform settlements come from payment-service).
func (s *Service) ActOnDamageClaim(ctx context.Context, subj authz.Subject, claimID ids.ID, in DamageClaimAction, ev Evidence) (DamageClaimView, error) {
	if err := schema.Validate(in); err != nil {
		return DamageClaimView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return DamageClaimView{}, err
	}
	var out DamageClaimView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		c, err := s.q.GetDamageClaimForUpdate(ctx, tx, claimID)
		if isNoRows(err) {
			return notFound("damage_claim")
		}
		if err != nil {
			return err
		}
		b, _, err := s.bookingFor(ctx, subj, c.BookingID, authz.Do(authz.CapShortletBookings))
		if err != nil {
			return err
		}
		switch {
		case in.Action == "WITHDRAW" && slices.Contains([]string{"OPEN", "GUEST_ACCEPTED", "DISPUTED"}, c.Status):
			if c, err = s.updateClaim(ctx, c, "WITHDRAWN", nil, nil, c.AwardedMinor, c.Settlement, nil); err == nil && b.Status == "DISPUTED" {
				err = s.bookingTo(ctx, b.ID, "COMPLETED", "claim_withdrawn", "")
			}
		case in.Action == "RECORD_SETTLEMENT" && slices.Contains([]string{"GUEST_ACCEPTED", "RESOLVED"}, c.Status) && deref(c.Settlement) == "OFF_PLATFORM":
			if len(in.EvidenceMediaIDs) == 0 {
				return precondition("EVIDENCE_REQUIRED", "Attach proof of the guest's payment.")
			}
			now := s.now()
			c, err = s.q.UpdateDamageClaim(ctx, tx, db.UpdateDamageClaimParams{ID: c.ID, Status: "SETTLED", GuestEvidenceMediaIds: nonNilIDs(c.GuestEvidenceMediaIds),
				AwardedMinor: c.AwardedMinor, Settlement: c.Settlement, SettledAt: &now, UpdatedAt: now})
			if err == nil {
				err = s.record(ctx, "shortlet.damage_claim.offline_settlement", AggBooking, b.ID, map[string]any{"amount_minor": c.AwardedMinor},
					map[string]any{"evidence_media_ids": in.EvidenceMediaIDs, "attested_by": "OWNER"})
			}
		default:
			return conflict("CLAIM_ACTION_NOT_ALLOWED", "The claim is "+c.Status+" ("+deref(c.Settlement)+").")
		}
		if err != nil {
			return err
		}
		out = claimView(c)
		return s.record(ctx, "shortlet.damage_claim."+lower(in.Action), AggBooking, b.ID, nil, nil)
	})
	return out, err
}

// DamageClaimResolution is an operator's decision on a disputed claim.
type DamageClaimResolution struct {
	AwardedMinor int64   `json:"awarded_minor" v:"min=0" doc:"Amount the guest owes (0 rejects the claim)"`
	Note         *string `json:"note,omitempty" v:"max=1000"`
}

// AdminResolveDamageClaim decides a disputed claim (admin dispute contract).
func (s *Service) AdminResolveDamageClaim(ctx context.Context, subj authz.Subject, claimID ids.ID, in DamageClaimResolution) (DamageClaimView, error) {
	if err := schema.Validate(in); err != nil {
		return DamageClaimView{}, err
	}
	var out DamageClaimView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminDispute, authz.Resource{}); err != nil {
			return err
		}
		c, err := s.q.GetDamageClaimForUpdate(ctx, mustTx(ctx), claimID)
		if isNoRows(err) {
			return notFound("damage_claim")
		}
		if err != nil {
			return err
		}
		if c.Status != "DISPUTED" {
			return conflict("CLAIM_NOT_DISPUTED", "The claim is not disputed.")
		}
		if in.AwardedMinor > c.ClaimedMinor {
			return precondition("AWARD_EXCEEDS_CLAIM", "The award cannot exceed the claim.")
		}
		if c, err = s.agreeClaim(ctx, c, "RESOLVED", in.AwardedMinor, nil, in.Note); err != nil {
			return err
		}
		if err := s.bookingTo(ctx, c.BookingID, "COMPLETED", "claim_resolved", ""); err != nil {
			return err
		}
		out = claimView(c)
		return s.record(ctx, "shortlet.damage_claim.resolve", AggBooking, c.BookingID, map[string]any{"awarded_minor": in.AwardedMinor}, nil)
	})
	return out, err
}

// ---- jobs (§11 short-let calendar, turnover) ----

// RunShortletJobs expires holds, completes stays past the claim window,
// requests and reconciles payments/refunds/claim settlements, applies the
// guest-response deadline of claims and imports external calendars.
func (s *Service) RunShortletJobs(ctx context.Context, batch int32) error {
	now := s.now()
	holds, err := s.q.ExpiredHolds(ctx, s.Pool, db.ExpiredHoldsParams{HoldExpiresAt: &now, Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range holds {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			b, err := s.q.GetBookingForUpdate(ctx, mustTx(ctx), id)
			if err != nil || b.HoldExpiresAt == nil || b.HoldExpiresAt.After(now) {
				return err
			}
			return s.releaseBooking(ctx, b, "CANCELLED", "SYSTEM", "HOLD_EXPIRED", b.PaidMinor)
		}); err != nil && !benign(err) {
			return err
		}
	}
	done, err := s.q.BookingsToComplete(ctx, s.Pool, db.BookingsToCompleteParams{Now: now, MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range done {
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.bookingTo(ctx, id, "COMPLETED", "stay_completed", "") }); err != nil && !benign(err) {
			return err
		}
	}
	lapsed, err := s.q.ExpiredClaimResponses(ctx, s.Pool, db.ExpiredClaimResponsesParams{RespondBy: now, Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range lapsed {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			c, err := s.q.GetDamageClaimForUpdate(ctx, mustTx(ctx), id)
			if err != nil || c.Status != "OPEN" {
				return err
			}
			if _, err := s.agreeClaim(ctx, c, "GUEST_ACCEPTED", c.ClaimedMinor, nil, nil); err != nil {
				return err
			}
			return s.record(ctx, "shortlet.damage_claim.response_window_lapsed", AggBooking, c.BookingID, nil,
				map[string]any{"policy": "no response by the deadline accepts the claim"})
		}); err != nil {
			return err
		}
	}
	if err := s.runExternalCalendars(ctx, batch); err != nil {
		return err
	}
	if s.Payment == nil {
		return nil
	}
	return s.runShortletPayments(ctx, batch)
}

func (s *Service) runShortletPayments(ctx context.Context, batch int32) error {
	now := s.now()
	need, err := s.q.BookingsNeedingIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range need {
		if err := s.requestBookingIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "booking payment intent deferred", "booking_id", id, "error", err)
		}
	}
	recon, err := s.q.BookingsToReconcile(ctx, s.Pool, db.BookingsToReconcileParams{CheckedBefore: now.Add(-s.Policy.Domain.PaymentReconcileAfter), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range recon {
		b, err := s.q.GetBooking(ctx, s.Pool, id)
		if err != nil {
			return err
		}
		in, err := s.Payment.GetIntent(ctx, *b.PaymentIntentRef)
		if err != nil {
			s.Logger.WarnContext(ctx, "booking payment read-back deferred", "booking_id", id, "error", err)
			continue
		}
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.projectBookingIntent(ctx, id, in) }); err != nil && !benign(err) {
			return err
		}
	}
	stale, err := s.q.CancelledBookingIntents(ctx, s.Pool, db.CancelledBookingIntentsParams{CheckedBefore: now.Add(-s.Policy.Domain.PaymentReconcileAfter), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range stale {
		b, err := s.q.GetBooking(ctx, s.Pool, id)
		if err != nil {
			return err
		}
		in, err := s.Payment.CancelIntent(ctx, *b.PaymentIntentRef, "booking "+lower(b.Status))
		if err != nil {
			s.Logger.WarnContext(ctx, "booking intent cancel deferred", "booking_id", id, "error", err)
			continue
		}
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.projectBookingIntent(ctx, id, in) }); err != nil && !benign(err) {
			return err
		}
	}
	refunds, err := s.q.BookingsNeedingRefund(ctx, s.Pool, db.BookingsNeedingRefundParams{CheckedBefore: now.Add(-s.Policy.Domain.PaymentReconcileAfter), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range refunds {
		if err := s.processBookingRefund(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "booking refund deferred", "booking_id", id, "error", err)
		}
	}
	claims, err := s.q.ClaimsNeedingIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range claims {
		if err := s.requestClaimIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "damage claim intent deferred", "claim_id", id, "error", err)
		}
	}
	open, err := s.q.ClaimsToReconcile(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range open {
		var ref string
		if err := s.Pool.QueryRow(ctx, `SELECT payment_intent_ref FROM damage_claims WHERE id = $1`, id).Scan(&ref); err != nil {
			return err
		}
		in, err := s.Payment.GetIntent(ctx, ref)
		if err != nil {
			s.Logger.WarnContext(ctx, "damage claim read-back deferred", "claim_id", id, "error", err)
			continue
		}
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.projectClaimIntent(ctx, id, in) }); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) requestBookingIntent(ctx context.Context, id ids.ID) error {
	b, err := s.q.GetBooking(ctx, s.Pool, id)
	if err != nil || b.PaymentIntentRef != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, s.Pool, b.PropertyID)
	if err != nil {
		return err
	}
	in, err := s.Payment.CreateIntent(ctx, payment.IntentRequest{Purpose: payment.PurposeShortletStay, ReferenceType: AggBooking, ReferenceID: b.ID,
		PayeeOwnerAccountID: p.OwnerAccountID, PayerDomain: string(platform.DomainMarketplace), PayerRefID: &b.CustomerRefID, AmountMinor: b.TotalMinor,
		Currency: b.Currency, DueDate: deref(b.HoldExpiresAt), Escrow: true}, "shortlet_booking:"+b.ID.String())
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		if err := s.q.SetBookingIntent(ctx, mustTx(ctx), db.SetBookingIntentParams{ID: id, PaymentIntentRef: &in.Ref, LastPaymentStatus: &in.Status,
			LastCheckedAt: ptr(s.now())}); err != nil {
			return err
		}
		return s.projectBookingIntent(ctx, id, in)
	})
}

// projectBookingIntent applies payment truth: a fully paid booking is
// confirmed; money arriving after cancellation is refunded.
func (s *Service) projectBookingIntent(ctx context.Context, id ids.ID, in payment.Intent) error {
	tx, now := mustTx(ctx), s.now()
	b, err := s.q.GetBookingForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := s.q.SetBookingPayment(ctx, tx, db.SetBookingPaymentParams{ID: id, PaidMinor: in.PaidMinor, LastPaymentStatus: &in.Status, LastCheckedAt: &now}); err != nil {
		return err
	}
	paid := (in.Status == payment.IntentSucceeded || in.Status == payment.IntentHeld) && in.PaidMinor >= b.TotalMinor
	switch {
	case paid && b.Status == "PENDING_PAYMENT":
		return s.confirmBooking(ctx, id)
	case in.PaidMinor > b.PaidMinor && b.Status == "CANCELLED":
		// Money that arrived after cancellation is returned in full.
		if err := s.q.AddBookingRefundDue(ctx, tx, db.AddBookingRefundDueParams{ID: id, AmountMinor: in.PaidMinor - b.PaidMinor, Now: now}); err != nil {
			return err
		}
		return s.record(ctx, "shortlet.booking.payment_after_cancellation", AggBooking, id, map[string]any{"amount_minor": in.PaidMinor - b.PaidMinor}, nil)
	}
	return nil
}

// processBookingRefund requests (or reads back) the refund owed on a
// cancelled booking. refunded_minor accumulates completed refunds; a later
// increase of refund_due_minor (money arriving after cancellation) requests
// the difference under a new idempotency key.
func (s *Service) processBookingRefund(ctx context.Context, id ids.ID) error {
	b, err := s.q.GetBooking(ctx, s.Pool, id)
	if err != nil {
		return err
	}
	var r payment.Refund
	refunded := b.RefundedMinor
	if b.RefundRequestRef != nil && deref(b.RefundStatus) == payment.RefundPending {
		if r, err = s.Payment.GetRefund(ctx, *b.RefundRequestRef); err == nil && r.Status == payment.RefundCompleted {
			refunded += r.RefundedMinor
		}
	} else {
		if b.RefundDueMinor <= b.RefundedMinor {
			return nil
		}
		key := fmt.Sprintf("shortlet_booking:%s:refund:%d", b.ID, b.RefundDueMinor)
		if deref(b.RefundStatus) == payment.RefundFailed && b.LastCheckedAt != nil {
			key += ":" + b.LastCheckedAt.Format("20060102150405")
		}
		r, err = s.Payment.RequestRefund(ctx, payment.RefundRequest{IntentRef: *b.PaymentIntentRef, ReferenceType: AggBooking, ReferenceID: b.ID,
			AmountMinor: b.RefundDueMinor - b.RefundedMinor, Reason: deref(b.CancelReason)}, key)
		if err == nil && r.Status == payment.RefundCompleted {
			refunded += r.RefundedMinor
		}
	}
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		if err := s.q.SetBookingRefund(ctx, mustTx(ctx), db.SetBookingRefundParams{ID: id, RefundRequestRef: &r.Ref, RefundStatus: r.Status,
			RefundedMinor: refunded, Now: s.now()}); err != nil {
			return err
		}
		if r.Status == payment.RefundCompleted {
			return s.record(ctx, "shortlet.booking.refunded", AggBooking, id, map[string]any{"refunded_minor": refunded}, nil)
		}
		return nil
	})
}

func (s *Service) requestClaimIntent(ctx context.Context, id ids.ID) error {
	var c db.DamageClaim
	var err error
	if c, err = s.q.GetDamageClaimForUpdate(ctx, s.Pool, id); err != nil || c.PaymentIntentRef != nil {
		return err
	}
	b, err := s.q.GetBooking(ctx, s.Pool, c.BookingID)
	if err != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, s.Pool, c.PropertyID)
	if err != nil {
		return err
	}
	in, err := s.Payment.CreateIntent(ctx, payment.IntentRequest{Purpose: payment.PurposeDamageClaim, ReferenceType: "damage_claim", ReferenceID: c.ID,
		PayeeOwnerAccountID: p.OwnerAccountID, PayerDomain: string(platform.DomainMarketplace), PayerRefID: &b.CustomerRefID, AmountMinor: deref(c.AwardedMinor),
		Currency: c.Currency, DueDate: s.now().Add(7 * 24 * time.Hour)}, "damage_claim:"+c.ID.String())
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		if err := s.q.SetClaimIntent(ctx, mustTx(ctx), db.SetClaimIntentParams{ID: id, PaymentIntentRef: &in.Ref, LastPaymentStatus: &in.Status, UpdatedAt: s.now()}); err != nil {
			return err
		}
		return s.projectClaimIntent(ctx, id, in)
	})
}

func (s *Service) projectClaimIntent(ctx context.Context, id ids.ID, in payment.Intent) error {
	tx, now := mustTx(ctx), s.now()
	if err := s.q.SetClaimPayment(ctx, tx, db.SetClaimPaymentParams{ID: id, PaidMinor: in.PaidMinor, LastPaymentStatus: &in.Status, UpdatedAt: now}); err != nil {
		return err
	}
	c, err := s.q.GetDamageClaimForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if in.Status != payment.IntentSucceeded || in.PaidMinor < deref(c.AwardedMinor) || c.Status == "SETTLED" {
		return nil
	}
	_, err = s.updateClaim(ctx, c, "SETTLED", nil, nil, c.AwardedMinor, c.Settlement, &now)
	return err
}

// runExternalCalendars imports busy ranges from external calendars: free
// days are blocked (source EXTERNAL), stale external blocks are released,
// and overlaps with platform bookings are reported, never overwritten.
func (s *Service) runExternalCalendars(ctx context.Context, batch int32) error {
	if s.CalendarSync == nil {
		return nil
	}
	settings, err := s.q.SettingsWithExternalCalendar(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, st := range settings {
		ranges, err := s.CalendarSync.Fetch(ctx, *st.ExternalCalendarRef)
		if err != nil {
			s.Logger.WarnContext(ctx, "external calendar fetch deferred", "listing_id", st.ListingID, "error", err)
			continue
		}
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.importExternal(ctx, st, ranges) }); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) importExternal(ctx context.Context, st db.ShortletSetting, ranges []calendarsync.BusyRange) error {
	tx, now := mustTx(ctx), s.now()
	today := dayOf(now)
	horizon := today.AddDate(0, 0, externalCalendarWindow)
	if _, err := tx.Exec(ctx, `UPDATE availability_calendar SET status = 'AVAILABLE', source = 'SYSTEM', updated_at = $3
		WHERE listing_id = $1 AND source = 'EXTERNAL' AND day BETWEEN $2 AND $4`, st.ListingID, today, now, horizon); err != nil {
		return err
	}
	var conflicts []string
	var applied []AvailabilityRange
	for _, r := range ranges {
		from, to := dayOf(r.Start), dayOf(r.End).AddDate(0, 0, -1)
		if to.Before(today) || from.After(horizon) || to.Before(from) {
			continue
		}
		if from.Before(today) {
			from = today
		}
		busy, err := s.q.ExternalConflicts(ctx, tx, db.ExternalConflictsParams{ListingID: st.ListingID, FromDay: from, ToDay: to.AddDate(0, 0, 1)})
		if err != nil {
			return err
		}
		for _, b := range busy {
			conflicts = append(conflicts, b.Day.Format(time.DateOnly))
		}
		if _, err := s.q.SetCalendarRange(ctx, tx, db.SetCalendarRangeParams{ListingID: st.ListingID, Status: "BLOCKED", Source: "EXTERNAL", Now: now,
			FromDay: from, ToDay: to}); err != nil {
			return err
		}
		applied = append(applied, AvailabilityRange{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Status: "BLOCKED"})
	}
	l, err := s.q.GetListing(ctx, tx, st.ListingID)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		if err := s.emitAvailability(ctx, l, "EXTERNAL", applied); err != nil {
			return err
		}
	}
	if len(conflicts) > 0 {
		p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateCalendarConflict, map[string]string{"listing_title": l.Title,
			"dates": joinCodes(conflicts)}); err != nil {
			return err
		}
	}
	return s.record(ctx, "shortlet.calendar.external_import", AggListing, l.ID, map[string]any{"ranges": len(applied), "conflicts": conflicts}, nil)
}

func init() {
	produces("shortlet.availability.updated.v1", AggListing, "Short-let date inventory changed (owner blocks, booking holds/releases, external imports).", "Marketplace", ShortletAvailabilityEvent{})
	produces("shortlet.booking.updated.v1", AggBooking, "Short-let booking state (dates, guests, total; never payment details).", "Marketplace, Notification, Payment", ShortletBookingEvent{})
	closureHoldChecks = append(closureHoldChecks, struct{ code, sql string }{"UPCOMING_BOOKING", `SELECT EXISTS (SELECT 1 FROM shortlet_bookings b
		JOIN properties p ON p.id = b.property_id WHERE p.owner_account_id = $1 AND b.status IN ('PENDING_PAYMENT','CONFIRMED','CHECKED_IN','CHECKED_OUT','DISPUTED'))`})
	archiveBlockerChecks = append(archiveBlockerChecks, struct{ name, sql string }{"short-let booking",
		`SELECT EXISTS (SELECT 1 FROM shortlet_bookings WHERE property_id = $1 AND status IN ('HOLD','PENDING_APPROVAL','PENDING_PAYMENT','CONFIRMED','CHECKED_IN','CHECKED_OUT','DISPUTED'))`})
}
