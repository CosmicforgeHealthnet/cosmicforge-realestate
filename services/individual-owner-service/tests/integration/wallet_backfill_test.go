package integration

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"testing"
)

func TestWalletBackfillDryRunResumeAndReplay(t *testing.T) {
	h := newHarness(t)
	a, b, c := h.register(false), h.register(true), h.register(true)
	ctx := context.Background()
	if _, e := h.db.Pool.Exec(ctx, `DELETE FROM wallet_registration_delivery WHERE account_id IN($1,$2)`, a.id, b.id); e != nil {
		t.Fatal(e)
	}
	if _, e := h.db.Pool.Exec(ctx, `UPDATE wallet_registration_delivery SET delivered_at=now(),attempts=3 WHERE account_id=$1`, c.id); e != nil {
		t.Fatal(e)
	}
	dry, e := usecases.QueueWalletBackfill(ctx, h.db.Pool, ids.Nil, 100, true)
	if e != nil || dry.Scanned != 3 || dry.Queued != 2 || h.count(`SELECT count(*) FROM wallet_registration_delivery`) != 1 {
		t.Fatal("dry run mutated", dry, e)
	}
	cursor := ids.Nil
	var scanned int
	var queued int64
	for range 4 {
		p, e := usecases.QueueWalletBackfill(ctx, h.db.Pool, cursor, 1, false)
		if e != nil {
			t.Fatal(e)
		}
		scanned += p.Scanned
		queued += p.Queued
		cursor = p.NextCursor
		if p.Scanned == 0 {
			break
		}
	}
	if scanned != 3 || queued != 2 || h.count(`SELECT count(*) FROM wallet_registration_delivery WHERE account_id=$1 AND delivered_at IS NOT NULL AND attempts=3`, c.id) != 1 {
		t.Fatal("backfill lost progress", scanned, queued)
	}
	replay, e := usecases.QueueWalletBackfill(ctx, h.db.Pool, ids.Nil, 100, false)
	if e != nil || replay.Queued != 0 {
		t.Fatal("non-idempotent backfill", replay, e)
	}
	if _, e = usecases.QueueWalletBackfill(ctx, h.db.Pool, ids.Nil, 1001, false); e == nil {
		t.Fatal("unbounded backfill")
	}
}
func TestFinancialWalletDependencyUnavailable(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.expect(h.get(o, "/financial-wallet"), 503, "FEATURE_UNAVAILABLE")
}
