package config

import (
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"testing"
)

func TestLiveOperationsCannotBeEnabled(t *testing.T) {
	for _, key := range []string{"PAYMENT_LIVE_CUSTODY_ENABLED", "PAYMENT_LIVE_TRANSFERS_ENABLED", "PAYMENT_LIVE_COLLECTIONS_ENABLED"} {
		if _, e := Load(sharedcfg.FromMap(map[string]string{key: "true"}), false); e == nil {
			t.Fatal(key)
		}
	}
}
func TestNoHardcodedCredentialFallback(t *testing.T) {
	if _, e := Load(sharedcfg.FromMap(map[string]string{"APP_ENV": "production", "DATABASE_URL": "postgres://fixture"}), false); e == nil {
		t.Fatal("missing service keys accepted")
	}
}
