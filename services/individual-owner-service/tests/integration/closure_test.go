package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// TestStatutoryCalendarReminderAndRecurrence (§13.25): the owner's own
// tax/statutory calendar, reminded once before the due date; completing a
// recurring entry (with receipts) schedules the next occurrence once.
func TestStatutoryCalendarReminderAndRecurrence(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "8 Ledger Lane")
	other := h.register(true)
	now := h.clock.now()

	body := map[string]any{"property_id": p.ID, "obligation_type": "LAND_USE_CHARGE", "title": "Land use charge 2027", "jurisdiction": "NG",
		"authority": "Lagos State", "due_on": dayN(now, 10), "recurrence": "ANNUAL", "estimated_amount_minor": 12000000}
	h.expect(h.post(o, "/statutory-obligations", body), 400, "")
	body["currency"] = "NGN"
	h.expect(h.post(other, "/statutory-obligations", body), 404, "")
	r := h.post(o, "/statutory-obligations", body)
	h.expect(r, 201, "")
	var st usecases.StatutoryView
	r.data(t, &st)
	if st.Status != "UPCOMING" || st.ReminderDays != 14 || *st.PropertyID != p.ID {
		t.Fatalf("statutory %+v", st)
	}
	h.expect(h.post(o, "/statutory-obligations", map[string]any{"obligation_type": "INCOME_TAX_FILING", "title": "Annual return", "jurisdiction": "NG",
		"due_on": dayN(now, 200), "recurrence": "NONE"}), 201, "")

	h.runJobs("statutory-reminders-and-assistance-expiry")
	h.runJobs("statutory-reminders-and-assistance-expiry")
	if n := h.count(`SELECT count(*) FROM notification_dispatches WHERE template_key=$1 AND account_id=$2`, usecases.TemplateStatutoryDue, o.id); n != 1 {
		t.Fatalf("reminders %d", n)
	}

	cp := "/statutory-obligations/" + st.ID.String() + "/close"
	h.expect(h.post(other, cp, map[string]any{"action": "COMPLETE"}), 404, "")
	receipt := h.media.put(o.id, "DOCUMENT", "READY")
	h.expect(h.post(o, cp, map[string]any{"action": "CANCEL", "evidence_media_ids": []ids.ID{receipt}}), 400, "")
	r = h.post(o, cp, map[string]any{"action": "COMPLETE", "evidence_media_ids": []ids.ID{receipt}, "notes": "Paid at bank"})
	h.expect(r, 200, "")
	r.data(t, &st)
	if st.Status != "COMPLETED" || len(st.EvidenceMediaIDs) != 1 || st.CompletedAt == nil {
		t.Fatalf("completed %+v", st)
	}
	h.expect(h.post(o, cp, map[string]any{"action": "COMPLETE"}), 409, "")
	var list []usecases.StatutoryView
	h.get(o, "/statutory-obligations?status=UPCOMING&property_id="+p.ID.String()).data(t, &list)
	next, _ := time.Parse(time.DateOnly, dayN(now, 10))
	if len(list) != 1 || list[0].DueOn != next.AddDate(1, 0, 0).Format(time.DateOnly) || *list[0].PreviousID != st.ID {
		t.Fatalf("next occurrence %+v", list)
	}
	h.get(o, "/statutory-obligations").data(t, &list)
	if len(list) != 3 {
		t.Fatalf("calendar %d", len(list))
	}
	h.get(other, "/statutory-obligations").data(t, &list)
	if len(list) != 0 {
		t.Fatal("cross-owner leak")
	}
}

