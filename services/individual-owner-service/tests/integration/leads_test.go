package integration

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

const internal = "/internal/v1/owner"

func customer() *serviceauth.Delegation {
	return &serviceauth.Delegation{ActorDomain: platform.DomainMarketplace, ActorID: ids.New()}
}

func (h *harness) asCustomer(c *serviceauth.Delegation, method, path string, body any) result {
	h.t.Helper()
	return h.do(call{method: method, path: internal + path, as: platform.MarketplaceService, delegate: c, key: key(), body: body})
}

// liveListing returns an owner (identity verified) with a published rent listing.
func (h *harness) liveListing(line1 string) (*owner, usecases.ListingView) {
	h.t.Helper()
	o := h.register(true)
	h.verifyIdentity(o)
	_, l := h.publishableListing(o, line1)
	h.expect(h.post(o, "/listings/"+l.ID.String()+"/publish", nil), 200, "")
	return o, h.listing(o, l.ID)
}

func TestInquiriesAndViewings(t *testing.T) {
	h := newHarness(t)
	o, l := h.liveListing("50 Lead Avenue")
	c1, c2 := customer(), customer()

	// Inquiry intake is idempotent by external_ref and requires a customer actor.
	body := map[string]any{"listing_id": l.ID, "customer_display_name": "Tolu A.", "external_ref": "mkt-inq-1", "occupants": 2}
	h.expect(h.do(call{method: "POST", path: internal + "/inquiries", as: platform.MarketplaceService, key: key(), body: body}), 403, "DELEGATED_ACTOR_REQUIRED")
	r := h.asCustomer(c1, "POST", "/inquiries", body)
	h.expect(r, 201, "")
	var inq usecases.InquiryView
	r.data(t, &inq)
	var again usecases.InquiryView
	h.asCustomer(c1, "POST", "/inquiries", body).data(t, &again)
	if again.ID != inq.ID {
		t.Fatal("external_ref not matched")
	}
	h.expect(h.asCustomer(c1, "POST", "/inquiries", map[string]any{"listing_id": ids.New(), "customer_display_name": "x"}), 404, "LISTING_NOT_AVAILABLE")
	if n := h.count(`SELECT count(*) FROM notification_dispatches WHERE template_key=$1`, usecases.TemplateNewInquiry); n != 1 {
		t.Fatalf("new inquiry notifications %d", n)
	}
	// Lawful qualification only.
	ipath := "/inquiries/" + inq.ID.String()
	h.expect(h.post(o, ipath+"/qualify", map[string]any{"outcome": "CLOSED_LOST", "reason_code": "NATIONALITY"}), 400, "INVALID_REQUEST")
	r = h.post(o, ipath+"/qualify", map[string]any{"outcome": "QUALIFIED", "budget_within_range": true, "term_months": 12})
	h.expect(r, 200, "")
	r.data(t, &inq)
	if inq.Status != "QUALIFIED" || inq.AcknowledgedAt == nil {
		t.Fatalf("qualified %+v", inq)
	}
	other := h.register(true)
	h.expect(h.get(other, ipath), 404, "")

	// Slots: capacity enforced.
	start := h.clock.now().Add(48 * time.Hour).Truncate(time.Hour)
	r = h.post(o, "/viewing-slots", map[string]any{"listing_id": l.ID, "starts_at": start, "ends_at": start.Add(30 * time.Minute), "mode": "PHYSICAL"})
	h.expect(r, 201, "")
	var slot usecases.SlotView
	r.data(t, &slot)
	h.expect(h.post(o, "/viewing-slots", map[string]any{"listing_id": l.ID, "starts_at": start.Add(10 * time.Minute), "ends_at": start.Add(40 * time.Minute), "mode": "PHYSICAL"}), 409, "SLOT_OVERLAP")
	r = h.asCustomer(c1, "POST", "/viewings", map[string]any{"listing_id": l.ID, "inquiry_id": inq.ID, "customer_display_name": "Tolu A.", "mode": "PHYSICAL", "slot_id": slot.ID})
	h.expect(r, 201, "")
	var v usecases.ViewingView
	r.data(t, &v)
	h.expect(h.asCustomer(c2, "POST", "/viewings", map[string]any{"listing_id": l.ID, "customer_display_name": "B", "mode": "PHYSICAL", "slot_id": slot.ID}), 409, "SLOT_FULL")
	h.expect(h.asCustomer(c2, "POST", "/viewings", map[string]any{"listing_id": l.ID, "inquiry_id": inq.ID, "customer_display_name": "B", "mode": "PHYSICAL",
		"starts_at": start.Add(3 * time.Hour), "ends_at": start.Add(4 * time.Hour)}), 422, "INQUIRY_INVALID")

	// Approve → access code once; customer reads it; check-in verifies it.
	vpath := "/viewings/" + v.ID.String()
	r = h.post(o, vpath+"/approve", nil)
	h.expect(r, 200, "")
	r.data(t, &v)
	if v.Status != "CONFIRMED" || v.AccessCode == nil || len(*v.AccessCode) != 6 {
		t.Fatalf("approve %+v", v)
	}
	code := *v.AccessCode
	var acc usecases.CustomerViewingAccess
	h.do(call{method: "GET", path: internal + vpath + "/access", as: platform.MarketplaceService, delegate: c1}).data(t, &acc)
	if acc.AccessCode == nil || *acc.AccessCode != code {
		t.Fatalf("customer access %+v", acc)
	}
	h.expect(h.do(call{method: "GET", path: internal + vpath + "/access", as: platform.MarketplaceService, delegate: c2}), 404, "")
	var stored []byte
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT access_code_sealed FROM viewing_requests WHERE id=$1`, v.ID).Scan(&stored)
	if strings.Contains(string(stored), code) {
		t.Fatal("access code stored in plaintext")
	}
	h.expect(h.post(o, vpath+"/check-in", map[string]any{"access_code": "000000"}), 422, "ACCESS_CODE_INVALID")
	h.expect(h.post(o, vpath+"/check-in", map[string]any{"access_code": code}), 200, "")
	r = h.post(o, vpath+"/outcome", map[string]any{"outcome": "COMPLETED", "rating": 5, "feedback": "Interested"})
	h.expect(r, 200, "")
	r.data(t, &v)
	if v.Status != "COMPLETED" || len(v.Attendance) != 2 || v.AccessCodeIssued {
		t.Fatalf("completed %+v", v)
	}
	h.get(o, ipath).data(t, &inq)
	if inq.Status != "VIEWING" {
		t.Fatalf("pipeline %s", inq.Status)
	}

	// Reschedule: owner proposes, approval waits for the customer.
	r = h.asCustomer(c2, "POST", "/viewings", map[string]any{"listing_id": l.ID, "customer_display_name": "B", "mode": "VIRTUAL_LIVE",
		"starts_at": start.Add(3 * time.Hour), "ends_at": start.Add(4 * time.Hour)})
	h.expect(r, 201, "")
	var v2 usecases.ViewingView
	r.data(t, &v2)
	if v2.VideoSession != "UNAVAILABLE" {
		t.Fatalf("video session %s", v2.VideoSession)
	}
	v2path := "/viewings/" + v2.ID.String()
	h.expect(h.post(o, v2path+"/reschedule", map[string]any{"starts_at": start.Add(5 * time.Hour), "ends_at": start.Add(6 * time.Hour)}), 200, "")
	h.expect(h.post(o, v2path+"/approve", nil), 409, "AWAITING_CUSTOMER")
	h.expect(h.asCustomer(c2, "POST", v2path+"/customer-response", map[string]any{"action": "ACCEPT_RESCHEDULE"}), 200, "")
	h.get(o, v2path).data(t, &v2)
	if v2.Status != "CONFIRMED" || !v2.StartsAt.Equal(start.Add(5*time.Hour)) || v2.AccessCodeIssued {
		t.Fatalf("rescheduled %+v", v2)
	}
	// A representative without viewings.manage cannot be assigned.
	h.expect(h.post(o, v2path+"/representative", map[string]any{"host_type": "REPRESENTATIVE", "host_ref_id": ids.New()}), 422, "REPRESENTATIVE_NOT_AUTHORIZED")
	h.validateEvents(v.ID)
}

func TestOccupiedPropertyViewingNeedsOccupantConsent(t *testing.T) {
	h := newHarness(t)
	o, l := h.liveListing("51 Occupied Close")
	var av usecases.AvailabilityView
	h.get(o, "/listings/"+l.ID.String()+"/availability").data(t, &av)
	from := h.clock.now().Add(60 * 24 * time.Hour).Format(time.DateOnly)
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/availability", av.Version, map[string]any{"status": "OCCUPIED", "available_from": from}), 200, "")
	if got := h.listing(o, l.ID); got.Status != "PUBLISHED" {
		t.Fatalf("re-letting listing paused: %s", got.Status)
	}
	c := customer()
	start := h.clock.now().Add(72 * time.Hour)
	r := h.asCustomer(c, "POST", "/viewings", map[string]any{"listing_id": l.ID, "customer_display_name": "C", "mode": "PHYSICAL", "starts_at": start, "ends_at": start.Add(time.Hour)})
	h.expect(r, 201, "")
	var v usecases.ViewingView
	r.data(t, &v)
	r = h.post(o, "/viewings/"+v.ID.String()+"/approve", nil)
	h.expect(r, 200, "")
	r.data(t, &v)
	if !v.Occupied || v.AccessCode != nil || v.AccessCodeIssued {
		t.Fatalf("occupied gate %+v", v)
	}
	r = h.post(o, "/viewings/"+v.ID.String()+"/occupant-consent", map[string]any{"consent_ref": "tenant-consent-77"})
	h.expect(r, 200, "")
	r.data(t, &v)
	if v.AccessCode == nil || !v.OccupantConsent {
		t.Fatalf("after consent %+v", v)
	}
}

func TestApplicationsScreeningDecisionsAndOffers(t *testing.T) {
	h := newHarness(t)
	o, l := h.liveListing("52 Apply Street")
	pid := l.PropertyID.String()
	h.expect(h.post(o, "/properties/"+pid+"/screening-policies", map[string]any{"criteria": []map[string]any{{"type": "RELIGION"}}}), 400, "INVALID_REQUEST")
	h.expect(h.post(o, "/properties/"+pid+"/screening-policies", map[string]any{"criteria": []map[string]any{
		{"type": "MAX_OCCUPANTS", "int_value": 4}, {"type": "PETS_ALLOWED", "bool_value": false}, {"type": "MIN_INCOME_MULTIPLE", "int_value": 30000}}}), 201, "")

	c1, c2 := customer(), customer()
	app := func(c *serviceauth.Delegation, occupants int, pets bool, income int64) result {
		return h.asCustomer(c, "POST", "/applications", map[string]any{"listing_id": l.ID, "term_months": 12, "occupants": occupants, "has_pets": pets, "consent": true,
			"participants": []map[string]any{{"role": "PRIMARY", "display_name": "Applicant", "employment_status": "EMPLOYED",
				"declared_monthly_income_minor": income, "income_currency": "NGN"}}})
	}
	h.expect(h.asCustomer(c1, "POST", "/applications", map[string]any{"listing_id": l.ID, "participants": []map[string]any{{"role": "PRIMARY", "display_name": "A"}}}), 422, "CONSENT_REQUIRED")
	r := app(c1, 5, true, 50000000)
	h.expect(r, 201, "")
	var a usecases.ApplicationView
	r.data(t, &a)
	h.expect(app(c1, 2, false, 1), 409, "APPLICATION_ALREADY_OPEN")
	flags := map[string]string{}
	for _, res := range a.ScreeningResults {
		if res.Source == "AUTOMATED_FLAG" {
			flags[res.Criterion] = res.Outcome
		}
	}
	// Monthly all-in cost ≈ 3.6m/12 + fees; 3x needs ≥ ~0.95m/month: 0.5m fails.
	if flags["MAX_OCCUPANTS"] != "NOT_MET" || flags["PETS_ALLOWED"] != "NOT_MET" || flags["MIN_INCOME_MULTIPLE"] != "NOT_MET" || a.Status != "SUBMITTED" {
		t.Fatalf("automated flags %v status %s", flags, a.Status)
	}
	apath := "/applications/" + a.ID.String()

	// Evidence request → customer grant → back to review → owner download.
	r = h.post(o, apath+"/request-info", map[string]any{"items": []map[string]any{{"evidence_type": "PROOF_OF_INCOME", "note": "Payslip or alternative"}}})
	h.expect(r, 200, "")
	r.data(t, &a)
	if a.Status != "INFO_REQUESTED" || len(a.Evidence) != 1 {
		t.Fatalf("request info %+v", a)
	}
	media := ids.New()
	h.expect(h.asCustomer(c2, "POST", apath+"/evidence", map[string]any{"grant_id": a.Evidence[0].ID, "media_id": media}), 404, "")
	h.expect(h.asCustomer(c1, "POST", apath+"/evidence", map[string]any{"grant_id": a.Evidence[0].ID, "media_id": media}), 201, "")
	h.media.files[media] = h.media.files[h.media.put(ids.New(), "DOCUMENT", "READY")]
	r = h.do(call{method: "POST", path: api + apath + "/evidence/" + a.Evidence[0].ID.String() + "/download-url", token: o.access})
	h.expect(r, 200, "")
	h.get(o, apath).data(t, &a)
	if a.Status != "UNDER_REVIEW" || a.Evidence[0].AccessCount != 1 {
		t.Fatalf("after evidence %+v", a)
	}

	// Human decision with lawful reasons; appeal → human review → approval.
	h.expect(h.post(o, apath+"/decision", map[string]any{"decision": "DECLINED"}), 400, "INVALID_REQUEST")
	h.expect(h.post(o, apath+"/decision", map[string]any{"decision": "DECLINED", "reason_codes": []string{"TRIBE"}}), 422, "DECLINE_REASON_NOT_CONFIGURED")
	h.expect(h.post(o, apath+"/decision", map[string]any{"decision": "DECLINED", "reason_codes": []string{"OCCUPANCY_LIMIT"}}), 200, "")
	h.expect(h.post(o, apath+"/offer", map[string]any{"start_date": "2026-12-01", "term_months": 12}), 409, "APPLICATION_NOT_APPROVED")
	h.expect(h.asCustomer(c1, "POST", apath+"/customer-action", map[string]any{"action": "APPEAL"}), 200, "")
	h.expect(h.post(o, apath+"/review", nil), 200, "")
	r = h.post(o, apath+"/decision", map[string]any{"decision": "CONDITIONAL", "conditions": []string{"Guarantor required"}})
	h.expect(r, 200, "")
	r.data(t, &a)
	if a.Status != "CONDITIONAL" || len(a.Decisions) != 2 || !a.Decisions[1].HumanReview || a.Decisions[1].ScreeningPolicyID == nil {
		t.Fatalf("decisions %+v", a.Decisions)
	}
	if _, err := h.db.Pool.Exec(context.Background(), `UPDATE application_decisions SET decision='APPROVED' WHERE application_id=$1`, a.ID); err == nil {
		t.Fatal("decisions must be immutable")
	}

	// Offer: draft → send → accept (freezes price, pauses the listing).
	r = h.post(o, apath+"/offer", map[string]any{"start_date": "2026-12-01", "term_months": 12, "conditions": []string{"Guarantor form"}})
	h.expect(r, 201, "")
	var off usecases.OfferView
	r.data(t, &off)
	h.expect(h.post(o, apath+"/offer", map[string]any{"start_date": "2026-12-01", "term_months": 12}), 409, "OFFER_ALREADY_OPEN")
	h.expect(h.write(o, "PATCH", "/offers/"+off.ID.String(), off.Version, map[string]any{"start_date": "2026-12-15", "term_months": 24}), 200, "")
	h.expect(h.post(o, "/offers/"+off.ID.String()+"/send", nil), 200, "")
	h.expect(h.asCustomer(c2, "POST", "/offers/"+off.ID.String()+"/response", map[string]any{"decision": "ACCEPT"}), 404, "")
	r = h.asCustomer(c1, "POST", "/offers/"+off.ID.String()+"/response", map[string]any{"decision": "ACCEPT"})
	h.expect(r, 200, "")
	r.data(t, &off)
	if off.Status != "ACCEPTED" {
		t.Fatalf("offer %+v", off)
	}
	var accepted *time.Time
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT accepted_at FROM pricing_versions WHERE id=$1`, off.PricingVersionID).Scan(&accepted)
	got := h.listing(o, l.ID)
	if accepted == nil || got.Status != "PAUSED" || got.Availability.Status != "OFFER_ACCEPTED" {
		t.Fatalf("acceptance effects: accepted=%v listing=%s availability=%s", accepted, got.Status, got.Availability.Status)
	}
	var costs map[string]any
	_ = json.Unmarshal(off.Cost, &costs)
	if costs["term_months"].(float64) != 24 {
		t.Fatalf("offer cost %v", costs)
	}
	h.validateEvents(a.ID)
	h.validateEvents(off.ID)
}

