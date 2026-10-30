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
