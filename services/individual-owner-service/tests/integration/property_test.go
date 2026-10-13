package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

const api = "/api/v1/owner"

var keySeq int64
var keyMu sync.Mutex

func key() string {
	keyMu.Lock()
	defer keyMu.Unlock()
	keySeq++
	return fmt.Sprintf("k-%d-%s", keySeq, ids.New().String()[24:])
}

func (h *harness) post(o *owner, path string, body any) result {
	h.t.Helper()
	return h.do(call{method: "POST", path: api + path, token: o.access, key: key(), body: body})
}

func (h *harness) get(o *owner, path string) result {
	h.t.Helper()
	return h.do(call{method: "GET", path: api + path, token: o.access})
}

func (h *harness) write(o *owner, method, path string, version int64, body any) result {
	h.t.Helper()
	return h.do(call{method: method, path: api + path, token: o.access, body: body, headers: map[string]string{"If-Match": fmt.Sprintf(`"%d"`, version)}})
}

func address(line1 string) map[string]any {
	return map[string]any{"line1": line1, "city": "Lagos", "state_region": "Lagos", "country_code": "NG", "latitude": 6.4281, "longitude": 3.4219}
}

func (h *harness) createProperty(o *owner, line1 string) usecases.PropertyView {
	h.t.Helper()
	r := h.post(o, "/properties", map[string]any{"category": "RESIDENTIAL", "property_type": "APARTMENT", "title": "Family home",
		"bedrooms": 3, "bathrooms": 2, "address": address(line1), "features": []map[string]any{{"key": "borehole", "category": "UTILITY"}}})
	h.expect(r, 201, "")
	var v usecases.PropertyView
	r.data(h.t, &v)
	return v
}

// verifyIdentity runs an IDENTITY verification to VERIFIED.
func (h *harness) verifyIdentity(o *owner) {
	h.t.Helper()
	doc := h.media.put(o.id, "VERIFICATION_DOCUMENT", "READY")
	r := h.post(o, "/verification", map[string]any{"evidence": []map[string]any{{"media_id": doc, "evidence_type": "ID_DOCUMENT_FRONT"}}})
	h.expect(r, 201, "")
	var l usecases.VerificationLinkView
	r.data(h.t, &l)
	if err := h.app.Service.ApplyVerificationEvent(context.Background(), h.verif.decide(h.t, l.VerificationID, "VERIFIED", "", nil)); err != nil {
		h.t.Fatal(err)
	}
}

// verifyProperty takes a property's primary claim to VERIFIED.
func (h *harness) verifyProperty(o *owner, pid ids.ID) {
	h.t.Helper()
	doc := h.media.put(o.id, "DOCUMENT", "READY")
	h.expect(h.post(o, "/properties/"+pid.String()+"/ownership", map[string]any{"documents": []map[string]any{{"media_id": doc, "document_type": "DEED_OF_ASSIGNMENT"}}}), 200, "")
	r := h.post(o, "/properties/"+pid.String()+"/verification", nil)
	h.expect(r, 200, "")
	var ov usecases.OwnershipView
	r.data(h.t, &ov)
	if ov.Primary.Status != "UNDER_VERIFICATION" || ov.Primary.VerificationID == nil {
		h.t.Fatalf("claim not submitted: %+v", ov.Primary)
	}
	if err := h.app.Service.ApplyVerificationEvent(context.Background(), h.verif.decide(h.t, *ov.Primary.VerificationID, "VERIFIED", "", nil)); err != nil {
		h.t.Fatal(err)
	}
}

