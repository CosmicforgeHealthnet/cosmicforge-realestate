package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func envelope(t *testing.T, producer platform.Service, typ string, payload any) events.Envelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return events.Envelope{EventID: ids.New(), EventType: typ, Producer: string(producer), AggregateType: "test", AggregateID: ids.New(),
		OccurredAt: time.Now().UTC(), Payload: raw}
}

func (f *fakePayment) refOf(reference ids.ID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ref, r := range f.requests {
		if r.ReferenceID == reference {
			return ref
		}
	}
	return ""
}

func TestEventConsumersApplyOnceThroughUseCases(t *testing.T) {
	h := newHarness(t, withPayment)
	svc, ctx := h.app.Service, context.Background()

	// payment.intent.updated.v1 confirms a booking (exactly once).
	o, l := h.shortletListing("30 Event Street")
	settings := shortletSettings("PLATFORM", true, false)
	settings["cancellation_policy"] = "FLEXIBLE"
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, settings), 200, "")
	c := customer()
	b := h.requestBooking(c, bookingBody(l, dayN(h.clock.now(), 5), dayN(h.clock.now(), 6), "Ife"))
	h.runJobs("shortlet-bookings-calendar-and-claims")
	ref := h.pay.refOf(b.ID)
	paid := envelope(t, platform.PaymentService, "payment.intent.updated.v1", map[string]any{"intent_ref": ref, "status": payment.IntentHeld,
		"amount_minor": b.TotalMinor, "paid_minor": b.TotalMinor, "updated_at": time.Now()})
	if err := svc.ApplyPaymentEvent(ctx, envelope(t, platform.MarketplaceService, "payment.intent.updated.v1", map[string]any{"intent_ref": ref})); !events.IsPermanent(err) {
		t.Fatalf("foreign producer accepted: %v", err)
	}
	unknown := envelope(t, platform.PaymentService, "payment.intent.updated.v1", map[string]any{"intent_ref": "pi_unknown", "status": "SUCCEEDED", "paid_minor": 1, "updated_at": time.Now()})
	if err := svc.ApplyPaymentEvent(ctx, unknown); err == nil || events.IsPermanent(err) {
		t.Fatalf("unknown intent must be retried: %v", err)
	}
	for range 2 {
		if err := svc.ApplyPaymentEvent(ctx, paid); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.booking(o, b.ID); got.Status != "CONFIRMED" || got.PaidMinor != b.TotalMinor {
		t.Fatalf("payment event %+v", got)
	}
	if n := h.count(`SELECT count(*) FROM processed_events WHERE consumer = $1`, usecases.ConsumerPayment); n != 1 {
		t.Fatalf("processed %d", n)
	}

	// payment.refund.updated.v1 completes the refund once.
	h.expect(h.asCustomer(c, "POST", "/customer-shortlet-bookings/"+b.ID.String()+"/action", map[string]any{"action": "CANCEL"}), 200, "")
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	var refundRef string
	if err := h.db.Pool.QueryRow(ctx, `SELECT refund_request_ref FROM shortlet_bookings WHERE id = $1`, b.ID).Scan(&refundRef); err != nil {
		t.Fatal(err)
	}
	done := map[string]any{"refund_ref": refundRef, "intent_ref": ref, "status": payment.RefundCompleted, "refunded_minor": b.TotalMinor}
	for range 2 {
		if err := svc.ApplyPaymentEvent(ctx, envelope(t, platform.PaymentService, "payment.refund.updated.v1", done)); err != nil {
			t.Fatal(err)
		}
	}
	h.relogin(o)
	if got := h.booking(o, b.ID); got.RefundedMinor != b.TotalMinor || *got.RefundStatus != payment.RefundCompleted {
		t.Fatalf("refund event %+v", got)
	}

	// marketplace.listing.reported.v1 routes a stale report to the listing.
	rep := envelope(t, platform.MarketplaceService, "marketplace.listing.reported.v1", map[string]any{"listing_id": l.ID, "report_type": "STALE", "report_ref": "mk-1"})
	if err := svc.ApplyMarketplaceEvent(ctx, rep); err != nil {
		t.Fatal(err)
	}
	if h.count(`SELECT count(*) FROM listing_publication WHERE listing_id=$1 AND action='REPORTED'`, l.ID) != 1 {
		t.Fatal("report not recorded")
	}
	bad := envelope(t, platform.MarketplaceService, "marketplace.listing.reported.v1", map[string]any{"listing_id": l.ID, "report_type": "SPAM"})
	if err := svc.ApplyMarketplaceEvent(ctx, bad); !events.IsPermanent(err) {
		t.Fatalf("invalid report must be terminal: %v", err)
	}

	// manager.* responses and activity go through the mandate rules.
	mgrID := ids.New()
	r := h.post(o, "/manager-proposals", map[string]any{"property_id": l.PropertyID, "manager_domain": "property-manager", "manager_ref_id": mgrID,
		"manager_display_name": "Event Managers", "role_type": "LETTING_ONLY", "fee_type": "NONE", "capabilities": []string{"property.view"}})
	h.expect(r, 201, "")
	var pr usecases.ProposalView
	r.data(t, &pr)
	now := h.clock.now()
	r = h.post(o, "/manager-assignments", map[string]any{"proposal_id": pr.ID, "starts_at": now, "ends_at": now.Add(30 * 24 * time.Hour), "currency": "NGN"})
	h.expect(r, 201, "")
	var as usecases.AssignmentView
	r.data(t, &as)
	if err := svc.ApplyManagerEvent(ctx, envelope(t, platform.PropertyManagerService, "manager.mandate.responded.v1",
		map[string]any{"assignment_id": as.ID, "manager_ref_id": ids.New(), "decision": "ACCEPT"})); err == nil || events.IsPermanent(err) {
		t.Fatalf("another manager's response must not apply: %v", err)
	}
	if err := svc.ApplyManagerEvent(ctx, envelope(t, platform.PropertyManagerService, "manager.mandate.responded.v1",
		map[string]any{"assignment_id": as.ID, "manager_ref_id": mgrID, "decision": "ACCEPT"})); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyManagerEvent(ctx, envelope(t, platform.PropertyManagerService, "manager.activity.recorded.v1",
		map[string]any{"assignment_id": as.ID, "manager_ref_id": mgrID, "activity_type": "INSPECTION_COMPLETED", "summary": "Quarterly inspection"})); err != nil {
		t.Fatal(err)
	}
	if h.count(`SELECT count(*) FROM manager_activity_refs WHERE assignment_id=$1 AND activity_type='INSPECTION_COMPLETED'`, as.ID) != 1 {
		t.Fatal("manager activity not recorded")
	}

	// artisan.* quotes and job records; professional.* updates.
	r = h.post(o, "/maintenance", map[string]any{"property_id": l.PropertyID, "title": "Fix tap", "description": "Dripping.", "category": "PLUMBING", "priority": "LOW"})
	h.expect(r, 201, "")
	var m usecases.MaintenanceView
	r.data(t, &m)
	h.expect(h.write(o, "PATCH", "/maintenance/"+m.ID.String(), m.Version, map[string]any{"priority": "LOW", "responsibility": "OWNER"}), 200, "")
	h.expect(h.post(o, "/maintenance/"+m.ID.String()+"/request-quotes", map[string]any{"sourcing": "PLATFORM"}), 200, "")
	art := artisan()
	q := envelope(t, platform.ArtisanService, "artisan.quote.submitted.v1", map[string]any{"request_id": m.ID, "provider_ref": art.ActorID,
		"external_quote_ref": "evq-1", "provider_display_name": "Tap Pros", "scope": "New washer", "amount_minor": 50000, "currency": "NGN"})
	if err := svc.ApplyArtisanEvent(ctx, q); err != nil {
		t.Fatal(err)
	}
	m = h.maintenance(o, m.ID)
	if m.Status != "APPROVAL_PENDING" || len(m.Quotes) != 1 {
		t.Fatalf("quote event %+v", m)
	}
	h.expect(h.post(o, "/maintenance/"+m.ID.String()+"/approve", map[string]any{"decision": "APPROVE", "quote_id": m.Quotes[0].ID, "payment_collection": "OFF_PLATFORM"}), 200, "")
	m = h.maintenance(o, m.ID)
	job := envelope(t, platform.ArtisanService, "artisan.job.event_recorded.v1", map[string]any{"job_id": m.Job.ID, "provider_ref": art.ActorID,
		"event_id": "ev-1", "event_type": "COMPLETION_SUBMITTED", "evidence_media_ids": []ids.ID{h.providerMedia(art, "IMAGE")}})
	if err := svc.ApplyArtisanEvent(ctx, job); err != nil {
		t.Fatal(err)
	}
	if got := h.maintenance(o, m.ID); got.Status != "COMPLETION_REVIEW" {
		t.Fatalf("job event %s", got.Status)
	}
	noEvidence := envelope(t, platform.ArtisanService, "artisan.job.event_recorded.v1", map[string]any{"job_id": m.Job.ID, "provider_ref": art.ActorID,
		"event_id": "ev-2", "event_type": "COMPLETION_SUBMITTED"})
	if err := svc.ApplyArtisanEvent(ctx, noEvidence); !events.IsPermanent(err) {
		t.Fatalf("completion without evidence must be terminal: %v", err)
	}

	pro := professional()
	r = h.post(o, "/professional-appointments", map[string]any{"property_id": l.PropertyID, "purpose": "VALUATION", "scope": "Market value",
		"professional_ref": pro.ActorID, "professional_label": "Valuers"})
	h.expect(r, 201, "")
	var a usecases.AppointmentView
	r.data(t, &a)
	if err := svc.ApplyProfessionalEvent(ctx, envelope(t, platform.ProfessionalServicesService, "professional.engagement.updated.v1",
		map[string]any{"appointment_id": a.ID, "professional_ref": pro.ActorID, "type": "QUOTE", "quote_amount_minor": 1000000, "licence_ref": "ESV-9"})); err != nil {
		t.Fatal(err)
	}
	if got := h.appointment(o, a.ID); got.Status != "QUOTING" || *got.LicenceRef != "ESV-9" {
		t.Fatalf("professional event %+v", got)
	}
}
