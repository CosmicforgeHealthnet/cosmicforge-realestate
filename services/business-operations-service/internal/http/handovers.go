package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type handoverCommand struct {
	Version   int64    `json:"version"`
	Successor ids.ID   `json:"successor_membership_id"`
	Reason    string   `json:"reason"`
	Evidence  []ids.ID `json:"evidence"`
}

func (h *Handler) registerHandovers(r chi.Router) {
	r.Get("/staff/{staff_id}/handover", h.getHandover)
	r.Post("/staff/{staff_id}/handover", h.startHandover)
	r.Post("/staff/{staff_id}/handover/{id}/acknowledge", func(w http.ResponseWriter, r *http.Request) { h.finishHandover(w, r, false) })
	r.Post("/staff/{staff_id}/handover/{id}/complete", func(w http.ResponseWriter, r *http.Request) { h.finishHandover(w, r, true) })
}
func (h *Handler) startHandover(w http.ResponseWriter, r *http.Request) {
	member, err := ids.ParseV7(chi.URLParam(r, "staff_id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid staff ID."))
		return
	}
	var in handoverCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.Successor.ValidateV7() != nil || in.Successor == member || !validStaffReason(in.Reason) || len(in.Evidence) > 30 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid handover details."))
		return
	}
	for _, id := range in.Evidence {
		if id.ValidateV7() != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid evidence reference."))
			return
		}
	}
	_, target, err := h.staffTarget(r, member, "staff:write", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	h.execute(w, r, "staff:handover:"+member.String(), in, "staff:write", target.Scope.Kind, target.Scope.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "staff:write", target.Scope.Kind, target.Scope.ID, true)
		if err != nil {
			return 0, nil, err
		}
		var account ids.ID
		var status string
		var version int64
		if err = tx.QueryRow(ctx, `SELECT account_id,status,version FROM staff_memberships WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, member).Scan(&account, &status, &version); err != nil {
			return 0, nil, err
		}
		if account == t.Representative || account == t.Account {
			return 0, nil, apperrors.PermissionDenied("Independent staff administration and representative continuity required.")
		}
		if version != in.Version || (status != "ACTIVE" && status != "SUSPENDED") {
			return 0, nil, apperrors.Conflict("HANDOVER_STATE_CONFLICT", "Membership is stale or cannot begin handover.")
		}
		successor, err := directMember(ctx, tx, t.Organisation, in.Successor)
		if err != nil {
			return 0, nil, err
		}
		for i := range successor.Grants {
			successor.Grants[i].RequireMFA = false
		}
		if workforce.Authorize(successor, workforce.DecisionInput{AccountID: successor.AccountID, Target: target, Capability: "staff:read", Now: h.Auth.Service.Now().UTC(), StepUpWindow: 5 * time.Minute}) != nil {
			return 0, nil, apperrors.PermissionDenied("The successor lacks current scope authority. Handover never grants permissions.")
		}
		evidence := in.Evidence
		if evidence == nil {
			evidence = []ids.ID{}
		}
		id := ids.New()
		if _, err = tx.Exec(ctx, `INSERT INTO staff_handovers(id,organisation_id,membership_id,successor_membership_id,initiated_by,reason,evidence_ids) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, member, in.Successor, t.Account, in.Reason, evidence); err != nil {
			return 0, nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE staff_memberships SET status='HANDOVER_PENDING',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, member); err != nil {
			return 0, nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO staff_membership_history(id,organisation_id,membership_id,actor_id,from_status,to_status,reason) VALUES($1,$2,$3,$4,$5,'HANDOVER_PENDING',$6)`, ids.New(), t.Organisation, member, t.Account, status, in.Reason); err != nil {
			return 0, nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE business_sessions SET revoked_at=coalesce(revoked_at,now()),revoke_reason='HANDOVER_PENDING' WHERE organisation_id=$1 AND account_id=$2`, t.Organisation, account); err != nil {
			return 0, nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE organisations SET authorization_version=authorization_version+1 WHERE id=$1`, t.Organisation); err != nil {
			return 0, nil, err
		}
		if err = h.accessEvent(ctx, tx, t, "STAFF_HANDOVER_STARTED", "business.staff.handover_changed.v1", "staff_handover", id, in); err != nil {
			return 0, nil, err
		}
		if err = h.queueNotice(ctx, tx, successor.AccountID, "Staff handover", "A scoped handover requires your acknowledgement. No additional permissions were granted.", false); err != nil {
			return 0, nil, err
		}
		if err = h.staffEvent(ctx, tx, t.Organisation, member, account, "HANDOVER_PENDING"); err != nil {
			return 0, nil, err
		}
		return 201, map[string]any{"handover_id": id, "staff_id": member, "status": "PENDING", "version": 1, "staff_version": version + 1}, nil
	})
}
func (h *Handler) getHandover(w http.ResponseWriter, r *http.Request) {
	member, err := ids.ParseV7(chi.URLParam(r, "staff_id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid staff ID."))
		return
	}
	t, _, err := h.staffTarget(r, member, "staff:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	out, err := oneJSON(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM (SELECT id AS handover_id,membership_id AS staff_id,successor_membership_id,status,reason,evidence_ids,version,initiated_by,acknowledged_by,acknowledged_at,completed_by,completed_at,completion_reason,created_at FROM staff_handovers WHERE organisation_id=$1 AND membership_id=$2 ORDER BY created_at DESC,id DESC LIMIT 1)x`, t.Organisation, member)
	reply(w, r, 200, out, err)
}
func (h *Handler) finishHandover(w http.ResponseWriter, r *http.Request, complete bool) {
	member, err := ids.ParseV7(chi.URLParam(r, "staff_id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid staff ID."))
		return
	}
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid handover ID."))
		return
	}
	var in struct {
		Version int64  `json:"version"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A version and written reason are required."))
		return
	}
	capability := "staff:read"
	if complete {
		capability = "staff:write"
	}
	_, target, err := h.staffTarget(r, member, capability, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	h.execute(w, r, "staff:handover:finish:"+id.String()+":"+capability, in, capability, target.Scope.Kind, target.Scope.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, capability, target.Scope.Kind, target.Scope.ID, true)
		if err != nil {
			return 0, nil, err
		}
		var status string
		var successor, account ids.ID
		var version int64
		if err = tx.QueryRow(ctx, `SELECT h.status,h.successor_membership_id,h.version,m.account_id FROM staff_handovers h JOIN staff_memberships m ON m.organisation_id=h.organisation_id AND m.id=h.membership_id WHERE h.organisation_id=$1 AND h.id=$2 AND h.membership_id=$3 FOR UPDATE OF h`, t.Organisation, id, member).Scan(&status, &successor, &version, &account); err != nil {
			return 0, nil, apperrors.NotFound("HANDOVER_NOT_FOUND", "Handover not found.")
		}
		if version != in.Version || account == t.Account {
			return 0, nil, apperrors.Conflict("HANDOVER_STATE_CONFLICT", "Independent current handover required.")
		}
		next, action := "ACKNOWLEDGED", "STAFF_HANDOVER_ACKNOWLEDGED"
		if complete {
			if status != "ACKNOWLEDGED" {
				return 0, nil, apperrors.Conflict("HANDOVER_ACKNOWLEDGEMENT_REQUIRED", "The successor must acknowledge first.")
			}
			next, action = "COMPLETED", "STAFF_HANDOVER_COMPLETED"
			_, err = tx.Exec(ctx, `UPDATE staff_handovers SET status='COMPLETED',completed_by=$3,completed_at=now(),completion_reason=$4,version=version+1 WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, t.Account, in.Reason)
		} else {
			if status != "PENDING" || successor != t.Member {
				return 0, nil, apperrors.PermissionDenied("The designated successor must acknowledge a pending handover.")
			}
			_, err = tx.Exec(ctx, `UPDATE staff_handovers SET status='ACKNOWLEDGED',acknowledged_by=$3,acknowledged_at=now(),acknowledgement_reason=$4,version=version+1 WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, t.Account, in.Reason)
		}
		if err != nil {
			return 0, nil, err
		}
		if err = h.accessEvent(ctx, tx, t, action, "business.staff.handover_changed.v1", "staff_handover", id, map[string]any{"status": next, "reason": in.Reason}); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"handover_id": id, "status": next, "version": version + 1}, nil
	})
}
