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
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

var rentPricing = map[string]any{"currency": "NGN", "base_amount_minor": 360000000, "base_period": "YEAR", "payment_cycle": "ANNUAL",
	"deposit_amount_minor": 50000000, "fees": []map[string]any{
		{"code": "agency_fee", "category": "AGENCY", "label": "Agency fee", "calculation": "PERCENT_OF_BASE", "rate_bps": 1000, "frequency": "ONE_TIME", "mandatory": true, "refundable": false, "payable_to": "THIRD_PARTY"},
		{"code": "service_charge", "category": "SERVICE_CHARGE", "label": "Service charge", "calculation": "FIXED", "amount_minor": 20000000, "frequency": "PER_CYCLE", "mandatory": true, "refundable": false, "payable_to": "OWNER"}}}

// photos links n READY photos to the property and returns their media IDs.
func (h *harness) photos(o *owner, pid ids.ID, n int) []ids.ID {
	h.t.Helper()
	var out []ids.ID
	for i := 0; i < n; i++ {
		m := h.media.put(o.id, "IMAGE", "READY")
		h.expect(h.post(o, "/properties/"+pid.String()+"/media", map[string]any{"media_id": m, "purpose": "PHOTO", "position": i}), 201, "")
		out = append(out, m)
	}
	return out
}

func selection(media []ids.ID) []map[string]any {
	var out []map[string]any
	for i, m := range media {
		out = append(out, map[string]any{"media_id": m, "position": i, "is_cover": i == 0})
	}
	return out
}

// publishableListing returns a verified property and a complete DRAFT rent listing.
func (h *harness) publishableListing(o *owner, line1 string) (usecases.PropertyView, usecases.ListingView) {
	h.t.Helper()
	p := h.createProperty(o, line1)
	h.verifyProperty(o, p.ID)
	media := h.photos(o, p.ID, 3)
	r := h.post(o, "/listings", map[string]any{"property_id": p.ID, "transaction_type": "RENT_LONG_TERM", "title": "Bright 3-bed flat",
		"description": strings.Repeat("Spacious and bright. ", 12), "details": map[string]any{"furnishing": "SEMI_FURNISHED", "min_term_months": 12},
		"media": selection(media)})
	h.expect(r, 201, "")
	var l usecases.ListingView
	r.data(h.t, &l)
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/pricing", 0, rentPricing), 200, "")
	return p, l
}

func (h *harness) listing(o *owner, id ids.ID) usecases.ListingView {
	h.t.Helper()
	var l usecases.ListingView
	r := h.get(o, "/listings/"+id.String())
	h.expect(r, 200, "")
	r.data(h.t, &l)
	return l
}

func (h *harness) outboxTypes(aggregate ids.ID) map[string]int {
	h.t.Helper()
	rows, err := h.db.Pool.Query(context.Background(), `SELECT event_type FROM outbox_events WHERE aggregate_id=$1`, aggregate)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t string
		_ = rows.Scan(&t)
		out[t]++
	}
	return out
}

func (h *harness) validateEvents(aggregate ids.ID) {
	h.t.Helper()
	rows, err := h.db.Pool.Query(context.Background(), `SELECT event_id FROM outbox_events WHERE aggregate_id=$1`, aggregate)
	if err != nil {
		h.t.Fatal(err)
	}
	var list []ids.ID
	for rows.Next() {
		var id ids.ID
		_ = rows.Scan(&id)
		list = append(list, id)
	}
	rows.Close()
	for _, id := range list {
		env, typ := h.outboxEnvelope(id)
		testkit.ValidateJSONSchema(h.t, contracts+typ+".schema.json", env)
	}
}

