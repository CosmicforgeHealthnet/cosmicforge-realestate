package integration

import (
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"testing"
	"time"
)

func TestBusinessAnnexTerminationHandoverAndDraftClosure(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, staff := h.user(), h.user()
	h.secure(owner)
	member := h.join(staff, owner, []string{"staff:read"})
	org := owner.registered.OrganisationID
	if status, _ := h.call(owner, "PATCH", "/staff/"+member.String(), map[string]any{"version": 1, "status": "TERMINATED", "reason": "End employment"}, nil); status != 409 {
		t.Fatalf("termination without handover %d", status)
	}
	var successor ids.ID
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2`, org, owner.registered.AccountID).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	status, raw := h.call(owner, "POST", "/staff/"+member.String()+"/handover", map[string]any{"version": 1, "successor_membership_id": successor, "reason": "Documented transfer of responsibilities", "evidence": []ids.ID{ids.New()}}, nil)
	if status != 201 {
		t.Fatalf("handover %d %s", status, raw)
	}
	var handover struct {
		Data struct {
			ID ids.ID `json:"handover_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &handover)
	if status, _ := h.call(staff, "GET", "/staff/me", nil, nil); status != 401 {
		t.Fatalf("departing access %d", status)
	}
	base := "/staff/" + member.String() + "/handover/" + handover.Data.ID.String()
	if status, _ := h.call(owner, "POST", base+"/complete", map[string]any{"version": 1, "reason": "Close transfer"}, nil); status != 409 {
		t.Fatalf("unacknowledged completion %d", status)
	}
	if status, raw = h.call(owner, "POST", base+"/acknowledge", map[string]any{"version": 1, "reason": "Successor confirms receipt of responsibilities"}, nil); status != 200 {
		t.Fatalf("ack %d %s", status, raw)
	}
	if status, raw = h.call(owner, "POST", base+"/complete", map[string]any{"version": 2, "reason": "Administrator confirms handover completion"}, nil); status != 200 {
		t.Fatalf("complete %d %s", status, raw)
	}
	if status, raw = h.call(owner, "PATCH", "/staff/"+member.String(), map[string]any{"version": 2, "status": "TERMINATED", "reason": "Completed handover, employment ended"}, nil); status != 200 {
		t.Fatalf("terminate %d %s", status, raw)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE staff_handovers SET completion_reason='changed' WHERE id=$1`, handover.Data.ID); err == nil {
		t.Fatal("completed handover was mutable")
	}
	// No later-domain inventory is fabricated for a never-activated draft.
	if status, raw = h.call(owner, "POST", "/organisation/lifecycle", map[string]any{"version": 1, "state": "CLOSURE_PENDING", "reason": "Onboarding abandoned"}, nil); status != 200 {
		t.Fatalf("closure pending %d %s", status, raw)
	}
	if status, raw = h.call(owner, "POST", "/organisation/lifecycle", map[string]any{"version": 2, "state": "CLOSED", "reason": "No operations or outstanding obligations"}, nil); status != 200 {
		t.Fatalf("closed %d %s", status, raw)
	}
	if status, _ := h.call(owner, "GET", "/account", nil, nil); status != 401 {
		t.Fatalf("closed organisation session %d", status)
	}
}
func TestBusinessChannelPolicyAndDelegatedApprovalSeparation(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, source, b, c := h.user(), h.user(), h.user(), h.user()
	for _, u := range []*businessUser{owner, source, b, c} {
		h.secure(u)
	}
	h.join(source, owner, []string{"approvals:read", "approvals:create", "approvals:decide", "staff:approve", "delegations:write"})
	bm := h.join(b, owner, nil)
	cm := h.join(c, owner, nil)
	policy := map[string]any{"action": "STAFF_ROLE_CHANGE", "risk": "HIGH", "scope_kind": "COMPANY", "channel": "DIRECT", "levels": []map[string]any{{"capability": "staff:approve", "count": 1, "require_mfa": true}, {"capability": "staff:approve", "count": 1, "require_mfa": true}}}
	if status, raw := h.call(owner, "POST", "/approval-policies", policy, nil); status != 201 {
		t.Fatalf("policy %d %s", status, raw)
	}
	request := map[string]any{"action": "STAFF_ROLE_CHANGE", "risk": "HIGH", "scope_kind": "COMPANY", "channel": "OTA", "operation": map[string]any{"resource": "role_template", "name": "Reader", "capabilities": []string{"hierarchy:read"}}, "reason": "Independent privilege review"}
	if status, _ := h.call(owner, "POST", "/approvals", request, nil); status != 403 {
		t.Fatalf("wrong channel %d", status)
	}
	request["channel"] = "DIRECT"
	now := time.Now().UTC()
	h.service.Now = func() time.Time { return now }
	expiry := now.Add(time.Hour)
	for _, member := range []ids.ID{bm, cm} {
		for _, cap := range []string{"approvals:decide", "staff:approve"} {
			body := map[string]any{"delegate_membership_id": member, "grant": map[string]any{"capability": cap, "scope_kind": "COMPANY", "effective_at": now, "expires_at": expiry, "require_mfa": true}, "reason": "Temporary independent approval cover"}
			if status, raw := h.call(source, "POST", "/delegations", body, nil); status != 201 {
				t.Fatalf("delegate %s %d %s", cap, status, raw)
			}
		}
	}
	status, raw := h.call(owner, "POST", "/approvals", request, nil)
	if status != 201 {
		t.Fatalf("request %d %s", status, raw)
	}
	var approval struct {
		Data struct {
			Approval struct {
				ID ids.ID `json:"id"`
			} `json:"approval"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &approval)
	path := "/approvals/" + approval.Data.Approval.ID.String() + "/decision"
	decision := map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Independent evidence checked"}
	if status, raw := h.call(b, "POST", path, decision, nil); status != 200 {
		t.Fatalf("delegated first approval %d %s", status, raw)
	}
	decision["version"] = 2
	decision["level"] = 1
	if status, _ := h.call(c, "POST", path, decision, nil); status != 403 {
		t.Fatalf("two proxies for one authority %d", status)
	}
	if status, _ := h.call(source, "POST", path, decision, nil); status != 403 {
		t.Fatalf("source authority reused after proxy %d", status)
	}
	// A delegate cannot approve its own delegator's request.
	status, raw = h.call(source, "POST", "/approvals", request, nil)
	if status != 201 {
		t.Fatalf("source request %d %s", status, raw)
	}
	_ = json.Unmarshal(raw, &approval)
	decision["version"] = 1
	decision["level"] = 0
	if status, _ := h.call(b, "POST", "/approvals/"+approval.Data.Approval.ID.String()+"/decision", decision, nil); status != 403 {
		t.Fatalf("delegator self-approval by proxy %d", status)
	}
}
