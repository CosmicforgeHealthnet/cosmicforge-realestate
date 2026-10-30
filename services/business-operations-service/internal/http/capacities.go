package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/organisation"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"regexp"
	"time"
)

var capacityCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

func (h *Handler) registerCapacities(r chi.Router) {
	r.Post("/organisation/capacities", h.declareCapacity)
	r.Patch("/organisation/capacities/{id}", h.restrictCapacity)
	r.Post("/organisation/capacities/{id}/verification", h.refreshCapacity)
	r.Get("/organisation/capability-packs", h.listPacks)
	r.Post("/organisation/capability-packs", h.writePack)
	r.Post("/organisation/capability-packs/{id}/evaluate", h.evaluatePack)
}
func (h *Handler) declareCapacity(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !capacityCode.MatchString(in.Code) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid capacity code."))
		return
	}
	h.execute(w, r, "capacity:declare", in, "capacity:write", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "capacity:write", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		id := ids.New()
		_, err = tx.Exec(ctx, `INSERT INTO organisation_capacities(id,organisation_id,code,status,effective_at) VALUES($1,$2,$3,'DECLARED',$4)`, id, t.Organisation, in.Code, h.Auth.Service.Now().UTC())
		if database.IsUniqueViolation(err, "") {
			return 0, nil, apperrors.Conflict("CAPACITY_EXISTS", "Capacity is already declared.")
		}
		if err != nil {
			return 0, nil, err
		}
		err = h.accessEvent(ctx, tx, t, "CAPACITY_DECLARED", "business.capacity.changed.v1", "capacity", id, in)
		return 201, map[string]any{"capacity_id": id, "status": "DECLARED", "authorizes": false}, err
	})
}
func (h *Handler) restrictCapacity(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid capacity ID."))
		return
	}
	var in struct {
		Version int64  `json:"version"`
		Status  string `json:"status"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || (in.Status != "RESTRICTED" && in.Status != "SUSPENDED") || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Only a reasoned restriction or suspension is accepted."))
		return
	}
	h.execute(w, r, "capacity:restrict:"+id.String(), in, "capacity:write", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "capacity:write", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE organisation_capacities SET status=$4,restricted_at=now(),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND version=$3`, t.Organisation, id, in.Version, in.Status)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Capacity version changed or is unavailable.")
		}
		err = h.accessEvent(ctx, tx, t, "CAPACITY_RESTRICTED", "business.capacity.changed.v1", "capacity", id, in)
		return 200, map[string]any{"capacity_id": id, "status": in.Status, "version": in.Version + 1}, err
	})
}
func (h *Handler) refreshCapacity(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid capacity ID."))
		return
	}
	var in struct {
		CaseID  ids.ID `json:"verification_id"`
		Version int64  `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.CaseID.ValidateV7() != nil || in.Version < 1 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A case reference and current version are required."))
		return
	}
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "capacity:write", "COMPANY", ids.Nil, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	var code string
	if err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT code FROM organisation_capacities WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&code); err != nil {
		reply(w, r, 0, nil, apperrors.NotFound("CAPACITY_NOT_FOUND", "Capacity not found."))
		return
	}
	if h.CapacityAuthority == nil || !h.CapacityAuthority.SupportsCapacity(code) {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification Service does not yet expose an operating-capacity eligibility assertion."))
		return
	}
	assertion, err := h.CapacityAuthority.Capacity(r.Context(), t.Organisation, code, in.CaseID)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Capacity authority is unavailable."))
		return
	}
	if assertion.OrganisationID != t.Organisation || assertion.Code != code || assertion.CaseID != in.CaseID || assertion.UpdatedAt.IsZero() {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Capacity assertion identity mismatch."))
		return
	}
	switch assertion.Status {
	case "VERIFIED", "PENDING", "REJECTED", "EXPIRED", "RESTRICTED":
	default:
		reply(w, r, 0, nil, apperrors.Unavailable("Unsupported capacity assertion."))
		return
	}
	err = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		current, _, err := h.authorize(ctx, tx, "capacity:write", "COMPANY", ids.Nil, true)
		if err != nil {
			return err
		}
		var restricted *time.Time
		var version int64
		if err = tx.QueryRow(ctx, `SELECT version,restricted_at FROM organisation_capacities WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id).Scan(&version, &restricted); err != nil {
			return err
		}
		if version != in.Version {
			return apperrors.Conflict("VERSION_CONFLICT", "Capacity version changed.")
		}
		if restricted != nil {
			return apperrors.PermissionDenied("A manual capacity restriction requires independent review before release.")
		}
		status := "PENDING"
		now := h.Auth.Service.Now().UTC()
		if assertion.Status == "VERIFIED" && assertion.ExpiresAt != nil && now.Before(*assertion.ExpiresAt) {
			status = "ACTIVE"
		} else if assertion.Status == "EXPIRED" || assertion.ExpiresAt != nil && !now.Before(*assertion.ExpiresAt) {
			status = "EXPIRED"
		} else if assertion.Status == "RESTRICTED" || assertion.Status == "REJECTED" {
			status = "RESTRICTED"
		}
		var link ids.ID
		err = tx.QueryRow(ctx, `INSERT INTO verification_links(id,organisation_id,case_id,purpose,provider_status,expires_at,provider_updated_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(organisation_id,purpose) DO UPDATE SET provider_status=excluded.provider_status,expires_at=excluded.expires_at,provider_updated_at=excluded.provider_updated_at,updated_at=now() WHERE verification_links.case_id=excluded.case_id AND (verification_links.provider_updated_at IS NULL OR verification_links.provider_updated_at<=excluded.provider_updated_at) RETURNING id`, ids.New(), t.Organisation, in.CaseID, "CAPACITY:"+code, assertion.Status, assertion.ExpiresAt, assertion.UpdatedAt).Scan(&link)
		if err != nil {
			return apperrors.Conflict("CAPACITY_ASSERTION_CONFLICT", "Capacity assertion is stale or belongs to a different case.")
		}
		_, err = tx.Exec(ctx, `UPDATE organisation_capacities SET verification_link_id=$3,status=$4,expires_at=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, link, status, assertion.ExpiresAt)
		if err != nil {
			return err
		}
		return h.accessEvent(ctx, tx, current, "CAPACITY_VERIFICATION_UPDATED", "business.capacity.changed.v1", "capacity", id, map[string]any{"status": status, "verification_id": in.CaseID})
	})
	reply(w, r, 200, map[string]any{"capacity_id": id, "version": in.Version + 1}, err)
}
func (h *Handler) writePack(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string   `json:"code"`
		Required []string `json:"required_capacities"`
		Enabled  bool     `json:"enabled"`
		Version  int64    `json:"version,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !capacityCode.MatchString(in.Code) || len(in.Required) < 1 || len(in.Required) > 30 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid capability pack."))
		return
	}
	seen := map[string]bool{}
	for _, c := range in.Required {
		if !capacityCode.MatchString(c) || seen[c] {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid or duplicate required capacity."))
			return
		}
		seen[c] = true
	}
	h.execute(w, r, "pack:write", in, "capacity:write", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "capacity:write", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		var id ids.ID
		err = tx.QueryRow(ctx, `INSERT INTO capability_packs(id,organisation_id,code,required_capacities,enabled) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organisation_id,code) DO UPDATE SET required_capacities=excluded.required_capacities,enabled=excluded.enabled,version=capability_packs.version+1 WHERE capability_packs.version=$6 RETURNING id`, ids.New(), t.Organisation, in.Code, in.Required, in.Enabled, in.Version).Scan(&id)
		if err != nil {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Pack version changed or is unavailable.")
		}
		err = h.accessEvent(ctx, tx, t, "CAPABILITY_PACK_CONFIGURED", "business.capacity.pack_changed.v1", "capability_pack", id, in)
		return 200, map[string]any{"capability_pack_id": id}, err
	})
}
func (h *Handler) listPacks(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT to_jsonb(x) FROM (SELECT id AS capability_pack_id,code,required_capacities,enabled,version FROM capability_packs WHERE organisation_id=$1 ORDER BY code LIMIT $2 OFFSET $3)x`, t.Organisation, limit, offset)
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
func (h *Handler) evaluatePack(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid pack ID."))
		return
	}
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:read", "COMPANY", ids.Nil, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	var required []string
	var enabled bool
	if err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT required_capacities,enabled FROM capability_packs WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&required, &enabled); err != nil {
		reply(w, r, 0, nil, apperrors.NotFound("PACK_NOT_FOUND", "Capability pack not found."))
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT c.code,c.status,c.effective_at,c.expires_at,c.restricted_at,v.case_id,v.provider_status,v.expires_at FROM organisation_capacities c JOIN verification_links v ON v.organisation_id=c.organisation_id AND v.id=c.verification_link_id AND v.purpose='CAPACITY:'||c.code WHERE c.organisation_id=$1`, t.Organisation)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	capacities := []organisation.Capacity{}
	for rows.Next() {
		var c organisation.Capacity
		if err := rows.Scan(&c.Code, &c.Status, &c.EffectiveAt, &c.ExpiresAt, &c.RestrictedAt, &c.Verification.CaseID, &c.Verification.Status, &c.Verification.ExpiresAt); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		capacities = append(capacities, c)
	}
	reply(w, r, 200, map[string]any{"capability_pack_id": id, "authorizes": enabled && h.packSupported(required) && organisation.EvaluatePack(required, capacities, organisation.State(t.State), h.Auth.Service.Now().UTC())}, rows.Err())
}

