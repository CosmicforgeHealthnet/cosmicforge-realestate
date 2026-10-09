package integration

import (
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// closeSale drives an accepted off-platform sale to COMPLETED.
func (h *harness) closeSale(o *owner, spath string, buyer func(path string, body map[string]any) result) {
	h.t.Helper()
	doc := func() []ids.ID { return []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")} }
	for _, to := range []string{"DUE_DILIGENCE", "CONTRACTING", "CONDITIONS_PENDING"} {
		h.expect(h.post(o, spath+"/advance", map[string]any{"to": to}), 200, "")
	}
	for _, code := range []string{"PURCHASE_AGREEMENT", "BALANCE_PAYMENT"} {
		h.expect(h.post(o, spath+"/milestones", map[string]any{"code": code, "action": "EVIDENCE", "evidence_media_ids": doc()}), 200, "")
	}
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "CLOSING"}), 200, "")
	h.expect(buyer(spath+"/customer-action", map[string]any{"action": "CONFIRM_MILESTONE", "milestone_code": "BALANCE_PAYMENT"}), 200, "")
	h.expect(h.post(o, spath+"/milestones", map[string]any{"code": "POSSESSION_HANDOVER", "action": "EVIDENCE", "evidence_media_ids": doc()}), 200, "")
	h.expect(h.post(o, spath+"/advance", map[string]any{"to": "COMPLETED"}), 200, "")
}

// Completing a sale never replaces the owner; authority moves only through
// a SALE ownership-change case with the full transfer guards.
func TestOwnershipTransferAfterSale(t *testing.T) {
	h := newHarness(t)
	o, l := h.saleListing("72 Conveyance Close")
	pid := l.PropertyID.String()
	c := customer()
	off := h.offer(c, l, 7000000000)
	sid := h.offerSale(off.ID)
	spath := "/transactions/sales/" + sid.String()
	h.expect(h.post(o, spath+"/offers/"+off.ID.String()+"/decision", map[string]any{"decision": "ACCEPT"}), 200, "")

	buyer, other, stranger := h.register(true), h.register(true), h.register(true)
	h.verifyIdentity(buyer)
	open := func(email string) result {
		return h.post(o, spath+"/ownership-transfer", map[string]any{"to_email": email, "reason": "Completed sale"})
	}
	h.expect(open(buyer.email), 409, "SALE_NOT_COMPLETED")
	h.expect(h.post(stranger, spath+"/ownership-transfer", map[string]any{"to_email": buyer.email}), 404, "")

	h.closeSale(o, spath, func(path string, body map[string]any) result { return h.asCustomer(c, "POST", path, body) })

	// Completion leaves the owner, the ownership record and the property untouched.
	var pv usecases.PropertyView
	h.get(o, "/properties/"+pid).data(t, &pv)
	if pv.MyRole != "OWNER" || pv.Status != "ACTIVE" || pv.Ownership.Status != "VERIFIED" {
		t.Fatalf("owner after completion %s/%s/%s", pv.MyRole, pv.Status, pv.Ownership.Status)
	}
	if n := h.count(`SELECT count(*) FROM ownership_change_cases WHERE property_id=$1`, l.PropertyID); n != 0 {
		t.Fatalf("sale completion opened %d change cases", n)
	}

	// Guarded: step-up freshness, then idempotent per sale and recipient.
	_, a := h.login(o, o.device)
	o.apply(a)
	h.expect(open(o.email), 422, "CANNOT_TRANSFER_TO_SELF")
	r := open(buyer.email)
	h.expect(r, 201, "")
	var tv usecases.TransferView
	r.data(t, &tv)
	if tv.CaseType != "SALE" || tv.SaleID == nil || *tv.SaleID != sid || tv.Status != "OPEN" {
		t.Fatalf("sale transfer %+v", tv)
	}
	var again usecases.TransferView
	r = open(buyer.email)
	h.expect(r, 201, "")
	r.data(t, &again)
	if again.ID != tv.ID {
		t.Fatalf("retry opened a second case %s != %s", again.ID, tv.ID)
	}
	h.expect(open(other.email), 409, "SALE_TRANSFER_EXISTS")
	h.expect(h.post(o, "/properties/"+pid+"/ownership-transfer", map[string]any{"case_type": "TRANSFER", "to_email": other.email}), 409, "PROPERTY_NOT_TRANSFERABLE")
	if n := h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='ownership.transfer.open' AND changes->>'sale_id'=$2`, tv.ID, sid.String()); n != 1 {
		t.Fatalf("transfer audit %d", n)
	}

	// A cancelled case can be reopened for the sale; history is preserved.
	h.expect(h.post(o, "/properties/"+pid+"/ownership-transfer/cancel", nil), 200, "")
	r = open(buyer.email)
	h.expect(r, 201, "")
	r.data(t, &tv)
	if n := h.count(`SELECT count(*) FROM ownership_change_cases WHERE sale_id=$1`, sid); n != 2 {
		t.Fatalf("sale cases %d", n)
	}
	h.get(o, "/properties/"+pid).data(t, &pv)
	if pv.MyRole != "OWNER" {
		t.Fatalf("owner changed before approval: %s", pv.MyRole)
	}

	// Buyer accepts, evidence is submitted and platform review approves.
	h.completeTransfer(t, o, stranger, buyer, pid, tv.ID)
	var ov usecases.OwnershipView
	h.get(buyer, "/properties/"+pid+"/ownership").data(t, &ov)
	if ov.Transfer == nil || ov.Transfer.CaseType != "SALE" || ov.Transfer.SaleID == nil || *ov.Transfer.SaleID != sid {
		t.Fatalf("ownership transfer %+v", ov.Transfer)
	}
	// The former owner can no longer act on the sale or its property.
	h.expect(open(buyer.email), 404, "")
}
