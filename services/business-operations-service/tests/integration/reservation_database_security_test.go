package integration

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBusinessReservationRuntimeRoleCannotRewriteFinancialOrSourceHistory(t *testing.T) {
	db := testkit.NewDatabase(t, migrations.FS)
	role := "bo_runtime_" + strings.ReplaceAll(ids.New().String(), "-", "")
	if _, e := db.Pool.Exec(t.Context(), `CREATE ROLE `+role+` NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION`); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(t.Context(), `DROP OWNED BY `+role)
		_, _ = db.Pool.Exec(t.Context(), `DROP ROLE `+role)
	})
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	raw, e := os.ReadFile(filepath.Join(root, "scripts/database/business-operations-runtime-grants.sql"))
	if e != nil {
		t.Fatal(e)
	}
	var name string
	if e = db.Pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&name); e != nil {
		t.Fatal(e)
	}
	// Apply the actual operator script to unique disposable database/role names.
	// Its database guard remains active; no shared runtime role is altered.
	script := strings.ReplaceAll(strings.ReplaceAll(string(raw), "cf_business_runtime", role), "business_operations_db", name)
	lines := []string{}
	for _, line := range strings.Split(script, "\n") {
		if !strings.HasPrefix(line, "\\") {
			lines = append(lines, line)
		}
	}
	if _, e = db.Pool.Exec(t.Context(), strings.Join(lines, "\n")); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"reservation_versions", "reservation_price_snapshots", "reservation_source_refs", "reservation_financial_credits", "reservation_cancellation_obligations", "reservation_payment_bindings", "reservation_payment_projections"} {
		var read, insert, update, remove, truncate bool
		if e = db.Pool.QueryRow(t.Context(), `SELECT has_table_privilege($1,$2,'SELECT'),has_table_privilege($1,$2,'INSERT'),has_table_privilege($1,$2,'UPDATE'),has_table_privilege($1,$2,'DELETE'),has_table_privilege($1,$2,'TRUNCATE')`, role, table).Scan(&read, &insert, &update, &remove, &truncate); e != nil {
			t.Fatal(e)
		}
		if !read || !insert || update || remove || truncate {
			t.Fatalf("incorrect immutable runtime privileges on %s: %v %v %v %v %v", table, read, insert, update, remove, truncate)
		}
	}
	for _, table := range []string{"reservations", "reservation_rooms", "reservation_financial_requests", "reservation_inventory_days"} {
		var write, remove bool
		if e = db.Pool.QueryRow(t.Context(), `SELECT has_table_privilege($1,$2,'UPDATE'),has_table_privilege($1,$2,'DELETE')`, role, table).Scan(&write, &remove); e != nil || !write || remove {
			t.Fatalf("mutable lifecycle privilege %s %v %v %v", table, write, remove, e)
		}
	}
}
