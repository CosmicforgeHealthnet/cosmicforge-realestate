package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestOutboxMigrationFromEmptyDatabase(t *testing.T) {
	base := os.Getenv("CF_TEST_POSTGRES_URL")
	if base == "" {
		if os.Getenv("CF_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("CF_TEST_POSTGRES_URL is required")
		}
		t.Skip("PostgreSQL integration URL not configured")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dbname := "bo_migration_" + uuid.NewString()[:8]
	if _, err = admin.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", dbname)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", dbname))
	})
	u.Path = "/" + dbname
	testDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()
	if err := migrate.Up(ctx, testDB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, testDB, migrations.FS); err != nil {
		t.Fatalf("second up should be a no-op: %v", err)
	}
	version, err := migrate.Version(ctx, testDB, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if version != 9 {
		t.Fatalf("version=%d want=9", version)
	}
	var exists bool
	if err := testDB.QueryRowContext(ctx, "SELECT to_regclass('public.outbox_events') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("outbox_events table missing after migration")
	}
}
