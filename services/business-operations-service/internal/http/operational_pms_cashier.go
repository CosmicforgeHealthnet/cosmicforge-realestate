package businesshttp

import (
	"context"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type PMSDrawerCommand struct {
	PropertyID ids.ID `json:"property_id"`
	Code       string `json:"code"`
	Currency   string `json:"currency"`
	Reason     string `json:"reason"`
}
type PMSCashierOpenCommand struct {
	DrawerID     ids.ID `json:"drawer_id"`
	OpeningMinor int64  `json:"opening_minor"`
	Reason       string `json:"reason"`
}
type PMSCashierCloseCommand struct {
	Version      int64  `json:"version"`
	ClosingMinor int64  `json:"closing_minor"`
	ApprovalID   ids.ID `json:"approval_id,omitempty"`
	Reason       string `json:"reason"`
}
type PMSCashierHandoverCommand struct {
	Version           int64  `json:"version"`
	DeclaredMinor     int64  `json:"declared_minor"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
}
type PMSCashierShift struct {
	ID             ids.ID `json:"id"`
	OrganisationID ids.ID `json:"organisation_id"`
	PropertyID     ids.ID `json:"property_id"`
	DrawerID       ids.ID `json:"drawer_id"`
	BusinessDayID  ids.ID `json:"business_day_id"`
	CashierID      ids.ID `json:"cashier_id"`
	OpeningMinor   int64  `json:"opening_minor"`
	ClosingMinor   *int64 `json:"closing_minor,omitempty"`
	ExpectedMinor  *int64 `json:"expected_minor,omitempty"`
	VarianceMinor  *int64 `json:"variance_minor,omitempty"`
	State          string `json:"state"`
	Version        int64  `json:"version"`
}

// Payment owns the actual movement calculation. This signed adapter contract
// does not imply a configured producer, nor permit a browser to attest receipts.
type PMSCashAttestation struct {
	OrganisationID        ids.ID    `json:"organisation_id"`
	PropertyID            ids.ID    `json:"property_id"`
	ShiftID               ids.ID    `json:"shift_id"`
	SourceVersion         int64     `json:"source_version"`
	ExpectedMovementMinor int64     `json:"expected_movement_minor"`
	Currency              string    `json:"currency"`
	SourceReference       string    `json:"source_reference"`
	ObservedAt            time.Time `json:"observed_at"`
	ExpiresAt             time.Time `json:"expires_at"`
}

func (h *Handler) registerPMSCashier(r chi.Router) {
	r.Get("/cash-drawers", h.pmsCashDrawers)
	r.Post("/cash-drawers", h.pmsCashDrawerCreate)
	r.Get("/cashier-shifts", h.pmsCashierShifts)
	r.Post("/cashier-shifts/open", h.pmsCashierOpen)
	r.Post("/cashier-shifts/{id}/close", h.pmsCashierClose)
	r.Post("/cashier-shifts/{id}/handover", h.pmsCashierHandover)
}
func (h *Handler) pmsCashDrawerCreate(w http.ResponseWriter, r *http.Request) {
	var in PMSDrawerCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !crm.OpaqueReference(in.Code) || len(in.Code) > 80 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, bounded drawer code and reason required."))
		return
	}
	h.execute(w, r, "pms:drawer:create", in, "cashier:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "cashier:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		var currency string
		if e = tx.QueryRow(ctx, `SELECT currency FROM inventory_nodes WHERE organisation_id=$1 AND id=$2 AND kind='PROPERTY'`, t.Organisation, in.PropertyID).Scan(&currency); e != nil {
			return 0, nil, e
		}
		if currency != in.Currency {
			return 0, nil, apperrors.InvalidRequest("Drawer currency must match the canonical property currency.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO cash_drawers(id,organisation_id,property_id,code,currency,state,created_by) VALUES($1,$2,$3,$4,$5,'ACTIVE',$6)`, id, t.Organisation, in.PropertyID, in.Code, in.Currency, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, id, "cash_drawer", "CASH_DRAWER_CREATED", "hospitality.cashier.updated.v1", 1, in); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": id, "state": "ACTIVE", "version": 1}, nil
	})
}
func (h *Handler) pmsCashierOpen(w http.ResponseWriter, r *http.Request) {
	var in PMSCashierOpenCommand
	if !decode(w, r, &in) {
		return
	}
	if in.DrawerID.ValidateV7() != nil || in.OpeningMinor < 0 || in.OpeningMinor > 1000000000000 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Drawer, integer minor opening declaration and reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var property ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM cash_drawers WHERE organisation_id=$1 AND id=$2 AND state='ACTIVE'`, t.Organisation, in.DrawerID).Scan(&property); e != nil {
		reply(w, r, 0, nil, apperrors.NotFound("CASH_DRAWER_NOT_FOUND", "Active drawer not found."))
		return
	}
	h.execute(w, r, "pms:cashier:open:"+in.DrawerID.String(), in, "cashier:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "cashier:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		day, e := h.pmsDay(ctx, tx, t, property, true)
		if e != nil {
			return 0, nil, e
		}
		var active bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cashier_shifts WHERE organisation_id=$1 AND drawer_id=$2 AND state IN ('OPEN','RECONCILIATION_REQUIRED'))`, t.Organisation, in.DrawerID).Scan(&active); e != nil {
			return 0, nil, e
		}
		if active {
			return 0, nil, apperrors.Conflict("DRAWER_SHIFT_ACTIVE", "Close and reconcile the existing drawer shift first.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO cashier_shifts(id,organisation_id,property_id,drawer_id,business_day_id,cashier_id,opening_minor,state,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,'OPEN',$6)`, id, t.Organisation, property, in.DrawerID, day.ID, t.Account, in.OpeningMinor); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, property, id, "cashier_shift", "CASHIER_SHIFT_OPENED", "hospitality.cashier.updated.v1", 1, in); e != nil {
			return 0, nil, e
		}
		out, e := hospitality.Load[PMSCashierShift](ctx, tx, `SELECT to_jsonb(s) FROM cashier_shifts s WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 201, out, e
	})
}
func (h *Handler) pmsCashierClose(w http.ResponseWriter, r *http.Request) {
	var in PMSCashierCloseCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.ClosingMinor < 0 || in.ClosingMinor > 1000000000000 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current shift version, actual count and reason required."))
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
	pre, e := hospitality.Load[PMSCashierShift](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(s) FROM cashier_shifts s WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:cashier:close:"+id.String(), in, "cashier:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "cashier:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		s, e := hospitality.Load[PMSCashierShift](ctx, tx, `SELECT to_jsonb(s) FROM cashier_shifts s WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if s.Version != in.Version || s.State == "CLOSED" || s.CashierID != t.Account || s.ClosingMinor != nil && *s.ClosingMinor != in.ClosingMinor {
			return 0, nil, apperrors.Conflict("CASHIER_CLOSE_STATE", "Only the owning cashier can declare the current open shift; a retained closing count cannot be rewritten.")
		}
		var movement *int64
		var currency string
		var proof *ids.ID
		if e = tx.QueryRow(ctx, `SELECT a.expected_movement_minor,d.currency,a.id FROM cash_drawers d LEFT JOIN LATERAL(SELECT * FROM cashier_financial_attestations a WHERE a.organisation_id=d.organisation_id AND a.shift_id=$2 ORDER BY source_version DESC LIMIT 1)a ON a.expires_at>$3 WHERE d.organisation_id=$1 AND d.id=$4`, t.Organisation, id, h.Auth.Service.Now(), s.DrawerID).Scan(&movement, &currency, &proof); e != nil {
			return 0, nil, e
		}
		var expected, variance *int64
		state := "RECONCILIATION_REQUIRED"
		exposure := max(s.OpeningMinor, in.ClosingMinor)
		if movement != nil {
			n := s.OpeningMinor + *movement
			v := in.ClosingMinor - n
			expected = &n
			variance = &v
			state = "CLOSED"
			exposure = v
			if exposure < 0 {
				exposure = -exposure
			}
		}
		var approval any
		if variance == nil || *variance != 0 {
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_CASHIER_VARIANCE", target.Scope, in.ApprovalID, map[string]any{"shift_id": id, "financial_attestation_id": proof, "command": op}, "", &exposure, currency); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("A balanced authoritative shift does not consume a variance approval.")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO cashier_variances(id,organisation_id,property_id,shift_id,shift_version,expected_minor,closing_minor,variance_minor,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, ids.New(), t.Organisation, s.PropertyID, id, s.Version+1, expected, in.ClosingMinor, variance, approval, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE cashier_shifts SET state=$3,closing_minor=$4,expected_minor=$5,variance_minor=$6,closed_at=coalesce(closed_at,$7),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, state, in.ClosingMinor, expected, variance, h.Auth.Service.Now()); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, s.PropertyID, id, "cashier_shift", "CASHIER_SHIFT_CLOSED", "hospitality.cashier.updated.v1", s.Version+1, map[string]any{"state": state, "expected_minor": expected, "closing_minor": in.ClosingMinor, "variance_minor": variance, "financial_attestation_id": proof, "approval_id": approval, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := hospitality.Load[PMSCashierShift](ctx, tx, `SELECT to_jsonb(s) FROM cashier_shifts s WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 200, out, e
	})
}
func (h *Handler) pmsCashierHandover(w http.ResponseWriter, r *http.Request) {
	var in PMSCashierHandoverCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.DeclaredMinor < 0 || !validStaffReason(in.Reason) || !crm.Text(in.EvidenceReference, 5, 300) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current shift, actual received declaration, evidence and reason required."))
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
	s, e := hospitality.Load[PMSCashierShift](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(s) FROM cashier_shifts s WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:cashier:handover:"+id.String(), in, "cashier:write", "PROPERTY", s.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "cashier:write", "PROPERTY", s.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		s, e := hospitality.Load[PMSCashierShift](ctx, tx, `SELECT to_jsonb(s) FROM cashier_shifts s WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if s.Version != in.Version || s.State != "CLOSED" || s.CashierID == t.Account || s.ClosingMinor == nil || *s.ClosingMinor != in.DeclaredMinor {
			return 0, nil, apperrors.Conflict("CASHIER_HANDOVER_DENIED", "A distinct authorized receiver must confirm the reconciled shift's immutable closing count.")
		}
		receipt := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO cashier_handovers(id,organisation_id,property_id,shift_id,from_account_id,to_account_id,declared_minor,evidence_reference,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, receipt, t.Organisation, s.PropertyID, id, s.CashierID, t.Account, in.DeclaredMinor, in.EvidenceReference, in.Reason); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, s.PropertyID, receipt, "cashier_handover", "CASHIER_HANDOVER_ACKNOWLEDGED", "hospitality.cashier.updated.v1", 1, in); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": receipt, "state": "ACKNOWLEDGED"}, nil
	})
}
func (h *Handler) internalPMSCashAttestation(w http.ResponseWriter, r *http.Request) {
	var in PMSCashAttestation
	if !decode(w, r, &in) {
		return
	}
	p, ok := serviceauth.PrincipalFrom(r.Context())
	if !ok || p.Service != platform.PaymentService || p.Delegation.OrganizationID != in.OrganisationID {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Signed owning Payment identity and exact delegated organisation required."))
		return
	}
	now := h.Auth.Service.Now().UTC()
	if in.OrganisationID.ValidateV7() != nil || in.PropertyID.ValidateV7() != nil || in.ShiftID.ValidateV7() != nil || in.SourceVersion < 1 || in.ExpectedMovementMinor > 1000000000000 || in.ExpectedMovementMinor < -1000000000000 || !crm.OpaqueReference(in.SourceReference) || in.ObservedAt.IsZero() || in.ObservedAt.After(now.Add(time.Minute)) || !in.ExpiresAt.After(in.ObservedAt) || in.ExpiresAt.After(now.Add(24*time.Hour)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Bounded, monotonic, time-limited cash reconciliation attestation required."))
		return
	}
	var out any
	e := database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, in.OrganisationID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','payment-service',true)`); e != nil {
			return e
		}
		var currency string
		if e := tx.QueryRow(ctx, `SELECT d.currency FROM cashier_shifts s JOIN cash_drawers d ON d.organisation_id=s.organisation_id AND d.id=s.drawer_id WHERE s.organisation_id=$1 AND s.property_id=$2 AND s.id=$3`, in.OrganisationID, in.PropertyID, in.ShiftID).Scan(&currency); e != nil {
			return apperrors.NotFound("CASHIER_SHIFT_NOT_FOUND", "Exact delegated shift not found.")
		}
		if currency != in.Currency {
			return apperrors.InvalidRequest("Attested currency differs from the actual drawer.")
		}
		var version int64
		var lastDigest *string
		if e := tx.QueryRow(ctx, `SELECT coalesce(max(source_version),0),(array_agg(payload_digest ORDER BY source_version DESC))[1] FROM cashier_financial_attestations WHERE organisation_id=$1 AND shift_id=$2`, in.OrganisationID, in.ShiftID).Scan(&version, &lastDigest); e != nil {
			return e
		}
		digest := security.SHA256Hex(marshalCommercial(in))
		if in.SourceVersion <= version {
			if in.SourceVersion == version && lastDigest != nil && *lastDigest == digest {
				out = map[string]any{"source_version": version, "duplicate": true}
				return nil
			}
			return apperrors.Conflict("CASH_ATTESTATION_VERSION", "Stale or conflicting source evidence cannot replace the latest cash attestation.")
		}
		id := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO cashier_financial_attestations(id,organisation_id,property_id,shift_id,source_version,expected_movement_minor,currency,source_reference,observed_at,expires_at,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, in.OrganisationID, in.PropertyID, in.ShiftID, in.SourceVersion, in.ExpectedMovementMinor, in.Currency, in.SourceReference, in.ObservedAt, in.ExpiresAt, digest); e != nil {
			return e
		}
		if e := h.commercialCommit(ctx, tx, tenant{Organisation: in.OrganisationID}, in.PropertyID, id, "cash_attestation", "CASH_FINANCIAL_ATTESTATION_RECORDED", "hospitality.cashier.updated.v1", in.SourceVersion, map[string]any{"shift_id": in.ShiftID, "source_service": "payment-service", "source_reference": in.SourceReference, "expires_at": in.ExpiresAt}); e != nil {
			return e
		}
		out = map[string]any{"id": id, "source_version": in.SourceVersion, "duplicate": false}
		return nil
	})
	reply(w, r, 200, out, e)
}
func (h *Handler) pmsCashDrawers(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "cash_drawers", "cashier:read")
}
func (h *Handler) pmsCashierShifts(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "cashier_shifts", "cashier:read")
}
