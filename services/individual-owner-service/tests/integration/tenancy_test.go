package integration

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func withPayment(h *harness) { h.pay = &fakePayment{status: "VERIFIED", match: "MATCH"} }

func (h *harness) admin(method, path string, body any) result {
	h.t.Helper()
	return h.do(call{method: method, path: internal + path, as: platform.AdminService, delegate: adminActor(), key: key(), body: body})
}

// customerMedia registers a file uploaded by a marketplace customer.
func (h *harness) customerMedia(c *serviceauth.Delegation, class string) ids.ID {
	id := ids.New()
	h.media.mu.Lock()
	h.media.files[id] = media.Media{MediaID: id, OwnerDomain: string(platform.DomainMarketplace), OwnerType: "USER", OwnerID: c.ActorID,
		MediaClass: class, Status: "READY", MimeType: "image/jpeg", AccessClassification: "PRIVATE"}
	h.media.mu.Unlock()
	return id
}

func (h *harness) agreementTemplate(kind string, required ...string) ids.ID {
	h.t.Helper()
	var clauses []map[string]any
	for _, c := range required {
		clauses = append(clauses, map[string]any{"code": c, "title": c, "required": true})
	}
	r := h.admin("POST", "/admin/agreement-templates", map[string]any{"code": "NG_" + kind, "country": "NG", "agreement_kind": kind,
		"title": "Standard " + strings.ToLower(kind), "document_media_id": ids.New(), "clauses": clauses})
	h.expect(r, 201, "")
	var v usecases.AgreementTemplateView
	r.data(h.t, &v)
	return v.ID
}

func day(t time.Time, days int) string { return t.AddDate(0, 0, days).Format(time.DateOnly) }

// acceptedOffer runs application → approval → offer → acceptance.
func (h *harness) acceptedOffer(o *owner, l usecases.ListingView, c *serviceauth.Delegation, start string) usecases.OfferView {
	h.t.Helper()
	r := h.asCustomer(c, "POST", "/applications", map[string]any{"listing_id": l.ID, "term_months": 12, "consent": true,
		"participants": []map[string]any{{"role": "PRIMARY", "display_name": "Kemi Tenant"}}})
	h.expect(r, 201, "")
	var a usecases.ApplicationView
	r.data(h.t, &a)
	h.expect(h.post(o, "/applications/"+a.ID.String()+"/decision", map[string]any{"decision": "APPROVED"}), 200, "")
	r = h.post(o, "/applications/"+a.ID.String()+"/offer", map[string]any{"start_date": start, "term_months": 12})
	h.expect(r, 201, "")
	var off usecases.OfferView
	r.data(h.t, &off)
	h.expect(h.post(o, "/offers/"+off.ID.String()+"/send", nil), 200, "")
	h.expect(h.asCustomer(c, "POST", "/offers/"+off.ID.String()+"/response", map[string]any{"decision": "ACCEPT"}), 200, "")
	return off
}

func (h *harness) agreementOf(o *owner, pid ids.ID, source string) usecases.AgreementView {
	h.t.Helper()
	var page struct {
		Data []usecases.AgreementView `json:"data"`
	}
	r := h.get(o, "/agreements?property_id="+pid.String())
	h.expect(r, 200, "")
	_ = json.Unmarshal(r.body, &page)
	for _, a := range page.Data {
		if a.Source == source && a.Status != "CANCELLED" {
			return a
		}
	}
	h.t.Fatalf("no %s agreement in %s", source, r.body)
	return usecases.AgreementView{}
}

// prepareDraft attaches template, clauses and the document to a draft.
func (h *harness) prepareDraft(o *owner, a usecases.AgreementView, tpl ids.ID, clauses []string) usecases.AgreementView {
	h.t.Helper()
	terms := a.CurrentVersion.Terms
	terms.Clauses = clauses
	doc := h.media.put(o.id, "DOCUMENT", "READY")
	r := h.write(o, "PATCH", "/agreements/"+a.ID.String(), a.Version, map[string]any{"template_id": tpl, "terms": terms, "document_media_id": doc})
	h.expect(r, 200, "")
	r.data(h.t, &a)
	return a
}

// signOffline takes a prepared draft through review and offline signing.
func (h *harness) signOffline(o *owner, c *serviceauth.Delegation, a usecases.AgreementView) usecases.AgreementView {
	h.t.Helper()
	apath := "/agreements/" + a.ID.String()
	h.expect(h.post(o, apath+"/submit-review", nil), 200, "")
	h.expect(h.post(o, apath+"/send-for-signature", map[string]any{"method": "OFFLINE"}), 200, "")
	h.get(o, apath).data(h.t, &a)
	for _, p := range a.Participants {
		scan := h.media.put(o.id, "DOCUMENT", "READY")
		h.expect(h.post(o, apath+"/signatures", map[string]any{"participant_id": p.ID, "evidence_media_id": scan, "signed_at": h.clock.now()}), 200, "")
	}
	if c != nil {
		h.expect(h.asCustomer(c, "POST", apath+"/customer-action", map[string]any{"action": "CONFIRM_SIGNATURE"}), 200, "")
	}
	h.get(o, apath).data(h.t, &a)
	if a.Status != "FULLY_SIGNED" {
		h.t.Fatalf("not fully signed: %+v", a.Signatures)
	}
	return a
}

func (h *harness) tenancy(o *owner, id ids.ID) usecases.TenancyView {
	h.t.Helper()
	var v usecases.TenancyView
	r := h.get(o, "/tenancies/"+id.String())
	h.expect(r, 200, "")
	r.data(h.t, &v)
	return v
}

func (h *harness) runJobs(names ...string) {
	h.t.Helper()
	for _, j := range h.app.Service.DomainJobs(usecases.JobConfig{Owner: "test", Batch: 50}) {
		for _, n := range names {
			if j.Name == n {
				if err := j.Run(context.Background()); err != nil {
					h.t.Fatalf("job %s: %v", n, err)
				}
			}
		}
	}
}

func TestMoveInCannotPrecedeAcceptedBusinessPeriod(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "15 Accepted Start Road")
	h.verifyProperty(o, p.ID)
	tpl := h.agreementTemplate("RESIDENTIAL_LEASE")
	r := h.post(o, "/agreements", map[string]any{"property_id": p.ID, "agreement_kind": "RESIDENTIAL_LEASE", "template_id": tpl, "terms": map[string]any{"start_date": day(h.clock.now(), 10), "term_months": 12, "currency": "NGN", "rent_minor": 1000, "frequency": "MONTHLY", "due_day": 1, "rent_collection": "OFF_PLATFORM", "deposit_handling": "NONE"}, "tenant": map[string]any{"display_name": "Period Tenant"}})
	h.expect(r, 201, "")
	var a usecases.AgreementView
	r.data(t, &a)
	a = h.signOffline(o, nil, h.prepareDraft(o, a, tpl, nil))
	h.post(o, "/agreements/"+a.ID.String()+"/activate", nil).data(t, &a)
	h.expect(h.post(o, "/tenancies/"+a.TenancyID.String()+"/move-in", nil), 422, "AGREEMENT_NOT_STARTED")
	h.clock.advance(10 * 24 * time.Hour)
	h.relogin(o)
	h.expect(h.post(o, "/tenancies/"+a.TenancyID.String()+"/move-in", nil), 200, "")
}