func TestListingPublicationPricingAndVersions(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p, l := h.publishableListing(o, "30 Listing Lane")
	lpath := "/listings/" + l.ID.String()

	// Identity policy gate first, then every readiness problem is reported.
	h.expect(h.post(o, lpath+"/publish", nil), 422, "IDENTITY_VERIFICATION_REQUIRED")
	h.verifyIdentity(o)
	bare := h.post(o, "/listings", map[string]any{"property_id": p.ID, "transaction_type": "SALE", "title": "House for sale"})
	h.expect(bare, 201, "")
	var bl usecases.ListingView
	bare.data(t, &bl)
	r := h.post(o, "/listings/"+bl.ID.String()+"/publish", nil)
	h.expect(r, 422, "LISTING_NOT_PUBLISHABLE")
	for _, code := range []string{"DETAILS_INCOMPLETE", "MEDIA_INSUFFICIENT", "PRICING_NOT_SET"} {
		if !strings.Contains(string(r.body), code) {
			t.Fatalf("missing %s in %s", code, r.body)
		}
	}
	h.expect(h.post(o, "/listings", map[string]any{"property_id": p.ID, "transaction_type": "LAND_LEASE", "title": "Wrong type"}), 422, "TRANSACTION_TYPE_NOT_ALLOWED")

	// Preview, then publish.
	var pv usecases.PreviewView
	h.do(call{method: "POST", path: api + lpath + "/preview", token: o.access}).data(t, &pv)
	if !pv.Publishable || pv.Snapshot.AllInCost == nil || pv.Snapshot.AllInCost.UpfrontTotal != 360000000+50000000+36000000+20000000 {
		t.Fatalf("preview %+v", pv)
	}
	r = h.post(o, lpath+"/publish", nil)
	h.expect(r, 200, "")
	r.data(t, &l)
	if l.Status != "PUBLISHED" || l.CurrentVersionNo != 1 || l.QualityScore == nil {
		t.Fatalf("published %+v", l)
	}
	// One published listing per property/unit + transaction type.
	r = h.post(o, lpath+"/copy", nil)
	h.expect(r, 201, "")
	var cp usecases.ListingView
	r.data(t, &cp)
	if cp.Pricing == nil || cp.Status != "DRAFT" || len(cp.Media) != 3 {
		t.Fatalf("copy %+v", cp)
	}
	h.expect(h.post(o, "/listings/"+cp.ID.String()+"/publish", nil), 409, "LISTING_ALREADY_PUBLISHED")

	// Editing a published listing emits a new immutable version; an edit that
	// would make it unpublishable is refused.
	h.expect(h.write(o, "PATCH", lpath, l.Version, map[string]any{"details": map[string]any{"furnishing": "FURNISHED"}}), 422, "LISTING_NOT_PUBLISHABLE")
	r = h.write(o, "PATCH", lpath, l.Version, map[string]any{"title": "Bright 3-bed flat with view"})
	h.expect(r, 200, "")
	r.data(t, &l)
	if l.CurrentVersionNo != 2 {
		t.Fatalf("version after edit %d", l.CurrentVersionNo)
	}
	// Price change: new pricing version, republished.
	var sch usecases.ScheduleView
	h.get(o, lpath+"/pricing").data(t, &sch)
	bad := map[string]any{"currency": "NGN", "base_amount_minor": 1, "base_period": "YEAR", "payment_cycle": "ANNUAL",
		"fees": []map[string]any{{"code": "xx_fee", "category": "MYSTERY", "label": "x", "calculation": "FIXED", "amount_minor": 1, "frequency": "ONE_TIME", "mandatory": true, "payable_to": "OWNER"}}}
	h.expect(h.write(o, "PUT", lpath+"/pricing", sch.Version, bad), 422, "FEE_CATEGORY_NOT_CONFIGURED")
	bad["fees"] = []map[string]any{{"code": "xx_fee", "category": "AGENCY", "label": "x", "calculation": "FIXED", "amount_minor": 1, "frequency": "ONE_TIME", "mandatory": true, "payable_to": "PLATFORM"}}
	h.expect(h.write(o, "PUT", lpath+"/pricing", sch.Version, bad), 400, "INVALID_REQUEST")
	h.expect(h.write(o, "PUT", lpath+"/pricing", 0, rentPricing), 409, "VERSION_CONFLICT")
	newPrice := map[string]any{"currency": "NGN", "base_amount_minor": 400000000, "base_period": "YEAR", "payment_cycle": "BIANNUAL"}
	h.expect(h.write(o, "PUT", lpath+"/pricing", sch.Version, newPrice), 200, "")
	l = h.listing(o, l.ID)
	if l.CurrentVersionNo != 3 || l.Pricing.VersionNo != 2 || l.Pricing.PaymentCycle != "BIANNUAL" {
		t.Fatalf("after price change %+v %+v", l.CurrentVersionNo, l.Pricing)
	}
	var hist struct {
		Data []usecases.PricingView `json:"data"`
	}
	_ = json.Unmarshal(h.get(o, lpath+"/price-history").body, &hist)
	if len(hist.Data) != 2 || !hist.Data[0].Active || hist.Data[1].Active {
		t.Fatalf("price history %+v", hist)
	}
	// Accepted pricing versions are frozen at the database level.
	ctx := context.Background()
	old := hist.Data[1].PricingVersionID
	if _, err := h.db.Pool.Exec(ctx, `UPDATE pricing_versions SET accepted_at=now() WHERE id=$1`, old); err != nil {
		t.Fatalf("first acceptance must be recordable: %v", err)
	}
	if _, err := h.db.Pool.Exec(ctx, `UPDATE pricing_versions SET base_amount_minor=1 WHERE id=$1`, old); err == nil {
		t.Fatal("accepted pricing version changed")
	}
	if _, err := h.db.Pool.Exec(ctx, `UPDATE listing_versions SET reason='EDIT' WHERE listing_id=$1`, l.ID); err == nil {
		t.Fatal("listing versions must be immutable")
	}

	// Cost preview (owner) and marketplace read-back.
	var cpv usecases.CostPreviewView
	h.do(call{method: "POST", path: api + lpath + "/cost-preview", token: o.access, body: map[string]any{"term_months": 24}}).data(t, &cpv)
	if cpv.Cost.CycleCount != 4 || cpv.Cost.RecurringPerCycle != 200000000 {
		t.Fatalf("cost preview %+v", cpv.Cost)
	}
	h.expect(h.do(call{method: "POST", path: api + lpath + "/cost-preview", token: o.access, body: map[string]any{"term_months": 6}}), 422, "TERM_BELOW_MINIMUM")
	ir := h.do(call{method: "GET", path: "/internal/v1/owner" + lpath, as: platform.MarketplaceService})
	h.expect(ir, 200, "")
	var iv usecases.InternalListingView
	ir.data(t, &iv)
	if !iv.Bookable || *iv.PublishedVersionNo != 3 || !strings.Contains(string(iv.Snapshot), `"version_no":3`) || strings.Contains(string(ir.body), "Listing Lane") {
		t.Fatalf("read-back %s", ir.body)
	}
	h.expect(h.do(call{method: "GET", path: "/internal/v1/owner/listings/" + bl.ID.String(), as: platform.MarketplaceService}), 404, "")
	h.expect(h.do(call{method: "GET", path: "/internal/v1/owner" + lpath, as: platform.ArtisanService}), 403, "")
	h.expect(h.do(call{method: "POST", path: "/internal/v1/owner" + lpath + "/cost-preview", as: platform.MarketplaceService, body: map[string]any{}}), 200, "")

	// Pause / resume / withdraw with events.
	h.expect(h.post(o, lpath+"/pause", map[string]any{"reason_code": "RENOVATION"}), 200, "")
	h.expect(h.post(o, lpath+"/publish", nil), 200, "")
	// A live listing blocks property archiving and media unlinking.
	h.expect(h.post(o, "/properties/"+p.ID.String()+"/archive", nil), 409, "PROPERTY_HAS_ACTIVE_BUSINESS")
	var links struct {
		Data []usecases.MediaLinkView `json:"data"`
	}
	_ = json.Unmarshal(h.get(o, "/properties/"+p.ID.String()+"/media").body, &links)
	h.expect(h.do(call{method: "DELETE", path: api + "/properties/" + p.ID.String() + "/media/" + links.Data[0].ID.String(), token: o.access, key: key()}), 409, "MEDIA_IN_USE")
	h.expect(h.post(o, lpath+"/withdraw", nil), 200, "")
	types := h.outboxTypes(l.ID)
	for _, want := range []string{"listing.published.v1", "listing.updated.v1", "listing.paused.v1", "listing.withdrawn.v1"} {
		if types[want] == 0 {
			t.Fatalf("missing %s: %v", want, types)
		}
	}
	h.validateEvents(l.ID)
}

