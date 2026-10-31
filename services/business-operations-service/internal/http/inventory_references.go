package businesshttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type InventoryMapping struct {
	NodeVersion         int64  `json:"node_version,omitempty"`
	TreeVersion         int64  `json:"tree_version,omitempty"`
	PreviousNodeVersion int64  `json:"previous_node_version,omitempty"`
	PreviousTreeVersion int64  `json:"previous_tree_version,omitempty"`
	Provider            string `json:"provider"`
	ExternalID          string `json:"external_id"`
	SourceReference     string `json:"source_reference"`
	ExpectedNodeID      ids.ID `json:"expected_node_id,omitempty"`
	Version             int64  `json:"version,omitempty"`
	Reason              string `json:"reason"`
	ApprovalID          ids.ID `json:"approval_id,omitempty"`
}
type InventoryVerification struct {
	Version        int64  `json:"version"`
	VerificationID ids.ID `json:"verification_id,omitempty"`
	CreateCase     bool   `json:"create_case"`
}
type InventoryMedia struct {
	Version int64  `json:"version"`
	MediaID ids.ID `json:"media_id"`
	Purpose string `json:"purpose"`
	Reason  string `json:"reason"`
}
type InventorySpace struct {
	NodeID ids.ID      `json:"node_id"`
	Label  string      `json:"label"`
	Points [][]float64 `json:"points"`
}
type SpaceGraphCommand struct {
	Version          int64            `json:"version"`
	MediaLinkID      ids.ID           `json:"media_link_id"`
	CoordinateSystem string           `json:"coordinate_system"`
	Spaces           []InventorySpace `json:"spaces"`
	Confirmed        bool             `json:"confirmed"`
	Reason           string           `json:"reason"`
}