func TestRentArrearsUsesPropertyBusinessDate(t *testing.T) {
	h := newHarness(t, withPayment) // Date/projection test only; no real money is certified.
	now := h.clock.now()
	cutover := time.Date(now.Year(), now.Month()+1, 15, 0, 30, 0, 0, time.UTC)
	h.clock.advance(cutover.Sub(now))
	o := h.register(true)
	p := h.createProperty(o, "15 Rent Clock Road")
	h.expect(h.write(o, "PATCH", "/properties/"+p.ID.String(), p.Version, map[string]any{"timezone": "America/Los_Angeles"}), 200, "")
	h.verifyProperty(o, p.ID)
	tpl := h.agreementTemplate("RESIDENTIAL_LEASE")
	r := h.post(o, "/agreements", map[string]any{"property_id": p.ID, "agreement_kind": "RESIDENTIAL_LEASE", "template_id": tpl, "terms": map[string]any{"start_date": day(cutover, -1), "term_months": 12, "currency": "NGN", "rent_minor": 1000, "frequency": "MONTHLY", "due_day": 14, "rent_collection": "PLATFORM", "deposit_handling": "NONE"}, "tenant": map[string]any{"display_name": "Clock Tenant"}})
	h.expect(r, 201, "")
	var a usecases.AgreementView
	r.data(t, &a)
	a = h.signOffline(o, nil, h.prepareDraft(o, a, tpl, nil))
	h.post(o, "/agreements/"+a.ID.String()+"/activate", nil).data(t, &a)
	path := "/tenancies/" + a.TenancyID.String() + "/rent-obligations"
	h.runJobs("rent-obligations-arrears")
	var obligations []usecases.RentObligationView
	h.get(o, path).data(t, &obligations)
	if len(obligations) == 0 || obligations[0].Status == "OVERDUE" {
		t.Fatal("rent became overdue before property midnight")
	}
	h.clock.advance(9 * time.Hour)
	h.relogin(o)
	h.runJobs("rent-obligations-arrears")
	h.get(o, path).data(t, &obligations)
	if obligations[0].Status != "OVERDUE" {
		t.Fatalf("rent not overdue after local midnight: %s", obligations[0].Status)
	}
}

