package businesshttp

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type CommercialChannelConnection struct {
	AuthorizedCapabilities []string   `json:"authorized_capabilities"`
	ID                     ids.ID     `json:"id"`
	OrganisationID         ids.ID     `json:"organisation_id"`
	PropertyID             ids.ID     `json:"property_id"`
	ProviderID             ids.ID     `json:"provider_id"`
	ProviderCode           string     `json:"provider_code"`
	ProviderAccountID      string     `json:"provider_account_id"`
	State                  string     `json:"state"`
	Version                int64      `json:"version"`
	ValidatedAt            *time.Time `json:"validated_at,omitempty"`
	CredentialExpiresAt    *time.Time `json:"credential_expires_at,omitempty"`
	CredentialsConfigured  bool       `json:"credentials_configured"`
	CredentialReference    string     `json:"-"`
}

func (c CommercialChannelConnection) contract() distribution.Connection {
	return distribution.Connection{ID: c.ID, OrganisationID: c.OrganisationID, PropertyID: c.PropertyID, ProviderAccountID: c.ProviderAccountID, CredentialReference: c.CredentialReference}
}
func (h *Handler) loadChannelConnection(ctx context.Context, q database.Querier, org, id ids.ID) (CommercialChannelConnection, error) {
	var c CommercialChannelConnection
	e := q.QueryRow(ctx, `SELECT c.id,c.organisation_id,c.property_id,c.provider_id,p.code,c.provider_account_id,c.state,c.version,c.validated_at,c.credential_expires_at,c.credential_reference,c.authorized_capabilities FROM channel_connections c JOIN channel_providers p ON p.id=c.provider_id WHERE c.organisation_id=$1 AND c.id=$2`, org, id).Scan(&c.ID, &c.OrganisationID, &c.PropertyID, &c.ProviderID, &c.ProviderCode, &c.ProviderAccountID, &c.State, &c.Version, &c.ValidatedAt, &c.CredentialExpiresAt, &c.CredentialReference, &c.AuthorizedCapabilities)
	if errors.Is(e, pgx.ErrNoRows) {
		e = apperrors.NotFound("CHANNEL_CONNECTION_NOT_FOUND", "Channel connection not found.")
	}
	c.CredentialsConfigured = c.CredentialReference != ""
	return c, e
}
func (h *Handler) channelTarget(r *http.Request, cap string, mfa bool) (tenant, CommercialChannelConnection, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, CommercialChannelConnection{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, CommercialChannelConnection{}, e
	}
	c, e := h.loadChannelConnection(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		t, e = h.commercialScope(r, cap, c.PropertyID, mfa)
	}
	return t, c, e
}
func (h *Handler) requireChannelCapability(ctx context.Context, q database.Querier, c CommercialChannelConnection, cap string) error {
	if c.State != "CONNECTED" || c.ValidatedAt == nil || c.CredentialReference == "" || c.CredentialExpiresAt == nil || !h.Auth.Service.Now().Before(*c.CredentialExpiresAt) || h.ChannelAdapters[c.ProviderCode] == nil {
		return apperrors.Unavailable("A configured, authenticated active provider connection is required.")
	}
	if !slices.Contains(c.AuthorizedCapabilities, cap) {
		return apperrors.PermissionDenied("The provider credential does not authorize this connection operation.")
	}
	if _, disabled := h.ChannelAdapters[c.ProviderCode].(distribution.DisabledAdapter); disabled {
		return apperrors.Unavailable("The optional provider adapter is disabled until real access and certification are available.")
	}
	var certified bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channel_providers p JOIN channel_capabilities c ON c.provider_id=p.id WHERE p.id=$1 AND p.status='CERTIFIED' AND p.certification_reference IS NOT NULL AND c.capability=$2 AND c.status='CERTIFIED' AND c.evidence_reference IS NOT NULL)`, c.ProviderID, cap).Scan(&certified)
	if e != nil {
		return e
	}
	if !certified {
		return apperrors.Unavailable("Provider capability is not certified; configuration and operator evidence are required.")
	}
	return nil
}
func (h *Handler) registerDistribution(r chi.Router) {
	r.Put("/channels/{id}/webhook-key", h.channelWebhookKeySet)
	r.Get("/channels/providers", h.channelProviders)
	r.Get("/channels/connections", h.channelList)
	r.Post("/channels/connections", h.channelCreate)
	r.Get("/channels/{id}", h.channelGet)
	r.Patch("/channels/{id}", h.channelPatch)
	r.Post("/channels/{id}/authorize", h.channelAuthorize)
	r.Get("/channels/{id}/mappings", h.channelMappings)
	r.Post("/channels/{id}/mappings/preview", h.channelMappingPreview)
	r.Post("/channels/{id}/mappings", h.channelMappingConfirm)
	r.Get("/channels/{id}/field-authority", h.channelFieldList)
	r.Get("/channels/{id}/content-overrides", h.channelContentOverrides)
	r.Put("/channels/{id}/field-authority", h.channelFieldSet)
	r.Put("/channels/{id}/content-overrides/{listing_id}", h.channelContentOverride)
	r.Get("/channels/{id}/sync-health", h.channelHealth)
	r.Post("/channels/{id}/resync", h.channelResync)
	r.Get("/channels/{id}/sync-jobs", h.channelJobs)
	r.Get("/channels/{id}/sync-history", h.channelSyncHistory)
	r.Get("/channels/{id}/receipts", h.channelReceipts)
	r.Get("/channels/{id}/metrics", h.channelMetrics)
	r.Get("/channels/{id}/profitability", h.channelProfitability)
	r.Get("/channels/{id}/commission-rules", h.channelCommissionList)
	r.Post("/channels/{id}/commission-rules", h.channelCommissionCreate)
	r.Get("/channel-conflicts", h.channelConflicts)
	r.Get("/channel-conflicts/{id}", h.channelConflictGet)
	r.Post("/channel-conflicts/{id}/resolve", h.channelConflictResolve)
}
func (h *Handler) channelProviders(w http.ResponseWriter, r *http.Request) {
	_, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "channels:read", "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p)||jsonb_build_object('capabilities',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY capability),'[]') FROM channel_capabilities c WHERE c.provider_id=p.id)) FROM channel_providers p ORDER BY code`)
	reply(w, r, 200, map[string]any{"items": items, "certification_changes": "DEPLOYMENT_OPERATOR_ONLY"}, e)
}
func (h *Handler) channelList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "channel_connections", "channels:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return h.loadChannelConnection(ctx, h.Auth.Service.Pool, org, id)
	})
}
func (h *Handler) channelGet(w http.ResponseWriter, r *http.Request) {
	_, c, e := h.channelTarget(r, "channels:read", false)
	reply(w, r, 200, c, e)
}

