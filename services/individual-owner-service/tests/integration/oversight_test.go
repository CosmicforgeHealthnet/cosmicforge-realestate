package integration

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func TestDisputesConsentsAndPrivacyRequests(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "3 Fraud Close")
	other := h.register(true)

	// Disputes target records the owner can act on; fraud categories are urgent.
	body := map[string]any{"target_type": "property", "target_id": p.ID, "category": "FALSE_OWNERSHIP", "summary": "Someone listed my house",
		"evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}
	r := h.post(o, "/disputes", body)
	h.expect(r, 201, "")
	var d usecases.DisputeView
	r.data(t, &d)
	if d.Status != "OPEN" || !d.Urgent || len(d.Evidence) != 1 || d.PropertyID == nil {
		t.Fatalf("dispute %+v", d)
	}
	h.expect(h.post(o, "/disputes", body), 409, "DISPUTE_ALREADY_OPEN")
	body["evidence_media_ids"] = nil
	h.expect(h.post(other, "/disputes", body), 404, "")
	h.expect(h.post(o, "/disputes", map[string]any{"target_type": "account", "target_id": other.id, "category": "BILLING", "summary": "x"}), 404, "")
	h.expect(h.post(o, "/disputes", map[string]any{"target_type": "account", "target_id": o.id, "category": "BILLING", "summary": "Charged twice"}), 201, "")
	h.expect(h.get(other, "/disputes/"+d.ID.String()), 404, "")

	// Operators review through the admin contract; the owner answers.
	var queue struct {
		Data []usecases.DisputeView `json:"data"`
	}
	_ = json.Unmarshal(h.admin("GET", "/admin/disputes?status=OPEN", nil).body, &queue)
	if len(queue.Data) != 2 || !queue.Data[0].Urgent {
		t.Fatalf("admin queue %+v", queue.Data)
	}
	h.expect(h.admin("GET", "/admin/disputes/"+d.ID.String(), nil), 200, "")
	if h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='dispute.privileged_read'`, d.ID) != 1 {
		t.Fatal("privileged read not audited")
	}
	dp := "/admin/disputes/" + d.ID.String() + "/status"
	h.expect(h.admin("POST", dp, map[string]any{"status": "AWAITING_INFORMATION"}), 400, "")
	h.expect(h.admin("POST", dp, map[string]any{"status": "AWAITING_INFORMATION", "admin_case_ref": "ADM-91", "information_request": "Send your title deed"}), 200, "")
	h.expect(h.post(o, "/disputes/"+d.ID.String()+"/updates", map[string]any{"action": "ADD_INFORMATION"}), 400, "")
	h.expect(h.post(o, "/disputes/"+d.ID.String()+"/updates", map[string]any{"action": "ADD_INFORMATION", "note": "Deed attached",
		"evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 200, "")
	h.get(o, "/disputes/"+d.ID.String()).data(t, &d)
	if d.Status != "UNDER_REVIEW" || len(d.Evidence) != 3 || *d.AdminCaseRef != "ADM-91" {
		t.Fatalf("after info %+v", d)
	}
	h.expect(h.admin("POST", dp, map[string]any{"status": "RESOLVED"}), 400, "")
	h.expect(h.admin("POST", dp, map[string]any{"status": "RESOLVED", "resolution_code": "LISTING_REMOVED", "resolution_note": "Fraudulent listing removed"}), 200, "")
	h.expect(h.post(o, "/disputes/"+d.ID.String()+"/updates", map[string]any{"action": "WITHDRAW"}), 409, "DISPUTE_CLOSED")
	if h.outboxTypes(d.ID)["dispute.case.updated.v1"] != 4 {
		t.Fatalf("dispute events %v", h.outboxTypes(d.ID))
	}
	h.validateEvents(d.ID)

	// Consents are append-only decisions.
	h.expect(h.post(o, "/privacy/consents", map[string]any{"purpose": "AI_INSIGHTS", "decision": "GRANTED", "policy_version": "2027-01"}), 200, "")
	h.expect(h.post(o, "/privacy/consents", map[string]any{"purpose": "AI_INSIGHTS", "decision": "WITHDRAWN", "policy_version": "2027-01"}), 200, "")
	var consents []usecases.PrivacyConsentView
	h.get(o, "/privacy/consents").data(t, &consents)
	if len(consents) != 1 || consents[0].Decision != "WITHDRAWN" || h.count(`SELECT count(*) FROM consents WHERE account_id=$1`, o.id) != 2 {
		t.Fatalf("consents %+v", consents)
	}

	// Export: a fresh login verifies the requester; the export is a PDF in
	// media-document-service.
	r = h.post(o, "/privacy/requests", map[string]any{"request_type": "EXPORT"})
	h.expect(r, 201, "")
	var x usecases.PrivacyRequestView
	r.data(t, &x)
	if x.Status != "VERIFIED_REQUESTER" || x.VerifiedAt == nil {
		t.Fatalf("export request %+v", x)
	}
	h.expect(h.post(o, "/privacy/requests", map[string]any{"request_type": "EXPORT"}), 409, "REQUEST_ALREADY_OPEN")
	h.runJobs("privacy-requests-and-retention")
	h.runJobs("report-generation")
	h.runJobs("privacy-requests-and-retention")
	h.get(o, "/privacy/requests/"+x.ID.String()).data(t, &x)
	if x.Status != "FULFILLED" || x.ReportID == nil {
		t.Fatalf("export fulfilled %+v", x)
	}
	var rep usecases.OwnerReportView
	h.get(o, "/reports/"+x.ReportID.String()).data(t, &rep)
	if rep.Status != "READY" || rep.DownloadURL == nil || rep.ReportType != "PRIVACY_EXPORT" {
		t.Fatalf("export report %+v", rep)
	}
	h.media.mu.Lock()
	pdf := h.media.uploads[*rep.MediaID]
	h.media.mu.Unlock()
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf, []byte("3 Fraud Close")) || !bytes.Contains(pdf, []byte("AI_INSIGHTS")) {
		t.Fatalf("export content %q", pdf[:min(200, len(pdf))])
	}
	h.expect(h.get(other, "/reports/"+x.ReportID.String()), 404, "")

	// Deletion: a stale session must step up; an operator decides what is
	// retained; decided requests close after 30 days.
	h.clock.advance(6 * time.Minute)
	r = h.post(o, "/privacy/requests", map[string]any{"request_type": "DELETION"})
	h.expect(r, 201, "")
	var del usecases.PrivacyRequestView
	r.data(t, &del)
	if del.Status != "SUBMITTED" {
		t.Fatalf("stale step-up %s", del.Status)
	}
	h.expect(h.post(o, "/privacy/requests/"+del.ID.String()+"/verify", nil), 403, "STEP_UP_REQUIRED")
	h.relogin(o)
	h.expect(h.post(o, "/privacy/requests/"+del.ID.String()+"/verify", nil), 200, "")
	h.runJobs("privacy-requests-and-retention")
	dpath := "/admin/privacy-requests/" + del.ID.String() + "/decision"
	h.expect(h.admin("POST", "/admin/privacy-requests/"+x.ID.String()+"/decision", map[string]any{"outcome": "FULFILLED", "reason": "x"}), 409, "")
	h.expect(h.admin("POST", dpath, map[string]any{"outcome": "PARTIALLY_FULFILLED", "reason": "Marketing data erased"}), 400, "")
	h.expect(h.admin("POST", dpath, map[string]any{"outcome": "PARTIALLY_FULFILLED", "reason": "Marketing data erased",
		"retained_categories": []string{"OWNERSHIP_RECORDS", "AUDIT_TRAIL"}}), 200, "")
	h.clock.advance(31 * 24 * time.Hour)
	h.runJobs("privacy-requests-and-retention")
	h.relogin(o)
	h.get(o, "/privacy/requests/"+del.ID.String()).data(t, &del)
	if del.Status != "CLOSED" || len(del.RetainedCategories) != 2 {
		t.Fatalf("closed %+v", del)
	}

	// The owner's audit trail.
	var act struct {
		Data []usecases.AuditActivityView `json:"data"`
	}
	_ = json.Unmarshal(h.get(o, "/audit/activity?limit=100").body, &act)
	seen := map[string]bool{}
	for _, a := range act.Data {
		seen[a.Action] = true
	}
	if !seen["dispute.raise"] || !seen["privacy.request.submit"] {
		t.Fatalf("activity %v", seen)
	}
}

func TestReportsDashboardCalendarAndMarketInsights(t *testing.T) {
	h := newHarness(t, withPayment)
	o, l := h.shortletListingPriced("1 Insight Way", 5000000)
	lp := "/listings/" + l.ID.String() + "/shortlet"
	settings := shortletSettings("PLATFORM", true, false)
	h.expect(h.write(o, "PUT", lp+"/settings", 0, settings), 200, "")
	now := h.clock.now()
	b := h.requestBooking(customer(), bookingBody(l, dayN(now, 3), dayN(now, 5), "Kemi"))
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.pay.settle(b.ID, b.TotalMinor, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")

	// Live portfolio report and dashboard.
	var rep usecases.PortfolioReport
	h.get(o, "/reports/portfolio?from="+dayN(now, 0)+"&to="+dayN(now, 29)).data(t, &rep)
	if rep.Properties != 1 || rep.Shortlet.NightsBooked != 2 || rep.Shortlet.Revenue["NGN"] != b.TotalMinor || rep.Shortlet.ADR["NGN"] != b.TotalMinor/2 ||
		rep.ListingsByStatus["PUBLISHED"] != 1 || rep.NetIncome["NGN"] != b.TotalMinor || rep.YieldBps != nil {
		t.Fatalf("portfolio %+v", rep)
	}
	h.expect(h.get(o, "/reports/portfolio?from=2027-02-01&to=2027-01-01"), 400, "")
	var dash usecases.DashboardView
	h.get(o, "/dashboard").data(t, &dash)
	if dash.Properties != 1 || len(dash.Upcoming) == 0 || dash.ActionItems["overdue_rent"] != 0 {
		t.Fatalf("dashboard %+v", dash)
	}
	h.expect(h.do(call{method: "PUT", path: api + "/dashboard/preferences", token: o.access, key: key(), body: map[string]any{"widgets": []string{"CALENDAR", "SHORTLET"},
		"default_property_id": l.PropertyID}}), 200, "")
	h.get(o, "/dashboard").data(t, &dash)
	if len(dash.Preferences.Widgets) != 2 || *dash.Preferences.DefaultPropertyID != l.PropertyID {
		t.Fatalf("preferences %+v", dash.Preferences)
	}
	var cal []usecases.CalendarEntry
	h.get(o, "/calendar?from="+dayN(now, 0)+"&to="+dayN(now, 10)).data(t, &cal)
	kinds := map[string]bool{}
	for _, e := range cal {
		kinds[e.Kind] = true
	}
	if !kinds["CHECK_IN"] || !kinds["CHECK_OUT"] {
		t.Fatalf("calendar %+v", cal)
	}

	// Exported PDF report through media-document-service.
	r := h.post(o, "/reports/export", map[string]any{"report_type": "SHORTLET", "from": dayN(now, 0), "to": dayN(now, 29)})
	h.expect(r, 202, "")
	var ex usecases.OwnerReportView
	r.data(t, &ex)
	if ex.Status != "QUEUED" {
		t.Fatalf("queued %+v", ex)
	}
	h.runJobs("report-generation")
	h.get(o, "/reports/"+ex.ID.String()).data(t, &ex)
	if ex.Status != "READY" || ex.DownloadURL == nil || len(ex.Summary) == 0 {
		t.Fatalf("ready %+v", ex)
	}
	h.media.mu.Lock()
	pdf := h.media.uploads[*ex.MediaID]
	h.media.mu.Unlock()
	if !bytes.Contains(pdf, []byte("Nights booked: 2")) {
		t.Fatalf("pdf %s", pdf)
	}

	// Market insights: advisory comparables from the same area, never naming
	// other owners' listings.
	h.expect(h.get(o, "/market-insights"), 400, "")
	var ins []usecases.InsightView
	h.get(o, "/market-insights?listing_id="+l.ID.String()).data(t, &ins)
	var price map[string]any
	_ = json.Unmarshal(ins[0].Result, &price)
	if len(ins) != 2 || ins[0].InsightType != "PRICE_RECOMMENDATION" || price["status"] != "INSUFFICIENT_DATA" || !ins[0].Advisory || ins[0].ModelVersion == "" {
		t.Fatalf("insufficient %+v", ins)
	}
	for i, n := range []int64{4000000, 5500000, 7000000} {
		h.shortletListingPriced(string(rune('2'+i))+" Insight Way", n)
	}
	h.clock.advance(25 * time.Hour)
	h.relogin(o)
	h.get(o, "/market-insights?listing_id="+l.ID.String()).data(t, &ins)
	_ = json.Unmarshal(ins[0].Result, &price)
	var inputs map[string]any
	_ = json.Unmarshal(ins[0].Inputs, &inputs)
	if price["status"] != "OK" || price["median_minor"].(float64) != 5500000 || price["position"] != "WITHIN_RANGE" || inputs["comparable_count"].(float64) != 3 ||
		inputs["comparable_listing_ids"] != nil || ins[0].Confidence <= 0 || ins[0].Confidence > 1 {
		t.Fatalf("insights %+v %v %v", ins[0], price, inputs)
	}
	var again []usecases.InsightView
	h.get(o, "/market-insights?listing_id="+l.ID.String()).data(t, &again)
	if !again[0].GeneratedAt.Equal(ins[0].GeneratedAt) {
		t.Fatal("insights not cached")
	}
	h.expect(h.get(h.register(true), "/market-insights?listing_id="+l.ID.String()), 404, "")
}
