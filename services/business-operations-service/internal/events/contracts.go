// Package events defines Business Operations event names and stream ownership.
package events

import "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"

const (
	BusinessVerified = "business.verified.v1"
	StaffUpdated     = "business.staff.updated.v1"
	CapacityChanged  = "business.capacity.changed.v1"
)

// StreamConfig is shared by the worker and broker integration certification.
// Keep the original stream name and reserved legacy subject while accepting
// the canonical business.* event names required by Section 3.
func StreamConfig(prefix string) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:     jetstream.StreamName(prefix, "business_operations"),
		Subjects: []string{prefix + ".business.>", prefix + ".business_operations.>"},
	}
}
