package verification

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"time"
)

// These assertions are not implied by a generic BUSINESS KYB result. The
// current provider has no such operations; adapters must be explicitly wired
// when its authenticated contract supports them. Nil adapters fail closed.
type CapacityAssertion struct {
	OrganisationID ids.ID
	Code           string
	CaseID         ids.ID
	Status         string
	ExpiresAt      *time.Time
	UpdatedAt      time.Time
}
type CapacityAuthority interface {
	SupportsCapacity(string) bool
	Capacity(context.Context, ids.ID, string, ids.ID) (CapacityAssertion, error)
}
type RepresentativeAssertion struct {
	OrganisationID, AccountID, CaseID ids.ID
	Verified                          bool
	ExpiresAt                         time.Time
}
type RepresentativeAuthority interface {
	Representative(context.Context, ids.ID, ids.ID, ids.ID) (RepresentativeAssertion, error)
}
type ClosureReadiness interface {
	Ready(context.Context, ids.ID) (bool, []string, error)
}
