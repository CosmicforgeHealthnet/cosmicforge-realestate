package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

type PMSFolioCloseReviewCommand struct {
	Version        int64     `json:"version"`
	AutomaticClose bool      `json:"automatic_close"`
	ExpiresAt      time.Time `json:"expires_at"`
	Reason         string    `json:"reason"`
}
type PMSFolioCloseReview = hospitality.FolioCloseReview

type PMSFolioClosed struct {
	Folio              hospitality.Folio `json:"folio"`
	ReservationState   string            `json:"reservation_state"`
	ReservationVersion int64             `json:"reservation_version"`
	BalanceMinor       int64             `json:"balance_minor"`
}

// Review binds the exact immutable postings, never a client asserted balance.
func folioPostingDigest(ctx context.Context, q database.Querier, org, id ids.ID) (string, error) {
	var raw []byte
	e := q.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(i) ORDER BY i.id),'[]'::jsonb) FROM folio_items i WHERE organisation_id=$1 AND folio_id=$2`, org, id).Scan(&raw)
	return security.SHA256Hex(raw), e
}

func (h *Handler) pmsFolioCloseReview(w http.ResponseWriter, r *http.Request) {
	var in PMSFolioCloseReviewCommand
	if !decode(w, r, &in) {
		return
	}
	now := h.Auth.Service.Now().UTC()
	if in.Version < 1 || !validStaffReason(in.Reason) || !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(7*24*time.Hour)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, reason and expiry within seven days required."))
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
	f, e := hospitality.GetFolio(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:folio:close-review:"+id.String(), in, "folios:write", "PROPERTY", f.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", f.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(7*24*time.Hour)) || t.Direct.ExpiresAt != nil && in.ExpiresAt.After(*t.Direct.ExpiresAt) {
			return 0, nil, apperrors.PermissionDenied("Review cannot outlive its current authorizing membership.")
		}
		if e = workforce.Authorize(t.Direct, workforce.DecisionInput{AccountID: t.Account, Target: target, Capability: "folios:write", Now: now, MFAAt: t.MFAAt, StepUpWindow: 5 * time.Minute}); e != nil {
			return 0, nil, apperrors.PermissionDenied("Direct current folio authority is required; temporary authority cannot create worker grants.")
		}
		f, e := hospitality.GetFolio(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		s, e := hospitality.GetStay(ctx, tx, t.Organisation, f.StayID)
		if e != nil {
			return 0, nil, e
		}
		if f.Version != in.Version || s.State != "DEPARTED" || f.State == "DISPUTED" {
			return 0, nil, apperrors.Conflict("FOLIO_CLOSE_REVIEW_STATE", "Current departed, undisputed folio required.")
		}
		digest, e := folioPostingDigest(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		v := PMSFolioCloseReview{ID: ids.New(), FolioID: id, FolioVersion: f.Version, PostingDigest: digest, AutomaticClose: in.AutomaticClose, AuthorizingMembershipID: t.Member, AuthorizingRoleID: t.Role, ExpiresAt: in.ExpiresAt}
		_, e = tx.Exec(ctx, `INSERT INTO folio_close_reviews(id,organisation_id,property_id,folio_id,folio_version,posting_digest,automatic_close,authorizing_membership_id,authorizing_role_id,expires_at,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, v.ID, t.Organisation, f.PropertyID, id, f.Version, digest, in.AutomaticClose, t.Member, t.Role, in.ExpiresAt, in.Reason, t.Account)
		if e != nil {
			return 0, nil, e
		}
		e = h.pmsCommit(ctx, tx, t, f.PropertyID, v.ID, "folio_close_review", "FOLIO_CLOSE_REVIEWED", "hospitality.folio.reviewed.v1", 1, map[string]any{"review": v, "reason": in.Reason})
		return 201, v, e
	})
}

func (h *Handler) pmsFolioReviewRevoke(w http.ResponseWriter, r *http.Request) {
	var in PMSVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version != 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Immutable review version one and revocation reason required."))
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
	e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM folio_close_reviews WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&property)
	if errors.Is(e, pgx.ErrNoRows) {
		e = apperrors.NotFound("FOLIO_REVIEW_NOT_FOUND", "Review not found.")
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:folio:review:revoke:"+id.String(), in, "folios:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		rid := ids.New()
		_, e = tx.Exec(ctx, `INSERT INTO folio_close_review_revocations(id,organisation_id,property_id,review_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6)`, rid, t.Organisation, property, id, in.Reason, t.Account)
		if e != nil {
			return 0, nil, e
		}
		e = h.pmsCommit(ctx, tx, t, property, rid, "folio_close_review_revocation", "FOLIO_CLOSE_REVIEW_REVOKED", "hospitality.folio.review.revoked.v1", 1, map[string]any{"review_id": id, "reason": in.Reason})
		return 201, map[string]any{"id": rid, "review_id": id, "state": "REVOKED"}, e
	})
}