func TestResidentialLeaseLifecycle(t *testing.T) {
	h := newHarness(t, withPayment)
	ctx := context.Background()
	o, l := h.liveListing("60 Tenancy Crescent")
	c := customer()
	start := day(h.clock.now(), 0)
	off := h.acceptedOffer(o, l, c, start)

	// Acceptance prepared the agreement from the frozen price snapshot.
	a := h.agreementOf(o, l.PropertyID, "RENTAL_OFFER")
	terms := a.CurrentVersion.Terms
	if a.Status != "DRAFT" || a.OfferID == nil || *a.OfferID != off.ID || terms.RentMinor != 360000000 || terms.RecurringFeesMinor != 20000000 ||
		terms.Frequency != "ANNUAL" || terms.DepositMinor != 50000000 || terms.RentCollection != "PLATFORM" || terms.DepositHandling != "PLATFORM" || len(a.Participants) != 2 {
		t.Fatalf("prepared agreement %+v terms %+v", a, terms)
	}
	apath := "/agreements/" + a.ID.String()
	h.expect(h.post(o, apath+"/submit-review", nil), 422, "AGREEMENT_TEMPLATE_REQUIRED")
	tpl := h.agreementTemplate("RESIDENTIAL_LEASE", "RENT_PAYMENT")
	terms.Clauses = nil
	h.expect(h.write(o, "PATCH", apath, a.Version, map[string]any{"template_id": tpl, "terms": map[string]any{"start_date": start}}), 400, "")
	a = h.prepareDraft(o, a, tpl, nil)
	h.expect(h.post(o, apath+"/submit-review", nil), 422, "REQUIRED_CLAUSE_MISSING")
	a = h.prepareDraft(o, a, tpl, []string{"RENT_PAYMENT"})
	if a.CurrentVersion.VersionNo != 3 || a.CurrentVersion.TemplateVersionNo == nil {
		t.Fatalf("versions %+v", a.CurrentVersion)
	}
	// Late fees are capped by policy.
	bad := a.CurrentVersion.Terms
	bad.LateFeeMinor = bad.RentMinor
	h.expect(h.write(o, "PATCH", apath, a.Version, map[string]any{"terms": bad}), 422, "LATE_FEE_ABOVE_POLICY")

	// Review: the counterparty sees it and may request changes.
	h.expect(h.post(o, apath+"/submit-review", nil), 200, "")
	h.expect(h.do(call{method: "GET", path: internal + apath + "/customer-view", as: platform.MarketplaceService, delegate: customer()}), 404, "")
	h.expect(h.do(call{method: "GET", path: internal + apath + "/customer-view", as: platform.MarketplaceService, delegate: c}), 200, "")
	h.expect(h.asCustomer(c, "POST", apath+"/customer-action", map[string]any{"action": "REQUEST_CHANGES", "reason": "Fix the due day"}), 200, "")
	h.get(o, apath).data(t, &a)
	if a.Status != "DRAFT" {
		t.Fatalf("after changes requested %s", a.Status)
	}
	h.expect(h.post(o, apath+"/send-for-signature", map[string]any{"method": "OFFLINE"}), 409, "INVALID_STATE_TRANSITION")
	h.expect(h.post(o, apath+"/submit-review", nil), 200, "")
	h.expect(h.post(o, apath+"/send-for-signature", map[string]any{"method": "ESIGN"}), 503, "FEATURE_UNAVAILABLE")
	h.expect(h.post(o, apath+"/send-for-signature", map[string]any{"method": "OFFLINE"}), 200, "")
	h.get(o, apath).data(t, &a)
	h.expect(h.write(o, "PATCH", apath, a.Version, map[string]any{"terms": a.CurrentVersion.Terms}), 409, "AGREEMENT_NOT_EDITABLE")
	if _, err := h.db.Pool.Exec(ctx, `UPDATE agreement_versions SET terms='{}' WHERE agreement_id=$1`, a.ID); err == nil {
		t.Fatal("agreement versions must be immutable")
	}
	var ownerP, tenantP ids.ID
	for _, p := range a.Participants {
		if p.Role == "OWNER" {
			ownerP = p.ID
		} else {
			tenantP = p.ID
		}
	}
	scan := h.media.put(o.id, "DOCUMENT", "READY")
	h.expect(h.post(o, apath+"/signatures", map[string]any{"participant_id": ownerP, "evidence_media_id": ids.New(), "signed_at": h.clock.now()}), 422, "MEDIA_NOT_FOUND")
	h.expect(h.post(o, apath+"/signatures", map[string]any{"participant_id": ownerP, "evidence_media_id": scan, "signed_at": h.clock.now()}), 200, "")
	h.expect(h.post(o, apath+"/signatures", map[string]any{"participant_id": tenantP, "evidence_media_id": scan, "signed_at": h.clock.now()}), 200, "")
	h.get(o, apath).data(t, &a)
	if a.Status != "SIGNING" {
		t.Fatalf("the tenant's signature needs their confirmation: %s", a.Status)
	}
	h.expect(h.post(o, apath+"/activate", nil), 409, "INVALID_STATE_TRANSITION")
	h.expect(h.asCustomer(c, "POST", apath+"/customer-action", map[string]any{"action": "CONFIRM_SIGNATURE"}), 200, "")
	h.get(o, apath).data(t, &a)
	if a.Status != "FULLY_SIGNED" || a.FullySignedAt == nil {
		t.Fatalf("signed %+v", a)
	}

	// Effective → tenancy with schedule and deposit; move-in waits for payment truth.
	r := h.post(o, apath+"/activate", nil)
	h.expect(r, 200, "")
	r.data(t, &a)
	if a.Status != "EFFECTIVE" || a.TenancyID == nil {
		t.Fatalf("activate %+v", a)
	}
	tid := *a.TenancyID
	tpath := "/tenancies/" + tid.String()
	tn := h.tenancy(o, tid)
	if tn.Status != "PENDING_MOVE_IN" || tn.Deposit == nil || tn.Deposit.Status != "REQUIRED" || tn.Outstanding == nil {
		t.Fatalf("tenancy %+v", tn)
	}
	var obl []usecases.RentObligationView
	h.get(o, tpath+"/rent-obligations").data(t, &obl)
	if len(obl) != 1 || obl[0].AmountMinor != 380000000 || obl[0].Status != "SCHEDULED" || obl[0].DueDate != start {
		t.Fatalf("schedule %+v", obl)
	}
	h.expect(h.post(o, tpath+"/move-in", nil), 422, "INITIAL_RENT_UNPAID")
	h.runJobs("rent-obligations-arrears", "deposits")
	h.get(o, tpath+"/rent-obligations").data(t, &obl)
	if obl[0].Status != "DUE" || obl[0].PaymentIntentRef == nil {
		t.Fatalf("due %+v", obl[0])
	}
	h.runJobs("rent-obligations-arrears", "deposits")
	if h.pay.creates != 2 {
		t.Fatalf("payment intents created %d (must be idempotent)", h.pay.creates)
	}
	// Partial payment → PARTIAL; full payment via the event path → PAID.
	ref := h.pay.settle(obl[0].ID, 100000000, payment.IntentPartiallyPaid)
	h.clock.advance(time.Second)
	h.runJobs("rent-obligations-arrears")
	h.get(o, tpath+"/rent-obligations").data(t, &obl)
	if obl[0].Status != "PARTIAL" || obl[0].PaidMinor != 100000000 {
		t.Fatalf("partial %+v", obl[0])
	}
	if err := h.app.Service.ProjectPaymentIntent(ctx, ids.New(), payment.Intent{Ref: ref, Status: payment.IntentSucceeded, AmountMinor: 380000000, PaidMinor: 380000000, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Service.ProjectPaymentIntent(ctx, ids.New(), payment.Intent{Ref: "pi_unknown", Status: payment.IntentSucceeded}); err == nil {
		t.Fatal("an unknown intent must be retried, not dropped")
	}
	h.expect(h.post(o, tpath+"/move-in", nil), 422, "DEPOSIT_NOT_FUNDED")
	h.pay.settle(tn.Deposit.ID, 50000000, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("deposits")
	r = h.post(o, tpath+"/move-in", nil)
	h.expect(r, 200, "")
	r.data(t, &tn)
	if tn.Status != "ACTIVE" || tn.Deposit.Status != "HELD" || tn.Outstanding.OutstandingMinor != 0 {
		t.Fatalf("moved in %+v", tn)
	}
	if got := h.listing(o, l.ID); got.Availability.Status != "OCCUPIED" {
		t.Fatalf("availability %s", got.Availability.Status)
	}
	var hist usecases.RentObligationView
	h.get(o, "/rent-obligations/"+obl[0].ID.String()).data(t, &hist)
	if len(hist.History) != 3 || hist.History[2].Source != "PAYMENT_EVENT" {
		t.Fatalf("obligation history %+v", hist.History)
	}

	// Move-in checklist: owner records, tenant acknowledges → locked; later entries are addenda.
	photo := h.media.put(o.id, "IMAGE", "READY")
	h.expect(h.post(o, tpath+"/move-in-checklist/submit", nil), 422, "CHECKLIST_EMPTY")
	h.expect(h.post(o, tpath+"/move-in-checklist/items", map[string]any{"items": []map[string]any{{"room": "Kitchen", "item": "Hob", "condition": "GOOD", "media_ids": []ids.ID{photo}}}}), 200, "")
	h.expect(h.post(o, tpath+"/move-in-checklist/submit", nil), 200, "")
	h.expect(h.post(o, tpath+"/move-in-checklist/items", map[string]any{"items": []map[string]any{{"room": "Kitchen", "item": "Sink", "condition": "FAIR"}}}), 409, "CHECKLIST_AWAITING_TENANT")
	h.expect(h.asCustomer(c, "POST", tpath+"/move-in-checklist/customer-response", map[string]any{"decision": "ACKNOWLEDGE"}), 200, "")
	r = h.post(o, tpath+"/move-in-checklist/items", map[string]any{"items": []map[string]any{{"room": "Kitchen", "item": "Sink", "condition": "FAIR"}}})
	h.expect(r, 200, "")
	var cl usecases.ChecklistView
	r.data(t, &cl)
	if cl.Status != "ACKNOWLEDGED" || cl.LockedAt == nil || len(cl.Items) != 2 || cl.Items[0].Addendum || !cl.Items[1].Addendum {
		t.Fatalf("checklist %+v", cl)
	}
	if _, err := h.db.Pool.Exec(ctx, `UPDATE condition_items SET condition='POOR' WHERE checklist_id=$1`, cl.ID); err == nil {
		t.Fatal("condition evidence must be immutable")
	}
	r = h.post(o, tpath+"/keys", map[string]any{"item_type": "KEY", "label": "Front door", "quantity": 2})
	h.expect(r, 201, "")
	var k usecases.KeyItemView
	r.data(t, &k)

	// Renewal: explained rent change, tenant counter, owner accepts → addendum.
	h.expect(h.post(o, tpath+"/renewal", map[string]any{"rent_minor": 400000000, "term_months": 12}), 422, "RENT_CHANGE_EXPLANATION_REQUIRED")
	r = h.post(o, tpath+"/renewal", map[string]any{"rent_minor": 400000000, "term_months": 12, "change_explanation": "Market rents in the area rose about 10%.",
		"market_context": map[string]any{"median_rent_minor": 410000000, "comparables": 7}})
	h.expect(r, 201, "")
	var rn usecases.RenewalView
	r.data(t, &rn)
	h.expect(h.post(o, tpath+"/renewal", map[string]any{"rent_minor": 1, "term_months": 12}), 409, "")
	rpath := "/renewals/" + rn.ID.String()
	h.expect(h.post(o, rpath+"/response", map[string]any{"decision": "ACCEPT"}), 409, "RENEWAL_NOT_AWAITING_YOU")
	h.expect(h.asCustomer(c, "POST", rpath+"/customer-response", map[string]any{"decision": "COUNTER",
		"counter": map[string]any{"rent_minor": 380000000, "term_months": 12, "change_explanation": "Please keep the current rent."}}), 200, "")
	r = h.post(o, rpath+"/response", map[string]any{"decision": "ACCEPT"})
	h.expect(r, 200, "")
	r.data(t, &rn)
	if rn.Status != "ACCEPTED" || rn.AddendumAgreementID == nil || len(rn.Rounds) != 2 || h.tenancy(o, tid).Status != "RENEWAL_PENDING" {
		t.Fatalf("renewal %+v", rn)
	}
	var add usecases.AgreementView
	h.get(o, "/agreements/"+rn.AddendumAgreementID.String()).data(t, &add)
	if add.AgreementKind != "RENEWAL_ADDENDUM" || add.CurrentVersion.Terms.RentMinor != 380000000 || add.CurrentVersion.Terms.StartDate != h.clock.now().AddDate(1, 0, 0).Format(time.DateOnly) {
		t.Fatalf("addendum %+v", add.CurrentVersion)
	}
	add = h.prepareDraft(o, add, h.agreementTemplate("RENEWAL_ADDENDUM"), nil)
	add = h.signOffline(o, c, add)
	h.expect(h.post(o, "/agreements/"+add.ID.String()+"/activate", nil), 200, "")
	tn = h.tenancy(o, tid)
	if tn.Status != "ACTIVE" || tn.EndDate == nil || *tn.EndDate != h.clock.now().AddDate(2, 0, -1).Format(time.DateOnly) {
		t.Fatalf("renewed tenancy %+v", tn)
	}

	// Termination notice: legal template required; effective date by policy.
	h.expect(h.post(o, tpath+"/notices", map[string]any{"notice_type": "TERMINATION", "summary": "Notice to quit", "delivery_channel": "PLATFORM"}), 422, "NOTICE_TEMPLATE_REQUIRED")
	r = h.admin("POST", "/admin/notice-templates", map[string]any{"code": "NG_QUIT", "country": "NG", "notice_type": "TERMINATION", "title": "Notice to quit", "min_notice_days": 30})
	h.expect(r, 201, "")
	var nt usecases.NoticeTemplateView
	r.data(t, &nt)
	r = h.post(o, tpath+"/notices", map[string]any{"notice_type": "TERMINATION", "template_id": nt.ID, "summary": "Notice to quit", "delivery_channel": "PLATFORM",
		"requested_effective_date": day(h.clock.now(), 7)})
	h.expect(r, 201, "")
	var n usecases.NoticeView
	r.data(t, &n)
	r = h.post(o, "/notices/"+n.ID.String()+"/issue", nil)
	h.expect(r, 200, "")
	r.data(t, &n)
	if n.Status != "ISSUED" || n.EffectiveDate == nil || *n.EffectiveDate != day(h.clock.now(), 30) {
		t.Fatalf("notice %+v", n)
	}
	h.expect(h.asCustomer(c, "POST", "/notices/"+n.ID.String()+"/customer-action", map[string]any{"action": "RECEIVED"}), 200, "")
	r = h.asCustomer(c, "POST", "/notices/"+n.ID.String()+"/customer-action", map[string]any{"action": "ACKNOWLEDGE"})
	h.expect(r, 200, "")
	r.data(t, &n)
	if n.Status != "ACKNOWLEDGED" || *n.EffectiveDate != day(h.clock.now(), 30) || h.tenancy(o, tid).Status != "TERMINATION_PENDING" {
		t.Fatalf("acknowledged %+v", n)
	}

	// Move-out: inspection, keys, deposit deductions disputed in part.
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "COMPLETE", "return_to_market": true}), 409, "MOVE_OUT_NOT_STARTED")
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "START", "move_out_date": day(h.clock.now(), 30), "notice_id": n.ID}), 200, "")
	dpath := tpath + "/deposit/settlement"
	evidence := h.media.put(o.id, "IMAGE", "READY")
	ded := map[string]any{"deductions": []map[string]any{{"code": "WALL_REPAIR", "description": "Hole in bedroom wall", "amount_minor": 10000000, "media_ids": []ids.ID{evidence}}}}
	h.expect(h.post(o, dpath, ded), 409, "MOVE_OUT_INSPECTION_REQUIRED")
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "COMPLETE_INSPECTION"}), 422, "MOVE_OUT_CHECKLIST_REQUIRED")
	h.expect(h.post(o, tpath+"/move-out-checklist/items", map[string]any{"items": []map[string]any{{"room": "Bedroom", "item": "Wall", "condition": "DAMAGED", "media_ids": []ids.ID{evidence}}}}), 200, "")
	h.expect(h.post(o, tpath+"/move-out-checklist/submit", nil), 200, "")
	// The tenant disputes with their own evidence (owner files are not theirs to use).
	tenantItem := func(m ids.ID) map[string]any {
		return map[string]any{"decision": "DISPUTE", "items": []map[string]any{{"room": "Bedroom", "item": "Wall", "condition": "FAIR", "notes": "Crack was there at move-in",
			"media_ids": []ids.ID{m}}}}
	}
	h.expect(h.asCustomer(c, "POST", tpath+"/move-out-checklist/customer-response", tenantItem(evidence)), 422, "MEDIA_NOT_OWNED")
	h.expect(h.asCustomer(c, "POST", tpath+"/move-out-checklist/customer-response", tenantItem(h.customerMedia(c, "IMAGE"))), 200, "")
	r = h.post(o, tpath+"/move-out-checklist/submit", nil)
	h.expect(r, 200, "")
	r.data(t, &cl)
	if cl.Status != "ACKNOWLEDGED" || len(cl.Items) != 2 || cl.Items[1].RecordedByParty != "TENANT" || !cl.Items[1].Addendum {
		t.Fatalf("disputed move-out checklist %+v", cl)
	}
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "COMPLETE_INSPECTION"}), 200, "")
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "KEYS_RETURNED"}), 422, "KEYS_OUTSTANDING")
	h.expect(h.post(o, tpath+"/keys/"+k.ID.String()+"/return", map[string]any{"returned_quantity": 2}), 200, "")
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "KEYS_RETURNED"}), 200, "")
	h.expect(h.post(o, dpath, map[string]any{"deductions": []map[string]any{{"code": "WALL_REPAIR", "description": "x", "amount_minor": 60000000, "media_ids": []ids.ID{evidence}}}}), 422, "DEDUCTIONS_EXCEED_DEPOSIT")
	r = h.post(o, dpath, ded)
	h.expect(r, 200, "")
	var dep usecases.DepositView
	r.data(t, &dep)
	if dep.Status != "DEDUCTION_PROPOSED" || dep.DeductionTotalMinor != 10000000 {
		t.Fatalf("proposal %+v", dep)
	}
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "COMPLETE", "return_to_market": true}), 422, "DEPOSIT_SETTLEMENT_PENDING")
	r = h.asCustomer(c, "POST", tpath+"/deposit/customer-response", map[string]any{"decision": "DISPUTE", "disputed_minor": 4000000, "reason": "Pre-existing crack"})
	h.expect(r, 200, "")
	r.data(t, &dep)
	if dep.Status != "DISPUTED" || dep.DisputedMinor != 4000000 || !h.tenancy(o, tid).DisputeFlag {
		t.Fatalf("dispute %+v", dep)
	}
	h.runJobs("deposits")
	done := h.pay.completeReleases()
	if len(done) != 1 || done[0].RefundMinor != 40000000 || done[0].DeductionMinor != 6000000 || done[0].WithheldMinor != 4000000 {
		t.Fatalf("undisputed release %+v", done)
	}
	h.clock.advance(time.Second)
	h.runJobs("deposits")
	h.expect(h.post(o, tpath+"/move-out", map[string]any{"action": "COMPLETE"}), 422, "OWNER_CONFIRMATION_REQUIRED")
	r = h.post(o, tpath+"/move-out", map[string]any{"action": "COMPLETE", "return_to_market": true})
	h.expect(r, 200, "")
	tn = h.tenancy(o, tid)
	if tn.Status != "ENDED" || tn.EndedAt == nil {
		t.Fatalf("ended %+v", tn)
	}
	if got := h.listing(o, l.ID); got.Availability.Status != "AVAILABLE" || got.Status != "PAUSED" {
		t.Fatalf("after move-out availability=%s listing=%s", got.Availability.Status, got.Status)
	}
	// An operator resolves the frozen amount; payment-service executes it.
	h.expect(h.do(call{method: "POST", path: internal + "/admin/tenancies/" + tid.String() + "/deposit/resolution", as: platform.AdminService, key: key(),
		body: map[string]any{"owner_awarded_minor": 1000000}}), 403, "")
	h.expect(h.admin("POST", "/admin/tenancies/"+tid.String()+"/deposit/resolution", map[string]any{"owner_awarded_minor": 9000000}), 422, "AWARD_EXCEEDS_DISPUTE")
	h.expect(h.admin("POST", "/admin/tenancies/"+tid.String()+"/deposit/resolution", map[string]any{"owner_awarded_minor": 1000000}), 200, "")
	h.runJobs("deposits")
	done = h.pay.completeReleases()
	if len(done) != 1 || done[0].RefundMinor != 3000000 || done[0].DeductionMinor != 1000000 {
		t.Fatalf("resolution release %+v", done)
	}
	h.clock.advance(time.Second)
	h.runJobs("deposits")
	h.get(o, tpath+"/deposit").data(t, &dep)
	if dep.Status != "PARTIAL_RELEASED" || h.tenancy(o, tid).DisputeFlag {
		t.Fatalf("settled %+v", dep)
	}
	types := h.outboxTypes(tid)
	if types["tenancy.started.v1"] != 1 || types["tenancy.ended.v1"] != 1 {
		t.Fatalf("tenancy events %v", types)
	}
	h.validateEvents(tid)
	// Every transition of every aggregate is in the immutable history.
	if n := h.count(`SELECT count(*) FROM lifecycle_transitions WHERE aggregate_id=$1 AND aggregate_type='tenancy'`, tid); n < 6 {
		t.Fatalf("tenancy transitions %d", n)
	}
}

