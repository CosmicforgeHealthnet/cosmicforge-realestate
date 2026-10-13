package usecases

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// RetainEventFailure is called before a poison/exhausted message is terminated.
// Idempotent reference retention never copies a financial or private payload.
func (s *Service) RetainEventFailure(ctx context.Context, f jetstream.Failure) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO owner_event_review
 (id,consumer,stream,stream_sequence,event_id,payload_sha256,failure_class,attempt,observed_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(consumer,stream,stream_sequence) DO NOTHING`,
		ids.New(), f.Consumer, f.Stream, int64(f.Sequence), f.EventID, f.Digest, f.Class, int64(f.Attempt), s.now())
	return err
}

// EventReviewDecision records operator review, not a financial approval or
// execution/replay result. The original producer must perform approved replay.
type EventReviewDecision struct {
	Decision    string `json:"decision" v:"required,enum=REPLAY_APPROVED|NO_ACTION"`
	ReasonCode  string `json:"reason_code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
	EvidenceRef ids.ID `json:"evidence_ref" v:"required"`
}

func (s *Service) DecideEventReview(ctx context.Context, subj authz.Subject, id ids.ID, in EventReviewDecision) (EventReviewDecision, error) {
	if err := schema.Validate(in); err != nil {
		return in, err
	}
	if _, err := s.authorize(ctx, subj, authz.ActInternalAdminDispute, ids.Nil); err != nil {
		return in, err
	}
	err := s.InTx(ctx, func(ctx context.Context) error {
		var found ids.ID
		if err := mustTx(ctx).QueryRow(ctx, `SELECT id FROM owner_event_review WHERE id=$1 FOR UPDATE`, id).Scan(&found); isNoRows(err) {
			return notFound("event_review")
		} else if err != nil {
			return err
		}
		tag, err := mustTx(ctx).Exec(ctx, `INSERT INTO owner_event_review_decisions(id,review_id,decision,reason_code,evidence_ref,actor_id,decided_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(review_id) DO NOTHING`, ids.New(), id, in.Decision, in.ReasonCode, in.EvidenceRef, subj.ActorID, s.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return conflict("EVENT_ALREADY_REVIEWED", "The incident has a retained decision.")
		}
		return s.record(ctx, "owner.event_review.decided", "event_review", id, map[string]any{"decision": in.Decision, "reason_code": in.ReasonCode, "evidence_ref": in.EvidenceRef}, nil)
	})
	return in, err
}
