package businesshttp

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

// Accepted terms opt each reservation in. A separate current, scoped and
// independently approved operator policy bounds execution and cumulative gross
// exposure. Neither enabling a new policy nor elapsed time rewrites old terms.
func (h *Handler) ProcessPMSArrivals(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT r.organisation_id,r.property_id,r.id,p.id FROM reservations r JOIN hospitality_arrival_policies p ON p.organisation_id=r.organisation_id AND p.property_id=r.property_id LEFT JOIN hospitality_arrival_cursors c ON c.organisation_id=r.organisation_id AND c.reservation_id=r.id WHERE r.state='CONFIRMED' AND r.allocation_state='ACTIVE' AND coalesce((r.policy_snapshot->>'automatic_no_show')::boolean,false) AND r.date_from<=($1::timestamptz AT TIME ZONE 'UTC')::date+1 AND p.mode='AUTOMATIC_NO_SHOW' AND p.effective_at<=$1 AND p.expires_at>$1 AND r.source=ANY(p.allowed_sources) AND NOT EXISTS(SELECT 1 FROM hospitality_arrival_policies later WHERE later.organisation_id=p.organisation_id AND later.property_id=p.property_id AND later.version>p.version) AND NOT EXISTS(SELECT 1 FROM hospitality_arrival_revocations rev WHERE rev.organisation_id=p.organisation_id AND rev.policy_id=p.id) AND coalesce(c.state,'READY')='READY' AND (c.next_check_at IS NULL OR c.next_check_at<=$1) ORDER BY coalesce(c.next_check_at,r.created_at),r.id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, property, id, policy ids.ID }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.property, &v.id, &v.policy); e != nil {
			break
		}
		refs = append(refs, v)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	processed := map[ids.ID]int{}
	var failures []error
	for _, ref := range refs {
		var operationErr error
		marked := false
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, ref.org); e != nil {
				return e
			}
			p, e := loadPMSArrivalPolicy(ctx, tx, ref.org, ref.property)
			if e != nil {
				return e
			}
			now := h.Auth.Service.Now().UTC()
			var stopped bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hospitality_arrival_revocations WHERE organisation_id=$1 AND policy_id=$2) OR EXISTS(SELECT 1 FROM hospitality_arrival_cursors WHERE organisation_id=$1 AND reservation_id=$3 AND (next_check_at>$4 OR state='QUARANTINED'))`, ref.org, p.ID, ref.id, now).Scan(&stopped); e != nil {
				return e
			}
			if stopped || p.ID != ref.policy || p.Mode != "AUTOMATIC_NO_SHOW" || now.Before(p.EffectiveAt) || !now.Before(p.ExpiresAt) || processed[p.ID] >= p.MaxReservationsPerRun {
				return nil
			}
			v, e := reservations.Get(ctx, tx, ref.org, ref.id)
			if e != nil {
				return e
			}
			if v.PropertyID != p.PropertyID || v.State != "CONFIRMED" || v.AllocationState != "ACTIVE" || !v.PolicySnapshot.AutomaticNoShow || !slices.Contains(p.AllowedSources, v.Source) {
				return nil
			}
			if _, e = tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO hospitality_arrival_cursors(organisation_id,property_id,reservation_id,next_check_at) VALUES($1,$2,$3,$4) ON CONFLICT(organisation_id,reservation_id) DO UPDATE SET next_check_at=excluded.next_check_at`, ref.org, ref.property, v.ID, now.Add(time.Minute)); e != nil {
				return e
			}
			t := tenant{Organisation: ref.org}
			if _, e = tx.Exec(ctx, `SAVEPOINT pms_arrival`); e != nil {
				return e
			}
			operationErr = func() error {
				var raw []byte
				if e = tx.QueryRow(ctx, `SELECT jsonb_build_object('reservation',to_jsonb(r),'assignments',(SELECT coalesce(jsonb_agg(jsonb_build_array(a.id,a.version,a.state,s.version,s.state) ORDER BY a.id),'[]') FROM room_assignments a LEFT JOIN room_status_current s ON s.organisation_id=a.organisation_id AND s.room_id=a.room_id WHERE a.organisation_id=r.organisation_id AND a.reservation_id=r.id),'stays',(SELECT coalesce(jsonb_agg(jsonb_build_array(st.id,st.version,st.state) ORDER BY st.id),'[]') FROM stay_records st WHERE st.organisation_id=r.organisation_id AND st.reservation_id=r.id)) FROM reservations r WHERE r.organisation_id=$1 AND r.id=$2`, ref.org, v.ID).Scan(&raw); e != nil {
					return e
				}
				digest := security.SHA256Hex(raw)
				finish := func(state, code string) error { return h.recordPMSArrivalRun(ctx, tx, t, p, v, digest, state, code) }
				for _, capability := range []permissions.Permission{"reservations:cancel", "pms:write"} {
					allowed, e := h.currentRecordedPMSAuthority(ctx, tx, ref.org, ref.property, p.AuthorizingMembershipID, p.AuthorizingRoleID, capability)
					if e != nil {
						return e
					}
					if !allowed {
						return finish("AUTHORITY_REVOKED", "CURRENT_DIRECT_AUTHORITY_REQUIRED")
					}
				}
				rooms, e := reservations.Rooms(ctx, tx, ref.org, v.ID, true)
				if e != nil {
					return e
				}
				if len(rooms) == 0 {
					return finish("REVIEW_REQUIRED", "ACTIVE_ACCEPTED_ROOMS_REQUIRED")
				}
				deadline := time.Time{}
				if v.ConfirmedAt == nil {
					return finish("REVIEW_REQUIRED", "CANONICAL_CONFIRMATION_EVIDENCE_REQUIRED")
				}
				for _, room := range rooms {
					arrival, e := hospitality.LocalInstant(room.DateFrom, v.PolicySnapshot.CheckInLocal, v.PolicySnapshot.AcceptanceTimezone)
					if e != nil {
						return e
					}
					due := arrival.Add(time.Duration(v.PolicySnapshot.NoShowGraceHours) * time.Hour)
					if due.Before(*v.ConfirmedAt) {
						return finish("REVIEW_REQUIRED", "LATE_BOOKING_REQUIRES_MANUAL_ARRIVAL_REVIEW")
					}
					if deadline.Before(due) {
						deadline = due
					}
				}
				if now.Before(deadline) {
					_, e = tx.Exec(ctx, `UPDATE hospitality_arrival_cursors SET next_check_at=$3 WHERE organisation_id=$1 AND reservation_id=$2`, ref.org, v.ID, deadline)
					return e
				}
				var blocked bool
				if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stay_records WHERE organisation_id=$1 AND reservation_id=$2) OR EXISTS(SELECT 1 FROM room_assignments a LEFT JOIN room_status_current s ON s.organisation_id=a.organisation_id AND s.room_id=a.room_id WHERE a.organisation_id=$1 AND a.reservation_id=$2 AND (a.state='OCCUPIED' OR a.state='ASSIGNED' AND (s.id IS NULL OR s.state NOT IN ('VACANT_CLEAN','VACANT_DIRTY','INSPECTED')))) OR EXISTS(SELECT 1 FROM reservation_conflicts WHERE organisation_id=$1 AND reservation_id=$2 AND state NOT IN ('RESOLVED','REJECTED')) OR NOT EXISTS(SELECT 1 FROM inventory_nodes n WHERE n.organisation_id=$1 AND n.id=$3 AND n.kind='PROPERTY' AND n.state='ACTIVE')`, ref.org, v.ID, ref.property).Scan(&blocked); e != nil {
					return e
				}
				if blocked || v.FinancialState == "DISPUTED" {
					return finish("REVIEW_REQUIRED", "OCCUPANCY_SOURCE_OR_PROPERTY_REVIEW_REQUIRED")
				}
				for _, room := range rooms {
					if e = reservations.RequirePhysicalBacking(ctx, tx, ref.org, room.ID, inventory.DateRange{DateFrom: room.DateFrom, DateTo: room.DateTo}); e != nil {
						var app *apperrors.Error
						if errors.As(e, &app) && app.Status == 409 {
							return finish("REVIEW_REQUIRED", "OVERBOOKING_RECOVERY_REQUIRES_MANUAL_REVIEW")
						}
						return e
					}
				}
				if e = h.reservationRiskGate(ctx, tx, ref.org, v); e != nil {
					var app *apperrors.Error
					if errors.As(e, &app) && app.Status == 409 {
						return finish("REVIEW_REQUIRED", "RISK_REVIEW_REQUIRED")
					}
					return e
				}
				var spent int64
				if e = tx.QueryRow(ctx, `SELECT coalesce(sum(original_amount_minor),0)::bigint FROM hospitality_arrival_runs WHERE organisation_id=$1 AND policy_id=$2 AND state='MARKED_NO_SHOW'`, ref.org, p.ID).Scan(&spent); e != nil {
					return e
				}
				if v.Currency != p.Currency || v.AmountMinor > p.MaxReservationMinor || v.AmountMinor > p.MaxTotalMinor || spent > p.MaxTotalMinor-v.AmountMinor {
					return finish("BUDGET_EXHAUSTED", "APPROVED_CUMULATIVE_EXPOSURE_LIMIT")
				}
				assignments, e := hospitality.Load[[]hospitality.Assignment](ctx, tx, `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]'::jsonb) FROM room_assignments a WHERE organisation_id=$1 AND reservation_id=$2 AND state='ASSIGNED'`, ref.org, v.ID)
				if e != nil {
					return e
				}
				for _, a := range assignments {
					if _, e = tx.Exec(ctx, `UPDATE room_assignment_days SET released_at=$3 WHERE organisation_id=$1 AND assignment_id=$2 AND released_at IS NULL`, ref.org, a.ID, now); e != nil {
						return e
					}
					if _, e = tx.Exec(ctx, `UPDATE room_assignments SET state='RELEASED',version=version+1,updated_at=$3,reason='Accepted no-show deadline under bounded approved policy' WHERE organisation_id=$1 AND id=$2`, ref.org, a.ID, now); e != nil {
						return e
					}
					if e = h.pmsCommit(ctx, tx, t, ref.property, a.ID, "room_assignment", "ASSIGNMENT_RELEASED", "hospitality.room.assigned.v1", a.Version+1, map[string]any{"reservation_id": v.ID, "arrival_policy_id": p.ID, "actor_service": "business-operations-service", "reason": "Release only a verified unoccupied assignment before the accepted no-show command"}); e != nil {
						return e
					}
				}
				command := reservations.Command{Version: v.Version, Reason: "Accepted no-show deadline elapsed under independently approved bounded arrival policy"}
				event, e := h.executeReservationRelease(ctx, tx, t, v, command, "NO_SHOW", false)
				if e != nil {
					return e
				}
				next, e := reservations.Get(ctx, tx, ref.org, v.ID)
				if e != nil {
					return e
				}
				if e = h.reservationCommit(ctx, tx, t, next, "NO_SHOW", event, command.Reason, map[string]any{"arrival_policy_id": p.ID, "accepted_policy_id": v.PolicyID, "deadline": deadline, "actor_service": "business-operations-service"}); e != nil {
					return e
				}
				if e = h.recordPMSArrivalRun(ctx, tx, t, p, next, digest, "MARKED_NO_SHOW", "ACCEPTED_DEADLINE_ELAPSED"); e != nil {
					return e
				}
				if e = h.queueNotice(ctx, tx, p.CreatedBy, "Automatic arrival status recorded", "Reservation "+v.ID.String()+" reached its accepted no-show deadline under reviewed policy "+p.ID.String()+". Any financial remedy remains separately pending owning-service reconciliation.", false, "reservation:"+v.ID.String()); e != nil {
					return e
				}
				marked = true
				return nil
			}()
			if operationErr == nil {
				if _, e = tx.Exec(ctx, `RELEASE SAVEPOINT pms_arrival`); e != nil {
					return e
				}
				_, e = tx.Exec(ctx, `UPDATE hospitality_arrival_cursors SET consecutive_failures=0,last_error_code='' WHERE organisation_id=$1 AND reservation_id=$2`, ref.org, v.ID)
				return e
			}
			marked = false
			if _, e = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT pms_arrival`); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `RELEASE SAVEPOINT pms_arrival`); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE hospitality_arrival_cursors SET consecutive_failures=consecutive_failures+1,state=CASE WHEN consecutive_failures+1>=12 THEN 'QUARANTINED' ELSE 'READY' END,last_error_code='SOURCE_TRANSACTION_FAILED',next_check_at=$3 WHERE organisation_id=$1 AND reservation_id=$2`, ref.org, v.ID, now.Add(5*time.Minute)); e != nil {
				return e
			}
			return h.recordPMSArrivalRun(ctx, tx, t, p, v, security.SHA256Hex([]byte(v.ID.String()+":SOURCE_TRANSACTION_FAILED")), "SOURCE_REVIEW_REQUIRED", "SOURCE_TRANSACTION_FAILED")
		})
		if e != nil {
			failures = append(failures, e)
		} else if operationErr != nil {
			failures = append(failures, operationErr)
		} else if marked {
			processed[ref.policy]++
		}
	}
	return errors.Join(failures...)
}
func (h *Handler) recordPMSArrivalRun(ctx context.Context, tx pgx.Tx, t tenant, p PMSArrivalPolicy, v reservations.Reservation, digest, state, code string) error {
	id := ids.New()
	evidence := map[string]any{"code": code, "approval_id": p.ApprovalID, "policy_version": p.Version, "accepted_policy_id": v.PolicyID, "actual_refund_executed": false, "financial_execution": "PAYMENT_SERVICE_ONLY"}
	tag, e := tx.Exec(ctx, `INSERT INTO hospitality_arrival_runs(id,organisation_id,property_id,policy_id,reservation_id,reservation_version,source_digest,state,original_amount_minor,currency,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(policy_id,reservation_id,source_digest,state) DO NOTHING`, id, t.Organisation, p.PropertyID, p.ID, v.ID, v.Version, digest, state, v.AmountMinor, v.Currency, marshalCommercial(evidence))
	if e != nil || tag.RowsAffected() == 0 {
		return e
	}
	return h.pmsCommit(ctx, tx, t, p.PropertyID, id, "hospitality_arrival_run", "ARRIVAL_AUTOMATION_EXECUTED", "hospitality.arrival.automation.executed.v1", 1, map[string]any{"reservation_id": v.ID, "reservation_version": v.Version, "policy_id": p.ID, "source_digest": digest, "state": state, "evidence": evidence, "actor_service": "business-operations-service"})
}
