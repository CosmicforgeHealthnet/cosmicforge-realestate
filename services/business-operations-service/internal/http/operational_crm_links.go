package businesshttp

import (
	"context"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type CRMVersionCommand struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

func (h *Handler) crmGuestLinkRevoke(w http.ResponseWriter, r *http.Request) {
	var in CRMVersionCommand
	if !decode(w, r, &in) {
		return
	}
	link, e := ids.ParseV7(chi.URLParam(r, "link_id"))
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current identity-link version and reason required."))
		return
	}
	_, pre, property, e := h.crmGuestTarget(r, "guest_privacy:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:identity-revoke:"+link.String(), in, "guest_privacy:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		var guest, owningProperty ids.ID
		var version int64
		var state string
		if e = tx.QueryRow(ctx, `SELECT guest_id,property_id,version,state FROM guest_identity_links WHERE organisation_id=$1 AND id=$2`, t.Organisation, link).Scan(&guest, &owningProperty, &version, &state); e != nil {
			return 0, nil, e
		}
		if guest != pre.ID {
			return 0, nil, apperrors.PermissionDenied("Identity link belongs to another guest.")
		}
		if _, _, e = h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", owningProperty, true); e != nil {
			return 0, nil, e
		}
		if version != in.Version || state != "ACTIVE" {
			return 0, nil, apperrors.Conflict("GUEST_LINK_VERSION", "Identity link changed or was revoked.")
		}
		if _, e = tx.Exec(ctx, `UPDATE guest_identity_links SET state='REVOKED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, link); e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, owningProperty, guest, link, "guest_identity_link", version+1, "GUEST_IDENTITY_LINK_REVOKED", map[string]any{"state": "REVOKED", "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"id": link, "guest_id": guest, "state": "REVOKED", "version": version + 1}, nil
	})
}

type CRMIdentityLinkCommand struct {
	GuestVersion      int64   `json:"guest_version"`
	SourceKind        string  `json:"source_kind"`
	SourceReference   string  `json:"source_reference"`
	ProjectionID      *ids.ID `json:"projection_id,omitempty"`
	ApprovalID        ids.ID  `json:"approval_id"`
	EvidenceReference string  `json:"evidence_reference"`
	Reason            string  `json:"reason"`
}
type CRMGuestScopeCommand struct {
	PropertyID      ids.ID `json:"property_id"`
	ExpectedVersion int64  `json:"expected_version"`
	State           string `json:"state"`
	ApprovalID      ids.ID `json:"approval_id,omitempty"`
	Reason          string `json:"reason"`
}
type CRMSourceAssertion struct {
	OrganisationID    ids.ID    `json:"organisation_id"`
	ConnectionID      ids.ID    `json:"connection_id"`
	PropertyID        ids.ID    `json:"property_id"`
	SourceReference   string    `json:"source_reference"`
	SourceVersion     int64     `json:"source_version"`
	State             string    `json:"state"`
	ExpiresAt         time.Time `json:"expires_at"`
	EvidenceReference string    `json:"evidence_reference"`
}
type CRMIdentityProjection struct {
	ID                  ids.ID    `json:"id"`
	PropertyID          ids.ID    `json:"property_id"`
	ConnectionID        ids.ID    `json:"connection_id"`
	SourceKind          string    `json:"source_kind"`
	SourceService       string    `json:"source_service"`
	SourceReferenceHash string    `json:"source_reference_hash"`
	SourceVersion       int64     `json:"source_version"`
	State               string    `json:"state"`
	ExpiresAt           time.Time `json:"expires_at"`
}

func (h *Handler) crmGuestLinks(w http.ResponseWriter, r *http.Request) {
	t, g, property, e := h.crmGuestTarget(r, "guests:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(l)-'source_reference_hash' FROM guest_identity_links l WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY created_at,id LIMIT $4 OFFSET $5`, t.Organisation, g.ID, property, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) crmGuestLink(w http.ResponseWriter, r *http.Request) {
	var in CRMIdentityLinkCommand
	if !decode(w, r, &in) {
		return
	}
	if in.GuestVersion < 1 || !crm.OpaqueReference(in.SourceReference) || !crm.Text(in.EvidenceReference, 5, 300) || !validStaffReason(in.Reason) || !crm.LocalSource(in.SourceKind) && in.SourceKind != "MARKETPLACE" && in.SourceKind != "OTA" || in.ProjectionID != nil && in.ProjectionID.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact typed guest source, current version and evidence required."))
		return
	}
	_, pre, property, e := h.crmGuestTarget(r, "guests:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:identity-link:"+pre.ID.String(), in, "guests:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		g, e := crm.GetGuest(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if e = crm.RequireGuest(ctx, tx, t.Organisation, property, g.ID); e != nil {
			return 0, nil, e
		}
		if g.Version != in.GuestVersion {
			return 0, nil, apperrors.Conflict("GUEST_VERSION", "Guest changed.")
		}
		hash := h.crmSourceHash(t.Organisation, in.SourceKind, in.SourceReference)
		authority := "business-operations-service"
		var connection *ids.ID
		if in.SourceKind == "MARKETPLACE" || in.SourceKind == "OTA" {
			if in.ProjectionID == nil {
				return 0, nil, apperrors.PermissionDenied("An authenticated owning source assertion is required; staff approval alone cannot verify an external account.")
			}
			p, e := crm.Load[CRMIdentityProjection](ctx, tx, `SELECT to_jsonb(p) FROM customer_accounts_projection p WHERE organisation_id=$1 AND id=$2`, t.Organisation, *in.ProjectionID)
			if e != nil {
				return 0, nil, e
			}
			if p.PropertyID != property || p.SourceKind != in.SourceKind || p.SourceReferenceHash != hash || p.State != "ACTIVE" || !h.Auth.Service.Now().Before(p.ExpiresAt) {
				return 0, nil, apperrors.PermissionDenied("Current exact source identity projection required.")
			}
			var latest int64
			if e = tx.QueryRow(ctx, `SELECT max(source_version) FROM customer_accounts_projection WHERE organisation_id=$1 AND connection_id=$2 AND source_kind=$3 AND source_reference_hash=$4`, t.Organisation, p.ConnectionID, p.SourceKind, hash).Scan(&latest); e != nil {
				return 0, nil, e
			}
			if latest != p.SourceVersion {
				return 0, nil, apperrors.Conflict("GUEST_SOURCE_STALE", "Source identity assertion was superseded.")
			}
			authority = p.SourceService
			connection = &p.ConnectionID
		} else if in.ProjectionID != nil {
			return 0, nil, apperrors.InvalidRequest("Local registration cannot borrow an external projection.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_GUEST_IDENTITY_LINK", target.Scope, in.ApprovalID, map[string]any{"guest_id": g.ID, "property_id": property, "command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		var conflict bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_identity_links WHERE organisation_id=$1 AND source_kind=$2 AND source_authority=$3 AND connection_id IS NOT DISTINCT FROM $4::uuid AND source_reference_hash=$5)`, t.Organisation, in.SourceKind, authority, connection, hash).Scan(&conflict); e != nil {
			return 0, nil, e
		}
		if conflict {
			return 0, nil, apperrors.Conflict("GUEST_IDENTITY_CONFLICT", "This source identity already has a retained link and requires explicit conflict review; no automatic merge is allowed.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO guest_identity_links(id,organisation_id,guest_id,property_id,source_kind,source_authority,source_reference_hash,connection_id,projection_id,proof_kind,approval_id,evidence_reference,state,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'APPROVED_SOURCE_LINK',$10,$11,'ACTIVE',$12)`, id, t.Organisation, g.ID, property, in.SourceKind, authority, hash, connection, in.ProjectionID, in.ApprovalID, in.EvidenceReference, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, property, g.ID, id, "guest_identity_link", 1, "GUEST_IDENTITY_LINKED", map[string]any{"source_kind": in.SourceKind, "source_authority": authority, "projection_id": in.ProjectionID, "approval_id": in.ApprovalID, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": id, "guest_id": g.ID, "property_id": property, "source_kind": in.SourceKind, "state": "ACTIVE", "version": 1}, nil
	})
}
func (h *Handler) crmGuestScope(w http.ResponseWriter, r *http.Request) {
	var in CRMGuestScopeCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.ExpectedVersion < 0 || in.State != "ACTIVE" && in.State != "REVOKED" || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact destination property, state, version and reason required."))
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
	pre, e := crm.GetGuest(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:guest-scope:"+pre.ID.String()+":"+in.PropertyID.String(), in, "guests:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		_, target, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		var current int64
		var old *ids.ID
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0),(array_agg(id))[1] FROM guest_property_scopes WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3`, t.Organisation, pre.ID, in.PropertyID).Scan(&current, &old); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("GUEST_SCOPE_VERSION", "Guest property scope changed.")
		}
		var approval any
		if in.State == "ACTIVE" {
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_GUEST_SCOPE_LINK", target.Scope, in.ApprovalID, map[string]any{"guest_id": pre.ID, "command": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("Revocation does not consume scope expansion approval.")
		}
		if in.State == "REVOKED" {
			var active bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_guests g JOIN reservations r ON r.id=g.reservation_id AND r.organisation_id=g.organisation_id WHERE g.organisation_id=$1 AND g.guest_id=$2 AND g.property_id=$3 AND r.state IN ('CONFIRMED','CHECKED_IN'))`, t.Organisation, pre.ID, in.PropertyID).Scan(&active); e != nil {
				return 0, nil, e
			}
			if active {
				return 0, nil, apperrors.Conflict("GUEST_SCOPE_ACTIVE_STAY", "Retain the property association while confirmed/in-house guest obligations require it.")
			}
		}
		id := ids.New()
		if old == nil {
			_, e = tx.Exec(ctx, `INSERT INTO guest_property_scopes(id,organisation_id,guest_id,property_id,state,approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, pre.ID, in.PropertyID, in.State, approval, t.Account)
		} else {
			id = *old
			_, e = tx.Exec(ctx, `UPDATE guest_property_scopes SET state=$3,approval_id=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State, approval)
		}
		if e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, in.PropertyID, pre.ID, id, "guest_property_scope", current+1, "GUEST_PROPERTY_SCOPE_CHANGED", map[string]any{"state": in.State, "approval_id": approval, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"id": id, "guest_id": pre.ID, "property_id": in.PropertyID, "state": in.State, "version": current + 1}, nil
	})
}

