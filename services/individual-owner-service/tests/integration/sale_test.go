package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func (h *harness) propertyOf(o *owner, line1, category, typ string) usecases.PropertyView {
	h.t.Helper()
	r := h.post(o, "/properties", map[string]any{"category": category, "property_type": typ, "title": "Premises", "address": address(line1)})
	h.expect(r, 201, "")
	var v usecases.PropertyView
	r.data(h.t, &v)
	return v
}

// liveOf publishes a listing of any transaction type on a verified property.
func (h *harness) liveOf(o *owner, p usecases.PropertyView, txn string, details map[string]any, pricing map[string]any) usecases.ListingView {
	h.t.Helper()
	h.verifyProperty(o, p.ID)
	media := h.photos(o, p.ID, 3)
	r := h.post(o, "/listings", map[string]any{"property_id": p.ID, "transaction_type": txn, "title": "Prime " + strings.ToLower(txn),
		"description": strings.Repeat("Well located and well kept. ", 10), "details": details, "media": selection(media)})
	h.expect(r, 201, "")
	var l usecases.ListingView
	r.data(h.t, &l)
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/pricing", 0, pricing), 200, "")
	h.expect(h.post(o, "/listings/"+l.ID.String()+"/publish", nil), 200, "")
	return h.listing(o, l.ID)
}

var salePricing = map[string]any{"currency": "NGN", "base_amount_minor": 9000000000, "base_period": "TOTAL", "payment_cycle": "ONE_TIME"}

func (h *harness) saleListing(line1 string) (*owner, usecases.ListingView) {
	h.t.Helper()
	o := h.register(true)
	h.verifyIdentity(o)
	p := h.createProperty(o, line1)
	return o, h.liveOf(o, p, "SALE", map[string]any{"title_document_type": "CERTIFICATE_OF_OCCUPANCY"}, salePricing)
}

func (h *harness) offer(c *serviceauth.Delegation, l usecases.ListingView, amount int64) usecases.SaleOfferView {
	h.t.Helper()
	r := h.asCustomer(c, "POST", "/sale-offers", map[string]any{"listing_id": l.ID, "buyer_display_name": "Buyer", "amount_minor": amount, "currency": "NGN",
		"financing": "CASH", "proof_of_funds_ref": "pof-ref-1"})
	h.expect(r, 201, "")
	var o usecases.SaleOfferView
	r.data(h.t, &o)
	return o
}

func (h *harness) sale(o *owner, id ids.ID) usecases.SaleView {
	h.t.Helper()
	var v usecases.SaleView
	r := h.get(o, "/transactions/sales/"+id.String())
	h.expect(r, 200, "")
	r.data(h.t, &v)
	return v
}