func TestDirectAgreementOffPlatformAndAuthorization(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.verifyIdentity(o)
	p := h.createProperty(o, "61 Direct Road")
	tpl := h.agreementTemplate("RESIDENTIAL_LEASE")
	start := day(h.clock.now(), -30)
	terms := map[string]any{"start_date": start, "term_months": 12, "currency": "NGN", "rent_minor": 150000000, "frequency": "MONTHLY", "due_day": 1,
		"rent_collection": "PLATFORM", "deposit_handling": "NONE"}
	body := map[string]any{"property_id": p.ID, "agreement_kind": "RESIDENTIAL_LEASE", "template_id": tpl, "terms": terms,
		"tenant": map[string]any{"display_name": "Existing Tenant", "signing_email": "tenant@example.com"}}
	h.expect(h.post(o, "/agreements", body), 503, "FEATURE_UNAVAILABLE")
	terms["rent_collection"] = "OFF_PLATFORM"
	terms["frequency"] = "QUARTERLY"
	terms["term_months"] = 10
	h.expect(h.post(o, "/agreements", body), 422, "TERM_NOT_WHOLE_CYCLES")
	terms["term_months"] = 12
	other := h.register(true)
	h.expect(h.post(other, "/agreements", body), 404, "")
	r := h.post(o, "/agreements", body)
	h.expect(r, 201, "")
	var a usecases.AgreementView
	r.data(t, &a)
	h.expect(h.get(other, "/agreements/"+a.ID.String()), 404, "")
	a = h.prepareDraft(o, a, tpl, nil)
	// Signing a lease needs verified ownership authority.
	h.expect(h.post(o, "/agreements/"+a.ID.String()+"/submit-review", nil), 200, "")
	h.expect(h.post(o, "/agreements/"+a.ID.String()+"/send-for-signature", map[string]any{"method": "OFFLINE"}), 422, "PROPERTY_AUTHORITY_UNVERIFIED")
	h.verifyProperty(o, p.ID)
	h.expect(h.post(o, "/agreements/"+a.ID.String()+"/reopen", nil), 200, "")
	a = h.signOffline(o, nil, h.agreementOf(o, p.ID, "DIRECT"))
	for _, s := range a.Signatures {
		if s.AttestedBy == nil || *s.AttestedBy != "OWNER" || s.EvidenceMediaID == nil {
			t.Fatalf("off-platform signatures are owner-attested with evidence: %+v", s)
		}
	}
	r = h.post(o, "/agreements/"+a.ID.String()+"/activate", nil)
	h.expect(r, 200, "")
	r.data(t, &a)
	tpath := "/tenancies/" + a.TenancyID.String()
	if tn := h.tenancy(o, *a.TenancyID); tn.Outstanding != nil || tn.Deposit != nil || tn.RentCollection != "OFF_PLATFORM" {
		t.Fatalf("off-platform tenancy %+v", tn)
	}
	h.expect(h.post(other, tpath+"/move-in", nil), 404, "")
	h.expect(h.post(o, tpath+"/move-in", nil), 200, "")
	// Off-platform tenants acknowledge the checklist on paper.
	h.expect(h.post(o, tpath+"/move-in-checklist/items", map[string]any{"items": []map[string]any{{"room": "Lounge", "item": "Sofa", "condition": "GOOD"}}}), 200, "")
	h.expect(h.post(o, tpath+"/move-in-checklist/submit", nil), 422, "TENANT_ACK_EVIDENCE_REQUIRED")
	signed := h.media.put(o.id, "DOCUMENT", "READY")
	r = h.post(o, tpath+"/move-in-checklist/submit", map[string]any{"tenant_ack_evidence_media_id": signed})
	h.expect(r, 200, "")
	var cl usecases.ChecklistView
	r.data(t, &cl)
	if cl.Status != "ACKNOWLEDGED" || cl.LockedAt == nil {
		t.Fatalf("checklist %+v", cl)
	}
	h.expect(h.post(o, tpath+"/notices", map[string]any{"notice_type": "GENERAL", "summary": "Water off on Monday", "delivery_channel": "PLATFORM"}), 422, "TENANT_NOT_ON_PLATFORM")
	r = h.post(o, tpath+"/notices", map[string]any{"notice_type": "GENERAL", "summary": "Water off on Monday", "delivery_channel": "EMAIL"})
	h.expect(r, 201, "")
	var n usecases.NoticeView
	r.data(t, &n)
	h.expect(h.post(o, "/notices/"+n.ID.String()+"/delivery", map[string]any{"outcome": "DELIVERED", "delivery_ref": "early"}), 409, "INVALID_STATE_TRANSITION")
	h.expect(h.post(o, "/notices/"+n.ID.String()+"/issue", nil), 200, "")
	h.expect(h.post(o, "/notices/"+n.ID.String()+"/delivery", map[string]any{"outcome": "DELIVERED"}), 422, "DELIVERY_EVIDENCE_REQUIRED")
	h.expect(h.post(o, "/notices/"+n.ID.String()+"/delivery", map[string]any{"outcome": "DELIVERED", "delivery_ref": "smtp-250-ok"}), 200, "")
	// Open tenancies block archiving the property and closing the account.
	h.expect(h.post(o, "/properties/"+p.ID.String()+"/archive", map[string]any{"reason": "Sold"}), 409, "PROPERTY_HAS_ACTIVE_BUSINESS")
	tx, err := h.db.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	holds, err := h.app.Service.ClosureHolds.Holds(context.Background(), tx, o.id)
	if err != nil || len(holds) != 1 || holds[0] != "OPEN_TENANCY" {
		t.Fatalf("closure holds %v %v", holds, err)
	}
}

