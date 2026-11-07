package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
)

// TestShortletLagosMidnightBusinessDate pins the window (23:00–24:00 UTC)
// where the UTC civil date lags the Africa/Lagos property date. Clients must
// derive check-in dates from the property calendar; the service keeps
// rejecting dates that are past in that calendar.
func TestShortletLagosMidnightBusinessDate(t *testing.T) {
	h := newHarness(t)
	o, l := h.shortletListing("23 Lagos Midnight Road")
	var prop usecases.PropertyView
	h.get(o, "/properties/"+l.PropertyID.String()).data(t, &prop)
	if prop.Timezone != "Africa/Lagos" {
		t.Fatalf("default property timezone %q", prop.Timezone)
	}
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("OFF_PLATFORM", true, false)), 200, "")

	// One second before Lagos midnight: UTC and Lagos agree on date D.
	base := h.clock.now()
	d := time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 2)
	h.clock.advance(d.Add(22*time.Hour + 59*time.Minute + 59*time.Second).Sub(base))
	h.relogin(o)
	utcDay, lagosNext := d.Format(time.DateOnly), d.AddDate(0, 0, 1).Format(time.DateOnly)
	b := h.requestBooking(customer(), bookingBody(l, lagosNext, d.AddDate(0, 0, 2).Format(time.DateOnly), "Midnight Guest"))
	bp := "/shortlet/bookings/" + b.ID.String()
	h.expect(h.write(o, "PATCH", bp, h.booking(o, b.ID).Version, map[string]any{"action": "CHECK_IN"}), 422, "TOO_EARLY")

	// Exactly Lagos midnight (23:00 UTC): the UTC date D is now past locally.
	h.clock.advance(time.Second)
	h.relogin(o)
	if utc, local := h.clock.now().UTC().Format(time.DateOnly), h.clock.now().In(mustLagos(t)).Format(time.DateOnly); utc != utcDay || local != lagosNext {
		t.Fatalf("boundary fixture utc=%s local=%s", utc, local)
	}
	r := h.asCustomer(customer(), "POST", "/customer-shortlet-bookings", bookingBody(l, utcDay, lagosNext, "Stale UTC Guest"))
	h.expect(r, 400, "")
	if !strings.Contains(string(r.body), "check_in cannot be in the past.") {
		t.Fatalf("past local date body %s", r.body)
	}

	// The property-local arrival date stays valid across the stay journey.
	h.expect(h.write(o, "PATCH", bp, h.booking(o, b.ID).Version, map[string]any{"action": "CHECK_IN"}), 200, "")
	h.clock.advance(12 * time.Hour)
	h.relogin(o)
	h.expect(h.write(o, "PATCH", bp, h.booking(o, b.ID).Version, map[string]any{"action": "CHECK_OUT"}), 200, "")
	var jobs []usecases.TurnoverView
	r = h.get(o, "/listings/"+l.ID.String()+"/shortlet/turnover?booking_id="+b.ID.String())
	h.expect(r, 200, "")
	r.data(t, &jobs)
	cleaning := false
	for _, j := range jobs {
		cleaning = cleaning || j.JobType == "CLEANING"
	}
	if !cleaning {
		t.Fatalf("turnover after local-date stay %+v", jobs)
	}
	if got := h.booking(o, b.ID).Status; got != "CHECKED_OUT" && got != "COMPLETED" {
		t.Fatalf("stay status %s", got)
	}
}

func mustLagos(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
