package integration

import (
	"context"
	"encoding/json"
	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"sync"
	"testing"
	"time"
)

func TestBusinessAccessKeysExpiryPasswordAndRateLimits(t *testing.T) {
	service, db := newBusinessAuth(t)
	ctx := context.Background()
	registered, err := service.Register(ctx, businessauth.Registration{Email: "security@example.test", Password: "correct horse battery", DisplayName: "Security test", OrganisationName: "Test business"}, "security-register")
	if err != nil {
		t.Fatal(err)
	}
	logged, err := service.Login(ctx, businessauth.Login{Email: "security@example.test", Password: "correct horse battery", OrganisationID: registered.OrganisationID}, "security-client")
	if err != nil {
		t.Fatal(err)
	}
	old := testkit.SigningKey(t, "retained-business-key")
	fresh := testkit.SigningKey(t, "current-business-key")
	now := time.Now().UTC()
	verifier := func(keys *jwt.StaticKeys, clock time.Time) *sharedauth.Verifier {
		v, err := sharedauth.NewVerifier(sharedauth.VerifierConfig{Issuers: []sharedauth.TrustedIssuer{{Issuer: string(platform.BusinessOperationsService), Domain: platform.DomainBusinessOperations, Keys: keys}}, Now: func() time.Time { return clock }})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	mint := func(key jwt.SigningKey, clock time.Time) string {
		i, err := sharedauth.NewIssuer(sharedauth.IssuerConfig{Issuer: string(platform.BusinessOperationsService), Key: key, TTL: time.Minute, Now: func() time.Time { return clock }})
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := i.Issue(sharedauth.IssueRequest{Subject: registered.AccountID, SessionID: logged.Tokens.SessionID, OrganizationID: registered.OrganisationID})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	service.Verifier = verifier(jwt.NewStaticKeys().Add(string(platform.BusinessOperationsService), old.KID, old.PublicKey()).Add(string(platform.BusinessOperationsService), fresh.KID, fresh.PublicKey()), now)
	for _, token := range []string{mint(old, now), mint(fresh, now)} {
		if _, _, err := service.Authenticate(ctx, token); err != nil {
			t.Fatal("retained key failed", err)
		}
	}
	if _, _, err := service.Authenticate(ctx, mint(testkit.SigningKey(t, fresh.KID), now)); err == nil {
		t.Fatal("untrusted signature accepted")
	}
	service.Verifier = verifier(jwt.NewStaticKeys().Add(string(platform.BusinessOperationsService), fresh.KID, fresh.PublicKey()), now)
	if _, _, err := service.Authenticate(ctx, mint(old, now)); err == nil {
		t.Fatal("retired key accepted after removal")
	}
	if _, _, err := service.Authenticate(ctx, mint(fresh, now.Add(-5*time.Minute))); err == nil {
		t.Fatal("expired access accepted")
	}
	if err := service.ChangePassword(ctx, registered.AccountID, logged.Tokens.SessionID, "correct horse battery", "a different secure passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(ctx, logged.Tokens.RefreshToken); err == nil {
		t.Fatal("password change left sessions active")
	}
	if _, err := service.Login(ctx, businessauth.Login{Email: "security@example.test", Password: "correct horse battery"}, "after-password-change"); err == nil {
		t.Fatal("old password accepted")
	}
	active, err := service.Login(ctx, businessauth.Login{Email: "security@example.test", Password: "a different secure passphrase"}, "new-password")
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return time.Now().Add(100 * 24 * time.Hour) }
	if _, err := service.Refresh(ctx, active.Tokens.RefreshToken); err == nil {
		t.Fatal("expired refresh accepted")
	}
	service.Now = time.Now
	for i := 0; i < 12; i++ {
		_, _ = service.Login(ctx, businessauth.Login{Email: "security@example.test", Password: "wrong password"}, "rate-client")
	}
	var attempts int
	if err := db.Pool.QueryRow(ctx, `SELECT max(attempts) FROM business_auth_limits`).Scan(&attempts); err != nil || attempts < 11 {
		t.Fatalf("rate counter %d %v", attempts, err)
	}
}
func TestBusinessConcurrentRoleVersionsAndInvitationCreation(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner := h.user()
	h.secure(owner)
	status, raw := h.call(owner, "POST", "/roles", map[string]any{"name": "Concurrent reader", "capabilities": []string{"staff:read"}}, nil)
	if status != 201 {
		t.Fatalf("role %d %s", status, raw)
	}
	var role struct {
		Data struct {
			ID ids.ID `json:"role_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &role)
	runTwo := func(fn func(int) int) (int, int) {
		var wg sync.WaitGroup
		results := [2]int{}
		for i := range results {
			wg.Add(1)
			go func(i int) { defer wg.Done(); results[i] = fn(i) }(i)
		}
		wg.Wait()
		return results[0], results[1]
	}
	a, b := runTwo(func(i int) int {
		status, _ := h.call(owner, "PATCH", "/roles/"+role.Data.ID.String(), map[string]any{"version": 1, "name": "Concurrent role version", "capabilities": []string{"staff:read"}}, map[string]string{"Idempotency-Key": ids.New().String()})
		return status
	})
	if !((a == 201 && b == 409) || (a == 409 && b == 201)) {
		t.Fatalf("role version race %d %d", a, b)
	}
	invitation := map[string]any{"email": "race-invite@example.test", "role_id": role.Data.ID, "scope_kind": "COMPANY", "reason": "Concurrent staff invitation", "grants": []map[string]any{{"capability": "staff:read", "scope_kind": "COMPANY", "inherit": true}}}
	a, b = runTwo(func(i int) int {
		status, _ := h.call(owner, "POST", "/staff", invitation, map[string]string{"Idempotency-Key": ids.New().String()})
		return status
	})
	if !((a == 201 && b == 409) || (a == 409 && b == 201)) {
		t.Fatalf("duplicate invite race %d %d", a, b)
	}
}

func TestBusinessConcurrentApprovalDecisionsCommitOnce(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, b, c := h.user(), h.user(), h.user()
	for _, u := range []*businessUser{owner, b, c} {
		h.secure(u)
	}
	for _, u := range []*businessUser{b, c} {
		h.join(u, owner, []string{"approvals:decide", "staff:approve"})
	}
	policy := map[string]any{"action": "STAFF_ROLE_CHANGE", "risk": "HIGH", "scope_kind": "COMPANY", "levels": []map[string]any{{"capability": "staff:approve", "count": 1, "require_mfa": true}}}
	if status, raw := h.call(owner, "POST", "/approval-policies", policy, nil); status != 201 {
		t.Fatalf("policy %d %s", status, raw)
	}
	status, raw := h.call(owner, "POST", "/approvals", map[string]any{"action": "STAFF_ROLE_CHANGE", "scope_kind": "COMPANY", "operation": map[string]any{"resource": "role_template", "name": "Reader", "capabilities": []string{"staff:read"}}, "reason": "Concurrent decision certification"}, nil)
	if status != 201 {
		t.Fatalf("request %d %s", status, raw)
	}
	var response struct {
		Data struct {
			Approval struct {
				ID ids.ID `json:"id"`
			} `json:"approval"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := [2]int{}
	for i, u := range []*businessUser{b, c} {
		wg.Add(1)
		go func(i int, u *businessUser) {
			defer wg.Done()
			results[i], _ = h.call(u, "POST", "/approvals/"+response.Data.Approval.ID.String()+"/decision", map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Evidence confirmed independently"}, nil)
		}(i, u)
	}
	wg.Wait()
	if !((results[0] == 200 && results[1] == 409) || (results[0] == 409 && results[1] == 200)) {
		t.Fatalf("concurrent decisions %v", results)
	}
	var count int
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM approval_decisions WHERE request_id=$1`, response.Data.Approval.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("decision count %d %v", count, err)
	}
}

func TestBusinessConfiguredPolicySealsOnboardingPrivilegeException(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner := h.user()
	h.secure(owner)
	policy := map[string]any{"action": "STAFF_ROLE_CHANGE", "risk": "HIGH", "scope_kind": "COMPANY", "levels": []map[string]any{{"capability": "roles:approve", "count": 1, "require_mfa": true}}}
	status, raw := h.call(owner, "POST", "/approval-policies", policy, nil)
	if status != 201 {
		t.Fatalf("policy %d %s", status, raw)
	}
	var p struct {
		Data struct {
			Policy struct {
				ID ids.ID `json:"id"`
			} `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	create := map[string]any{"name": "Forbidden bootstrap bypass", "capabilities": []string{"staff:read"}}
	if status, _ := h.call(owner, "POST", "/roles", create, nil); status != 403 {
		t.Fatalf("configured SoD bypass %d", status)
	}
	if status, raw := h.call(owner, "POST", "/approval-policies/"+p.Data.Policy.ID.String()+"/activation", map[string]any{"version": 1, "active": false}, nil); status != 200 {
		t.Fatalf("deactivate %d %s", status, raw)
	}
	if status, _ := h.call(owner, "POST", "/roles", create, nil); status != 403 {
		t.Fatalf("policy deactivation reopened bootstrap %d", status)
	}
}
