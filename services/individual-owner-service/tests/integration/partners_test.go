package integration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/financing"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/smartaccess"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// fakeFinancing and fakeSmartAccess stand in for partner/device adapters
// that do not exist yet (tests only; never certification evidence).
type fakeFinancing struct {
	mu        sync.Mutex
	decisions []string
}

func (f *fakeFinancing) Options(_ context.Context, req financing.OptionsRequest, _ string) ([]financing.Offer, error) {
	exp := time.Now().Add(72 * time.Hour)
	return []financing.Offer{
		{PartnerRef: "partner-a", ExternalOfferRef: "o-1-" + req.ListingID.String()[30:], ProductType: "RENT_ADVANCE", ExpiresAt: &exp,
			Disclosure: map[string]any{"total_cost_minor": req.PriceMinor * 12, "fee_bps": 250, "term_months": 12}},
		{PartnerRef: "partner-a", ExternalOfferRef: "bad", ProductType: "CRYPTO_LOAN", Disclosure: map[string]any{"x": 1}},
	}, nil
}

func (f *fakeFinancing) Decide(_ context.Context, partnerRef, offerRef, decision, payout, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decisions = append(f.decisions, offerRef+":"+decision+":"+payout)
	return nil
}

type fakeSmartAccess struct {
	mu      sync.Mutex
	issued  map[string]string // credential -> device
	revoked []string
}

func (f *fakeSmartAccess) Provision(_ context.Context, req smartaccess.ProvisionRequest, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issued == nil {
		f.issued = map[string]string{}
	}
	ref := "cred-" + key
	f.issued[ref] = req.DeviceRef
	return ref, nil
}

func (f *fakeSmartAccess) Revoke(_ context.Context, _, cred, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, cred)
	return nil
}