func (h *Handler) pmsFolioClose(w http.ResponseWriter, r *http.Request) {
	var in PMSVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current folio version and final review reason required."))
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
	f, e := hospitality.GetFolio(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:folio:close:"+id.String(), in, "folios:write", "PROPERTY", f.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", f.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		f, e := hospitality.GetFolio(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if f.Version != in.Version {
			return 0, nil, apperrors.Conflict("FOLIO_VERSION", "Folio changed after review.")
		}
		out, e := h.closePMSFolio(ctx, tx, t, f, in.Reason)
		return 200, out, e
	})
}

func (h *Handler) closePMSFolio(ctx context.Context, tx pgx.Tx, t tenant, f hospitality.Folio, reason string) (out PMSFolioClosed, e error) {
	s, e := hospitality.GetStay(ctx, tx, t.Organisation, f.StayID)
	if e != nil {
		return out, e
	}
	if s.State != "DEPARTED" || f.State == "DISPUTED" {
		return out, apperrors.Conflict("FOLIO_CLOSE_STATE", "Actual departure and undisputed folio required.")
	}
	now := h.Auth.Service.Now().UTC()
	balance, e := hospitality.Balance(ctx, tx, t.Organisation, f.ID, now)
	if e != nil {
		return out, e
	}
	if balance != 0 {
		return out, apperrors.Conflict("FOLIO_NOT_SETTLED", "Fresh owning-service financial evidence must cover all postings exactly.")
	}
	if f.State != "CLOSED" {
		if _, e = tx.Exec(ctx, `UPDATE folios SET state='CLOSED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, f.ID); e != nil {
			return out, e
		}
		f.Version++
		f.State = "CLOSED"
		if e = h.pmsCommit(ctx, tx, t, f.PropertyID, f.ID, "folio", "FOLIO_CLOSED", "hospitality.folio.updated.v1", f.Version, map[string]any{"state": "CLOSED", "stay_id": s.ID, "reason": reason, "actor_service": serviceActor(t)}); e != nil {
			return out, e
		}
	}
	v, e := reservations.Get(ctx, tx, t.Organisation, f.ReservationID)
	if e != nil {
		return out, e
	}
	// Every accepted quantity slot must have an actual departed physical stay.
	// Facility or unfulfilled mixed roots remain outside this hotel completion.
	var ready bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE') AND NOT EXISTS(SELECT 1 FROM reservation_rooms rr WHERE rr.organisation_id=$1 AND rr.reservation_id=$2 AND rr.state='ACTIVE' AND rr.quantity<>(SELECT count(*) FROM stay_records s JOIN room_assignments a ON a.organisation_id=s.organisation_id AND a.id=s.assignment_id JOIN folios f ON f.organisation_id=s.organisation_id AND f.stay_id=s.id WHERE s.organisation_id=rr.organisation_id AND s.reservation_id=rr.reservation_id AND a.reservation_room_id=rr.id AND s.state='DEPARTED' AND f.state='CLOSED'))`, t.Organisation, v.ID).Scan(&ready)
	if e != nil {
		return out, e
	}
	if ready && v.State == "CHECKED_OUT" {
		rows, e := tx.Query(ctx, `SELECT id FROM folios WHERE organisation_id=$1 AND reservation_id=$2`, t.Organisation, v.ID)
		if e != nil {
			return out, e
		}
		var folios []ids.ID
		for rows.Next() {
			var id ids.ID
			if e = rows.Scan(&id); e != nil {
				break
			}
			folios = append(folios, id)
		}
		re := rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		if re != nil {
			return out, re
		}
		for _, id := range folios {
			b, e := hospitality.Balance(ctx, tx, t.Organisation, id, now)
			if e != nil {
				return out, e
			}
			if b != 0 {
				ready = false
				break
			}
		}
		if ready {
			if _, e = tx.Exec(ctx, `UPDATE reservations SET state='COMPLETED',completed_at=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, now); e != nil {
				return out, e
			}
			v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
			if e != nil {
				return out, e
			}
			if e = h.reservationCommit(ctx, tx, t, v, "COMPLETED", "reservation.completed.v1", reason, map[string]any{"folio_id": f.ID, "actor_service": serviceActor(t)}); e != nil {
				return out, e
			}
		}
	}
	if e = h.resolvePMSFolioExceptions(ctx, tx, t, f); e != nil {
		return out, e
	}
	return PMSFolioClosed{Folio: f, ReservationState: v.State, ReservationVersion: v.Version, BalanceMinor: 0}, nil
}
func serviceActor(t tenant) string {
	if t.Account.IsZero() {
		return "business-operations-service"
	}
	return ""
}

// Recorded authority is reloaded without a session, delegated grants or invented
// MFA. A scope requiring fresh per-action MFA cannot authorize a worker.
func (h *Handler) currentFolioReviewAuthority(ctx context.Context, tx pgx.Tx, org, property ids.ID, review PMSFolioCloseReview) (bool, error) {
	var m workforce.Membership
	var state string
	e := tx.QueryRow(ctx, `SELECT m.account_id,m.organisation_id,m.status,m.effective_at,m.expires_at,o.state FROM staff_memberships m JOIN role_templates r ON r.organisation_id=m.organisation_id AND r.id=m.role_id JOIN business_accounts a ON a.id=m.account_id JOIN organisations o ON o.id=m.organisation_id WHERE m.organisation_id=$1 AND m.id=$2 AND m.role_id=$3 AND r.active AND a.status='ACTIVE' FOR SHARE OF m,r,a`, org, review.AuthorizingMembershipID, review.AuthorizingRoleID).Scan(&m.AccountID, &m.OrganisationID, &m.Status, &m.EffectiveAt, &m.ExpiresAt, &state)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	now := h.Auth.Service.Now().UTC()
	var verified bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verification_links WHERE organisation_id=$1 AND purpose='KYB' AND provider_status='VERIFIED' AND expires_at>$2)`, org, now).Scan(&verified)
	if e != nil {
		return false, e
	}
	m.RoleCapabilities = permissions.NewSet()
	if e = workforce.LoadDirectGrants(ctx, tx, org, review.AuthorizingMembershipID, review.AuthorizingRoleID, &m); e != nil {
		return false, e
	}
	target, e := resolve(ctx, tx, org, "PROPERTY", property)
	if e != nil {
		return false, e
	}
	return workforce.Authorize(m, workforce.DecisionInput{AccountID: m.AccountID, Target: target, Capability: "folios:write", Now: now, Restricted: state != "ACTIVE" || !verified}) == nil, nil
}

func (h *Handler) pmsCloseReviews(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "folios:read", r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(v)||jsonb_build_object('review_state',CASE WHEN EXISTS(SELECT 1 FROM folio_close_review_revocations x WHERE x.organisation_id=v.organisation_id AND x.review_id=v.id) THEN 'REVOKED' WHEN v.expires_at<=$5 THEN 'EXPIRED' WHEN EXISTS(SELECT 1 FROM folio_close_reviews newer WHERE newer.organisation_id=v.organisation_id AND newer.folio_id=v.folio_id AND (newer.created_at,newer.id)>(v.created_at,v.id)) THEN 'SUPERSEDED' WHEN v.automatic_close THEN 'AUTOMATIC_REVIEW_RECORDED' ELSE 'MANUAL_REVIEW_RECORDED' END) FROM folio_close_reviews v WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset, h.Auth.Service.Now().UTC())
	if items == nil {
		items = []json.RawMessage{}
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) pmsOperationExceptions(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "hospitality_operation_exceptions", "pms:read")
}
func (h *Handler) resolvePMSFolioExceptions(ctx context.Context, tx pgx.Tx, t tenant, f hospitality.Folio) error {
	rows, e := tx.Query(ctx, `UPDATE hospitality_operation_exceptions SET state='RESOLVED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND source_type='folio' AND source_id=$2 AND state='OPEN' RETURNING id,version`, t.Organisation, f.ID)
	if e != nil {
		return e
	}
	type ref struct {
		id      ids.ID
		version int64
	}
	var refs []ref
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.id, &v.version); e != nil {
			break
		}
		refs = append(refs, v)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	for _, v := range refs {
		if e = h.pmsCommit(ctx, tx, t, f.PropertyID, v.id, "hospitality_exception", "HOSPITALITY_EXCEPTION_RESOLVED", "hospitality.operation.exception.v1", v.version, map[string]any{"folio_id": f.ID, "folio_version": f.Version, "actor_service": serviceActor(t), "reason": "Current reviewed folio closed with exact fresh financial backing"}); e != nil {
			return e
		}
	}
	return nil
}