func TestConcurrentOfferAcceptanceAllowsExactlyOne(t *testing.T) {
	h := newHarness(t)
	o, l := h.liveListing("53 Race Road")
	var offers []usecases.OfferView
	var custs []*serviceauth.Delegation
	for i := 0; i < 2; i++ {
		c := customer()
		r := h.asCustomer(c, "POST", "/applications", map[string]any{"listing_id": l.ID, "term_months": 12, "consent": true,
			"participants": []map[string]any{{"role": "PRIMARY", "display_name": "P"}}})
		h.expect(r, 201, "")
		var a usecases.ApplicationView
		r.data(t, &a)
		h.expect(h.post(o, "/applications/"+a.ID.String()+"/decision", map[string]any{"decision": "APPROVED"}), 200, "")
		r = h.post(o, "/applications/"+a.ID.String()+"/offer", map[string]any{"start_date": "2026-12-01", "term_months": 12})
		var off usecases.OfferView
		r.data(t, &off)
		h.expect(h.post(o, "/offers/"+off.ID.String()+"/send", nil), 200, "")
		offers, custs = append(offers, off), append(custs, c)
	}
	var wg sync.WaitGroup
	codes := make([]string, 2)
	statuses := make([]int, 2)
	for i := range offers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := h.asCustomer(custs[i], "POST", "/offers/"+offers[i].ID.String()+"/response", map[string]any{"decision": "ACCEPT"})
			statuses[i], codes[i] = r.status, r.code()
		}(i)
	}
	wg.Wait()
	ok := 0
	for i := range statuses {
		if statuses[i] == 200 {
			ok++
		} else if statuses[i] != 409 {
			t.Fatalf("unexpected %d %s", statuses[i], codes[i])
		}
	}
	if ok != 1 || h.count(`SELECT count(*) FROM rental_offers WHERE listing_id=$1 AND status='ACCEPTED'`, l.ID) != 1 {
		t.Fatalf("acceptances %v %v", statuses, codes)
	}
}