type CommercialChannelCreate struct {
	PropertyID        ids.ID `json:"property_id"`
	ProviderCode      string `json:"provider_code"`
	ProviderAccountID string `json:"provider_account_id"`
	Reason            string `json:"reason"`
}

var channelOpaqueReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_:/.-]{0,299}$`)

func (h *Handler) channelCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialChannelCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !commercialCode.MatchString(in.ProviderCode) || !channelOpaqueReference.MatchString(in.ProviderAccountID) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Canonical property, registered provider/account reference and reason required."))
		return
	}
	h.execute(w, r, "commercial:channel:create", in, "channels:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "channels:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		node, e := portfolio.Get(ctx, tx, t.Organisation, in.PropertyID)
		if e != nil {
			return 0, nil, e
		}
		if node.Kind != portfolio.Property {
			return 0, nil, apperrors.InvalidRequest("Channel connections require an existing canonical property.")
		}
		var provider ids.ID
		var state string
		if e = tx.QueryRow(ctx, `SELECT id,status FROM channel_providers WHERE code=$1`, in.ProviderCode).Scan(&provider, &state); e != nil {
			return 0, nil, apperrors.InvalidRequest("Unknown provider registry entry.")
		}
		if state == "DISABLED" || state == "SUSPENDED" {
			return 0, nil, apperrors.Unavailable("Provider is disabled or suspended.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO channel_connections(id,organisation_id,property_id,provider_id,provider_account_id,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, t.Organisation, in.PropertyID, provider, in.ProviderAccountID, t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO channel_sync_cursors(organisation_id,connection_id) VALUES($1,$2)`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		c, e := h.loadChannelConnection(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "channel_connection", "CHANNEL_CONNECTION_CREATED", businessEvents.ChannelConnectionChanged, 1, c)
		return 201, c, e
	})
}

type CommercialChannelAuthorize struct {
	Version             int64  `json:"version"`
	CredentialReference string `json:"credential_reference"`
	Reason              string `json:"reason"`
	ApprovalID          ids.ID `json:"approval_id"`
}

