package integration

import (
	"io/fs"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/artisan-service/migrations"
)

func migrationsBase(t *testing.T) []byte {
	raw, err := fs.ReadFile(migrations.FS, "00001_platform.sql")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
