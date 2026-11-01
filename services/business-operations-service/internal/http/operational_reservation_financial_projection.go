package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"time"
)

func (h *Handler) projectReservationFinancialRoots(ctx context.Context, tx pgx.Tx, job reservationFinancialJob, observation reservationFinancialObservation, observedState string, now time.Time) error {
	// Split children and amendment-credit descendants depend on the same owning
	// receipt. Update their projections in this transaction as well as the source
	// root, while retaining every independent price/collection reference.
	refs, e := commercialJSONRows(ctx, tx, `WITH RECURSIVE affected(id) AS (SELECT $2::uuid UNION SELECT c.target_group_id FROM affected a JOIN reservation_financial_credits c ON c.organisation_id=$1 AND c.source_group_id=a.id), roots(id) AS (SELECT $3::uuid UNION SELECT r.reservation_id FROM reservation_rooms r JOIN affected a ON a.id=r.financial_group_id WHERE r.organisation_id=$1 AND r.state='ACTIVE') SELECT jsonb_build_object('id',id) FROM roots ORDER BY id LIMIT 201`, job.OrganisationID, job.FinancialGroupID, job.ReservationID)
	if e != nil {
		return e
	}
	if len(refs) > 200 {
		return apperrors.Conflict("FINANCIAL_ROOT_REVIEW_REQUIRED", "Financial projection graph exceeds its bounded reconciliation batch.")
	}
	for _, raw := range refs {
		var ref struct {
			ID ids.ID `json:"id"`
		}
		if e = json.Unmarshal(raw, &ref); e != nil {
			return e
		}
		v, e := reservations.Get(ctx, tx, job.OrganisationID, ref.ID)
		if e != nil {
			return e
		}
		rooms, e := reservations.Rooms(ctx, tx, job.OrganisationID, v.ID, true)
		if e != nil {
			return e
		}
		state := observedState
		if len(rooms) > 0 {
			var disputed, unknown, failed, pending, guarantee bool
			if e = tx.QueryRow(ctx, `WITH RECURSIVE backing(id) AS (SELECT financial_group_id FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE' UNION SELECT c.source_group_id FROM backing b JOIN reservation_financial_credits c ON c.organisation_id=$1 AND c.target_group_id=b.id) SELECT coalesce(bool_or(f.state='DISPUTED'),false),coalesce(bool_or(f.state='UNKNOWN'),false),coalesce(bool_or(f.state='FAILED'),false),coalesce(bool_or(f.state NOT IN ('DISPUTED','UNKNOWN','FAILED','VERIFIED','APPROVED_GUARANTEE')),false),coalesce(bool_or(f.state='APPROVED_GUARANTEE'),false) FROM backing b JOIN reservation_financial_groups f ON f.organisation_id=$1 AND f.id=b.id`, job.OrganisationID, v.ID).Scan(&disputed, &unknown, &failed, &pending, &guarantee); e != nil {
				return e
			}
			state = "VERIFIED"
			switch {
			case disputed:
				state = "DISPUTED"
			case unknown:
				state = "UNKNOWN"
			case failed:
				state = "FAILED"
			case pending:
				state = "PENDING"
			case guarantee:
				state = "APPROVED_GUARANTEE"
			}
			if state == "VERIFIED" || state == "APPROVED_GUARANTEE" {
				for _, room := range rooms {
					if e = h.reservationFinancialEligibility(ctx, tx, job.OrganisationID, room.FinancialGroupID); e != nil {
						if !isReservationFinancialPending(e) {
							return e
						}
						state = "PENDING"
						break
					}
				}
			}
		}
		if v.ID != job.ReservationID && v.FinancialState == state {
			continue
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET financial_state=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, v.ID, state, now); e != nil {
			return e
		}
		v, e = reservations.Get(ctx, tx, job.OrganisationID, v.ID)
		if e != nil {
			return e
		}
		if e = h.reservationCommit(ctx, tx, tenant{Organisation: job.OrganisationID, Account: job.CreatedBy}, v, "PAYMENT_READ_BACK", "reservation.modified.v1", "Authoritative bound Payment Service collection and remedy readback", map[string]any{"financial_group_id": job.FinancialGroupID, "intent_id": observation.Intent.ID, "collection_state": observation.Intent.State, "settlement_state": observation.Intent.SettlementState, "reversed_minor": observation.Receipt.ReversedMinor, "remedy_version": observation.Receipt.RemedyVersion, "remedy_review_required": observation.Receipt.RemedyReviewRequired, "funding_confirmed": observation.Receipt.FundingConfirmed}); e != nil {
			return e
		}
	}
	return nil
}
