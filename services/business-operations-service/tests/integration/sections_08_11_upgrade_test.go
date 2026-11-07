package integration

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
)

// Populate the published schema 26 through supported HTTP journeys, then apply
// every later Sections 8–11 migration twice. Accepted reservations, prices,
// stock and history must survive unchanged and remain operable.
func TestBusinessSections0811UpgradeFromPopulatedSchema26(t *testing.T) {
	previous := fstest.MapFS{}
	entries, e := fs.ReadDir(migrations.FS, ".")
	if e != nil {
		t.Fatal(e)
	}
	latest := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if n, err := strconv.Atoi(entry.Name()[:5]); err == nil && n > latest {
			latest = n
		}
		if entry.Name() >= "00027" {
			continue
		}
		raw, err := fs.ReadFile(migrations.FS, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	if latest <= 26 {
		t.Fatalf("expected Sections 8–11 migrations after schema 26, latest=%d", latest)
	}
	c := newCommercialHTTP(t, previous)
	reservationPolicyFixture(c, false)
	booking := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, true), 201))
	booking = commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+booking.Reservation.ID.String()+"/confirm", reservations.Command{Version: booking.Reservation.Version, Reason: "Confirm the approved guarantee before upgrade"}, 200))
	calendar := `SELECT coalesce(jsonb_agg(jsonb_build_object('day',day,'capacity',capacity,'blocked',blocked,'committed',committed,'overbooked',overbooked,'version',version) ORDER BY day),'[]'::jsonb)::text FROM inventory_calendar WHERE organisation_id=$1 AND pool_id=$2`
	history := `SELECT count(*)::text||':'||coalesce(max(reservation_version),0)::text FROM reservation_versions WHERE reservation_id=$1`
	var beforeStock, beforeHistory string
	if e = c.db.Pool.QueryRow(t.Context(), calendar, c.owner.registered.OrganisationID, c.pool.ID).Scan(&beforeStock); e != nil {
		t.Fatal(e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), history, booking.Reservation.ID).Scan(&beforeHistory); e != nil {
		t.Fatal(e)
	}
	sqlDB, e := migrate.Open(c.db.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer sqlDB.Close()
	version, e := migrate.Version(t.Context(), sqlDB, migrations.FS)
	if e != nil || version != 26 {
		t.Fatalf("starting schema %d err=%v", version, e)
	}
	for range 2 {
		if e = migrate.Up(t.Context(), sqlDB, migrations.FS); e != nil {
			t.Fatal(e)
		}
	}
	if version, e = migrate.Version(t.Context(), sqlDB, migrations.FS); e != nil || version != int64(latest) {
		t.Fatalf("upgraded schema %d want %d err=%v", version, latest, e)
	}
	after := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+booking.Reservation.ID.String(), nil, 200))
	if after.Reservation.Version != booking.Reservation.Version || after.Reservation.State != "CONFIRMED" || after.Reservation.AmountMinor != booking.Reservation.AmountMinor || len(after.Rooms) != len(booking.Rooms) || after.Rooms[0].QuoteID != booking.Rooms[0].QuoteID || after.Rooms[0].Quantity != booking.Rooms[0].Quantity {
		t.Fatal("upgrade altered accepted reservation, price or quantity history")
	}
	var afterStock, afterHistory string
	if e = c.db.Pool.QueryRow(t.Context(), calendar, c.owner.registered.OrganisationID, c.pool.ID).Scan(&afterStock); e != nil {
		t.Fatal(e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), history, booking.Reservation.ID).Scan(&afterHistory); e != nil {
		t.Fatal(e)
	}
	if afterStock != beforeStock || afterHistory != beforeHistory {
		t.Fatalf("upgrade changed stock or history\nstock %s -> %s\nhistory %s -> %s", beforeStock, afterStock, beforeHistory, afterHistory)
	}
	// The upgraded reservation remains operable: cancellation releases exactly
	// the committed stock captured before the upgrade.
	cancelled := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+after.Reservation.ID.String()+"/cancel", reservations.Command{Version: after.Reservation.Version, Reason: "Cancel the upgraded reservation"}, 200))
	if cancelled.Reservation.State != "CANCELLED" {
		t.Fatalf("cancel after upgrade %s", cancelled.Reservation.State)
	}
	var committed int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT coalesce(sum(committed),0) FROM inventory_calendar WHERE organisation_id=$1 AND pool_id=$2`, c.owner.registered.OrganisationID, c.pool.ID).Scan(&committed); e != nil || committed != 0 {
		t.Fatalf("stock not conserved after upgraded cancellation committed=%d err=%v", committed, e)
	}
}