func TestFinancingConsentOffersAndInsurance(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.fin = &fakeFinancing{} })
	o, l := h.liveListing("4 Finance Row")
	fo := "/financing/options?listing_id=" + l.ID.String()
	h.expect(h.get(o, fo), 422, "CONSENT_REQUIRED")
	h.expect(h.post(o, "/financing/consents", map[string]any{"decision": "GRANTED", "policy_version": "2027-01"}), 200, "")
	var offers []usecases.FinanceOfferView
	h.get(o, fo).data(t, &offers)
	if len(offers) != 1 || offers[0].ProductType != "RENT_ADVANCE" || offers[0].Status != "OFFERED" {
		t.Fatalf("offers %+v", offers)
	}
	var disc map[string]any
	_ = json.Unmarshal(offers[0].Disclosure, &disc)
	if disc["term_months"] != float64(12) {
		t.Fatalf("disclosure %s", offers[0].Disclosure)
	}
	h.expect(h.get(h.register(true), fo), 404, "")
	dp := "/financing/offers/" + offers[0].ID.String() + "/decision"
	h.expect(h.post(o, dp, map[string]any{"decision": "ACCEPT"}), 400, "")
	h.expect(h.post(o, dp, map[string]any{"decision": "ACCEPT", "payout_preference": "UPFRONT"}), 200, "")
	h.expect(h.post(o, dp, map[string]any{"decision": "DECLINE"}), 409, "OFFER_NOT_OPEN")
	if len(h.fin.decisions) != 1 {
		t.Fatalf("partner decisions %v", h.fin.decisions)
	}
	h.expect(h.post(o, "/financing/consents", map[string]any{"decision": "WITHDRAWN", "policy_version": "2027-01"}), 200, "")
	h.expect(h.get(o, fo), 422, "CONSENT_REQUIRED")

	// Insurance: no partner → options unavailable; owner-held policies and claims.
	h.expect(h.get(o, "/insurance/options?property_id="+l.PropertyID.String()), 503, "FEATURE_UNAVAILABLE")
	today := h.clock.now()
	pol := map[string]any{"property_id": l.PropertyID, "product_type": "BUILDING", "insurer_name": "Safe Assurance", "policy_ref": "POL-1",
		"coverage_minor": 500000000, "currency": "NGN", "starts_on": dayN(today, -100), "ends_on": dayN(today, 40),
		"document_media_id": h.media.put(o.id, "DOCUMENT", "READY")}
	r := h.post(o, "/insurance/policies", pol)
	h.expect(r, 201, "")
	var p usecases.PolicyView
	r.data(t, &p)
	if p.Status != "ACTIVE" || p.Source != "OWNER_RECORDED" {
		t.Fatalf("policy %+v", p)
	}
	h.expect(h.post(o, "/insurance/policies", pol), 409, "POLICY_EXISTS")
	claim := map[string]any{"policy_id": p.ID, "incident_date": dayN(today, -120), "description": "Storm damage", "claimed_minor": 2000000,
		"evidence_media_ids": []ids.ID{h.media.put(o.id, "IMAGE", "READY")}}
	h.expect(h.post(o, "/insurance/claims", claim), 422, "INCIDENT_OUTSIDE_COVER")
	claim["incident_date"], claim["claimed_minor"] = dayN(today, -2), 900000000
	h.expect(h.post(o, "/insurance/claims", claim), 422, "ABOVE_COVERAGE")
	claim["claimed_minor"] = 2000000
	r = h.post(o, "/insurance/claims", claim)
	h.expect(r, 201, "")
	var c usecases.InsuranceClaimView
	r.data(t, &c)
	cp := "/insurance/claims/" + c.ID.String()
	h.expect(h.write(o, "PATCH", cp, c.Version, map[string]any{"status": "PARTIALLY_APPROVED", "approved_minor": 1500000}), 422, "EVIDENCE_REQUIRED")
	letter := []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}
	h.expect(h.write(o, "PATCH", cp, c.Version, map[string]any{"status": "APPROVED", "approved_minor": 1500000, "evidence_media_ids": letter}), 400, "")
	h.expect(h.write(o, "PATCH", cp, c.Version, map[string]any{"status": "PARTIALLY_APPROVED", "approved_minor": 1500000, "evidence_media_ids": letter}), 200, "")
	h.expect(h.write(o, "PATCH", cp, c.Version+1, map[string]any{"status": "PAID", "paid_minor": 1600000, "evidence_media_ids": letter}), 400, "")
	h.expect(h.write(o, "PATCH", cp, c.Version, map[string]any{"status": "PAID", "paid_minor": 1500000, "evidence_media_ids": letter}), 409, "")
	h.expect(h.write(o, "PATCH", cp, c.Version+1, map[string]any{"status": "PAID", "paid_minor": 1500000, "evidence_media_ids": letter}), 200, "")

	// Renewal reminder 30 days ahead, then expiry.
	h.clock.advance(15 * 24 * time.Hour)
	h.runJobs("insurance-renewals")
	h.relogin(o)
	var pols []usecases.PolicyView
	h.get(o, "/insurance/policies").data(t, &pols)
	if pols[0].Status != "EXPIRING" {
		t.Fatalf("expiring %+v", pols[0])
	}
	h.clock.advance(30 * 24 * time.Hour)
	h.runJobs("insurance-renewals")
	h.relogin(o)
	h.get(o, "/insurance/policies").data(t, &pols)
	if pols[0].Status != "EXPIRED" {
		t.Fatalf("expired %+v", pols[0])
	}
}

