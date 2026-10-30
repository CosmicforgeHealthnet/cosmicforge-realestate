package unit

import (
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/config"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
)

func TestConfigRequiresOwnedDatabase(t *testing.T) {
	_, err := config.Load(sharedcfg.FromMap(map[string]string{"DATABASE_URL": "postgres://u:p@localhost/other_db"}), config.ModeAPI)
	if err == nil {
		t.Fatal("expected a clear failure for a database outside this service's ownership")
	}
}

func TestDatabaseQueryAliasesCannotOverrideOwnership(t *testing.T) {
	for _, raw := range []string{"postgres://u:p@localhost/business_operations_db?dbname=other_db", "postgres://u:p@localhost/business_operations_db?database=other_db", "https://localhost/business_operations_db"} {
		if config.ValidateOwnedDatabase(raw) == nil {
			t.Fatalf("database boundary bypass accepted")
		}
	}
	if err := config.ValidateOwnedDatabase("postgres://u:p@localhost/business_operations_db?dbname=business_operations_db&sslmode=disable"); err != nil {
		t.Fatal(err)
	}
}

func TestEventNamespaceRejectsWildcards(t *testing.T) {
	for _, prefix := range []string{">", "*", "realestate.>", "realestate *"} {
		if _, err := config.Load(sharedcfg.FromMap(map[string]string{"DATABASE_URL": "postgres://u:p@localhost/business_operations_db", "NATS_SUBJECT_PREFIX": prefix}), config.ModeAPI); err == nil {
			t.Fatal("unsafe event namespace accepted")
		}
	}
}

func TestConfigAcceptsBusinessOperationsDatabase(t *testing.T) {
	c, err := config.Load(sharedcfg.FromMap(map[string]string{"DATABASE_URL": "postgres://u:p@localhost/business_operations_db?sslmode=disable"}), config.ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" {
		t.Fatalf("default HTTP address = %q", c.HTTPAddr)
	}
}

func TestWorkerRequiresNATS(t *testing.T) {
	_, err := config.Load(sharedcfg.FromMap(map[string]string{"DATABASE_URL": "postgres://u:p@localhost/business_operations_db"}), config.ModeWorker)
	if err == nil {
		t.Fatal("expected worker configuration to require NATS_URL")
	}
}
