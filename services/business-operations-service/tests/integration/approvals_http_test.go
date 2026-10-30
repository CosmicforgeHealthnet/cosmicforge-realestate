package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func TestBusinessDurableMultiLevelApprovalsAndPolicyVersion(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, b, c := h.user(), h.user(), h.user()
	h.secure(owner)
	h.secure(b)
	h.secure(c)
	capabilities := []string{"approvals:read", "approvals:decide", "staff:approve", "roles:approve"}
	status, raw := h.call(owner, "POST", "/roles", map[string]any{"name": "Independent Approver", "capabilities": capabilities}, nil)
	if status != 201 {
		t.Fatalf("role %d %s", status, raw)
	}
	var role struct {
		Data struct {
			ID ids.ID `json:"role_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &role); err != nil {
		t.Fatal(err)
	}
	for _, u := range []*businessUser{b, c} {
		membership := ids.New()
		org := owner.registered.OrganisationID
		if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO staff_memberships(id,organisation_id,account_id,role_id,status,effective_at,scope_id) VALUES($1,$2,$3,$4,'ACTIVE',now(),$2)`, membership, org, u.registered.AccountID, role.Data.ID); err != nil {
			t.Fatal(err)
		}
		for _, capability := range capabilities {
			if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES($1,$2,$3,$4,'COMPANY',$2,true,now())`, ids.New(), org, membership, capability); err != nil {
				t.Fatal(err)
			}
		}
		status, raw := h.call(u, "POST", "/auth/organisation", map[string]any{"organisation_id": org}, nil)
		if status != 200 {
			t.Fatalf("select membership %d %s", status, raw)
		}
		var selected struct {
			Data struct {
				Token string `json:"access_token"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &selected); err != nil {
			t.Fatal(err)
		}
		u.tokens.AccessToken = selected.Data.Token
		u.registered.OrganisationID = org
	}
	policyBody := map[string]any{"action": approvals.ActionStaffRoleChange, "risk": "HIGH", "scope_kind": "COMPANY", "levels": []map[string]any{{"capability": "staff:approve", "count": 1, "require_mfa": true}, {"capability": "roles:approve", "count": 1, "require_mfa": true}}}
	status, raw = h.call(owner, "POST", "/approval-policies", policyBody, nil)
	if status != 201 {
		t.Fatalf("policy %d %s", status, raw)
	}
	var policy struct {
		Data struct {
			Policy approvals.Policy `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	requestBody := map[string]any{"action": approvals.ActionStaffRoleChange, "risk": "HIGH", "scope_kind": "COMPANY", "operation": map[string]any{"staff_id": b.registered.AccountID, "version": 1, "role_id": role.Data.ID}, "reason": "Controlled role reassignment"}
	status, raw = h.call(owner, "POST", "/approvals", requestBody, nil)
	if status != 201 {
		t.Fatalf("request %d %s", status, raw)
	}
	var created struct {
		Data struct {
			Approval approvals.View `json:"approval"`
			Version  int64          `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	id := created.Data.Approval.ID
	// A later policy version cannot rewrite an existing request's snapshot.
	policyBody["version"] = 1
	status, raw = h.call(owner, "PATCH", "/approval-policies/"+policy.Data.Policy.ID.String(), policyBody, nil)
	if status != 201 {
		t.Fatalf("policy version %d %s", status, raw)
	}
	status, raw = h.call(owner, "GET", "/approvals/"+id.String(), nil, nil)
	if status != 200 {
		t.Fatalf("request detail %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Approval.Policy.Version != 1 {
		t.Fatal("request silently changed policy version")
	}
	decision := map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Evidence reviewed"}
	if status, _ := h.call(owner, "POST", "/approvals/"+id.String()+"/decision", decision, nil); status != 403 {
		t.Fatalf("self approval %d", status)
	}
	headers := map[string]string{"Idempotency-Key": "first-independent-decision"}
	status, raw = h.call(b, "POST", "/approvals/"+id.String()+"/decision", decision, headers)
	if status != 200 {
		t.Fatalf("first level %d %s", status, raw)
	}
	if status, _ := h.call(b, "POST", "/approvals/"+id.String()+"/decision", decision, headers); status != 200 {
		t.Fatalf("decision retry %d", status)
	}
	decision["version"] = 2
	decision["level"] = 1
	if status, _ := h.call(b, "POST", "/approvals/"+id.String()+"/decision", decision, nil); status != 403 {
		t.Fatalf("same actor across independent levels %d", status)
	}
	status, raw = h.call(c, "POST", "/approvals/"+id.String()+"/decision", decision, nil)
	if status != 200 {
		t.Fatalf("second level %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Approval.State != "APPROVED" || len(created.Data.Approval.Decisions) != 2 {
		t.Fatalf("final approval %s", raw)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE approval_decisions SET reason='tampered' WHERE request_id=$1`, id); err == nil {
		t.Fatal("decision history mutable")
	}
	// After activation the representative no longer has the onboarding setup
	// exception. A concrete role mutation must consume its exact approved intent.
	if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO verification_links(id,organisation_id,case_id,purpose,provider_status,expires_at) VALUES($1,$2,$3,'KYB','VERIFIED',now()+interval '30 days')`, ids.New(), owner.registered.OrganisationID, ids.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE organisations SET state='ACTIVE',activated_at=now() WHERE id=$1`, owner.registered.OrganisationID); err != nil {
		t.Fatal(err)
	}
	operation := map[string]any{"resource": "role_template", "name": "Approved Template", "capabilities": []string{"hierarchy:read"}, "senior_emergency_eligible": false}
	boundRequest := map[string]any{"action": approvals.ActionStaffRoleChange, "risk": "HIGH", "scope_kind": "COMPANY", "operation": operation, "reason": "Reviewed role definition"}
	status, raw = h.call(owner, "POST", "/approvals", boundRequest, nil)
	if status != 201 {
		t.Fatalf("bound request %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	bound := created.Data.Approval.ID
	for i, u := range []*businessUser{b, c} {
		status, raw = h.call(u, "POST", "/approvals/"+bound.String()+"/decision", map[string]any{"version": i + 1, "level": i, "outcome": "APPROVED", "reason": "Independent role review"}, nil)
		if status != 200 {
			t.Fatalf("bound decision %d %s", status, raw)
		}
	}
	roleWrite := map[string]any{"name": "Tampered Template", "capabilities": []string{"hierarchy:read"}, "approval_id": bound}
	if status, _ := h.call(owner, "POST", "/roles", roleWrite, nil); status != 403 {
		t.Fatalf("approval authorized different operation %d", status)
	}
	roleWrite["name"] = "Approved Template"
	if status, raw := h.call(owner, "POST", "/roles", roleWrite, nil); status != 201 {
		t.Fatalf("approved role execution %d %s", status, raw)
	}
	if status, _ := h.call(owner, "POST", "/roles", roleWrite, nil); status != 403 {
		t.Fatalf("approval reused for another command %d", status)
	}
	requestBody["draft"] = true
	status, raw = h.call(owner, "POST", "/approvals", requestBody, nil)
	if status != 201 {
		t.Fatalf("draft %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	draft := created.Data.Approval.ID
	status, raw = h.call(owner, "POST", "/approvals/"+draft.String()+"/submit", map[string]any{"version": 1, "reason": "Submit for review"}, nil)
	if status != 200 {
		t.Fatalf("draft submit %d %s", status, raw)
	}
	lifecycle := workers.Lifecycle{Pool: h.db.Pool, Outbox: h.service.Outbox, Now: func() time.Time { return time.Now().Add(48 * time.Hour) }}
	if err := lifecycle.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT state FROM approval_requests WHERE id=$1`, draft).Scan(&state); err != nil || state != "EXPIRED" {
		t.Fatalf("deadline expiry %s %v", state, err)
	}
}
