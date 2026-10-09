// Package dbx carries the active transaction through context so that reads
// made on behalf of a command (authorization facts, preconditions) observe
// the same snapshot and row locks as the command itself.
package dbx

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
)

type txKey struct{}

// WithTx returns ctx carrying tx.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// Q returns the transaction in ctx, or fallback.
func Q(ctx context.Context, fallback db.DBTX) db.DBTX {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return fallback
}

// Tx returns the transaction carried by ctx.
func Tx(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}
