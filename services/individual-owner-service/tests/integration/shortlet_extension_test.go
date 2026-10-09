package integration

import (
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func (h *harness) extensionReq(r result) usecases.ExtensionView {
	h.t.Helper()
	var e usecases.ExtensionView
	r.data(h.t, &e)
	return e
}

func (h *harness) checkIn(o *owner, id ids.ID) {
	h.t.Helper()
	b := h.booking(o, id)
	h.expect(h.write(o, "PATCH", "/shortlet/bookings/"+id.String(), b.Version, map[string]any{"action": "CHECK_IN"}), 200, "")
}

// TestShortletStayExtensionOffPlatform: extensions price only the added
// nights with an immutable snapshot, claim the calendar under the
// per-listing lock, never overlap another booking, give their days back when
// declined, expired or cancelled at check-out, and move the check-out only
// when accepted. Confirmation schedules the pre-arrival inspection.
func TestShortletStayExtensionOffPlatform(t *testing.T) {
	h := newHarness(t)
	o, l := h.shortletListing("4 Extension Row")
	lp := "/listings/" + l.ID.String() + "/shortlet"
	h.expect(h.write(o, "PUT", lp+"/settings", 0, shortletSettings("OFF_PLATFORM", true, false)), 200, "")
	now := h.clock.now()
	guest, other := customer(), customer()
	b := h.requestBooking(guest, bookingBody(l, dayN(now, 2), dayN(now, 4), "Tobi"))
	if b.Status != "CONFIRMED" {
		t.Fatalf("instant off-platform booking %s", b.Status)
	}
	h.requestBooking(other, bookingBody(l, dayN(now, 8), dayN(now, 10), "Other"))

	// Pre-arrival inspection: 14:00 Africa/Lagos (13:00 UTC) less three hours.
	var jobs []usecases.TurnoverView
	h.get(o, lp+"/turnover?booking_id="+b.ID.String()).data(t, &jobs)
	want, _ := time.Parse(time.DateOnly, dayN(now, 2))
	if len(jobs) != 1 || jobs[0].JobType != "INSPECTION" || !jobs[0].ScheduledFor.Equal(want.Add(10*time.Hour)) || len(jobs[0].Checklist) == 0 {
		t.Fatalf("pre-arrival inspection %+v", jobs)
	}

	ext := "/customer-shortlet-bookings/" + b.ID.String() + "/extensions"
	h.expect(h.asCustomer(guest, "POST", ext, map[string]any{"new_check_out": dayN(now, 5)}), 422, "EXTENSION_NOT_ALLOWED")
	h.clock.advance(2 * 24 * time.Hour)
	h.relogin(o)
	h.checkIn(o, b.ID)

	h.expect(h.asCustomer(other, "POST", ext, map[string]any{"new_check_out": dayN(now, 5)}), 404, "")
	h.expect(h.asCustomer(guest, "POST", ext, map[string]any{"new_check_out": dayN(now, 4)}), 400, "")
	// Nights 4–8 would run into the other booking.
	h.expect(h.asCustomer(guest, "POST", ext, map[string]any{"new_check_out": dayN(now, 9)}), 409, "DATES_UNAVAILABLE")
	days := h.calendarDays(o, l, dayN(now, 4), dayN(now, 8))
	if days[dayN(now, 4)].Status != "BLOCKED" || days[dayN(now, 5)].Status != "AVAILABLE" || days[dayN(now, 8)].Status != "BOOKED" {
		t.Fatalf("refused extension changed the calendar %+v", days)
	}

	// Guest asks for one more night; the owner declines.
	r := h.asCustomer(guest, "POST", ext, map[string]any{"new_check_out": dayN(now, 5)})
	h.expect(r, 201, "")
	e := h.extensionReq(r)
	if e.Status != "REQUESTED" || *e.Awaiting != "OWNER" || e.AmountMinor != 5000000 || e.PreviousCheckOut != dayN(now, 4) {
		t.Fatalf("extension %+v", e)
	}
	days = h.calendarDays(o, l, dayN(now, 4), dayN(now, 5))
	if days[dayN(now, 4)].Status != "HOLD" || days[dayN(now, 5)].Status != "BLOCKED" {
		t.Fatalf("claimed days %+v", days)
	}
	h.expect(h.asCustomer(guest, "POST", ext, map[string]any{"new_check_out": dayN(now, 6)}), 409, "EXTENSION_ALREADY_OPEN")
	decide := "/shortlet/extensions/" + e.ID.String() + "/decision"
	h.expect(h.post(h.register(true), decide, map[string]any{"decision": "ACCEPT"}), 404, "")
	h.expect(h.asCustomer(guest, "POST", "/customer-shortlet-extensions/"+e.ID.String()+"/response", map[string]any{"decision": "ACCEPT"}), 409, "")
	h.expect(h.post(o, decide, map[string]any{"decision": "DECLINE"}), 400, "")
	h.expect(h.post(o, decide, map[string]any{"decision": "DECLINE", "reason_code": "MAINTENANCE_PLANNED"}), 200, "")
	h.expect(h.post(o, decide, map[string]any{"decision": "ACCEPT"}), 409, "")
	days = h.calendarDays(o, l, dayN(now, 4), dayN(now, 5))
	if days[dayN(now, 4)].Status != "BLOCKED" || days[dayN(now, 5)].Status != "AVAILABLE" {
		t.Fatalf("declined extension kept days %+v", days)
	}

	// The owner proposes; the guest accepts; the check-out moves.
	r = h.post(o, "/shortlet/bookings/"+b.ID.String()+"/extensions", map[string]any{"new_check_out": dayN(now, 5)})
	h.expect(r, 201, "")
	e = h.extensionReq(r)
	if *e.Awaiting != "GUEST" {
		t.Fatalf("owner proposal %+v", e)
	}
	h.expect(h.post(o, "/shortlet/extensions/"+e.ID.String()+"/decision", map[string]any{"decision": "ACCEPT"}), 409, "")
	h.expect(h.asCustomer(guest, "POST", "/customer-shortlet-extensions/"+e.ID.String()+"/response", map[string]any{"decision": "ACCEPT"}), 200, "")
	nb := h.booking(o, b.ID)
	if nb.CheckOut != dayN(now, 5) || nb.TotalMinor != b.TotalMinor || nb.ExtensionsMinor != 5000000 || len(nb.Extensions) != 2 ||
		nb.Extensions[1].Status != "APPLIED" || string(nb.Cost) != string(b.Cost) {
		t.Fatalf("extended booking %+v", nb)
	}
	days = h.calendarDays(o, l, dayN(now, 4), dayN(now, 5))
	if days[dayN(now, 4)].Status != "BOOKED" || days[dayN(now, 5)].Status != "BLOCKED" {
		t.Fatalf("applied days %+v", days)
	}
	h.expect(h.asCustomer(customer(), "POST", "/customer-shortlet-bookings", bookingBody(l, dayN(now, 4), dayN(now, 5), "Late")), 409, "DATES_UNAVAILABLE")

	// An unanswered request expires and gives the days back.
	r = h.asCustomer(guest, "POST", ext, map[string]any{"new_check_out": dayN(now, 6)})
	h.expect(r, 201, "")
	e = h.extensionReq(r)
	h.clock.advance(25 * time.Hour)
	h.relogin(o)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	var list []usecases.ExtensionView
	h.asCustomer(guest, "GET", ext, nil).data(t, &list)
	if list[len(list)-1].Status != "EXPIRED" {
		t.Fatalf("expiry %+v", list[len(list)-1])
	}
	days = h.calendarDays(o, l, dayN(now, 5), dayN(now, 6))
	if days[dayN(now, 5)].Status != "BLOCKED" || days[dayN(now, 6)].Status != "AVAILABLE" {
		t.Fatalf("expired extension kept days %+v", days)
	}

	// Check-out cancels an open extension.
	h.expect(h.asCustomer(guest, "POST", ext, map[string]any{"new_check_out": dayN(now, 6)}), 201, "")
	h.relogin(o)
	nb = h.booking(o, b.ID)
	h.expect(h.write(o, "PATCH", "/shortlet/bookings/"+b.ID.String(), nb.Version, map[string]any{"action": "CHECK_OUT"}), 200, "")
	h.asCustomer(guest, "GET", ext, nil).data(t, &list)
	if list[len(list)-1].Status != "CANCELLED" || *list[len(list)-1].DeclineReason != "GUEST_CHECKED_OUT" {
		t.Fatalf("check-out %+v", list[len(list)-1])
	}
	if h.count(`SELECT count(*) FROM availability_calendar WHERE ref_id IN (SELECT id FROM shortlet_booking_extensions WHERE booking_id=$1)`, b.ID) != 0 {
		t.Fatal("extension days left behind")
	}
	if h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action LIKE 'shortlet.extension.%'`, b.ID) < 7 {
		t.Fatal("extensions not audited")
	}
	h.validateEvents(b.ID)
}

// TestShortletStayExtensionPlatformPayment: on-platform, an accepted
// extension waits for payment-service; it is applied only when the added
// amount is reported paid.
func TestShortletStayExtensionPlatformPayment(t *testing.T) {
	h := newHarness(t, withPayment)
	o, l := h.shortletListing("5 Extension Row")
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("PLATFORM", true, false)), 200, "")
	now := h.clock.now()
	guest := customer()
	b := h.requestBooking(guest, bookingBody(l, dayN(now, 1), dayN(now, 3), "Ife"))
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.pay.settle(b.ID, b.TotalMinor, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	if h.booking(o, b.ID).Status != "CONFIRMED" {
		t.Fatal("booking not confirmed by payment")
	}
	h.clock.advance(24 * time.Hour)
	h.relogin(o)
	h.checkIn(o, b.ID)

	r := h.asCustomer(guest, "POST", "/customer-shortlet-bookings/"+b.ID.String()+"/extensions", map[string]any{"new_check_out": dayN(now, 4)})
	h.expect(r, 201, "")
	e := h.extensionReq(r)
	r = h.post(o, "/shortlet/extensions/"+e.ID.String()+"/decision", map[string]any{"decision": "ACCEPT"})
	h.expect(r, 200, "")
	if e = h.extensionReq(r); e.Status != "PAYMENT_PENDING" || h.booking(o, b.ID).CheckOut != dayN(now, 3) {
		t.Fatalf("accepted platform extension %+v", e)
	}
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	if h.booking(o, b.ID).CheckOut != dayN(now, 3) {
		t.Fatal("extension applied without payment")
	}
	if ref := h.pay.settle(e.ID, e.AmountMinor, payment.IntentSucceeded); ref == "" {
		t.Fatal("no payment intent requested for the extension")
	}
	h.clock.advance(time.Minute)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	nb := h.booking(o, b.ID)
	if nb.CheckOut != dayN(now, 4) || nb.Extensions[0].Status != "APPLIED" || nb.Extensions[0].PaidMinor != e.AmountMinor {
		t.Fatalf("paid extension %+v", nb.Extensions)
	}
}

// TestShortletExtensionRacesNewBooking: an extension and a new booking race
// for the same night; exactly one wins and the loser gets a clean 409.
func TestShortletExtensionRacesNewBooking(t *testing.T) {
	h := newHarness(t)
	o, l := h.shortletListing("6 Extension Row")
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("OFF_PLATFORM", true, false)), 200, "")
	now := h.clock.now()
	guest := customer()
	b := h.requestBooking(guest, bookingBody(l, dayN(now, 1), dayN(now, 3), "Sade"))
	h.clock.advance(24 * time.Hour)
	h.relogin(o)
	h.checkIn(o, b.ID)
	for round := range 5 {
		in, out := dayN(now, 3+round*3), dayN(now, 4+round*3)
		var wg sync.WaitGroup
		res := make([]result, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			res[0] = h.asCustomer(guest, "POST", "/customer-shortlet-bookings/"+b.ID.String()+"/extensions", map[string]any{"new_check_out": out})
		}()
		go func() {
			defer wg.Done()
			res[1] = h.asCustomer(customer(), "POST", "/customer-shortlet-bookings", bookingBody(l, in, out, "Rival"))
		}()
		wg.Wait()
		wins := 0
		for _, r := range res {
			switch {
			case r.status == 201:
				wins++
			case r.status == 409 && (r.code() == "DATES_UNAVAILABLE" || r.code() == "EXTENSION_NOT_ALLOWED"):
			default:
				t.Fatalf("round %d: %d %s", round, r.status, r.body)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d winners", round, wins)
		}
		if res[0].status == 201 {
			// Decline so the next round starts from the stay's own check-out.
			e := h.extensionReq(res[0])
			h.expect(h.post(o, "/shortlet/extensions/"+e.ID.String()+"/decision", map[string]any{"decision": "DECLINE", "reason_code": "TEST"}), 200, "")
		}
		if h.count(`SELECT count(*) FROM availability_calendar WHERE listing_id=$1 AND day=$2::date AND status IN ('HOLD','BOOKED')`, l.ID, in) > 1 {
			t.Fatal("night double-allocated")
		}
	}
}