func TestPropertySaleOffersClosingAndDataRoom(t *testing.T) {
	h := newHarness(t)
	o, l := h.saleListing("70 Vendor Road")
	c1, c2 := customer(), customer()
	o1 := h.offer(c1, l, 8000000000)
	h.expect(h.asCustomer(c1, "POST", "/sale-offers", map[string]any{"listing_id": l.ID, "buyer_display_name": "B", "amount_minor": 1, "currency": "NGN", "financing": "CASH"}), 409, "OFFER_ALREADY_OPEN")
	h.expect(h.asCustomer(c2, "POST", "/sale-offers", map[string]any{"listing_id": l.ID, "buyer_display_name": "B", "amount_minor": 1, "currency": "USD", "financing": "CASH"}), 422, "CURRENCY_MISMATCH")
	o2 := h.offer(c2, l, 8500000000)
	var page struct {
		Data []usecases.SaleView `json:"data"`
	}
	r := h.get(o, "/transactions/sales?property_id="+l.PropertyID.String())
	_ = json.Unmarshal(r.body, &page)
	if len(page.Data) != 1 || page.Data[0].Status != "OFFERING" || page.Data[0].SaleKind != "PROPERTY" {
		t.Fatalf("sales %s", r.body)
	}
	sid := page.Data[0].ID
	spath := "/transactions/sales/" + sid.String()
	h.expect(h.get(h.register(true), spath), 404, "")
	h.expect(h.post(o, spath+"/offers/"+o2.ID.String()+"/decision", map[string]any{"decision": "ACCEPT", "settlement": "PLATFORM"}), 503, "FEATURE_UNAVAILABLE")
	h.expect(h.post(o, spath+"/offers/"+o2.ID.String()+"/decision", map[string]any{"decision": "DECLINE", "reason_code": "PRICE_TOO_LOW"}), 200, "")
	r = h.post(o, spath+"/offers/"+o1.ID.String()+"/decision", map[string]any{"decision": "COUNTER", "counter_amount_minor": 8800000000, "counter_conditions": []string{"Completion within 60 days"}})
	h.expect(r, 200, "")
	var s usecases.SaleView
	r.data(t, &s)
	counter := s.Offers[len(s.Offers)-1]
	if counter.MadeBy != "OWNER" || counter.ParentOfferID == nil || *counter.ParentOfferID != o1.ID || counter.Status != "SUBMITTED" {
		t.Fatalf("counter %+v", counter)
	}
	h.expect(h.post(o, spath+"/offers/"+counter.ID.String()+"/decision", map[string]any{"decision": "ACCEPT"}), 409, "OFFER_NOT_AWAITING_OWNER")
	h.expect(h.asCustomer(c2, "POST", "/sale-offers/"+counter.ID.String()+"/customer-response", map[string]any{"decision": "ACCEPT"}), 404, "")
	h.expect(h.asCustomer(c1, "POST", "/sale-offers/"+counter.ID.String()+"/customer-response", map[string]any{"decision": "ACCEPT"}), 200, "")
	s = h.sale(o, sid)
	if s.Status != "OFFER_ACCEPTED" || s.AgreedPriceMinor == nil || *s.AgreedPriceMinor != 8800000000 || len(s.Milestones) != 4 {
		t.Fatalf("accepted %+v", s)
	}
	if got := h.listing(o, l.ID); got.Status != "PAUSED" || got.Availability.Status != "OFFER_ACCEPTED" {
		t.Fatalf("listing after acceptance %s/%s", got.Status, got.Availability.Status)
	}

	// Due diligence gates contracting.
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "COMPLETED"}), 409, "INVALID_STATE_TRANSITION")
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "DUE_DILIGENCE"}), 200, "")
	r = h.post(o, spath+"/due-diligence", map[string]any{"category": "TITLE_SEARCH", "title": "Search at the land registry", "blocking": true})
	h.expect(r, 201, "")
	var dd usecases.DiligenceView
	r.data(t, &dd)
	buyerDoc := h.customerMedia(c1, "DOCUMENT")
	h.expect(h.asCustomer(c1, "POST", spath+"/customer-due-diligence", map[string]any{"category": "SURVEY", "title": "Independent survey", "evidence_media_ids": []ids.ID{buyerDoc}}), 201, "")
	h.expect(h.asCustomer(c2, "POST", spath+"/customer-due-diligence", map[string]any{"category": "SURVEY", "title": "x"}), 404, "")
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "CONTRACTING"}), 422, "BLOCKING_DILIGENCE_OPEN")
	h.expect(h.write(o, "PATCH", spath+"/due-diligence/"+dd.ID.String(), dd.Version, map[string]any{"status": "RESOLVED", "blocking": true}), 400, "")
	h.expect(h.write(o, "PATCH", spath+"/due-diligence/"+dd.ID.String(), dd.Version, map[string]any{"status": "RESOLVED", "blocking": true,
		"finding": "No encumbrance", "resolution_note": "Clean search report"}), 200, "")
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "CONTRACTING"}), 200, "")

	// Data room: least privilege by category, audited downloads, revocation.
	r = h.post(o, spath+"/data-room", nil)
	h.expect(r, 200, "")
	var room usecases.DataRoomView
	r.data(t, &room)
	title, survey := h.media.put(o.id, "DOCUMENT", "READY"), h.media.put(o.id, "DOCUMENT", "READY")
	h.expect(h.post(o, spath+"/data-room/documents", map[string]any{"media_id": title, "category": "TITLE", "title": "C of O"}), 200, "")
	r = h.post(o, spath+"/data-room/documents", map[string]any{"media_id": survey, "category": "SURVEY", "title": "Survey plan"})
	h.expect(r, 200, "")
	r.data(t, &room)
	pro := &serviceauth.Delegation{ActorDomain: platform.DomainProfessionalServices, ActorID: ids.New()}
	h.expect(h.post(o, spath+"/data-room/grants", map[string]any{"grantee_domain": "marketplace", "grantee_ref_id": c1.ActorID, "grantee_label": "Buyer",
		"purpose": "sale.due_diligence", "categories": []string{"TITLE"}, "expires_in_days": 7}), 200, "")
	h.expect(h.post(o, spath+"/data-room/grants", map[string]any{"grantee_domain": "professional-services", "grantee_ref_id": pro.ActorID, "grantee_label": "Surveyor",
		"purpose": "sale.survey_review", "categories": []string{"SURVEY"}, "expires_in_days": 3}), 200, "")
	rpath := internal + "/data-rooms/" + room.ID.String()
	var seen usecases.DataRoomView
	h.do(call{method: "GET", path: rpath, as: platform.MarketplaceService, delegate: c1}).data(t, &seen)
	if len(seen.Documents) != 1 || seen.Documents[0].Category != "TITLE" || seen.Grants != nil {
		t.Fatalf("buyer scope %+v", seen)
	}
	h.expect(h.do(call{method: "GET", path: rpath, as: platform.MarketplaceService, delegate: c2}), 404, "")
	var titleDoc, surveyDoc ids.ID
	for _, d := range room.Documents {
		if d.Category == "TITLE" {
			titleDoc = d.ID
		} else {
			surveyDoc = d.ID
		}
	}
	dl := func(as platform.Service, d *serviceauth.Delegation, doc ids.ID) result {
		return h.do(call{method: "POST", path: rpath + "/documents/" + doc.String() + "/download-url", as: as, delegate: d})
	}
	h.expect(dl(platform.MarketplaceService, c1, titleDoc), 200, "")
	h.expect(dl(platform.MarketplaceService, c1, surveyDoc), 404, "")
	h.expect(dl(platform.ProfessionalServicesService, pro, surveyDoc), 200, "")
	h.expect(dl(platform.ArtisanService, &serviceauth.Delegation{ActorDomain: platform.DomainArtisan, ActorID: pro.ActorID}, surveyDoc), 403, "")
	if n := h.count(`SELECT count(*) FROM data_room_access_log l JOIN data_room_grants g ON g.id = l.grant_id WHERE g.data_room_id=$1`, room.ID); n != 2 {
		t.Fatalf("access log %d", n)
	}
	h.get(o, spath+"/data-room").data(t, &room)
	for _, g := range room.Grants {
		if g.GranteeDomain == "marketplace" {
			h.expect(h.post(o, spath+"/data-room/grants/"+g.ID.String()+"/revoke", nil), 200, "")
		}
	}
	h.expect(dl(platform.MarketplaceService, c1, titleDoc), 404, "")

	// Closing checklist: evidence, buyer confirmation of off-platform money.
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "CONDITIONS_PENDING"}), 200, "")
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "CLOSING"}), 422, "MILESTONES_INCOMPLETE")
	doc := func() []ids.ID { return []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")} }
	h.expect(h.post(o, spath+"/milestones", map[string]any{"code": "PURCHASE_AGREEMENT", "action": "EVIDENCE"}), 422, "EVIDENCE_REQUIRED")
	h.expect(h.post(o, spath+"/milestones", map[string]any{"code": "PURCHASE_AGREEMENT", "action": "EVIDENCE", "evidence_media_ids": doc()}), 200, "")
	h.expect(h.post(o, spath+"/milestones", map[string]any{"code": "BALANCE_PAYMENT", "action": "WAIVE", "reason_code": "PAID"}), 422, "PAYMENT_MILESTONE_NOT_WAIVABLE")
	h.expect(h.post(o, spath+"/milestones", map[string]any{"code": "BALANCE_PAYMENT", "action": "EVIDENCE", "evidence_media_ids": doc()}), 200, "")
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "CLOSING"}), 200, "")
	r = h.post(o, spath+"/advance", map[string]any{"to": "COMPLETED"})
	h.expect(r, 422, "MILESTONES_INCOMPLETE")
	if !strings.Contains(string(r.body), "BALANCE_PAYMENT,POSSESSION_HANDOVER") {
		t.Fatalf("unmet milestones %s", r.body)
	}
	h.expect(h.asCustomer(c1, "POST", spath+"/customer-action", map[string]any{"action": "CONFIRM_MILESTONE", "milestone_code": "POSSESSION_HANDOVER"}), 409, "MILESTONE_NOT_AWAITING_CONFIRMATION")
	h.expect(h.asCustomer(c1, "POST", spath+"/customer-action", map[string]any{"action": "CONFIRM_MILESTONE", "milestone_code": "BALANCE_PAYMENT"}), 200, "")
	h.expect(h.post(o, spath+"/milestones", map[string]any{"code": "POSSESSION_HANDOVER", "action": "EVIDENCE", "evidence_media_ids": doc()}), 200, "")
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "COMPLETED"}), 200, "")
	s = h.sale(o, sid)
	ms := map[string]usecases.MilestoneView{}
	for _, m := range s.Milestones {
		ms[m.Code] = m
	}
	if s.Status != "COMPLETED" || ms["BALANCE_PAYMENT"].AttestedBy == nil || *ms["BALANCE_PAYMENT"].AttestedBy != "OWNER_AND_BUYER" || ms["TITLE_PERFECTION"].Status != "PENDING" {
		t.Fatalf("completed %+v", s)
	}
	if got := h.listing(o, l.ID); got.Status != "WITHDRAWN" || got.Availability.Status != "SOLD" {
		t.Fatalf("listing after completion %s/%s", got.Status, got.Availability.Status)
	}
	h.expect(dl(platform.ProfessionalServicesService, pro, surveyDoc), 404, "")
	h.validateEvents(sid)
	if n := h.count(`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='sale.transaction.updated.v1'`, sid); n != 7 {
		t.Fatalf("sale events %d", n)
	}
}