func TestListingFreshnessAvailabilityReportsAndModeration(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.verifyIdentity(o)
	_, l := h.publishableListing(o, "31 Fresh Road")
	lpath := "/listings/" + l.ID.String()
	h.expect(h.post(o, lpath+"/publish", nil), 200, "")
	ctx := context.Background()

	// Reminder before due, system pause after due + grace, resume on confirmation.
	h.clock.advance(12 * 24 * time.Hour)
	if rem, paused, err := h.app.Service.RunFreshness(ctx, 50); err != nil || rem != 1 || paused != 0 {
		t.Fatalf("reminders %d %d %v", rem, paused, err)
	}
	if n := h.count(`SELECT count(*) FROM notification_dispatches WHERE template_key=$1`, usecases.TemplateListingFreshness); n != 1 {
		t.Fatalf("reminder dispatches %d", n)
	}
	h.clock.advance(5 * 24 * time.Hour)
	if _, paused, err := h.app.Service.RunFreshness(ctx, 50); err != nil || paused != 1 {
		t.Fatalf("pause %d %v", paused, err)
	}
	_, a := h.login(o, o.device)
	o.apply(a)
	got := h.listing(o, l.ID)
	if got.Status != "PAUSED" || *got.PauseSource != "SYSTEM" || *got.PauseReason != "FRESHNESS_EXPIRED" {
		t.Fatalf("freshness pause %+v", got)
	}
	r := h.post(o, lpath+"/confirm-availability", nil)
	h.expect(r, 200, "")
	r.data(t, &got)
	if got.Status != "PUBLISHED" || got.Availability.LastConfirmedAt == nil {
		t.Fatalf("after confirmation %+v", got)
	}

	// Marketplace report forces an immediate freshness check.
	r = h.do(call{method: "POST", path: "/internal/v1/owner" + lpath + "/reports", as: platform.MarketplaceService, key: key(), body: map[string]any{"report_type": "STALE"}})
	h.expect(r, 200, "")
	if !strings.Contains(string(r.body), "FRESHNESS_CHECK_DUE_NOW") {
		t.Fatalf("report %s", r.body)
	}
	if n := h.count(`SELECT count(*) FROM freshness_checks WHERE listing_id=$1 AND status='PENDING' AND trigger='REPORT'`, l.ID); n != 1 {
		t.Fatalf("report check %d", n)
	}

	// Availability OCCUPIED pauses the published listing.
	var av usecases.AvailabilityView
	h.get(o, lpath+"/availability").data(t, &av)
	h.expect(h.write(o, "PUT", lpath+"/availability", av.Version, map[string]any{"status": "SOLD"}), 400, "INVALID_REQUEST")
	h.expect(h.write(o, "PUT", lpath+"/availability", av.Version, map[string]any{"status": "OCCUPIED"}), 200, "")
	got = h.listing(o, l.ID)
	if got.Status != "PAUSED" || *got.PauseReason != "AVAILABILITY_OCCUPIED" {
		t.Fatalf("occupied %+v", got)
	}
	h.expect(h.post(o, lpath+"/publish", nil), 422, "LISTING_NOT_PUBLISHABLE")
	h.get(o, lpath+"/availability").data(t, &av)
	h.expect(h.write(o, "PUT", lpath+"/availability", av.Version, map[string]any{"status": "AVAILABLE", "available_from": "2026-12-01"}), 200, "")
	h.expect(h.post(o, lpath+"/publish", nil), 200, "")

	// Admin moderation: an admin pause cannot be lifted by the owner.
	admin := adminActor()
	mod := func(action string) result {
		return h.do(call{method: "POST", path: "/internal/v1/owner" + lpath + "/moderation", as: platform.AdminService, delegate: admin, key: key(),
			body: map[string]any{"action": action, "reason_code": "MISLEADING_PHOTOS"}})
	}
	h.expect(mod("PAUSE"), 200, "")
	h.expect(h.post(o, lpath+"/publish", nil), 409, "LISTING_ADMIN_PAUSED")
	h.expect(mod("RELEASE"), 200, "")
	h.expect(h.post(o, lpath+"/publish", nil), 200, "")

	// Losing verified authority pauses published listings automatically.
	var ov usecases.OwnershipView
	h.get(o, "/properties/"+got.PropertyID.String()+"/ownership").data(t, &ov)
	c, _ := h.verif.Get(ctx, *ov.Primary.VerificationID)
	c.Status = "EXPIRED"
	if err := h.app.Service.ApplyVerificationEvent(ctx, verificationEvent(t, c)); err != nil {
		t.Fatal(err)
	}
	got = h.listing(o, l.ID)
	if got.Status != "PAUSED" || *got.PauseReason != "AUTHORITY_REVERIFICATION_REQUIRED" {
		t.Fatalf("authority lost %+v", got)
	}
	h.validateEvents(l.ID)
}

