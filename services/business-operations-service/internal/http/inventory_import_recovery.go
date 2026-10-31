package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) importRecoveryPlan(w http.ResponseWriter, r *http.Request) {
	_, j, err := h.importTarget(r, "imports:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT jsonb_build_object('source_key',source_key,'row_number',source_row_number,'node_id',node_id,'applied_operation',action,'applied_version',applied_version,'mapping_id',mapping_id,'mapping_created',mapping_created,'recovery_status',recovery_status,'plan_action',CASE WHEN status='QUARANTINED' OR status='INVALID' THEN 'RECONCILE' WHEN action='CREATE' AND status='APPLIED' THEN 'ARCHIVE_IF_UNCHANGED_DRAFT' WHEN action='UPDATE' AND status='APPLIED' THEN 'RESTORE_IF_UNCHANGED_DRAFT' ELSE 'NONE' END,'before',before_snapshot,'after',after_snapshot,'error_code',error_code) FROM inventory_import_records WHERE job_id=$1 ORDER BY row_number DESC LIMIT $2 OFFSET $3`, j.id, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		items = append(items, raw)
	}
	reply(w, r, 200, map[string]any{"import_id": j.id, "items": items, "limit": limit, "offset": offset, "policy": "Bounded reverse-order soft archival/restore only for unchanged never-active drafts. Acquired dependencies, authority loss or version conflicts produce reconciliation tasks. External mappings retain immutable canonical traceability; no automatic deletion."}, rows.Err())
}
func (h *Handler) importRollback(w http.ResponseWriter, r *http.Request) {
	var in ImportControl
	if !decode(w, r, &in) {
		return
	}
	_, j, err := h.importTarget(r, "imports:write", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A current import version and recovery reason are required."))
		return
	}
	h.execute(w, r, "inventory:import:rollback:"+j.id.String(), in, "imports:write", "BRANCH", j.branch, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "imports:write", "BRANCH", j.branch, true)
		if err != nil {
			return 0, nil, err
		}
		current, err := loadImport(ctx, tx, t.Organisation, j.id, true)
		if err != nil {
			return 0, nil, err
		}
		if current.version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Import version changed.")
		}
		if current.status != "COMPLETED" && current.status != "COMPLETED_WITH_ERRORS" && current.status != "CANCELLED" && current.status != "PAUSED" {
			return 0, nil, apperrors.Conflict("IMPORT_RUNNING", "Pause or finish the import before recovery.")
		}
		rows, err := tx.Query(ctx, `SELECT row_number,node_id,action,applied_version,before_snapshot FROM inventory_import_records WHERE job_id=$1 AND status='APPLIED' AND recovery_status='PENDING' ORDER BY row_number DESC LIMIT 20`, j.id)
		if err != nil {
			return 0, nil, err
		}
		type undo struct {
			number  int
			id      ids.ID
			action  string
			version int64
			before  []byte
		}
		operations := []undo{}
		for rows.Next() {
			var operation undo
			if err = rows.Scan(&operation.number, &operation.id, &operation.action, &operation.version, &operation.before); err != nil {
				break
			}
			operations = append(operations, operation)
		}
		e := rows.Err()
		rows.Close()
		if err == nil {
			err = e
		}
		if err != nil {
			return 0, nil, err
		}
		restored, reconcile := 0, 0
		for _, operation := range operations {
			sp, e := tx.Begin(ctx)
			if e != nil {
				return 0, nil, e
			}
			recoverOne := func() (string, error) {
				node, e := portfolio.Get(ctx, sp, t.Organisation, operation.id)
				if e != nil {
					return "", e
				}
				if node.Version != operation.version || node.State != "DRAFT" || node.ActivatedAt != nil {
					return "", apperrors.Conflict("RECOVERY_CONFLICT", "Canonical identity changed after import.")
				}
				if _, _, e = h.authorize(ctx, sp, portfolio.Capability(node.Kind, true), string(node.Kind), node.ID, false); e != nil {
					return "", e
				}
				var conflict bool
				e = sp.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_edges e JOIN inventory_nodes n ON n.id=e.child_id AND n.organisation_id=e.organisation_id WHERE e.organisation_id=$1 AND e.parent_id=$2 AND n.state NOT IN ('ARCHIVED','RETIRED')) OR EXISTS(SELECT 1 FROM inventory_media_links WHERE organisation_id=$1 AND node_id=$2) OR EXISTS(SELECT 1 FROM inventory_verification_links WHERE organisation_id=$1 AND node_id=$2) OR EXISTS(SELECT 1 FROM inventory_import_records r JOIN inventory_external_mappings m ON m.id=r.mapping_id WHERE r.job_id=$3 AND r.row_number=$4 AND (m.node_id<>r.node_id OR m.version IS DISTINCT FROM r.mapping_version))`, t.Organisation, node.ID, j.id, operation.number).Scan(&conflict)
				if e != nil {
					return "", e
				}
				if conflict {
					return "", apperrors.Conflict("RECOVERY_DEPENDENCIES", "Acquired dependencies or remapped identities require reconciliation.")
				}
				recovery := "RESTORED"
				if operation.action == "CREATE" {
					recovery = "ARCHIVED"
					_, e = sp.Exec(ctx, `UPDATE inventory_nodes SET state='ARCHIVED',version=version+1,updated_at=now(),updated_by=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, node.ID, t.Account)
				} else {
					var original portfolio.Node
					if e = json.Unmarshal(operation.before, &original); e != nil {
						return "", e
					}
					attrs, _ := json.Marshal(original.Attributes)
					_, e = sp.Exec(ctx, `UPDATE inventory_nodes SET name=$3,code=$4,attributes=$5,room_type_id=$7,version=version+1,updated_at=now(),updated_by=$6 WHERE organisation_id=$1 AND id=$2`, t.Organisation, node.ID, original.Name, original.Code, attrs, t.Account, original.RoomTypeID)
				}
				if e != nil {
					return "", e
				}
				if _, e = sp.Exec(ctx, `SET CONSTRAINTS inventory_node_integrity,inventory_edge_integrity IMMEDIATE`); e != nil {
					return "", e
				}
				if _, e = sp.Exec(ctx, `SET CONSTRAINTS inventory_node_integrity,inventory_edge_integrity DEFERRED`); e != nil {
					return "", e
				}
				if e = (portfolio.Repository{Outbox: h.Auth.Service.Outbox}).Commit(ctx, sp, t.Organisation, node.ID, t.Account, "IMPORT_RECOVERY_"+recovery, &node); e != nil {
					return "", e
				}
				return recovery, nil
			}
			recovery, e := recoverOne()
			if e != nil {
				if e = sp.Rollback(ctx); e != nil {
					return 0, nil, e
				}
				reconcile++
				if _, err = tx.Exec(ctx, `INSERT INTO inventory_import_reconciliation(id,job_id,node_id,action,expected_version,reason) VALUES($1,$2,$3,'MANUAL_REVIEW',$4,'VERSION_AUTHORITY_OR_DEPENDENCY_CONFLICT') ON CONFLICT(job_id,node_id,action) DO NOTHING`, ids.New(), j.id, operation.id, operation.version); err != nil {
					return 0, nil, err
				}
				recovery = "RECONCILE"
			} else {
				if e = sp.Commit(ctx); e != nil {
					return 0, nil, e
				}
				restored++
			}
			if _, err = tx.Exec(ctx, `UPDATE inventory_import_records SET recovery_status=$3 WHERE job_id=$1 AND row_number=$2`, j.id, operation.number, recovery); err != nil {
				return 0, nil, err
			}
		}
		var remaining, bad int
		if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='APPLIED' AND recovery_status='PENDING'),count(*) FILTER(WHERE recovery_status='RECONCILE') FROM inventory_import_records WHERE job_id=$1`, j.id).Scan(&remaining, &bad); err != nil {
			return 0, nil, err
		}
		state := "RUNNING"
		if remaining == 0 {
			state = "APPLIED"
			if bad > 0 {
				state = "PARTIAL"
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE inventory_import_jobs SET status=CASE WHEN status='PAUSED' THEN 'CANCELLED' ELSE status END,recovery_state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, j.id, state); err != nil {
			return 0, nil, err
		}
		if err = h.accessEvent(ctx, tx, t, "INVENTORY_IMPORT_RECOVERY", "business.inventory.import.changed.v1", "inventory_import", j.id, map[string]any{"status": "RECOVERY_" + state, "restored": restored, "reconciliation_tasks": reconcile, "reason": in.Reason}); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"import_id": j.id, "recovery_state": state, "restored": restored, "reconciliation_tasks": reconcile, "remaining": remaining, "version": current.version + 1}, nil
	})
}
