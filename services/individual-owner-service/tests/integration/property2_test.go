package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func (h *harness) completeTransfer(t *testing.T, o, co, buyer *owner, pid string, caseID ids.ID) {
	t.Helper()
	var incoming struct {
		Data []usecases.TransferView `json:"data"`
	}
	_ = json.Unmarshal(h.get(buyer, "/ownership-transfers/incoming").body, &incoming)
	if len(incoming.Data) != 1 || incoming.Data[0].ID != caseID {
		t.Fatalf("incoming %+v", incoming)
	}
	h.expect(h.post(co, "/ownership-transfers/"+caseID.String()+"/accept", nil), 404, "")
	h.expect(h.post(buyer, "/ownership-transfers/"+caseID.String()+"/accept", nil), 200, "")
	deed := h.media.put(o.id, "DOCUMENT", "READY")
	h.expect(h.post(o, "/properties/"+pid+"/ownership-transfer/submit", map[string]any{"documents": []map[string]any{{"media_id": deed, "document_type": "DEED_OF_ASSIGNMENT"}}}), 200, "")
	path := "/internal/v1/owner/ownership-transfers/" + caseID.String() + "/decision"
	h.expect(h.do(call{method: "POST", path: path, as: platform.MarketplaceService, key: key(), body: map[string]any{"decision": "APPROVE"}}), 403, "")
	h.expect(h.do(call{method: "POST", path: path, as: platform.AdminService, delegate: adminActor(), key: key(),
		body: map[string]any{"decision": "APPROVE", "note": "Registry search matched"}}), 200, "")

	var bp usecases.PropertyView
	h.get(buyer, "/properties/"+pid).data(t, &bp)
	if bp.MyRole != "OWNER" || bp.Status != "ACTIVE" || bp.Ownership.Status != "DRAFT" {
		t.Fatalf("new owner view %+v %+v", bp, bp.Ownership)
	}
	h.expect(h.get(o, "/properties/"+pid), 404, "PROPERTY_NOT_FOUND")
	h.expect(h.get(co, "/properties/"+pid), 404, "PROPERTY_NOT_FOUND")
	var ov usecases.OwnershipView
	h.get(buyer, "/properties/"+pid+"/ownership").data(t, &ov)
	if len(ov.Previous) != 1 || ov.Previous[0].Status != "TRANSFERRED" || ov.Transfer == nil || ov.Transfer.Status != "COMPLETED" {
		t.Fatalf("ownership after transfer %+v", ov)
	}
}

func TestInternalPublicSummaryAdminRestrictionAndCapabilities(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "21 Summary Road")
	h.verifyProperty(o, p.ID)
	path := "/internal/v1/owner/properties/" + p.ID.String()
	r := h.do(call{method: "GET", path: path + "/public-summary", as: platform.MarketplaceService})
	h.expect(r, 200, "")
	var s usecases.PublicSummary
	r.data(t, &s)
	if !s.AuthorityVerified || s.VerificationLevel != "OWNER_AUTHORITY_VERIFIED" || s.Latitude != nil || strings.Contains(string(r.body), "Summary Road") {
		t.Fatalf("summary %s", r.body)
	}
	h.expect(h.do(call{method: "GET", path: path + "/public-summary", as: platform.ArtisanService}), 403, "")
	h.expect(h.do(call{method: "GET", path: path + "/public-summary"}), 401, "")
	h.expect(h.do(call{method: "GET", path: "/internal/v1/owner/properties/" + ids.New().String() + "/public-summary", as: platform.MarketplaceService}), 404, "")

	// Admin restriction: reads still work, writes are blocked, then lifted.
	admin := adminActor()
	h.expect(h.do(call{method: "POST", path: path + "/restrict", as: platform.AdminService, key: key(), body: map[string]any{"reason_code": "FRAUD_REVIEW"}}), 403, "DELEGATED_ACTOR_REQUIRED")
	h.expect(h.do(call{method: "POST", path: path + "/restrict", as: platform.AdminService, delegate: admin, key: key(), body: map[string]any{"reason_code": "FRAUD_REVIEW"}}), 200, "")
	var pv usecases.PropertyView
	h.get(o, "/properties/"+p.ID.String()).data(t, &pv)
	if pv.Status != "RESTRICTED" || pv.RestrictionReason == nil {
		t.Fatalf("restricted view %+v", pv)
	}
	h.expect(h.write(o, "PATCH", "/properties/"+p.ID.String(), pv.Version, map[string]any{"title": "Nope"}), 403, "PROPERTY_RESTRICTED")
	h.expect(h.do(call{method: "POST", path: path + "/lift-restriction", as: platform.AdminService, delegate: admin, key: key(), body: map[string]any{"reason_code": "CLEARED"}}), 200, "")

	// A delegated manager without an assignment has no authority (fail closed).
	mgr := &serviceauth.Delegation{ActorDomain: platform.DomainPropertyManager, ActorID: ids.New()}
	h.expect(h.do(call{method: "GET", path: path + "/public-summary", as: platform.PropertyManagerService, delegate: mgr}), 403, "")

	// Ownership conflicts are reviewable by admins only.
	rival := h.register(true)
	rp := h.createProperty(rival, "21 Summary Road")
	r = h.do(call{method: "GET", path: "/internal/v1/owner/ownership-conflicts", as: platform.AdminService, delegate: admin})
	h.expect(r, 200, "")
	if !strings.Contains(string(r.body), rp.Ownership.RecordID.String()) {
		t.Fatalf("conflicts %s", r.body)
	}
	h.expect(h.do(call{method: "GET", path: "/internal/v1/owner/ownership-conflicts", as: platform.MarketplaceService}), 403, "")
	h.expect(h.do(call{method: "POST", path: "/internal/v1/owner/ownership-records/" + rp.Ownership.RecordID.String() + "/conflict-decision",
		as: platform.AdminService, delegate: admin, key: key(), body: map[string]any{"decision": "REJECT", "reason_code": "DUPLICATE_CLAIM"}}), 200, "")
	var rv usecases.PropertyView
	h.get(rival, "/properties/"+rp.ID.String()).data(t, &rv)
	if rv.Ownership.Status != "REJECTED" {
		t.Fatalf("rival claim %s", rv.Ownership.Status)
	}
	// The rejected claimant may re-claim (a fresh DRAFT record; history kept).
	r = h.post(rival, "/properties/"+rp.ID.String()+"/claim", map[string]any{"claim_type": "LEASEHOLD"})
	h.expect(r, 200, "")
	var ov usecases.OwnershipView
	r.data(t, &ov)
	if ov.Primary.Status != "DRAFT" || ov.Primary.ClaimType != "LEASEHOLD" || len(ov.Previous) != 1 {
		t.Fatalf("re-claim %+v", ov)
	}
}