func TestProfileContactsAndAccountStatus(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	r := h.get(o, "/profile")
	h.expect(r, 200, "")
	var p usecases.ProfileView
	r.data(t, &p)
	if p.Version != 0 || p.Timezone != "Africa/Lagos" {
		t.Fatalf("default profile %+v", p)
	}
	body := map[string]any{"timezone": "Africa/Lagos", "preferred_channel": "EMAIL", "languages": []string{"en", "yo"}, "accessibility_needs": []string{"LARGE_TEXT"}}
	h.expect(h.do(call{method: "PATCH", path: api + "/profile", token: o.access, body: body}), 428, "PRECONDITION_REQUIRED")
	h.expect(h.write(o, "PATCH", "/profile", 0, map[string]any{"timezone": "Africa/Lagos", "preferred_channel": "FAX"}), 400, "INVALID_REQUEST")
	r = h.write(o, "PATCH", "/profile", 0, body)
	h.expect(r, 200, "")
	if r.header.Get("ETag") != `"1"` {
		t.Fatalf("etag %q", r.header.Get("ETag"))
	}
	h.expect(h.write(o, "PATCH", "/profile", 0, body), 409, "VERSION_CONFLICT")
	h.expect(h.write(o, "PATCH", "/profile", 1, body), 200, "")

	// Contacts: idempotent create, list, remove, other account cannot remove.
	k := key()
	c1 := h.do(call{method: "POST", path: api + "/profile/contacts", token: o.access, key: k, body: map[string]any{"contact_type": "EMERGENCY", "name": "Bola", "phone": "+2348012345678"}})
	h.expect(c1, 201, "")
	c2 := h.do(call{method: "POST", path: api + "/profile/contacts", token: o.access, key: k, body: map[string]any{"contact_type": "EMERGENCY", "name": "Bola", "phone": "+2348012345678"}})
	if c2.header.Get("Idempotent-Replayed") != "true" || string(c1.body) != string(c2.body) {
		t.Fatal("contact create not replayed")
	}
	h.expect(h.post(o, "/profile/contacts", map[string]any{"contact_type": "TRUSTED"}), 422, "CONTACT_VALUE_REQUIRED")
	var cv usecases.ContactView
	c1.data(t, &cv)
	other := h.register(true)
	h.expect(h.do(call{method: "DELETE", path: api + "/profile/contacts/" + cv.ID.String(), token: other.access, key: key()}), 404, "")
	h.expect(h.do(call{method: "DELETE", path: api + "/profile/contacts/" + cv.ID.String(), token: o.access, key: key()}), 200, "")

	// Gates: identity not verified yet.
	var st usecases.AccountStatusView
	h.get(o, "/account/status").data(t, &st)
	if st.IdentityStatus != "NONE" || st.Gates[0].Allowed || st.Gates[0].Missing[0] != "IDENTITY_VERIFIED" {
		t.Fatalf("gates %+v", st)
	}
	h.verifyIdentity(o)
	h.get(o, "/account/status").data(t, &st)
	if st.IdentityStatus != "VERIFIED" || !st.Gates[0].Allowed {
		t.Fatalf("gates after verification %+v", st)
	}
}

