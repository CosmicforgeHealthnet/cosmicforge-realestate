package businesshttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type CommercialMappingPreview struct {
	Version int64                           `json:"version"`
	Entries []distribution.MappingReference `json:"entries"`
	Reason  string                          `json:"reason"`
}
type CommercialStoredMappingPreview struct {
	ID                ids.ID                          `json:"id"`
	ConnectionID      ids.ID                          `json:"connection_id"`
	ConnectionVersion int64                           `json:"connection_version"`
	Entries           []distribution.MappingReference `json:"entries"`
	Proofs            []distribution.MappingProof     `json:"proofs"`
	Digest            string                          `json:"digest"`
	CreatedBy         ids.ID                          `json:"created_by"`
	ExpiresAt         time.Time                       `json:"expires_at"`
}

func (h *Handler) channelMappings(w http.ResponseWriter, r *http.Request) {
	t, c, e := h.channelTarget(r, "channels:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	result := map[string]any{}
	for kind, table := range commercialMappingTables() {
		values, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(m) FROM `+table+` m WHERE organisation_id=$1 AND connection_id=$2 ORDER BY id`, t.Organisation, c.ID)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		result[kind] = values
	}
	reply(w, r, 200, result, nil)
}
func commercialMappingTables() map[string]string {
	return map[string]string{"PROPERTY": "channel_property_mappings", "PRODUCT": "channel_product_mappings", "RATE_PLAN": "channel_rate_mappings"}
}
func mappingColumn(kind string) string {
	switch kind {
	case "PROPERTY":
		return "property_id"
	case "PRODUCT":
		return "product_id"
	case "RATE_PLAN":
		return "rate_plan_id"
	}
	return ""
}
func (h *Handler) validateMappingCanonical(ctx context.Context, q database.Querier, c CommercialChannelConnection, v distribution.MappingReference) error {
	if v.CanonicalID.ValidateV7() != nil || v.CanonicalVersion < 1 || v.ExpectedMappingVersion < 0 || len(v.ExternalID) < 1 || len(v.ExternalID) > 200 || commercialMappingTables()[v.Kind] == "" {
		return apperrors.InvalidRequest("Valid canonical mapping types, IDs, versions and external IDs required.")
	}
	var version int64
	switch v.Kind {
	case "PROPERTY":
		if v.CanonicalID != c.PropertyID {
			return apperrors.PermissionDenied("Property mapping must match its owning connection.")
		}
		n, e := portfolio.Get(ctx, q, c.OrganisationID, v.CanonicalID)
		if e != nil {
			return e
		}
		version = n.Version
	case "PRODUCT":
		p, e := inventory.GetProduct(ctx, q, c.OrganisationID, v.CanonicalID)
		if e != nil {
			return e
		}
		if p.PropertyID != c.PropertyID {
			return apperrors.PermissionDenied("Product mapping cannot cross canonical properties.")
		}
		version = p.Version
	case "RATE_PLAN":
		p, e := revenue.GetPlan(ctx, q, c.OrganisationID, v.CanonicalID)
		if e != nil {
			return e
		}
		if p.PropertyID != c.PropertyID {
			return apperrors.PermissionDenied("Rate mapping cannot cross canonical properties.")
		}
		version = p.Version
	}
	if version != v.CanonicalVersion {
		return apperrors.Conflict("MAPPING_CANONICAL_VERSION_CHANGED", "Canonical object changed after mapping review.")
	}
	return nil
}
func (h *Handler) channelMappingPreview(w http.ResponseWriter, r *http.Request) {
	var in CommercialMappingPreview
	if !decode(w, r, &in) {
		return
	}
	_, c, e := h.channelTarget(r, "channels:mappings", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version != c.Version || len(in.Entries) < 1 || len(in.Entries) > 100 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current connection, 1–100 mapping entries and reason required."))
		return
	}
	if e = h.requireChannelMappingCapabilities(r.Context(), h.Auth.Service.Pool, c, in.Entries); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	proofs := []distribution.MappingProof{}
	seen := map[string]bool{}
	for _, entry := range in.Entries {
		if e = h.validateMappingCanonical(r.Context(), h.Auth.Service.Pool, c, entry); e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		key := entry.Kind + ":" + entry.CanonicalID.String()
		external := entry.Kind + ":external:" + entry.ExternalID
		if seen[key] || seen[external] {
			reply(w, r, 0, nil, apperrors.InvalidRequest("A preview cannot contain duplicate canonical or external object mappings."))
			return
		}
		seen[key] = true
		seen[external] = true
		proof, e := h.ChannelAdapters[c.ProviderCode].InspectObject(r.Context(), c.contract(), entry)
		if e != nil {
			reply(w, r, 0, nil, apperrors.Unavailable("Authoritative provider mapping inspection unavailable."))
			return
		}
		if proof.Kind != entry.Kind || proof.ExternalID != entry.ExternalID || proof.ProviderAccountID != c.ProviderAccountID || proof.Reference == "" || len(proof.Reference) > 500 {
			reply(w, r, 0, nil, apperrors.PermissionDenied("Provider mapping proof does not match this account/object."))
			return
		}
		proofs = append(proofs, proof)
	}
	h.execute(w, r, "commercial:channel:mapping-preview:"+c.ID.String(), in, "channels:mappings", "PROPERTY", c.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "channels:mappings", "PROPERTY", c.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := h.loadChannelConnection(ctx, tx, t.Organisation, c.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Connection changed during provider inspection.")
		}
		propertyExternal := ""
		for _, entry := range in.Entries {
			if entry.Kind == "PROPERTY" {
				propertyExternal = entry.ExternalID
			}
		}
		if propertyExternal == "" {
			e = tx.QueryRow(ctx, `SELECT external_id FROM channel_property_mappings WHERE organisation_id=$1 AND connection_id=$2 AND state='CONFIRMED'`, t.Organisation, c.ID).Scan(&propertyExternal)
			if e != nil {
				return 0, nil, apperrors.Conflict("PROPERTY_MAPPING_REQUIRED", "Preview a property mapping first or in this same command.")
			}
		}
		for i, entry := range in.Entries {
			if e = h.validateMappingCanonical(ctx, tx, current, entry); e != nil {
				return 0, nil, e
			}
			if entry.Kind != "PROPERTY" && proofs[i].PropertyExternalID != propertyExternal {
				return 0, nil, apperrors.PermissionDenied("Provider object belongs to a different external property.")
			}
			var actual int64
			if e = h.checkMappingCollision(ctx, tx, current, entry); e != nil {
				return 0, nil, e
			}
			table := commercialMappingTables()[entry.Kind]
			column := mappingColumn(entry.Kind)
			e = tx.QueryRow(ctx, `SELECT version FROM `+table+` WHERE organisation_id=$1 AND connection_id=$2 AND `+column+`=$3`, t.Organisation, c.ID, entry.CanonicalID).Scan(&actual)
			if errors.Is(e, pgx.ErrNoRows) {
				actual = 0
			} else if e != nil {
				return 0, nil, e
			}
			if actual != entry.ExpectedMappingVersion {
				return 0, nil, apperrors.Conflict("MAPPING_VERSION_CHANGED", "Existing mapping version differs from the reviewed command.")
			}
		}
		raw, _ := json.Marshal(in.Entries)
		proofRaw, _ := json.Marshal(proofs)
		digestInput, _ := json.Marshal(map[string]any{"connection_id": c.ID, "connection_version": in.Version, "entries": in.Entries, "proofs": proofs})
		sum := sha256.Sum256(digestInput)
		digest := hex.EncodeToString(sum[:])
		id := ids.New()
		expiry := h.Auth.Service.Now().UTC().Add(10 * time.Minute)
		if _, e = tx.Exec(ctx, `INSERT INTO channel_mapping_previews(id,organisation_id,property_id,connection_id,connection_version,entries,proofs,digest,created_by,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, t.Organisation, c.PropertyID, c.ID, c.Version, raw, proofRaw, digest, t.Account, expiry); e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, id, "channel_mapping_preview", "CHANNEL_MAPPING_PREVIEWED", "", 1, map[string]any{"connection_id": c.ID, "digest": digest, "reason": in.Reason})
		return 201, map[string]any{"preview_id": id, "connection_version": c.Version, "digest": digest, "entries": in.Entries, "expires_at": expiry}, e
	})
}

