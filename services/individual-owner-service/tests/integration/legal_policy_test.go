package integration

import (
	"context"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func legalRules(depositSilence, autoLateFees bool) map[string]any {
	return map[string]any{"damage_claim_response_hours": 48, "deposit_response_days": 10, "deposit_release_days_max": 30,
		"deposit_silence_acceptance": depositSilence, "automatic_late_fees": autoLateFees, "max_late_fee_bps": 2000,
		"shortlet_approval_hours": 12, "shortlet_payment_hold_minutes": 45}
}

// approvePolicy drafts and approves (by a second operator) a NG policy.
func (h *harness) approvePolicy(region *string, rules map[string]any) usecases.JurisdictionPolicyView {
	h.t.Helper()
	body := map[string]any{"country": "NG", "summary": "Lagos tenancy and short-let rules", "rules": rules}
	if region != nil {
		body["region"] = *region
	}
	r := h.admin("POST", "/admin/jurisdiction-policies", body)
	h.expect(r, 201, "")
	var p usecases.JurisdictionPolicyView
	r.data(h.t, &p)
	r = h.admin("POST", "/admin/jurisdiction-policies/"+p.ID.String()+"/approval", map[string]any{"legal_approval_ref": "LEGAL-OPINION-2026-114"})
	h.expect(r, 200, "")
	r.data(h.t, &p)
	return p
}

// leaseAwaitingDepositResponse runs a platform-held tenancy to proposed
// deposit deductions awaiting the tenant's answer.
func (h *harness) leaseAwaitingDepositResponse(line1 string) (*owner, string, usecases.DepositView) {
	h.t.Helper()
	t := h.t
	o, l := h.liveListing(line1)
	c := customer()
	h.acceptedOffer(o, l, c, day(h.clock.now(), 0)) // This fixture moves in immediately, within the accepted period.
	a := h.agreementOf(o, l.PropertyID, "RENTAL_OFFER")
	a = h.prepareDraft(o, a, h.agreementTemplate("RESIDENTIAL_LEASE"), nil)
	a = h.signOffline(o, c, a)
	h.post(o, "/agreements/"+a.ID.String()+"/activate", nil).data(t, &a)
	tpath := "/tenancies/" + a.TenancyID.String()
	h.runJobs("rent-obligations-arrears", "deposits")
	var obl []usecases.RentObligationView
	h.get(o, tpath+"/rent-obligations").data(t, &obl)
	tn := h.tenancy(o, *a.TenancyID)
	h.pay.settle(obl[0].ID, obl[0].AmountMinor, payment.IntentSucceeded)
	h.pay.settle(tn.Deposit.ID, tn.Deposit.AmountMinor, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("rent-obligations-arrears", "deposits")
	h.expect(h.post(o, tpath+"/move-in", nil), 200, "")
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "START", "move_out_date": *tn.EndDate}), 200, "")
	evidence := h.media.put(o.id, "IMAGE", "READY")
	h.expect(h.post(o, tpath+"/move-out-checklist/items", map[string]any{"items": []map[string]any{{"room": "Bedroom", "item": "Wall", "condition": "DAMAGED",
		"media_ids": []ids.ID{evidence}}}}), 200, "")
	h.expect(h.post(o, tpath+"/move-out-checklist/submit", nil), 200, "")
	h.expect(h.asCustomer(c, "POST", tpath+"/move-out-checklist/customer-response", map[string]any{"decision": "ACKNOWLEDGE"}), 200, "")
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "COMPLETE_INSPECTION"}), 200, "")
	r := h.post(o, tpath+"/deposit/settlement", map[string]any{"deductions": []map[string]any{{"code": "WALL_REPAIR", "description": "Hole in bedroom wall",
		"amount_minor": 10000000, "media_ids": []ids.ID{evidence}}}})
	h.expect(r, 200, "")
	var dep usecases.DepositView
	r.data(t, &dep)
	if dep.Status != "DEDUCTION_PROPOSED" || dep.ResponseDueAt == nil {
		t.Fatalf("proposal %+v", dep)
	}
	return o, tpath, dep
}

