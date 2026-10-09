package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/calendarsync"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// fakeCalendar stands in for an external calendar feed (no provider adapter
// exists; tests only).
type fakeCalendar struct {
	mu    sync.Mutex
	busy  []calendarsync.BusyRange
	calls int
}

func (f *fakeCalendar) Fetch(context.Context, string) ([]calendarsync.BusyRange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return append([]calendarsync.BusyRange(nil), f.busy...), nil
}

func withCalendar(h *harness) { h.cal = &fakeCalendar{} }

var shortletPricing = map[string]any{"currency": "NGN", "base_amount_minor": 5000000, "base_period": "NIGHT", "payment_cycle": "PER_STAY"}

// shortletListing publishes a SHORT_LET listing (5 photos) for a verified owner.
func (h *harness) shortletListing(line1 string) (*owner, usecases.ListingView) {
	h.t.Helper()
	return h.shortletListingPriced(line1, 5000000)
}

// shortletListingPriced publishes a SHORT_LET listing at a nightly price.
func (h *harness) shortletListingPriced(line1 string, nightly int64) (*owner, usecases.ListingView) {
	h.t.Helper()
	o := h.register(true)
	h.verifyIdentity(o)
	p := h.createProperty(o, line1)
	h.verifyProperty(o, p.ID)
	media := h.photos(o, p.ID, 5)
	r := h.post(o, "/listings", map[string]any{"property_id": p.ID, "transaction_type": "SHORT_LET", "title": "Garden apartment",
		"description": "Bright, quiet and close to the beach. Fully serviced with fast internet and backup power.",
		"details":     map[string]any{"max_guests": 4, "min_nights": 1, "check_in_time": "14:00", "check_out_time": "11:00"}, "media": selection(media)})
	h.expect(r, 201, "")
	var l usecases.ListingView
	r.data(h.t, &l)
	pricing := map[string]any{}
	for k, v := range shortletPricing {
		pricing[k] = v
	}
	pricing["base_amount_minor"] = nightly
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/pricing", 0, pricing), 200, "")
	h.expect(h.post(o, "/listings/"+l.ID.String()+"/publish", nil), 200, "")
	return o, h.listing(o, l.ID)
}

// relogin refreshes the owner's tokens after a large clock jump.
func (h *harness) relogin(o *owner) {
	h.t.Helper()
	_, ar := h.login(o, o.device)
	o.apply(ar)
}

func shortletSettings(collection string, instant, verify bool) map[string]any {
	return map[string]any{"max_guests": 4, "min_nights": 1, "max_nights": 30, "check_in_time": "14:00", "check_out_time": "11:00",
		"instant_book": instant, "guest_verification_required": verify, "payment_collection": collection, "cancellation_policy": "MODERATE",
		"turnover_buffer_days": 1, "add_ons": []map[string]any{{"code": "airport_pickup", "label": "Airport pickup", "amount_minor": 1500000, "per": "STAY"}}}
}

func dayN(t time.Time, n int) string {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, n).Format(time.DateOnly)
}

func bookingBody(l usecases.ListingView, in, out string, guest string) map[string]any {
	return map[string]any{"listing_id": l.ID, "check_in": in, "check_out": out, "guests": 2,
		"guest_list": []map[string]any{{"display_name": guest, "primary": true}, {"display_name": "Companion"}}}
}

func (h *harness) booking(o *owner, id ids.ID) usecases.BookingView {
	h.t.Helper()
	r := h.get(o, "/shortlet/bookings/"+id.String())
	h.expect(r, 200, "")
	var b usecases.BookingView
	r.data(h.t, &b)
	return b
}

func (h *harness) calendarDays(o *owner, l usecases.ListingView, from, to string) map[string]usecases.CalendarDay {
	h.t.Helper()
	r := h.get(o, "/listings/"+l.ID.String()+"/shortlet/calendar?from="+from+"&to="+to)
	h.expect(r, 200, "")
	var days []usecases.CalendarDay
	r.data(h.t, &days)
	out := map[string]usecases.CalendarDay{}
	for _, d := range days {
		out[d.Day] = d
	}
	return out
}