// The source service authenticates its own account assertion. Delegated tenant,
// certified Marketplace connection and monotonic source version are mandatory.
func (h *Handler) internalCRMSourceAssertion(w http.ResponseWriter, r *http.Request) {
	var in CRMSourceAssertion
	if !decode(w, r, &in) {
		return
	}
	p, ok := serviceauth.PrincipalFrom(r.Context())
	now := h.Auth.Service.Now().UTC()
	if !ok || p.Service != platform.MarketplaceService || p.Delegation.OrganizationID != in.OrganisationID {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Authenticated owning source and delegated organisation required."))
		return
	}
	if in.OrganisationID.ValidateV7() != nil || in.PropertyID.ValidateV7() != nil || in.ConnectionID.ValidateV7() != nil || in.SourceVersion < 1 || !crm.OpaqueReference(in.SourceReference) || !crm.Text(in.EvidenceReference, 5, 300) || in.State != "ACTIVE" && in.State != "RESTRICTED" && in.State != "REVOKED" || in.ExpiresAt.IsZero() || in.State == "ACTIVE" && in.ExpiresAt.After(now.Add(24*time.Hour)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid short-lived versioned source assertion required."))
		return
	}
	connection, e := h.loadChannelConnection(r.Context(), h.Auth.Service.Pool, in.OrganisationID, in.ConnectionID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if connection.PropertyID != in.PropertyID || connection.ProviderCode != distribution.Marketplace {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Source assertion connection mismatch."))
		return
	}
	if in.State == "ACTIVE" {
		if e = h.requireChannelCapability(r.Context(), h.Auth.Service.Pool, connection, "RESERVATION_INGEST"); e != nil {
			reply(w, r, 0, nil, e)
			return
		}
	}
	bound, e := idempotency.Fingerprint(r.Method, "crm:source-assertion", marshalCommercial(in))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	store := idempotency.NewStore(h.Auth.Service.Pool, 30*time.Second, 7*24*time.Hour)
	result, e := store.Execute(r.Context(), idempotency.Key{Scope: "business:" + in.OrganisationID.String() + ":crm:source-assertion", CallerType: idempotency.CallerService, CallerID: string(p.Service), Key: r.Header.Get("Idempotency-Key")}, bound, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, in.OrganisationID); e != nil {
			return idempotency.Result{}, e
		}
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','marketplace-service',true)`); e != nil {
			return idempotency.Result{}, e
		}
		connection, e := h.loadChannelConnection(ctx, tx, in.OrganisationID, in.ConnectionID)
		if e != nil {
			return idempotency.Result{}, e
		}
		if connection.PropertyID != in.PropertyID || connection.ProviderCode != distribution.Marketplace {
			return idempotency.Result{}, apperrors.PermissionDenied("Source assertion connection mismatch.")
		}
		if in.State == "ACTIVE" {
			if e = h.requireChannelCapability(ctx, tx, connection, "RESERVATION_INGEST"); e != nil {
				return idempotency.Result{}, e
			}
		}
		hash := h.crmSourceHash(in.OrganisationID, "MARKETPLACE", in.SourceReference)
		digest := hashCommercialPayload(marshalCommercial(in))
		var latest int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(source_version),0) FROM customer_accounts_projection WHERE organisation_id=$1 AND connection_id=$2 AND source_kind='MARKETPLACE' AND source_reference_hash=$3`, in.OrganisationID, in.ConnectionID, hash).Scan(&latest); e != nil {
			return idempotency.Result{}, e
		}
		if in.SourceVersion <= latest {
			var oldID ids.ID
			var oldDigest string
			e = tx.QueryRow(ctx, `SELECT id,assertion_digest FROM customer_accounts_projection WHERE organisation_id=$1 AND connection_id=$2 AND source_kind='MARKETPLACE' AND source_reference_hash=$3 AND source_version=$4`, in.OrganisationID, in.ConnectionID, hash, in.SourceVersion).Scan(&oldID, &oldDigest)
			if e == nil && oldDigest == digest {
				return idempotency.Result{Status: 200, Body: map[string]any{"data": map[string]any{"id": oldID, "source_version": in.SourceVersion, "state": in.State}}}, nil
			}
			return idempotency.Result{}, apperrors.Conflict("GUEST_SOURCE_VERSION", "Source assertion is stale or conflicts with retained evidence.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO customer_accounts_projection(id,organisation_id,property_id,connection_id,source_kind,source_service,source_reference_hash,source_version,assertion_digest,evidence_reference,expires_at,state) VALUES($1,$2,$3,$4,'MARKETPLACE','marketplace-service',$5,$6,$7,$8,$9,$10)`, id, in.OrganisationID, in.PropertyID, in.ConnectionID, hash, in.SourceVersion, digest, in.EvidenceReference, in.ExpiresAt, in.State); e != nil {
			return idempotency.Result{}, e
		}
		if e = h.commercialCommit(ctx, tx, tenant{Organisation: in.OrganisationID}, in.PropertyID, id, "customer_projection", "GUEST_SOURCE_ASSERTION_RECORDED", "guest.profile.updated.v1", in.SourceVersion, map[string]any{"connection_id": in.ConnectionID, "state": in.State, "expires_at": in.ExpiresAt}); e != nil {
			return idempotency.Result{}, e
		}
		return idempotency.Result{Status: 201, Body: map[string]any{"data": map[string]any{"id": id, "source_version": in.SourceVersion, "state": in.State}}}, nil
	})
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if result.Replayed {
		w.Header().Set(idempotency.HeaderReplayed, "true")
	}
	httpx.WriteRaw(w, result.Status, result.Body)
}