func TestElectronicSignatureReconciliation(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.esign = newFakeESign(); h.esign.failAfterCreate = true })
	o := h.register(true)
	p := h.createProperty(o, "62 Envelope Street")
	h.verifyProperty(o, p.ID)
	tpl := h.agreementTemplate("COMMERCIAL_LEASE")
	r := h.post(o, "/agreements", map[string]any{"property_id": p.ID, "agreement_kind": "COMMERCIAL_LEASE", "template_id": tpl,
		"terms": map[string]any{"start_date": day(h.clock.now(), 10), "term_months": 24, "currency": "NGN", "rent_minor": 900000000, "frequency": "ANNUAL",
			"due_day": 5, "rent_collection": "OFF_PLATFORM", "deposit_handling": "NONE"},
		"tenant": map[string]any{"display_name": "Acme Ltd"}})
	h.expect(r, 201, "")
	var a usecases.AgreementView
	r.data(t, &a)
	a = h.prepareDraft(o, a, tpl, nil)
	apath := "/agreements/" + a.ID.String()
	h.expect(h.post(o, apath+"/submit-review", nil), 200, "")
	h.expect(h.post(o, apath+"/send-for-signature", map[string]any{"method": "ESIGN"}), 422, "SIGNING_EMAIL_REQUIRED")
	var tenant ids.ID
	for _, pt := range a.Participants {
		if pt.Role == "TENANT" {
			tenant = pt.ID
		}
	}
	h.expect(h.post(o, apath+"/reopen", nil), 200, "")
	h.expect(h.post(o, apath+"/invite-signers", map[string]any{"participants": []map[string]any{
		{"role": "TENANT", "display_name": "Acme Ltd", "party_domain": "external", "signing_email": "legal@acme.example"},
		{"role": "WITNESS", "display_name": "W", "party_domain": "external", "sign_required": false},
		{"participant_id": tenant, "role": "TENANT", "display_name": "x", "party_domain": "external"}}}), 422, "SIGNERS_INCOMPLETE")
	h.get(o, apath).data(t, &a)
	var keep []map[string]any
	for _, pt := range a.Participants {
		if pt.Role == "OWNER" {
			keep = append(keep, map[string]any{"participant_id": pt.ID, "role": "OWNER", "display_name": pt.DisplayName, "party_domain": "individual-owner", "party_ref_id": o.id})
		}
	}
	keep = append(keep, map[string]any{"role": "TENANT", "display_name": "Acme Ltd", "party_domain": "external", "signing_email": "legal@acme.example"})
	h.expect(h.post(o, apath+"/invite-signers", map[string]any{"participants": keep}), 200, "")
	h.expect(h.post(o, apath+"/submit-review", nil), 200, "")
	h.expect(h.post(o, apath+"/send-for-signature", map[string]any{"method": "ESIGN"}), 200, "")
	h.runJobs("signature-reconciliation")
	h.runJobs("signature-reconciliation")
	if h.esign.creates != 1 {
		t.Fatalf("envelopes created %d", h.esign.creates)
	}
	cert := h.media.put(o.id, "DOCUMENT", "READY")
	h.esign.signAll(cert)
	h.runJobs("signature-reconciliation")
	h.get(o, apath).data(t, &a)
	if a.Status != "FULLY_SIGNED" || len(a.Signatures) != 2 || a.Signatures[0].CertificateMediaID == nil || *a.Signatures[0].AttestedBy != "PROVIDER" {
		t.Fatalf("e-signed %+v", a)
	}
}

