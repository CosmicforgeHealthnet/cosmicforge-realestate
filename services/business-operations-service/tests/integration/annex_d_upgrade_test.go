package integration

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
)

// Every governed table (00036 and 00038) carries a database state guard
// whose edges are exactly the generated domain transitions, and illegal
// direct transitions are rejected by PostgreSQL itself.
func TestAnnexDDatabaseGuardsMatchEveryGeneratedDomain(t *testing.T) {
	c := newCommercialHTTP(t)
	rows, err := c.db.Pool.Query(t.Context(), `SELECT c.relname,encode(t.tgargs,'escape') FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid WHERE t.tgname=c.relname||'_guard' AND t.tgfoid='business_ops_state_guard'::regproc`)
	if err != nil {
		t.Fatal(err)
	}
	guards := map[string][]string{}
	for rows.Next() {
		var table, args string
		if err = rows.Scan(&table, &args); err != nil {
			t.Fatal(err)
		}
		guards[table] = strings.Split(strings.TrimSuffix(args, `\000`), ",")
	}
	rows.Close()
	domains := businesshttp.OpsDomainGuards()
	if len(domains) != 71 {
		t.Fatalf("governed domains %d", len(domains))
	}
	for table, edges := range domains {
		got := guards[table]
		if got == nil {
			t.Fatalf("missing guard for %s", table)
		}
		a, b := slices.Clone(edges), slices.Clone(got)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			t.Fatalf("%s guard drift\nwant %v\ngot  %v", table, a, b)
		}
	}
	// A direct illegal transition and a delete are rejected by the database.
	rent := int64(1000)
	start := time.Now().UTC().Add(-24 * time.Hour)
	ten := opsRec(c, c.owner, "POST", "/tenancies", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "PERIODIC", Title: "Guard tenancy", CounterpartyReference: "tenant:guard", AmountMinor: &rent, Currency: "NGN", StartsAt: &start, Reason: "Guard fixture"}, 201)
	ag := opsRec(c, c.owner, "POST", "/agreements", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &ten.ID, Kind: "RESIDENTIAL_TENANCY", Title: "Guard agreement", CounterpartyReference: "tenant:guard", Reason: "Guard fixture"}, 201)
	if _, err = c.db.Pool.Exec(t.Context(), `UPDATE agreements SET state='SIGNED',version=version+1 WHERE id=$1`, ag.ID); err == nil {
		t.Fatal("DRAFT->SIGNED bypassed the database guard")
	}
	if _, err = c.db.Pool.Exec(t.Context(), `DELETE FROM agreements WHERE id=$1`, ag.ID); err == nil {
		t.Fatal("agreement history deleted")
	}
}

// Populate schema 37 through supported journeys, apply 00038 twice, and
// continue the same records through the new Annex D lifecycles.
func TestAnnexDUpgradeFromPopulatedSchema37(t *testing.T) {
	previous := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "00038" {
			continue
		}
		raw, err := fs.ReadFile(migrations.FS, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	c := newCommercialHTTP(t, previous)
	rent := int64(120000)
	start := time.Now().UTC().Add(24 * time.Hour)
	ten := opsRec(c, c.owner, "POST", "/tenancies", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "PERIODIC", Title: "Pre-upgrade tenancy", CounterpartyReference: "tenant:upgrade", AmountMinor: &rent, Currency: "NGN", StartsAt: &start, Reason: "Existing tenancy"}, 201)
	ten = opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "SEND_FOR_SIGNATURE"}, 200)
	var beforeHistory int
	if err = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM operations_history WHERE entity_id=$1`, ten.ID).Scan(&beforeHistory); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := migrate.Open(c.db.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if v, err := migrate.Version(t.Context(), sqlDB, migrations.FS); err != nil || v != 37 {
		t.Fatalf("starting schema %d %v", v, err)
	}
	for range 2 {
		if err = migrate.Up(t.Context(), sqlDB, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}
	if v, err := migrate.Version(t.Context(), sqlDB, migrations.FS); err != nil || v != 38 {
		t.Fatalf("upgraded schema %d %v", v, err)
	}
	var afterHistory int
	if err = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM operations_history WHERE entity_id=$1`, ten.ID).Scan(&afterHistory); err != nil || afterHistory != beforeHistory {
		t.Fatalf("history changed %d -> %d %v", beforeHistory, afterHistory, err)
	}
	signAgreement(c, ten.ID, "RESIDENTIAL_TENANCY", "tenant:upgrade")
	approval := c.approval("COMMERCIAL_TENANCY", "residential:write", "", &rent, "NGN", map[string]any{"domain": "tenancy", "record_id": ten.ID, "action": "ACTIVATE", "version": ten.Version})
	ten = opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "ACTIVATE", ApprovalID: approval, EvidenceRefs: evidence()}, 200)
	var scheduled int
	if err = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM rent_obligations WHERE subject_id=$1`, ten.ID).Scan(&scheduled); err != nil || scheduled != 12 {
		t.Fatalf("rent schedule after upgrade %d %v", scheduled, err)
	}
}