type CommercialMappingConfirm struct {
	PreviewID  ids.ID `json:"preview_id"`
	Digest     string `json:"digest"`
	Version    int64  `json:"version"`
	Reason     string `json:"reason"`
	ApprovalID ids.ID `json:"approval_id"`
}

func (h *Handler) channelMappingConfirm(w http.ResponseWriter, r *http.Request) {
	var in CommercialMappingConfirm
	if !decode(w, r, &in) {
		return
	}
	_, c, e := h.channelTarget(r, "channels:mappings", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.PreviewID.ValidateV7() != nil || len(in.Digest) != 64 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Immutable preview/digest, current connection and reason required."))
		return
	}
	h.execute(w, r, "commercial:channel:mapping-confirm:"+c.ID.String(), in, "channels:mappings", "PROPERTY", c.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "channels:mappings", "PROPERTY", c.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := h.loadChannelConnection(ctx, tx, t.Organisation, c.ID)
		if e != nil {
			return 0, nil, e
		}
		preview, e := typedJSON[CommercialStoredMappingPreview](ctx, tx, `SELECT to_jsonb(p) FROM channel_mapping_previews p WHERE organisation_id=$1 AND id=$2`, t.Organisation, in.PreviewID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version || preview.ConnectionVersion != in.Version || preview.ConnectionID != c.ID || preview.CreatedBy != t.Account || preview.Digest != in.Digest || !h.Auth.Service.Now().Before(preview.ExpiresAt) {
			return 0, nil, apperrors.Conflict("MAPPING_PREVIEW_STALE", "Mapping preview scope, actor, versions, digest or lifetime changed.")
		}
		if e = h.requireChannelMappingCapabilities(ctx, tx, current, preview.Entries); e != nil {
			return 0, nil, e
		}
		remap := false
		for _, entry := range preview.Entries {
			remap = remap || entry.ExpectedMappingVersion > 0
		}
		if remap {
			var busy bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM listing_publication WHERE organisation_id=$1 AND connection_id=$2 AND (desired_state='PUBLISHED' OR delivery_state<>'CONFIRMED')) OR EXISTS(SELECT 1 FROM inventory_holds WHERE organisation_id=$1 AND property_id=$3 AND channel=$4 AND state='ACTIVE' AND expires_at>now())`, t.Organisation, c.ID, c.PropertyID, c.ProviderCode).Scan(&busy); e != nil {
				return 0, nil, e
			}
			if busy {
				return 0, nil, apperrors.Conflict("CHANNEL_REMAP_BUSY", "Confirm provider pause/withdrawal and release channel holds before remapping.")
			}
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CHANNEL_MAPPING_CHANGE", target.Scope, in.ApprovalID, map[string]any{"connection_id": c.ID, "command": command}, c.ProviderCode, nil, ""); e != nil {
			return 0, nil, e
		}
		for i, entry := range preview.Entries {
			if e = h.validateMappingCanonical(ctx, tx, current, entry); e != nil {
				return 0, nil, e
			}
			if e = h.checkMappingCollision(ctx, tx, current, entry); e != nil {
				return 0, nil, e
			}
			table := commercialMappingTables()[entry.Kind]
			column := mappingColumn(entry.Kind)
			var existing ids.ID
			var version int64
			e = tx.QueryRow(ctx, `SELECT id,version FROM `+table+` WHERE organisation_id=$1 AND connection_id=$2 AND `+column+`=$3 FOR UPDATE`, t.Organisation, c.ID, entry.CanonicalID).Scan(&existing, &version)
			if errors.Is(e, pgx.ErrNoRows) {
				if entry.ExpectedMappingVersion != 0 {
					return 0, nil, apperrors.Conflict("MAPPING_VERSION_CHANGED", "Previous mapping no longer exists.")
				}
				id := ids.New()
				if entry.Kind == "PROPERTY" {
					_, e = tx.Exec(ctx, `INSERT INTO channel_property_mappings(id,organisation_id,connection_id,property_id,external_id,state,provenance,validated_at) VALUES($1,$2,$3,$4,$5,'CONFIRMED',$6,$7)`, id, t.Organisation, c.ID, c.PropertyID, entry.ExternalID, preview.Proofs[i].Reference, h.Auth.Service.Now().UTC())
				} else {
					_, e = tx.Exec(ctx, `INSERT INTO `+table+`(id,organisation_id,connection_id,property_id,`+column+`,external_id,state,provenance,validated_at) VALUES($1,$2,$3,$4,$5,$6,'CONFIRMED',$7,$8)`, id, t.Organisation, c.ID, c.PropertyID, entry.CanonicalID, entry.ExternalID, preview.Proofs[i].Reference, h.Auth.Service.Now().UTC())
				}
			} else if e == nil {
				if version != entry.ExpectedMappingVersion {
					return 0, nil, apperrors.Conflict("MAPPING_VERSION_CHANGED", "Canonical mapping changed since preview.")
				}
				_, e = tx.Exec(ctx, `UPDATE `+table+` SET external_id=$3,state='CONFIRMED',provenance=$4,validated_at=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, existing, entry.ExternalID, preview.Proofs[i].Reference, h.Auth.Service.Now().UTC())
			}
			if e != nil {
				return 0, nil, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE channel_connections SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, c.ID); e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, c.ID, "channel_connection", "CHANNEL_MAPPINGS_CONFIRMED", businessEvents.ChannelConnectionChanged, current.Version+1, map[string]any{"preview_id": preview.ID, "digest": preview.Digest, "approval_id": in.ApprovalID, "entries": preview.Entries, "reason": in.Reason})
		return 200, map[string]any{"connection_id": c.ID, "version": current.Version + 1, "mapping_state": "CONFIRMED", "entries": preview.Entries}, e
	})
}

