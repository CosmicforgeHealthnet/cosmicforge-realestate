package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/organisation"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func Register(authHandler *auth.Handler, verifier verification.API, notify notification.Sender) *Handler {
	h := &Handler{Auth: authHandler, Verification: verifier, Notification: notify}
	authHandler.Router.Post("/staff/invitations/accept", h.acceptInvitation)
	authHandler.Router.Group(func(r chi.Router) {
		r.Use(authHandler.Authenticate)
		h.registerHierarchy(r)
		h.registerApprovals(r)
		h.registerRoles(r)
		h.registerStaff(r)
		h.registerHandovers(r)
		h.registerTemporary(r)
		h.registerCapacities(r)
		h.registerLifecycle(r)
		r.Get("/organisation", h.getOrganisation)
		r.Patch("/organisation", h.updateOrganisation)
		// Annex D uses profile/verification family shorthand; these explicit aliases
		// share the canonical organisation handlers and all security middleware.
		r.Get("/profile", h.getOrganisation)
		r.Patch("/profile", h.updateOrganisation)
		r.Get("/verification", h.getKYB)
		r.Get("/capabilities", h.effectiveCapabilities)
		r.Post("/organisation/verification", h.createKYB)
		r.Get("/organisation/verification", h.getKYB)
		r.Post("/organisation/verification/refresh", h.refreshKYB)
		r.Post("/organisation/verification/submit", h.submitKYB)
		r.Post("/organisation/verification/renew", h.renewKYB)
		r.Post("/organisation/verification/evidence", h.attachKYBEvidence)
		r.Get("/organisation/capacities", h.capacities)
	})
	return h
}

const profileQuery = `SELECT to_jsonb(x) FROM (SELECT id AS organisation_id,name,registered_name,registration_number,category,jurisdiction,currency,timezone,state,version,representative_account_id,created_at,updated_at FROM organisations WHERE id=$1) x`

func (h *Handler) getOrganisation(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:read", "COMPANY", ids.Nil, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	out, err := oneJSON(r.Context(), h.Auth.Service.Pool, profileQuery, t.Organisation)
	reply(w, r, 200, out, err)
}

type ProfilePatch struct {
	Version            int64   `json:"version"`
	Name               *string `json:"name,omitempty"`
	RegisteredName     *string `json:"registered_name,omitempty"`
	RegistrationNumber *string `json:"registration_number,omitempty"`
	Category           *string `json:"category,omitempty"`
	Jurisdiction       *string `json:"jurisdiction,omitempty"`
	Currency           *string `json:"currency,omitempty"`
	Timezone           *string `json:"timezone,omitempty"`
}

func (h *Handler) updateOrganisation(w http.ResponseWriter, r *http.Request) {
	var in ProfilePatch
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A current version is required."))
		return
	}
	for _, v := range []*string{in.Name, in.RegisteredName, in.RegistrationNumber, in.Category, in.Jurisdiction, in.Currency, in.Timezone} {
		if v != nil && (len(*v) > 200 || strings.ContainsRune(*v, 0)) {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid profile field."))
			return
		}
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" || in.Jurisdiction != nil && (len(*in.Jurisdiction) != 2 || strings.ToUpper(*in.Jurisdiction) != *in.Jurisdiction) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid name or jurisdiction."))
		return
	}
	if in.Currency != nil {
		if _, err := money.ParseCurrency(*in.Currency); err != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid currency."))
			return
		}
	}
	if in.Timezone != nil {
		if _, err := time.LoadLocation(*in.Timezone); err != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid timezone."))
			return
		}
	}
	var result json.RawMessage
	err := database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		t, _, err := h.authorize(ctx, tx, "organisation:write", "COMPANY", ids.Nil, false)
		if err != nil {
			return err
		}
		if t.State != "DRAFT" && (in.RegisteredName != nil || in.RegistrationNumber != nil || in.Jurisdiction != nil) {
			return apperrors.Conflict("VERIFIED_IDENTITY_CHANGE_REQUIRES_REVIEW", "Registered identity changes require a new verified review.")
		}
		tag, err := tx.Exec(ctx, `UPDATE organisations SET name=coalesce($3,name),registered_name=coalesce($4,registered_name),registration_number=coalesce($5,registration_number),category=coalesce($6,category),jurisdiction=coalesce($7,jurisdiction),currency=coalesce($8,currency),timezone=coalesce($9,timezone),version=version+1,updated_at=now() WHERE id=$1 AND version=$2`, t.Organisation, in.Version, in.Name, in.RegisteredName, in.RegistrationNumber, in.Category, in.Jurisdiction, in.Currency, in.Timezone)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apperrors.Conflict("VERSION_CONFLICT", "Organisation version has changed.")
		}
		if err := audit(ctx, tx, t, "ORGANISATION_PROFILE_UPDATED", "organisation", t.Organisation, in); err != nil {
			return err
		}
		result, err = oneJSON(ctx, tx, profileQuery, t.Organisation)
		return err
	})
	reply(w, r, 200, result, err)
}

