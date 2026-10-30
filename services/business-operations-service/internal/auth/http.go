package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type principalKey struct{}

func PrincipalFrom(ctx context.Context) (sharedauth.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(sharedauth.Principal)
	return p, ok
}

type Handler struct {
	Service *Service
	Router  chi.Router
}

func NewHandler(s *Service, gateway *serviceauth.Verifier) (*Handler, error) {
	if s == nil || gateway == nil {
		return nil, errors.New("business HTTP identity dependencies required")
	}
	h := &Handler{Service: s}
	r := chi.NewRouter()
	h.Router = r
	r.Use(serviceauth.Authenticate(gateway, serviceauth.HeaderServiceAuthorization), serviceauth.RequireCaller(platform.APIGateway), security.Headers(security.HeadersConfig{}))
	r.Post("/auth/register", h.register)
	r.Post("/auth/login", h.login)
	r.Post("/auth/refresh", h.refresh)
	r.Post("/auth/mfa/verify", h.verifyMFA)
	r.Group(func(r chi.Router) {
		r.Use(h.Authenticate)
		r.Post("/auth/logout", h.logout)
		r.Post("/auth/logout-all", h.logoutAll)
		r.Post("/auth/mfa/enrol", h.enrol)
		r.Post("/auth/mfa/confirm", h.confirm)
		r.Delete("/auth/mfa", h.removeMFA)
		r.Get("/auth/mfa", h.mfaStatus)
		r.Get("/sessions", h.sessions)
		r.Delete("/sessions/{session_id}", h.revokeSession)
		r.Get("/devices", h.devices)
		r.Get("/memberships", h.memberships)
		r.Post("/auth/organisation", h.selectOrganisation)
	})
	r.NotFound(httpx.NotFoundHandler().ServeHTTP)
	return h, nil
}
func (h *Handler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			httpx.WriteError(w, r, apperrors.AuthenticationRequired())
			return
		}
		p, _, err := h.Service.Authenticate(r.Context(), strings.TrimPrefix(raw, "Bearer "))
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		gateway, ok := serviceauth.PrincipalFrom(r.Context())
		if !ok || gateway.Service != platform.APIGateway || gateway.Delegation.ActorID != p.ActorID || gateway.Delegation.ActorDomain != platform.DomainBusinessOperations || !gateway.Delegation.OrganizationID.IsZero() && gateway.Delegation.OrganizationID != p.OrganizationID {
			httpx.WriteError(w, r, apperrors.PermissionDenied("Gateway identity does not match the business account."))
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		ctx = requestcontext.WithActor(ctx, requestcontext.Actor{Domain: string(platform.DomainBusinessOperations), ID: p.ActorID})
		ctx = requestcontext.WithOrganization(ctx, p.OrganizationID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func decode(w http.ResponseWriter, r *http.Request, in any) bool {
	if err := httpx.DecodeJSON(w, r, in, 32<<10); err != nil {
		httpx.WriteError(w, r, err)
		return false
	}
	return true
}
func reply(w http.ResponseWriter, r *http.Request, status int, data any, err error) {
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, status, data)
}
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in Registration
	if !decode(w, r, &in) {
		return
	}
	if err := h.Service.limit(r.Context(), "registration-client:"+r.Header.Get("X-Forwarded-For"), 5); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.Service.Register(r.Context(), in, r.Header.Get("Idempotency-Key"))
	reply(w, r, 201, out, err)
}
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in Login
	if !decode(w, r, &in) {
		return
	}
	out, err := h.Service.Login(r.Context(), in, r.Header.Get("X-Forwarded-For"))
	reply(w, r, 200, out, err)
}
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.Service.limit(r.Context(), "refresh-client:"+r.Header.Get("X-Forwarded-For"), 100); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.Service.Refresh(r.Context(), in.RefreshToken)
	reply(w, r, 200, out, err)
}
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	reply(w, r, 200, map[string]bool{"revoked": true}, h.Service.Revoke(r.Context(), p.ActorID, p.SessionID, false))
}
func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	reply(w, r, 200, map[string]bool{"revoked": true}, h.Service.Revoke(r.Context(), p.ActorID, p.SessionID, true))
}
func (h *Handler) enrol(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	out, err := h.Service.EnrolTOTP(r.Context(), p.ActorID, p.SessionID, in.Password)
	reply(w, r, 201, out, err)
}
func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	out, err := h.Service.ConfirmTOTP(r.Context(), p.ActorID, p.SessionID, in.Code)
	reply(w, r, 200, map[string]any{"recovery_codes": out}, err)
}
func (h *Handler) verifyMFA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MFAToken string `json:"mfa_token,omitempty"`
		Code     string `json:"code"`
		Recovery bool   `json:"recovery,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.MFAToken != "" {
		out, err := h.Service.CompleteMFALogin(r.Context(), in.MFAToken, in.Code, in.Recovery)
		reply(w, r, 200, out, err)
		return
	}
	h.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		reply(w, r, 200, map[string]bool{"verified": true}, h.Service.StepUp(r.Context(), p.ActorID, p.SessionID, in.Code, in.Recovery))
	})).ServeHTTP(w, r)
}
func (h *Handler) removeMFA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	reply(w, r, 200, map[string]bool{"removed": true}, h.Service.RemoveMFA(r.Context(), p.ActorID, p.SessionID, in.Password))
}
func (h *Handler) mfaStatus(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	rows, err := h.Service.Pool.Query(r.Context(), `SELECT id,kind,status,created_at,confirmed_at FROM business_mfa_methods WHERE account_id=$1 AND status<>'REMOVED' ORDER BY created_at,id`, p.ActorID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id ids.ID
		var kind, status string
		var created, confirmed any
		if err := rows.Scan(&id, &kind, &status, &created, &confirmed); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{"method_id": id, "kind": kind, "status": status, "created_at": created, "confirmed_at": confirmed})
	}
	reply(w, r, 200, map[string]any{"items": out}, rows.Err())
}
func page(r *http.Request) (int32, int32, error) {
	limit, offset := 50, 0
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil {
			return 0, 0, apperrors.InvalidRequest("Invalid pagination.")
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil {
			return 0, 0, apperrors.InvalidRequest("Invalid pagination.")
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return 0, 0, apperrors.InvalidRequest("Invalid pagination.")
	}
	return int32(limit), int32(offset), nil
}
func (h *Handler) sessions(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	limit, offset, err := page(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.Service.q.ListSessions(r.Context(), h.Service.Pool, db.ListSessionsParams{AccountID: p.ActorID, Limit: limit, Offset: offset})
	out := []map[string]any{}
	for _, s := range items {
		out = append(out, map[string]any{"session_id": s.ID, "device_id": s.DeviceID, "organisation_id": s.OrganisationID, "created_at": s.CreatedAt, "expires_at": s.ExpiresAt, "last_refreshed_at": s.LastRefreshedAt, "revoked_at": s.RevokedAt, "mfa_at": s.MfaAt})
	}
	reply(w, r, 200, map[string]any{"items": out, "limit": limit, "offset": offset}, err)
}
func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "session_id"))
	if err != nil {
		httpx.WriteError(w, r, apperrors.InvalidRequest("Invalid session identifier."))
		return
	}
	p, _ := PrincipalFrom(r.Context())
	reply(w, r, 200, map[string]bool{"revoked": true}, h.Service.Revoke(r.Context(), p.ActorID, id, false))
}
func (h *Handler) devices(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	limit, offset, err := page(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rows, err := h.Service.Pool.Query(r.Context(), `SELECT id,label,platform,risk_flags,created_at,last_seen_at FROM business_devices WHERE account_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, p.ActorID, limit, offset)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id ids.ID
		var label, platformName string
		var flags, created, last any
		if err := rows.Scan(&id, &label, &platformName, &flags, &created, &last); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{"device_id": id, "label": label, "platform": platformName, "risk_flags": flags, "created_at": created, "last_seen_at": last})
	}
	reply(w, r, 200, map[string]any{"items": out, "limit": limit, "offset": offset}, rows.Err())
}
func (h *Handler) memberships(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	rows, err := h.Service.Pool.Query(r.Context(), `SELECT m.id,m.organisation_id,o.name,m.status,m.role_id,m.effective_at,m.expires_at FROM staff_memberships m JOIN organisations o ON o.id=m.organisation_id WHERE m.account_id=$1 ORDER BY m.organisation_id LIMIT 100`, p.ActorID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, org, role ids.ID
		var name, status string
		var effective, expiry any
		if err := rows.Scan(&id, &org, &name, &status, &role, &effective, &expiry); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{"membership_id": id, "organisation_id": org, "name": name, "status": status, "role_id": role, "effective_at": effective, "expires_at": expiry})
	}
	reply(w, r, 200, map[string]any{"items": out}, rows.Err())
}
func (h *Handler) selectOrganisation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrganisationID ids.ID `json:"organisation_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	if in.OrganisationID.ValidateV7() != nil {
		httpx.WriteError(w, r, apperrors.InvalidRequest("Invalid organisation identifier."))
		return
	}
	var status string
	err := h.Service.Pool.QueryRow(r.Context(), `SELECT m.status FROM staff_memberships m JOIN organisations o ON o.id=m.organisation_id WHERE m.account_id=$1 AND m.organisation_id=$2 AND m.status='ACTIVE' AND m.effective_at<=now() AND (m.expires_at IS NULL OR m.expires_at>now()) AND o.state<>'CLOSED'`, p.ActorID, in.OrganisationID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, apperrors.PermissionDenied("Organisation access denied."))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	tag, err := h.Service.Pool.Exec(r.Context(), `UPDATE business_sessions SET organisation_id=$3 WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL AND expires_at>now()`, p.SessionID, p.ActorID, in.OrganisationID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() != 1 {
		httpx.WriteError(w, r, apperrors.InvalidToken())
		return
	}
	token, claims, err := h.Service.Issuer.Issue(sharedauth.IssueRequest{Subject: p.ActorID, SessionID: p.SessionID, OrganizationID: in.OrganisationID})
	reply(w, r, 200, map[string]any{"access_token": token, "expires_at": claims.ExpiresAt.Time()}, err)
}
