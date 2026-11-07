package integration

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// crmReviewer replaces the default approver with an independent reviewer that
// holds every approval capability the CRM journeys need.
func crmReviewer(c *commercialHarness) {
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "guest_privacy:write", "reservations:approve", "recovery:approve", "recovery:read", "recovery:write", "loyalty:approve", "rates:approve", "pricing:approve", "channels:approve", "folios:approve", "pms:approve"})
}

func mergePreview(c *commercialHarness, source, target ids.ID, basis string, status int) businesshttp.CRMGuestMerge {
	c.t.Helper()
	raw := c.must(c.owner, "POST", "/guests/"+source.String()+"/merge-previews", businesshttp.CRMGuestMergePreviewCommand{TargetGuestID: target, MatchBasis: basis, OwnershipEvidenceReference: "front-desk-identity-document-review", Reason: "Review a suspected duplicate guest record"}, status)
	if status != 201 {
		return businesshttp.CRMGuestMerge{}
	}
	return commercialData[businesshttp.CRMGuestMerge](c.t, raw)
}

func hasConflict(p businesshttp.CRMMergePreview, code string, blocking bool) bool {
	return slices.ContainsFunc(p.Conflicts, func(v businesshttp.CRMMergeConflict) bool { return v.Code == code && v.Blocking == blocking })
}

func mergeApproval(c *commercialHarness, m businesshttp.CRMGuestMerge, in businesshttp.CRMGuestMergeExecute) ids.ID {
	op := in
	op.ApprovalID = ids.Nil
	return c.approval("COMMERCIAL_GUEST_MERGE", "guest_privacy:write", "", nil, "", map[string]any{"merge_id": m.ID, "source_guest_id": m.SourceGuestID, "target_guest_id": m.TargetGuestID, "preview_digest": m.PreviewDigest, "command": op})
}