func TestIdentityVerificationEventsAreIdempotentAndOrdered(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	doc := h.media.put(o.id, "VERIFICATION_DOCUMENT", "READY")
	foreign := h.media.put(ids.New(), "VERIFICATION_DOCUMENT", "READY")
	h.expect(h.post(o, "/verification", map[string]any{"evidence": []map[string]any{{"media_id": foreign, "evidence_type": "ID_DOCUMENT_FRONT"}}}), 422, "MEDIA_NOT_OWNED")
	k := key()
	req := call{method: "POST", path: api + "/verification", token: o.access, key: k, body: map[string]any{"evidence": []map[string]any{{"media_id": doc, "evidence_type": "ID_DOCUMENT_FRONT"}}}}
	r := h.do(req)
	h.expect(r, 201, "")
	var l usecases.VerificationLinkView
	r.data(t, &l)
	if l.Status != "PROCESSING" {
		t.Fatalf("status %s", l.Status)
	}
	if rr := h.do(req); rr.status != 201 || rr.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %d %s", rr.status, rr.body)
	}
	if len(h.verif.cases) != 1 || h.verif.calls["create"] != 1 {
		t.Fatalf("duplicate downstream cases: %d", len(h.verif.cases))
	}
	h.expect(h.post(o, "/verification", map[string]any{"evidence": []map[string]any{{"media_id": doc, "evidence_type": "ID_DOCUMENT_FRONT"}}}), 409, "VERIFICATION_IN_PROGRESS")

	ctx := context.Background()
	verified := h.verif.decide(t, l.VerificationID, "VERIFIED", "", nil)
	if err := h.app.Service.ApplyVerificationEvent(ctx, verified); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Service.ApplyVerificationEvent(ctx, verified); err != nil { // duplicate delivery
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM processed_events WHERE consumer=$1`, usecases.ConsumerVerification); n != 1 {
		t.Fatalf("processed %d", n)
	}
	// Out-of-order: a late PROCESSING event never regresses VERIFIED.
	c, _ := h.verif.Get(ctx, l.VerificationID)
	c.Status = "PROCESSING"
	if err := h.app.Service.ApplyVerificationEvent(ctx, verificationEvent(t, c)); err != nil {
		t.Fatal(err)
	}
	var st string
	_ = h.db.Pool.QueryRow(ctx, `SELECT status FROM verification_links WHERE verification_id=$1`, l.VerificationID).Scan(&st)
	if st != "VERIFIED" {
		t.Fatalf("regressed to %s", st)
	}
	// Unknown case (link not committed yet) is retried, not dropped.
	c.VerificationID, c.Status = ids.New(), "VERIFIED"
	if err := h.app.Service.ApplyVerificationEvent(ctx, verificationEvent(t, c)); err == nil {
		t.Fatal("unknown verification must be retried")
	}
	// Another domain's event is ignored.
	c.Subject.Domain = string(platform.DomainArtisan)
	if err := h.app.Service.ApplyVerificationEvent(ctx, verificationEvent(t, c)); err != nil {
		t.Fatal(err)
	}
}

func TestVerificationSubjectBindingAndLostExpiryReadback(t *testing.T) {
	h := newHarness(t)
	o, l := h.liveListing("14 Verification Readback Road")
	ctx := context.Background()
	var vid ids.ID
	if err := h.db.Pool.QueryRow(ctx, `SELECT verification_id FROM verification_links WHERE property_id=$1 AND status='VERIFIED'`, l.PropertyID).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	c, err := h.verif.Get(ctx, vid)
	if err != nil {
		t.Fatal(err)
	}
	c.Status = "EXPIRED"
	c.Subject.ID = ids.New()
	if err := h.app.Service.ApplyVerificationEvent(ctx, verificationEvent(t, c)); err == nil {
		t.Fatal("a different property's outcome changed this case")
	}
	h.verif.mu.Lock()
	h.verif.cases[vid].Subject.ID = c.Subject.ID
	h.verif.mu.Unlock()
	if err := h.app.Service.SyncVerification(ctx, vid); err == nil {
		t.Fatal("mismatched readback subject accepted")
	}
	h.verif.mu.Lock()
	h.verif.cases[vid].Subject.ID = l.PropertyID
	h.verif.mu.Unlock()
	// Source expired authority, but the expiry delivery was lost.
	h.verif.decide(t, vid, "EXPIRED", "SOURCE_EXPIRED", nil)
	if _, err := h.db.Pool.Exec(ctx, `UPDATE verification_links SET last_synced_at=$2 WHERE verification_id=$1`, vid, h.clock.now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Service.ReconcileVerifications(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if got := h.listing(o, l.ID); got.Status != "PAUSED" {
		t.Fatalf("lost expiry left publication live: %s", got.Status)
	}
}

func TestPropertyLifecycleOwnershipVerificationAndEvents(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	pending := h.register(false)
	h.expect(h.post(pending, "/properties", map[string]any{"category": "RESIDENTIAL", "property_type": "APARTMENT", "title": "Pending",
		"address": address("1 Pending Road")}), 403, "ACCOUNT_NOT_ACTIVE")
	h.expect(h.post(o, "/properties", map[string]any{"category": "LAND", "property_type": "PLOT", "title": "Plot", "bedrooms": 2,
		"address": address("2 Land Road")}), 400, "INVALID_REQUEST")

	p := h.createProperty(o, "12 Admiralty Way")
	if p.Status != "DRAFT" || p.Ownership == nil || p.Ownership.Status != "DRAFT" || p.MyRole != "OWNER" || len(p.Features) != 1 {
		t.Fatalf("created %+v", p)
	}
	pid := p.ID.String()
	// Same owner, same address (normalized) → duplicate.
	h.expect(h.post(o, "/properties", map[string]any{"category": "RESIDENTIAL", "property_type": "APARTMENT", "title": "Again",
		"address": address("12, ADMIRALTY  way")}), 409, "PROPERTY_DUPLICATE")
	// Another owner claiming the same address → recorded CONFLICT (no disclosure).
	rival := h.register(true)
	rp := h.createProperty(rival, "12 Admiralty Way.")
	if rp.Ownership.Status != "CONFLICT" || !rp.Ownership.Conflict {
		t.Fatalf("conflict not recorded: %+v", rp.Ownership)
	}
	// IDOR: the rival cannot see o's property.
	h.expect(h.get(rival, "/properties/"+pid), 404, "PROPERTY_NOT_FOUND")
	h.expect(h.get(rival, "/properties/not-a-uuid"), 404, "")

	// List and filters.
	var list struct {
		Data []usecases.PropertySummary `json:"data"`
	}
	_ = json.Unmarshal(h.get(o, "/properties?status=DRAFT").body, &list)
	if len(list.Data) != 1 || !list.Data[0].OwnerAccount {
		t.Fatalf("list %+v", list)
	}

	// PATCH with If-Match; concurrent writers: exactly one wins.
	var wg sync.WaitGroup
	results := make([]int, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.write(o, "PATCH", "/properties/"+pid, p.Version, map[string]any{"title": fmt.Sprintf("Title %d", i)}).status
		}(i)
	}
	wg.Wait()
	if !(results[0] == 200 && results[1] == 409 || results[0] == 409 && results[1] == 200) {
		t.Fatalf("concurrent PATCH results %v", results)
	}

	// Ownership evidence → verification → VERIFIED → property ACTIVE.
	h.expect(h.post(o, "/properties/"+pid+"/verification", nil), 409, "OWNERSHIP_NOT_SUBMITTABLE")
	scanning := h.media.put(o.id, "DOCUMENT", "SCANNING")
	h.expect(h.post(o, "/properties/"+pid+"/ownership", map[string]any{"documents": []map[string]any{{"media_id": scanning, "document_type": "SURVEY_PLAN"}}}), 200, "")
	h.expect(h.post(o, "/properties/"+pid+"/verification", nil), 422, "EVIDENCE_NOT_READY")
	h.verifyProperty(o, p.ID)
	var pv usecases.PropertyView
	h.get(o, "/properties/"+pid).data(t, &pv)
	if pv.Status != "ACTIVE" || !pv.Ownership.Verified {
		t.Fatalf("after verification %+v %+v", pv.Status, pv.Ownership)
	}
	// Media status projection: the scanning document becomes READY via media.ready.
	m, _ := h.media.Get(context.Background(), scanning)
	if err := h.app.Service.ApplyMediaEvent(context.Background(), mediaEvent(t, m, "READY")); err != nil {
		t.Fatal(err)
	}
	var ov usecases.OwnershipView
	h.get(o, "/properties/"+pid+"/ownership").data(t, &ov)
	ready := 0
	for _, d := range ov.Primary.Documents {
		if d.MediaStatus == "READY" {
			ready++
		}
	}
	if ready != 2 {
		t.Fatalf("documents %+v", ov.Primary.Documents)
	}

	// Address change on a verified property requires re-verification.
	h.get(o, "/properties/"+pid).data(t, &pv)
	r := h.write(o, "PATCH", "/properties/"+pid, pv.Version, map[string]any{"address": address("14 Admiralty Way")})
	h.expect(r, 200, "")
	r.data(t, &pv)
	if pv.Ownership.Status != "REVERIFICATION_REQUIRED" {
		t.Fatalf("address change: %s", pv.Ownership.Status)
	}

	// Immutable history and lifecycle transitions.
	if n := h.count(`SELECT count(*) FROM lifecycle_transitions WHERE aggregate_id=$1`, pv.Ownership.RecordID); n < 4 {
		t.Fatalf("ownership transitions %d", n)
	}
	if _, err := h.db.Pool.Exec(context.Background(), `UPDATE property_history SET summary='x' WHERE property_id=$1`, p.ID); err == nil {
		t.Fatal("property_history must be append-only")
	}
	var hist struct {
		Data []usecases.HistoryView `json:"data"`
		Page struct {
			HasMore bool `json:"has_more"`
		} `json:"page"`
	}
	_ = json.Unmarshal(h.get(o, "/properties/"+pid+"/history?limit=2").body, &hist)
	if len(hist.Data) != 2 || !hist.Page.HasMore {
		t.Fatalf("history page %+v", hist)
	}

	// Events: property.created / updated / verification_changed with valid payloads.
	rows, err := h.db.Pool.Query(context.Background(), `SELECT event_type, payload FROM outbox_events WHERE aggregate_id=$1 ORDER BY created_at`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for rows.Next() {
		var typ string
		var payload []byte
		_ = rows.Scan(&typ, &payload)
		seen[typ]++
		if strings.Contains(string(payload), "Admiralty") {
			t.Fatalf("exact address leaked in %s", typ)
		}
	}
	rows.Close()
	for _, typ := range []string{"property.created.v1", "property.updated.v1", "property.verification_changed.v1"} {
		if seen[typ] == 0 {
			t.Fatalf("missing %s: %v", typ, seen)
		}
	}
}

func TestPropertyEventsMatchGeneratedSchemas(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "3 Schema Street")
	h.verifyProperty(o, p.ID)
	rows, err := h.db.Pool.Query(context.Background(), `SELECT event_id FROM outbox_events WHERE aggregate_id=$1`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var idsList []ids.ID
	for rows.Next() {
		var id ids.ID
		_ = rows.Scan(&id)
		idsList = append(idsList, id)
	}
	rows.Close()
	for _, id := range idsList {
		e, typ := h.outboxEnvelope(id)
		testkit.ValidateJSONSchema(t, contracts+typ+".schema.json", e)
	}
}

func (h *harness) outboxEnvelope(id ids.ID) ([]byte, string) {
	h.t.Helper()
	var typ string
	var env []byte
	if err := h.db.Pool.QueryRow(context.Background(), `SELECT event_type, jsonb_build_object('event_id', event_id, 'event_type', event_type,
		'producer', producer, 'aggregate_type', aggregate_type, 'aggregate_id', aggregate_id, 'occurred_at', occurred_at, 'payload', payload)::text::bytea
		FROM outbox_events WHERE event_id=$1`, id).Scan(&typ, &env); err != nil {
		h.t.Fatal(err)
	}
	return env, typ
}

func TestUnitsParcelsAssetsMediaAndImports(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "5 Unit Close")
	base := "/properties/" + p.ID.String()
	r := h.post(o, base+"/units", map[string]any{"unit_type": "BLOCK", "label": "Block A"})
	h.expect(r, 201, "")
	var block usecases.UnitView
	r.data(t, &block)
	r = h.post(o, base+"/units", map[string]any{"unit_type": "UNIT", "label": "Flat 1", "parent_unit_id": block.ID, "bedrooms": 2})
	h.expect(r, 201, "")
	var flat usecases.UnitView
	r.data(t, &flat)
	h.expect(h.post(o, base+"/units", map[string]any{"unit_type": "UNIT", "label": "flat 1", "parent_unit_id": block.ID}), 409, "UNIT_LABEL_TAKEN")
	h.expect(h.write(o, "PATCH", base+"/units/"+block.ID.String(), block.Version, map[string]any{"status": "ARCHIVED"}), 409, "UNIT_HAS_CHILDREN")
	h.expect(h.write(o, "PATCH", base+"/units/"+flat.ID.String(), flat.Version, map[string]any{"status": "ARCHIVED"}), 200, "")
	h.expect(h.post(o, base+"/parcels", map[string]any{"parcel_ref": "SP-1"}), 422, "PARCELS_NOT_APPLICABLE")

	r = h.post(o, base+"/assets", map[string]any{"asset_type": "GENERATOR", "name": "Backup generator", "installed_on": "2025-01-10", "service_interval_days": 90})
	h.expect(r, 201, "")
	var asset usecases.AssetView
	r.data(t, &asset)
	if asset.NextServiceDue == nil || *asset.NextServiceDue != "2025-04-10" {
		t.Fatalf("next service %v", asset.NextServiceDue)
	}
	h.expect(h.write(o, "PUT", base+"/assets/"+asset.ID.String(), asset.Version, map[string]any{"asset_type": "GENERATOR", "name": "Gen", "status": "RETIRED"}), 200, "")

	// Land property with a parcel boundary.
	r = h.post(o, "/properties", map[string]any{"category": "LAND", "property_type": "PLOT", "title": "Lekki plot", "address": address("Plot 9 Lekki")})
	h.expect(r, 201, "")
	var land usecases.PropertyView
	r.data(t, &land)
	h.expect(h.post(o, "/properties/"+land.ID.String()+"/parcels", map[string]any{"parcel_ref": "SP-1", "boundary_geojson": map[string]any{"type": "Point", "coordinates": []float64{1, 2}}}), 400, "INVALID_REQUEST")
	h.expect(h.post(o, "/properties/"+land.ID.String()+"/parcels", map[string]any{"parcel_ref": "SP-1",
		"boundary_geojson": map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{3.4, 6.4}, {3.5, 6.4}, {3.5, 6.5}, {3.4, 6.4}}}}}), 201, "")

	// Media: own READY photo links; foreign media refused; duplicate refused.
	photo := h.media.put(o.id, "IMAGE", "READY")
	h.expect(h.post(o, base+"/media", map[string]any{"media_id": photo, "purpose": "PHOTO", "geotag_lat": 6.4, "geotag_lon": 3.4}), 201, "")
	h.expect(h.post(o, base+"/media", map[string]any{"media_id": photo, "purpose": "PHOTO"}), 409, "MEDIA_ALREADY_LINKED")
	h.expect(h.post(o, base+"/media", map[string]any{"media_id": h.media.put(ids.New(), "IMAGE", "READY"), "purpose": "PHOTO"}), 422, "MEDIA_NOT_OWNED")
	h.expect(h.post(o, base+"/media", map[string]any{"media_id": photo, "purpose": "VIDEO"}), 422, "MEDIA_CLASS_NOT_ALLOWED")

	// Bulk import: valid, invalid and matched rows; re-processing never duplicates.
	rows := []map[string]any{
		{"external_ref": "EXT-1", "category": "RESIDENTIAL", "property_type": "DUPLEX", "title": "Imported duplex", "address": address("1 Import Ave")},
		{"external_ref": "EXT-2", "category": "RESIDENTIAL", "property_type": "duplex", "title": "x", "address": address("2 Import Ave")},
		{"external_ref": "EXT-1", "category": "RESIDENTIAL", "property_type": "DUPLEX", "title": "Repeat", "address": address("3 Import Ave")},
	}
	r = h.post(o, "/properties/imports", map[string]any{"rows": rows})
	h.expect(r, 202, "")
	var imp usecases.ImportView
	r.data(t, &imp)
	if more, err := h.app.Service.ProcessImports(context.Background(), "w1", time.Minute, 5); err != nil || !more {
		t.Fatalf("process: %v %v", more, err)
	}
	h.get(o, "/properties/imports/"+imp.ID.String()).data(t, &imp)
	if imp.Status != "COMPLETED_WITH_ERRORS" || imp.CreatedCount != 1 || imp.ErrorCount != 2 {
		t.Fatalf("import %+v", imp)
	}
	r = h.post(o, "/properties/imports", map[string]any{"rows": rows[:1]})
	r.data(t, &imp)
	_, _ = h.app.Service.ProcessImports(context.Background(), "w1", time.Minute, 5)
	h.get(o, "/properties/imports/"+imp.ID.String()).data(t, &imp)
	if imp.Status != "COMPLETED" || imp.MatchedCount != 1 || imp.CreatedCount != 0 {
		t.Fatalf("re-import %+v", imp)
	}
	other := h.register(true)
	h.expect(h.get(other, "/properties/imports/"+imp.ID.String()), 404, "")
}

func TestCoOwnersConsentAndGuardedTransfer(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.secure(o)
	co := h.register(true)
	buyer := h.register(true)
	p := h.createProperty(o, "8 Transfer Street")
	pid := p.ID.String()

	// Invite a co-owner (40%); invitee sees and accepts with the verified email.
	r := h.post(o, "/properties/"+pid+"/co-owners", map[string]any{"party_role": "CO_OWNER", "email": strings.ToUpper(co.email), "display_name": "Co Owner", "share_bps": 4000})
	h.expect(r, 201, "")
	var party usecases.PartyView
	r.data(t, &party)
	h.expect(h.get(co, "/properties/"+pid), 404, "")
	var inv struct {
		Data []usecases.InvitationView `json:"data"`
	}
	_ = json.Unmarshal(h.get(co, "/co-owner-invitations").body, &inv)
	if len(inv.Data) != 1 || inv.Data[0].PartyID != party.ID {
		t.Fatalf("invitations %+v", inv)
	}
	h.expect(h.post(buyer, "/co-owner-invitations/"+party.ID.String()+"/accept", nil), 404, "")
	h.expect(h.post(co, "/co-owner-invitations/"+party.ID.String()+"/accept", nil), 200, "")
	var cv usecases.PropertyView
	h.get(co, "/properties/"+pid).data(t, &cv)
	if cv.MyRole != "CO_OWNER" {
		t.Fatalf("co-owner role %q", cv.MyRole)
	}
	var ov usecases.OwnershipView
	h.get(o, "/properties/"+pid+"/ownership").data(t, &ov)
	for _, pt := range ov.Parties {
		if pt.PartyRole == "OWNER" && (pt.ShareBps == nil || *pt.ShareBps != 6000) {
			t.Fatalf("owner share not rebalanced: %+v", pt)
		}
	}
	// Owner-only capabilities can never be delegated to a representative.
	h.expect(h.post(o, "/properties/"+pid+"/co-owners", map[string]any{"party_role": "AUTHORIZED_REPRESENTATIVE", "email": "rep@example.com",
		"display_name": "Rep", "capabilities": []string{"payout.manage"}, "valid_until": h.clock.now().Add(24 * time.Hour)}), 400, "INVALID_REQUEST")

	h.verifyProperty(o, p.ID)
	h.verifyIdentity(o)
	h.verifyIdentity(buyer)

	// Transfer needs co-owner consent.
	transfer := map[string]any{"case_type": "TRANSFER", "to_email": buyer.email, "reason": "Sale completed off-platform"}
	h.expect(h.post(o, "/properties/"+pid+"/ownership-transfer", transfer), 409, "CO_OWNER_CONSENT_REQUIRED")
	r = h.post(o, "/properties/"+pid+"/consents", map[string]any{"action_type": "OWNERSHIP_TRANSFER", "target_type": "property", "target_id": p.ID})
	h.expect(r, 201, "")
	var cs usecases.ConsentStatus
	r.data(t, &cs)
	if cs.Satisfied || len(cs.Consents) != 1 {
		t.Fatalf("consent status %+v", cs)
	}
	var pending struct {
		Data []usecases.PendingConsentView `json:"data"`
	}
	_ = json.Unmarshal(h.get(co, "/consents").body, &pending)
	if len(pending.Data) != 1 {
		t.Fatalf("pending consents %+v", pending)
	}
	cid := pending.Data[0].ID.String()
	h.expect(h.post(o, "/consents/"+cid+"/decision", map[string]any{"decision": "APPROVED"}), 404, "")
	h.expect(h.post(co, "/consents/"+cid+"/decision", map[string]any{"decision": "APPROVED"}), 200, "")
	h.expect(h.post(co, "/consents/"+cid+"/decision", map[string]any{"decision": "REJECTED"}), 409, "CONSENT_ALREADY_DECIDED")
	if _, err := h.db.Pool.Exec(context.Background(), `UPDATE ownership_consents SET decision='REJECTED' WHERE id=$1`, pending.Data[0].ID); err == nil {
		t.Fatal("decided consent must be immutable")
	}

	// Step-up freshness: an old sign-in no longer counts.
	h.clock.advance(10 * time.Minute)
	h.expect(h.post(o, "/properties/"+pid+"/ownership-transfer", transfer), 403, "STEP_UP_REQUIRED")
	_, a := h.login(o, o.device)
	o.apply(a)
	r = h.post(o, "/properties/"+pid+"/ownership-transfer", transfer)
	h.expect(r, 201, "")
	var tv usecases.TransferView
	r.data(t, &tv)
	h.get(o, "/properties/"+pid).data(t, &cv)
	if cv.Status != "TRANSFER_PENDING" {
		t.Fatalf("property %s", cv.Status)
	}
	h.expect(h.write(o, "PATCH", "/properties/"+pid, cv.Version, map[string]any{"title": "Locked"}), 409, "PROPERTY_TRANSFER_PENDING")
	h.expect(h.post(o, "/properties/"+pid+"/ownership-transfer", transfer), 409, "PROPERTY_NOT_TRANSFERABLE")
	h.completeTransfer(t, o, co, buyer, pid, tv.ID)
}
