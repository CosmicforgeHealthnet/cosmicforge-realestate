// Package smartaccess is the provider-neutral smart-lock/keypad contract
// (spec §12 "Smart access", §13.30). The owner domain's access grants are
// the authority; the adapter only provisions and revokes device credentials
// for a grant's window. Credential secrets are delivered by the provider to
// the principal and never stored here.
//
// No device provider adapter is implemented: registering smart devices
// answers 503 FEATURE_UNAVAILABLE; physical keys, cards and lockboxes are
// managed as owner-issued grants.
package smartaccess

import (
	"context"
	"time"
)

// ProvisionRequest asks the provider for a time-bound credential.
type ProvisionRequest struct {
	DeviceRef      string
	GrantRef       string
	PrincipalLabel string
	StartsAt       time.Time
	EndsAt         time.Time
}

// API is the adapter contract.
type API interface {
	Provision(ctx context.Context, req ProvisionRequest, idemKey string) (credentialRef string, err error)
	Revoke(ctx context.Context, deviceRef, credentialRef, idemKey string) error
}

// ReadbackAPI is required to recover an ambiguous provisioning attempt.
// found=false must mean definitive absence at the provider; timeouts/pending
// results are errors. The observation must bind to the ORIGINAL request/key.
type ReadbackAPI interface {
	LookupProvision(context.Context, ProvisionRequest, string) (credentialRef string, found bool, err error)
}