// TestValuationsYieldAndPropertyInsights (§13.25): yield uses collected
// money over a disclosed valuation basis; property insights are advisory
// with provenance and cached.
func TestValuationsYieldAndPropertyInsights(t *testing.T) {
	h := newHarness(t, withPayment)
	o, l := h.shortletListingPriced("9 Yield Street", 5000000)
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("PLATFORM", true, false)), 200, "")
	now := h.clock.now()
	b := h.requestBooking(customer(), bookingBody(l, dayN(now, 3), dayN(now, 5), "Kunle"))
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.pay.settle(b.ID, b.TotalMinor, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")

	period := "/reports/portfolio?from=" + dayN(now, 0) + "&to=" + dayN(now, 29)
	var rep usecases.PortfolioReport
	h.get(o, period).data(t, &rep)
	if rep.Yield != nil || rep.YieldBps != nil {
		t.Fatalf("yield without valuation %+v", rep.Yield)
	}
	vp := "/properties/" + l.PropertyID.String() + "/valuations"
	h.expect(h.post(o, vp, map[string]any{"value_minor": 1, "currency": "NGN", "valued_on": dayN(now, 1), "basis": "OWNER_ESTIMATE"}), 400, "")
	h.expect(h.post(o, vp, map[string]any{"value_minor": 1, "currency": "NGN", "valued_on": dayN(now, 0), "basis": "PROFESSIONAL_VALUATION"}), 400, "")
	h.expect(h.post(o, vp, map[string]any{"value_minor": 1, "currency": "NGN", "valued_on": dayN(now, 0), "basis": "PROFESSIONAL_VALUATION",
		"professional_appointment_id": ids.New()}), 422, "VALUATION_REPORT_REQUIRED")
	h.expect(h.post(h.register(true), vp, map[string]any{"value_minor": 1, "currency": "NGN", "valued_on": dayN(now, 0), "basis": "OWNER_ESTIMATE"}), 404, "")
	h.expect(h.post(o, vp, map[string]any{"value_minor": 9000000000, "currency": "NGN", "valued_on": dayN(now, -400), "basis": "PURCHASE_PRICE"}), 201, "")
	h.expect(h.post(o, vp, map[string]any{"value_minor": 10000000000, "currency": "NGN", "valued_on": dayN(now, 0), "basis": "OWNER_ESTIMATE",
		"evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 201, "")
	var vals []usecases.ValuationView
	h.get(o, vp).data(t, &vals)
	if len(vals) != 2 || vals[0].Basis != "OWNER_ESTIMATE" {
		t.Fatalf("valuations %+v", vals)
	}
	if _, err := h.app.Pool.Exec(context.Background(), `UPDATE property_valuations SET value_minor = 1`); err == nil {
		t.Fatal("valuations are not append-only")
	}
	h.get(o, period).data(t, &rep)
	annual := b.TotalMinor * 365 / 30
	if rep.Yield == nil || rep.Yield.ValuedProperties != 1 || rep.Yield.Valuation["NGN"] != 10000000000 || rep.Yield.AnnualizedIncome["NGN"] != annual ||
		rep.YieldBps == nil || *rep.YieldBps != annual*10000/10000000000 || rep.Yield.Bases[0] != "OWNER_ESTIMATE" {
		t.Fatalf("yield %+v %v", rep.Yield, rep.YieldBps)
	}

	// Property insights: advisory, with provenance, cached for a day.
	ip := "/properties/" + l.PropertyID.String() + "/insights"
	var ins []usecases.InsightView
	h.get(o, ip).data(t, &ins)
	if len(ins) != 3 {
		t.Fatalf("insights %+v", ins)
	}
	kinds := map[string]usecases.InsightView{}
	for _, i := range ins {
		kinds[i.InsightType] = i
		if !i.Advisory || i.ModelName != "cf-portfolio-rules" || i.ModelVersion == "" || i.Confidence <= 0 || !i.ExpiresAt.After(i.GeneratedAt) {
			t.Fatalf("provenance %+v", i)
		}
		var in map[string]any
		_ = json.Unmarshal(i.Inputs, &in)
		if in["method"] == nil {
			t.Fatalf("inputs %s", i.Inputs)
		}
	}
	var vac map[string]any
	_ = json.Unmarshal(kinds["VACANCY_RISK"].Result, &vac)
	if vac["risk_level"] != "HIGH" {
		t.Fatalf("vacancy %s", kinds["VACANCY_RISK"].Result)
	}
	if kinds["RENEWAL_OUTLOOK"].InsightType == "" || kinds["MAINTENANCE_PATTERN"].InsightType == "" {
		t.Fatalf("kinds %v", kinds)
	}
	h.get(o, ip).data(t, &ins)
	if h.count(`SELECT count(*) FROM market_insights WHERE property_id=$1`, l.PropertyID) != 3 {
		t.Fatal("insights not cached")
	}
	h.expect(h.get(h.register(true), ip), 404, "")
}

// TestAssistedUseIsAuthorizedAttributedAndRevocable (§13.27).
func TestAssistedUseIsAuthorizedAttributedAndRevocable(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p1 := h.createProperty(o, "10 Concierge Road")
	p2 := h.createProperty(o, "11 Concierge Road")
	stranger := h.register(true)
	sp := h.createProperty(stranger, "12 Concierge Road")
	staff := &serviceauth.Delegation{ActorDomain: platform.DomainAdmin, ActorID: ids.New()}
	support := func(method, path string, body any) result {
		return h.do(call{method: method, path: internal + path, as: platform.AdminService, delegate: staff, key: key(), body: body})
	}
	acct := "/admin/accounts/" + o.id.String()

	grant := map[string]any{"scopes": []string{"ONBOARDING", "PROPERTY_RECORDS", "MEDIA_CAPTURE"}, "property_ids": []ids.ID{p1.ID}, "channel": "PHONE",
		"reason": "Help listing my flats", "expires_in_hours": 48}
	h.clock.advance(6 * time.Minute)
	h.relogin(o)
	h.clock.advance(6 * time.Minute)
	h.expect(h.post(o, "/assistance/authorizations", grant), 403, "STEP_UP_REQUIRED")
	h.relogin(o)
	grant["property_ids"] = []ids.ID{sp.ID}
	h.expect(h.post(o, "/assistance/authorizations", grant), 404, "")
	grant["property_ids"] = []ids.ID{p1.ID}
	r := h.post(o, "/assistance/authorizations", grant)
	h.expect(r, 201, "")
	var a usecases.AssistanceView
	r.data(t, &a)
	if a.Status != "ACTIVE" || len(a.Scopes) != 3 {
		t.Fatalf("authorization %+v", a)
	}

	// Only an attributable admin actor; reads are audited.
	h.expect(h.do(call{method: "GET", path: internal + acct + "/assistance", as: platform.MarketplaceService, delegate: customer()}), 403, "")
	h.expect(h.do(call{method: "GET", path: internal + acct + "/assistance", as: platform.AdminService}), 403, "")
	var av usecases.AdminAssistanceView
	support("GET", acct+"/assistance", nil).data(t, &av)
	if len(av.Authorizations) != 1 || av.AccountStatus != "ACTIVE" {
		t.Fatalf("admin view %+v", av)
	}
	if h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='assistance.privileged_read' AND actor_id=$2`, o.id, staff.ActorID) != 1 {
		t.Fatal("privileged read not attributed")
	}

	// Assisted photography on an authorized property.
	photo := h.media.put(p1.ID, "IMAGE", "READY")
	act := map[string]any{"authorization_id": a.ID, "activity_type": "PHOTOGRAPHY_CAPTURED", "property_id": p1.ID, "media_ids": []ids.ID{photo},
		"note": "Captured 12 photos on site"}
	r = support("POST", acct+"/assisted-activities", act)
	h.expect(r, 201, "")
	var rec usecases.AssistedActivityView
	r.data(t, &rec)
	if rec.SupportActorID != staff.ActorID || rec.SupportActorDomain != string(platform.DomainAdmin) || rec.Scope != "MEDIA_CAPTURE" || len(rec.MediaIDs) != 1 {
		t.Fatalf("activity %+v", rec)
	}
	act["property_id"] = p2.ID
	act["media_ids"] = nil
	h.expect(support("POST", acct+"/assisted-activities", act), 403, "")
	act["property_id"] = sp.ID
	h.expect(support("POST", acct+"/assisted-activities", act), 403, "")
	h.expect(support("POST", acct+"/assisted-activities", map[string]any{"authorization_id": a.ID, "activity_type": "DOCUMENT_SCANNED", "note": "Scanned deed"}), 403, "")
	h.expect(support("POST", "/admin/accounts/"+stranger.id.String()+"/assisted-activities", map[string]any{"authorization_id": a.ID,
		"activity_type": "REMOTE_HELP", "note": "Wrong account"}), 404, "")
	h.expect(support("POST", acct+"/assisted-activities", map[string]any{"authorization_id": a.ID, "activity_type": "ONBOARDING_GUIDANCE",
		"media_ids": []ids.ID{photo}, "note": "x y z"}), 400, "")

	// Concierge onboarding creates a DRAFT property attributed to the staff member.
	r = support("POST", acct+"/assisted-properties", map[string]any{"authorization_id": a.ID, "note": "Entered over the phone",
		"property": map[string]any{"category": "RESIDENTIAL", "property_type": "APARTMENT", "title": "Assisted flat", "bedrooms": 2, "bathrooms": 1,
			"address": address("13 Concierge Road")}})
	h.expect(r, 201, "")
	var ap usecases.AssistedPropertyView
	r.data(t, &ap)
	if ap.Property.Status != "DRAFT" || ap.Activity.ActivityType != "PROPERTY_DRAFT_CREATED" || *ap.Activity.PropertyID != ap.Property.ID {
		t.Fatalf("assisted property %+v", ap)
	}
	if h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='property.create' AND actor_id=$2`, ap.Property.ID, staff.ActorID) != 1 {
		t.Fatal("property creation not attributed to the staff member")
	}
	h.expect(h.get(o, "/properties/"+ap.Property.ID.String()), 200, "")
	if n := h.count(`SELECT count(*) FROM notification_dispatches WHERE template_key=$1 AND account_id=$2`, usecases.TemplateAssistedActivity, o.id); n != 2 {
		t.Fatalf("owner notified %d times", n)
	}
	var acts []usecases.AssistedActivityView
	h.get(o, "/assistance/activity").data(t, &acts)
	if len(acts) != 2 {
		t.Fatalf("owner activity %+v", acts)
	}
	if _, err := h.app.Pool.Exec(context.Background(), `DELETE FROM assisted_activities`); err == nil {
		t.Fatal("assisted activity is not append-only")
	}

	// Revocation and expiry end assistance immediately.
	h.expect(h.post(stranger, "/assistance/authorizations/"+a.ID.String()+"/revoke", map[string]any{}), 404, "")
	h.expect(h.post(o, "/assistance/authorizations/"+a.ID.String()+"/revoke", map[string]any{"reason": "Done"}), 200, "")
	h.expect(h.post(o, "/assistance/authorizations/"+a.ID.String()+"/revoke", map[string]any{}), 409, "")
	h.expect(support("POST", acct+"/assisted-activities", map[string]any{"authorization_id": a.ID, "activity_type": "REMOTE_HELP", "note": "After revoke"}), 403, "")
	grant["expires_in_hours"] = 1
	r = h.post(o, "/assistance/authorizations", grant)
	h.expect(r, 201, "")
	r.data(t, &a)
	h.clock.advance(2 * time.Hour)
	h.expect(support("POST", acct+"/assisted-activities", map[string]any{"authorization_id": a.ID, "activity_type": "REMOTE_HELP", "note": "Too late"}), 403, "")
	h.runJobs("statutory-reminders-and-assistance-expiry")
	h.relogin(o)
	var all []usecases.AssistanceView
	h.get(o, "/assistance/authorizations").data(t, &all)
	if len(all) != 2 || all[0].Status != "EXPIRED" || all[1].Status != "REVOKED" {
		t.Fatalf("authorizations %+v", all)
	}
}
