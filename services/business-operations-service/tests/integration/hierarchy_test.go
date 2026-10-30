package integration

import (
	"encoding/json"
	"testing"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func TestBusinessHierarchyScopedAccessAndIdempotency(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	a, b := h.user(), h.user()
	create := func(path string, body any, key string) ids.ID {
		t.Helper()
		status, raw := h.call(a, "POST", path, body, map[string]string{"Idempotency-Key": key})
		if status != 201 {
			t.Fatalf("create %s %d %s", path, status, raw)
		}
		var out struct {
			Data struct {
				ID ids.ID `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out.Data.ID
	}
	first := create("/branches", map[string]any{"name": "First"}, "create-first-branch")
	repeated := create("/branches", map[string]any{"name": "First"}, "create-first-branch")
	if first != repeated {
		t.Fatal("duplicate branch after retry")
	}
	if status, _ := h.call(a, "POST", "/branches", map[string]any{"name": "Changed"}, map[string]string{"Idempotency-Key": "create-first-branch"}); status != 409 {
		t.Fatalf("idempotency conflict %d", status)
	}
	second := create("/branches", map[string]any{"name": "Second"}, "create-second-branch")
	department := create("/departments", map[string]any{"name": "Operations", "branch_id": first}, "create-department")
	_ = create("/teams", map[string]any{"name": "Front Desk", "department_id": department}, "create-team")
	if status, _ := h.call(a, "POST", "/departments", map[string]any{"name": "Foreign", "branch_id": b.registered.OrganisationID}, nil); status != 404 {
		t.Fatalf("foreign hierarchy parent %d", status)
	}
	role, member := ids.New(), ids.New()
	org := a.registered.OrganisationID
	if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO role_templates(id,organisation_id,role_key,name,version) VALUES($1,$2,$1,'Branch Viewer',1)`, role, org); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO role_permissions(role_id,permission) VALUES($1,'hierarchy:read')`, role); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO staff_memberships(id,organisation_id,account_id,role_id,status,effective_at,scope_id) VALUES($1,$2,$3,$4,'ACTIVE',now(),$2)`, member, org, b.registered.AccountID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES($1,$2,$3,'hierarchy:read','BRANCH',$4,true,now())`, ids.New(), org, member, first); err != nil {
		t.Fatal(err)
	}
	logged, err := h.service.Login(t.Context(), businessauth.Login{Email: b.email, Password: b.password, OrganisationID: org}, "branch-viewer")
	if err != nil {
		t.Fatal(err)
	}
	b.tokens = *logged.Tokens
	b.registered.OrganisationID = org
	status, raw := h.call(b, "GET", "/branches", nil, nil)
	if status != 200 {
		t.Fatalf("branch list %d %s", status, raw)
	}
	var list struct {
		Data struct {
			Items []struct {
				ID ids.ID `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data.Items) != 1 || list.Data.Items[0].ID != first {
		t.Fatalf("cross-branch listing: %s", raw)
	}
	if status, _ := h.call(b, "GET", "/departments/"+department.String(), nil, nil); status != 200 {
		t.Fatalf("inherited department access %d", status)
	}
	if status, _ := h.call(b, "GET", "/branches/"+second.String(), nil, nil); status != 403 {
		t.Fatalf("cross-branch detail %d", status)
	}
	if status, _ := h.call(b, "POST", "/branches", map[string]any{"name": "Escalated"}, nil); status != 403 {
		t.Fatalf("read-only create %d", status)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `DELETE FROM role_permissions WHERE role_id=$1`, role); err == nil {
		t.Fatal("historical role mapping was mutable")
	}
	h.secure(a)
	if status, raw := h.call(a, "POST", "/roles/"+role.String()+"/activation", map[string]any{"version": 1, "active": false}, nil); status != 200 {
		t.Fatalf("role revocation %d %s", status, raw)
	}
	if status, _ := h.call(b, "GET", "/branches", nil, nil); status != 403 {
		t.Fatalf("role revocation ignored: %d", status)
	}
}
