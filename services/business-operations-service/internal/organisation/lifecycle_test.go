package organisation

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"testing"
	"time"
)

func TestLifecycleAndAuthoritativeVerification(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	verification := Verification{CaseID: ids.New(), Status: "VERIFIED", ExpiresAt: &expiry}
	for _, transition := range [][2]State{{Draft, KYBPending}, {KYBPending, Active}, {Active, Restricted}, {Restricted, Active}, {Active, Suspended}, {Suspended, ClosurePending}, {ClosurePending, Closed}} {
		if Transition(transition[0], transition[1], verification, now, true) != nil {
			t.Fatalf("valid transition %v", transition)
		}
	}
	for _, transition := range [][2]State{{Draft, Active}, {Closed, Active}, {Active, Draft}, {KYBPending, Closed}, {ClosurePending, Active}} {
		if Transition(transition[0], transition[1], verification, now, true) == nil {
			t.Fatalf("invalid transition %v", transition)
		}
	}
	if Transition(ClosurePending, Closed, verification, now, false) == nil {
		t.Fatal("unsafe closure")
	}
	for _, status := range []string{"PENDING", "REJECTED", "EXPIRED", "RESTRICTED"} {
		v := verification
		v.Status = status
		if Transition(KYBPending, Active, v, now, false) == nil {
			t.Fatalf("unverified activation %s", status)
		}
	}
	if Transition(KYBPending, Active, verification, expiry, false) == nil {
		t.Fatal("expired verification activation")
	}
	c := Capacity{Code: "OPERATE", Status: "ACTIVE", Verification: verification, EffectiveAt: now, ExpiresAt: &expiry}
	if !EvaluatePack([]string{"OPERATE"}, []Capacity{c}, Active, now) {
		t.Fatal("valid pack denied")
	}
	if EvaluatePack([]string{"OPERATE", "UNSUPPORTED"}, []Capacity{c}, Active, now) || c.Authorizes("OPERATE", Restricted, now) || c.Authorizes("OPERATE", Active, expiry) {
		t.Fatal("unsafe capacity authorization")
	}
}
