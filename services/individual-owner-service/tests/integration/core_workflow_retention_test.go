package integration

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"io/fs"
	"strings"
	"testing"
)

// Immutable source references only; this fixture certifies migration retention,
// not a payment, financial approval, wallet balance or provider outcome.
func TestCoreWorkflowHistoryCannotBeReassignedOrRolledBack(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "Financial Reference Retention Street")
	business, quote, intent := ids.New(), ids.New(), ids.New()
	_, e := h.db.Pool.Exec(context.Background(), `INSERT INTO financial_core_business_bindings(business_type,business_id,property_id,owner_account_id,quote_id,intent_id,amount_minor,currency,business_context,quote_snapshot,intent_snapshot,observed_state,settlement_state) VALUES('shortlet_booking',$1,$2,$3,$4,$5,1,'NGN','{}','{}','{}','NOT_INITIATED','PENDING')`, business, p.ID, o.id, quote, intent)
	if e != nil {
		t.Fatal(e)
	}
	for _, sql := range []string{`UPDATE financial_core_business_bindings SET quote_id=$1`, `DELETE FROM financial_core_business_bindings`, `TRUNCATE financial_core_business_bindings`} {
		var e error
		if strings.Contains(sql, "$1") {
			_, e = h.db.Pool.Exec(context.Background(), sql, ids.New())
		} else {
			_, e = h.db.Pool.Exec(context.Background(), sql)
		}
		if e == nil {
			t.Fatal("financial source identity was mutable", sql)
		}
	}
	if _, e = h.db.Pool.Exec(context.Background(), `UPDATE financial_core_business_bindings SET observed_state='INITIALIZED' WHERE business_id=$1`, business); e != nil {
		t.Fatal("readback observation cannot advance", e)
	}
	raw, e := fs.ReadFile(migrations.FS, "00021_financial_core_workflows.sql")
	if e != nil {
		t.Fatal(e)
	}
	down := strings.SplitN(string(raw), "-- +goose Down", 2)[1]
	tx, e := h.db.Pool.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(context.Background(), down); e == nil {
		t.Fatal("populated financial history rollback succeeded")
	}
	if h.count(`SELECT count(*) FROM financial_core_business_bindings WHERE business_id=$1 AND quote_id=$2 AND intent_id=$3 AND owner_account_id=$4`, business, quote, intent, o.id) != 1 {
		t.Fatal("original issuer/history changed")
	}
}
