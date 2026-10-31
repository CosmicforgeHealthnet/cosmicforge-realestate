package integration

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestBusinessCompleteCatalogueMatchesActualPostgreSQL(t *testing.T) {
	db := testkit.NewDatabase(t, migrations.FS)
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	raw, err := os.ReadFile(filepath.Join(root, "docs/implementation/business-operations-database-catalogue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalogue struct {
		Entries []struct {
			Canonical string `json:"canonical_table"`
			Decision  string `json:"decision"`
			Schema    *struct {
				Organisation bool     `json:"organisation_column"`
				ForeignKeys  int      `json:"foreign_key_count"`
				Indexes      []string `json:"indexes"`
			} `json:"schema"`
		} `json:"entries"`
	}
	if err = json.Unmarshal(raw, &catalogue); err != nil {
		t.Fatal(err)
	}
	if len(catalogue.Entries) < 200 {
		t.Fatal("catalogue does not cover the full specification and annex supplements")
	}
	present, future := 0, 0
	declared := map[string]bool{}
	for _, entry := range catalogue.Entries {
		var exists bool
		if err = db.Pool.QueryRow(t.Context(), `SELECT to_regclass('public.'||$1) IS NOT NULL`, entry.Canonical).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if entry.Decision == "DEFERRED_FUTURE_DOMAIN" {
			future++
			if exists {
				t.Fatalf("deferred table exists without catalogue reconciliation: %s", entry.Canonical)
			}
			continue
		}
		present++
		declared[entry.Canonical] = true
		if !exists || entry.Schema == nil {
			t.Fatalf("missing implemented schema %s", entry.Canonical)
		}
		var fks int
		if err = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass('public.'||$1) AND contype='f'`, entry.Canonical).Scan(&fks); err != nil || fks < entry.Schema.ForeignKeys {
			t.Fatalf("foreign keys %s: %d %v", entry.Canonical, fks, err)
		}
		for _, index := range entry.Schema.Indexes {
			if err = db.Pool.QueryRow(t.Context(), `SELECT to_regclass('public.'||$1) IS NOT NULL`, index).Scan(&exists); err != nil || !exists {
				t.Fatalf("missing index %s %v", index, err)
			}
		}
	}
	rows, e := db.Pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename<>'goose_db_version'`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		if !declared[name] {
			t.Fatalf("Uncatalogued actual table %s", name)
		}
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	t.Logf("verified %d implemented tables and %d deliberate future-table decisions against PostgreSQL", present, future)
}

func TestBusinessCommercialMigrationPreservesPopulatedSection4Database(t *testing.T) {
	previous := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "00006" {
			continue
		}
		raw, e := fs.ReadFile(migrations.FS, entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	db := testkit.NewDatabase(t, previous)
	account, org, branch, property := ids.New(), ids.New(), ids.New(), ids.New()
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO business_accounts(id,email,display_name) VALUES($1,'upgrade@example.test','Upgrade owner')`, []any{account}},
		{`INSERT INTO organisations(id,representative_account_id,name) VALUES($1,$2,'Section four organisation')`, []any{org, account}},
		{`INSERT INTO branches(id,organisation_id,name,timezone,currency) VALUES($1,$2,'Existing branch','Europe/Rome','EUR')`, []any{branch, org}},
		{`INSERT INTO inventory_nodes(id,organisation_id,branch_id,kind,code,name,timezone,currency,created_by,updated_by) VALUES($1,$2,$3,'PROPERTY','STABLE','Existing canonical property','Europe/Rome','EUR',$4,$4)`, []any{property, org, branch, account}},
	} {
		if _, err = db.Pool.Exec(t.Context(), s.sql, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	sqlDB, err := migrate.Open(db.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err = migrate.Up(t.Context(), sqlDB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(t.Context(), sqlDB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var name, state string
	var version int64
	if err = db.Pool.QueryRow(t.Context(), `SELECT name,state,version FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, org, property).Scan(&name, &state, &version); err != nil || name != "Existing canonical property" || state != "DRAFT" || version != 1 {
		t.Fatalf("historical graph changed: %s %s %d %v", name, state, version, err)
	}
	var count int
	if err = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM properties WHERE organisation_id=$1 AND id=$2`, org, property).Scan(&count); err != nil || count != 1 {
		t.Fatalf("canonical anchor lost: %d %v", count, err)
	}
}
