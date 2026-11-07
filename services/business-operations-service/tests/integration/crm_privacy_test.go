package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type privacyView struct {
	ID      ids.ID `json:"id"`
	State   string `json:"state"`
	Version int64  `json:"version"`
}

// Export authorization, legal hold blocking erasure, approved anonymisation
// through the retention worker and retained financial/stay evidence.
func TestBusinessCRMPrivacyExportLegalHoldAndErasure(t *testing.T) {
	c := newCommercialHTTP(t)
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "guest_privacy:write"})
	g := crmGuestFixture(c)
	crmConsent(c, g, "MARKETING", "GRANTED", 0)
	gp := "/guests/" + g.Guest.ID.String()

	export := commercialData[privacyView](t, c.must(c.owner, "POST", gp+"/privacy-requests", businesshttp.CRMPrivacyRequestCommand{Kind: "EXPORT", RequesterKind: "GUEST", VerificationReference: "front-desk-identity-check-reference", Reason: "Record a verified data-subject export request"}, 201))
	c.must(c.owner, "GET", "/privacy-requests/"+export.ID.String()+"/export", nil, 409) // not yet approved
	export = commercialData[privacyView](t, c.must(c.owner, "POST", "/privacy-requests/"+export.ID.String()+"/decision", businesshttp.CRMPrivacyDecision{Version: 1, Decision: "APPROVE", Reason: "Approve the verified export within scope"}, 200))
	if export.State != "COMPLETED" {
		t.Fatalf("export %+v", export)
	}
	data := commercialData[struct {
		Scope string         `json:"scope"`
		Data  map[string]any `json:"data"`
	}](t, c.must(c.owner, "GET", "/privacy-requests/"+export.ID.String()+"/export", nil, 200))
	if data.Scope != "BUSINESS_OWNED_CRM_DATA_FOR_PROPERTY" || data.Data["profile"] == nil || len(data.Data["consents"].([]any)) != 1 {
		t.Fatalf("export data %+v", data)
	}
	c.must(c.user(), "GET", "/privacy-requests/"+export.ID.String()+"/export", nil, 404) // tenant isolation

	hold := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", gp+"/legal-holds", businesshttp.CRMLegalHoldCommand{Scope: "ALL_GUEST_DATA", AuthorityReference: "counsel-instruction-reference", Reason: "Preserve evidence under a legal hold"}, 201))
	erase := commercialData[privacyView](t, c.must(c.owner, "POST", gp+"/privacy-requests", businesshttp.CRMPrivacyRequestCommand{Kind: "ERASURE", RequesterKind: "GUEST", VerificationReference: "verified-erasure-identity-reference", Reason: "Record a verified erasure request"}, 201))
	decision := businesshttp.CRMPrivacyDecision{Version: 1, Decision: "APPROVE", Reason: "Approve the verified erasure request"}
	c.must(c.owner, "POST", "/privacy-requests/"+erase.ID.String()+"/decision", decision, 403) // erasure needs an approval
	op := decision
	decision.ApprovalID = c.approval("COMMERCIAL_GUEST_ERASURE", "guest_privacy:write", "", nil, "", map[string]any{"privacy_request_id": erase.ID, "guest_id": g.Guest.ID, "command": op})
	erase = commercialData[privacyView](t, c.must(c.owner, "POST", "/privacy-requests/"+erase.ID.String()+"/decision", decision, 200))
	if erase.State != "BLOCKED_BY_LEGAL_HOLD" {
		t.Fatalf("hold ignored %+v", erase)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE guest_profiles SET state='ANONYMISED',version=version+1 WHERE id=$1`, g.Guest.ID); e == nil {
		t.Fatal("direct anonymisation bypassed the legal hold")
	}

	release := businesshttp.CRMLegalHoldRelease{Version: 1, Reason: "Release the hold after independent legal review"}
	releaseOp := release
	release.ApprovalID = c.approval("COMMERCIAL_LEGAL_HOLD_RELEASE", "guest_privacy:write", "", nil, "", map[string]any{"legal_hold_id": hold.ID, "command": releaseOp})
	c.must(c.owner, "POST", "/legal-holds/"+hold.ID.String()+"/release", release, 200)
	decision = businesshttp.CRMPrivacyDecision{Version: erase.Version, Decision: "APPROVE", Reason: "Approve erasure after the hold was released"}
	op = decision
	decision.ApprovalID = c.approval("COMMERCIAL_GUEST_ERASURE", "guest_privacy:write", "", nil, "", map[string]any{"privacy_request_id": erase.ID, "guest_id": g.Guest.ID, "command": op})
	erase = commercialData[privacyView](t, c.must(c.owner, "POST", "/privacy-requests/"+erase.ID.String()+"/decision", decision, 200))
	if erase.State != "APPROVED" {
		t.Fatalf("erasure %+v", erase)
	}
	if e := c.domain.ProcessDataRetention(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	var state, profileState string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT p.state,g.state FROM privacy_requests p JOIN guest_profiles g ON g.id=p.guest_id WHERE p.id=$1`, erase.ID).Scan(&state, &profileState); e != nil || state != "COMPLETED" || profileState != "ANONYMISED" {
		t.Fatalf("anonymisation %s/%s %v", state, profileState, e)
	}
	if e := c.domain.ProcessDataRetention(t.Context(), 10); e != nil { // replay is a no-op
		t.Fatal(e)
	}
	var consents int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM consents WHERE guest_id=$1`, g.Guest.ID).Scan(&consents); e != nil || consents != 1 {
		t.Fatalf("consent evidence not retained %d %v", consents, e)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE guest_profiles SET state='ACTIVE',version=version+1 WHERE id=$1`, g.Guest.ID); e == nil {
		t.Fatal("anonymisation reversed")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `DELETE FROM privacy_requests WHERE id=$1`, erase.ID); e == nil {
		t.Fatal("privacy request evidence deleted")
	}
}