func TestJurisdictionPolicyAdministration(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "80 Statute Street")
	pp := "/properties/" + p.ID.String() + "/legal-policy"
	var lp usecases.LegalPolicy
	h.get(o, pp).data(t, &lp)
	if lp.Approved || lp.ID != nil || lp.Rules.DepositSilenceAcceptance || lp.Rules.AutomaticLateFees || lp.Rules.DamageClaimResponseHours != 72 {
		t.Fatalf("no approved policy %+v", lp)
	}
	h.expect(h.get(h.register(true), pp), 404, "")

	bad := legalRules(false, false)
	bad["damage_claim_response_hours"] = 1
	h.expect(h.admin("POST", "/admin/jurisdiction-policies", map[string]any{"country": "NG", "summary": "Too short", "rules": bad}), 400, "")

	drafter := adminActor()
	as := func(d *serviceauth.Delegation, path string, body any) result {
		return h.do(call{method: "POST", path: internal + path, as: platform.AdminService, delegate: d, key: key(), body: body})
	}
	r := as(drafter, "/admin/jurisdiction-policies", map[string]any{"country": "NG", "summary": "Federal baseline", "rules": legalRules(true, false)})
	h.expect(r, 201, "")
	var v1 usecases.JurisdictionPolicyView
	r.data(t, &v1)
	if v1.Status != "DRAFT" || v1.VersionNo != 1 {
		t.Fatalf("draft %+v", v1)
	}
	// Drafts have no effect; the drafter cannot approve their own draft.
	h.get(o, pp).data(t, &lp)
	if lp.Approved {
		t.Fatal("a draft must not take effect")
	}
	approval := map[string]any{"legal_approval_ref": "LEGAL-OPINION-2026-101"}
	h.expect(as(drafter, "/admin/jurisdiction-policies/"+v1.ID.String()+"/approval", approval), 409, "SECOND_APPROVER_REQUIRED")
	h.expect(as(adminActor(), "/admin/jurisdiction-policies/"+v1.ID.String()+"/approval", map[string]any{}), 400, "")
	h.expect(as(adminActor(), "/admin/jurisdiction-policies/"+v1.ID.String()+"/approval", approval), 200, "")
	h.expect(as(adminActor(), "/admin/jurisdiction-policies/"+v1.ID.String()+"/approval", approval), 409, "POLICY_NOT_DRAFT")
	h.get(o, pp).data(t, &lp)
	if !lp.Approved || lp.ID == nil || *lp.ID != v1.ID || lp.Version != 1 || !lp.Rules.DepositSilenceAcceptance {
		t.Fatalf("country policy %+v", lp)
	}
	if _, err := h.db.Pool.Exec(context.Background(), `UPDATE jurisdiction_policies SET rules='{}' WHERE id=$1`, v1.ID); err == nil {
		t.Fatal("approved rules must be immutable")
	}
	if _, err := h.db.Pool.Exec(context.Background(), `DELETE FROM jurisdiction_policies WHERE id=$1`, v1.ID); err == nil {
		t.Fatal("approved policies must not be deleted")
	}

	// A region-specific version wins over the country-wide one; approving a
	// newer version retires the one it supersedes.
	lagos := "Lagos"
	v2 := h.approvePolicy(&lagos, legalRules(false, true))
	h.get(o, pp).data(t, &lp)
	if lp.ID == nil || *lp.ID != v2.ID || lp.Region == nil || *lp.Region != "LAGOS" || lp.Rules.DepositSilenceAcceptance || !lp.Rules.AutomaticLateFees {
		t.Fatalf("regional policy %+v", lp)
	}
	v3 := h.approvePolicy(&lagos, legalRules(false, false))
	var all []usecases.JurisdictionPolicyView
	h.admin("GET", "/admin/jurisdiction-policies?country=NG", nil).data(t, &all)
	status := map[ids.ID]string{}
	for _, x := range all {
		status[x.ID] = x.Status
	}
	if status[v1.ID] != "APPROVED" || status[v2.ID] != "RETIRED" || status[v3.ID] != "APPROVED" || v3.VersionNo != 2 {
		t.Fatalf("versions %+v", all)
	}
	// Retiring the regional version falls back to the country-wide one.
	h.expect(h.admin("POST", "/admin/jurisdiction-policies/"+v3.ID.String()+"/retire", nil), 200, "")
	h.expect(h.admin("POST", "/admin/jurisdiction-policies/"+v3.ID.String()+"/retire", nil), 409, "POLICY_NOT_APPROVED")
	h.get(o, pp).data(t, &lp)
	if lp.ID == nil || *lp.ID != v1.ID {
		t.Fatalf("fallback %+v", lp)
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE target_type='jurisdiction_policy' AND action IN ('legal_policy.draft','legal_policy.approve','legal_policy.retire')`); n != 8 {
		t.Fatalf("policy audit %d", n)
	}
	h.expect(h.do(call{method: "GET", path: internal + "/admin/jurisdiction-policies", as: platform.MarketplaceService}), 403, "")
}

// Damage claims are never accepted by guest silence; the response window
// comes from the approved policy.
func TestDamageClaimSilenceGoesToReview(t *testing.T) {
	h := newHarness(t)
	o, l := h.shortletListing("81 Silence Close")
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("OFF_PLATFORM", true, false)), 200, "")
	h.approvePolicy(nil, legalRules(true, true))
	now := h.clock.now()
	g := customer()
	b := h.requestBooking(g, bookingBody(l, dayN(now, 1), dayN(now, 2), "Guest"))
	bp := "/shortlet/bookings/" + b.ID.String()
	h.clock.advance(24*time.Hour + 15*time.Hour)
	h.relogin(o)
	h.expect(h.write(o, "PATCH", bp, h.booking(o, b.ID).Version, map[string]any{"action": "CHECK_IN"}), 200, "")
	h.expect(h.write(o, "PATCH", bp, h.booking(o, b.ID).Version, map[string]any{"action": "CHECK_OUT"}), 200, "")
	r := h.post(o, bp+"/damage-claims", map[string]any{"items": []map[string]any{{"description": "Broken lamp", "amount_minor": 2000000,
		"media_ids": []ids.ID{h.media.put(o.id, "IMAGE", "READY")}}}})
	h.expect(r, 201, "")
	var dc usecases.DamageClaimView
	r.data(t, &dc)
	if !dc.RespondBy.Equal(h.clock.now().Add(48 * time.Hour)) {
		t.Fatalf("respond by %s (policy: 48h from %s)", dc.RespondBy, h.clock.now())
	}
	h.clock.advance(49 * time.Hour)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.relogin(o)
	var claims []usecases.DamageClaimView
	h.get(o, bp+"/damage-claims").data(t, &claims)
	if len(claims) != 1 || claims[0].Status != "RESPONSE_OVERDUE" || claims[0].AwardedMinor != nil {
		t.Fatalf("overdue claim %+v", claims)
	}
	if got := h.booking(o, b.ID); got.Status != "CHECKED_OUT" {
		t.Fatalf("booking must stay open for review: %s", got.Status)
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='shortlet.damage_claim.response_overdue'`, b.ID); n != 1 {
		t.Fatalf("overdue audit %d", n)
	}
	// The owner cannot settle an unanswered claim; an operator decides.
	h.expect(h.post(o, "/shortlet/damage-claims/"+dc.ID.String()+"/action", map[string]any{"action": "RECORD_SETTLEMENT",
		"evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 409, "CLAIM_ACTION_NOT_ALLOWED")
	r = h.admin("POST", "/admin/damage-claims/"+dc.ID.String()+"/resolution", map[string]any{"awarded_minor": 500000, "note": "Partial evidence"})
	h.expect(r, 200, "")
	r.data(t, &dc)
	if dc.Status != "RESOLVED" || dc.AwardedMinor == nil || *dc.AwardedMinor != 500000 || h.booking(o, b.ID).Status != "COMPLETED" {
		t.Fatalf("resolved %+v", dc)
	}
}

// Deposit deductions proceed on tenant silence only under an approved
// policy that says so; otherwise they are frozen for manual review.
func TestDepositSilenceNeedsApprovedPolicy(t *testing.T) {
	h := newHarness(t, withPayment)
	o, tpath, dep := h.leaseAwaitingDepositResponse("82 Escrow Row")
	if !dep.ResponseDueAt.Equal(h.clock.now().Add(14 * 24 * time.Hour)) {
		t.Fatalf("operational response window %s", dep.ResponseDueAt)
	}
	h.clock.advance(15 * 24 * time.Hour)
	h.runJobs("deposits")
	h.relogin(o)
	h.get(o, tpath+"/deposit").data(t, &dep)
	if dep.Status != "DISPUTED" || dep.TenantResponse == nil || *dep.TenantResponse != "NO_RESPONSE" || dep.DisputedMinor != dep.DeductionTotalMinor {
		t.Fatalf("no policy: manual review %+v", dep)
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='deposit.response_window_lapsed' AND changes->>'outcome'='MANUAL_REVIEW'`, dep.ID); n != 1 {
		t.Fatalf("manual review audit %d", n)
	}
	// The undisputed refund proceeds; the deductions wait for a decision.
	h.runJobs("deposits")
	done := h.pay.completeReleases()
	if len(done) != 1 || done[0].WithheldMinor != dep.DeductionTotalMinor || done[0].DeductionMinor != 0 {
		t.Fatalf("release with frozen deductions %+v", done)
	}

	// With an approved policy that makes silence acceptance, the lapse applies it.
	h.approvePolicy(nil, legalRules(true, false))
	o2, tpath2, dep2 := h.leaseAwaitingDepositResponse("83 Escrow Row")
	if !dep2.ResponseDueAt.Equal(h.clock.now().Add(10 * 24 * time.Hour)) {
		t.Fatalf("policy response window %s", dep2.ResponseDueAt)
	}
	h.clock.advance(11 * 24 * time.Hour)
	h.runJobs("deposits")
	h.relogin(o2)
	h.get(o2, tpath2+"/deposit").data(t, &dep2)
	if dep2.TenantResponse == nil || *dep2.TenantResponse != "ACCEPTED" || dep2.DisputedMinor != 0 {
		t.Fatalf("policy acceptance %+v", dep2)
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='deposit.response_window_lapsed' AND metadata->>'legal_policy'='APPROVED'`, dep2.ID); n != 1 {
		t.Fatalf("policy audit %d", n)
	}
}

// Agreed late fees are charged automatically only under an approved policy.
func TestAutomaticLateFeesNeedApprovedPolicy(t *testing.T) {
	h := newHarness(t, withPayment)
	h.approvePolicy(nil, legalRules(false, true))
	o, l := h.liveListing("84 Arrears Avenue")
	c := customer()
	h.acceptedOffer(o, l, c, day(h.clock.now(), 1))
	a := h.agreementOf(o, l.PropertyID, "RENTAL_OFFER")
	terms := a.CurrentVersion.Terms
	terms.LateFeeMinor, terms.LateFeeGraceDays, terms.DepositReleaseDays = 5000000, 3, 60
	doc := h.media.put(o.id, "DOCUMENT", "READY")
	tpl := h.agreementTemplate("RESIDENTIAL_LEASE")
	h.expect(h.write(o, "PATCH", "/agreements/"+a.ID.String(), a.Version, map[string]any{"template_id": tpl, "terms": terms, "document_media_id": doc}), 200, "")
	h.get(o, "/agreements/"+a.ID.String()).data(t, &a)
	// The approved policy caps the deposit settlement deadline at 30 days.
	h.expect(h.post(o, "/agreements/"+a.ID.String()+"/submit-review", nil), 422, "DEPOSIT_RELEASE_ABOVE_POLICY")
	terms.DepositReleaseDays = 30
	h.expect(h.write(o, "PATCH", "/agreements/"+a.ID.String(), a.Version, map[string]any{"template_id": tpl, "terms": terms, "document_media_id": doc}), 200, "")
	h.get(o, "/agreements/"+a.ID.String()).data(t, &a)
	a = h.signOffline(o, c, a)
	h.post(o, "/agreements/"+a.ID.String()+"/activate", nil).data(t, &a)
	tpath := "/tenancies/" + a.TenancyID.String()
	h.runJobs("rent-obligations-arrears")
	h.clock.advance(6 * 24 * time.Hour)
	h.runJobs("rent-obligations-arrears")
	h.relogin(o)
	var obl []usecases.RentObligationView
	h.get(o, tpath+"/rent-obligations").data(t, &obl)
	if len(obl) != 2 || obl[1].Kind != "LATE_FEE" || obl[1].AmountMinor != 5000000 {
		t.Fatalf("automatic late fee %+v", obl)
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='rent.late_fee.assessed' AND changes->>'by'='POLICY'`, obl[0].ID); n != 1 {
		t.Fatalf("policy late fee audit %d", n)
	}
	h.expect(h.post(o, "/rent-obligations/"+obl[0].ID.String()+"/late-fee", nil), 409, "LATE_FEE_ALREADY_ASSESSED")
}
