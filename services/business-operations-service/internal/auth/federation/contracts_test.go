package federation

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"testing"
	"time"
)

func TestFederationBindingRejectsReplayAndUntrustedClaims(t *testing.T) {
	now := time.Now().UTC()
	c := Challenge{OrganisationID: ids.New(), ProviderID: "configured-provider", StateHash: "state-hash", NonceHash: "nonce-hash", ExpiresAt: now.Add(time.Minute)}
	a := Assertion{ProviderID: c.ProviderID, Issuer: "https://idp.example.test", Audience: "business-operations", Subject: "immutable-external-subject", StateHash: c.StateHash, NonceHash: c.NonceHash, AuthenticatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}
	if err := Validate(c, a, a.Issuer, a.Audience, now); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Challenge, *Assertion){"state": func(_ *Challenge, a *Assertion) { a.StateHash = "wrong" }, "nonce": func(_ *Challenge, a *Assertion) { a.NonceHash = "wrong" }, "issuer": func(_ *Challenge, a *Assertion) { a.Issuer = "https://attacker.example" }, "audience": func(_ *Challenge, a *Assertion) { a.Audience = "other-service" }, "provider": func(_ *Challenge, a *Assertion) { a.ProviderID = "other-provider" }, "consumed": func(c *Challenge, _ *Assertion) { c.ConsumedAt = &now }, "expired": func(c *Challenge, _ *Assertion) { c.ExpiresAt = now }, "future-auth": func(_ *Challenge, a *Assertion) { a.AuthenticatedAt = now.Add(time.Minute) }}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			copyC, copyA := c, a
			change(&copyC, &copyA)
			if Validate(copyC, copyA, a.Issuer, a.Audience, now) == nil {
				t.Fatal("untrusted or replayed assertion accepted")
			}
		})
	}
}