func TestMediaDeletionPausesListing(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.verifyIdentity(o)
	_, l := h.publishableListing(o, "32 Media Way")
	h.expect(h.post(o, "/listings/"+l.ID.String()+"/publish", nil), 200, "")
	m, _ := h.media.Get(context.Background(), l.Media[0].MediaID)
	if err := h.app.Service.ApplyMediaEvent(context.Background(), mediaEvent(t, m, "DELETED")); err != nil {
		t.Fatal(err)
	}
	got := h.listing(o, l.ID)
	if got.Status != "PAUSED" || *got.PauseReason != "MEDIA_DELETED" {
		t.Fatalf("media deletion %+v", got)
	}
}

func TestManagerDelegationLifecycleAndCapabilityEnforcement(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.verifyIdentity(o)
	p, l := h.publishableListing(o, "40 Manager Street")
	h.expect(h.post(o, "/listings/"+l.ID.String()+"/publish", nil), 200, "")
	mgrID := ids.New()
	mgr := &serviceauth.Delegation{ActorDomain: platform.DomainPropertyManager, ActorID: mgrID}
	other := &serviceauth.Delegation{ActorDomain: platform.DomainPropertyManager, ActorID: ids.New()}
	managed := "/internal/v1/owner/managed/properties/" + p.ID.String()
	h.expect(h.do(call{method: "GET", path: managed, as: platform.PropertyManagerService, delegate: mgr}), 404, "PROPERTY_NOT_FOUND")

	proposal := map[string]any{"property_id": p.ID, "manager_domain": "property-manager", "manager_ref_id": mgrID, "manager_display_name": "Prime Managers",
		"role_type": "LETTING_ONLY", "fee_type": "PERCENT_OF_RENT", "fee_bps": 1000}
	proposal["capabilities"] = []string{"payout.manage"}
	h.expect(h.post(o, "/manager-proposals", proposal), 400, "INVALID_REQUEST")
	proposal["capabilities"] = []string{"maintenance.approve"}
	h.expect(h.post(o, "/manager-proposals", proposal), 400, "INVALID_REQUEST")
	proposal["capabilities"] = []string{"property.view", "listing.manage", "listing.pause"}
	r := h.post(o, "/manager-proposals", proposal)
	h.expect(r, 201, "")
	var pr usecases.ProposalView
	r.data(t, &pr)
	now := h.clock.now()
	r = h.post(o, "/manager-assignments", map[string]any{"proposal_id": pr.ID, "starts_at": now, "ends_at": now.Add(90 * 24 * time.Hour),
		"spend_limit_minor": 5000000, "emergency_limit_minor": 1000000, "currency": "NGN"})
	h.expect(r, 201, "")
	var as usecases.AssignmentView
	r.data(t, &as)
	if as.Status != "PROPOSED" || len(as.Permissions.Capabilities) != 3 {
		t.Fatalf("assignment %+v", as)
	}
	apath := "/internal/v1/owner/manager-assignments/" + as.ID.String()
	// Not yet accepted: no authority. Another manager cannot respond.
	h.expect(h.do(call{method: "GET", path: managed, as: platform.PropertyManagerService, delegate: mgr}), 404, "")
	h.expect(h.do(call{method: "POST", path: apath + "/respond", as: platform.PropertyManagerService, delegate: other, key: key(), body: map[string]any{"decision": "ACCEPT"}}), 404, "")
	h.expect(h.do(call{method: "POST", path: apath + "/respond", as: platform.MarketplaceService, key: key(), body: map[string]any{"decision": "ACCEPT"}}), 403, "")
	r = h.do(call{method: "POST", path: apath + "/respond", as: platform.PropertyManagerService, delegate: mgr, key: key(), body: map[string]any{"decision": "ACCEPT"}})
	h.expect(r, 200, "")
	r.data(t, &as)
	if as.Status != "ACTIVE" {
		t.Fatalf("after acceptance %s", as.Status)
	}

	// Explicit capabilities: view, read listing, pause — but not availability.
	h.expect(h.do(call{method: "GET", path: managed, as: platform.PropertyManagerService, delegate: mgr}), 200, "")
	lm := "/internal/v1/owner/managed/listings/" + l.ID.String()
	h.expect(h.do(call{method: "GET", path: lm, as: platform.PropertyManagerService, delegate: mgr}), 200, "")
	h.expect(h.do(call{method: "POST", path: lm + "/confirm-availability", as: platform.PropertyManagerService, delegate: mgr, key: key()}), 403, "PROPERTY_AUTHORITY_REQUIRED")
	h.expect(h.do(call{method: "POST", path: lm + "/pause", as: platform.PropertyManagerService, delegate: mgr, key: key(), body: map[string]any{"reason_code": "VIEWINGS_FULL"}}), 200, "")
	h.expect(h.do(call{method: "GET", path: lm, as: platform.PropertyManagerService, delegate: other}), 404, "")

	// Activity trail: delegated command + manager-reported activity.
	h.expect(h.do(call{method: "POST", path: apath + "/activity", as: platform.PropertyManagerService, delegate: mgr, key: key(),
		body: map[string]any{"activity_type": "INSPECTION_COMPLETED", "summary": "Quarterly inspection"}}), 201, "")
	var act struct {
		Data []usecases.ActivityView `json:"data"`
	}
	_ = json.Unmarshal(h.get(o, "/manager-assignments/"+as.ID.String()+"/activity").body, &act)
	if len(act.Data) != 2 {
		t.Fatalf("activity %+v", act)
	}

	// Widening permissions creates a new immutable version.
	h.get(o, "/manager-assignments/"+as.ID.String()).data(t, &as)
	r = h.write(o, "PATCH", "/manager-assignments/"+as.ID.String(), as.Version, map[string]any{"capabilities": []string{"property.view", "listing.manage", "listing.pause", "availability.manage"}})
	h.expect(r, 200, "")
	r.data(t, &as)
	if as.Permissions.VersionNo != 2 || len(as.PermissionHistory) != 2 {
		t.Fatalf("permissions %+v", as.Permissions)
	}
	h.expect(h.do(call{method: "POST", path: lm + "/confirm-availability", as: platform.PropertyManagerService, delegate: mgr, key: key()}), 200, "")
	ctx := context.Background()
	if _, err := h.db.Pool.Exec(ctx, `UPDATE manager_permissions SET spend_limit_minor=0 WHERE assignment_id=$1`, as.ID); err == nil {
		t.Fatal("permission versions must be append-only")
	}
	if _, err := h.db.Pool.Exec(ctx, `INSERT INTO manager_permissions (id, assignment_id, version_no, capabilities, currency, created_by_account, created_at)
		VALUES ($1,$2,99,ARRAY['payout.manage'],'NGN',$3,now())`, ids.New(), as.ID, o.id); err == nil {
		t.Fatal("payout.manage must be rejected by the database")
	}

	// Revocation is immediate.
	h.expect(h.post(o, "/manager-assignments/"+as.ID.String()+"/revoke", map[string]any{"reason_code": "OWNER_DECISION"}), 200, "")
	h.expect(h.do(call{method: "GET", path: managed, as: platform.PropertyManagerService, delegate: mgr}), 404, "")
	types := h.outboxTypes(as.ID)
	if types["manager.assignment.created.v1"] != 1 || types["manager.assignment.revoked.v1"] != 1 || types["manager.assignment.updated.v1"] < 2 {
		t.Fatalf("assignment events %v", types)
	}
	h.validateEvents(as.ID)
}

