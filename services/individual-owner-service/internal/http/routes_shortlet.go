package http

import (
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

const tagShortlet = "Short-let"

// dateRange parses the from/to query (defaults: today and 90 days ahead).
func dateRange(c *Call) (time.Time, time.Time, error) {
	from, to := time.Now().UTC().Truncate(24*time.Hour), time.Time{}
	if v := c.Query("from"); v != "" {
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return from, to, apperrors.InvalidRequest("from must be a date (YYYY-MM-DD).")
		}
		from = d
	}
	to = from.AddDate(0, 0, 90)
	if v := c.Query("to"); v != "" {
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return from, to, apperrors.InvalidRequest("to must be a date (YYYY-MM-DD).")
		}
		to = d
	}
	return from, to, nil
}

var rangeParams = []Param{{Name: "from", Doc: "First day (default today)"}, {Name: "to", Doc: "Last day, inclusive (default from + 90 days; at most 366 days)"}}

func shortletRoutes() []Route {
	lid := func(c *Call) ids.ID { return c.ID("listing_id") }
	bid := func(c *Call) ids.ID { return c.ID("booking_id") }
	return []Route{
		Get("/listings/{listing_id}/shortlet/settings", "getShortletSettings", tagShortlet, "Short-let settings",
			func(c *Call) (usecases.ShortletSettingsView, error) {
				return c.S.GetShortletSettings(c.Ctx, c.Subj, lid(c))
			}),
		Update("PUT", "/listings/{listing_id}/shortlet/settings", "putShortletSettings", tagShortlet, "Configure a short-let listing (If-Match; \"0\" to create)", true,
			func(c *Call, in *usecases.ShortletSettingsInput) (usecases.ShortletSettingsView, error) {
				return c.S.PutShortletSettings(c.Ctx, c.Subj, lid(c), c.Version, *in)
			}).Describe("Guests, stay limits, check-in/out times, instant book, guest verification, cancellation policy, add-ons, turnover buffer and damage-claim window. "+
			"PLATFORM collection needs payment-service and an external calendar needs a calendar-sync provider (503 FEATURE_UNAVAILABLE otherwise).").Errs(409, 422, 503),
		List("/listings/{listing_id}/shortlet/rates", "getShortletRates", tagShortlet, "Active date rates",
			func(c *Call, _ Page) (pagination.Response[usecases.RateView], error) {
				return slice(c.S.GetRates(c.Ctx, c.Subj, lid(c)))
			}),
		Command("PUT", "/listings/{listing_id}/shortlet/rates", "putShortletRates", tagShortlet, "Replace the date rates", http.StatusOK,
			func(c *Call, in *usecases.RatesInput) ([]usecases.RateView, error) {
				return c.S.PutRates(c.Ctx, c.Subj, lid(c), *in)
			}).
			Describe("Non-overlapping ranges override the nightly rate (and may raise the minimum stay). Existing bookings keep their price snapshot.").Errs(422),
		List("/listings/{listing_id}/shortlet/calendar", "getShortletCalendar", tagShortlet, "Date inventory",
			func(c *Call, _ Page) (pagination.Response[usecases.CalendarDay], error) {
				from, to, err := dateRange(c)
				if err != nil {
					return pagination.Response[usecases.CalendarDay]{}, err
				}
				return slice(c.S.GetCalendar(c.Ctx, c.Subj, lid(c), from, to))
			}).Q(rangeParams...),
		Command("PUT", "/listings/{listing_id}/shortlet/calendar", "putShortletCalendar", tagShortlet, "Block or free days", http.StatusOK,
			func(c *Call, in *usecases.CalendarInput) ([]usecases.CalendarDay, error) {
				return c.S.PutCalendar(c.Ctx, c.Subj, lid(c), *in)
			}).
			Describe("Held or booked days cannot be changed: the whole update is refused with 409 CALENDAR_CONFLICT.").Errs(409),
		List("/listings/{listing_id}/shortlet/turnover", "listTurnoverJobs", tagShortlet, "Turnover jobs",
			func(c *Call, _ Page) (pagination.Response[usecases.TurnoverView], error) {
				b, err := c.QueryID("booking_id")
				if err != nil {
					return pagination.Response[usecases.TurnoverView]{}, err
				}
				return slice(c.S.ListTurnover(c.Ctx, c.Subj, lid(c), b))
			}).Q(Param{Name: "booking_id"}),
		List("/shortlet/bookings", "listShortletBookings", tagShortlet, "Bookings across my short-let listings",
			func(c *Call, p Page) (pagination.Response[usecases.BookingView], error) {
				l, err := c.QueryID("listing_id")
				if err != nil {
					return pagination.Response[usecases.BookingView]{}, err
				}
				return c.S.ListBookings(c.Ctx, c.Subj, l, c.Query("status"), p.Cursor, p.Limit)
			}).Q(Param{Name: "listing_id"}, Param{Name: "status", Enum: []string{"HOLD", "PENDING_APPROVAL", "PENDING_PAYMENT", "CONFIRMED", "CHECKED_IN",
			"CHECKED_OUT", "COMPLETED", "CANCELLED", "NO_SHOW", "DISPUTED"}}),
		Get("/shortlet/bookings/{booking_id}", "getShortletBooking", tagShortlet, "Booking with guests and price snapshot",
			func(c *Call) (usecases.BookingView, error) { return c.S.GetBooking(c.Ctx, c.Subj, bid(c)) }),
		Update("PATCH", "/shortlet/bookings/{booking_id}", "actOnShortletBooking", tagShortlet, "Approve, decline, check in/out, no-show or cancel (If-Match)", false,
			func(c *Call, in *usecases.BookingCommand) (usecases.BookingView, error) {
				return c.S.ActOnBooking(c.Ctx, c.Subj, bid(c), c.Version, *in)
			}).Describe("Approval and check-in need a verified primary guest when the listing requires it. An owner cancellation refunds everything paid; "+
			"check-out schedules a cleaning turnover job.").Errs(409, 422),
		Command("POST", "/shortlet/bookings/{booking_id}/turnover", "scheduleTurnover", tagShortlet, "Schedule a turnover job", http.StatusCreated,
			func(c *Call, in *usecases.TurnoverInput) (usecases.TurnoverView, error) {
				return c.S.ScheduleTurnover(c.Ctx, c.Subj, bid(c), *in)
			}).Capture("turnover_job_id", "data.id").Capture("turnover_version", "data.version").Errs(409),
		Update("PATCH", "/shortlet/turnover/{job_id}", "updateTurnoverJob", tagShortlet, "Progress a turnover job (If-Match)", false,
			func(c *Call, in *usecases.TurnoverUpdate) (usecases.TurnoverView, error) {
				ev, err := c.S.CheckMediaEvidence(c.Ctx, c.Subj, usecases.ScopeTurnover, c.ID("job_id"), authz.Do(authz.CapShortletManage), in.MediaRefs(), []string{"IMAGE"}...)
				if err != nil {
					return usecases.TurnoverView{}, err
				}
				return c.S.UpdateTurnover(c.Ctx, c.Subj, c.ID("job_id"), c.Version, *in, ev)
			}).Describe("Completion needs photo evidence.").Errs(409, 422),
		List("/shortlet/bookings/{booking_id}/damage-claims", "listDamageClaims", tagShortlet, "Damage claims of a booking",
			func(c *Call, _ Page) (pagination.Response[usecases.DamageClaimView], error) {
				return slice(c.S.ListDamageClaims(c.Ctx, c.Subj, bid(c)))
			}),
		CommandPre("POST", "/shortlet/bookings/{booking_id}/damage-claims", "raiseDamageClaim", tagShortlet, "Raise a damage claim", http.StatusCreated,
			ownerEvidence[usecases.DamageClaimInput](usecases.ScopeBooking, "booking_id", authz.Do(authz.CapShortletBookings), []string{"IMAGE"}),
			func(c *Call, in *usecases.DamageClaimInput, ev usecases.Evidence) (usecases.DamageClaimView, error) {
				return c.S.RaiseDamageClaim(c.Ctx, c.Subj, bid(c), *in, ev)
			}).Describe("Itemized with photo evidence, within the listing's claim window after check-out. The guest has 72 hours to accept or dispute; "+
			"no answer accepts the claim. Settlement follows the booking's payment collection.").Capture("damage_claim_id", "data.id").Errs(409, 422),
		CommandPre("POST", "/shortlet/damage-claims/{claim_id}/action", "actOnDamageClaim", tagShortlet, "Withdraw a claim or record an off-platform settlement", http.StatusOK,
			ownerEvidence[usecases.DamageClaimAction](usecases.ScopeDamageClaim, "claim_id", authz.Do(authz.CapShortletBookings), evidenceClasses),
			func(c *Call, in *usecases.DamageClaimAction, ev usecases.Evidence) (usecases.DamageClaimView, error) {
				return c.S.ActOnDamageClaim(c.Ctx, c.Subj, c.ID("claim_id"), *in, ev)
			}).Describe("Platform settlements are confirmed only by payment-service.").Errs(409, 422),
	}
}