func TestSmartAccessGrantsLifecycle(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.smart = &fakeSmartAccess{} })
	o, l := h.shortletListing("6 Access Lane")
	lp := "/listings/" + l.ID.String() + "/shortlet"
	h.expect(h.write(o, "PUT", lp+"/settings", 0, shortletSettings("OFF_PLATFORM", true, false)), 200, "")
	now := h.clock.now()
	b := h.requestBooking(customer(), bookingBody(l, dayN(now, 2), dayN(now, 4), "Ngozi"))

	r := h.post(o, "/access/devices", map[string]any{"property_id": l.PropertyID, "device_type": "LOCKBOX", "label": "Front lockbox"})
	h.expect(r, 201, "")
	var box usecases.AccessDeviceView
	r.data(t, &box)
	h.expect(h.post(o, "/access/devices", map[string]any{"property_id": l.PropertyID, "device_type": "SMART_LOCK", "label": "Door"}), 400, "")
	r = h.post(o, "/access/devices", map[string]any{"property_id": l.PropertyID, "device_type": "SMART_LOCK", "label": "Door", "provider_device_ref": "lock-77"})
	h.expect(r, 201, "")
	var lock usecases.AccessDeviceView
	r.data(t, &lock)

	checkIn, _ := time.Parse(time.DateOnly, dayN(now, 2))
	checkOut, _ := time.Parse(time.DateOnly, dayN(now, 4))
	grant := map[string]any{"device_id": box.ID, "relation_type": "shortlet_booking", "relation_id": b.ID, "starts_at": checkIn.Add(-time.Hour), "ends_at": checkOut}
	h.expect(h.post(o, "/access/grants", grant), 422, "OUTSIDE_RELATION_PERIOD")
	grant["starts_at"] = checkIn.Add(14 * time.Hour)
	r = h.post(o, "/access/grants", grant)
	h.expect(r, 201, "")
	var g usecases.GrantView
	r.data(t, &g)
	if g.Status != "APPROVED" || g.Purpose != "GUEST" || g.PrincipalRef != b.CustomerRefID {
		t.Fatalf("grant %+v", g)
	}
	gp := "/access/grants/" + g.ID.String()
	h.expect(h.post(o, gp+"/actions", map[string]any{"action": "ISSUE", "note": "Lockbox code shared by phone"}), 200, "")

	// Smart lock: the provider issues the credential; the owner never sees it.
	grant["device_id"] = lock.ID
	r = h.post(o, "/access/grants", grant)
	h.expect(r, 201, "")
	var sg usecases.GrantView
	r.data(t, &sg)
	h.expect(h.post(o, "/access/grants/"+sg.ID.String()+"/actions", map[string]any{"action": "ISSUE"}), 409, "PROVIDER_ISSUES_CREDENTIAL")
	h.runJobs("smart-access-expiry")
	var list []usecases.GrantView
	h.get(o, "/access/grants?property_id="+l.PropertyID.String()+"&status=ISSUED").data(t, &list)
	if len(list) != 2 {
		t.Fatalf("issued %+v", list)
	}
	for _, x := range list {
		if x.ID == sg.ID && !x.CredentialIssued {
			t.Fatalf("smart credential %+v", x)
		}
	}

	// Activation at check-in; a compromised credential is revoked at once.
	h.clock.advance(2*24*time.Hour + 15*time.Hour)
	h.runJobs("smart-access-expiry")
	h.relogin(o)
	h.expect(h.post(o, "/access/grants/"+sg.ID.String()+"/actions", map[string]any{"action": "REVOKE", "compromised": true}), 400, "")
	h.expect(h.post(o, "/access/grants/"+sg.ID.String()+"/actions", map[string]any{"action": "REVOKE", "compromised": true, "reason_code": "CODE_LEAKED"}), 200, "")
	h.get(o, "/access/grants?property_id="+l.PropertyID.String()).data(t, &list)
	for _, x := range list {
		if x.ID == sg.ID && x.Status != "COMPROMISED" || x.ID == g.ID && x.Status != "ACTIVE" {
			t.Fatalf("after compromise %+v", list)
		}
	}
	h.smart.mu.Lock()
	revoked := len(h.smart.revoked)
	h.smart.mu.Unlock()
	if revoked != 1 || h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='access.grant.compromised'`, sg.ID) != 1 {
		t.Fatalf("compromise handling revoked=%d", revoked)
	}

	// Managers: requests need the owner's remote approval; no self-grants;
	// revoking the mandate ends the manager's access immediately.
	mgr := &serviceauth.Delegation{ActorDomain: platform.DomainPropertyManager, ActorID: ids.New()}
	r = h.post(o, "/manager-proposals", map[string]any{"property_id": l.PropertyID, "manager_domain": "property-manager", "manager_ref_id": mgr.ActorID,
		"manager_display_name": "Keys Ltd", "role_type": "FULL_MANAGEMENT", "fee_type": "NONE", "capabilities": []string{"property.view", "access.grant"}})
	h.expect(r, 201, "")
	var pr usecases.ProposalView
	r.data(t, &pr)
	at := h.clock.now()
	r = h.post(o, "/manager-assignments", map[string]any{"proposal_id": pr.ID, "starts_at": at, "ends_at": at.Add(60 * 24 * time.Hour), "currency": "NGN"})
	h.expect(r, 201, "")
	var as usecases.AssignmentView
	r.data(t, &as)
	h.expect(h.do(call{method: "POST", path: internal + "/manager-assignments/" + as.ID.String() + "/respond", as: platform.PropertyManagerService,
		delegate: mgr, key: key(), body: map[string]any{"decision": "ACCEPT"}}), 200, "")
	managed := func(body map[string]any) result {
		return h.do(call{method: "POST", path: internal + "/managed/access/grants", as: platform.PropertyManagerService, delegate: mgr, key: key(), body: body})
	}
	h.expect(managed(map[string]any{"device_id": box.ID, "relation_type": "manager_assignment", "relation_id": as.ID, "starts_at": at, "ends_at": at.Add(time.Hour)}), 403, "")
	r = managed(map[string]any{"device_id": box.ID, "relation_type": "shortlet_booking", "relation_id": b.ID, "starts_at": at, "ends_at": at.Add(2 * time.Hour)})
	h.expect(r, 201, "")
	var req usecases.GrantView
	r.data(t, &req)
	if req.Status != "REQUESTED" || req.RequestedBy != "MANAGER" {
		t.Fatalf("manager request %+v", req)
	}
	h.expect(h.post(o, "/access/grants/"+req.ID.String()+"/actions", map[string]any{"action": "APPROVE"}), 200, "")
	r = h.post(o, "/access/grants", map[string]any{"device_id": box.ID, "relation_type": "manager_assignment", "relation_id": as.ID, "starts_at": at,
		"ends_at": at.Add(30 * 24 * time.Hour)})
	h.expect(r, 201, "")
	var mg usecases.GrantView
	r.data(t, &mg)
	h.expect(h.post(o, "/manager-assignments/"+as.ID.String()+"/revoke", map[string]any{"reason_code": "OWNER_DECISION"}), 200, "")
	h.get(o, "/access/grants?status=REVOKED").data(t, &list)
	if len(list) != 1 || list[0].ID != mg.ID || *list[0].EndReason != "MANDATE_REVOKED" {
		t.Fatalf("mandate revocation %+v", list)
	}

	// Check-out ends the guest's access; the request lapses at its window end.
	h.expect(h.write(o, "PATCH", "/shortlet/bookings/"+b.ID.String(), h.booking(o, b.ID).Version, map[string]any{"action": "CHECK_IN"}), 200, "")
	h.expect(h.write(o, "PATCH", "/shortlet/bookings/"+b.ID.String(), h.booking(o, b.ID).Version, map[string]any{"action": "CHECK_OUT"}), 200, "")
	h.runJobs("smart-access-expiry")
	h.get(o, "/access/grants?status=REVOKED").data(t, &list)
	ended := map[ids.ID]string{}
	for _, x := range list {
		ended[x.ID] = *x.EndReason
	}
	if ended[g.ID] != "RELATION_ENDED" || ended[req.ID] != "RELATION_ENDED" {
		t.Fatalf("relation ended %+v", list)
	}
	var hist struct {
		Data []usecases.AccessEventView `json:"data"`
	}
	_ = json.Unmarshal(h.get(o, "/access/history?grant_id="+g.ID.String()).body, &hist)
	types := map[string]bool{}
	for _, e := range hist.Data {
		types[e.EventType] = true
	}
	if !types["KEY_HANDED_OVER"] || !types["ACTIVATED"] || !types["REVOKED"] {
		t.Fatalf("history %+v", hist.Data)
	}
	if h.outboxTypes(g.ID)["access.grant.revoked.v1"] != 1 || h.outboxTypes(sg.ID)["access.grant.revoked.v1"] != 1 {
		t.Fatal("revocation events")
	}
	h.validateEvents(g.ID)
	h.validateEvents(sg.ID)
}