type CommercialFieldAuthority struct {
	Field           string          `json:"field"`
	Mode            string          `json:"mode"`
	Value           json.RawMessage `json:"value,omitempty"`
	Reason          string          `json:"reason"`
	SourceReference string          `json:"source_reference"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	Version         int64           `json:"version"`
	ApprovalID      ids.ID          `json:"approval_id"`
}

func (h *Handler) channelFieldList(w http.ResponseWriter, r *http.Request) {
	t, c, e := h.channelTarget(r, "channels:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(f)-'edited_by' FROM channel_field_authority f WHERE organisation_id=$1 AND connection_id=$2 ORDER BY field`, t.Organisation, c.ID)
	reply(w, r, 200, map[string]any{"items": items, "default_authority": "INTERNAL", "expired_overrides": "RECONCILIATION_REQUIRED"}, e)
}
func (h *Handler) channelContentOverrides(w http.ResponseWriter, r *http.Request) {
	t, c, e := h.channelTarget(r, "channels:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(o)-'edited_by' FROM channel_content_overrides o WHERE organisation_id=$1 AND connection_id=$2 ORDER BY updated_at DESC,listing_id LIMIT $3 OFFSET $4`, t.Organisation, c.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func validCommercialField(in CommercialFieldAuthority) bool {
	if !slices.Contains([]string{"title", "description", "amenities", "house_rules", "stop_sell", "minimum_nights"}, in.Field) || in.Mode != "INTERNAL" && in.Mode != "CHANNEL_OVERRIDE" && in.Mode != "MANUAL_OVERRIDE" || in.Version < 0 || !validStaffReason(in.Reason) || len(in.SourceReference) < 1 || len(in.SourceReference) > 500 {
		return false
	}
	if in.Mode != "MANUAL_OVERRIDE" {
		return len(in.Value) == 0 || string(in.Value) == "null"
	}
	if in.ExpiresAt == nil {
		return false
	}
	switch in.Field {
	case "title", "description":
		var value string
		return json.Unmarshal(in.Value, &value) == nil && len(value) > 0 && len(value) <= 20000
	case "amenities", "house_rules":
		var value []string
		if json.Unmarshal(in.Value, &value) != nil || len(value) > 100 {
			return false
		}
		for _, v := range value {
			if len(v) < 1 || len(v) > 500 {
				return false
			}
		}
		return true
	case "rate_adjustment":
		var a revenue.Adjustment
		return json.Unmarshal(in.Value, &a) == nil && a.Validate() == nil
	case "stop_sell":
		return string(in.Value) == "true"
	case "minimum_nights":
		var nights int
		return json.Unmarshal(in.Value, &nights) == nil && nights >= 1 && nights <= 366
	}
	return false
}
func (h *Handler) channelFieldSet(w http.ResponseWriter, r *http.Request) {
	var in CommercialFieldAuthority
	if !decode(w, r, &in) {
		return
	}
	_, c, e := h.channelTarget(r, "channels:override", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !validCommercialField(in) || in.ExpiresAt != nil && (!h.Auth.Service.Now().Before(*in.ExpiresAt) || in.ExpiresAt.After(h.Auth.Service.Now().Add(30*24*time.Hour))) || in.Mode == "CHANNEL_OVERRIDE" && (in.Field == "rate_adjustment" || in.Field == "minimum_nights" || in.Field == "stop_sell") {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Only bounded content overrides or stricter sale controls are allowed; canonical ownership, capacity and compliance cannot be overridden."))
		return
	}
	h.execute(w, r, "commercial:channel:authority:"+c.ID.String(), in, "channels:override", "PROPERTY", c.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "channels:override", "PROPERTY", c.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		command := in
		command.ApprovalID = ids.Nil
		var amount *int64
		currency := ""
		if in.Field == "rate_adjustment" {
			return 0, nil, apperrors.InvalidRequest("Rate overrides use approved canonical rate-plan versions; field authority cannot create an unpriced channel rate.")
		}
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CHANNEL_FIELD_OVERRIDE", target.Scope, in.ApprovalID, map[string]any{"connection_id": c.ID, "command": command}, c.ProviderCode, amount, currency); e != nil {
			return 0, nil, e
		}
		var version int64
		e = tx.QueryRow(ctx, `SELECT version FROM channel_field_authority WHERE organisation_id=$1 AND connection_id=$2 AND field=$3 FOR UPDATE`, t.Organisation, c.ID, in.Field).Scan(&version)
		if errors.Is(e, pgx.ErrNoRows) {
			if in.Version != 0 {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Field authority does not exist.")
			}
			version = 1
			_, e = tx.Exec(ctx, `INSERT INTO channel_field_authority(organisation_id,connection_id,field,mode,value,reason,source_reference,expires_at,edited_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, t.Organisation, c.ID, in.Field, in.Mode, nullableCommercialJSON(in.Value), in.Reason, in.SourceReference, in.ExpiresAt, t.Account)
		} else if e == nil {
			if version != in.Version {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Field authority changed.")
			}
			version++
			_, e = tx.Exec(ctx, `UPDATE channel_field_authority SET mode=$4,value=$5,reason=$6,source_reference=$7,expires_at=$8,edited_by=$9,version=version+1,updated_at=now() WHERE organisation_id=$1 AND connection_id=$2 AND field=$3`, t.Organisation, c.ID, in.Field, in.Mode, nullableCommercialJSON(in.Value), in.Reason, in.SourceReference, in.ExpiresAt, t.Account)
		}
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, c.ID, "channel_field_authority", "CHANNEL_FIELD_AUTHORITY_CHANGED", businessEvents.ChannelConnectionChanged, version, map[string]any{"field": in.Field, "mode": in.Mode, "value": in.Value, "version": version, "expires_at": in.ExpiresAt, "source_reference": in.SourceReference, "reason": in.Reason})
		return 200, map[string]any{"field": in.Field, "mode": in.Mode, "version": version, "resync_required": true}, e
	})
}
func nullableCommercialJSON(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return []byte(raw)
}

type CommercialContentOverride struct {
	Version    int64             `json:"version"`
	Content    CommercialContent `json:"content"`
	ExpiresAt  time.Time         `json:"expires_at"`
	Reason     string            `json:"reason"`
	ApprovalID ids.ID            `json:"approval_id"`
}

func (h *Handler) channelContentOverride(w http.ResponseWriter, r *http.Request) {
	var in CommercialContentOverride
	if !decode(w, r, &in) {
		return
	}
	_, c, e := h.channelTarget(r, "channels:override", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	listingID, e := ids.ParseV7(chi.URLParam(r, "listing_id"))
	if e != nil || in.Content.validate() != nil || in.Version < 0 || !validStaffReason(in.Reason) || !h.Auth.Service.Now().Before(in.ExpiresAt) || in.ExpiresAt.After(h.Auth.Service.Now().Add(30*24*time.Hour)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid listing/content, bounded override lifetime and reason required."))
		return
	}
	h.execute(w, r, "commercial:channel:content-override:"+listingID.String(), in, "channels:override", "PROPERTY", c.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "channels:override", "PROPERTY", c.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		l, e := loadListing(ctx, tx, t.Organisation, listingID)
		if e != nil {
			return 0, nil, e
		}
		v, e := latestListingVersion(ctx, tx, t.Organisation, listingID)
		if e != nil {
			return 0, nil, e
		}
		if l.PropertyID != c.PropertyID || !slices.Equal(in.Content.MediaLinkIDs, v.Content.MediaLinkIDs) {
			return 0, nil, apperrors.PermissionDenied("Overrides cannot alter canonical identity or bypass approved Media Service references.")
		}
		score, issues := in.Content.quality()
		if score < 100 || len(issues) > 0 {
			return 0, nil, apperrors.Conflict("CONTENT_NOT_READY", "An override cannot reduce publication content quality.")
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CHANNEL_FIELD_OVERRIDE", target.Scope, in.ApprovalID, map[string]any{"connection_id": c.ID, "listing_id": listingID, "command": command}, c.ProviderCode, nil, ""); e != nil {
			return 0, nil, e
		}
		var version int64
		e = tx.QueryRow(ctx, `SELECT version FROM channel_content_overrides WHERE organisation_id=$1 AND connection_id=$2 AND listing_id=$3 FOR UPDATE`, t.Organisation, c.ID, listingID).Scan(&version)
		raw, _ := json.Marshal(in.Content)
		if errors.Is(e, pgx.ErrNoRows) {
			if in.Version != 0 {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Override does not exist.")
			}
			version = 1
			_, e = tx.Exec(ctx, `INSERT INTO channel_content_overrides(organisation_id,connection_id,listing_id,content,reason,expires_at,edited_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, t.Organisation, c.ID, listingID, raw, in.Reason, in.ExpiresAt, t.Account)
		} else if e == nil {
			if version != in.Version {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Override changed.")
			}
			version++
			_, e = tx.Exec(ctx, `UPDATE channel_content_overrides SET content=$4,reason=$5,expires_at=$6,edited_by=$7,version=version+1,updated_at=now() WHERE organisation_id=$1 AND connection_id=$2 AND listing_id=$3`, t.Organisation, c.ID, listingID, raw, in.Reason, in.ExpiresAt, t.Account)
		}
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, listingID, "channel_content_override", "CHANNEL_CONTENT_OVERRIDE_CHANGED", businessEvents.ChannelConnectionChanged, version, map[string]any{"connection_id": c.ID, "listing_id": listingID, "version": version, "expires_at": in.ExpiresAt, "reason": in.Reason})
		return 200, map[string]any{"version": version, "resync_required": true}, e
	})
}