func TestBankingAndTaxProfile(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.secure(o)
	acct := map[string]any{"bank_code": "058", "bank_name": "GTBank", "account_name": "Ada Owner", "account_number": "0123456789", "currency": "NGN"}
	// payment-service is not implemented: honest 503, nothing stored.
	h.verifyIdentity(o)
	h.expect(h.post(o, "/payout-accounts", acct), 503, "FEATURE_UNAVAILABLE")
	h.expect(h.get(o, "/statements"), 503, "FEATURE_UNAVAILABLE")
	if n := h.count(`SELECT count(*) FROM payout_accounts`); n != 0 {
		t.Fatalf("stored %d", n)
	}
	// Tax profile: step-up, masked storage, If-Match.
	r := h.write(o, "PUT", "/tax-profile", 0, map[string]any{"tax_id_type": "TIN", "tax_id": "12345678-0001", "residency_country": "NG"})
	h.expect(r, 200, "")
	var tp usecases.TaxProfileView
	r.data(t, &tp)
	if tp.TaxIDLast4 == nil || *tp.TaxIDLast4 != "0001" || strings.Contains(string(r.body), "12345678") {
		t.Fatalf("tax %s", r.body)
	}
	if n := h.count(`SELECT count(*) FROM tax_profiles WHERE position('12345678' in coalesce(tax_id_fingerprint,'')) > 0`); n != 0 {
		t.Fatal("raw tax id stored")
	}
	h.clock.advance(10 * time.Minute)
	h.expect(h.write(o, "PUT", "/tax-profile", 1, map[string]any{"residency_country": "GH"}), 403, "STEP_UP_REQUIRED")
}

func TestPayoutAccountsWithPaymentContract(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.pay = &fakePayment{status: "VERIFIED", match: "MATCH"} })
	o := h.register(true)
	h.secure(o)
	acct := map[string]any{"bank_code": "058", "bank_name": "GTBank", "account_name": "Ada Owner", "account_number": "0123456789", "currency": "NGN"}
	h.expect(h.post(o, "/payout-accounts", acct), 422, "IDENTITY_VERIFICATION_REQUIRED")
	h.verifyIdentity(o)
	r := h.post(o, "/payout-accounts", acct)
	h.expect(r, 201, "")
	var pa usecases.PayoutAccountView
	r.data(t, &pa)
	if pa.Status != "COOLING_OFF" || pa.AccountLast4 != "6789" || strings.Contains(string(r.body), "0123456789") {
		t.Fatalf("payout %s", r.body)
	}
	if n := h.count(`SELECT count(*) FROM payout_accounts WHERE position('0123456789' in account_fingerprint) > 0`); n != 0 {
		t.Fatal("raw account number stored")
	}
	h.expect(h.post(o, "/payout-accounts", acct), 409, "PAYOUT_ACCOUNT_EXISTS")
	h.expect(h.post(o, "/payout-accounts/"+pa.ID.String()+"/make-default", nil), 409, "PAYOUT_ACCOUNT_NOT_ACTIVE")
	h.expect(h.write(o, "PUT", "/payout-preferences", 0, map[string]any{"rent_payout_account_id": pa.ID}), 422, "PAYOUT_ACCOUNT_NOT_ACTIVE")

	// Cooling-off completes in the worker; then default + preferences.
	h.clock.advance(49 * time.Hour)
	if n, err := h.app.Service.ActivateCooledPayoutAccounts(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("activate %d %v", n, err)
	}
	_, a := h.login(o, o.device)
	o.apply(a)
	h.expect(h.post(o, "/payout-accounts/"+pa.ID.String()+"/make-default", nil), 200, "")
	h.expect(h.write(o, "PUT", "/payout-preferences", 0, map[string]any{"default_payout_account_id": pa.ID}), 409, "VERSION_CONFLICT")
	var prefs usecases.PayoutPreferencesView
	h.get(o, "/payout-preferences").data(t, &prefs)
	h.expect(h.write(o, "PUT", "/payout-preferences", prefs.Version, map[string]any{"default_payout_account_id": pa.ID, "rent_payout_account_id": pa.ID}), 200, "")
	h.expect(h.do(call{method: "DELETE", path: api + "/payout-accounts/" + pa.ID.String(), token: o.access, key: key()}), 200, "")
	if n := h.count(`SELECT count(*) FROM notification_dispatches WHERE account_id=$1 AND template_key='account.security_alert'`, o.id); n < 3 {
		t.Fatalf("payout alerts %d", n)
	}
}