func (h *harness) requestBooking(c *serviceauth.Delegation, body map[string]any) usecases.BookingView {
	h.t.Helper()
	r := h.asCustomer(c, "POST", "/customer-shortlet-bookings", body)
	h.expect(r, 201, "")
	var b usecases.BookingView
	r.data(h.t, &b)
	return b
}

func TestShortletBookingRaceApprovalStayAndDamageClaim(t *testing.T) {
	h := newHarness(t)
	o, l := h.shortletListing("12 Lagoon Close")
	lp := "/listings/" + l.ID.String() + "/shortlet"
	now := h.clock.now()
	c1, c2 := customer(), customer()

	// Settings gate bookings; unavailable integrations answer 503.
	h.expect(h.get(o, lp+"/settings"), 404, "SHORTLET_NOT_CONFIGURED")
	h.expect(h.asCustomer(c1, "POST", "/customer-shortlet-bookings", bookingBody(l, dayN(now, 10), dayN(now, 13), "Ada")), 409, "SHORTLET_NOT_CONFIGURED")
	h.expect(h.write(o, "PUT", lp+"/settings", 0, shortletSettings("PLATFORM", false, true)), 503, "FEATURE_UNAVAILABLE")
	withRef := shortletSettings("OFF_PLATFORM", false, true)
	withRef["external_calendar_ref"] = "https://calendar.example/feed.ics"
	h.expect(h.write(o, "PUT", lp+"/settings", 0, withRef), 503, "FEATURE_UNAVAILABLE")
	h.expect(h.write(o, "PUT", lp+"/settings", 3, shortletSettings("OFF_PLATFORM", false, true)), 409, "VERSION_CONFLICT")
	h.expect(h.write(o, "PUT", lp+"/settings", 0, shortletSettings("OFF_PLATFORM", false, true)), 200, "")
	h.expect(h.get(h.register(true), lp+"/settings"), 404, "")

	// Date rates: an override inside the stay and a minimum-stay range.
	rates := map[string]any{"rates": []map[string]any{
		{"start_date": dayN(now, 11), "end_date": dayN(now, 11), "nightly_rate_minor": 8000000, "label": "Festival"},
		{"start_date": dayN(now, 40), "end_date": dayN(now, 45), "nightly_rate_minor": 6000000, "min_nights": 3}}}
	h.expect(h.do(call{method: "PUT", path: api + lp + "/rates", token: o.access, key: key(), body: map[string]any{"rates": []map[string]any{
		{"start_date": dayN(now, 1), "end_date": dayN(now, 5), "nightly_rate_minor": 1}, {"start_date": dayN(now, 5), "end_date": dayN(now, 6), "nightly_rate_minor": 1}}}}), 400, "")
	h.expect(h.do(call{method: "PUT", path: api + lp + "/rates", token: o.access, key: key(), body: rates}), 200, "")
	h.expect(h.asCustomer(c1, "POST", "/customer-shortlet-bookings", bookingBody(l, dayN(now, 41), dayN(now, 42), "Ada")), 422, "STAY_BELOW_MINIMUM")

	// Owner blocks a day; a stay over it is refused.
	h.expect(h.do(call{method: "PUT", path: api + lp + "/calendar", token: o.access, key: key(),
		body: map[string]any{"ranges": []map[string]any{{"from": dayN(now, 20), "to": dayN(now, 21), "status": "BLOCKED"}}}}), 200, "")
	h.expect(h.asCustomer(c1, "POST", "/customer-shortlet-bookings", bookingBody(l, dayN(now, 19), dayN(now, 22), "Ada")), 409, "DATES_UNAVAILABLE")

	// Two guests race for the same nights: exactly one hold wins.
	var wg sync.WaitGroup
	results := make([]result, 6)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := c1
			if i%2 == 1 {
				c = c2
			}
			results[i] = h.do(call{method: "POST", path: internal + "/customer-shortlet-bookings", as: platform.MarketplaceService, delegate: c, key: key(),
				body: bookingBody(l, dayN(now, 10), dayN(now, 13), "Racer")})
		}(i)
	}
	wg.Wait()
	var won *usecases.BookingView
	for _, r := range results {
		switch r.status {
		case 201:
			if won != nil {
				t.Fatalf("two holds won: %s", r.body)
			}
			var b usecases.BookingView
			r.data(t, &b)
			won = &b
		case 409:
			if r.code() != "DATES_UNAVAILABLE" {
				t.Fatalf("race loser %s", r.body)
			}
		default:
			t.Fatalf("race %d %s", r.status, r.body)
		}
	}
	if won == nil || won.Status != "PENDING_APPROVAL" || won.HoldExpiresAt == nil {
		t.Fatalf("winner %+v", won)
	}
	guest := c1
	if won.CustomerRefID == c2.ActorID {
		guest = c2
	}
	// 2 nights at 50,000 + 1 festival night at 80,000.
	if won.TotalMinor != 2*5000000+8000000 {
		t.Fatalf("total %d", won.TotalMinor)
	}
	days := h.calendarDays(o, l, dayN(now, 10), dayN(now, 13))
	if days[dayN(now, 10)].Status != "HOLD" || days[dayN(now, 12)].Status != "HOLD" || days[dayN(now, 13)].Status != "BLOCKED" {
		t.Fatalf("calendar %+v", days)
	}
	h.expect(h.do(call{method: "PUT", path: api + lp + "/calendar", token: o.access, key: key(),
		body: map[string]any{"ranges": []map[string]any{{"from": dayN(now, 11), "to": dayN(now, 11), "status": "AVAILABLE"}}}}), 409, "CALENDAR_CONFLICT")
	var public []usecases.CalendarDay
	r := h.asCustomer(c2, "GET", "/listings/"+l.ID.String()+"/shortlet/customer-calendar?from="+dayN(now, 10)+"&to="+dayN(now, 10), nil)
	h.expect(r, 200, "")
	r.data(t, &public)
	if len(public) != 1 || public[0].Status != "HOLD" || public[0].RefID != nil {
		t.Fatalf("public calendar %+v", public)
	}

	// Approval needs a verified primary guest; then it confirms (off-platform).
	bp := "/shortlet/bookings/" + won.ID.String()
	h.expect(h.write(o, "PATCH", bp, won.Version, map[string]any{"action": "APPROVE"}), 422, "GUEST_VERIFICATION_REQUIRED")
	primary := won.GuestList[0]
	h.expect(h.asCustomer(guest, "POST", "/customer-shortlet-bookings/"+won.ID.String()+"/action",
		map[string]any{"action": "UPDATE_VERIFICATION", "guest_id": primary.ID, "verification_status": "VERIFIED", "verification_ref": "kyc-ref-9"}), 200, "")
	b := h.booking(o, won.ID)
	h.expect(h.write(o, "PATCH", bp, b.Version, map[string]any{"action": "APPROVE"}), 200, "")
	b = h.booking(o, won.ID)
	if b.Status != "CONFIRMED" {
		t.Fatalf("approved %s", b.Status)
	}
	if d := h.calendarDays(o, l, dayN(now, 10), dayN(now, 10)); d[dayN(now, 10)].Status != "BOOKED" {
		t.Fatalf("booked day %+v", d)
	}
	h.expect(h.write(o, "PATCH", bp, b.Version, map[string]any{"action": "CHECK_IN"}), 422, "TOO_EARLY")

	// Stay: check in, check out (turnover scheduled), turnover completion needs photos.
	h.clock.advance(10 * 24 * time.Hour)
	h.relogin(o)
	h.expect(h.write(o, "PATCH", bp, b.Version, map[string]any{"action": "CHECK_IN", "access_grant_ref": ids.New()}), 200, "")
	h.clock.advance(3 * 24 * time.Hour)
	h.relogin(o)
	b = h.booking(o, won.ID)
	h.expect(h.write(o, "PATCH", bp, b.Version, map[string]any{"action": "CHECK_OUT"}), 200, "")
	var jobs []usecases.TurnoverView
	r = h.get(o, lp+"/turnover?booking_id="+won.ID.String())
	h.expect(r, 200, "")
	r.data(t, &jobs)
	if len(jobs) != 1 || jobs[0].JobType != "CLEANING" || jobs[0].Status != "SCHEDULED" {
		t.Fatalf("turnover %+v", jobs)
	}
	tp := "/shortlet/turnover/" + jobs[0].ID.String()
	h.expect(h.write(o, "PATCH", tp, jobs[0].Version, map[string]any{"status": "COMPLETED"}), 422, "EVIDENCE_REQUIRED")
	photo := h.media.put(o.id, "IMAGE", "READY")
	h.expect(h.write(o, "PATCH", tp, jobs[0].Version, map[string]any{"status": "COMPLETED", "evidence_media_ids": []ids.ID{photo}}), 200, "")
	h.expect(h.post(o, bp+"/turnover", map[string]any{"job_type": "CLEANING", "scheduled_for": h.clock.now(), "assignee_type": "OWNER"}), 409, "TURNOVER_EXISTS")

	// Damage claim: guest disputes, an operator decides, the owner records
	// the off-platform settlement with evidence.
	claim := map[string]any{"items": []map[string]any{{"description": "Broken lamp", "amount_minor": 2000000, "media_ids": []ids.ID{h.media.put(o.id, "IMAGE", "READY")}}}}
	r = h.post(o, bp+"/damage-claims", claim)
	h.expect(r, 201, "")
	var dc usecases.DamageClaimView
	r.data(t, &dc)
	h.expect(h.post(o, bp+"/damage-claims", claim), 409, "CLAIM_ALREADY_OPEN")
	h.expect(h.asCustomer(customer(), "POST", "/damage-claims/"+dc.ID.String()+"/customer-response", map[string]any{"decision": "ACCEPT"}), 404, "")
	h.expect(h.asCustomer(guest, "POST", "/damage-claims/"+dc.ID.String()+"/customer-response",
		map[string]any{"decision": "DISPUTE", "note": "The lamp was already cracked", "evidence_media_ids": []ids.ID{h.customerMedia(guest, "IMAGE")}}), 200, "")
	if got := h.booking(o, won.ID); got.Status != "DISPUTED" {
		t.Fatalf("disputed booking %s", got.Status)
	}
	h.expect(h.admin("POST", "/admin/damage-claims/"+dc.ID.String()+"/resolution", map[string]any{"awarded_minor": 3000000}), 422, "AWARD_EXCEEDS_CLAIM")
	r = h.admin("POST", "/admin/damage-claims/"+dc.ID.String()+"/resolution", map[string]any{"awarded_minor": 1000000})
	h.expect(r, 200, "")
	r.data(t, &dc)
	if dc.Status != "RESOLVED" || dc.Settlement == nil || *dc.Settlement != "OFF_PLATFORM" {
		t.Fatalf("resolved %+v", dc)
	}
	ap := "/shortlet/damage-claims/" + dc.ID.String() + "/action"
	h.expect(h.post(o, ap, map[string]any{"action": "RECORD_SETTLEMENT"}), 422, "EVIDENCE_REQUIRED")
	r = h.post(o, ap, map[string]any{"action": "RECORD_SETTLEMENT", "evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}})
	h.expect(r, 200, "")
	r.data(t, &dc)
	if dc.Status != "SETTLED" || dc.SettledAt == nil {
		t.Fatalf("settled %+v", dc)
	}
	if got := h.booking(o, won.ID); got.Status != "COMPLETED" {
		t.Fatalf("completed booking %s", got.Status)
	}

	types := h.outboxTypes(won.ID)
	if types["shortlet.booking.updated.v1"] < 5 {
		t.Fatalf("booking events %v", types)
	}
	if h.outboxTypes(l.ID)["shortlet.availability.updated.v1"] < 2 {
		t.Fatalf("availability events %v", h.outboxTypes(l.ID))
	}
	h.validateEvents(won.ID)
	h.validateEvents(l.ID)
}

func TestShortletPlatformPaymentRefundsAndHoldExpiry(t *testing.T) {
	h := newHarness(t, withPayment)
	o, l := h.shortletListing("14 Lagoon Close")
	lp := "/listings/" + l.ID.String() + "/shortlet"
	now := h.clock.now()
	settings := shortletSettings("PLATFORM", true, false)
	settings["cancellation_policy"] = "FLEXIBLE"
	h.expect(h.write(o, "PUT", lp+"/settings", 0, settings), 200, "")

	// Instant book → payment → confirmation from payment-service truth.
	c1 := customer()
	b := h.requestBooking(c1, bookingBody(l, dayN(now, 5), dayN(now, 7), "Bola"))
	if b.Status != "PENDING_PAYMENT" || b.PaymentCollection != "PLATFORM" {
		t.Fatalf("instant %+v", b)
	}
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.pay.settle(b.ID, b.TotalMinor, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	if got := h.booking(o, b.ID); got.Status != "CONFIRMED" || got.PaidMinor != b.TotalMinor {
		t.Fatalf("paid booking %+v", got)
	}

	// Guest cancels more than a day out: FLEXIBLE refunds everything,
	// through payment-service.
	h.expect(h.asCustomer(c1, "POST", "/customer-shortlet-bookings/"+b.ID.String()+"/action", map[string]any{"action": "CANCEL"}), 200, "")
	got := h.booking(o, b.ID)
	if got.Status != "CANCELLED" || got.RefundDueMinor != b.TotalMinor || got.CancelledBy == nil || *got.CancelledBy != "GUEST" {
		t.Fatalf("cancelled %+v", got)
	}
	if d := h.calendarDays(o, l, dayN(now, 5), dayN(now, 7)); d[dayN(now, 5)].Status != "AVAILABLE" || d[dayN(now, 7)].Status != "AVAILABLE" {
		t.Fatalf("released %+v", d)
	}
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	if got = h.booking(o, b.ID); got.RefundStatus == nil || *got.RefundStatus != payment.RefundPending || got.RefundedMinor != 0 {
		t.Fatalf("refund requested %+v", got)
	}
	if done := h.pay.completeRefunds(); len(done) != 1 || done[0].AmountMinor != b.TotalMinor {
		t.Fatalf("refund requests %+v", done)
	}
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	if got = h.booking(o, b.ID); *got.RefundStatus != payment.RefundCompleted || got.RefundedMinor != b.TotalMinor {
		t.Fatalf("refunded %+v", got)
	}

	// The freed dates can be booked again; an unpaid hold expires and money
	// that arrives after expiry is refunded in full.
	c2 := customer()
	b2 := h.requestBooking(c2, bookingBody(l, dayN(now, 5), dayN(now, 6), "Chidi"))
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.clock.advance(31 * time.Minute)
	h.relogin(o)
	h.pay.settle(b2.ID, b2.TotalMinor, payment.IntentSucceeded)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	got = h.booking(o, b2.ID)
	if got.Status != "CANCELLED" || got.CancelReason == nil || *got.CancelReason != "HOLD_EXPIRED" || got.RefundDueMinor != b2.TotalMinor {
		t.Fatalf("expired hold %+v", got)
	}
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	if got = h.booking(o, b2.ID); got.RefundStatus == nil || *got.RefundStatus != payment.RefundPending {
		t.Fatalf("late payment refund %+v", got)
	}
	if h.count(`SELECT count(*) FROM audit_events WHERE action = 'shortlet.booking.payment_after_cancellation' AND target_id = $1`, b2.ID) != 1 {
		t.Fatal("late payment not audited")
	}

	// Owner cancellation of a confirmed stay refunds everything paid.
	c3 := customer()
	b3 := h.requestBooking(c3, bookingBody(l, dayN(now, 8), dayN(now, 9), "Dayo"))
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.pay.settle(b3.ID, b3.TotalMinor, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	got = h.booking(o, b3.ID)
	h.expect(h.write(o, "PATCH", "/shortlet/bookings/"+b3.ID.String(), got.Version, map[string]any{"action": "CANCEL"}), 400, "")
	h.expect(h.write(o, "PATCH", "/shortlet/bookings/"+b3.ID.String(), got.Version, map[string]any{"action": "CANCEL", "reason_code": "PROPERTY_UNAVAILABLE"}), 200, "")
	if got = h.booking(o, b3.ID); got.RefundDueMinor != b3.TotalMinor || *got.CancelledBy != "OWNER" {
		t.Fatalf("owner cancel %+v", got)
	}
	h.validateEvents(b.ID)
	h.validateEvents(b2.ID)
}

func TestShortletExternalCalendarSync(t *testing.T) {
	h := newHarness(t, withCalendar)
	o, l := h.shortletListing("16 Lagoon Close")
	lp := "/listings/" + l.ID.String() + "/shortlet"
	now := h.clock.now()
	settings := shortletSettings("OFF_PLATFORM", true, false)
	settings["external_calendar_ref"] = "https://calendar.example/feed.ics"
	h.expect(h.write(o, "PUT", lp+"/settings", 0, settings), 200, "")
	b := h.requestBooking(customer(), bookingBody(l, dayN(now, 3), dayN(now, 5), "Efe"))
	if b.Status != "CONFIRMED" {
		t.Fatalf("instant off-platform %s", b.Status)
	}
	start := func(n int) time.Time { t0, _ := time.Parse(time.DateOnly, dayN(now, n)); return t0 }
	h.cal.mu.Lock()
	h.cal.busy = []calendarsync.BusyRange{{Start: start(4), End: start(6), ExternalID: "x1"}, {Start: start(10), End: start(12), ExternalID: "x2"}}
	h.cal.mu.Unlock()
	h.runJobs("shortlet-bookings-calendar-and-claims")
	d := h.calendarDays(o, l, dayN(now, 3), dayN(now, 12))
	// Booked nights stay booked; free days are blocked as EXTERNAL.
	if d[dayN(now, 4)].Status != "BOOKED" || d[dayN(now, 10)].Status != "BLOCKED" || d[dayN(now, 10)].Source != "EXTERNAL" || d[dayN(now, 12)].Status != "AVAILABLE" {
		t.Fatalf("after import %+v", d)
	}
	if h.count(`SELECT count(*) FROM audit_events WHERE action = 'shortlet.calendar.external_import' AND target_id = $1 AND changes::text LIKE $2`, l.ID, "%"+dayN(now, 4)+"%") != 1 {
		t.Fatal("conflict not reported")
	}
	// The feed drops a reservation: the external block is released.
	h.cal.mu.Lock()
	h.cal.busy = h.cal.busy[:1]
	h.cal.mu.Unlock()
	h.runJobs("shortlet-bookings-calendar-and-claims")
	if d = h.calendarDays(o, l, dayN(now, 10), dayN(now, 10)); d[dayN(now, 10)].Status != "AVAILABLE" {
		t.Fatalf("after release %+v", d)
	}
	h.validateEvents(l.ID)
}