func (h *Handler) requireChannelMappingCapabilities(ctx context.Context, q database.Querier, c CommercialChannelConnection, entries []distribution.MappingReference) error {
	for _, entry := range entries {
		capability := ""
		switch entry.Kind {
		case "PROPERTY":
			capability = "PROPERTY_MAPPING"
		case "PRODUCT":
			capability = "PRODUCT_MAPPING"
		case "RATE_PLAN":
			capability = "RATE_MAPPING"
		default:
			return apperrors.InvalidRequest("Unsupported canonical mapping kind.")
		}
		if e := h.requireChannelCapability(ctx, q, c, capability); e != nil {
			return e
		}
	}
	return nil
}

func (h *Handler) checkMappingCollision(ctx context.Context, q database.Querier, c CommercialChannelConnection, entry distribution.MappingReference) error {
	table, ok := commercialMappingTables()[entry.Kind]
	if !ok {
		return apperrors.InvalidRequest("Unsupported mapping kind.")
	}
	column := mappingColumn(entry.Kind)
	var collision bool
	if e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` m JOIN channel_connections c ON c.organisation_id=m.organisation_id AND c.id=m.connection_id WHERE m.organisation_id=$1 AND c.provider_id=$2 AND c.provider_account_id=$3 AND m.external_id=$4 AND m.`+column+`<>$5 AND m.state='CONFIRMED')`, c.OrganisationID, c.ProviderID, c.ProviderAccountID, entry.ExternalID, entry.CanonicalID).Scan(&collision); e != nil {
		return e
	}
	if collision {
		return apperrors.Conflict("CHANNEL_MAPPING_COLLISION", "The provider object is already bound to another canonical object in this organisation.")
	}
	return nil
}
