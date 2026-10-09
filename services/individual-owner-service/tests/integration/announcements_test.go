package integration

import (
	"context"
	"sync"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/customernotify"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
)

// fakeRecipientDomain stands in for marketplace-service's customer
// notification contract (tests only): it suppresses recipients without
// consent and records accepted requests.
type fakeRecipientDomain struct {
	mu        sync.Mutex
	noConsent map[string]bool
	accepted  []customernotify.Request
	keys      map[string]bool
}

func (f *fakeRecipientDomain) Notify(_ context.Context, req customernotify.Request, key string) (customernotify.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys == nil {
		f.keys = map[string]bool{}
	}
	if f.noConsent[req.RecipientRef.String()] {
		return customernotify.Result{Status: customernotify.StatusSuppressed, Reason: "NO_CONSENT"}, nil
	}
	if !f.keys[key] {
		f.keys[key] = true
		f.accepted = append(f.accepted, req)
	}
	return customernotify.Result{Status: customernotify.StatusAccepted, Channels: []string{"PUSH"}, Ref: "mk-" + req.NotificationID.String()}, nil
}

func TestGuestAnnouncementsThroughRecipientDomain(t *testing.T) {
	fake := &fakeRecipientDomain{noConsent: map[string]bool{}}
	h := newHarness(t, func(h *harness) { h.customerNotify = fake })
	o, l := h.shortletListing("60 Notice Road")
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("OFF_PLATFORM", true, false)), 200, "")
	now := h.clock.now()
	g1, g2 := customer(), customer()
	h.requestBooking(g1, bookingBody(l, dayN(now, 2), dayN(now, 3), "Guest One"))
	h.requestBooking(g2, bookingBody(l, dayN(now, 5), dayN(now, 6), "Guest Two"))
	fake.noConsent[g2.ActorID.String()] = true
	ap := "/properties/" + l.PropertyID.String() + "/announcements"

	h.expect(h.post(o, ap, map[string]any{"audience": "GUESTS", "purpose": "MARKETING", "title": "Sale", "message": "Discounts"}), 400, "")
	h.expect(h.post(h.register(true), ap, map[string]any{"audience": "GUESTS", "purpose": "UTILITY_INTERRUPTION", "title": "Water", "message": "Off 9-12"}), 404, "")
	r := h.post(o, ap, map[string]any{"audience": "GUESTS", "purpose": "UTILITY_INTERRUPTION", "title": "Water outage", "message": "Water is off 9am-12pm."})
	h.expect(r, 201, "")
	var a usecases.AnnouncementView
	r.data(t, &a)
	if a.Recipients != 2 || a.Delivery["PENDING"] != 2 {
		t.Fatalf("announcement %+v", a)
	}
	h.runJobs("customer-notifications")
	h.runJobs("customer-notifications")
	var list []usecases.AnnouncementView
	h.get(o, ap).data(t, &list)
	if list[0].Delivery["ACCEPTED"] != 1 || list[0].Delivery["SUPPRESSED"] != 1 || len(fake.accepted) != 1 {
		t.Fatalf("delivery %+v accepted=%d", list[0].Delivery, len(fake.accepted))
	}
	got := fake.accepted[0]
	if got.RecipientRef != g1.ActorID || got.Purpose != "UTILITY_INTERRUPTION" || got.RelationshipType != "shortlet_booking" ||
		got.Variables["message"] != "Water is off 9am-12pm." || got.RecipientDomain != "marketplace" {
		t.Fatalf("request %+v", got)
	}
	for range 4 {
		h.expect(h.post(o, ap, map[string]any{"audience": "GUESTS", "purpose": "SERVICE_ANNOUNCEMENT", "title": "Note", "message": "Info"}), 201, "")
	}
	h.expect(h.post(o, ap, map[string]any{"audience": "GUESTS", "purpose": "SERVICE_ANNOUNCEMENT", "title": "Note", "message": "Info"}), 409, "ANNOUNCEMENT_LIMIT")
}

func TestAnnouncementsBlockedWithoutRecipientDomain(t *testing.T) {
	h := newHarness(t)
	o, l := h.shortletListing("61 Notice Road")
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("OFF_PLATFORM", true, false)), 200, "")
	b := h.requestBooking(customer(), bookingBody(l, dayN(h.clock.now(), 2), dayN(h.clock.now(), 3), "Guest"))
	ap := "/properties/" + l.PropertyID.String() + "/announcements"
	h.expect(h.post(o, ap, map[string]any{"audience": "GUESTS", "purpose": "SAFETY_NOTICE", "title": "Gas check", "message": "Engineer visiting."}), 201, "")
	h.runJobs("customer-notifications")
	var list []usecases.AnnouncementView
	h.get(o, ap).data(t, &list)
	if list[0].Delivery["BLOCKED"] != 1 {
		t.Fatalf("blocked %+v", list[0].Delivery)
	}
	// The stay ends: the request is suppressed rather than delivered later.
	h.expect(h.write(o, "PATCH", "/shortlet/bookings/"+b.ID.String(), h.booking(o, b.ID).Version, map[string]any{"action": "CANCEL", "reason_code": "OWNER_UNAVAILABLE"}), 200, "")
	h.clock.advance(61 * 60 * 1e9)
	h.runJobs("customer-notifications")
	h.relogin(o)
	h.get(o, ap).data(t, &list)
	if list[0].Delivery["SUPPRESSED"] != 1 {
		t.Fatalf("suppressed %+v", list[0].Delivery)
	}
}