func (h *Handler) createKYB(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:verify", "COMPANY", ids.Nil, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if h.Verification == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification Service is unavailable."))
		return
	}
	var complete bool
	err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT registered_name<>'' AND registration_number<>'' AND jurisdiction<>'' FROM organisations WHERE id=$1`, t.Organisation).Scan(&complete)
	if err != nil || !complete {
		if err == nil {
			err = apperrors.InvalidRequest("Complete the registered business identity before KYB.")
		}
		reply(w, r, 0, nil, err)
		return
	}
	callerReference := "business-kyb:" + t.Organisation.String()
	c, err := h.Verification.Create(r.Context(), verification.TypeBusiness, verification.Subject{Type: verification.SubjectBusiness, Domain: string(platform.DomainBusinessOperations), ID: t.Organisation}, callerReference, callerReference)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification Service could not create the case."))
		return
	}
	c, err = h.Verification.Get(r.Context(), c.VerificationID)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Authoritative verification status is unavailable."))
		return
	}
	err = h.applyVerification(r.Context(), t, c)
	reply(w, r, 202, c, err)
}
func (h *Handler) getKYB(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:read", "COMPANY", ids.Nil, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	out, err := oneJSON(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM (SELECT id AS verification_link_id,case_id,provider_status,expires_at,provider_updated_at,updated_at FROM verification_links WHERE organisation_id=$1 AND purpose='KYB') x`, t.Organisation)
	reply(w, r, 200, out, err)
}

func (h *Handler) submitKYB(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:verify", "COMPANY", ids.Nil, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if h.Verification == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification Service is unavailable."))
		return
	}
	var id ids.ID
	if err := h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT case_id FROM verification_links WHERE organisation_id=$1 AND purpose='KYB'`, t.Organisation).Scan(&id); err != nil {
		reply(w, r, 0, nil, apperrors.NotFound("VERIFICATION_NOT_FOUND", "No KYB case is linked."))
		return
	}
	_, err = h.Verification.Submit(r.Context(), id, "business-kyb:"+t.Organisation.String()+":case:"+id.String()+":submit")
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification submission is unavailable."))
		return
	}
	c, err := h.Verification.Get(r.Context(), id)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("The authoritative verification status is unavailable."))
		return
	}
	if c.VerificationID != id {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Verification identity mismatch."))
		return
	}
	err = h.applyVerification(r.Context(), t, c)
	reply(w, r, 202, c, err)
}
func (h *Handler) refreshKYB(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:verify", "COMPANY", ids.Nil, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if h.Verification == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification Service is unavailable."))
		return
	}
	var id ids.ID
	err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT case_id FROM verification_links WHERE organisation_id=$1 AND purpose='KYB'`, t.Organisation).Scan(&id)
	if err != nil {
		reply(w, r, 0, nil, apperrors.NotFound("VERIFICATION_NOT_FOUND", "No KYB case is linked."))
		return
	}
	c, err := h.Verification.Get(r.Context(), id)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification Service is unavailable."))
		return
	}
	if c.VerificationID != id {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Verification identity mismatch."))
		return
	}
	err = h.applyVerification(r.Context(), t, c)
	reply(w, r, 200, c, err)
}
func (h *Handler) applyVerification(ctx context.Context, t tenant, c verification.Case) error {
	p := organisation.Projection{Sealer: h.Auth.Service.Sealer, Pool: h.Auth.Service.Pool, Outbox: h.Auth.Service.Outbox, Now: h.Auth.Service.Now}
	return p.Apply(ctx, t.Organisation, &t.Account, c)
}
func (h *Handler) attachKYBEvidence(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MediaID      ids.ID `json:"media_id"`
		EvidenceType string `json:"evidence_type"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.MediaID.ValidateV7() != nil || len(in.EvidenceType) < 1 || len(in.EvidenceType) > 100 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid evidence reference."))
		return
	}
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:verify", "COMPANY", ids.Nil, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if h.Verification == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification Service is unavailable."))
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		reply(w, r, 0, nil, apperrors.IdempotencyKeyRequired())
		return
	}
	var id ids.ID
	if err := h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT case_id FROM verification_links WHERE organisation_id=$1 AND purpose='KYB'`, t.Organisation).Scan(&id); err != nil {
		reply(w, r, 0, nil, apperrors.NotFound("VERIFICATION_NOT_FOUND", "No KYB case is linked."))
		return
	}
	out, err := h.Verification.AttachEvidence(r.Context(), id, in.MediaID, in.EvidenceType, "business-evidence:"+t.Organisation.String()+":"+security.SHA256Hex([]byte(key)))
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification evidence operation is unavailable."))
		return
	}
	reply(w, r, 201, out, nil)
}
func (h *Handler) capacities(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:read", "COMPANY", ids.Nil, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT to_jsonb(x) FROM (SELECT id AS capacity_id,code,status,verification_link_id,effective_at,expires_at,restricted_at,version FROM organisation_capacities WHERE organisation_id=$1 ORDER BY code LIMIT $2 OFFSET $3) x`, t.Organisation, limit, offset)
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