func shortletInternalRoutes() []Route {
	bid := func(c *Call) ids.ID { return c.ID("booking_id") }
	return []Route{
		Get("/listings/{listing_id}/shortlet/customer-calendar", "customerShortletCalendar", tagInternal, "Public date inventory of a short-let listing (marketplace)",
			func(c *Call) ([]usecases.CalendarDay, error) {
				from, to, err := dateRange(c)
				if err != nil {
					return nil, err
				}
				return c.S.CustomerShortletCalendar(c.Ctx, c.Subj, c.ID("listing_id"), from, to)
			}).Internal().Q(rangeParams...),
		Command("POST", "/customer-shortlet-bookings", "customerRequestBooking", tagInternal, "Guest requests a stay", http.StatusCreated,
			func(c *Call, in *usecases.CustomerBookingInput) (usecases.BookingView, error) {
				return c.S.CustomerRequestBooking(c.Ctx, c.Subj, *in)
			}).Internal().Describe("Holds the dates (409 DATES_UNAVAILABLE when taken). Instant-book listings go to payment (or confirmation when paid off-platform); "+
			"otherwise the owner has 24 hours to approve.").Capture("booking_id", "data.id").Capture("booking_version", "data.version").Errs(409, 422),
		List("/customer-shortlet-bookings", "customerListBookings", tagInternal, "The guest's bookings",
			func(c *Call, _ Page) (pagination.Response[usecases.BookingView], error) {
				return slice(c.S.CustomerListBookings(c.Ctx, c.Subj))
			}).Internal(),
		Get("/customer-shortlet-bookings/{booking_id}", "customerGetBooking", tagInternal, "Booking as seen by the guest",
			func(c *Call) (usecases.BookingView, error) { return c.S.CustomerGetBooking(c.Ctx, c.Subj, bid(c)) }).Internal(),
		Command("POST", "/customer-shortlet-bookings/{booking_id}/action", "customerActOnBooking", tagInternal, "Guest cancels or updates guest verification", http.StatusOK,
			func(c *Call, in *usecases.CustomerBookingAction) (usecases.BookingView, error) {
				return c.S.CustomerActOnBooking(c.Ctx, c.Subj, bid(c), *in)
			}).Internal().Describe("Cancellation refunds per the cancellation policy snapshot; verification status is the customer domain's attestation.").Errs(409),
		CommandPre("POST", "/damage-claims/{claim_id}/customer-response", "customerRespondDamageClaim", tagInternal, "Guest accepts or disputes a damage claim", http.StatusOK,
			customerEvidence[usecases.CustomerDamageResponse](evidenceClasses),
			func(c *Call, in *usecases.CustomerDamageResponse, ev usecases.Evidence) (usecases.DamageClaimView, error) {
				return c.S.CustomerRespondDamageClaim(c.Ctx, c.Subj, c.ID("claim_id"), *in, ev)
			}).Internal().Errs(409),
		Command("POST", "/admin/damage-claims/{claim_id}/resolution", "adminResolveDamageClaim", tagAdmin, "Decide a disputed damage claim", http.StatusOK,
			func(c *Call, in *usecases.DamageClaimResolution) (usecases.DamageClaimView, error) {
				return c.S.AdminResolveDamageClaim(c.Ctx, c.Subj, c.ID("claim_id"), *in)
			}).Internal().Describe("The award (≤ claim) is then settled through payment-service or off-platform evidence.").Errs(409, 422),
	}
}

