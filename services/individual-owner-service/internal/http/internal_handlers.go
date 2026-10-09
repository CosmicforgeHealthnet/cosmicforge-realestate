package http

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
)

func (h *Handler) adminGetAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "account_id", "ACCOUNT_NOT_FOUND", "Account not found.")
	var v usecases.AdminAccountView
	if err == nil {
		v, err = h.svc.AdminGetAccount(r.Context(), serviceSubject(r), id)
	}
	write(w, r, http.StatusOK, v, err)
}

type adminTransitionRequest struct {
	ReasonCode          string `json:"reason_code"`
	Note                string `json:"note"`
	ResolutionReference string `json:"resolution_reference"`
	TargetStatus        string `json:"target_status"`
}

// optionalIfMatch returns the expected version or 0 when absent.
func optionalIfMatch(r *http.Request) (int64, error) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64)
	if err != nil || n <= 0 {
		return 0, apperrors.InvalidRequest("If-Match must be the quoted account version.")
	}
	return n, nil
}

// adminTransition runs an idempotent admin lifecycle command. The capability
// policy (admin-service + delegated admin actor) is enforced before the
// idempotency key is claimed so unauthorized callers cannot occupy keys.
func (h *Handler) adminTransition(action authz.Action, scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subj := serviceSubject(r)
		id, err := pathID(r, "account_id", "ACCOUNT_NOT_FOUND", "Account not found.")
		var ver int64
		if err == nil {
			_, err = h.svc.Authz.Require(r.Context(), subj, action, authz.Resource{AccountID: id})
		}
		if err == nil {
			ver, err = optionalIfMatch(r)
		}
		var body []byte
		if err == nil {
			body, err = httpx.ReadBody(w, r, maxCommandBody)
		}
		var b adminTransitionRequest
		if err == nil {
			err = httpx.DecodeJSONBytes(body, &b)
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		h.idem.ServeCommand(w, r, scope, body, nil, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			v, err := h.svc.AdminTransitionTx(ctx, tx, subj, action, id, usecases.AdminCommand{ReasonCode: b.ReasonCode, Note: b.Note,
				ResolutionReference: b.ResolutionReference, TargetStatus: b.TargetStatus, ExpectedVersion: ver})
			if err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Status: http.StatusOK, Body: httpx.DataEnvelope{Data: v}, ResourceType: "owner_account", ResourceID: id.String()}, nil
		})
	}
}

type revokeSessionsRequest struct {
	ReasonCode string `json:"reason_code"`
}

func (h *Handler) adminRevokeSessions(w http.ResponseWriter, r *http.Request) {
	subj := serviceSubject(r)
	id, err := pathID(r, "account_id", "ACCOUNT_NOT_FOUND", "Account not found.")
	if err == nil {
		_, err = h.svc.Authz.Require(r.Context(), subj, authz.ActAdminRevokeSessions, authz.Resource{AccountID: id})
	}
	var body []byte
	if err == nil {
		body, err = httpx.ReadBody(w, r, maxCommandBody)
	}
	var b revokeSessionsRequest
	if err == nil {
		err = httpx.DecodeJSONBytes(body, &b)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.idem.ServeCommand(w, r, "owner.admin.revoke_sessions", body, nil, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		n, err := h.svc.AdminRevokeSessionsTx(ctx, tx, subj, id, b.ReasonCode)
		if err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{Status: http.StatusOK, Body: httpx.DataEnvelope{Data: map[string]int64{"revoked_sessions": n}},
			ResourceType: "owner_account", ResourceID: id.String()}, nil
	})
}