func (h *Handler) registerInventoryReferences(r chi.Router) {
	r.Get("/inventory-nodes/{id}/mappings", func(w http.ResponseWriter, r *http.Request) { h.inventoryReferenceList(w, r, "mappings") })
	r.Post("/inventory-nodes/{id}/mappings", h.inventoryMap)
	r.Get("/inventory-nodes/{id}/verification", h.inventoryVerificationGet)
	r.Post("/inventory-nodes/{id}/verification", h.inventoryVerificationLink)
	r.Get("/inventory-nodes/{id}/media", func(w http.ResponseWriter, r *http.Request) { h.inventoryReferenceList(w, r, "media") })
	r.Post("/inventory-nodes/{id}/media", h.inventoryMediaLink)
	r.Post("/inventory-nodes/{id}/media/{link_id}/access", h.inventoryMediaAccess)
	r.Get("/inventory-nodes/{id}/space-graphs", func(w http.ResponseWriter, r *http.Request) { h.inventoryReferenceList(w, r, "spaces") })
	r.Post("/inventory-nodes/{id}/space-graphs", h.inventorySpaceGraph)
}
func (h *Handler) inventoryReferenceList(w http.ResponseWriter, r *http.Request, kind string) {
	t, n, err := h.inventoryTarget(r, "structure:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	queries := map[string]string{
		"mappings": `SELECT jsonb_build_object('mapping_id',id,'object_type',object_type,'provider',provider,'external_id',external_id,'source_reference',source_reference,'version',version,'created_at',created_at,'updated_at',updated_at) FROM inventory_external_mappings WHERE organisation_id=$1 AND node_id=$2 ORDER BY provider,external_id,id LIMIT $3 OFFSET $4`,
		"media":    `SELECT jsonb_build_object('media_link_id',id,'media_id',media_id,'purpose',purpose,'provider_status',provider_status,'access_classification',access_classification,'checked_at',provider_checked_at,'version',version) FROM inventory_media_links WHERE organisation_id=$1 AND node_id=$2 ORDER BY created_at,id LIMIT $3 OFFSET $4`,
		"spaces":   `SELECT jsonb_build_object('space_graph_id',id,'media_link_id',media_link_id,'coordinate_system',coordinate_system,'spaces',spaces,'confirmed_by',confirmed_by,'version',version,'created_at',created_at) FROM space_graphs WHERE organisation_id=$1 AND node_id=$2 ORDER BY version DESC,id DESC LIMIT $3 OFFSET $4`}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), queries[kind], t.Organisation, n.ID, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		items = append(items, raw)
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, rows.Err())
}
func (h *Handler) inventoryMap(w http.ResponseWriter, r *http.Request) {
	var in InventoryMapping
	if !decode(w, r, &in) {
		return
	}
	t, n, err := h.inventoryTarget(r, "inventory:mappings", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if !inventoryProvider(in.Provider) || len(in.ExternalID) < 1 || len(in.ExternalID) > 200 || len(in.SourceReference) > 500 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid provider, external identifier, provenance and reason required."))
		return
	}
	command := in
	command.ApprovalID = ids.Nil
	operation := inventoryOperation(n.ID, command)
	var original *portfolio.Node
	if !in.ExpectedNodeID.IsZero() {
		old, e := portfolio.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, in.ExpectedNodeID)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		if old.Kind != n.Kind {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Remapping must retain the external object type."))
			return
		}
		original = &old
		if in.NodeVersion != n.Version || in.TreeVersion != n.TreeVersion || in.PreviousNodeVersion != old.Version || in.PreviousTreeVersion != old.TreeVersion {
			reply(w, r, 0, nil, apperrors.Conflict("GRAPH_CHANGED", "Remapping review requires current source and destination node/subtree versions."))
			return
		}
		digest, _ := operationDigest(approvals.ActionInventoryMapping, operation)
		if e = portfolio.CheckSafety(r.Context(), h.InventoryDependencies, []portfolio.Node{old, n}, digest, h.Auth.Service.Now().UTC()); e != nil {
			reply(w, r, 0, nil, e)
			return
		}
	}
	h.execute(w, r, "inventory:map:"+n.ID.String(), in, "inventory:mappings", string(n.Kind), n.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "inventory:mappings", string(n.Kind), n.ID, true)
		if err != nil {
			return 0, nil, err
		}
		for _, snapshot := range []*portfolio.Node{&n, original} {
			if snapshot == nil {
				continue
			}
			current, e := portfolio.Get(ctx, tx, t.Organisation, snapshot.ID)
			if e != nil {
				return 0, nil, e
			}
			if current.Version != snapshot.Version || current.TreeVersion != snapshot.TreeVersion {
				return 0, nil, apperrors.Conflict("GRAPH_CHANGED", "Mapping safety evidence is stale; review the current identities.")
			}
		}
		var id, current ids.ID
		var version int64
		err = tx.QueryRow(ctx, `SELECT id,node_id,version FROM inventory_external_mappings WHERE organisation_id=$1 AND provider=$2 AND external_id=$3 AND object_type=$4 FOR UPDATE`, t.Organisation, in.Provider, in.ExternalID, string(n.Kind)).Scan(&id, &current, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			if !in.ExpectedNodeID.IsZero() || in.Version != 0 {
				return 0, nil, apperrors.Conflict("MAPPING_CHANGED", "Expected mapping does not exist.")
			}
			id = ids.New()
			version = 1
			_, err = tx.Exec(ctx, `INSERT INTO inventory_external_mappings(id,organisation_id,node_id,provider,external_id,source_reference,created_by,object_type) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, n.ID, in.Provider, in.ExternalID, in.SourceReference, t.Account, string(n.Kind))
		} else if err == nil {
			if current == n.ID {
				return 0, nil, apperrors.Conflict("MAPPING_EXISTS", "This external identifier is already mapped; use its canonical UUID.")
			}
			if in.ExpectedNodeID != current || in.Version != version {
				return 0, nil, apperrors.Conflict("MAPPING_COLLISION", "External identifier already maps to another canonical identity.")
			}
			if _, _, err = h.authorize(ctx, tx, "inventory:mappings", string(n.Kind), current, true); err != nil {
				return 0, nil, err
			}
			if err = h.requirePrivilegeChange(ctx, tx, t, approvals.ActionInventoryMapping, target.Scope, in.ApprovalID, operation); err != nil {
				return 0, nil, err
			}
			version++
			_, err = tx.Exec(ctx, `UPDATE inventory_external_mappings SET node_id=$3,version=version+1,source_reference=$4,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, n.ID, in.SourceReference)
		}
		if err != nil {
			return 0, nil, err
		}
		if err = h.accessEvent(ctx, tx, t, "INVENTORY_EXTERNAL_MAPPING_CHANGED", "business.inventory.mapping.changed.v1", "inventory_node", n.ID, map[string]any{"mapping_id": id, "provider": in.Provider, "previous_node_id": current, "version": version, "reason": in.Reason}); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"mapping_id": id, "node_id": n.ID, "object_type": n.Kind, "provider": in.Provider, "external_id": in.ExternalID, "version": version}, nil
	})
}
func (h *Handler) inventoryVerificationGet(w http.ResponseWriter, r *http.Request) {
	_, n, err := h.inventoryTarget(r, "structure:read", false)
	reply(w, r, 200, n.Verification, err)
}
func (h *Handler) inventoryVerificationLink(w http.ResponseWriter, r *http.Request) {
	var in InventoryVerification
	if !decode(w, r, &in) {
		return
	}
	t, n, err := h.inventoryTarget(r, "inventory:verify", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if in.Version < 1 || n.Kind != portfolio.Property && n.Kind != portfolio.Parcel || in.CreateCase && !in.VerificationID.IsZero() {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Specify a current property/parcel version and one case operation."))
		return
	}
	if h.Verification == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification Service unavailable."))
		return
	}
	id := in.VerificationID
	if in.CreateCase {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 128 {
			reply(w, r, 0, nil, apperrors.InvalidRequest("A bounded idempotency key is required."))
			return
		}
		digest := sha256.Sum256([]byte(n.ID.String() + ":" + key))
		reference := "business-property:" + hex.EncodeToString(digest[:])
		c, e := h.Verification.Create(r.Context(), verification.TypePropertyAuthority, verification.Subject{Type: verification.SubjectProperty, Domain: string(platform.DomainBusinessOperations), ID: n.ID}, reference, reference)
		if e != nil {
			reply(w, r, 0, nil, apperrors.Unavailable("Property verification case creation unavailable."))
			return
		}
		id = c.VerificationID
	}
	if id.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid authoritative case required."))
		return
	}
	c, err := h.Verification.Get(r.Context(), id)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Authoritative property status unavailable."))
		return
	}
	if c.VerificationID != id {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Verification case identity mismatch."))
		return
	}
	p := portfolio.VerificationProjector{Pool: h.Auth.Service.Pool, Outbox: h.Auth.Service.Outbox, Now: h.Auth.Service.Now, Authorize: func(ctx context.Context, tx pgx.Tx) error {
		_, _, err := h.authorize(ctx, tx, "inventory:verify", string(n.Kind), n.ID, true)
		return err
	}}
	err = p.Apply(r.Context(), t.Organisation, n.ID, t.Account, in.Version, c, nil)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	out, err := portfolio.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, n.ID)
	reply(w, r, 200, out.Verification, err)
}
func mediaOwned(m media.Metadata, t tenant, n portfolio.Node) bool {
	return m.ID.ValidateV7() == nil && m.OwnerDomain == string(platform.DomainBusinessOperations) && ((m.OwnerType == "ORGANIZATION" || m.OwnerType == "BUSINESS") && m.OwnerID == t.Organisation || m.OwnerType == "PROPERTY" && m.OwnerID == n.ID || m.OwnerType == "USER" && m.OwnerID == t.Account)
}
func (h *Handler) inventoryMediaLink(w http.ResponseWriter, r *http.Request) {
	var in InventoryMedia
	if !decode(w, r, &in) {
		return
	}
	t, n, err := h.inventoryTarget(r, "inventory:media", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if in.MediaID.ValidateV7() != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A media reference, current version and reason are required."))
		return
	}
	switch in.Purpose {
	case "PHOTO", "DOCUMENT", "SURVEY", "FLOOR_PLAN", "DIGITAL_TWIN", "WARRANTY":
	default:
		reply(w, r, 0, nil, apperrors.InvalidRequest("Unsupported structural media purpose."))
		return
	}
	if h.Media == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Media Service unavailable."))
		return
	}
	m, err := h.Media.Get(r.Context(), in.MediaID)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Media metadata unavailable."))
		return
	}
	if m.ID != in.MediaID || !mediaOwned(m, t, n) || m.Status == "DELETED" || m.Status == "DELETE_PENDING" || m.Status == "REJECTED" {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Media reference is unavailable or belongs to another owner."))
		return
	}
	h.execute(w, r, "inventory:media:"+n.ID.String(), in, "inventory:media", string(n.Kind), n.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "inventory:media", string(n.Kind), n.ID, true)
		if err != nil {
			return 0, nil, err
		}
		current, err := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		if err != nil {
			return 0, nil, err
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Inventory version changed.")
		}
		var id ids.ID
		var version int64
		err = tx.QueryRow(ctx, `INSERT INTO inventory_media_links(id,organisation_id,node_id,media_id,purpose,provider_status,access_classification,created_by,owner_type,owner_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(organisation_id,node_id,media_id,purpose) DO UPDATE SET provider_status=excluded.provider_status,access_classification=excluded.access_classification,provider_checked_at=now(),version=inventory_media_links.version+1 RETURNING id,version`, ids.New(), t.Organisation, n.ID, m.ID, in.Purpose, m.Status, m.Classification, t.Account, m.OwnerType, m.OwnerID).Scan(&id, &version)
		if err != nil {
			return 0, nil, err
		}
		if err = h.accessEvent(ctx, tx, t, "INVENTORY_MEDIA_LINKED", "business.inventory.media.changed.v1", "inventory_node", n.ID, map[string]any{"media_link_id": id, "media_id": m.ID, "purpose": in.Purpose, "status": m.Status, "reason": in.Reason}); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"media_link_id": id, "media_id": m.ID, "provider_status": m.Status, "version": version}, nil
	})
}
func (h *Handler) inventoryMediaAccess(w http.ResponseWriter, r *http.Request) {
	t, n, err := h.inventoryTarget(r, "inventory:media", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	link, err := ids.ParseV7(chi.URLParam(r, "link_id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid media link."))
		return
	}
	var mediaID, owner ids.ID
	var purpose, ownerType string
	if err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT media_id,purpose,owner_type,owner_id FROM inventory_media_links WHERE organisation_id=$1 AND node_id=$2 AND id=$3`, t.Organisation, n.ID, link).Scan(&mediaID, &purpose, &ownerType, &owner); err != nil {
		reply(w, r, 0, nil, apperrors.NotFound("MEDIA_LINK_NOT_FOUND", "Media link not found."))
		return
	}
	if h.Media == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Media Service unavailable."))
		return
	}
	m, err := h.Media.Get(r.Context(), mediaID)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Authoritative media status unavailable."))
		return
	}
	if m.ID != mediaID || m.OwnerDomain != string(platform.DomainBusinessOperations) || m.OwnerType != ownerType || m.OwnerID != owner || m.Status != "READY" {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Media artifact is not currently usable."))
		return
	}
	access, err := h.Media.DownloadURL(r.Context(), mediaID, "BUSINESS_INVENTORY_"+purpose)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Purpose-bound Media access unavailable."))
		return
	}
	u, e := url.Parse(access.URL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || !h.Auth.Service.Now().Before(access.ExpiresAt) || access.ExpiresAt.After(h.Auth.Service.Now().Add(15*time.Minute)) {
		reply(w, r, 0, nil, apperrors.Unavailable("Invalid Media access assertion."))
		return
	}
	if err = audit(r.Context(), h.Auth.Service.Pool, t, "INVENTORY_MEDIA_ACCESS_ISSUED", "inventory_node", n.ID, map[string]any{"media_link_id": link, "purpose": purpose, "expires_at": access.ExpiresAt}); err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, 200, access, nil)
}
func (h *Handler) inventorySpaceGraph(w http.ResponseWriter, r *http.Request) {
	var in SpaceGraphCommand
	if !decode(w, r, &in) {
		return
	}
	_, n, err := h.inventoryTarget(r, "inventory:media", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if !in.Confirmed || in.Version < 1 || !validStaffReason(in.Reason) || len(in.Spaces) < 1 || len(in.Spaces) > 100 || in.MediaLinkID.ValidateV7() != nil || (in.CoordinateSystem != "LOCAL_METRES" && in.CoordinateSystem != "WGS84") {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Confirmed bounded spaces, coordinate system, media link and version required."))
		return
	}
	h.execute(w, r, "inventory:spaces:"+n.ID.String(), in, "inventory:media", string(n.Kind), n.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "inventory:media", string(n.Kind), n.ID, true)
		if err != nil {
			return 0, nil, err
		}
		current, err := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		if err != nil {
			return 0, nil, err
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Inventory version changed.")
		}
		var purpose, status string
		if err = tx.QueryRow(ctx, `SELECT purpose,provider_status FROM inventory_media_links WHERE organisation_id=$1 AND node_id=$2 AND id=$3 AND provider_checked_at>now()-interval '5 minutes'`, t.Organisation, n.ID, in.MediaLinkID).Scan(&purpose, &status); err != nil || status != "READY" || (purpose != "FLOOR_PLAN" && purpose != "DIGITAL_TWIN") {
			return 0, nil, apperrors.PermissionDenied("A fresh READY floor-plan/digital-twin reference is required.")
		}
		seen := map[ids.ID]bool{}
		for _, space := range in.Spaces {
			if space.NodeID.ValidateV7() != nil || len(space.Label) > 200 || len(space.Points) < 1 || len(space.Points) > 256 || seen[space.NodeID] {
				return 0, nil, apperrors.InvalidRequest("Invalid or duplicate canonical space reference.")
			}
			seen[space.NodeID] = true
			if _, _, err = h.authorize(ctx, tx, "structure:read", stringKind(ctx, tx, t.Organisation, space.NodeID), space.NodeID, false); err != nil {
				return 0, nil, err
			}
			var same bool
			if err = tx.QueryRow(ctx, `SELECT business_graph_property($1,$2) IS NOT DISTINCT FROM business_graph_property($1,$3) AND business_graph_property($1,$2) IS NOT NULL`, t.Organisation, n.ID, space.NodeID).Scan(&same); err != nil {
				return 0, nil, err
			}
			if !same {
				return 0, nil, apperrors.PermissionDenied("Spaces must reference the same canonical physical property.")
			}
			for _, point := range space.Points {
				if len(point) != 3 {
					return 0, nil, apperrors.InvalidRequest("Space coordinates must be three-dimensional.")
				}
				for _, v := range point {
					if math.IsInf(v, 0) || math.IsNaN(v) || math.Abs(v) > 1000000 {
						return 0, nil, apperrors.InvalidRequest("Space coordinate exceeds bounds.")
					}
				}
				if in.CoordinateSystem == "WGS84" && (math.Abs(point[0]) > 180 || math.Abs(point[1]) > 90) {
					return 0, nil, apperrors.InvalidRequest("WGS84 space coordinate invalid.")
				}
			}
		}
		raw, _ := json.Marshal(in.Spaces)
		id := ids.New()
		var version int64
		if err = tx.QueryRow(ctx, `SELECT coalesce(max(version),0)+1 FROM space_graphs WHERE organisation_id=$1 AND node_id=$2`, t.Organisation, n.ID).Scan(&version); err != nil {
			return 0, nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO space_graphs(id,organisation_id,node_id,media_link_id,coordinate_system,spaces,confirmed_by,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, n.ID, in.MediaLinkID, in.CoordinateSystem, raw, t.Account, version); err != nil {
			return 0, nil, err
		}
		if purpose == "DIGITAL_TWIN" {
			if _, err = tx.Exec(ctx, `INSERT INTO digital_twin_refs(id,organisation_id,node_id) VALUES($1,$2,$3)`, id, t.Organisation, n.ID); err != nil {
				return 0, nil, err
			}
		}
		if err = h.accessEvent(ctx, tx, t, "SPACE_GRAPH_CONFIRMED", "business.inventory.space.confirmed.v1", "inventory_node", n.ID, map[string]any{"space_graph_id": id, "version": version, "reason": in.Reason}); err != nil {
			return 0, nil, err
		}
		return 201, map[string]any{"space_graph_id": id, "version": version}, nil
	})
}
func stringKind(ctx context.Context, tx pgx.Tx, org, id ids.ID) string {
	var kind string
	_ = tx.QueryRow(ctx, `SELECT kind FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&kind)
	return kind
}
