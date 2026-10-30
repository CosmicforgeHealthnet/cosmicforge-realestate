package integration

import (
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"testing"
	"time"
)

// Fixtures associate independently registered accounts without bypassing any
// production authentication; invitation acceptance has its own HTTP journey.
func (h *businessHarness) join(u, owner *businessUser, caps []string) ids.ID {
	h.t.Helper()
	org := owner.registered.OrganisationID
	var role ids.ID
	if err := h.db.Pool.QueryRow(h.t.Context(), `SELECT role_id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2`, org, owner.registered.AccountID).Scan(&role); err != nil {
		h.t.Fatal(err)
	}
	member := ids.New()
	if _, err := h.db.Pool.Exec(h.t.Context(), `INSERT INTO staff_memberships(id,organisation_id,account_id,role_id,status,effective_at,scope_id) VALUES($1,$2,$3,$4,'ACTIVE',now(),$2)`, member, org, u.registered.AccountID, role); err != nil {
		h.t.Fatal(err)
	}
	for _, cap := range caps {
		if _, err := h.db.Pool.Exec(h.t.Context(), `INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES($1,$2,$3,$4,'COMPANY',$2,true,now())`, ids.New(), org, member, cap); err != nil {
			h.t.Fatal(err)
		}
	}
	status, raw := h.call(u, "POST", "/auth/organisation", map[string]any{"organisation_id": org}, nil)
	if status != 200 {
		h.t.Fatalf("select %d %s", status, raw)
	}
	var selected struct {
		Data struct {
			Token string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &selected); err != nil {
		h.t.Fatal(err)
	}
	u.tokens.AccessToken = selected.Data.Token
	u.registered.OrganisationID = org
	return member
}
func TestBusinessDelegationLiveAuthorityExpiryAndNoChaining(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, b, c := h.user(), h.user(), h.user()
	h.secure(owner)
	h.secure(b)
	bm := h.join(b, owner, []string{"delegations:write"})
	cm := h.join(c, owner, nil)
	status, raw := h.call(owner, "POST", "/branches", map[string]any{"name": "Delegated branch"}, nil)
	if status != 201 {
		t.Fatalf("branch %d %s", status, raw)
	}
	var branch struct {
		Data struct {
			ID ids.ID `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &branch)
	now := time.Now().UTC()
	h.service.Now = func() time.Time { return now }
	expiry := now.Add(time.Hour)
	body := map[string]any{"delegate_membership_id": bm, "grant": map[string]any{"capability": "hierarchy:read", "scope_kind": "BRANCH", "scope_id": branch.Data.ID, "effective_at": now, "expires_at": expiry, "inherit": true}, "reason": "Cover temporary absence"}
	status, raw = h.call(owner, "POST", "/delegations", body, nil)
	if status != 201 {
		t.Fatalf("delegation %d %s", status, raw)
	}
	if status, raw = h.call(b, "GET", "/branches/"+branch.Data.ID.String(), nil, nil); status != 200 {
		t.Fatalf("delegated read %d %s", status, raw)
	}
	body["delegate_membership_id"] = cm
	if status, _ = h.call(b, "POST", "/delegations", body, nil); status != 403 {
		t.Fatalf("chained delegation %d", status)
	}
	// Revoking the source authority invalidates borrowed authority immediately.
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability='hierarchy:read'`, owner.registered.OrganisationID, owner.registered.AccountID); err != nil {
		t.Fatal(err)
	}
	if status, _ = h.call(b, "GET", "/branches/"+branch.Data.ID.String(), nil, nil); status != 403 {
		t.Fatalf("revoked source %d", status)
	}
	now = expiry.Add(time.Second)
	if status, _ = h.call(b, "GET", "/branches/"+branch.Data.ID.String(), nil, nil); status != 403 {
		t.Fatalf("expired delegation %d", status)
	}
}
func TestBusinessEmergencyScopeMFAImmutableReviewAndExpiry(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, reviewer := h.user(), h.user()
	h.secure(owner)
	h.secure(reviewer)
	h.join(reviewer, owner, []string{"break_glass:review"})
	status, raw := h.call(owner, "POST", "/branches", map[string]any{"name": "Emergency branch"}, nil)
	if status != 201 {
		t.Fatalf("branch %d %s", status, raw)
	}
	var branch struct {
		Data struct {
			ID ids.ID `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &branch)
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE organisation_id=$1 AND capability='hierarchy:read'`, owner.registered.OrganisationID); err != nil {
		t.Fatal(err)
	}
	if status, _ = h.call(owner, "GET", "/branches/"+branch.Data.ID.String(), nil, nil); status != 403 {
		t.Fatalf("ordinary grant unexpectedly present %d", status)
	}
	body := map[string]any{"scope_kind": "BRANCH", "scope_id": branch.Data.ID, "capabilities": []string{"hierarchy:read"}, "reason": "Emergency incident requires scoped inspection", "duration_seconds": 60}
	status, raw = h.call(owner, "POST", "/break-glass", body, nil)
	if status != 201 {
		t.Fatalf("emergency %d %s", status, raw)
	}
	var grant struct {
		Data struct {
			ID ids.ID `json:"break_glass_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &grant)
	if status, raw = h.call(owner, "GET", "/branches/"+branch.Data.ID.String(), nil, nil); status != 200 {
		t.Fatalf("emergency read %d %s", status, raw)
	}
	var count int
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE organisation_id=$1 AND action='BREAK_GLASS_USED'`, owner.registered.OrganisationID).Scan(&count); err != nil || count < 1 {
		t.Fatalf("use audit %d %v", count, err)
	}
	review := map[string]any{"reason": "Independent incident review completed and access revoked"}
	if status, _ = h.call(owner, "POST", "/break-glass/"+grant.Data.ID.String()+"/review", review, nil); status != 409 {
		t.Fatalf("self review %d", status)
	}
	if status, raw = h.call(reviewer, "POST", "/break-glass/"+grant.Data.ID.String()+"/review", review, nil); status != 200 {
		t.Fatalf("review %d %s", status, raw)
	}
	if status, _ = h.call(owner, "GET", "/branches/"+branch.Data.ID.String(), nil, nil); status != 403 {
		t.Fatalf("revoked emergency %d", status)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE break_glass_grants SET review_reason='rewritten' WHERE id=$1`, grant.Data.ID); err == nil {
		t.Fatal("review was mutable")
	}
	body["scope_kind"] = "COMPANY"
	body["scope_id"] = owner.registered.OrganisationID
	if status, _ = h.call(owner, "POST", "/break-glass", body, nil); status != 400 {
		t.Fatalf("company scope %d", status)
	}
}
