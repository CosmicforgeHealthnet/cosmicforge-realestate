package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type CommercialContent struct {
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Language      string   `json:"language"`
	Amenities     []string `json:"amenities"`
	HouseRules    []string `json:"house_rules"`
	CheckInLocal  string   `json:"check_in_local"`
	CheckOutLocal string   `json:"check_out_local"`
	MediaLinkIDs  []ids.ID `json:"media_link_ids"`
}

func (c CommercialContent) validate() error {
	if !commercialName(c.Title) || len(c.Description) > 20000 || len(c.Language) < 2 || len(c.Language) > 16 || len(c.Amenities) > 100 || len(c.HouseRules) > 50 || len(c.MediaLinkIDs) > 30 {
		return apperrors.InvalidRequest("Bounded plain-text title, description, language, amenities, rules and Media Service references required.")
	}
	for _, v := range append(append([]string{}, c.Amenities...), c.HouseRules...) {
		if len(v) < 1 || len(v) > 500 || strings.ContainsRune(v, 0) {
			return apperrors.InvalidRequest("Invalid amenity or house rule.")
		}
	}
	for _, v := range []string{c.CheckInLocal, c.CheckOutLocal} {
		if v != "" {
			if _, e := time.Parse("15:04", v); e != nil {
				return apperrors.InvalidRequest("Check-in/out uses property-local HH:MM.")
			}
		}
	}
	seen := map[ids.ID]bool{}
	for _, id := range c.MediaLinkIDs {
		if id.ValidateV7() != nil || seen[id] {
			return apperrors.InvalidRequest("Media link IDs must be unique UUIDv7 values.")
		}
		seen[id] = true
	}
	return nil
}
func (c CommercialContent) quality() (int, []string) {
	score := 0
	issues := []string{}
	checks := []struct {
		ok     bool
		points int
		issue  string
	}{{len(strings.TrimSpace(c.Title)) >= 5, 15, "TITLE_TOO_SHORT"}, {len(strings.TrimSpace(c.Description)) >= 50, 25, "DESCRIPTION_TOO_SHORT"}, {len(c.MediaLinkIDs) > 0, 25, "PHOTO_REQUIRED"}, {len(c.Amenities) > 0, 10, "AMENITIES_REQUIRED"}, {len(c.HouseRules) > 0, 10, "HOUSE_RULES_REQUIRED"}, {c.CheckInLocal != "" && c.CheckOutLocal != "", 15, "CHECK_IN_OUT_REQUIRED"}}
	for _, check := range checks {
		if check.ok {
			score += check.points
		} else {
			issues = append(issues, check.issue)
		}
	}
	return score, issues
}

type CommercialListing struct {
	ID             ids.ID  `json:"id"`
	OrganisationID ids.ID  `json:"organisation_id"`
	PropertyID     ids.ID  `json:"property_id"`
	ProductID      ids.ID  `json:"product_id"`
	TemplateID     *ids.ID `json:"template_id,omitempty"`
	State          string  `json:"state"`
	Version        int64   `json:"version"`
}
type CommercialListingVersion struct {
	ID              ids.ID            `json:"id"`
	ListingID       ids.ID            `json:"listing_id"`
	Version         int64             `json:"version"`
	Content         CommercialContent `json:"content"`
	PropertyVersion int64             `json:"property_version"`
	Reason          string            `json:"reason"`
}
type CommercialListingCommand struct {
	PropertyID      ids.ID            `json:"property_id"`
	ProductID       ids.ID            `json:"product_id"`
	TemplateID      *ids.ID           `json:"template_id,omitempty"`
	Version         int64             `json:"version,omitempty"`
	PropertyVersion int64             `json:"property_version"`
	Content         CommercialContent `json:"content"`
	Reason          string            `json:"reason"`
}
type CommercialTemplateDefinition struct {
	RequiredFields []string `json:"required_fields"`
	MinimumQuality int      `json:"minimum_quality"`
	Language       string   `json:"language"`
}

