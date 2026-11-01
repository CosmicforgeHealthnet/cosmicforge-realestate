package reservations

import (
	"testing"
	"time"
)

func TestOverbookingLimitNeverRoundsAuthorityUp(t *testing.T) {
	for _, tc := range []struct {
		capacity, absolute int
		bps                int64
		want               int
	}{{1, 10, 1000, 0}, {1, 1, 10000, 1}, {100, 5, 100, 1}, {100, 5, 10000, 5}, {0, 5, 10000, 0}, {100, 1, 10001, 0}} {
		p := OverbookingPolicy{MaxQuantity: tc.absolute, MaxCapacityBPS: tc.bps}
		if got := p.Limit(tc.capacity); got != tc.want {
			t.Fatalf("capacity=%d bps=%d absolute=%d got=%d want=%d", tc.capacity, tc.bps, tc.absolute, got, tc.want)
		}
	}
}
func TestOverbookingPolicyRequiresExplicitBoundedStaffAuthority(t *testing.T) {
	now := time.Now().UTC()
	p := OverbookingPolicy{MaxQuantity: 1, MaxCapacityBPS: 1000, AllowedSources: []string{"DIRECT"}, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), PolicyReference: "approved-property-policy:v1"}
	if e := p.Validate(now); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*OverbookingPolicy){func(p *OverbookingPolicy) { p.AllowedSources = []string{"MARKETPLACE"} }, func(p *OverbookingPolicy) { p.MaxCapacityBPS = 10001 }, func(p *OverbookingPolicy) { p.ExpiresAt = now }, func(p *OverbookingPolicy) { p.ExpiresAt = now.Add(31 * 24 * time.Hour) }, func(p *OverbookingPolicy) { p.AllowedSources = []string{"DIRECT", "DIRECT"} }} {
		copy := p
		mutate(&copy)
		if copy.Validate(now) == nil {
			t.Fatal("invalid overbooking authority accepted")
		}
	}
}
