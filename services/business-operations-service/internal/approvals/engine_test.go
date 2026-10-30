package approvals

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
)

func fixture(t *testing.T) (*Request, Policy, workforce.Membership, workforce.Target, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	org := ids.New()
	scope := workforce.Scope{Organisation: org, Kind: "COMPANY", ID: org}
	target := workforce.Target{Scope: scope}
	p := Policy{ID: ids.New(), OrganisationID: org, Version: 1, Action: "STAFF_SCOPE_CHANGE", Risk: "HIGH", Active: true, EffectiveAt: now.Add(-time.Hour), SeparationOfDuties: true, DistinctAcrossLevels: true, Levels: []Level{{Capability: "staff:approve", Count: 1, RequireMFA: true}, {Capability: "roles:approve", Count: 1, RequireMFA: true}}}
	m := workforce.Membership{AccountID: ids.New(), OrganisationID: org, Status: "ACTIVE", RoleCapabilities: permissions.NewSet("staff:approve", "roles:approve"), Grants: []workforce.Grant{{Capability: "staff:approve", Scope: scope}, {Capability: "roles:approve", Scope: scope}}}
	r, err := NewRequest(p, ids.New(), scope, nil, "", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return r, p, m, target, now
}

func TestMultiLevelSeparationAndImmutablePolicy(t *testing.T) {
	r, p, m, target, now := fixture(t)
	p.Levels[0].Capability = "changed:capability"
	p.Version = 2
	if r.Snapshot().Policy.Version != 1 || r.Snapshot().Policy.Levels[0].Capability != "staff:approve" {
		t.Fatal("policy snapshot changed")
	}
	self := m
	self.AccountID = r.Snapshot().RequesterID
	if r.Decide(self, target, "APPROVED", "self approval", "self", nil, now, &now) != ErrDenied {
		t.Fatal("self approval allowed")
	}
	if r.Decide(m, target, "APPROVED", "reviewed evidence", "one", nil, now, nil) != ErrDenied {
		t.Fatal("MFA missing")
	}
	if err := r.Decide(m, target, "APPROVED", "reviewed evidence", "one", nil, now, &now); err != nil {
		t.Fatal(err)
	}
	if err := r.Decide(m, target, "APPROVED", "reviewed evidence", "one", nil, now, &now); err != nil {
		t.Fatal("idempotent retry", err)
	}
	if r.Decide(m, target, "REJECTED", "different decision", "one", nil, now, &now) != ErrConflict {
		t.Fatal("key reused for different decision")
	}
	if r.Decide(m, target, "APPROVED", "second level", "two", nil, now, &now) != ErrDenied {
		t.Fatal("one actor satisfied independent levels")
	}
	m.AccountID = ids.New()
	if err := r.Decide(m, target, "APPROVED", "second level", "two", nil, now, &now); err != nil {
		t.Fatal(err)
	}
	v := r.Snapshot()
	if v.State != "APPROVED" || len(v.Decisions) != 2 {
		t.Fatalf("%+v", v)
	}
	v.Decisions[0].Reason = "tampered"
	v.Policy.Levels[0].Capability = "tampered:permission"
	if r.Snapshot().Decisions[0].Reason == "tampered" || r.Snapshot().Policy.Levels[0].Capability == "tampered:permission" {
		t.Fatal("snapshot aliasing")
	}
}

func TestConcurrentDecisionAndExpiry(t *testing.T) {
	r, _, m, target, now := fixture(t)
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.Decide(m, target, "APPROVED", "concurrent review", ids.New().String(), nil, now, &now) == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 || len(r.Snapshot().Decisions) != 1 {
		t.Fatalf("duplicate concurrent decisions: %d", success.Load())
	}
	if !r.Expire(now.Add(2*time.Hour)) || r.Snapshot().State != "EXPIRED" || r.Expire(now.Add(3*time.Hour)) {
		t.Fatal("expiry not terminal/idempotent")
	}
	r, _, m, target, now = fixture(t)
	wrong := target
	wrong.Scope.Organisation = ids.New()
	if r.Decide(m, wrong, "APPROVED", "wrong organisation", "wrong", nil, now, &now) != ErrDenied {
		t.Fatal("cross-tenant approval")
	}
	if r.Cancel(r.Snapshot().RequesterID, "withdrawn", now) != nil || r.Snapshot().State != "CANCELLED" {
		t.Fatal("cancellation")
	}
}
