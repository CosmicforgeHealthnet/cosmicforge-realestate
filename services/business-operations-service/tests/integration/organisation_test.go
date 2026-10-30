package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Only this provider fake sets verification truth. Business HTTP cannot set it.
type businessVerificationFake struct {
	mu      sync.Mutex
	cases   map[ids.ID]verification.Case
	keys    map[string]ids.ID
	down    bool
	creates int
}

func (f *businessVerificationFake) Create(_ context.Context, typ string, subject verification.Subject, ref, key string) (verification.Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return verification.Case{}, errors.New("provider unavailable")
	}
	if f.keys == nil {
		f.keys = map[string]ids.ID{}
		f.cases = map[ids.ID]verification.Case{}
	}
	if id, ok := f.keys[key]; ok {
		return f.cases[id], nil
	}
	c := verification.Case{VerificationID: ids.New(), Type: typ, Subject: subject, Status: "CREATED", UpdatedAt: time.Now().UTC()}
	f.cases[c.VerificationID] = c
	f.keys[key] = c.VerificationID
	f.creates++
	return c, nil
}
func (f *businessVerificationFake) Get(_ context.Context, id ids.ID) (verification.Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return verification.Case{}, errors.New("provider unavailable")
	}
	c, ok := f.cases[id]
	if !ok {
		return c, errors.New("not found")
	}
	return c, nil
}
func (f *businessVerificationFake) Submit(_ context.Context, id ids.ID, key string) (verification.Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return verification.Case{}, errors.New("provider unavailable")
	}
	c := f.cases[id]
	c.Status = "SUBMITTED"
	c.UpdatedAt = time.Now().UTC()
	f.cases[id] = c
	return c, nil
}
func (f *businessVerificationFake) Process(ctx context.Context, id ids.ID, key string) (verification.Case, error) {
	return f.Get(ctx, id)
}
func (f *businessVerificationFake) AttachEvidence(_ context.Context, id, media ids.ID, typ, key string) (verification.Evidence, error) {
	return verification.Evidence{EvidenceID: ids.New(), MediaID: media, EvidenceType: typ}, nil
}
func (f *businessVerificationFake) set(id ids.ID, status string, expiry *time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.cases[id]
	c.Status = status
	c.ExpiresAt = expiry
	c.UpdatedAt = time.Now().UTC()
	f.cases[id] = c
}

func TestBusinessOrganisationKYBAndTenantIsolation(t *testing.T) {
	provider := &businessVerificationFake{}
	h := newBusinessHTTP(t, provider)
	a, b := h.user(), h.user()
	h.secure(a)
	status, body := h.call(a, "GET", "/organisation", nil, map[string]string{"X-Organisation-ID": b.registered.OrganisationID.String()})
	if status != 200 {
		t.Fatalf("profile %d %s", status, body)
	}
	var profile struct {
		Data struct {
			ID      ids.ID `json:"organisation_id"`
			Version int64  `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Data.ID != a.registered.OrganisationID {
		t.Fatal("client organisation header overrode trusted identity")
	}
	patch := map[string]any{"version": profile.Data.Version, "registered_name": "Test Registered Ltd", "registration_number": "TEST-123", "jurisdiction": "IT", "currency": "EUR", "timezone": "Europe/Rome"}
	if status, body := h.call(a, "PATCH", "/organisation", patch, nil); status != 200 {
		t.Fatalf("profile completion %d %s", status, body)
	}
	if status, _ := h.call(a, "PATCH", "/organisation", patch, nil); status != 409 {
		t.Fatalf("stale profile version %d", status)
	}
	if status, _ := h.call(a, "PATCH", "/organisation", map[string]any{"version": 2, "state": "ACTIVE"}, nil); status != 400 {
		t.Fatal("frontend could declare verification truth")
	}
	status, body = h.call(a, "POST", "/organisation/verification", nil, nil)
	if status != 202 {
		t.Fatalf("create KYB %d %s", status, body)
	}
	var created struct {
		Data verification.Case `json:"data"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.call(a, "POST", "/organisation/verification", nil, nil); status != 202 {
		t.Fatalf("KYB retry %d", status)
	}
	provider.mu.Lock()
	creates := provider.creates
	provider.mu.Unlock()
	if creates != 1 {
		t.Fatal("duplicate provider case")
	}
	var state string
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT state FROM organisations WHERE id=$1`, a.registered.OrganisationID).Scan(&state); err != nil || state != "KYB_PENDING" {
		t.Fatalf("KYB state %s %v", state, err)
	}
	if status, body := h.call(a, "POST", "/organisation/verification/submit", nil, nil); status != 202 {
		t.Fatalf("KYB submit %d %s", status, body)
	}
	expiry := time.Now().UTC().Add(24 * time.Hour)
	provider.set(created.Data.VerificationID, "VERIFIED", &expiry)
	if status, body := h.call(a, "POST", "/organisation/verification/refresh", nil, nil); status != 200 {
		t.Fatalf("KYB refresh %d %s", status, body)
	}
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT state FROM organisations WHERE id=$1`, a.registered.OrganisationID).Scan(&state); err != nil || state != "ACTIVE" {
		t.Fatalf("verified state %s %v", state, err)
	}
	provider.set(created.Data.VerificationID, "EXPIRED", &expiry)
	if status, _ := h.call(a, "POST", "/organisation/verification/refresh", nil, nil); status != 200 {
		t.Fatalf("expiry propagation %d", status)
	}
	if status, _ := h.call(a, "PATCH", "/organisation", map[string]any{"version": 5, "name": "Restricted edit"}, nil); status != 403 {
		t.Fatalf("restricted write %d", status)
	}
	provider.mu.Lock()
	provider.down = true
	provider.mu.Unlock()
	if status, _ := h.call(a, "POST", "/organisation/verification/refresh", nil, nil); status != 503 {
		t.Fatalf("provider outage %d", status)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE staff_memberships SET status='SUSPENDED' WHERE account_id=$1 AND organisation_id=$2`, a.registered.AccountID, a.registered.OrganisationID); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.call(a, "GET", "/organisation", nil, nil); status != 403 {
		t.Fatalf("suspended membership access %d", status)
	}
}