func (h *Handler) channelAuthorize(w http.ResponseWriter, r *http.Request) {
	var in CommercialChannelAuthorize
	if !decode(w, r, &in) {
		return
	}
	t, c, e := h.channelTarget(r, "channels:credentials", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version != c.Version || !channelOpaqueReference.MatchString(in.CredentialReference) || stringsCommercialURL(in.CredentialReference) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current connection version, opaque provider/vault reference and reason required. URLs and raw credentials are not accepted."))
		return
	}
	adapter := h.ChannelAdapters[c.ProviderCode]
	if adapter == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Provider adapter is not configured or certified."))
		return
	}
	var certified bool
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT status='CERTIFIED' AND certification_reference IS NOT NULL FROM channel_providers WHERE id=$1`, c.ProviderID).Scan(&certified); e != nil || !certified {
		reply(w, r, 0, nil, apperrors.Unavailable("Provider certification evidence is required before authorization."))
		return
	}
	c.CredentialReference = in.CredentialReference
	authorization, e := adapter.Validate(r.Context(), c.contract())
	if e != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Provider authorization could not be validated; connection remains unchanged."))
		return
	}
	now := h.Auth.Service.Now().UTC()
	if authorization.ProviderAccountID != c.ProviderAccountID || authorization.PropertyID != c.PropertyID || authorization.Reference == "" || len(authorization.Reference) > 500 || !now.Before(authorization.ExpiresAt) || authorization.ExpiresAt.After(now.Add(90*24*time.Hour)) || !slices.Contains(authorization.Capabilities, "ARI") || len(authorization.Capabilities) > 32 {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Provider proof does not authorize this account/property or a bounded ARI credential lifetime."))
		return
	}
	h.execute(w, r, "commercial:channel:authorize:"+c.ID.String(), in, "channels:credentials", "PROPERTY", c.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "channels:credentials", "PROPERTY", c.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := h.loadChannelConnection(ctx, tx, t.Organisation, c.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Connection changed during external authorization.")
		}
		if e = h.commercialGate(ctx, tx, t, c.PropertyID); e != nil {
			return 0, nil, e
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CHANNEL_CHANGE", target.Scope, in.ApprovalID, map[string]any{"connection_id": c.ID, "command": command}, c.ProviderCode, nil, ""); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE channel_connections SET state='CONNECTED',credential_reference=$3,credential_expires_at=$4,validation_reference=$5,validated_at=$6,authorized_capabilities=$7,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, c.ID, in.CredentialReference, authorization.ExpiresAt, authorization.Reference, now, authorization.Capabilities); e != nil {
			return 0, nil, e
		}
		value, e := h.loadChannelConnection(ctx, tx, t.Organisation, c.ID)
		if e != nil {
			return 0, nil, e
		}
		if e = h.requireChannelCapability(ctx, tx, value, "ARI"); e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, c.ID, "channel_connection", "CHANNEL_AUTHORIZATION_VALIDATED", businessEvents.ChannelConnectionChanged, value.Version, map[string]any{"connection": value, "reason": in.Reason})
		return 200, value, e
	})
	_ = t
}
func stringsCommercialURL(s string) bool {
	return len(s) > 6 && (s[:7] == "http://" || len(s) > 7 && s[:8] == "https://")
}
func (h *Handler) channelPatch(w http.ResponseWriter, r *http.Request) {
	var in CommercialStateChange
	if !decode(w, r, &in) {
		return
	}
	_, c, e := h.channelTarget(r, "channels:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.State != "SUSPENDED" && in.State != "DISCONNECTED" || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Suspend or disconnect with a written reason; activation uses authenticated provider authorization."))
		return
	}
	h.execute(w, r, "commercial:channel:state:"+c.ID.String(), in, "channels:write", "PROPERTY", c.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "channels:write", "PROPERTY", c.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := h.loadChannelConnection(ctx, tx, t.Organisation, c.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Connection version changed.")
		}
		var active bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM listing_publication WHERE organisation_id=$1 AND connection_id=$2 AND (desired_state='PUBLISHED' OR delivery_state<>'CONFIRMED'))`, t.Organisation, c.ID).Scan(&active); e != nil {
			return 0, nil, e
		}
		if in.State == "DISCONNECTED" && active {
			return 0, nil, apperrors.Conflict("PROVIDER_STOP_NOT_CONFIRMED", "Provider pause/withdrawal acknowledgement and readback are required before disconnecting credentials.")
		}
		if _, e = tx.Exec(ctx, `UPDATE channel_connections SET state=$3,credential_reference=CASE WHEN $3='DISCONNECTED' THEN '' ELSE credential_reference END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, c.ID, in.State); e != nil {
			return 0, nil, e
		}
		value, e := h.loadChannelConnection(ctx, tx, t.Organisation, c.ID)
		if e != nil {
			return 0, nil, e
		}
		if active {
			if e = h.recordChannelConflict(ctx, tx, t.Organisation, c.PropertyID, c.ID, ids.Nil, "CONNECTION_SUSPENDED_WHILE_PROVIDER_MAY_BE_ACTIVE", "CRITICAL", "CONNECTION", map[string]any{"reason": in.Reason}, "RESTORE_CREDENTIALS_AND_CONFIRM_PROVIDER_PAUSE"); e != nil {
				return 0, nil, e
			}
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, c.ID, "channel_connection", "CHANNEL_CONNECTION_STATE_CHANGED", businessEvents.ChannelConnectionChanged, value.Version, map[string]any{"connection": value, "provider_sellability_confirmed": !active, "reason": in.Reason})
		return 200, value, e
	})
}

func (h *Handler) requireConfirmedMappings(ctx context.Context, q database.Querier, c CommercialChannelConnection, p inventory.Product) error {
	var complete bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channel_property_mappings WHERE organisation_id=$1 AND connection_id=$2 AND property_id=$3 AND state='CONFIRMED') AND EXISTS(SELECT 1 FROM channel_product_mappings WHERE organisation_id=$1 AND connection_id=$2 AND product_id=$4 AND state='CONFIRMED') AND EXISTS(SELECT 1 FROM channel_rate_mappings WHERE organisation_id=$1 AND connection_id=$2 AND rate_plan_id=$5 AND state='CONFIRMED')`, c.OrganisationID, c.ID, c.PropertyID, p.ID, p.RatePlanID).Scan(&complete)
	if e != nil {
		return e
	}
	if !complete {
		return apperrors.Conflict("CHANNEL_MAPPING_REQUIRED", "Confirmed canonical property, product and rate mappings are required.")
	}
	return nil
}