// High-risk ownership merge: digest-bound independent approval, consent is
// never widened, open bookings follow the canonical guest, legal holds stay
// attached and still block erasure, concurrent execution applies once, and an
// approved reversal restores references from the recorded lineage.
func TestBusinessCRMGuestMergeApprovalConsentLegalHoldAndReversal(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	crmReviewer(c)
	source, target := crmGuestFixture(c), crmGuestFixture(c)
	crmConsent(c, target, "MARKETING", "GRANTED", 0)
	crmConsent(c, source, "MARKETING", "GRANTED", 0)
	crmConsent(c, source, "MARKETING", "WITHDRAWN", 1)
	crmConsent(c, source, "OPERATIONAL_CONTACT", "GRANTED", 0)
	booking := reservationCommandFixture(c, false)
	booking.Guests = []reservations.GuestCommand{{GuestID: &source.Guest.ID, SourceKind: source.SourceKind, SourceReference: source.SourceReference, IsPrimary: true}}
	v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", booking, 201))
	c.must(c.owner, "POST", "/guests/"+source.Guest.ID.String()+"/legal-holds", businesshttp.CRMLegalHoldCommand{Scope: "ALL_GUEST_DATA", AuthorityReference: "counsel-instruction-reference", Reason: "Preserve evidence under a legal hold"}, 201)

	// No authenticated source identity is shared: a source-backed basis is refused.
	blockedPreview := mergePreview(c, source.Guest.ID, target.Guest.ID, "SHARED_SOURCE_IDENTITY", 201)
	if blockedPreview.Preview.Executable || !hasConflict(blockedPreview.Preview, "SOURCE_MATCH_REQUIRED", true) {
		t.Fatalf("contact-free match accepted %+v", blockedPreview.Preview)
	}
	c.must(c.owner, "POST", "/guest-merges/"+blockedPreview.ID.String()+"/execute", businesshttp.CRMGuestMergeExecute{Version: 1, PreviewDigest: blockedPreview.PreviewDigest, Reason: "Attempt a blocked merge"}, 409)
	c.must(c.owner, "POST", "/guests/"+source.Guest.ID.String()+"/merge-previews", businesshttp.CRMGuestMergePreviewCommand{TargetGuestID: source.Guest.ID, MatchBasis: "VERIFIED_OWNERSHIP", OwnershipEvidenceReference: "self-merge-attempt", Reason: "A guest cannot merge into itself"}, 400)
	c.must(c.owner, "POST", "/guest-merges/"+blockedPreview.ID.String()+"/cancel", businesshttp.CRMVersionCommand{Version: 1, Reason: "Discard the unsupported preview"}, 200)

	m := mergePreview(c, source.Guest.ID, target.Guest.ID, "VERIFIED_OWNERSHIP", 201)
	p := m.Preview
	if !p.Executable || p.Risk != "HIGH" || !p.ApprovalRequired || !hasConflict(p, "LEGAL_HOLD_PRESENT", false) || !hasConflict(p, "CONSENT_CONFLICT", false) || !hasConflict(p, "VERIFIED_OWNERSHIP_REVIEW", false) || p.Counts.OpenReservationGuests != 1 || p.Counts.LegalHolds != 1 {
		t.Fatalf("preview %+v", p)
	}
	restrict := map[string]string{}
	for _, r := range p.ConsentRestrictions {
		restrict[r.Purpose] = r.Result
	}
	if restrict["MARKETING"] != "WITHDRAWN" || restrict["OPERATIONAL_CONTACT"] != "NOT_GRANTED" {
		t.Fatalf("consent restrictions %+v", p.ConsentRestrictions)
	}
	if strings.Contains(string(c.must(c.owner, "GET", "/guest-merges/"+m.ID.String(), nil, 200)), "@") {
		t.Fatal("merge preview exposed contact data")
	}
	exec := businesshttp.CRMGuestMergeExecute{Version: m.Version, PreviewDigest: m.PreviewDigest, Reason: "Merge the independently verified duplicate record"}
	path := "/guest-merges/" + m.ID.String() + "/execute"
	c.must(c.owner, "POST", path, exec, 403) // high-risk merge needs an independent approval
	stale := exec
	stale.PreviewDigest = strings.Repeat("0", 64)
	c.must(c.owner, "POST", path, stale, 409)
	exec.ApprovalID = mergeApproval(c, m, exec)
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := c.call(c.owner, "POST", path, exec, map[string]string{"Idempotency-Key": ids.New().String()})
			codes <- code
		}()
	}
	wg.Wait()
	if a, b := <-codes, <-codes; !(a == 200 && b == 409 || a == 409 && b == 200) {
		t.Fatalf("concurrent merge execution %d/%d", a, b)
	}

	var state string
	var into *ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state,merged_into FROM guest_profiles WHERE id=$1`, source.Guest.ID).Scan(&state, &into); e != nil || state != "MERGED" || into == nil || *into != target.Guest.ID {
		t.Fatalf("source %s %v %v", state, into, e)
	}
	var bookingGuest ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT guest_id FROM reservation_guests WHERE reservation_id=$1`, v.Reservation.ID).Scan(&bookingGuest); e != nil || bookingGuest != target.Guest.ID {
		t.Fatalf("open booking not re-pointed %v %v", bookingGuest, e)
	}
	var marketing string
	var operational int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT state FROM consents WHERE guest_id=$1 AND purpose='MARKETING' ORDER BY version DESC LIMIT 1),(SELECT count(*) FROM consents WHERE guest_id=$1 AND purpose='OPERATIONAL_CONTACT')`, target.Guest.ID).Scan(&marketing, &operational); e != nil || marketing != "WITHDRAWN" || operational != 0 {
		t.Fatalf("consent widened or not restricted %s/%d %v", marketing, operational, e)
	}
	var holdState string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM legal_holds WHERE guest_id=$1`, source.Guest.ID).Scan(&holdState); e != nil || holdState != "ACTIVE" {
		t.Fatalf("legal hold %s %v", holdState, e)
	}
	list := commercialData[struct {
		Canonical ids.ID `json:"canonical_guest_id"`
	}](t, c.must(c.owner, "GET", "/guests/"+source.Guest.ID.String()+"/merges", nil, 200))
	if list.Canonical != target.Guest.ID {
		t.Fatalf("canonical %v", list.Canonical)
	}
	history := commercialData[businesshttp.CRMStayHistoryList](t, c.must(c.owner, "GET", "/guests/"+target.Guest.ID.String()+"/stay-history", nil, 200))
	if len(history.MergedGuestIDs) != 2 || len(history.Items) != 1 || history.Items[0].History.ReservationID != v.Reservation.ID {
		t.Fatalf("merged history %+v", history)
	}

	// The legal hold on the merged record still blocks erasure of the canonical identity.
	gp := "/guests/" + target.Guest.ID.String()
	erase := commercialData[privacyView](t, c.must(c.owner, "POST", gp+"/privacy-requests", businesshttp.CRMPrivacyRequestCommand{Kind: "ERASURE", RequesterKind: "GUEST", VerificationReference: "verified-erasure-identity-reference", Reason: "Record a verified erasure request"}, 201))
	decision := businesshttp.CRMPrivacyDecision{Version: 1, Decision: "APPROVE", Reason: "Approve the verified erasure request"}
	op := decision
	decision.ApprovalID = c.approval("COMMERCIAL_GUEST_ERASURE", "guest_privacy:write", "", nil, "", map[string]any{"privacy_request_id": erase.ID, "guest_id": target.Guest.ID, "command": op})
	if erase = commercialData[privacyView](t, c.must(c.owner, "POST", "/privacy-requests/"+erase.ID.String()+"/decision", decision, 200)); erase.State != "BLOCKED_BY_LEGAL_HOLD" {
		t.Fatalf("merge escaped legal hold %+v", erase)
	}

	// The source cannot be merged again, and a merge cannot be fabricated directly.
	again := mergePreview(c, source.Guest.ID, target.Guest.ID, "VERIFIED_OWNERSHIP", 201)
	if again.Preview.Executable || !hasConflict(again.Preview, "SOURCE_NOT_ACTIVE", true) {
		t.Fatalf("repeated merge %+v", again.Preview)
	}
	other := crmGuestFixture(c)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE guest_profiles SET state='MERGED',merged_into=$2,version=version+1 WHERE id=$1`, other.Guest.ID, target.Guest.ID); e == nil {
		t.Fatal("merge without lineage accepted")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE guest_merges SET risk='STANDARD',version=version+1 WHERE id=$1`, m.ID); e == nil {
		t.Fatal("merge evidence mutable")
	}

	// Recovery: an approved reversal restores the recorded references.
	merged := commercialData[businesshttp.CRMGuestMergeDetail](t, c.must(c.owner, "GET", "/guest-merges/"+m.ID.String(), nil, 200))
	reverse := businesshttp.CRMGuestMergeReverse{Version: merged.Merge.Version, Reason: "The ownership evidence was later found to be wrong"}
	c.must(c.owner, "POST", "/guest-merges/"+m.ID.String()+"/reverse", reverse, 400) // approval reference required
	reverse.ApprovalID = ids.New()
	c.must(c.owner, "POST", "/guest-merges/"+m.ID.String()+"/reverse", reverse, 403)
	rop := reverse
	rop.ApprovalID = ids.Nil
	reverse.ApprovalID = c.approval("COMMERCIAL_GUEST_UNMERGE", "guest_privacy:write", "", nil, "", map[string]any{"merge_id": m.ID, "source_guest_id": m.SourceGuestID, "target_guest_id": m.TargetGuestID, "command": rop})
	result := commercialData[businesshttp.CRMGuestMergeResult](t, c.must(c.owner, "POST", "/guest-merges/"+m.ID.String()+"/reverse", reverse, 200))
	if result.Restored != 1 || result.Merge.State != "REVERSED" || result.CanonicalGuestID != source.Guest.ID {
		t.Fatalf("reversal %+v", result)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state,merged_into FROM guest_profiles WHERE id=$1`, source.Guest.ID).Scan(&state, &into); e != nil || state != "ACTIVE" || into != nil {
		t.Fatalf("restored source %s %v %v", state, into, e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT guest_id FROM reservation_guests WHERE reservation_id=$1`, v.Reservation.ID).Scan(&bookingGuest); e != nil || bookingGuest != source.Guest.ID {
		t.Fatalf("booking not restored %v %v", bookingGuest, e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM consents WHERE guest_id=$1 AND purpose='MARKETING' ORDER BY version DESC LIMIT 1`, target.Guest.ID).Scan(&marketing); e != nil || marketing != "WITHDRAWN" {
		t.Fatalf("consent re-widened by reversal %s %v", marketing, e)
	}
	var restored, mergedRefs int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE operation='RESTORED'),count(*) FILTER(WHERE operation='REASSIGNED') FROM guest_merge_references WHERE merge_id=$1`, m.ID).Scan(&restored, &mergedRefs); e != nil || restored != 1 || mergedRefs != 1 {
		t.Fatalf("lineage %d/%d %v", restored, mergedRefs, e)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `DELETE FROM guest_merge_references WHERE merge_id=$1`, m.ID); e == nil {
		t.Fatal("merge lineage deleted")
	}
}

