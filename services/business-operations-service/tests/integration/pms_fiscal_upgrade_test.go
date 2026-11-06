package integration

import (
	"context"
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	"github.com/jackc/pgx/v5"
)

// This is a database migration fixture, not certification of a historical API
// binary. Real HTTP creates the confirmed booking and physical assignment; the
// SQL fixture then writes the old schema18 stay/gross layout, retaining every
// version, identity, stock, audit and outbox guard. No trigger is disabled.
func TestBusinessPMSFiscalUpgradeRetainsGrossAndBackfillsOnceAsService(t *testing.T) {
	previous := fstest.MapFS{}
	entries, e := fs.ReadDir(migrations.FS, ".")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.Name() >= "00019" {
			continue
		}
		raw, e := fs.ReadFile(migrations.FS, entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	c := newCommercialHTTPForArrival(t, 0, previous)
	v, guest := pmsConfirmed(c)
	ready := pmsReady(c)
	assignment := pmsAssign(c, v, ready)
	stay, folio, item := ids.New(), ids.New(), ids.New()
	org := c.owner.registered.OrganisationID
	actor := c.owner.registered.AccountID
	dayID := ids.New()
	day, e := hospitality.BusinessDate(time.Now(), "00:00", c.property.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	civil, e := time.Parse(time.DateOnly, day)
	if e != nil {
		t.Fatal(e)
	}
	cutoff, e := hospitality.LocalInstant(civil.AddDate(0, 0, 1).Format(time.DateOnly), "00:00", c.property.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	policy, _ := json.Marshal(map[string]any{"accepted_quote": v.Rooms[0].Snapshot, "reservation_room_id": v.Rooms[0].ID, "slot": 1, "quantity": 1, "gross_includes_accepted_components": true})
	e = database.WithTx(t.Context(), c.db.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE room_assignments SET state='OCCUPIED',version=version+1,updated_at=now() WHERE id=$1`, assignment.ID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE room_status_current SET state='OCCUPIED',version=version+1,updated_at=now() WHERE id=$1`, ready.ID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO stay_records(id,organisation_id,property_id,reservation_id,assignment_id,guest_id,state,registration_reference,checked_in_at,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,'ACTIVE','schema18-migration-fixture',$7,'Retained schema18 physical stay fixture',$8)`, stay, org, c.property.ID, v.Reservation.ID, assignment.ID, guest.Guest.ID, time.Now().UTC(), actor); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO folios(id,organisation_id,property_id,reservation_id,stay_id,state,currency,created_by) VALUES($1,$2,$3,$4,$5,'OPEN',$6,$7)`, folio, org, c.property.ID, v.Reservation.ID, stay, v.Reservation.Currency, actor); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO hospitality_business_days(id,organisation_id,property_id,day,timezone,cutoff_local,cutoff_at,state,policy_version,created_by) VALUES($1,$2,$3,$4::date,$5,'00:00',$6,'OPEN',0,$7)`, dayID, org, c.property.ID, day, c.property.Timezone, cutoff, actor); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO folio_items(id,organisation_id,property_id,folio_id,kind,source_reference,quantity,amount_minor,currency,policy_snapshot,reason,created_by,business_day_id) VALUES($1,$2,$3,$4,'ACCOMMODATION',$5,1,$6,$7,$8,'Retained schema18 accepted gross fixture',$9,$10)`, item, org, c.property.ID, folio, "accepted-reservation-slot:"+assignment.ID.String(), v.Reservation.AmountMinor, v.Reservation.Currency, policy, actor, dayID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE reservations SET state='CHECKED_IN',version=version+1,updated_at=now() WHERE id=$1`, v.Reservation.ID); e != nil {
			return e
		}
		root, e := reservations.Get(ctx, tx, org, v.Reservation.ID)
		if e != nil {
			return e
		}
		snapshot, _ := json.Marshal(map[string]any{"reservation": root, "rooms": v.Rooms})
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_versions(id,organisation_id,property_id,reservation_id,reservation_version,action,snapshot,reason,created_by) VALUES($1,$2,$3,$4,$5,'CHECKED_IN',$6,'Schema18 migration fixture retaining authoritative evidence',$7)`, ids.New(), org, c.property.ID, root.ID, root.Version, snapshot, actor); e != nil {
			return e
		}
		if e = businessEvents.CommercialCommit(ctx, tx, c.domain.Auth.Service.Outbox, org, c.property.ID, root.ID, actor, "reservation", "CHECKED_IN", "reservation.checked_in.v1", root.Version, map[string]any{"version": root.Version, "state": root.State, "financial_state": root.FinancialState, "fixture": "SCHEMA18_MIGRATION_ONLY"}); e != nil {
			return e
		}
		for _, entity := range []struct {
			id          ids.ID
			kind, event string
			version     int64
		}{{dayID, "business_day", "hospitality.business_day.opened.v1", 1}, {assignment.ID, "room_assignment", "hospitality.room.assigned.v1", assignment.Version + 1}, {ready.ID, "room_status", "room.status.changed.v1", ready.Version + 1}, {stay, "stay", "hospitality.stay.checked_in.v1", 1}, {folio, "folio", "hospitality.folio.updated.v1", 1}} {
			data, _ := json.Marshal(map[string]any{"fixture": "SCHEMA18_MIGRATION_ONLY", "reason": "Preserve existing owned gross data across additive migration"})
			if _, e = tx.Exec(ctx, `INSERT INTO hospitality_history(id,organisation_id,property_id,entity_id,entity_type,entity_version,action,change_context,created_by) VALUES($1,$2,$3,$4,$5,$6,'SCHEMA18_MIGRATION_FIXTURE',$7,$8)`, ids.New(), org, c.property.ID, entity.id, entity.kind, entity.version, data, actor); e != nil {
				return e
			}
			if e = businessEvents.CommercialCommit(ctx, tx, c.domain.Auth.Service.Outbox, org, c.property.ID, entity.id, actor, entity.kind, "SCHEMA18_MIGRATION_FIXTURE", entity.event, entity.version, map[string]any{"fixture": "SCHEMA18_MIGRATION_ONLY"}); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	var before, after string
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT to_jsonb(i)::text FROM folio_items i WHERE id=$1`, item).Scan(&before); e != nil {
		t.Fatal(e)
	}
	sqlDB, e := migrate.Open(c.db.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer sqlDB.Close()
	if e = migrate.Up(t.Context(), sqlDB, migrations.FS); e != nil {
		t.Fatal(e)
	}
	if e = migrate.Up(t.Context(), sqlDB, migrations.FS); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = c.domain.ProcessFolioFiscalMaintenance(t.Context(), 50); e != nil {
			t.Fatal(e)
		}
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT to_jsonb(i)::text FROM folio_items i WHERE id=$1`, item).Scan(&after); e != nil || before != after {
		t.Fatalf("immutable original gross was rewritten %v", e)
	}
	records, e := hospitality.ListFiscalReviews(t.Context(), c.db.Pool, org, folio, 50, 0)
	if e != nil || len(records) != 1 || records[0].State != "ITEMIZED" || records[0].Breakdown.GrossMinor != v.Reservation.AmountMinor {
		t.Fatalf("backfill source mismatch %+v %v", records, e)
	}
	f, e := hospitality.GetFolio(t.Context(), c.db.Pool, org, folio)
	if e != nil || f.Version != 2 || f.State != "OPEN" {
		t.Fatalf("backfill created duplicate transitions %+v %v", f, e)
	}
	var serviceActor bool
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT created_by IS NULL AND created_by_service='business-operations-service' FROM folio_fiscal_reviews WHERE id=$1`, records[0].ID).Scan(&serviceActor); e != nil || !serviceActor {
		t.Fatalf("backfill impersonated original staff %v", e)
	}
	balance, e := hospitality.Balance(t.Context(), c.db.Pool, org, folio, time.Now())
	if e != nil || balance != v.Reservation.AmountMinor {
		t.Fatalf("backfill fabricated settlement %d %v", balance, e)
	}
}
