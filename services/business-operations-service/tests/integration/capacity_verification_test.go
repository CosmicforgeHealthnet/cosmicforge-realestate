package integration

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/organisation"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"testing"
	"time"
)

type capacityAuthorityFake struct {
	assertion verification.CapacityAssertion
}

func (f capacityAuthorityFake) SupportsCapacity(code string) bool { return code == f.assertion.Code }
func (f capacityAuthorityFake) Capacity(context.Context, ids.ID, string, ids.ID) (verification.CapacityAssertion, error) {
	return f.assertion, nil
}
func TestBusinessCapacityDeclarationEligibilityAndPackFailClosed(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	u := h.user()
	h.secure(u)
	status, raw := h.call(u, "POST", "/organisation/capacities", map[string]any{"code": "MANAGEMENT"}, nil)
	if status != 201 {
		t.Fatalf("declare %d %s", status, raw)
	}
	var capacity struct {
		Data struct {
			ID ids.ID `json:"capacity_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &capacity)
	status, raw = h.call(u, "POST", "/organisation/capability-packs", map[string]any{"code": "OPERATIONS", "required_capacities": []string{"MANAGEMENT"}, "enabled": true}, nil)
	if status != 200 {
		t.Fatalf("pack %d %s", status, raw)
	}
	var pack struct {
		Data struct {
			ID ids.ID `json:"capability_pack_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &pack)
	evaluate := func(want bool) {
		t.Helper()
		status, raw := h.call(u, "POST", "/organisation/capability-packs/"+pack.Data.ID.String()+"/evaluate", nil, nil)
		if status != 200 {
			t.Fatalf("evaluate %d %s", status, raw)
		}
		var out struct {
			Data struct {
				Allowed bool `json:"authorizes"`
			} `json:"data"`
		}
		_ = json.Unmarshal(raw, &out)
		if out.Data.Allowed != want {
			t.Fatalf("pack authorization %s", raw)
		}
	}
	evaluate(false)
	caseID := ids.New()
	body := map[string]any{"verification_id": caseID, "version": 1}
	if status, _ = h.call(u, "POST", "/organisation/capacities/"+capacity.Data.ID.String()+"/verification", body, nil); status != 503 {
		t.Fatalf("missing external contract %d", status)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	h.domain.CapacityAuthority = capacityAuthorityFake{verification.CapacityAssertion{OrganisationID: u.registered.OrganisationID, Code: "MANAGEMENT", CaseID: caseID, Status: "VERIFIED", ExpiresAt: &expiry, UpdatedAt: time.Now().UTC()}}
	if status, raw = h.call(u, "POST", "/organisation/capacities/"+capacity.Data.ID.String()+"/verification", body, nil); status != 200 {
		t.Fatalf("verified capacity %d %s", status, raw)
	}
	evaluate(false) // The organisation still lacks authoritative KYB.
	if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO verification_links(id,organisation_id,case_id,purpose,provider_status,expires_at) VALUES($1,$2,$3,'KYB','VERIFIED',$4)`, ids.New(), u.registered.OrganisationID, ids.New(), expiry); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE organisations SET state='ACTIVE',activated_at=now() WHERE id=$1`, u.registered.OrganisationID); err != nil {
		t.Fatal(err)
	}
	evaluate(true)
	if status, raw = h.call(u, "PATCH", "/organisation/capacities/"+capacity.Data.ID.String(), map[string]any{"version": 2, "status": "SUSPENDED", "reason": "Compliance review"}, nil); status != 200 {
		t.Fatalf("suspend %d %s", status, raw)
	}
	evaluate(false)
	h.service.Now = func() time.Time { return expiry.Add(time.Second) }
	evaluate(false)
}
func TestBusinessVerificationHintsCannotForgeTruthOrClearAdminRestriction(t *testing.T) {
	provider := &businessVerificationFake{}
	h := newBusinessHTTP(t, provider)
	u := h.user()
	h.secure(u)
	org := u.registered.OrganisationID
	c, err := provider.Create(t.Context(), verification.TypeBusiness, verification.Subject{Type: verification.SubjectBusiness, Domain: string(platform.DomainBusinessOperations), ID: org}, "kyb", "kyb")
	if err != nil {
		t.Fatal(err)
	}
	projection := organisation.Projection{Sealer: h.service.Sealer, Pool: h.db.Pool, Outbox: h.service.Outbox, Now: time.Now}
	if err = projection.Apply(t.Context(), org, &u.registered.AccountID, c); err != nil {
		t.Fatal(err)
	}
	w := workers.Verification{Projection: projection, API: provider}
	event, err := events.New(t.Context(), platform.VerificationService, "verification.verified.v1", "verification", c.VerificationID, map[string]any{"verification_id": c.VerificationID, "status": "VERIFIED"})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Handle(t.Context(), events.Delivery{Envelope: event}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = h.db.Pool.QueryRow(t.Context(), `SELECT state FROM organisations WHERE id=$1`, org).Scan(&state); err != nil || state != "KYB_PENDING" {
		t.Fatalf("hint forged activation %s %v", state, err)
	}
	expiry := time.Now().Add(time.Hour)
	provider.set(c.VerificationID, "VERIFIED", &expiry)
	if err = w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = h.db.Pool.QueryRow(t.Context(), `SELECT state FROM organisations WHERE id=$1`, org).Scan(&state); err != nil || state != "ACTIVE" {
		t.Fatalf("reconciliation %s %v", state, err)
	}
	if _, err = h.db.Pool.Exec(t.Context(), `UPDATE organisations SET state='RESTRICTED',restriction_source='ADMIN' WHERE id=$1`, org); err != nil {
		t.Fatal(err)
	}
	if err = w.Refresh(t.Context(), org, c.VerificationID); err != nil {
		t.Fatal(err)
	}
	if err = h.db.Pool.QueryRow(t.Context(), `SELECT state FROM organisations WHERE id=$1`, org).Scan(&state); err != nil || state != "RESTRICTED" {
		t.Fatalf("cleared independent restriction %s %v", state, err)
	}
	event.Producer = string(platform.NotificationService)
	event.EventID = ids.New()
	if err = w.Handle(t.Context(), events.Delivery{Envelope: event}); err == nil {
		t.Fatal("forged producer accepted")
	}
	old := c
	old.Status = "REJECTED"
	if err = projection.Apply(t.Context(), org, nil, old); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = h.db.Pool.QueryRow(t.Context(), `SELECT provider_status FROM verification_links WHERE organisation_id=$1 AND purpose='KYB'`, org).Scan(&status); err != nil || status != "VERIFIED" {
		t.Fatalf("stale assertion rewrote current truth %s %v", status, err)
	}
}
