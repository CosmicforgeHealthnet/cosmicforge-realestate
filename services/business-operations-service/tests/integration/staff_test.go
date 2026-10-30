package integration

import (
	"encoding/json"
	"testing"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func TestBusinessStaffInvitationAcceptanceScopedAccessAndSuspension(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner := h.user()
	h.secure(owner)
	create := func(path string, body any) []byte {
		t.Helper()
		status, raw := h.call(owner, "POST", path, body, nil)
		if status != 201 {
			t.Fatalf("create %s %d %s", path, status, raw)
		}
		return raw
	}
	branchRaw := create("/branches", map[string]any{"name": "Permitted branch"})
	var branch struct {
		Data struct {
			ID ids.ID `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(branchRaw, &branch); err != nil {
		t.Fatal(err)
	}
	_ = create("/branches", map[string]any{"name": "Isolated branch"})
	roleRaw := create("/roles", map[string]any{"name": "Branch Reader", "capabilities": []string{"hierarchy:read", "staff:read"}})
	var role struct {
		Data struct {
			ID ids.ID `json:"role_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(roleRaw, &role); err != nil {
		t.Fatal(err)
	}
	inviteBody := map[string]any{"email": "invited-staff@example.test", "role_id": role.Data.ID, "scope_kind": "BRANCH", "scope_id": branch.Data.ID, "kind": "EMPLOYEE", "reason": "Staff onboarding", "grants": []map[string]any{{"capability": "hierarchy:read", "scope_kind": "BRANCH", "scope_id": branch.Data.ID, "inherit": true}, {"capability": "staff:read", "scope_kind": "BRANCH", "scope_id": branch.Data.ID, "inherit": true}}}
	invitationRaw := create("/staff", inviteBody)
	var invitation struct {
		Data struct {
			ID      ids.ID `json:"invitation_id"`
			Staff   ids.ID `json:"staff_id"`
			Account ids.ID `json:"account_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(invitationRaw, &invitation); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.call(owner, "POST", "/staff", inviteBody, nil); status != 409 {
		t.Fatalf("duplicate invitation %d", status)
	}
	if _, err := h.service.Login(t.Context(), businessauth.Login{Email: "invited-staff@example.test", Password: "correct horse battery"}, "invited-client"); err == nil {
		t.Fatal("provisional account could log in")
	}
	var encrypted []byte
	var key string
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT encrypted_delivery,delivery_key_id FROM staff_invitations WHERE id=$1`, invitation.Data.ID).Scan(&encrypted, &key); err != nil {
		t.Fatal(err)
	}
	token, err := h.service.Sealer.Open(key, encrypted, []byte("business-invitation:"+invitation.Data.ID.String()))
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"invitation_token": string(token), "password": "correct horse battery", "display_name": "Invited Staff"}
	status, raw := h.call(nil, "POST", "/staff/invitations/accept", body, nil)
	if status != 200 {
		t.Fatalf("accept %d %s", status, raw)
	}
	if status, _ := h.call(nil, "POST", "/staff/invitations/accept", body, nil); status != 409 {
		t.Fatalf("single-use invitation %d", status)
	}
	logged, err := h.service.Login(t.Context(), businessauth.Login{Email: "invited-staff@example.test", Password: "correct horse battery", OrganisationID: owner.registered.OrganisationID}, "staff-client")
	if err != nil {
		t.Fatal(err)
	}
	staff := &businessUser{registered: businessauth.Registered{AccountID: invitation.Data.Account, OrganisationID: owner.registered.OrganisationID}, tokens: *logged.Tokens, email: "invited-staff@example.test", password: "correct horse battery"}
	status, raw = h.call(staff, "GET", "/branches", nil, nil)
	if status != 200 {
		t.Fatalf("scoped listing %d %s", status, raw)
	}
	var listing struct {
		Data struct {
			Items []struct {
				ID ids.ID `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Data.Items) != 1 || listing.Data.Items[0].ID != branch.Data.ID {
		t.Fatalf("branch isolation %s", raw)
	}
	if status, _ := h.call(staff, "GET", "/staff/me", nil, nil); status != 200 {
		t.Fatalf("staff self %d", status)
	}
	patch := map[string]any{"version": 2, "status": "SUSPENDED", "reason": "Security containment"}
	status, raw = h.call(owner, "PATCH", "/staff/"+invitation.Data.Staff.String(), patch, nil)
	if status != 200 {
		t.Fatalf("suspension %d %s", status, raw)
	}
	if status, _ := h.call(staff, "GET", "/branches", nil, nil); status != 401 {
		t.Fatalf("suspended staff retained active access %d", status)
	}
	if _, _, err := h.service.Authenticate(t.Context(), staff.tokens.AccessToken); err == nil {
		t.Fatal("membership suspension failed to revoke selected-organisation session")
	}
}