func TestManagerMandateExpiryWorker(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "41 Expiry Close")
	mgrID := ids.New()
	r := h.post(o, "/manager-proposals", map[string]any{"property_id": p.ID, "manager_domain": "property-manager", "manager_ref_id": mgrID,
		"manager_display_name": "Short Mandate Ltd", "role_type": "INSPECTION_ONLY", "fee_type": "NONE"})
	h.expect(r, 201, "")
	var pr usecases.ProposalView
	r.data(t, &pr)
	start := h.clock.now().Add(24 * time.Hour)
	r = h.post(o, "/manager-assignments", map[string]any{"proposal_id": pr.ID, "starts_at": start, "ends_at": start.Add(30 * 24 * time.Hour),
		"spend_limit_minor": 0, "emergency_limit_minor": 0, "currency": "NGN"})
	h.expect(r, 201, "")
	var as usecases.AssignmentView
	r.data(t, &as)
	mgr := &serviceauth.Delegation{ActorDomain: platform.DomainPropertyManager, ActorID: mgrID}
	r = h.do(call{method: "POST", path: "/internal/v1/owner/manager-assignments/" + as.ID.String() + "/respond", as: platform.PropertyManagerService,
		delegate: mgr, key: key(), body: map[string]any{"decision": "ACCEPT"}})
	h.expect(r, 200, "")
	r.data(t, &as)
	if as.Status != "ACCEPTED" {
		t.Fatalf("future start %s", as.Status)
	}
	ctx := context.Background()
	step := func(d time.Duration, want string) {
		t.Helper()
		h.clock.advance(d)
		if err := h.app.Service.AdvanceAssignments(ctx, 50); err != nil {
			t.Fatal(err)
		}
		var st string
		_ = h.db.Pool.QueryRow(ctx, `SELECT status FROM manager_assignments WHERE id=$1`, as.ID).Scan(&st)
		if st != want {
			t.Fatalf("after %v: %s want %s", d, st, want)
		}
	}
	step(25*time.Hour, "ACTIVE")
	step(20*24*time.Hour, "EXPIRING")
	step(10*24*time.Hour, "EXPIRED")
	h.expect(h.do(call{method: "GET", path: "/internal/v1/owner/managed/properties/" + p.ID.String(), as: platform.PropertyManagerService, delegate: mgr}), 404, "")
}

