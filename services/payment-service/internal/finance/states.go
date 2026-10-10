package finance

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5/pgconn"
)

type StateChange struct {
	AggregateID ids.ID `json:"aggregate_id"`
	Kind        string `json:"kind"`
	PriorState  string `json:"prior_state"`
	State       string `json:"state"`
}

// Submission state, audit and publication intent are durable before calling a
// provider. The compare-and-swap is the only authority for a first submission.
func (s *Service) ChangeProviderState(ctx context.Context, kind string, id ids.ID, prior, next, checkout string) (pgconn.CommandTag, error) {
	if e := core.Require(ctx, "payment-service"); e != nil {
		return pgconn.CommandTag{}, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return pgconn.CommandTag{}, e
	}
	defer tx.Rollback(ctx)
	var sql string
	switch kind {
	case "COLLECTION":
		sql = `UPDATE payment_intents SET state=$3,checkout_url=nullif($4,''),updated_at=now() WHERE intent_id=$1 AND state=$2`
	case "PAYOUT":
		sql = `UPDATE payout_attempts SET state=$3,updated_at=now() WHERE payout_id=$1 AND state=$2`
	default:
		return pgconn.CommandTag{}, core.Fail(422, "INVALID_STATE_AGGREGATE")
	}
	args := []any{id, prior, next}
	if kind == "COLLECTION" {
		args = append(args, checkout)
	}
	tag, e := tx.Exec(ctx, sql, args...)
	if e != nil {
		return tag, e
	}
	if tag.RowsAffected() == 1 {
		change := StateChange{id, kind, prior, next}
		if e = s.Audit(ctx, tx, "provider.state_changed", id.String(), change); e != nil {
			return tag, e
		}
		if e = s.Emit(ctx, tx, "finance.provider_state_changed.v1", "provider_attempt", id, change); e != nil {
			return tag, e
		}
	}
	return tag, tx.Commit(ctx)
}