func TestSalePlatformMilestonesAndCancellation(t *testing.T) {
	h := newHarness(t, withPayment)
	o, l := h.saleListing("71 Escrow Way")
	c := customer()
	off := h.offer(c, l, 5000000000)
	s := h.sale(o, h.offerSale(off.ID))
	spath := "/transactions/sales/" + s.ID.String()
	h.expect(h.post(o, spath+"/offers/"+off.ID.String()+"/decision", map[string]any{"decision": "ACCEPT", "settlement": "PLATFORM", "reservation_minor": 6000000000}), 400, "INVALID_REQUEST")
	h.expect(h.post(o, spath+"/offers/"+off.ID.String()+"/decision", map[string]any{"decision": "ACCEPT", "settlement": "PLATFORM", "reservation_minor": 500000000}), 200, "")
	s = h.sale(o, s.ID)
	if len(s.Milestones) != 5 || s.Milestones[0].Code != "RESERVATION_DEPOSIT" || *s.Milestones[0].AmountMinor != 500000000 || *s.Milestones[2].AmountMinor != 4500000000 {
		t.Fatalf("milestones %+v", s.Milestones)
	}
	h.expect(h.post(o, spath+"/milestones", map[string]any{"code": "BALANCE_PAYMENT", "action": "EVIDENCE", "evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 422, "PAYMENT_SERVICE_AUTHORITATIVE")
	h.runJobs("sale-offers-and-milestones")
	h.runJobs("sale-offers-and-milestones")
	if h.pay.creates != 2 {
		t.Fatalf("intents %d", h.pay.creates)
	}
	h.pay.settle(s.Milestones[0].ID, 500000000, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("sale-offers-and-milestones")
	s = h.sale(o, s.ID)
	if s.Milestones[0].Status != "SATISFIED" || *s.Milestones[0].AttestedBy != "PAYMENT_SERVICE" || s.Milestones[2].Status != "PENDING" {
		t.Fatalf("after reservation %+v", s.Milestones)
	}
	// Cancelling ends the sale, fails open milestones, cancels their intents and frees the listing.
	h.expect(h.post(o, spath+"/end", map[string]any{"action": "CANCEL", "reason_code": "BUYER_FINANCE_FELL_THROUGH"}), 200, "")
	h.runJobs("sale-offers-and-milestones")
	s = h.sale(o, s.ID)
	if s.Status != "CANCELLED" || s.Milestones[2].Status != "FAILED" {
		t.Fatalf("cancelled %+v", s)
	}
	var st string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT last_payment_status FROM closing_milestones WHERE id=$1`, s.Milestones[2].ID).Scan(&st)
	if st != payment.IntentCancelled {
		t.Fatalf("balance intent %s", st)
	}
	if got := h.listing(o, l.ID); got.Availability.Status != "AVAILABLE" {
		t.Fatalf("availability %s", got.Availability.Status)
	}
	// A fresh sale can start after the previous one ended.
	h.expect(h.post(o, "/transactions/sales", map[string]any{"property_id": l.PropertyID, "currency": "NGN"}), 201, "")
	h.expect(h.post(o, "/transactions/sales", map[string]any{"property_id": l.PropertyID, "currency": "NGN"}), 409, "SALE_ALREADY_OPEN")
}

func (h *harness) offerSale(offerID ids.ID) ids.ID {
	h.t.Helper()
	var id ids.ID
	if err := h.db.Pool.QueryRow(context.Background(), `SELECT sale_id FROM sale_offers WHERE id=$1`, offerID).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func TestCommercialTermsLOIAndLandTerms(t *testing.T) {
	h := newHarness(t, withPayment)
	o := h.register(true)
	h.verifyIdentity(o)
	home := h.createProperty(o, "72 Not Commercial")
	terms := map[string]any{"permitted_uses": []string{"OFFICE", "RETAIL"}, "zoning_class": "C2", "fit_out_allowed": true, "max_fit_out_days": 90,
		"service_charge_by": "TENANT", "insurance_by": "OWNER", "repairs_by": "SHARED",
		"obligations": []map[string]any{{"code": "FIRE_CERT", "description": "Fire safety certificate", "responsible": "OWNER", "first_due": day(h.clock.now(), 30), "recurrence_months": 6}}}
	h.expect(h.write(o, "PUT", "/properties/"+home.ID.String()+"/commercial-terms", 0, terms), 422, "NOT_A_COMMERCIAL_PROPERTY")
	p := h.propertyOf(o, "73 Office Park", "COMMERCIAL", "OFFICE")
	cpath := "/properties/" + p.ID.String() + "/commercial-terms"
	h.expect(h.get(o, cpath), 404, "COMMERCIAL_TERMS_NOT_FOUND")
	h.expect(h.write(o, "PUT", cpath, 0, terms), 200, "")
	h.expect(h.write(o, "PUT", cpath, 0, terms), 409, "VERSION_CONFLICT")
	r := h.write(o, "PUT", cpath, 1, terms)
	h.expect(r, 200, "")
	var ct usecases.CommercialTermsView
	r.data(t, &ct)
	if ct.Version != 2 || ct.Terms.ZoningClass == nil {
		t.Fatalf("terms %+v", ct)
	}
	var cal []usecases.ObligationDate
	h.get(o, cpath+"/calendar?months=12").data(t, &cal)
	if len(cal) != 2 || cal[0].Code != "FIRE_CERT" {
		t.Fatalf("calendar %+v", cal)
	}

	l := h.liveOf(o, p, "COMMERCIAL_LEASE", map[string]any{"permitted_use": "Office", "min_term_months": 12},
		map[string]any{"currency": "NGN", "base_amount_minor": 1200000000, "base_period": "YEAR", "payment_cycle": "ANNUAL"})
	biz := customer()
	loiTerms := map[string]any{"start_date": day(h.clock.now(), 30), "term_months": 24, "currency": "NGN", "rent_minor": 1200000000, "frequency": "ANNUAL",
		"rent_free_months": 6, "commercial": map[string]any{"permitted_use": "Office", "service_charge_by": "TENANT", "insurance_by": "OWNER", "repairs_by": "SHARED"}}
	body := map[string]any{"listing_id": l.ID, "business_name": "Acme Ltd", "business_verification_ref": "bv-123", "terms": loiTerms}
	h.expect(h.asCustomer(biz, "POST", "/commercial-lois", body), 422, "RENT_FREE_INVALID")
	loiTerms["rent_free_months"] = 12
	r = h.asCustomer(biz, "POST", "/commercial-lois", body)
	h.expect(r, 201, "")
	var loi usecases.LOIView
	r.data(t, &loi)
	lpath := "/commercial-lois/" + loi.ID.String()
	counter := map[string]any{}
	for k, v := range loiTerms {
		counter[k] = v
	}
	counter["rent_free_months"] = 0
	delete(counter, "rent_free_months")
	h.expect(h.post(o, lpath+"/response", map[string]any{"decision": "COUNTER", "counter": counter, "note": "No rent-free period"}), 200, "")
	h.expect(h.post(o, lpath+"/response", map[string]any{"decision": "COUNTER", "counter": counter}), 409, "LOI_NOT_AWAITING_YOU")
	h.expect(h.asCustomer(customer(), "POST", lpath+"/customer-response", map[string]any{"decision": "ACCEPT"}), 404, "")
	r = h.asCustomer(biz, "POST", lpath+"/customer-response", map[string]any{"decision": "COUNTER", "counter": loiTerms})
	h.expect(r, 200, "")
	r = h.post(o, lpath+"/response", map[string]any{"decision": "ACCEPT"})
	h.expect(r, 200, "")
	r.data(t, &loi)
	if loi.Status != "CONVERTED" || loi.AgreementID == nil || len(loi.Rounds) != 3 {
		t.Fatalf("loi %+v", loi)
	}
	var a usecases.AgreementView
	h.get(o, "/agreements/"+loi.AgreementID.String()).data(t, &a)
	at := a.CurrentVersion.Terms
	if a.Source != "COMMERCIAL_LOI" || a.AgreementKind != "COMMERCIAL_LEASE" || at.RentFreeMonths != 12 || at.Commercial == nil || at.Commercial.PermittedUse != "Office" ||
		at.RentCollection != "PLATFORM" {
		t.Fatalf("commercial lease %+v %+v", a, at)
	}
	if got := h.listing(o, l.ID); got.Availability.Status != "OFFER_ACCEPTED" {
		t.Fatalf("availability %s", got.Availability.Status)
	}
	// Rent-free months produce no obligations; move-in needs nothing paid yet.
	tpl := h.agreementTemplate("COMMERCIAL_LEASE")
	a = h.signOffline(o, biz, h.prepareDraft(o, a, tpl, nil))
	h.post(o, "/agreements/"+a.ID.String()+"/activate", nil).data(t, &a)
	var obl []usecases.RentObligationView
	h.get(o, "/tenancies/"+a.TenancyID.String()+"/rent-obligations").data(t, &obl)
	if len(obl) != 0 {
		t.Fatalf("rent-free period materialized obligations: %+v", obl)
	}
	h.expect(h.post(o, "/tenancies/"+a.TenancyID.String()+"/move-in", nil), 200, "")

	// Land terms: append-only versions with survey/title evidence.
	land := h.propertyOf(o, "74 Farm Road", "LAND", "FARMLAND")
	lt := map[string]any{"permitted_uses": []string{"AGRICULTURE"}, "lease_type": "AGRICULTURAL", "max_lease_years": 25, "restoration_required": true,
		"restoration_terms": "Return to arable condition", "evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}
	h.expect(h.write(o, "PUT", "/properties/"+p.ID.String()+"/land-terms", 0, lt), 422, "NOT_A_LAND_PROPERTY")
	r = h.write(o, "PUT", "/properties/"+land.ID.String()+"/land-terms", 0, lt)
	h.expect(r, 200, "")
	var lv usecases.LandTermsView
	r.data(t, &lv)
	if lv.Version != 1 || len(lv.Terms.EvidenceMediaIDs) != 1 || !lv.Terms.RestorationRequired {
		t.Fatalf("land terms %+v", lv)
	}
	if _, err := h.db.Pool.Exec(context.Background(), `UPDATE land_terms SET permitted_uses='{}'`); err == nil {
		t.Fatal("land terms must be append-only")
	}
}
