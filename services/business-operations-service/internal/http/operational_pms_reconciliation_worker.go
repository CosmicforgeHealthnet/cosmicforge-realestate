package businesshttp

import (
	"context"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

// Source reconciliation is maintenance of owned facts, not a grant to create
// guests, free occupied stock, declare cleanliness or execute provider commands.
func (h *Handler) ProcessPMSRooms(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT s.organisation_id,s.property_id,s.room_id FROM room_status_current s LEFT JOIN hospitality_room_cursors c ON c.organisation_id=s.organisation_id AND c.room_id=s.room_id WHERE coalesce(c.state,'READY')='READY' AND (c.next_check_at IS NULL OR c.next_check_at<=$1) ORDER BY coalesce(c.next_check_at,s.created_at),s.room_id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, property, room ids.ID }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if e = rows.Scan(&r.org, &r.property, &r.room); e != nil {
			break
		}
		refs = append(refs, r)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	var failures []error
	for _, r := range refs {
		var sourceError error
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, r.org); e != nil {
				return e
			}
			now := h.Auth.Service.Now().UTC()
			var skip bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hospitality_room_cursors WHERE organisation_id=$1 AND room_id=$2 AND (next_check_at>$3 OR state='QUARANTINED'))`, r.org, r.room, now).Scan(&skip); e != nil {
				return e
			}
			if skip {
				return nil
			}
			if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `INSERT INTO hospitality_room_cursors(organisation_id,room_id,next_check_at) VALUES($1,$2,$3) ON CONFLICT(organisation_id,room_id) DO UPDATE SET next_check_at=excluded.next_check_at`, r.org, r.room, now.Add(time.Minute)); e != nil {
				return e
			}
			t := tenant{Organisation: r.org}
			if _, e := tx.Exec(ctx, `SAVEPOINT pms_room_source`); e != nil {
				return e
			}
			_, sourceError = h.reconcilePMSRoom(ctx, tx, t, r.room, roomReconcileOptions{Reason: "Reconcile current owning room, stay and terminal assignment sources"})
			if sourceError == nil {
				if _, e := tx.Exec(ctx, `RELEASE SAVEPOINT pms_room_source`); e != nil {
					return e
				}
				_, e := tx.Exec(ctx, `UPDATE hospitality_room_cursors SET consecutive_failures=0,last_error_code='' WHERE organisation_id=$1 AND room_id=$2`, r.org, r.room)
				return e
			}
			if _, e := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT pms_room_source`); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `RELEASE SAVEPOINT pms_room_source`); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `UPDATE hospitality_room_cursors SET consecutive_failures=consecutive_failures+1,state=CASE WHEN consecutive_failures+1>=12 THEN 'QUARANTINED' ELSE 'READY' END,last_error_code='SOURCE_TRANSACTION_FAILED',next_check_at=$3 WHERE organisation_id=$1 AND room_id=$2`, r.org, r.room, now.Add(5*time.Minute)); e != nil {
				return e
			}
			if _, e := h.openRoomSafetyException(ctx, tx, t, r.property, r.room, security.SHA256Hex([]byte(r.room.String()+":SOURCE_TRANSACTION_FAILED")), []string{"SOURCE_TRANSACTION_FAILED"}); e != nil {
				return e
			}
			_, e := h.recordRoomReconciliation(ctx, tx, t, r.property, r.room, security.SHA256Hex([]byte(r.room.String()+":SOURCE_TRANSACTION_FAILED")), "SOURCE_REVIEW_REQUIRED", map[string]any{"error_code": "SOURCE_TRANSACTION_FAILED", "room_id": r.room}, map[string]any{"room_state_changed": false}, ids.Nil, "Source operation rolled back; bounded retry and qualified repair required")
			return e
		})
		if e != nil {
			failures = append(failures, e)
		} else if sourceError != nil {
			failures = append(failures, sourceError)
		}
	}
	return errors.Join(failures...)
}
