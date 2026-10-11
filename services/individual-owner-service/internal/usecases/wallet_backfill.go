package usecases

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WalletBackfillPage struct {
	Scanned    int    `json:"scanned"`
	Queued     int64  `json:"queued"`
	DryRun     bool   `json:"dry_run"`
	NextCursor ids.ID `json:"next_cursor"`
}

// QueueWalletBackfill runs only against the Owner-owned database, using the
// existing durable delivery queue. Restart/replay never changes a balance.
func QueueWalletBackfill(ctx context.Context, pool *pgxpool.Pool, after ids.ID, limit int, dryRun bool) (WalletBackfillPage, error) {
	out := WalletBackfillPage{DryRun: dryRun, NextCursor: after}
	if limit < 1 || limit > 1000 {
		return out, precondition("INVALID_BACKFILL_LIMIT", "Use a batch size from 1 to 1000.")
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT account_id FROM owner_accounts WHERE status<>'CLOSED' AND ($1::uuid IS NULL OR account_id>$1) ORDER BY account_id LIMIT $2`, after, limit)
	if e != nil {
		return out, e
	}
	accounts := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		accounts = append(accounts, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, id := range accounts {
		out.Scanned++
		out.NextCursor = id
		if dryRun {
			var missing bool
			if e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM wallet_registration_delivery WHERE account_id=$1)`, id).Scan(&missing); e != nil {
				return out, e
			}
			if missing {
				out.Queued++
			}
			continue
		}
		tag, e := tx.Exec(ctx, `INSERT INTO wallet_registration_delivery(account_id,registration_ref) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, "owner-registration:"+id.String())
		if e != nil {
			return out, e
		}
		out.Queued += tag.RowsAffected()
	}
	if dryRun {
		return out, nil
	}
	return out, tx.Commit(ctx)
}
