// Package calendarsync is the provider-neutral external calendar adapter
// contract (spec §12 "External marketplace/calendar sync"). Inbound busy
// ranges are best-effort input: they may block free dates but never
// override a held, confirmed or checked-in booking — such overlaps are
// reported as conflicts for the owner to resolve.
//
// No provider adapter is implemented, so the service is built with a nil
// API and connecting an external calendar answers 503 FEATURE_UNAVAILABLE.
package calendarsync

import (
	"context"
	"time"
)

// BusyRange is an external reservation [Start, End) in whole days.
type BusyRange struct {
	Start      time.Time
	End        time.Time
	ExternalID string
}

// API is the adapter contract.
type API interface {
	// Fetch returns the busy ranges of an external calendar feed.
	Fetch(ctx context.Context, feedRef string) ([]BusyRange, error)
}