func TestApplicationAndOfferDeadlines(t *testing.T) {
	h := newHarness(t)
	o, l := h.liveListing("54 Deadline Way")
	c := customer()
	r := h.asCustomer(c, "POST", "/applications", map[string]any{"listing_id": l.ID, "consent": true, "participants": []map[string]any{{"role": "PRIMARY", "display_name": "P"}}})
	var a usecases.ApplicationView
	r.data(t, &a)
	c2 := customer()
	r = h.asCustomer(c2, "POST", "/applications", map[string]any{"listing_id": l.ID, "consent": true, "participants": []map[string]any{{"role": "PRIMARY", "display_name": "Q"}}})
	var b usecases.ApplicationView
	r.data(t, &b)
	h.expect(h.post(o, "/applications/"+b.ID.String()+"/decision", map[string]any{"decision": "APPROVED"}), 200, "")
	r = h.post(o, "/applications/"+b.ID.String()+"/offer", map[string]any{"start_date": "2026-12-01", "term_months": 12, "expires_in_days": 2})
	var off usecases.OfferView
	r.data(t, &off)
	h.expect(h.post(o, "/offers/"+off.ID.String()+"/send", nil), 200, "")
	h.clock.advance(15 * 24 * time.Hour)
	if err := h.app.Service.RunApplicationDeadlines(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	var st string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT status FROM rental_applications WHERE id=$1`, a.ID).Scan(&st)
	if st != "EXPIRED" {
		t.Fatalf("application %s", st)
	}
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT status FROM rental_offers WHERE id=$1`, off.ID).Scan(&st)
	if st != "EXPIRED" {
		t.Fatalf("offer %s", st)
	}
	h.expect(h.asCustomer(c2, "POST", "/offers/"+off.ID.String()+"/response", map[string]any{"decision": "ACCEPT"}), 409, "")
}
