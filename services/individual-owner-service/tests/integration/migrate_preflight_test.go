package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// Every CREATE EXTENSION in the migrations is declared as a prerequisite.
func TestRequiredExtensionsMatchMigrations(t *testing.T) {
	re := regexp.MustCompile(`(?i)CREATE EXTENSION (?:IF NOT EXISTS )?"?([a-z0-9_]+)`)
	var found []string
	err := fs.WalkDir(migrations.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sql") {
			return err
		}
		b, err := fs.ReadFile(migrations.FS, p)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !slices.Contains(found, m[1]) {
				found = append(found, m[1])
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(found)
	want := slices.Clone(migrations.RequiredExtensions)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Fatalf("migrations create %v but RequiredExtensions declares %v", found, want)
	}
}

// A migration role that cannot create btree_gist is refused before any
// migration runs; after the documented administrator step the same role
// applies every owner migration.
func TestOwnerMigrationsPreflightOnRestrictedRole(t *testing.T) {
	ctx := context.Background()
	db := testkit.NewDatabase(t, nil)
	role := "cf_owner_mig_" + hex.EncodeToString(security.RandomBytes(6))
	pass := hex.EncodeToString(security.RandomBytes(12))
	admin, err := migrate.Open(db.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP OWNED BY ` + role)
		_ = admin.Close()
		if root, err := migrate.Open(testkit.PostgresAdminURL(t)); err == nil {
			_, _ = root.Exec(`DROP ROLE IF EXISTS ` + role)
			_ = root.Close()
		}
	})
	for _, q := range []string{
		`CREATE ROLE ` + role + ` LOGIN NOSUPERUSER PASSWORD '` + pass + `'`,
		`REVOKE CREATE ON DATABASE "` + db.Name + `" FROM PUBLIC`,
		`GRANT CONNECT ON DATABASE "` + db.Name + `" TO ` + role,
		`GRANT USAGE, CREATE ON SCHEMA public TO ` + role,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	u, _ := url.Parse(db.URL)
	u.User = url.UserPassword(role, pass)
	opts := migrate.Options{Extensions: migrations.RequiredExtensions}
	var out bytes.Buffer
	err = migrate.RunWith(ctx, []string{"up"}, u.String(), migrations.FS, &out, opts)
	if !errors.Is(err, migrate.ErrPrerequisite) || !strings.Contains(err.Error(), "CREATE EXTENSION IF NOT EXISTS btree_gist;") {
		t.Fatalf("restricted role: %v", err)
	}
	if n := tableCount(t, admin); n != 0 {
		t.Fatalf("refused migration created %d tables", n)
	}
	if _, err := admin.Exec(`CREATE EXTENSION IF NOT EXISTS btree_gist`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.RunWith(ctx, []string{"up"}, u.String(), migrations.FS, &out, opts); err != nil {
		t.Fatalf("after the administrator step: %v", err)
	}
	var v int64
	if err := admin.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&v); err != nil || v < 16 {
		t.Fatalf("applied version %d (%v)", v, err)
	}
}

func tableCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
