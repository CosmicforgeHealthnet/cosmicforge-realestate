package integration

import (
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/organisation"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"testing"
	"time"
)

func TestBusinessKYBExpiryRenewalIsIdempotentAndAuthoritative(t *testing.T) {
	provider := &businessVerificationFake{}
	h := newBusinessHTTP(t, provider)
	u := h.user()
	h.secure(u)
	org := u.registered.OrganisationID
	old, err := provider.Create(t.Context(), verification.TypeBusiness, verification.Subject{Type: verification.SubjectBusiness, Domain: string(platform.DomainBusinessOperations), ID: org}, "initial", "initial")
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour)
	provider.set(old.VerificationID, "VERIFIED", &expiry)
	old, err = provider.Get(t.Context(), old.VerificationID)
	if err != nil {
		t.Fatal(err)
	}
	projection := organisation.Projection{Sealer: h.service.Sealer, Pool: h.db.Pool, Outbox: h.service.Outbox, Now: time.Now}
	if err = projection.Apply(t.Context(), org, &u.registered.AccountID, old); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Hour)
	provider.set(old.VerificationID, "EXPIRED", &expired)
	old, err = provider.Get(t.Context(), old.VerificationID)
	if err != nil {
		t.Fatal(err)
	}
	if err = projection.Apply(t.Context(), org, nil, old); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"previous_verification_id": old.VerificationID}
	status, raw := h.call(u, "POST", "/organisation/verification/renew", body, nil)
	if status != 202 {
		t.Fatalf("renew %d %s", status, raw)
	}
	var result struct {
		Data verification.Case `json:"data"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	renewed := result.Data
	if renewed.VerificationID == old.VerificationID || renewed.SupersedesID == nil || *renewed.SupersedesID != old.VerificationID {
		t.Fatal("renewal broke case lineage")
	}
	if status, raw = h.call(u, "POST", "/organisation/verification/renew", body, nil); status != 202 {
		t.Fatalf("renew retry %d %s", status, raw)
	}
	provider.mu.Lock()
	cases := len(provider.cases)
	provider.mu.Unlock()
	if cases != 2 {
		t.Fatalf("duplicate renewal created %d cases", cases)
	}
	var state string
	if err = h.db.Pool.QueryRow(t.Context(), `SELECT state FROM organisations WHERE id=$1`, org).Scan(&state); err != nil || state != "RESTRICTED" {
		t.Fatalf("renewal fabricated approval %s %v", state, err)
	}
	expiry = time.Now().Add(2 * time.Hour)
	provider.set(renewed.VerificationID, "VERIFIED", &expiry)
	if status, raw = h.call(u, "POST", "/organisation/verification/refresh", nil, nil); status != 200 {
		t.Fatalf("renew verification %d %s", status, raw)
	}
	if err = h.db.Pool.QueryRow(t.Context(), `SELECT state FROM organisations WHERE id=$1`, org).Scan(&state); err != nil || state != "ACTIVE" {
		t.Fatalf("verified renewal %s %v", state, err)
	}
	if _, err = h.db.Pool.Exec(t.Context(), `DELETE FROM verification_link_history WHERE organisation_id=$1`, org); err == nil {
		t.Fatal("verification lineage could be erased")
	}
}
