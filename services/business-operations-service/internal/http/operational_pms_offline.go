package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type PMSOfflineCommand struct {
	PropertyID        ids.ID                `json:"property_id"`
	ClientOperationID ids.ID                `json:"client_operation_id"`
	OriginalActionAt  time.Time             `json:"original_action_at"`
	Action            string                `json:"action"`
	TargetID          ids.ID                `json:"target_id"`
	FolioPosting      *PMSFolioPosting      `json:"folio_posting,omitempty"`
	Assignment        *PMSAssignmentCommand `json:"assignment,omitempty"`
	CheckIn           *PMSCheckInCommand    `json:"check_in,omitempty"`
	Reason            string                `json:"reason"`
}
type PMSOfflineResult struct {
	ID          ids.ID          `json:"id"`
	State       string          `json:"state"`
	FailureCode string          `json:"failure_code"`
	Result      json.RawMessage `json:"result"`
	TaskID      *ids.ID         `json:"task_id,omitempty"`
}
type PMSOfflineResolution struct {
	Disposition       string `json:"disposition"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
}

func (h *Handler) registerPMSOffline(r chi.Router) {
	r.Post("/front-desk/offline-operations", h.pmsOfflineReplay)
	r.Get("/front-desk/offline-operations", h.pmsOfflineReceipts)
	r.Get("/front-desk/reconciliation-tasks", h.pmsOfflineTasks)
	r.Post("/front-desk/reconciliation-tasks/{id}/resolve", h.pmsOfflineResolve)
}
func (h *Handler) pmsOfflineReplay(w http.ResponseWriter, r *http.Request) {
	var in PMSOfflineCommand
	if !decode(w, r, &in) {
		return
	}
	principal, ok := businessauth.PrincipalFrom(r.Context())
	if !ok {
		reply(w, r, 0, nil, apperrors.AuthenticationRequired())
		return
	}
	count := 0
	if in.FolioPosting != nil {
		count++
	}
	if in.Assignment != nil {
		count++
	}
	if in.CheckIn != nil {
		count++
	}
	now := h.Auth.Service.Now()
	cap := "pms:write"
	valid := in.Action == "FOLIO_POST" && in.FolioPosting != nil && validPMSFolioPosting(*in.FolioPosting) || in.Action == "ASSIGN_ROOM" && in.Assignment != nil && validPMSAssignment(*in.Assignment) && in.TargetID == in.Assignment.ReservationID || in.Action == "CHECK_IN" && in.CheckIn != nil && validPMSCheckIn(*in.CheckIn)
	if count != 1 || !valid || in.PropertyID.ValidateV7() != nil || in.TargetID.ValidateV7() != nil || in.ClientOperationID.ValidateV7() != nil || in.OriginalActionAt.IsZero() || in.OriginalActionAt.After(now.Add(time.Minute)) || in.OriginalActionAt.Before(now.Add(-90*24*time.Hour)) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("One supported typed command, original timestamp, stable UUIDv7 client operation, target, expected versions and property required."))
		return
	}
	if in.Action == "FOLIO_POST" {
		cap = "folios:write"
	}
	h.execute(w, r, "pms:offline:"+in.ClientOperationID.String(), in, cap, "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, cap, "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		digest := security.SHA256Hex(marshalCommercial(in))
		var raw []byte
		var oldDigest string
		e = tx.QueryRow(ctx, `SELECT request_digest,result FROM offline_operation_receipts WHERE organisation_id=$1 AND account_id=$2 AND client_operation_id=$3`, t.Organisation, t.Account, in.ClientOperationID).Scan(&oldDigest, &raw)
		if e == nil {
			if oldDigest != digest {
				return 0, nil, apperrors.Conflict("OFFLINE_CLIENT_OPERATION_REUSED", "Client operation identity is bound to its immutable original command.")
			}
			var old PMSOfflineResult
			if e = json.Unmarshal(raw, &old); e != nil {
				return 0, nil, e
			}
			status := 201
			if old.State == "RECONCILIATION_REQUIRED" {
				status = 202
			}
			return status, old, nil
		}
		if e != pgx.ErrNoRows {
			return 0, nil, e
		}
		save, e := tx.Begin(ctx)
		if e != nil {
			return 0, nil, e
		}
		var result any
		var commandErr error
		switch in.Action {
		case "FOLIO_POST":
			pre, e := hospitality.GetFolio(ctx, save, t.Organisation, in.TargetID)
			if e != nil {
				commandErr = e
			} else if pre.PropertyID != in.PropertyID {
				commandErr = apperrors.PermissionDenied("Folio does not belong to the authorized property.")
			} else {
				_, result, commandErr = h.executePMSFolioPosting(ctx, save, pre, *in.FolioPosting)
			}
		case "ASSIGN_ROOM":
			pre, e := reservations.Get(ctx, save, t.Organisation, in.TargetID)
			if e != nil {
				commandErr = e
			} else if pre.PropertyID != in.PropertyID {
				commandErr = apperrors.PermissionDenied("Reservation does not belong to the authorized property.")
			} else {
				_, result, commandErr = h.executePMSAssignment(ctx, save, pre, *in.Assignment)
			}
		case "CHECK_IN":
			pre, e := hospitality.GetAssignment(ctx, save, t.Organisation, in.CheckIn.AssignmentID)
			if e != nil {
				commandErr = e
			} else if pre.PropertyID != in.PropertyID || pre.ReservationID != in.TargetID {
				commandErr = apperrors.PermissionDenied("Assignment does not belong to the exact route reservation and property.")
			} else {
				_, result, commandErr = h.executePMSCheckIn(ctx, save, pre, *in.CheckIn)
			}
		}
		receipt := PMSOfflineResult{ID: ids.New(), State: "APPLIED", Result: marshalCommercial(result)}
		status := 201
		if commandErr != nil {
			if e = save.Rollback(ctx); e != nil {
				return 0, nil, e
			}
			var app *apperrors.Error
			if !errors.As(commandErr, &app) || app.Status >= 500 {
				return 0, nil, commandErr
			}
			receipt.State = "RECONCILIATION_REQUIRED"
			receipt.FailureCode = app.Code
			receipt.Result = json.RawMessage(`null`)
			task := ids.New()
			receipt.TaskID = &task
			status = 202
		} else if e = save.Commit(ctx); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO offline_operation_receipts(id,organisation_id,property_id,account_id,session_id,client_operation_id,action,target_id,original_action_at,request_digest,state,result,failure_code) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, receipt.ID, t.Organisation, in.PropertyID, t.Account, principal.SessionID, in.ClientOperationID, in.Action, in.TargetID, in.OriginalActionAt, digest, receipt.State, marshalCommercial(receipt), receipt.FailureCode); e != nil {
			return 0, nil, e
		}
		if receipt.TaskID != nil {
			if _, e = tx.Exec(ctx, `INSERT INTO offline_reconciliation_tasks(id,organisation_id,property_id,receipt_id,classification) VALUES($1,$2,$3,$4,$5)`, *receipt.TaskID, t.Organisation, in.PropertyID, receipt.ID, receipt.FailureCode); e != nil {
				return 0, nil, e
			}
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, receipt.ID, "offline_receipt", "OFFLINE_OPERATION_RECONCILED", "hospitality.offline.reconciled.v1", 1, map[string]any{"action": in.Action, "state": receipt.State, "target_id": in.TargetID, "failure_code": receipt.FailureCode, "client_operation_id": in.ClientOperationID, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return status, receipt, nil
	})
}
func (h *Handler) pmsOfflineResolve(w http.ResponseWriter, r *http.Request) {
	var in PMSOfflineResolution
	if !decode(w, r, &in) {
		return
	}
	if in.Disposition != "RETRY_ONLINE" && in.Disposition != "CANCELLED" || !crm.Text(in.EvidenceReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Explicit online retry/cancellation disposition, evidence and reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var property ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM offline_reconciliation_tasks WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&property); e != nil {
		reply(w, r, 0, nil, apperrors.NotFound("RECONCILIATION_TASK_NOT_FOUND", "Reconciliation task not found."))
		return
	}
	h.execute(w, r, "pms:offline:resolve:"+id.String(), in, "pms:approve", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:approve", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		resolution := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO offline_reconciliation_resolutions(id,organisation_id,property_id,task_id,disposition,evidence_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, resolution, t.Organisation, property, id, in.Disposition, in.EvidenceReference, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, property, resolution, "offline_resolution", "OFFLINE_TASK_RESOLVED", "hospitality.offline.reconciled.v1", 1, map[string]any{"task_id": id, "disposition": in.Disposition, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": resolution, "disposition": in.Disposition, "original_operation_applied": false}, nil
	})
}
func (h *Handler) pmsOfflineReceipts(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "offline_operation_receipts", "pms:read")
}
func (h *Handler) pmsOfflineTasks(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "offline_reconciliation_tasks", "pms:read")
}
