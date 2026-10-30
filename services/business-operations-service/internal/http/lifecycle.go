package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/organisation"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) registerLifecycle(r chi.Router) {
	r.Post("/organisation/lifecycle", h.changeLifecycle)
	r.Get("/organisation/closure-readiness", h.closureStatus)
	r.Post("/organisation/representative", h.changeRepresentative)
	r.Get("/audit-events", h.listAudit)
}
func (h *Handler) closure(ctx context.Context, org ids.ID) (bool, []string, error) {
	var activated *time.Time
	if err := h.Auth.Service.Pool.QueryRow(ctx, `SELECT activated_at FROM organisations WHERE id=$1`, org).Scan(&activated); err != nil {
		return false, nil, err
	}
	if activated != nil {
		if h.ClosureReadiness == nil {
			return false, []string{"DOWNSTREAM_CLOSURE_READINESS_CONTRACT_UNAVAILABLE"}, nil
		}
		return h.ClosureReadiness.Ready(ctx, org)
	}
	var blockers int
	if err := h.Auth.Service.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM approval_requests WHERE organisation_id=$1 AND state IN ('PENDING','DRAFT'))+(SELECT count(*) FROM staff_invitations WHERE organisation_id=$1 AND status='PENDING')`, org).Scan(&blockers); err != nil {
		return false, nil, err
	}
	if blockers > 0 {
		return false, []string{"PENDING_APPROVALS_OR_INVITATIONS"}, nil
	}
	return true, []string{}, nil
}
func (h *Handler) closureStatus(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:read", "COMPANY", ids.Nil, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	ready, blockers, err := h.closure(r.Context(), t.Organisation)
	reply(w, r, 200, map[string]any{"ready": ready, "blockers": blockers}, err)
}
func (h *Handler) changeLifecycle(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State      organisation.State `json:"state"`
		Version    int64              `json:"version"`
		Reason     string             `json:"reason"`
		ApprovalID ids.ID             `json:"approval_id,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A current version and reason are required."))
		return
	}
	switch in.State {
	case organisation.Restricted, organisation.Suspended, organisation.ClosurePending, organisation.Closed, organisation.Active:
	default:
		reply(w, r, 0, nil, apperrors.InvalidRequest("Unsupported lifecycle command."))
		return
	}
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:read", "COMPANY", ids.Nil, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	// Resuming operations requires a fresh authenticated provider read. A cached
	// VERIFIED link must not override a newer restriction during an outage.
	if in.State == organisation.Active {
		if h.Verification == nil {
			reply(w, r, 0, nil, apperrors.Unavailable("Authoritative verification status is unavailable."))
			return
		}
		var caseID ids.ID
		if err := h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT case_id FROM verification_links WHERE organisation_id=$1 AND purpose='KYB'`, t.Organisation).Scan(&caseID); err != nil {
			reply(w, r, 0, nil, apperrors.PermissionDenied("Current KYB is required."))
			return
		}
		current, err := h.Verification.Get(r.Context(), caseID)
		if err != nil {
			reply(w, r, 0, nil, apperrors.Unavailable("Authoritative verification status is unavailable."))
			return
		}
		if current.VerificationID != caseID {
			reply(w, r, 0, nil, apperrors.PermissionDenied("Verification identity mismatch."))
			return
		}
		if err := h.applyVerification(r.Context(), t, current); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		fresh := organisation.Verification{CaseID: current.VerificationID, Status: current.Status, ExpiresAt: current.ExpiresAt}
		if !fresh.Current(h.Auth.Service.Now().UTC()) {
			reply(w, r, 0, nil, apperrors.PermissionDenied("Current authoritative KYB is required."))
			return
		}
	}
	ready := false
	if in.State == organisation.Closed || in.State == organisation.ClosurePending {
		var blockers []string
		ready, blockers, err = h.closure(r.Context(), t.Organisation)
		if err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		if !ready {
			reply(w, r, 0, nil, apperrors.Conflict("CLOSURE_BLOCKED", "Closure readiness has outstanding dependencies: "+strings.Join(blockers, ", ")))
			return
		}
	}
	// Restricted businesses retain containment/closure administration, but
	// cannot use the ordinary write capability to resume commercial operations.
	h.execute(w, r, "organisation:lifecycle", in, "organisation:read", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "organisation:read", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		if t.Account != t.Representative {
			return 0, nil, apperrors.PermissionDenied("Primary representative required.")
		}
		var state organisation.State
		var version int64
		if err = tx.QueryRow(ctx, `SELECT state,version FROM organisations WHERE id=$1 FOR UPDATE`, t.Organisation).Scan(&state, &version); err != nil {
			return 0, nil, err
		}
		if version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Organisation version changed.")
		}
		if in.State == organisation.Closed {
			var blockers int
			if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM approval_requests WHERE organisation_id=$1 AND state IN ('PENDING','DRAFT'))+(SELECT count(*) FROM staff_invitations WHERE organisation_id=$1 AND status='PENDING')`, t.Organisation).Scan(&blockers); err != nil {
				return 0, nil, err
			}
			if blockers > 0 {
				return 0, nil, apperrors.Conflict("CLOSURE_BLOCKED", "Pending approvals or invitations prevent closure.")
			}
		}
		v := organisation.Verification{}
		if in.State == organisation.Active {
			if err = tx.QueryRow(ctx, `SELECT case_id,provider_status,expires_at FROM verification_links WHERE organisation_id=$1 AND purpose='KYB'`, t.Organisation).Scan(&v.CaseID, &v.Status, &v.ExpiresAt); err != nil {
				return 0, nil, apperrors.PermissionDenied("Current KYB is required.")
			}
			if err = h.requirePrivilegeChange(ctx, tx, t, "ORGANISATION_STATE_CHANGE", workforce.Scope{Organisation: t.Organisation, Kind: "COMPANY", ID: t.Organisation}, in.ApprovalID, in); err != nil {
				return 0, nil, err
			}
		}
		if err = organisation.Transition(state, in.State, v, h.Auth.Service.Now().UTC(), ready); err != nil {
			return 0, nil, apperrors.Conflict("LIFECYCLE_CONFLICT", "Invalid organisation state transition.")
		}
		source := ""
		if in.State == organisation.Restricted || in.State == organisation.Suspended {
			source = "ADMIN"
		}
		if _, err = tx.Exec(ctx, `UPDATE organisations SET state=$2,restriction_source=$3,version=version+1,authorization_version=authorization_version+1,updated_at=now() WHERE id=$1`, t.Organisation, string(in.State), source); err != nil {
			return 0, nil, err
		}
		if in.State == organisation.Closed {
			if _, err = tx.Exec(ctx, `UPDATE business_sessions SET revoked_at=coalesce(revoked_at,now()),revoke_reason='ORGANISATION_CLOSED' WHERE organisation_id=$1`, t.Organisation); err != nil {
				return 0, nil, err
			}
		}
		if err = h.accessEvent(ctx, tx, t, "ORGANISATION_STATE_CHANGED", "business.organisation.state_changed.v1", "organisation", t.Organisation, in); err != nil {
			return 0, nil, err
		}
		if err = h.queueNotice(ctx, tx, t.Representative, "Organisation lifecycle", "Your organisation lifecycle state changed to "+string(in.State)+".", true); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"state": in.State, "version": version + 1}, nil
	})
}
func (h *Handler) changeRepresentative(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Account      ids.ID `json:"account_id"`
		Verification ids.ID `json:"verification_id"`
		Version      int64  `json:"version"`
		Reason       string `json:"reason"`
		ApprovalID   ids.ID `json:"approval_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Account.ValidateV7() != nil || in.Verification.ValidateV7() != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A verified representative reference, version and reason are required."))
		return
	}
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:write", "COMPANY", ids.Nil, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if h.RepresentativeAuthority == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification Service does not yet expose legal representative authority assertions."))
		return
	}
	assertion, err := h.RepresentativeAuthority.Representative(r.Context(), t.Organisation, in.Account, in.Verification)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Representative authority is unavailable."))
		return
	}
	if !assertion.Verified || assertion.OrganisationID != t.Organisation || assertion.AccountID != in.Account || assertion.CaseID != in.Verification || !h.Auth.Service.Now().Before(assertion.ExpiresAt) {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Current representative authority required."))
		return
	}
	h.execute(w, r, "organisation:representative", in, "organisation:write", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "organisation:write", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		if t.Account != t.Representative || in.Account == t.Account {
			return 0, nil, apperrors.PermissionDenied("Representative handover denied.")
		}
		if err = h.requirePrivilegeChange(ctx, tx, t, "REPRESENTATIVE_CHANGE", target.Scope, in.ApprovalID, in); err != nil {
			return 0, nil, err
		}
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staff_memberships m JOIN business_accounts a ON a.id=m.account_id WHERE m.organisation_id=$1 AND m.account_id=$2 AND m.status='ACTIVE' AND a.status='ACTIVE' AND m.effective_at<=now() AND (m.expires_at IS NULL OR m.expires_at>now()))`, t.Organisation, in.Account).Scan(&active); err != nil {
			return 0, nil, err
		}
		if !active {
			return 0, nil, apperrors.PermissionDenied("The successor must have an active membership.")
		}
		tag, err := tx.Exec(ctx, `UPDATE organisations SET representative_account_id=$3,version=version+1,authorization_version=authorization_version+1,updated_at=now() WHERE id=$1 AND version=$2`, t.Organisation, in.Version, in.Account)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Organisation version changed.")
		}
		if err = h.accessEvent(ctx, tx, t, "REPRESENTATIVE_CHANGED", "business.organisation.representative_changed.v1", "organisation", t.Organisation, in); err != nil {
			return 0, nil, err
		}
		if err = h.queueNotice(ctx, tx, in.Account, "Representative handover", "You are now the verified primary representative. Your explicit permissions are unchanged.", true); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"representative_account_id": in.Account, "version": in.Version + 1}, nil
	})
}
func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "audit:read", "COMPANY", ids.Nil, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT to_jsonb(x) FROM (SELECT id,actor_id,action,target_type,target_id,correlation_id,change_context,occurred_at FROM audit_events WHERE organisation_id=$1 ORDER BY occurred_at DESC,id DESC LIMIT $2 OFFSET $3)x`, t.Organisation, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		items = append(items, raw)
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, rows.Err())
}