func (d CommercialTemplateDefinition) validate() bool {
	if d.MinimumQuality < 75 || d.MinimumQuality > 100 || len(d.Language) < 2 || len(d.Language) > 16 || len(d.RequiredFields) > 6 {
		return false
	}
	for _, field := range d.RequiredFields {
		if !slices.Contains([]string{"description", "media", "amenities", "house_rules", "check_in_local", "check_out_local"}, field) {
			return false
		}
	}
	return true
}

type CommercialTemplateCommand struct {
	Name       string                       `json:"name"`
	Definition CommercialTemplateDefinition `json:"definition"`
	Version    int64                        `json:"version,omitempty"`
	Reason     string                       `json:"reason"`
}

func (h *Handler) registerCommercialContent(r chi.Router) {
	r.Get("/listing-templates", h.templateList)
	r.Post("/listing-templates", h.templateCreate)
	r.Patch("/listing-templates/{id}", h.templatePatch)
	r.Get("/listings", h.listingList)
	r.Post("/listings", h.listingCreate)
	r.Get("/listings/{id}", h.listingGet)
	r.Patch("/listings/{id}", h.listingPatch)
	r.Get("/listings/{id}/versions", h.listingVersions)
	r.Get("/listings/{id}/readiness", h.listingReadiness)
	r.Post("/listings/{id}/submit", h.listingSubmit)
	r.Post("/listings/{id}/publish", h.listingPublish)
	r.Post("/listings/{id}/pause", h.listingPause)
	r.Post("/listings/{id}/withdraw", h.listingWithdraw)
	r.Get("/listings/{id}/publication", h.listingPublication)
}
func (h *Handler) templateList(w http.ResponseWriter, r *http.Request) {
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "content:read", "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(t)-'created_by' FROM listing_templates t WHERE organisation_id=$1 ORDER BY created_at,id LIMIT $2 OFFSET $3`, t.Organisation, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) templateCreate(w http.ResponseWriter, r *http.Request) {
	h.templateWrite(w, r, false)
}
func (h *Handler) templatePatch(w http.ResponseWriter, r *http.Request) { h.templateWrite(w, r, true) }
func (h *Handler) templateWrite(w http.ResponseWriter, r *http.Request, update bool) {
	var in CommercialTemplateCommand
	if !decode(w, r, &in) {
		return
	}
	if !commercialName(in.Name) || !in.Definition.validate() || !validStaffReason(in.Reason) || update && in.Version < 1 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid template quality/required fields, name, current version and reason required."))
		return
	}
	id := ids.New()
	if update {
		var e error
		id, e = pathCommercialID(r)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
	}
	operationKey := "commercial:template:create"
	if update {
		operationKey = "commercial:template:" + id.String()
	}
	h.execute(w, r, operationKey, in, "content:write", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "content:write", "COMPANY", ids.Nil, true)
		if e != nil {
			return 0, nil, e
		}
		raw, _ := json.Marshal(in.Definition)
		status := 201
		if update {
			tag, e := tx.Exec(ctx, `UPDATE listing_templates SET name=$3,definition=$4,version=version+1 WHERE organisation_id=$1 AND id=$2 AND version=$5`, t.Organisation, id, in.Name, raw, in.Version)
			if e != nil {
				return 0, nil, e
			}
			if tag.RowsAffected() != 1 {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Template scope or version changed.")
			}
			status = 200
		} else {
			if _, e = tx.Exec(ctx, `INSERT INTO listing_templates(id,organisation_id,name,definition,created_by) VALUES($1,$2,$3,$4,$5)`, id, t.Organisation, in.Name, raw, t.Account); e != nil {
				return 0, nil, e
			}
		}
		value, e := typedJSON[json.RawMessage](ctx, tx, `SELECT to_jsonb(t)-'created_by' FROM listing_templates t WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = audit(ctx, tx, t, "LISTING_TEMPLATE_CHANGED", "listing_template", id, map[string]any{"template": value, "reason": in.Reason})
		return status, value, e
	})
}
func loadListing(ctx context.Context, q database.Querier, org, id ids.ID) (CommercialListing, error) {
	return typedJSON[CommercialListing](ctx, q, `SELECT to_jsonb(l) FROM listings l WHERE organisation_id=$1 AND id=$2`, org, id)
}
func latestListingVersion(ctx context.Context, q database.Querier, org, id ids.ID) (CommercialListingVersion, error) {
	return typedJSON[CommercialListingVersion](ctx, q, `SELECT to_jsonb(v) FROM listing_versions v WHERE organisation_id=$1 AND listing_id=$2 ORDER BY version DESC LIMIT 1`, org, id)
}
func (h *Handler) listingTarget(r *http.Request, cap string, mfa bool) (tenant, CommercialListing, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, CommercialListing{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, CommercialListing{}, e
	}
	v, e := loadListing(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		t, e = h.commercialScope(r, cap, v.PropertyID, mfa)
	}
	return t, v, e
}
func (h *Handler) listingList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "listings", "content:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		l, e := loadListing(ctx, h.Auth.Service.Pool, org, id)
		if e != nil {
			return nil, e
		}
		v, e := latestListingVersion(ctx, h.Auth.Service.Pool, org, id)
		return map[string]any{"listing": l, "current_version": v}, e
	})
}
func (h *Handler) listingGet(w http.ResponseWriter, r *http.Request) {
	t, l, e := h.listingTarget(r, "content:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	v, e := latestListingVersion(r.Context(), h.Auth.Service.Pool, t.Organisation, l.ID)
	reply(w, r, 200, map[string]any{"listing": l, "current_version": v}, e)
}
func (h *Handler) listingVersions(w http.ResponseWriter, r *http.Request) {
	t, l, e := h.listingTarget(r, "content:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	v, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(v)-'created_by' FROM listing_versions v WHERE organisation_id=$1 AND listing_id=$2 ORDER BY version DESC LIMIT $3 OFFSET $4`, t.Organisation, l.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": v, "limit": limit, "offset": offset}, e)
}
func (h *Handler) listingCreate(w http.ResponseWriter, r *http.Request) { h.listingWrite(w, r, false) }
func (h *Handler) listingPatch(w http.ResponseWriter, r *http.Request)  { h.listingWrite(w, r, true) }
func (h *Handler) listingWrite(w http.ResponseWriter, r *http.Request, update bool) {
	var in CommercialListingCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Content.validate() != nil || in.PropertyID.ValidateV7() != nil || in.ProductID.ValidateV7() != nil || in.PropertyVersion < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid canonical product/property versions, bounded content and reason required."))
		return
	}
	id := ids.New()
	if update {
		_, l, e := h.listingTarget(r, "content:write", true)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		if l.PropertyID != in.PropertyID || l.ProductID != in.ProductID {
			returnIDError(w, r)
			return
		}
		id = l.ID
	}
	operationKey := "commercial:listing:create"
	if update {
		operationKey = "commercial:listing:write:" + id.String()
	}
	h.execute(w, r, operationKey, in, "content:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "content:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		p, e := inventory.GetProduct(ctx, tx, t.Organisation, in.ProductID)
		if e != nil {
			return 0, nil, e
		}
		property, e := portfolio.Get(ctx, tx, t.Organisation, in.PropertyID)
		if e != nil {
			return 0, nil, e
		}
		if p.PropertyID != in.PropertyID || property.Kind != portfolio.Property || property.Version != in.PropertyVersion {
			return 0, nil, apperrors.Conflict("CANONICAL_CONTEXT_CHANGED", "Canonical product/property scope or version changed.")
		}
		if in.TemplateID != nil {
			var definition CommercialTemplateDefinition
			var raw []byte
			if e = tx.QueryRow(ctx, `SELECT definition FROM listing_templates WHERE organisation_id=$1 AND id=$2`, t.Organisation, *in.TemplateID).Scan(&raw); e != nil {
				return 0, nil, e
			}
			if e = json.Unmarshal(raw, &definition); e != nil {
				return 0, nil, e
			}
			if in.Content.Language != definition.Language {
				return 0, nil, apperrors.InvalidRequest("Listing language must match its template.")
			}
		}
		version := int64(1)
		status := 201
		if update {
			l, e := loadListing(ctx, tx, t.Organisation, id)
			if e != nil {
				return 0, nil, e
			}
			if l.Version != in.Version || l.State == "ARCHIVED" || l.State == "PUBLISHED" {
				return 0, nil, apperrors.Conflict("LISTING_VERSION_OR_STATE", "Pause active distribution and use the current listing version before editing.")
			}
			var pending bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM listing_publication WHERE organisation_id=$1 AND listing_id=$2 AND (desired_state='PUBLISHED' OR delivery_state<>'CONFIRMED'))`, t.Organisation, id).Scan(&pending); e != nil {
				return 0, nil, e
			}
			if pending {
				return 0, nil, apperrors.Conflict("PUBLICATION_NOT_PAUSED", "Provider pause or withdrawal must be confirmed before content changes.")
			}
			if _, e = tx.Exec(ctx, `UPDATE listings SET state='DRAFT',template_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.TemplateID); e != nil {
				return 0, nil, e
			}
			if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0)+1 FROM listing_versions WHERE organisation_id=$1 AND listing_id=$2`, t.Organisation, id).Scan(&version); e != nil {
				return 0, nil, e
			}
			status = 200
		} else {
			if _, e = tx.Exec(ctx, `INSERT INTO listings(id,organisation_id,property_id,product_id,template_id,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, t.Organisation, in.PropertyID, in.ProductID, in.TemplateID, t.Account); e != nil {
				return 0, nil, e
			}
		}
		vid := ids.New()
		raw, _ := json.Marshal(in.Content)
		if _, e = tx.Exec(ctx, `INSERT INTO listing_versions(id,organisation_id,listing_id,version,content,property_version,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, vid, t.Organisation, id, version, raw, in.PropertyVersion, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		for _, mediaID := range in.Content.MediaLinkIDs {
			var eligible bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_media_links m JOIN inventory_nodes n ON n.id=m.node_id AND n.organisation_id=m.organisation_id WHERE m.organisation_id=$1 AND m.id=$2 AND (n.id=$3 OR business_graph_property(n.organisation_id,n.id)=$3) AND m.purpose='PHOTO' AND m.provider_status='READY' AND m.access_classification='PUBLIC')`, t.Organisation, mediaID, in.PropertyID).Scan(&eligible); e != nil {
				return 0, nil, e
			}
			if !eligible {
				return 0, nil, apperrors.PermissionDenied("Listing photos must be public, ready Media Service links within this property.")
			}
			if _, e = tx.Exec(ctx, `INSERT INTO listing_media_links(organisation_id,listing_version_id,media_link_id) VALUES($1,$2,$3)`, t.Organisation, vid, mediaID); e != nil {
				return 0, nil, e
			}
		}
		score, issues := in.Content.quality()
		issueRaw, _ := json.Marshal(issues)
		if _, e = tx.Exec(ctx, `INSERT INTO content_quality_scores(id,organisation_id,listing_version_id,score,issues) VALUES($1,$2,$3,$4,$5)`, ids.New(), t.Organisation, vid, score, issueRaw); e != nil {
			return 0, nil, e
		}
		l, e := loadListing(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		v, e := latestListingVersion(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "listing", "LISTING_CONTENT_VERSION_CREATED", businessEvents.ListingUpdated, l.Version, map[string]any{"listing_id": id, "listing_version_id": vid, "content_version": version, "score": score, "issues": issues})
		return status, map[string]any{"listing": l, "current_version": v, "quality_score": score, "issues": issues}, e
	})
}
func returnIDError(w http.ResponseWriter, r *http.Request) {
	reply(w, r, 0, nil, apperrors.PermissionDenied("Listing identity cannot change its canonical property or product."))
}
func (h *Handler) listingQuality(ctx context.Context, q database.Querier, l CommercialListing, v CommercialListingVersion) (int, []string, error) {
	score, issues := v.Content.quality()
	minimum := 100
	if l.TemplateID != nil {
		var raw []byte
		if e := q.QueryRow(ctx, `SELECT definition FROM listing_templates WHERE organisation_id=$1 AND id=$2`, l.OrganisationID, *l.TemplateID).Scan(&raw); e != nil {
			return 0, nil, e
		}
		var d CommercialTemplateDefinition
		if e := json.Unmarshal(raw, &d); e != nil {
			return 0, nil, e
		}
		minimum = d.MinimumQuality
		for _, field := range d.RequiredFields {
			valid := true
			switch field {
			case "description":
				valid = len(v.Content.Description) >= 50
			case "media":
				valid = len(v.Content.MediaLinkIDs) > 0
			case "amenities":
				valid = len(v.Content.Amenities) > 0
			case "house_rules":
				valid = len(v.Content.HouseRules) > 0
			case "check_in_local":
				valid = v.Content.CheckInLocal != ""
			case "check_out_local":
				valid = v.Content.CheckOutLocal != ""
			}
			if !valid {
				issues = append(issues, "TEMPLATE_REQUIRED:"+field)
			}
		}
	}
	if score < minimum {
		issues = append(issues, "MINIMUM_QUALITY_NOT_MET")
	}
	return score, issues, nil
}
func (h *Handler) listingReadiness(w http.ResponseWriter, r *http.Request) {
	t, l, e := h.listingTarget(r, "content:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	v, e := latestListingVersion(r.Context(), h.Auth.Service.Pool, t.Organisation, l.ID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	score, issues, e := h.listingQuality(r.Context(), h.Auth.Service.Pool, l, v)
	reply(w, r, 200, map[string]any{"listing_id": l.ID, "version": l.Version, "listing_version_id": v.ID, "score": score, "issues": issues, "content_ready": len(issues) == 0, "live_commercial_eligibility_checked": false}, e)
}
func (h *Handler) listingSubmit(w http.ResponseWriter, r *http.Request) {
	var in CommercialStateChange
	if !decode(w, r, &in) {
		return
	}
	_, l, e := h.listingTarget(r, "content:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Review reason required."))
		return
	}
	h.execute(w, r, "commercial:listing:submit:"+l.ID.String(), in, "content:write", "PROPERTY", l.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "content:write", "PROPERTY", l.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		l, e := loadListing(ctx, tx, t.Organisation, l.ID)
		if e != nil {
			return 0, nil, e
		}
		if l.Version != in.Version || l.State != "DRAFT" && l.State != "PAUSED" && l.State != "WITHDRAWN" {
			return 0, nil, apperrors.Conflict("LISTING_REVIEW_CONFLICT", "Only current editable content may enter review.")
		}
		v, e := latestListingVersion(ctx, tx, t.Organisation, l.ID)
		if e != nil {
			return 0, nil, e
		}
		_, issues, e := h.listingQuality(ctx, tx, l, v)
		if e != nil {
			return 0, nil, e
		}
		if len(issues) > 0 {
			return 0, nil, apperrors.Conflict("CONTENT_NOT_READY", "Complete the persisted content-quality requirements before review.")
		}
		if _, e = tx.Exec(ctx, `UPDATE listings SET state='REVIEW',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, l.ID); e != nil {
			return 0, nil, e
		}
		l.State = "REVIEW"
		l.Version++
		e = h.commercialCommit(ctx, tx, t, l.PropertyID, l.ID, "listing", "LISTING_SUBMITTED", "", l.Version, map[string]any{"listing_version_id": v.ID, "reason": in.Reason})
		return 200, l, e
	})
}

type CommercialPublishCommand struct {
	Version          int64  `json:"version"`
	ListingVersionID ids.ID `json:"listing_version_id"`
	PropertyVersion  int64  `json:"property_version"`
	ProductVersion   int64  `json:"product_version"`
	PoolVersion      int64  `json:"pool_version"`
	RatePlanVersion  int64  `json:"rate_plan_version"`
	ConnectionID     ids.ID `json:"connection_id"`
	DateFrom         string `json:"date_from"`
	DateTo           string `json:"date_to"`
	Reason           string `json:"reason"`
	ApprovalID       ids.ID `json:"approval_id"`
}

func (h *Handler) listingPublish(w http.ResponseWriter, r *http.Request) {
	var in CommercialPublishCommand
	if !decode(w, r, &in) {
		return
	}
	t, l, e := h.listingTarget(r, "publication:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	v, e := latestListingVersion(r.Context(), h.Auth.Service.Pool, t.Organisation, l.ID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || in.ListingVersionID != v.ID || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current listing/content/canonical versions, connection, civil dates and reason required."))
		return
	}
	// Fresh Media metadata is fetched before opening the owning transaction. The
	// immutable content/link identities are fenced again in that transaction.
	metadata := map[ids.ID]media.Metadata{}
	if h.Media == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Live Media Service metadata is required for publication."))
		return
	}
	for _, link := range v.Content.MediaLinkIDs {
		var mediaID ids.ID
		if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT media_id FROM inventory_media_links WHERE organisation_id=$1 AND id=$2`, t.Organisation, link).Scan(&mediaID); e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		m, e := h.Media.Get(r.Context(), mediaID)
		if e != nil {
			reply(w, r, 0, nil, apperrors.Unavailable("Live listing media metadata unavailable."))
			return
		}
		metadata[link] = m
	}
	h.execute(w, r, "commercial:listing:publish:"+l.ID.String(), in, "publication:write", "PROPERTY", l.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "publication:write", "PROPERTY", l.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := loadListing(ctx, tx, t.Organisation, l.ID)
		if e != nil {
			return 0, nil, e
		}
		version, e := latestListingVersion(ctx, tx, t.Organisation, l.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version || version.ID != in.ListingVersionID || current.State != "REVIEW" && current.State != "APPROVED" && current.State != "PUBLISHED" {
			return 0, nil, apperrors.Conflict("PUBLICATION_REVIEW_CONFLICT", "Publish only current reviewed immutable content.")
		}
		property, e := portfolio.Get(ctx, tx, t.Organisation, l.PropertyID)
		if e != nil {
			return 0, nil, e
		}
		p, e := inventory.GetProduct(ctx, tx, t.Organisation, l.ProductID)
		if e != nil {
			return 0, nil, e
		}
		pool, e := inventory.GetPool(ctx, tx, t.Organisation, p.PoolID)
		if e != nil {
			return 0, nil, e
		}
		if p.RatePlanID == nil {
			return 0, nil, apperrors.Conflict("RATE_REQUIRED", "Publication requires a canonical priced product.")
		}
		plan, e := revenue.GetPlan(ctx, tx, t.Organisation, *p.RatePlanID)
		if e != nil {
			return 0, nil, e
		}
		if property.Version != in.PropertyVersion || version.PropertyVersion != property.Version || p.Version != in.ProductVersion || pool.Version != in.PoolVersion || plan.Version != in.RatePlanVersion {
			return 0, nil, apperrors.Conflict("PUBLICATION_CANONICAL_VERSION_CHANGED", "Canonical graph, stock or price context changed after review.")
		}
		connection, e := h.loadChannelConnection(ctx, tx, t.Organisation, in.ConnectionID)
		if e != nil {
			return 0, nil, e
		}
		if connection.PropertyID != l.PropertyID {
			return 0, nil, apperrors.PermissionDenied("Connection belongs to another canonical property.")
		}
		if e = h.requireChannelCapability(ctx, tx, connection, "CONTENT"); e != nil {
			return 0, nil, e
		}
		if e = h.requireConfirmedMappings(ctx, tx, connection, p); e != nil {
			return 0, nil, e
		}
		if e = h.commercialProductEligibility(ctx, tx, t, p, p.MinGuests, 0, connection.ProviderCode); e != nil {
			return 0, nil, e
		}
		_, issues, e := h.listingQuality(ctx, tx, current, version)
		if e != nil {
			return 0, nil, e
		}
		if len(issues) > 0 {
			return 0, nil, apperrors.Conflict("CONTENT_NOT_READY", "Current content-quality requirements must be satisfied.")
		}
		for link, m := range metadata {
			var mediaID, nodeID ids.ID
			if e = tx.QueryRow(ctx, `SELECT media_id,node_id FROM inventory_media_links WHERE organisation_id=$1 AND id=$2`, t.Organisation, link).Scan(&mediaID, &nodeID); e != nil {
				return 0, nil, e
			}
			node, e := portfolio.Get(ctx, tx, t.Organisation, nodeID)
			if e != nil {
				return 0, nil, e
			}
			if m.ID != mediaID || !mediaOwned(m, t, node) || m.Status != "READY" || m.Classification != "PUBLIC" {
				return 0, nil, apperrors.PermissionDenied("Every listing photo must remain ready, public and owned in Media Service.")
			}
		}
		quote, e := h.calculateQuote(ctx, tx, t, CommercialQuoteCommand{ProductID: p.ID, DateFrom: in.DateFrom, DateTo: in.DateTo, Adults: p.MinGuests, Quantity: 1, Channel: connection.ProviderCode}, true)
		if e != nil {
			return 0, nil, e
		}
		ari, e := h.currentChannelARI(ctx, tx, t, connection, p, pool, inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}, property.Timezone)
		if e != nil {
			return 0, nil, e
		}
		pricingContext, e := h.channelPricingContext(ctx, tx, t.Organisation, ari)
		if e != nil {
			return 0, nil, e
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, approvals.ActionVerifiedPropertyPublication, target.Scope, in.ApprovalID, map[string]any{"listing_id": l.ID, "command": command}, connection.ProviderCode, &quote.AmountMinor, quote.Currency); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO listing_approvals(organisation_id,listing_version_id,approval_id) VALUES($1,$2,$3)`, t.Organisation, version.ID, in.ApprovalID); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE listings SET state='APPROVED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, l.ID); e != nil {
			return 0, nil, e
		}
		current.Version++
		current.State = "APPROVED"
		result, e := h.queueListingPublication(ctx, tx, t, current, version, connection, "PUBLISHED", map[string]any{"content": version.Content, "canonical_property_id": property.ID, "property_version": property.Version, "product": p, "pool": pool, "ari": ari, "pricing_context": pricingContext, "restrictions": quote.Restrictions, "price_example": quote, "approval_id": in.ApprovalID})
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, l.PropertyID, l.ID, "listing", "LISTING_PUBLICATION_QUEUED", "", current.Version, map[string]any{"connection_id": connection.ID, "listing_version_id": version.ID, "delivery_state": "PENDING"})
		return 202, result, e
	})
}
func (h *Handler) listingPause(w http.ResponseWriter, r *http.Request) { h.listingStop(w, r, "PAUSED") }
func (h *Handler) listingWithdraw(w http.ResponseWriter, r *http.Request) {
	h.listingStop(w, r, "WITHDRAWN")
}
func (h *Handler) listingStop(w http.ResponseWriter, r *http.Request, state string) {
	var in CommercialStateChange
	if !decode(w, r, &in) {
		return
	}
	_, l, e := h.listingTarget(r, "publication:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Publication stop reason required."))
		return
	}
	h.execute(w, r, "commercial:listing:stop:"+l.ID.String(), in, "publication:write", "PROPERTY", l.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "publication:write", "PROPERTY", l.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		l, e := loadListing(ctx, tx, t.Organisation, l.ID)
		if e != nil {
			return 0, nil, e
		}
		if l.Version != in.Version || l.State == "ARCHIVED" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Listing version changed.")
		}
		v, e := latestListingVersion(ctx, tx, t.Organisation, l.ID)
		if e != nil {
			return 0, nil, e
		}
		refs, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(p) FROM listing_publication p WHERE organisation_id=$1 AND listing_id=$2 ORDER BY connection_id`, t.Organisation, l.ID)
		if e != nil {
			return 0, nil, e
		}
		l.Version++
		l.State = state
		results := []any{}
		for _, raw := range refs {
			var ref struct {
				ConnectionID ids.ID `json:"connection_id"`
			}
			if e = json.Unmarshal(raw, &ref); e != nil {
				return 0, nil, e
			}
			c, e := h.loadChannelConnection(ctx, tx, t.Organisation, ref.ConnectionID)
			if e != nil {
				return 0, nil, e
			}
			result, e := h.queueListingPublication(ctx, tx, t, l, v, c, state, map[string]any{"reason": in.Reason})
			if e != nil {
				return 0, nil, e
			}
			results = append(results, result)
		}
		if _, e = tx.Exec(ctx, `UPDATE listings SET state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, l.ID, state); e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, l.PropertyID, l.ID, "listing", "LISTING_STOP_REQUESTED", "", l.Version, map[string]any{"desired_state": state, "reason": in.Reason})
		return 202, map[string]any{"listing": l, "publications": results, "provider_state": "PENDING_ACKNOWLEDGEMENT"}, e
	})
}
func (h *Handler) listingPublication(w http.ResponseWriter, r *http.Request) {
	t, l, e := h.listingTarget(r, "channels:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM listing_publication p WHERE organisation_id=$1 AND listing_id=$2 ORDER BY connection_id`, t.Organisation, l.ID)
	reply(w, r, 200, map[string]any{"items": items}, e)
}
func (h *Handler) queueListingPublication(ctx context.Context, tx pgx.Tx, t tenant, l CommercialListing, v CommercialListingVersion, c CommercialChannelConnection, state string, payload map[string]any) (any, error) {
	id := ids.New()
	var existing ids.ID
	canonicalVersion := l.Version
	var previousVersion int64
	e := tx.QueryRow(ctx, `SELECT id,canonical_version FROM listing_publication WHERE organisation_id=$1 AND listing_id=$2 AND connection_id=$3 FOR UPDATE`, t.Organisation, l.ID, c.ID).Scan(&existing, &previousVersion)
	if errors.Is(e, pgx.ErrNoRows) {
		_, e = tx.Exec(ctx, `INSERT INTO listing_publication(id,organisation_id,listing_id,listing_version_id,connection_id,desired_state,canonical_version) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, l.ID, v.ID, c.ID, state, canonicalVersion)
	} else if e == nil {
		id = existing
		canonicalVersion = max(canonicalVersion, previousVersion+1)
		_, e = tx.Exec(ctx, `UPDATE listing_publication SET listing_version_id=$3,desired_state=$4,delivery_state='PENDING',canonical_version=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, v.ID, state, canonicalVersion)
	}
	if e != nil {
		return nil, e
	}
	kind := "CONTENT"
	if state == "PAUSED" {
		kind = "PAUSE"
	}
	if state == "WITHDRAWN" {
		kind = "WITHDRAW"
	}
	payload["listing_id"] = l.ID
	payload["listing_version_id"] = v.ID
	payload["canonical_version"] = canonicalVersion
	payload["desired_state"] = state
	payload["organisation_id"] = t.Organisation
	payload["property_id"] = l.PropertyID
	if state == "PUBLISHED" {
		if e = h.applyChannelContentAuthority(ctx, tx, c, l.ID, payload); e != nil {
			return nil, e
		}
	}
	raw, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	hash := hashCommercialPayload(raw)
	job := ids.New()
	principal, _ := businessauth.PrincipalFrom(ctx)
	if _, e = tx.Exec(ctx, `INSERT INTO channel_sync_jobs(id,organisation_id,property_id,connection_id,publication_id,kind,canonical_version,payload,payload_hash,idempotency_key,actor_id,session_id,correlation_context) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, job, t.Organisation, l.PropertyID, c.ID, id, kind, canonicalVersion, raw, hash, "publication:"+id.String()+":"+hash, t.Account, nullableCommercialID(principal.SessionID), marshalCommercial(requestcontext.From(ctx))); e != nil {
		return nil, e
	}
	return map[string]any{"publication_id": id, "job_id": job, "desired_state": state, "delivery_state": "PENDING", "canonical_version": canonicalVersion}, nil
}
