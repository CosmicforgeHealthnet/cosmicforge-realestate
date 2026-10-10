package lifecycle

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
)

type Cancellation struct {
	IntentID     ids.ID   `json:"intent_id"`
	AuthorityRef string   `json:"authority_ref"`
	ReasonRef    string   `json:"reason_ref"`
	EvidenceIDs  []ids.ID `json:"evidence_ids"`
}

// Only an unsubmitted intent can be cancelled without an external remedy.
// Collected money follows the amount-specific case/decision/refund engine.
func (s *Service) CancelIntent(ctx context.Context, in Cancellation, key string) (finance.Intent, error) {
	i, e := s.GetIntent(trusted(ctx), in.IntentID)
	if e != nil {
		return i, e
	}
	p, e := core.Caller(ctx)
	if e != nil {
		return i, e
	}
	var source string
	var quote *ids.ID
	e = s.DB.QueryRow(ctx, `SELECT source_service,locked_quote_id FROM payment_intents WHERE intent_id=$1`, in.IntentID).Scan(&source, &quote)
	if e != nil {
		return i, e
	}
	if p.Service != platform.AdminService && string(p.Service) != source {
		return i, core.Fail(403, "CANCELLATION_SOURCE_REQUIRED")
	}
	if key == "" || len(key) > 128 || in.AuthorityRef == "" || in.ReasonRef == "" || quote == nil {
		return i, fail("CANCELLATION_POLICY_EVIDENCE_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return i, e
	}
	defer tx.Rollback(ctx)
	op := "intent-cancellation:" + i.ID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return i, e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM lifecycle_decisions WHERE operation_key=$1`, op).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return i, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return i, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return i, e
	}
	var q Quote
	if e = load(ctx, tx, `SELECT snapshot FROM locked_quotes WHERE quote_id=$1`, *quote, &q); e != nil {
		return i, e
	}
	policy, e := s.Policy(ctx, q.PolicyID)
	if e != nil {
		return i, e
	}
	if !policy.Active || policy.CancellationRef == "" {
		return i, fail("CANCELLATION_POLICY_REQUIRED")
	}
	rp, e := s.remedyPolicy(ctx, tx, q.PolicyID)
	if e != nil {
		return i, e
	}
	if e = s.checkEvidence(ctx, tx, rp, q.BusinessRef, in.EvidenceIDs); e != nil {
		return i, e
	}
	if e = tx.QueryRow(ctx, `SELECT state FROM payment_intents WHERE intent_id=$1 FOR UPDATE`, i.ID).Scan(&i.State); e != nil {
		return i, e
	}
	if i.State != "REQUESTED" {
		return i, core.Fail(409, "ORIGINAL_READBACK_OR_APPROVED_CASE_REFUND_REQUIRED")
	}
	_, e = tx.Exec(ctx, `UPDATE payment_intents SET state='CANCELLED',updated_at=now() WHERE intent_id=$1`, i.ID)
	if e != nil {
		return i, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO lifecycle_decisions(decision_id,aggregate_id,operation_key,action,fingerprint,snapshot) VALUES($1,$2,$3,'CANCELLATION',$4,$5)`, ids.New(), i.ID, op, core.Fingerprint(in), raw(in))
	if e != nil {
		return i, e
	}
	i.State = "CANCELLED"
	if e = s.Audit(ctx, tx, "intent.cancelled_before_submission", i.ID.String(), in); e != nil {
		return i, e
	}
	if e = s.Emit(ctx, tx, "finance.provider_state_changed.v1", "provider_attempt", i.ID, finance.StateChange{AggregateID: i.ID, Kind: "COLLECTION", PriorState: "REQUESTED", State: i.State}); e != nil {
		return i, e
	}
	return i, tx.Commit(ctx)
}