// Source-backed merges use identical authenticated source identities from
// different connections; the same identity can never link to two guests, and
// different identities from one source block a merge. Tenants never cross.
func TestBusinessCRMGuestMergeSourceIdentityDuplicateAndTenantIsolation(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	_, adapter := newCommercialSupplyConsumer(t)
	first := c.connectMarketplace(adapter)
	crmReviewer(c)
	second := ids.New()
	if _, e := c.db.Pool.Exec(t.Context(), `INSERT INTO channel_connections SELECT (jsonb_populate_record(NULL::channel_connections,to_jsonb(c)||jsonb_build_object('id',$2::uuid,'provider_account_id','disposable-provider-account-2','version',1))).* FROM channel_connections c WHERE id=$1`, first.ID, second); e != nil {
		t.Fatal(e)
	}
	signer := c.supplySigner(platform.MarketplaceService)
	token, e := signer.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: c.owner.registered.OrganisationID})
	if e != nil {
		t.Fatal(e)
	}
	assert := func(connection ids.ID, reference string) ids.ID {
		in := businesshttp.CRMSourceAssertion{OrganisationID: c.owner.registered.OrganisationID, PropertyID: c.property.ID, ConnectionID: connection, SourceReference: reference, SourceVersion: 1, State: "ACTIVE", ExpiresAt: time.Now().UTC().Add(time.Hour), EvidenceReference: "owning-customer-assertion-reference"}
		status, raw := c.rootCall("/internal/v1/business-supply/guest-assertions", in, map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": ids.New().String()})
		if status != 201 {
			t.Fatalf("assertion %d %s", status, raw)
		}
		return commercialData[struct {
			ID ids.ID `json:"id"`
		}](t, raw).ID
	}
	link := func(g crmRegisteredGuest, projection ids.ID, reference string, status int) {
		in := businesshttp.CRMIdentityLinkCommand{GuestVersion: g.Guest.Version, SourceKind: "MARKETPLACE", SourceReference: reference, ProjectionID: &projection, EvidenceReference: "reviewed-matching-guest-source-reference", Reason: "Independent review links a signed owning source assertion"}
		op := in
		in.ApprovalID = c.approval("COMMERCIAL_GUEST_IDENTITY_LINK", "guest_privacy:write", "", nil, "", map[string]any{"guest_id": g.Guest.ID, "property_id": c.property.ID, "command": op})
		c.must(c.owner, "POST", "/guests/"+g.Guest.ID.String()+"/identity-links", in, status)
	}
	a, b, d := crmGuestFixture(c), crmGuestFixture(c), crmGuestFixture(c)
	p1 := assert(first.ID, "marketplace-customer-1")
	p2 := assert(second, "marketplace-customer-1")
	link(a, p1, "marketplace-customer-1", 201)
	link(b, p2, "marketplace-customer-1", 201)
	link(d, p1, "marketplace-customer-1", 409) // duplicate source identity denied
	link(d, assert(first.ID, "marketplace-customer-2"), "marketplace-customer-2", 201)

	// Different identities from the same authenticated source are different people.
	conflict := mergePreview(c, d.Guest.ID, a.Guest.ID, "VERIFIED_OWNERSHIP", 201)
	if conflict.Preview.Executable || !hasConflict(conflict.Preview, "DIFFERENT_VERIFIED_SOURCE_IDENTITY", true) {
		t.Fatalf("different source identities %+v", conflict.Preview)
	}
	c.must(c.owner, "POST", "/guest-merges/"+conflict.ID.String()+"/execute", businesshttp.CRMGuestMergeExecute{Version: 1, PreviewDigest: conflict.PreviewDigest, Reason: "Attempt a blocked merge"}, 409)

	// Shared authenticated identity, no other conflict: standard risk, MFA only.
	m := mergePreview(c, b.Guest.ID, a.Guest.ID, "SHARED_SOURCE_IDENTITY", 201)
	if !m.Preview.Executable || m.Preview.Risk != "STANDARD" || m.Preview.ApprovalRequired || m.Preview.SharedSourceIdentity != 1 {
		t.Fatalf("source-backed preview %+v", m.Preview)
	}
	limited := c.user()
	c.secure(limited)
	c.join(limited, c.owner, []string{"guests:read", "guests:write"})
	c.must(limited, "POST", "/guest-merges/"+m.ID.String()+"/execute", businesshttp.CRMGuestMergeExecute{Version: 1, PreviewDigest: m.PreviewDigest, Reason: "Merge without privacy authority"}, 403)
	done := commercialData[businesshttp.CRMGuestMergeResult](t, c.must(c.owner, "POST", "/guest-merges/"+m.ID.String()+"/execute", businesshttp.CRMGuestMergeExecute{Version: 1, PreviewDigest: m.PreviewDigest, Reason: "Merge the shared authenticated source identity"}, 200))
	if done.CanonicalGuestID != a.Guest.ID || done.Merge.State != "MERGED" {
		t.Fatalf("merge %+v", done)
	}
	var lineage int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM guest_merge_references WHERE merge_id=$1 AND entity_type='identity_link' AND operation='RESOLVED_VIA_LINEAGE'`, m.ID).Scan(&lineage); e != nil || lineage < 1 {
		t.Fatalf("source identity not preserved through lineage %d %v", lineage, e)
	}
	var links int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM guest_identity_links WHERE guest_id=$1 AND state='ACTIVE'`, b.Guest.ID).Scan(&links); e != nil || links != 2 {
		t.Fatalf("original source identities changed %d %v", links, e)
	}

	// Another organisation can neither see nor merge these guests.
	outsider := c.user()
	c.secure(outsider)
	c.must(outsider, "POST", "/guests/"+a.Guest.ID.String()+"/merge-previews", businesshttp.CRMGuestMergePreviewCommand{TargetGuestID: d.Guest.ID, MatchBasis: "VERIFIED_OWNERSHIP", OwnershipEvidenceReference: "foreign-organisation-attempt", Reason: "Cross-tenant merge attempt"}, 404)
	c.must(outsider, "GET", "/guest-merges/"+m.ID.String(), nil, 404)
	mergePreview(c, d.Guest.ID, ids.New(), "VERIFIED_OWNERSHIP", 404)
	if _, e = c.db.Pool.Exec(t.Context(), `INSERT INTO guest_merges(id,organisation_id,property_id,source_guest_id,target_guest_id,match_basis,ownership_evidence_reference,risk,preview,preview_digest,source_version,target_version,preview_expires_at,reason,created_by) VALUES($1,$2,$3,$4,$5,'VERIFIED_OWNERSHIP','foreign-evidence','HIGH','{}',repeat('a',64),1,1,now(),'Cross-tenant fabrication',$6)`, ids.New(), outsider.registered.OrganisationID, c.property.ID, a.Guest.ID, d.Guest.ID, outsider.registered.AccountID); e == nil {
		t.Fatal("cross-organisation merge lineage accepted")
	}
}
