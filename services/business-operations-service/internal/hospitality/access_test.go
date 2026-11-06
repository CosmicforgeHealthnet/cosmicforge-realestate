package hospitality

import (
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/smartaccess"
)

func TestAccessObservationsBindOriginalCommandAndFreshProviderEvidence(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	digest := "exact-original-request-digest"
	base := smartaccess.Observation{RequestDigest: digest, State: "ACTIVE", CredentialReference: "provider-credential-reference:original", EvidenceReference: "authenticated-original-command-receipt", ObservedAt: now}
	for _, tc := range []struct {
		name, operation string
		change          func(*smartaccess.Observation)
		valid           bool
	}{
		{"original issuance", "ISSUE", func(*smartaccess.Observation) {}, true},
		{"wrong original command", "ISSUE", func(o *smartaccess.Observation) { o.RequestDigest = "another-request" }, false},
		{"unconfirmed pending outcome", "ISSUE", func(o *smartaccess.Observation) { o.State = "PENDING" }, false},
		{"missing credential reference", "ISSUE", func(o *smartaccess.Observation) { o.CredentialReference = "" }, false},
		{"stale provider observation", "ISSUE", func(o *smartaccess.Observation) { o.ObservedAt = now.Add(-6 * time.Minute) }, false},
		{"future provider observation", "ISSUE", func(o *smartaccess.Observation) { o.ObservedAt = now.Add(2 * time.Minute) }, false},
		{"no provider evidence", "ISSUE", func(o *smartaccess.Observation) { o.EvidenceReference = "" }, false},
		{"bound revocation", "REVOKE", func(o *smartaccess.Observation) { o.State = "REVOKED" }, true},
		{"definitive absence", "REVOKE", func(o *smartaccess.Observation) { o.State = "ABSENT"; o.CredentialReference = "" }, true},
		{"unsupported operation", "REPLACE", func(*smartaccess.Observation) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.change(&o)
			if got := ValidAccessObservation(o, digest, tc.operation, now); got != tc.valid {
				t.Fatalf("provider evidence validity=%v want=%v", got, tc.valid)
			}
		})
	}
}
