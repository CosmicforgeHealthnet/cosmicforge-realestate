package integration

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	"io/fs"
	"testing"
	"testing/fstest"
)

// Populate the real v11 schema using its supported HTTP journeys, retaining all
// version/FK/audit/outbox guards. No final-state rows or triggers are bypassed.
func TestBusinessCRMUpgradePreservesActualVersion11ReservationAndStock(t *testing.T) {
	previous := fstest.MapFS{}
	entries, e := fs.ReadDir(migrations.FS, ".")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.Name() >= "00012" {
			continue
		}
		raw, err := fs.ReadFile(migrations.FS, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	c := newCommercialHTTP(t, previous)
	reservationPolicyFixture(c, false)
	booking := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, false), 201))
	calendar := `SELECT coalesce(jsonb_agg(jsonb_build_object('day',day,'capacity',capacity,'blocked',blocked,'committed',committed,'overbooked',overbooked,'version',version) ORDER BY day),'[]'::jsonb)::text FROM inventory_calendar WHERE organisation_id=$1 AND pool_id=$2`
	var beforeStock string
	if e = c.db.Pool.QueryRow(t.Context(), calendar, c.owner.registered.OrganisationID, c.pool.ID).Scan(&beforeStock); e != nil {
		t.Fatal(e)
	}
	sqlDB, e := migrate.Open(c.db.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer sqlDB.Close()
	version, e := migrate.Version(t.Context(), sqlDB, migrations.FS)
	if e != nil || version != 11 {
		t.Fatalf("starting schema %d err=%v", version, e)
	}
	if e = migrate.Up(t.Context(), sqlDB, migrations.FS); e != nil {
		t.Fatal(e)
	}
	if e = migrate.Up(t.Context(), sqlDB, migrations.FS); e != nil {
		t.Fatal(e)
	}
	after := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+booking.Reservation.ID.String(), nil, 200))
	if after.Reservation.ID != booking.Reservation.ID || after.Reservation.Version != booking.Reservation.Version || after.Reservation.AmountMinor != booking.Reservation.AmountMinor || after.Reservation.PolicyID != booking.Reservation.PolicyID || len(after.Rooms) != len(booking.Rooms) || after.Rooms[0].QuoteID != booking.Rooms[0].QuoteID {
		t.Fatal("Migration altered accepted reservation/price/policy history")
	}
	var afterStock string
	if e = c.db.Pool.QueryRow(t.Context(), calendar, c.owner.registered.OrganisationID, c.pool.ID).Scan(&afterStock); e != nil {
		t.Fatal(e)
	}
	if beforeStock != afterStock {
		t.Fatal("Migration changed actual physical inventory counters")
	}
	guest := crmGuestFixture(c)
	if guest.Guest.ID.ValidateV7() != nil {
		t.Fatal("Upgraded API could not create its actual owned guest identity")
	}
}