func (h *Handler) packSupported(required []string) bool {
	if h.CapacityAuthority == nil {
		return false
	}
	for _, code := range required {
		if !h.CapacityAuthority.SupportsCapacity(code) {
			return false
		}
	}
	return true
}
func (h *Handler) effectiveCapabilities(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT c.code,c.status,c.effective_at,c.expires_at,c.restricted_at,v.case_id,v.provider_status,v.expires_at FROM organisation_capacities c LEFT JOIN verification_links v ON v.organisation_id=c.organisation_id AND v.id=c.verification_link_id AND v.purpose='CAPACITY:'||c.code WHERE c.organisation_id=$1 ORDER BY c.code LIMIT $2 OFFSET $3`, t.Organisation, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var c organisation.Capacity
		var caseID *ids.ID
		var status *string
		if err := rows.Scan(&c.Code, &c.Status, &c.EffectiveAt, &c.ExpiresAt, &c.RestrictedAt, &caseID, &status, &c.Verification.ExpiresAt); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		if caseID != nil {
			c.Verification.CaseID = *caseID
		}
		if status != nil {
			c.Verification.Status = *status
		}
		supported := h.CapacityAuthority != nil && h.CapacityAuthority.SupportsCapacity(c.Code)
		items = append(items, map[string]any{"code": c.Code, "status": c.Status, "supported": supported, "authorizes": supported && c.Authorizes(c.Code, organisation.State(t.State), h.Auth.Service.Now().UTC()), "expires_at": c.ExpiresAt})
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, rows.Err())
}
