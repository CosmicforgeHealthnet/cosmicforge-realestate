// Package testkit provides reproducible integration-test infrastructure:
// ephemeral PostgreSQL databases (one per test, migrated from the service's
// own Goose files), an embedded NATS JetStream server, and key helpers.
//
// PostgreSQL is located through CF_TEST_POSTGRES_URL (a superuser/admin DSN,
// e.g. postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable).
// CI provisions it as a service container; developers may point it at any
// reachable PostgreSQL — Docker is not required locally. When the variable is
// absent tests are skipped, unless CF_REQUIRE_INTEGRATION=1 (set in CI), in
// which case they fail: integration gates can never pass by being skipped.
//
// The directory is named testing/ per the approved layout; the package is
// named testkit to avoid shadowing the standard library.
package testkit

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// EnvPostgresURL names the admin DSN variable.
const EnvPostgresURL = "CF_TEST_POSTGRES_URL"

// EnvRequireIntegration makes missing infrastructure a failure.
const EnvRequireIntegration = "CF_REQUIRE_INTEGRATION"

// RequireOrSkip skips (or fails in CI) when infrastructure is unavailable.
func RequireOrSkip(t testing.TB, what string) {
	t.Helper()
	if os.Getenv(EnvRequireIntegration) == "1" {
		t.Fatalf("%s is required (CF_REQUIRE_INTEGRATION=1) but not configured", what)
	}
	t.Skipf("%s not configured; skipping integration test", what)
}

// PostgresAdminURL returns the admin DSN or skips/fails.
func PostgresAdminURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv(EnvPostgresURL)
	if u == "" {
		RequireOrSkip(t, EnvPostgresURL)
	}
	return u
}

// Database is an ephemeral, migrated database.
type Database struct {
	Name string
	URL  string
	Pool *pgxpool.Pool
}

// NewDatabase creates a uniquely named database, applies migrations (if
// fsys is non-nil) and any extra SQL, and drops it when the test ends.
func NewDatabase(t testing.TB, fsys fs.FS, extraSQL ...string) *Database {
	t.Helper()
	admin := PostgresAdminURL(t)
	name := "cf_test_" + hex.EncodeToString(security.RandomBytes(6))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminDB, err := migrate.Open(admin)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	dbURL := withDatabase(t, admin, name)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a, err := migrate.Open(admin)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.ExecContext(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	if fsys != nil {
		sdb, err := migrate.Open(dbURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := migrate.Up(ctx, sdb, fsys); err != nil {
			sdb.Close()
			t.Fatalf("migrations failed: %v", err)
		}
		sdb.Close()
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, s := range extraSQL {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("extra SQL failed: %v", err)
		}
	}
	return &Database{Name: name, URL: dbURL, Pool: pool}
}

func withDatabase(t testing.TB, dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("invalid %s", EnvPostgresURL)
	}
	u.Path = "/" + name
	return u.String()
}

// MigrationRoundTrip proves migrations apply from empty, roll back to empty
// and re-apply cleanly (deterministic ordering, working down migrations).
func MigrationRoundTrip(t testing.TB, fsys fs.FS) {
	t.Helper()
	d := NewDatabase(t, nil)
	ctx := context.Background()
	db, err := migrate.Open(d.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate.Up(ctx, db, fsys); err != nil {
		t.Fatalf("up from empty: %v", err)
	}
	v1, err := migrate.Version(ctx, db, fsys)
	if err != nil || v1 == 0 {
		t.Fatalf("version after up: %d %v", v1, err)
	}
	if err := migrate.DownTo(ctx, db, fsys, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	if n := countUserTables(t, db); n != 0 {
		t.Fatalf("down migrations left %d tables behind", n)
	}
	if err := migrate.Up(ctx, db, fsys); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	v2, _ := migrate.Version(ctx, db, fsys)
	if v1 != v2 {
		t.Fatalf("version mismatch %d vs %d", v1, v2)
	}
}

func countUserTables(t testing.TB, db *sql.DB) int {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// SigningKey returns a fresh Ed25519 signing key.
func SigningKey(t testing.TB, kid string) jwt.SigningKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return jwt.SigningKey{KID: kid, PrivateKey: priv}
}

// Eventually polls cond until true or timeout.
func Eventually(t testing.TB, timeout time.Duration, cond func() bool, msg string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, fmt.Sprintf(msg, args...))
}

// SanitizeName makes a string safe for NATS stream/consumer names.
func SanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
}
