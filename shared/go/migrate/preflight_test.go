package migrate_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	. "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// restrictedDatabase creates a database and a login role that may create
// tables in its public schema but holds no CREATE privilege on the database
// (so it cannot create even a trusted extension). It returns the role's URL
// and an admin handle on the database.
func restrictedDatabase(t *testing.T) (roleURL string, admin *sql.DB) {
	t.Helper()
	db := testkit.NewDatabase(t, nil)
	role := "cf_mig_" + hex.EncodeToString(security.RandomBytes(6))
	pass := security.RandomToken(16)
	adm, err := Open(db.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adm.Exec(`REASSIGN OWNED BY ` + role + ` TO current_user`)
		_, _ = adm.Exec(`DROP OWNED BY ` + role)
		_ = adm.Close()
		if root, err := Open(testkit.PostgresAdminURL(t)); err == nil {
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
		if _, err := adm.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	u, err := url.Parse(db.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, pass)
	return u.String(), adm
}

func TestPreflightChecksExtensionsWithoutChangingTheDatabase(t *testing.T) {
	ctx := context.Background()
	db := testkit.NewDatabase(t, nil)
	h, err := Open(db.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := Preflight(ctx, h, []string{"plpgsql"}); err != nil {
		t.Fatalf("installed extension: %v", err)
	}
	err = Preflight(ctx, h, []string{"cf_no_such_extension"})
	if !errors.Is(err, ErrPrerequisite) || !strings.Contains(err.Error(), "not available") ||
		!strings.Contains(err.Error(), "CREATE EXTENSION IF NOT EXISTS cf_no_such_extension;") {
		t.Fatalf("unavailable extension: %v", err)
	}

	roleURL, admin := restrictedDatabase(t)
	fsys := os.DirFS("testdata/migrations")
	var out bytes.Buffer
	opts := Options{Extensions: []string{"btree_gist"}}
	err = RunWith(ctx, []string{"preflight"}, roleURL, fsys, &out, opts)
	if !errors.Is(err, ErrPrerequisite) || !strings.Contains(err.Error(), "CREATE EXTENSION IF NOT EXISTS btree_gist;") ||
		!strings.Contains(err.Error(), "database_create=false") {
		t.Fatalf("restricted role preflight: %v", err)
	}
	if err := RunWith(ctx, []string{"up"}, roleURL, fsys, &out, opts); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("up must refuse before applying anything: %v", err)
	}
	var tables int
	if err := admin.QueryRow(`SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("a refused preflight changed the database: %d tables (%v)", tables, err)
	}

	// The documented administrator step unblocks the same role.
	if _, err := admin.Exec(`CREATE EXTENSION IF NOT EXISTS btree_gist`); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := RunWith(ctx, []string{"preflight"}, roleURL, fsys, &out, opts); err != nil || !strings.Contains(out.String(), "preflight ok") {
		t.Fatalf("after the administrator step: %v %q", err, out.String())
	}
	if err := RunWith(ctx, []string{"up"}, roleURL, fsys, &out, opts); err != nil {
		t.Fatalf("up after the administrator step: %v", err)
	}
}