func TestBusinessCRMVIPAndSourceBackedSegments(t *testing.T) {
	c := newCommercialHTTP(t)
	a, b := crmGuestFixture(c), crmGuestFixture(c)
	crmConsent(c, a, "MARKETING", "GRANTED", 0)
	vip := businesshttp.CRMVIPCommand{Tier: "VIP", Basis: "OPERATOR_REVIEWED", EvidenceReference: "duty-manager-review-reference", Reason: "Classify the guest from reviewed evidence"}
	c.must(c.owner, "POST", "/guests/"+b.Guest.ID.String()+"/vip", vip, 201)
	c.must(c.owner, "POST", "/guests/"+b.Guest.ID.String()+"/vip", vip, 409) // stale version
	expires := time.Now().Add(-time.Hour)
	vip.ExpectedVersion, vip.ExpiresAt = 1, &expires
	c.must(c.owner, "POST", "/guests/"+b.Guest.ID.String()+"/vip", vip, 400) // expiry must be in the future

	segment := func(kind string, d businesshttp.CRMSegmentDefinition) ids.ID {
		return commercialData[struct {
			SegmentKey ids.ID `json:"segment_key"`
		}](t, c.must(c.owner, "POST", "/guest-segments", businesshttp.CRMSegmentCommand{PropertyID: c.property.ID, Name: "Segment " + kind, Kind: kind, Definition: d, State: "ACTIVE", Reason: "Define a source-backed guest segment"}, 201)).SegmentKey
	}
	c.must(c.owner, "POST", "/guest-segments", businesshttp.CRMSegmentCommand{PropertyID: c.property.ID, Name: "Bad", Kind: "MARKETING_ELIGIBLE", Definition: businesshttp.CRMSegmentDefinition{Tier: "VIP"}, State: "ACTIVE", Reason: "Parameters outside the closed criteria are rejected"}, 400)
	marketing := segment("MARKETING_ELIGIBLE", businesshttp.CRMSegmentDefinition{})
	vips := segment("VIP", businesshttp.CRMSegmentDefinition{Tier: "VIP"})
	evaluate := func(key ids.ID) map[string]any {
		return commercialData[map[string]any](t, c.must(c.owner, "POST", "/guest-segments/"+key.String()+"/evaluate", map[string]any{"reason": "Evaluate against current authoritative records"}, 200))
	}
	members := func(key ids.ID) []ids.ID {
		var out []ids.ID
		for _, m := range commercialData[struct {
			Items []struct {
				GuestID ids.ID `json:"guest_id"`
			} `json:"items"`
		}](t, c.must(c.owner, "GET", "/guest-segments/"+key.String()+"/members", nil, 200)).Items {
			out = append(out, m.GuestID)
		}
		return out
	}
	if r := evaluate(marketing); r["member_count"].(float64) != 1 {
		t.Fatalf("marketing %+v", r)
	}
	if m := members(marketing); len(m) != 1 || m[0] != a.Guest.ID {
		t.Fatalf("marketing members %v", m)
	}
	if m := (evaluate(vips)); m["member_count"].(float64) != 1 {
		t.Fatalf("vip %+v", m)
	}
	// Consent withdrawal removes marketing eligibility on the next evaluation.
	crmConsent(c, a, "MARKETING", "WITHDRAWN", 1)
	if r := evaluate(marketing); r["member_count"].(float64) != 0 || r["exited"].(float64) != 1 {
		t.Fatalf("withdrawn consent still eligible %+v", r)
	}
	c.must(c.user(), "GET", "/guest-segments/"+marketing.String()+"/members", nil, 404)
}