// Marketplace may read a file while a live listing uses it, and only then.
func TestListingMediaMarketplaceAccess(t *testing.T) {
	h := newHarness(t)
	o, l := h.liveListing("31 Gallery Road")
	marketplace := func() map[ids.ID]bool {
		h.media.mu.Lock()
		defer h.media.mu.Unlock()
		out := map[ids.ID]bool{}
		for id, gs := range h.media.grants {
			for _, g := range gs {
				if g.GranteeType == "SERVICE" && g.Service == string(platform.MarketplaceService) {
					out[id] = true
				}
			}
		}
		return out
	}
	h.runJobs("listing-media-access")
	got := marketplace()
	if len(got) != len(l.Media) {
		t.Fatalf("granted %d of %d listing files", len(got), len(l.Media))
	}
	for _, m := range l.Media {
		if !got[m.MediaID] {
			t.Fatalf("listing file %s not granted", m.MediaID)
		}
	}
	calls := h.media.policyCalls
	h.runJobs("listing-media-access")
	if h.media.policyCalls != calls {
		t.Fatal("a second run repeated media calls")
	}
	h.expect(h.post(o, "/listings/"+l.ID.String()+"/withdraw", nil), 200, "")
	h.runJobs("listing-media-access")
	if got := marketplace(); len(got) != 0 {
		t.Fatalf("withdrawn listing files still granted: %v", got)
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE action='listing.media.marketplace_access'`); n != 2*len(l.Media) {
		t.Fatalf("access audit %d", n)
	}
}