// shortletPropertyRoutes are the Section 8 property-scoped short-let paths.
// They resolve the property's short-let listing (listing_id selects one when
// the property has several) and call the same use cases as the
// listing-scoped routes.
func shortletPropertyRoutes() []Route {
	pp := "/properties/{property_id}/shortlet"
	resolve := func(c *Call) (ids.ID, error) {
		lid, err := c.QueryID("listing_id")
		if err != nil {
			return ids.Nil, err
		}
		return c.S.ShortletListingForProperty(c.Ctx, c.Subj, c.ID("property_id"), lid)
	}
	sel := Param{Name: "listing_id", Doc: "Required when the property has more than one short-let listing (409 AMBIGUOUS_SHORTLET_LISTING otherwise)"}
	alias := func(canonical string) string {
		return "Specification path (Section 8) for " + canonical + " on the property's short-let listing; identical behaviour."
	}
	return []Route{
		Get(pp+"/settings", "getPropertyShortletSettings", tagShortlet, "Short-let settings (property path)",
			func(c *Call) (usecases.ShortletSettingsView, error) {
				lid, err := resolve(c)
				if err != nil {
					return usecases.ShortletSettingsView{}, err
				}
				return c.S.GetShortletSettings(c.Ctx, c.Subj, lid)
			}).Q(sel).Describe(alias("GET /listings/{listing_id}/shortlet/settings")).Errs(409),
		Update("PUT", pp+"/settings", "putPropertyShortletSettings", tagShortlet, "Configure short-let (property path; If-Match, \"0\" to create)", true,
			func(c *Call, in *usecases.ShortletSettingsInput) (usecases.ShortletSettingsView, error) {
				lid, err := resolve(c)
				if err != nil {
					return usecases.ShortletSettingsView{}, err
				}
				return c.S.PutShortletSettings(c.Ctx, c.Subj, lid, c.Version, *in)
			}).Q(sel).Describe(alias("PUT /listings/{listing_id}/shortlet/settings")).Errs(409, 422, 503),
		List(pp+"/rates", "getPropertyShortletRates", tagShortlet, "Active date rates (property path)",
			func(c *Call, _ Page) (pagination.Response[usecases.RateView], error) {
				lid, err := resolve(c)
				if err != nil {
					return pagination.Response[usecases.RateView]{}, err
				}
				return slice(c.S.GetRates(c.Ctx, c.Subj, lid))
			}).Q(sel).Describe(alias("GET /listings/{listing_id}/shortlet/rates")).Errs(409),
		Command("PUT", pp+"/rates", "putPropertyShortletRates", tagShortlet, "Replace the date rates (property path)", http.StatusOK,
			func(c *Call, in *usecases.RatesInput) ([]usecases.RateView, error) {
				lid, err := resolve(c)
				if err != nil {
					return nil, err
				}
				return c.S.PutRates(c.Ctx, c.Subj, lid, *in)
			}).Q(sel).Describe(alias("PUT /listings/{listing_id}/shortlet/rates")).Errs(409, 422),
		List(pp+"/calendar", "getPropertyShortletCalendar", tagShortlet, "Date inventory (property path)",
			func(c *Call, _ Page) (pagination.Response[usecases.CalendarDay], error) {
				lid, err := resolve(c)
				if err != nil {
					return pagination.Response[usecases.CalendarDay]{}, err
				}
				from, to, err := dateRange(c)
				if err != nil {
					return pagination.Response[usecases.CalendarDay]{}, err
				}
				return slice(c.S.GetCalendar(c.Ctx, c.Subj, lid, from, to))
			}).Q(append([]Param{sel}, rangeParams...)...).Describe(alias("GET /listings/{listing_id}/shortlet/calendar")).Errs(409),
		Command("PUT", pp+"/calendar", "putPropertyShortletCalendar", tagShortlet, "Block or free days (property path)", http.StatusOK,
			func(c *Call, in *usecases.CalendarInput) ([]usecases.CalendarDay, error) {
				lid, err := resolve(c)
				if err != nil {
					return nil, err
				}
				return c.S.PutCalendar(c.Ctx, c.Subj, lid, *in)
			}).Q(sel).Describe(alias("PUT /listings/{listing_id}/shortlet/calendar")).Errs(409),
	}
}