func TestRentArrearsLateFeesAndWaivers(t *testing.T) {
	h := newHarness(t, withPayment)
	o, l := h.liveListing("63 Arrears Avenue")
	c := customer()
	h.acceptedOffer(o, l, c, day(h.clock.now(), 1))
	a := h.agreementOf(o, l.PropertyID, "RENTAL_OFFER")
	tpl := h.agreementTemplate("RESIDENTIAL_LEASE")
	terms := a.CurrentVersion.Terms
	terms.LateFeeMinor, terms.LateFeeGraceDays = 5000000, 3
	doc := h.media.put(o.id, "DOCUMENT", "READY")
	h.expect(h.write(o, "PATCH", "/agreements/"+a.ID.String(), a.Version, map[string]any{"template_id": tpl, "terms": terms, "document_media_id": doc}), 200, "")
	h.get(o, "/agreements/"+a.ID.String()).data(t, &a)
	a = h.signOffline(o, c, a)
	h.post(o, "/agreements/"+a.ID.String()+"/activate", nil).data(t, &a)
	tpath := "/tenancies/" + a.TenancyID.String()
	h.runJobs("rent-obligations-arrears")
	h.clock.advance(6 * 24 * time.Hour)
	h.runJobs("rent-obligations-arrears")
	_, ar := h.login(o, o.device)
	o.apply(ar)
	var obl []usecases.RentObligationView
	h.get(o, tpath+"/rent-obligations").data(t, &obl)
	// No approved jurisdiction policy: the agreed late fee is never charged
	// automatically; the case is recorded for the owner's decision.
	if len(obl) != 1 || obl[0].Status != "OVERDUE" {
		t.Fatalf("arrears without policy %+v", obl)
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='rent.late_fee.manual_review' AND metadata->>'legal_policy'='NONE_APPROVED'`, obl[0].ID); n != 1 {
		t.Fatalf("manual review audit %d", n)
	}
	h.expect(h.post(o, "/rent-obligations/"+obl[0].ID.String()+"/late-fee", nil), 201, "")
	h.expect(h.post(o, "/rent-obligations/"+obl[0].ID.String()+"/late-fee", nil), 409, "LATE_FEE_ALREADY_ASSESSED")
	h.get(o, tpath+"/rent-obligations").data(t, &obl)
	if len(obl) != 2 || obl[0].Status != "OVERDUE" || obl[1].Kind != "LATE_FEE" || obl[1].AmountMinor != 5000000 {
		t.Fatalf("arrears %+v", obl)
	}
	h.expect(h.post(o, "/rent-obligations/"+obl[1].ID.String()+"/late-fee", nil), 409, "LATE_FEE_NOT_APPLICABLE")
	if n := h.count(`SELECT count(*) FROM notification_dispatches WHERE template_key=$1`, usecases.TemplateRentOverdue); n != 1 {
		t.Fatalf("overdue notifications %d", n)
	}
	h.runJobs("rent-obligations-arrears")
	if n := h.count(`SELECT count(*) FROM rent_obligations WHERE tenancy_id=$1 AND kind='LATE_FEE'`, a.TenancyID); n != 1 {
		t.Fatalf("late fees must not repeat: %d", n)
	}
	// Waiving the late fee cancels its payment request with payment-service.
	h.runJobs("rent-obligations-arrears")
	h.expect(h.post(o, "/rent-obligations/"+obl[1].ID.String()+"/decision", map[string]any{"action": "WAIVE", "reason_code": "GOODWILL"}), 200, "")
	h.runJobs("rent-obligations-arrears")
	var lf usecases.RentObligationView
	h.get(o, "/rent-obligations/"+obl[1].ID.String()).data(t, &lf)
	if lf.Status != "WAIVED" || lf.LastPaymentStatus == nil || *lf.LastPaymentStatus != payment.IntentCancelled {
		t.Fatalf("waived late fee %+v", lf)
	}
	// Dispute and reinstate re-derive the state from payment truth.
	h.expect(h.post(o, "/rent-obligations/"+obl[0].ID.String()+"/decision", map[string]any{"action": "DISPUTE", "reason_code": "AMOUNT_QUERIED"}), 200, "")
	h.pay.settle(obl[0].ID, obl[0].AmountMinor, payment.IntentSucceeded)
	h.clock.advance(time.Second)
	h.runJobs("rent-obligations-arrears")
	h.get(o, "/rent-obligations/"+obl[0].ID.String()).data(t, &lf)
	if lf.Status != "PAID" {
		t.Fatalf("payment resolves the dispute: %+v", lf)
	}
	h.expect(h.post(o, "/rent-obligations/"+obl[0].ID.String()+"/decision", map[string]any{"action": "WAIVE", "reason_code": "X1"}), 409, "OBLIGATION_PARTLY_PAID")
}

func TestConcurrentMoveInAllowsOneOccupancy(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "64 Single Space")
	h.verifyProperty(o, p.ID)
	tpl := h.agreementTemplate("RESIDENTIAL_LEASE")
	var tenancies []ids.ID
	for i := 0; i < 2; i++ {
		r := h.post(o, "/agreements", map[string]any{"property_id": p.ID, "agreement_kind": "RESIDENTIAL_LEASE", "template_id": tpl,
			"terms": map[string]any{"start_date": day(h.clock.now(), 0), "term_months": 12, "currency": "NGN", "rent_minor": 1000, "frequency": "MONTHLY",
				"due_day": 1, "rent_collection": "OFF_PLATFORM", "deposit_handling": "NONE"},
			"tenant": map[string]any{"display_name": "T"}})
		h.expect(r, 201, "")
		var a usecases.AgreementView
		r.data(t, &a)
		a = h.signOffline(o, nil, h.prepareDraft(o, a, tpl, nil))
		h.post(o, "/agreements/"+a.ID.String()+"/activate", nil).data(t, &a)
		tenancies = append(tenancies, *a.TenancyID)
	}
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i, id := range tenancies {
		wg.Add(1)
		go func(i int, id ids.ID) {
			defer wg.Done()
			statuses[i] = h.post(o, "/tenancies/"+id.String()+"/move-in", nil).status
		}(i, id)
	}
	wg.Wait()
	if !((statuses[0] == 200 && statuses[1] == 409) || (statuses[0] == 409 && statuses[1] == 200)) {
		t.Fatalf("concurrent move-ins %v", statuses)
	}
	if n := h.count(`SELECT count(*) FROM tenancies WHERE property_id=$1 AND status='ACTIVE'`, p.ID); n != 1 {
		t.Fatalf("active tenancies %d", n)
	}
	// The losing tenancy can be cancelled before move-in (no money moved).
	for _, id := range tenancies {
		if tn := h.tenancy(o, id); tn.Status == "PENDING_MOVE_IN" {
			h.expect(h.post(o, "/tenancies/"+id.String()+"/cancel", map[string]any{"reason_code": "DOUBLE_BOOKED"}), 200, "")
		}
	}
}

func TestUtilitiesAndServiceCharges(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "65 Meter Mews")
	ppath := "/properties/" + p.ID.String()
	h.expect(h.post(o, ppath+"/utilities", map[string]any{"utility_type": "ELECTRICITY", "provider_name": "IKEDC", "account_ref_last4": "123456",
		"meter_type": "PREPAID", "responsibility": "SHARED"}), 400, "INVALID_REQUEST")
	r := h.post(o, ppath+"/utilities", map[string]any{"utility_type": "ELECTRICITY", "provider_name": "IKEDC", "account_ref_last4": "3456",
		"meter_number": "MTR-1", "meter_type": "POSTPAID", "responsibility": "SHARED"})
	h.expect(r, 201, "")
	var u usecases.UtilityAccountView
	r.data(t, &u)
	upath := "/utilities/" + u.ID.String()
	h.expect(h.post(o, upath+"/allocation-rule", map[string]any{"method": "PERCENTAGE", "shares": []map[string]any{{"party": "FLAT_A", "value": 6000}}}), 400, "")
	h.expect(h.post(o, upath+"/allocation-rule", map[string]any{"method": "PERCENTAGE", "shares": []map[string]any{{"party": "FLAT_A", "value": 6000}, {"party": "FLAT_B", "value": 4000}}}), 200, "")

	// Readings: move-in readings need a meter photo; corrections keep the original.
	h.expect(h.post(o, ppath+"/meter-readings", map[string]any{"utility_account_id": u.ID, "reading_type": "MOVE_IN", "value": 1200.5, "unit": "KWH"}), 422, "METER_PHOTO_REQUIRED")
	photo := h.media.put(o.id, "IMAGE", "READY")
	r = h.post(o, ppath+"/meter-readings", map[string]any{"utility_account_id": u.ID, "reading_type": "MOVE_IN", "value": 1200.5, "unit": "KWH", "media_id": photo})
	h.expect(r, 201, "")
	var mr usecases.MeterReadingView
	r.data(t, &mr)
	h.expect(h.post(o, ppath+"/meter-readings", map[string]any{"utility_account_id": u.ID, "reading_type": "CORRECTION", "value": 1201.5, "unit": "KWH", "corrects_id": mr.ID}), 201, "")
	var readings []usecases.MeterReadingView
	h.get(o, ppath+"/meter-readings").data(t, &readings)
	if len(readings) != 2 || readings[0].Value != 1201.5 || readings[1].Value != 1200.5 || readings[1].RecordedByParty != "OWNER" {
		t.Fatalf("readings %+v", readings)
	}

	// Bill allocation is versioned; disputes keep the original calculation.
	r = h.post(o, ppath+"/utility-bills", map[string]any{"utility_account_id": u.ID, "period_start": "2026-09-01", "period_end": "2026-09-30", "amount_minor": 1000001, "currency": "NGN"})
	h.expect(r, 201, "")
	var b usecases.UtilityBillView
	r.data(t, &b)
	bpath := "/utility-bills/" + b.ID.String() + "/action"
	r = h.write(o, "POST", bpath, b.Version, map[string]any{"action": "ALLOCATE"})
	h.expect(r, 200, "")
	r.data(t, &b)
	if b.Status != "ALLOCATED" || b.Allocation[0].AmountMinor+b.Allocation[1].AmountMinor != 1000001 || *b.AllocationRuleVersion != 1 {
		t.Fatalf("allocated %+v", b)
	}
	r = h.write(o, "POST", bpath, b.Version, map[string]any{"action": "DISPUTE", "reason": "Flat B was vacant"})
	h.expect(r, 200, "")
	r.data(t, &b)
	h.expect(h.post(o, upath+"/allocation-rule", map[string]any{"method": "PERCENTAGE", "shares": []map[string]any{{"party": "FLAT_A", "value": 10000}, {"party": "FLAT_B", "value": 0}}}), 200, "")
	r = h.write(o, "POST", bpath, b.Version, map[string]any{"action": "RESOLVE"})
	h.expect(r, 200, "")
	r.data(t, &b)
	if b.Status != "RESOLVED" || *b.AllocationRuleVersion != 2 || b.Allocation[0].AmountMinor != 1000001 || len(b.OriginalAllocation) != 2 || b.OriginalAllocation[1].AmountMinor == 0 {
		t.Fatalf("resolved %+v", b)
	}

	// Service-charge budget: draft → approved; items need evidence; adjustments keep history.
	r = h.post(o, ppath+"/service-charge-budgets", map[string]any{"period_start": "2027-01-01", "period_end": "2027-12-31", "currency": "NGN",
		"categories": []map[string]any{{"category": "SECURITY", "expected_minor": 1200000}, {"category": "CLEANING", "expected_minor": 600000}}})
	h.expect(r, 201, "")
	var bud usecases.BudgetView
	r.data(t, &bud)
	gpath := "/service-charge-budgets/" + bud.ID.String()
	item := map[string]any{"category": "SECURITY", "item_type": "ACTUAL", "amount_minor": 100000, "description": "January guards", "incurred_on": "2027-01-31"}
	h.expect(h.post(o, gpath+"/items", map[string]any{"category": "SECURITY", "item_type": "BILLED", "amount_minor": 1, "description": "x", "incurred_on": "2027-01-31"}), 409, "BUDGET_NOT_APPROVED")
	body := map[string]any{"period_start": "2027-01-01", "period_end": "2027-12-31", "currency": "NGN", "status": "APPROVED",
		"categories": []map[string]any{{"category": "SECURITY", "expected_minor": 1200000}, {"category": "CLEANING", "expected_minor": 600000}}}
	r = h.write(o, "PATCH", gpath, bud.Version, body)
	h.expect(r, 200, "")
	r.data(t, &bud)
	h.expect(h.post(o, gpath+"/items", item), 422, "EVIDENCE_REQUIRED")
	item["evidence_media_ids"] = []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}
	r = h.post(o, gpath+"/items", item)
	h.expect(r, 201, "")
	var it usecases.ServiceChargeItemView
	r.data(t, &it)
	h.expect(h.post(o, gpath+"/items", map[string]any{"category": "GARDEN", "item_type": "BILLED", "amount_minor": 1, "description": "x", "incurred_on": "2027-01-31"}), 422, "UNKNOWN_BUDGET_CATEGORY")
	h.expect(h.post(o, gpath+"/items", map[string]any{"category": "SECURITY", "item_type": "ADJUSTMENT", "amount_minor": -20000, "description": "Overtime refund",
		"incurred_on": "2027-02-01", "adjusts_id": it.ID}), 201, "")
	h.get(o, gpath).data(t, &bud)
	if bud.Lines[0].ActualMinor != 100000 || bud.Lines[0].AdjustedMinor != -20000 || bud.TotalExpectedMinor != 1800000 {
		t.Fatalf("budget lines %+v", bud.Lines)
	}
	body["categories"] = []map[string]any{{"category": "SECURITY", "expected_minor": 1}}
	h.expect(h.write(o, "PATCH", gpath, bud.Version, body), 409, "BUDGET_NOT_EDITABLE")
	if _, err := h.db.Pool.Exec(context.Background(), `UPDATE service_charge_items SET amount_minor=0 WHERE budget_id=$1`, bud.ID); err == nil {
		t.Fatal("service-charge items must be append-only")
	}
}
