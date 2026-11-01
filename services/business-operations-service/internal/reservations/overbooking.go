package reservations

import (
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"slices"
	"strings"
	"time"
)

// OverbookingPolicy is an explicit limit on accepted excess demand. It never
// increases physical capacity or authorises ordinary inventory sales.
type OverbookingPolicy struct {
	MaxQuantity     int       `json:"max_quantity"`
	MaxCapacityBPS  int64     `json:"max_capacity_bps"`
	AllowedSources  []string  `json:"allowed_sources"`
	EffectiveAt     time.Time `json:"effective_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	PolicyReference string    `json:"policy_reference"`
}

func (p OverbookingPolicy) Validate(now time.Time) error {
	if p.MaxQuantity < 1 || p.MaxQuantity > 1000 || p.MaxCapacityBPS < 1 || p.MaxCapacityBPS > 10000 || len(p.AllowedSources) < 1 || len(p.AllowedSources) > 3 || p.EffectiveAt.IsZero() || !p.ExpiresAt.After(p.EffectiveAt) || !now.Before(p.ExpiresAt) || p.ExpiresAt.After(now.Add(30*24*time.Hour)) || len(p.PolicyReference) < 1 || len(p.PolicyReference) > 200 || strings.ContainsRune(p.PolicyReference, 0) {
		return apperrors.InvalidRequest("Explicit bounded and expiring overbooking policy required.")
	}
	seen := map[string]bool{}
	for _, source := range p.AllowedSources {
		if !slices.Contains([]string{"DIRECT", "WALK_IN", "GROUP"}, source) || seen[source] {
			return apperrors.InvalidRequest("Unique supported staff-owned overbooking sources required.")
		}
		seen[source] = true
	}
	return nil
}
func (p OverbookingPolicy) Limit(capacity int) int {
	if capacity < 1 || p.MaxCapacityBPS < 1 || p.MaxCapacityBPS > 10000 || p.MaxQuantity < 1 {
		return 0
	}
	// Floor intentionally: fractional rooms never round permission upward.
	return min(p.MaxQuantity, int(int64(capacity)*p.MaxCapacityBPS/10000))
}
