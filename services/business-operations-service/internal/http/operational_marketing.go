package businesshttp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func init() {
	registerOpsTable("lead_sources", "leads", "segments", "campaigns", "storefront_collections", "upsell_offers", "upsell_acceptances")
}

type LeadSourceCreate struct {
	PropertyID            ids.ID `json:"property_id"`
	Code                  string `json:"code"`
	Name                  string `json:"name"`
	Channel               string `json:"channel"`
	AttributionWindowDays int    `json:"attribution_window_days"`
	ResponseSLASeconds    int    `json:"response_sla_seconds"`
	Reason                string `json:"reason"`
}

type Lead struct {
	ID                     ids.ID     `json:"id"`
	PropertyID             ids.ID     `json:"property_id"`
	SourceID               ids.ID     `json:"source_id"`
	CampaignID             *ids.ID    `json:"campaign_id"`
	ListingID              *ids.ID    `json:"listing_id"`
	GuestID                *ids.ID    `json:"guest_id"`
	Channel                string     `json:"channel"`
	ExternalReference      string     `json:"external_reference"`
	Interest               string     `json:"interest"`
	State                  string     `json:"state"`
	AssignedTo             *ids.ID    `json:"assigned_to"`
	FirstResponseDueAt     time.Time  `json:"first_response_due_at"`
	FirstRespondedAt       *time.Time `json:"first_responded_at"`
	FollowUpAt             *time.Time `json:"follow_up_at"`
	SLABreached            bool       `json:"sla_breached"`
	ConvertedReservationID *ids.ID    `json:"converted_reservation_id"`
	MergedInto             *ids.ID    `json:"merged_into"`
	Version                int64      `json:"version"`
}

type LeadCreate struct {
	PropertyID        ids.ID  `json:"property_id"`
	SourceID          ids.ID  `json:"source_id"`
	CampaignID        *ids.ID `json:"campaign_id,omitempty"`
	ListingID         *ids.ID `json:"listing_id,omitempty"`
	GuestID           *ids.ID `json:"guest_id,omitempty"`
	ExternalReference string  `json:"external_reference"`
	ContactReference  string  `json:"contact_reference,omitempty"`
	Interest          string  `json:"interest"`
	Reason            string  `json:"reason"`
}

type LeadCreateResult struct {
	Lead      Lead `json:"lead"`
	Duplicate bool `json:"duplicate"`
}

type LeadPatch struct {
	Version    int64      `json:"version"`
	State      string     `json:"state,omitempty"`
	FollowUpAt *time.Time `json:"follow_up_at,omitempty"`
	Reason     string     `json:"reason"`
}

type LeadActivityCreate struct {
	Kind             string    `json:"kind"`
	Summary          string    `json:"summary"`
	ContextReference string    `json:"context_reference,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

type LeadAssignmentCommand struct {
	LeadID     ids.ID `json:"lead_id"`
	Version    int64  `json:"version"`
	AssigneeID ids.ID `json:"assignee_id"`
	Reason     string `json:"reason"`
}

type LeadMergeCommand struct {
	Version      int64  `json:"version"`
	TargetLeadID ids.ID `json:"target_lead_id"`
	Reason       string `json:"reason"`
}

type LeadConvertCommand struct {
	Version       int64  `json:"version"`
	ReservationID ids.ID `json:"reservation_id"`
	Reason        string `json:"reason"`
}

type MarketingSegmentCreate struct {
	PropertyID     ids.ID `json:"property_id"`
	GuestSegmentKey ids.ID `json:"guest_segment_key"`
	Name           string `json:"name"`
	Reason         string `json:"reason"`
}

type Campaign struct {
	ID                  ids.ID     `json:"id"`
	PropertyID          ids.ID     `json:"property_id"`
	Name                string     `json:"name"`
	SegmentID           ids.ID     `json:"segment_id"`
	TemplateID          ids.ID     `json:"template_id"`
	ChannelCategory     string     `json:"channel_category"`
	State               string     `json:"state"`
	ScheduledAt         *time.Time `json:"scheduled_at"`
	BudgetMinor         int64      `json:"budget_minor"`
	Currency            *string    `json:"currency"`
	AudienceSize        *int       `json:"audience_size"`
	ApprovalID          *ids.ID    `json:"approval_id"`
	SponsorMembershipID *ids.ID    `json:"sponsor_membership_id"`
	SponsorRoleID       *ids.ID    `json:"sponsor_role_id"`
	LaunchedAt          *time.Time `json:"launched_at"`
	CompletedAt         *time.Time `json:"completed_at"`
	CampaignVersion     int64      `json:"campaign_version"`
	Version             int64      `json:"version"`
}

type CampaignCreate struct {
	PropertyID      ids.ID `json:"property_id"`
	Name            string `json:"name"`
	SegmentID       ids.ID `json:"segment_id"`
	TemplateID      ids.ID `json:"template_id"`
	ChannelCategory string `json:"channel_category"`
	BudgetMinor     int64  `json:"budget_minor"`
	Currency        string `json:"currency,omitempty"`
	Reason          string `json:"reason"`
}

type CampaignLaunch struct {
	Version     int64      `json:"version"`
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
	ApprovalID  ids.ID     `json:"approval_id,omitempty"`
	Reason      string     `json:"reason"`
}

type BrandProfileCommand struct {
	ExpectedVersion int64   `json:"expected_version"`
	DisplayName     string  `json:"display_name"`
	Tagline         string  `json:"tagline"`
	Description     string  `json:"description"`
	LogoMediaID     *ids.ID `json:"logo_media_id,omitempty"`
	CoverMediaID    *ids.ID `json:"cover_media_id,omitempty"`
	PrimaryColor    string  `json:"primary_color"`
	Reason          string  `json:"reason"`
}

type StorefrontCollection struct {
	ID                 ids.ID   `json:"id"`
	PropertyID         ids.ID   `json:"property_id"`
	Name               string   `json:"name"`
	ListingIDs         []ids.ID `json:"listing_ids"`
	PackageIDs         []ids.ID `json:"package_ids"`
	PromotionIDs       []ids.ID `json:"promotion_ids"`
	State              string   `json:"state"`
	PublicationVersion int64    `json:"publication_version"`
	ApprovalID         *ids.ID  `json:"approval_id"`
	MarketplaceState   string   `json:"marketplace_state"`
	Version            int64    `json:"version"`
}

type StorefrontCreate struct {
	PropertyID   ids.ID   `json:"property_id"`
	Name         string   `json:"name"`
	ListingIDs   []ids.ID `json:"listing_ids"`
	PackageIDs   []ids.ID `json:"package_ids"`
	PromotionIDs []ids.ID `json:"promotion_ids"`
	Reason       string   `json:"reason"`
}

type StorefrontAction struct {
	Version    int64  `json:"version"`
	ApprovalID ids.ID `json:"approval_id,omitempty"`
	Reason     string `json:"reason"`
}

type UpsellOffer struct {
	ID            ids.ID   `json:"id"`
	PropertyID    ids.ID   `json:"property_id"`
	AddOnID       ids.ID   `json:"add_on_id"`
	Category      string   `json:"category"`
	MinNights     int      `json:"min_nights"`
	Sources       []string `json:"sources"`
	DailyCapacity *int     `json:"daily_capacity"`
	ValidFrom     string   `json:"valid_from"`
	ValidTo       string   `json:"valid_to"`
	State         string   `json:"state"`
	Version       int64    `json:"version"`
}

type UpsellOfferCreate struct {
	PropertyID    ids.ID   `json:"property_id"`
	AddOnID       ids.ID   `json:"add_on_id"`
	Category      string   `json:"category"`
	MinNights     int      `json:"min_nights"`
	Sources       []string `json:"sources"`
	DailyCapacity *int     `json:"daily_capacity,omitempty"`
	ValidFrom     string   `json:"valid_from"`
	ValidTo       string   `json:"valid_to"`
	Reason        string   `json:"reason"`
}

type UpsellAcceptanceCreate struct {
	ReservationID ids.ID `json:"reservation_id"`
	ServiceDate   string `json:"service_date"`
	Quantity      int    `json:"quantity"`
	Reason        string `json:"reason"`
}

type UpsellAcceptance struct {
	ID              ids.ID  `json:"id"`
	PropertyID      ids.ID  `json:"property_id"`
	OfferID         ids.ID  `json:"offer_id"`
	ReservationID   ids.ID  `json:"reservation_id"`
	ServiceDate     string  `json:"service_date"`
	Quantity        int     `json:"quantity"`
	AddOnVersion    int64   `json:"add_on_version"`
	UnitAmountMinor int64   `json:"unit_amount_minor"`
	AmountMinor     int64   `json:"amount_minor"`
	Currency        string  `json:"currency"`
	State           string  `json:"state"`
	FolioItemID     *ids.ID `json:"folio_item_id"`
	Version         int64   `json:"version"`
}

type UpsellFolioLink struct {
	Version     int64  `json:"version"`
	FolioItemID ids.ID `json:"folio_item_id"`
	Reason      string `json:"reason"`
}

type FunnelObservationCommand struct {
	OrganisationID  ids.ID    `json:"organisation_id"`
	PropertyID      ids.ID    `json:"property_id"`
	ListingID       *ids.ID   `json:"listing_id,omitempty"`
	Stage           string    `json:"stage"`
	SourceReference string    `json:"source_reference"`
	Channel         string    `json:"channel,omitempty"`
	CampaignID      *ids.ID   `json:"campaign_id,omitempty"`
	Quantity        int       `json:"quantity"`
	ObservedAt      time.Time `json:"observed_at"`
}

func (h *Handler) registerMarketing(r chi.Router) {
	r.Get("/lead-sources", h.leadSources)
	r.Post("/lead-sources", h.leadSourceCreate)
	r.Get("/leads", h.leadList)
	r.Post("/leads", h.leadCreate)
	r.Get("/leads/{id}", h.leadGet)
	r.Patch("/leads/{id}", h.leadPatch)
	r.Post("/leads/{id}/activities", h.leadActivity)
	r.Post("/leads/{id}/merge", h.leadMerge)
	r.Post("/leads/{id}/convert", h.leadConvert)
	r.Get("/lead-assignments", h.leadAssignments)
	r.Post("/lead-assignments", h.leadAssign)
	r.Get("/segments", h.marketingSegments)
	r.Post("/segments", h.marketingSegmentCreate)
	r.Get("/campaigns", h.campaignList)
	r.Post("/campaigns", h.campaignCreate)
	r.Get("/campaigns/{id}", h.campaignGet)
	r.Post("/campaigns/{id}/launch", h.campaignLaunch)
	r.Post("/campaigns/{id}/pause", h.campaignPause)
	r.Post("/campaigns/{id}/resume", h.campaignResume)
	r.Post("/campaigns/{id}/cancel", h.campaignCancel)
	r.Get("/campaigns/{id}/report", h.campaignReport)
	r.Get("/brand", h.brandGet)
	r.Put("/brand", h.brandPut)
	r.Get("/storefront", h.storefrontList)
	r.Post("/storefront", h.storefrontCreate)
	r.Post("/storefront/{id}/publish", h.storefrontPublish)
	r.Post("/storefront/{id}/pause", h.storefrontPause)
	r.Post("/storefront/{id}/withdraw", h.storefrontWithdraw)
	r.Get("/upsell-offers", h.upsellOffers)
	r.Post("/upsell-offers", h.upsellOfferCreate)
	r.Post("/upsell-offers/{id}/state", h.upsellOfferState)
	r.Post("/upsell-offers/{id}/acceptances", h.upsellAccept)
	r.Get("/upsell-acceptances", h.upsellAcceptances)
	r.Post("/upsell-acceptances/{id}/folio-link", h.upsellFolioLink)
	r.Get("/attribution/touchpoints", h.attributionTouchpoints)
	r.Get("/conversion", h.conversionReport)
}

var leadChannels = []string{"MARKETPLACE", "WEBSITE", "OTA", "REFERRAL", "WALK_IN", "PHONE", "SOCIAL", "PARTNER", "EVENT", "CF_MEET"}

func (h *Handler) leadSources(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "leads:read", `SELECT to_jsonb(x) FROM lead_sources x WHERE organisation_id=$1 AND property_id=$2 ORDER BY code LIMIT $3 OFFSET $4`)
}

func (h *Handler) leadSourceCreate(w http.ResponseWriter, r *http.Request) {
	var in LeadSourceCreate
	if !decode(w, r, &in) {
		return
	}
	if in.AttributionWindowDays == 0 {
		in.AttributionWindowDays = 30
	}
	if in.ResponseSLASeconds == 0 {
		in.ResponseSLASeconds = 7200
	}
	if in.PropertyID.ValidateV7() != nil || len(in.Code) < 2 || len(in.Code) > 60 || strings.ToUpper(in.Code) != in.Code || !opsText(in.Name, 1, 200) || !slices.Contains(leadChannels, in.Channel) || in.AttributionWindowDays < 1 || in.AttributionWindowDays > 365 || in.ResponseSLASeconds < 300 || in.ResponseSLASeconds > 604800 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, upper-case code, name, approved channel, attribution window, response SLA and reason required."))
		return
	}
	h.execute(w, r, "leads:source:"+in.PropertyID.String(), in, "leads:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "leads:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO lead_sources(id,organisation_id,property_id,code,name,channel,attribution_window_days,response_sla_seconds,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, t.Organisation, in.PropertyID, in.Code, in.Name, in.Channel, in.AttributionWindowDays, in.ResponseSLASeconds, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM lead_sources x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "lead_source", "CREATED", "", "ACTIVE", "", 1, in)
	})
}

func leadDedupeKey(property ids.ID, source, external string) string {
	return security.SHA256Hex([]byte(property.String() + "\x00" + source + "\x00" + strings.ToLower(strings.TrimSpace(external))))
}

func (h *Handler) leadList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.opsList(w, r, "leads:read", `SELECT to_jsonb(x) FROM leads x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) AND ($4='' OR source_id::text=$4) ORDER BY first_response_due_at,id LIMIT $5 OFFSET $6`, q.Get("state"), q.Get("source_id"))
}

// createLead is shared by staff capture and signed source intake. The dedupe
// key is the source channel plus external reference; a duplicate returns the
// existing live lead with no second effect.
func (h *Handler) createLead(ctx context.Context, tx pgx.Tx, t tenant, in LeadCreate) (LeadCreateResult, error) {
	var channel string
	var sla int
	if e := tx.QueryRow(ctx, `SELECT channel,response_sla_seconds FROM lead_sources WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state='ACTIVE'`, t.Organisation, in.PropertyID, in.SourceID).Scan(&channel, &sla); e != nil {
		return LeadCreateResult{}, apperrors.Conflict("LEAD_SOURCE_NOT_ACTIVE", "The lead source is not an active approved source for this property.")
	}
	key := leadDedupeKey(in.PropertyID, channel, in.ExternalReference)
	existing, e := typedJSON[Lead](ctx, tx, `SELECT to_jsonb(x) FROM leads x WHERE organisation_id=$1 AND property_id=$2 AND dedupe_key=$3 AND state<>'MERGED'`, t.Organisation, in.PropertyID, key)
	if e == nil {
		return LeadCreateResult{Lead: existing, Duplicate: true}, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return LeadCreateResult{}, e
	}
	if in.ListingID != nil {
		var ok bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM listings WHERE organisation_id=$1 AND property_id=$2 AND id=$3)`, t.Organisation, in.PropertyID, *in.ListingID).Scan(&ok); e != nil || !ok {
			return LeadCreateResult{}, apperrors.Conflict("LISTING_NOT_IN_PROPERTY", "The listing does not belong to this property.")
		}
	}
	id := ids.New()
	now := h.Auth.Service.Now().UTC()
	if _, e = tx.Exec(ctx, `INSERT INTO leads(id,organisation_id,property_id,source_id,campaign_id,listing_id,guest_id,channel,external_reference,dedupe_key,contact_reference,interest,first_response_due_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, id, t.Organisation, in.PropertyID, in.SourceID, in.CampaignID, in.ListingID, in.GuestID, channel, in.ExternalReference, key, in.ContactReference, in.Interest, now.Add(time.Duration(sla)*time.Second), nilIfZero(t.Account)); e != nil {
		return LeadCreateResult{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO attribution_touchpoints(id,organisation_id,property_id,lead_id,guest_id,campaign_id,source_id,channel,touch_kind,source_reference,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'LEAD_CAPTURE',$9,$10) ON CONFLICT DO NOTHING`, ids.New(), t.Organisation, in.PropertyID, id, in.GuestID, in.CampaignID, in.SourceID, channel, "lead-capture:"+id.String(), now); e != nil {
		return LeadCreateResult{}, e
	}
	lead, e := opsGet[Lead](ctx, tx, "leads", t.Organisation, id)
	if e != nil {
		return LeadCreateResult{}, e
	}
	return LeadCreateResult{Lead: lead}, h.opsCommit(ctx, tx, t, in.PropertyID, id, "lead", "CREATED", "", "NEW", businessEvents.LeadUpdated, 1, map[string]any{"source_id": in.SourceID, "campaign_id": in.CampaignID, "listing_id": in.ListingID, "channel": channel, "interest": in.Interest})
}

func (h *Handler) leadCreate(w http.ResponseWriter, r *http.Request) {
	var in LeadCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.SourceID.ValidateV7() != nil || !opsText(in.ExternalReference, 1, 200) || len(in.ContactReference) > 300 || !slices.Contains([]string{"STAY", "PURCHASE", "RENTAL", "LEASE", "EVENT", "OTHER"}, in.Interest) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, approved source, external reference, interest and reason required."))
		return
	}
	h.execute(w, r, "leads:create:"+in.PropertyID.String(), in, "leads:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "leads:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		res, e := h.createLead(ctx, tx, t, in)
		if res.Duplicate {
			return 200, res, e
		}
		return 201, res, e
	})
}

func (h *Handler) leadGet(w http.ResponseWriter, r *http.Request) {
	t, _, id, e := h.opsRecordProperty(r, "leads", "leads:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	lead, e := opsGet[Lead](r.Context(), h.Auth.Service.Pool, "leads", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	activities, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM lead_activities x WHERE organisation_id=$1 AND lead_id=$2 ORDER BY occurred_at,id LIMIT 500`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	assignments, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM lead_assignments x WHERE organisation_id=$1 AND lead_id=$2 ORDER BY lead_version LIMIT 200`, t.Organisation, id)
	reply(w, r, 200, map[string]any{"lead": lead, "activities": activities, "assignments": assignments}, e)
}

var leadManualStates = []string{"CONTACTED", "QUALIFIED", "NURTURING", "LOST", "DISQUALIFIED"}

func (h *Handler) leadMutate(w http.ResponseWriter, r *http.Request, version int64, reason, action string, payload any, fn func(context.Context, pgx.Tx, tenant, Lead) (string, map[string]any, error)) {
	_, property, id, e := h.opsRecordProperty(r, "leads", "leads:write", false)
	if e != nil || version < 1 || !validStaffReason(reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "leads:"+action+":"+id.String(), payload, "leads:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "leads:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		lead, e := opsLock[Lead](ctx, tx, "leads", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if lead.Version != version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Lead version has changed.")
		}
		to, details, e := fn(ctx, tx, t, lead)
		if e != nil {
			return 0, nil, e
		}
		out, e := opsGet[Lead](ctx, tx, "leads", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		details["reason"] = reason
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "lead", action, lead.State, to, businessEvents.LeadUpdated, out.Version, details)
	})
}

func (h *Handler) leadPatch(w http.ResponseWriter, r *http.Request) {
	var in LeadPatch
	if !decode(w, r, &in) {
		return
	}
	if in.State != "" && !slices.Contains(leadManualStates, in.State) || in.State == "" && in.FollowUpAt == nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A manual lead state or follow-up time is required; conversion and merge use their own commands."))
		return
	}
	h.leadMutate(w, r, in.Version, in.Reason, "UPDATED", in, func(ctx context.Context, tx pgx.Tx, t tenant, lead Lead) (string, map[string]any, error) {
		state := lead.State
		if in.State != "" {
			state = in.State
		}
		if lead.State == "CONVERTED" || lead.State == "MERGED" || lead.State == "DISQUALIFIED" {
			return "", nil, apperrors.Conflict("LEAD_FINAL", "The lead is final.")
		}
		now := h.Auth.Service.Now().UTC()
		if in.FollowUpAt != nil && (in.FollowUpAt.Before(now) || in.FollowUpAt.After(now.AddDate(1, 0, 0))) {
			return "", nil, apperrors.InvalidRequest("Follow-up must be within the next year.")
		}
		if _, e := tx.Exec(ctx, `UPDATE leads SET state=$3,follow_up_at=coalesce($4,follow_up_at),follow_up_notified_at=CASE WHEN $4::timestamptz IS NOT NULL THEN NULL ELSE follow_up_notified_at END,first_responded_at=CASE WHEN $3 IN ('CONTACTED','QUALIFIED') THEN coalesce(first_responded_at,$5::timestamptz) ELSE first_responded_at END,version=version+1,updated_at=now() WHERE id=$1 AND organisation_id=$2`, lead.ID, t.Organisation, state, in.FollowUpAt, now); e != nil {
			return "", nil, e
		}
		kind := "STATUS_CHANGE"
		if in.State == "" {
			kind = "FOLLOW_UP_SCHEDULED"
		}
		if _, e := tx.Exec(ctx, `INSERT INTO lead_activities(id,organisation_id,property_id,lead_id,kind,summary,occurred_at,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), t.Organisation, lead.PropertyID, lead.ID, kind, in.Reason, now, t.Account); e != nil {
			return "", nil, e
		}
		return state, map[string]any{"follow_up_at": in.FollowUpAt}, nil
	})
}

var leadActivityKinds = []string{"NOTE", "CALL", "MESSAGE", "MEETING", "VIEWING"}

func (h *Handler) leadActivity(w http.ResponseWriter, r *http.Request) {
	var in LeadActivityCreate
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "leads", "leads:write", false)
	now := h.Auth.Service.Now().UTC()
	if e != nil || !slices.Contains(leadActivityKinds, in.Kind) || !opsText(in.Summary, 1, 2000) || len(in.ContextReference) > 300 || in.OccurredAt.IsZero() || in.OccurredAt.After(now.Add(5*time.Minute)) {
		if e == nil {
			e = apperrors.InvalidRequest("Supported activity kind, summary and actual occurrence time required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "leads:activity:"+id.String(), in, "leads:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "leads:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		lead, e := opsLock[Lead](ctx, tx, "leads", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		aid := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO lead_activities(id,organisation_id,property_id,lead_id,kind,summary,context_reference,occurred_at,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, aid, t.Organisation, property, id, in.Kind, in.Summary, in.ContextReference, in.OccurredAt.UTC(), t.Account); e != nil {
			return 0, nil, e
		}
		// A real outbound contact satisfies the first-response SLA.
		if in.Kind != "NOTE" && lead.FirstRespondedAt == nil {
			if _, e = tx.Exec(ctx, `UPDATE leads SET first_responded_at=$3,state=CASE WHEN state IN ('NEW','ASSIGNED') THEN 'CONTACTED' ELSE state END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.OccurredAt.UTC()); e != nil {
				return 0, nil, e
			}
		}
		out, e := opsGet[Lead](ctx, tx, "leads", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"activity_id": aid, "lead": out}, h.opsCommit(ctx, tx, t, property, id, "lead", "ACTIVITY_"+in.Kind, lead.State, out.State, businessEvents.LeadUpdated, out.Version, map[string]any{"activity_id": aid})
	})
}

func (h *Handler) leadMerge(w http.ResponseWriter, r *http.Request) {
	var in LeadMergeCommand
	if !decode(w, r, &in) {
		return
	}
	h.leadMutate(w, r, in.Version, in.Reason, "MERGED", in, func(ctx context.Context, tx pgx.Tx, t tenant, lead Lead) (string, map[string]any, error) {
		if in.TargetLeadID == lead.ID || lead.State == "CONVERTED" || lead.State == "MERGED" {
			return "", nil, apperrors.Conflict("INVALID_MERGE", "A lead merges once into a different live lead.")
		}
		target, e := opsLock[Lead](ctx, tx, "leads", t.Organisation, in.TargetLeadID)
		if e != nil {
			return "", nil, e
		}
		if target.PropertyID != lead.PropertyID || target.State == "MERGED" {
			return "", nil, apperrors.Conflict("INVALID_MERGE", "The target must be a live lead in the same property.")
		}
		if _, e = tx.Exec(ctx, `UPDATE leads SET state='MERGED',merged_into=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, lead.ID, target.ID); e != nil {
			return "", nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO lead_activities(id,organisation_id,property_id,lead_id,kind,summary,context_reference,occurred_at,actor_id) VALUES($1,$2,$3,$4,'MERGE',$5,$6,now(),$7),($8,$2,$3,$9,'MERGE',$5,$10,now(),$7)`, ids.New(), t.Organisation, lead.PropertyID, lead.ID, in.Reason, "merged-into:"+target.ID.String(), t.Account, ids.New(), target.ID, "merged-from:"+lead.ID.String()); e != nil {
			return "", nil, e
		}
		return "MERGED", map[string]any{"target_lead_id": target.ID}, nil
	})
}

// leadConvert links an actual canonical reservation; it never creates or
// verifies a customer identity.
func (h *Handler) leadConvert(w http.ResponseWriter, r *http.Request) {
	var in LeadConvertCommand
	if !decode(w, r, &in) {
		return
	}
	h.leadMutate(w, r, in.Version, in.Reason, "CONVERTED", in, func(ctx context.Context, tx pgx.Tx, t tenant, lead Lead) (string, map[string]any, error) {
		if lead.State == "CONVERTED" || lead.State == "MERGED" || lead.State == "DISQUALIFIED" {
			return "", nil, apperrors.Conflict("LEAD_FINAL", "The lead is final.")
		}
		var ok bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservations r WHERE r.organisation_id=$1 AND r.property_id=$2 AND r.id=$3 AND r.state NOT IN ('CANCELLED','REJECTED','EXPIRED') AND ($4::uuid IS NULL OR EXISTS(SELECT 1 FROM reservation_guests g WHERE g.organisation_id=r.organisation_id AND g.reservation_id=r.id AND g.guest_id=$4)))`, t.Organisation, lead.PropertyID, in.ReservationID, lead.GuestID).Scan(&ok); e != nil {
			return "", nil, e
		}
		if !ok {
			return "", nil, apperrors.Conflict("RESERVATION_NOT_ELIGIBLE", "Conversion requires an actual live reservation in this property for the lead's guest.")
		}
		if _, e := tx.Exec(ctx, `UPDATE leads SET state='CONVERTED',converted_reservation_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, lead.ID, in.ReservationID); e != nil {
			return "", nil, e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO lead_activities(id,organisation_id,property_id,lead_id,kind,summary,context_reference,occurred_at,actor_id) VALUES($1,$2,$3,$4,'CONVERSION',$5,$6,now(),$7)`, ids.New(), t.Organisation, lead.PropertyID, lead.ID, in.Reason, "reservation:"+in.ReservationID.String(), t.Account); e != nil {
			return "", nil, e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO attribution_touchpoints(id,organisation_id,property_id,lead_id,guest_id,reservation_id,campaign_id,source_id,channel,touch_kind,source_reference,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'LEAD_CONVERSION',$10,now()) ON CONFLICT DO NOTHING`, ids.New(), t.Organisation, lead.PropertyID, lead.ID, lead.GuestID, in.ReservationID, lead.CampaignID, lead.SourceID, lead.Channel, "lead-conversion:"+lead.ID.String()); e != nil {
			return "", nil, e
		}
		return "CONVERTED", map[string]any{"reservation_id": in.ReservationID, "campaign_id": lead.CampaignID}, nil
	})
}

func (h *Handler) leadAssignments(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "leads:read", `SELECT to_jsonb(x) FROM lead_assignments x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR lead_id::text=$3) ORDER BY created_at DESC,id LIMIT $4 OFFSET $5`, r.URL.Query().Get("lead_id"))
}

func (h *Handler) leadAssign(w http.ResponseWriter, r *http.Request) {
	var in LeadAssignmentCommand
	if !decode(w, r, &in) {
		return
	}
	if in.LeadID.ValidateV7() != nil || in.AssigneeID.ValidateV7() != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Lead, current version, assignee and reason required."))
		return
	}
	t0, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var property ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM leads WHERE organisation_id=$1 AND id=$2`, t0.Organisation, in.LeadID).Scan(&property); e != nil {
		reply(w, r, 0, nil, opsNotFound(e))
		return
	}
	h.execute(w, r, "leads:assign:"+in.LeadID.String(), in, "leads:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "leads:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		lead, e := opsLock[Lead](ctx, tx, "leads", t.Organisation, in.LeadID)
		if e != nil {
			return 0, nil, e
		}
		if lead.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Lead version has changed.")
		}
		if lead.State == "CONVERTED" || lead.State == "MERGED" || lead.State == "DISQUALIFIED" {
			return 0, nil, apperrors.Conflict("LEAD_FINAL", "The lead is final.")
		}
		ok, e := h.staffAuthority(ctx, tx, t.Organisation, property, in.AssigneeID, "leads:write")
		if e != nil {
			return 0, nil, e
		}
		if !ok {
			return 0, nil, apperrors.PermissionDenied("The assignee must currently hold lead access for this property.")
		}
		state := lead.State
		if state == "NEW" || state == "LOST" {
			state = "ASSIGNED"
		}
		if _, e = tx.Exec(ctx, `UPDATE leads SET assigned_to=$3,state=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, lead.ID, in.AssigneeID, state); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO lead_assignments(id,organisation_id,property_id,lead_id,lead_version,previous_assignee,assignee,reason,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), t.Organisation, property, lead.ID, lead.Version+1, lead.AssignedTo, in.AssigneeID, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[Lead](ctx, tx, "leads", t.Organisation, lead.ID)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, lead.ID, "lead", "ASSIGNED", lead.State, state, businessEvents.LeadUpdated, out.Version, map[string]any{"assignee_id": in.AssigneeID})
	})
}

func (h *Handler) marketingSegments(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "campaigns:read", `SELECT to_jsonb(x)||jsonb_build_object('current_members',(SELECT count(*) FROM `+strings.ReplaceAll(currentSegmentMembers, "=s.", "=x.")+` c)) FROM segments x WHERE organisation_id=$1 AND property_id=$2 ORDER BY created_at DESC LIMIT $3 OFFSET $4`)
}

func (h *Handler) marketingSegmentCreate(w http.ResponseWriter, r *http.Request) {
	var in MarketingSegmentCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.GuestSegmentKey.ValidateV7() != nil || !opsText(in.Name, 1, 200) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, Section 11 guest segment, name and reason required."))
		return
	}
	h.execute(w, r, "segments:create:"+in.PropertyID.String(), in, "campaigns:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "campaigns:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		var ok bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_segments WHERE organisation_id=$1 AND property_id=$2 AND segment_key=$3 AND state='ACTIVE')`, t.Organisation, in.PropertyID, in.GuestSegmentKey).Scan(&ok); e != nil {
			return 0, nil, e
		}
		if !ok {
			return 0, nil, apperrors.Conflict("GUEST_SEGMENT_NOT_ACTIVE", "Audience must reference an active Section 11 guest segment in this property.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO segments(id,organisation_id,property_id,guest_segment_key,name,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, t.Organisation, in.PropertyID, in.GuestSegmentKey, in.Name, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM segments x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "marketing_segment", "CREATED", "", "ACTIVE", "", 1, in)
	})
}

func (h *Handler) campaignList(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "campaigns:read", `SELECT to_jsonb(x) FROM campaigns x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) ORDER BY created_at DESC,id LIMIT $4 OFFSET $5`, r.URL.Query().Get("state"))
}

func (h *Handler) campaignCreate(w http.ResponseWriter, r *http.Request) {
	var in CampaignCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.SegmentID.ValidateV7() != nil || in.TemplateID.ValidateV7() != nil || !opsText(in.Name, 1, 200) || !slices.Contains([]string{"EMAIL", "SMS", "PUSH", "MULTI"}, in.ChannelCategory) || in.BudgetMinor < 0 || in.BudgetMinor > 0 && len(in.Currency) != 3 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, marketing segment, approved template, channel category, budget/currency and reason required."))
		return
	}
	h.execute(w, r, "campaigns:create:"+in.PropertyID.String(), in, "campaigns:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "campaigns:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		var ok bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_notification_templates WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND kind='CAMPAIGN_MESSAGE' AND purpose='MARKETING' AND state='ACTIVE')`, t.Organisation, in.PropertyID, in.TemplateID).Scan(&ok); e != nil {
			return 0, nil, e
		}
		if !ok {
			return 0, nil, apperrors.Conflict("TEMPLATE_NOT_APPROVED", "Campaigns require an active approved CAMPAIGN_MESSAGE marketing template.")
		}
		var currency any
		if in.BudgetMinor > 0 {
			currency = in.Currency
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO campaigns(id,organisation_id,property_id,name,segment_id,template_id,channel_category,budget_minor,currency,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, t.Organisation, in.PropertyID, in.Name, in.SegmentID, in.TemplateID, in.ChannelCategory, in.BudgetMinor, currency, t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO campaign_versions(id,organisation_id,campaign_id,campaign_version,snapshot,created_by) VALUES($1,$2,$3,1,$4,$5)`, ids.New(), t.Organisation, id, marshalCommercial(in), t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[Campaign](ctx, tx, "campaigns", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "campaign", "CREATED", "", "DRAFT", businessEvents.CampaignUpdated, 1, map[string]any{"segment_id": in.SegmentID, "template_id": in.TemplateID, "budget_minor": in.BudgetMinor})
	})
}

func (h *Handler) campaignGet(w http.ResponseWriter, r *http.Request) {
	t, _, id, e := h.opsRecordProperty(r, "campaigns", "campaigns:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	c, e := opsGet[Campaign](r.Context(), h.Auth.Service.Pool, "campaigns", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	members, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('eligibility',eligibility,'count',count(*)) FROM campaign_members WHERE organisation_id=$1 AND campaign_id=$2 GROUP BY eligibility ORDER BY eligibility`, t.Organisation, id)
	reply(w, r, 200, map[string]any{"campaign": c, "members": members}, e)
}

// campaignAudience counts current Section 11 segment members.
func campaignAudience(ctx context.Context, tx pgx.Tx, org ids.ID, c Campaign) (int, error) {
	var n int
	e := tx.QueryRow(ctx, `SELECT count(DISTINCT c.guest_id) FROM segments s CROSS JOIN LATERAL `+currentSegmentMembers+` c WHERE s.organisation_id=$1 AND s.id=$2`, org, c.SegmentID).Scan(&n)
	return n, e
}

// CampaignApprovalAudience is the bulk threshold above which an exact
// independent approval is required (budgets always require one).
const CampaignApprovalAudience = 50

// currentSegmentMembers selects current members of the active version of a
// marketing segment's Section 11 guest segment (latest evaluation only).
const currentSegmentMembers = `(SELECT m.guest_id FROM guest_segments gs JOIN guest_segment_memberships m ON m.organisation_id=gs.organisation_id AND m.segment_id=gs.id AND m.state='MEMBER' WHERE gs.organisation_id=s.organisation_id AND gs.property_id=s.property_id AND gs.segment_key=s.guest_segment_key AND gs.state='ACTIVE' AND m.evaluation_id=(SELECT x.evaluation_id FROM guest_segment_memberships x WHERE x.organisation_id=gs.organisation_id AND x.segment_id=gs.id ORDER BY x.created_at DESC,x.id DESC LIMIT 1))`

func (h *Handler) campaignLaunch(w http.ResponseWriter, r *http.Request) {
	var in CampaignLaunch
	if !decode(w, r, &in) {
		return
	}
	h.campaignCommand(w, r, in.Version, in.Reason, "LAUNCH", true, in, func(ctx context.Context, tx pgx.Tx, t tenant, c Campaign) (string, error) {
		if c.State != "DRAFT" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Only draft campaigns can be launched.")
		}
		audience, e := campaignAudience(ctx, tx, t.Organisation, c)
		if e != nil {
			return "", e
		}
		var tpl bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_notification_templates WHERE organisation_id=$1 AND id=$2 AND state='ACTIVE')`, t.Organisation, c.TemplateID).Scan(&tpl); e != nil {
			return "", e
		}
		if !tpl {
			return "", apperrors.Conflict("TEMPLATE_NOT_APPROVED", "The approved template was retired.")
		}
		var approval any
		if audience >= CampaignApprovalAudience || c.BudgetMinor > 0 {
			target, e := resolve(ctx, tx, t.Organisation, "PROPERTY", c.PropertyID)
			if e != nil {
				return "", e
			}
			var amount *int64
			currency := ""
			if c.BudgetMinor > 0 {
				amount, currency = &c.BudgetMinor, *c.Currency
			}
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CAMPAIGN_LAUNCH", target.Scope, in.ApprovalID, map[string]any{"campaign_id": c.ID, "campaign_version": c.CampaignVersion, "audience_size": audience}, "", amount, currency); e != nil {
				return "", e
			}
			approval = in.ApprovalID
		}
		now := h.Auth.Service.Now().UTC()
		to := "RUNNING"
		var scheduled any
		if in.ScheduledAt != nil && in.ScheduledAt.After(now.Add(time.Minute)) {
			if in.ScheduledAt.After(now.AddDate(0, 6, 0)) {
				return "", apperrors.InvalidRequest("Schedule within six months.")
			}
			to, scheduled = "SCHEDULED", in.ScheduledAt.UTC()
		}
		_, e = tx.Exec(ctx, `UPDATE campaigns SET state=$2,scheduled_at=$3,audience_size=$4,approval_id=$5,sponsor_membership_id=$6,sponsor_role_id=$7,launched_at=CASE WHEN $2='RUNNING' THEN now() END,version=version+1,updated_at=now() WHERE id=$1`, c.ID, to, scheduled, audience, approval, t.Member, t.Role)
		return to, e
	})
}

func (h *Handler) campaignPause(w http.ResponseWriter, r *http.Request) {
	h.campaignSimple(w, r, "PAUSE", []string{"RUNNING"}, "PAUSED")
}
func (h *Handler) campaignResume(w http.ResponseWriter, r *http.Request) {
	h.campaignSimple(w, r, "RESUME", []string{"PAUSED"}, "RUNNING")
}
func (h *Handler) campaignCancel(w http.ResponseWriter, r *http.Request) {
	h.campaignSimple(w, r, "CANCEL", []string{"DRAFT", "SCHEDULED", "RUNNING", "PAUSED"}, "CANCELLED")
}

func (h *Handler) campaignSimple(w http.ResponseWriter, r *http.Request, action string, from []string, to string) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	h.campaignCommand(w, r, in.Version, in.Reason, action, action == "RESUME", in, func(ctx context.Context, tx pgx.Tx, t tenant, c Campaign) (string, error) {
		if !slices.Contains(from, c.State) {
			return "", apperrors.Conflict("INVALID_TRANSITION", "The campaign action is not allowed from "+c.State+".")
		}
		if _, e := tx.Exec(ctx, `UPDATE campaigns SET state=$2,completed_at=CASE WHEN $2 IN ('CANCELLED') THEN now() ELSE completed_at END,version=version+1,updated_at=now() WHERE id=$1`, c.ID, to); e != nil {
			return "", e
		}
		if to == "CANCELLED" || to == "PAUSED" {
			// Pending sends stop before acceptance; accepted sends are never recalled.
			if _, e := tx.Exec(ctx, `UPDATE guest_notification_jobs j SET state='CANCELLED',outcome_reason='CAMPAIGN_'||$3,version=version+1,updated_at=now() FROM campaign_members m WHERE m.organisation_id=j.organisation_id AND m.id=j.source_id AND j.source_kind='CAMPAIGN_MEMBER' AND m.campaign_id=$1 AND j.organisation_id=$2 AND j.state IN ('PENDING','RETRY')`, c.ID, t.Organisation, to); e != nil {
				return "", e
			}
		}
		return to, nil
	})
}

func (h *Handler) campaignCommand(w http.ResponseWriter, r *http.Request, version int64, reason, action string, mfa bool, payload any, fn func(context.Context, pgx.Tx, tenant, Campaign) (string, error)) {
	_, property, id, e := h.opsRecordProperty(r, "campaigns", "campaigns:write", mfa)
	if e != nil || version < 1 || !validStaffReason(reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "campaigns:"+action+":"+id.String(), payload, "campaigns:write", "PROPERTY", property, mfa, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "campaigns:write", "PROPERTY", property, mfa)
		if e != nil {
			return 0, nil, e
		}
		c, e := opsLock[Campaign](ctx, tx, "campaigns", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Campaign version has changed.")
		}
		to, e := fn(ctx, tx, t, c)
		if e != nil {
			return 0, nil, e
		}
		out, e := opsGet[Campaign](ctx, tx, "campaigns", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "campaign", action, c.State, to, businessEvents.CampaignUpdated, out.Version, map[string]any{"reason": reason, "audience_size": out.AudienceSize, "approval_id": out.ApprovalID})
	})
}

// campaignReport is source-backed: delivery is Notification Service
// acceptance only and spend is the configured budget reference, never ledger.
func (h *Handler) campaignReport(w http.ResponseWriter, r *http.Request) {
	t, _, id, e := h.opsRecordProperty(r, "campaigns", "campaigns:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	c, e := opsGet[Campaign](r.Context(), h.Auth.Service.Pool, "campaigns", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var eligible, suppressed, accepted, pending, attributed int
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT count(*) FILTER (WHERE m.eligibility='ELIGIBLE'),count(*) FILTER (WHERE m.eligibility<>'ELIGIBLE'),count(*) FILTER (WHERE j.state='ACCEPTED'),count(*) FILTER (WHERE j.state IN ('PENDING','RUNNING','RETRY')),(SELECT count(DISTINCT reservation_id) FROM attribution_touchpoints WHERE organisation_id=$1 AND campaign_id=$2 AND touch_kind='LEAD_CONVERSION') FROM campaign_members m LEFT JOIN guest_notification_jobs j ON j.id=m.notification_job_id WHERE m.organisation_id=$1 AND m.campaign_id=$2`, t.Organisation, id).Scan(&eligible, &suppressed, &accepted, &pending, &attributed); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	reply(w, r, 200, map[string]any{"campaign_id": id, "state": c.State, "eligible_members": eligible, "suppressed_members": suppressed, "accepted_by_notification_service": accepted, "pending_dispatch": pending, "delivery_confirmed": nil, "delivery_basis": "NOTIFICATION_SERVICE_ACCEPTANCE_ONLY", "attributed_reservations": attributed, "spend_reference": map[string]any{"budget_minor": c.BudgetMinor, "currency": c.Currency, "basis": "CONFIGURED_BUDGET_NOT_PAYMENT_LEDGER"}}, nil)
}

func (h *Handler) brandGet(w http.ResponseWriter, r *http.Request) {
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "brand:read", "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := oneJSON(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM brand_profiles x WHERE organisation_id=$1`, t.Organisation)
	reply(w, r, 200, out, e)
}

func (h *Handler) brandPut(w http.ResponseWriter, r *http.Request) {
	var in BrandProfileCommand
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 0 || !opsText(in.DisplayName, 1, 120) || len(in.Tagline) > 200 || len(in.Description) > 4000 || in.PrimaryColor != "" && (len(in.PrimaryColor) != 7 || in.PrimaryColor[0] != '#') || in.LogoMediaID != nil && in.LogoMediaID.ValidateV7() != nil || in.CoverMediaID != nil && in.CoverMediaID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Expected version, display name, bounded copy, Media references, colour and reason required."))
		return
	}
	h.execute(w, r, "brand:put", in, "brand:write", "COMPANY", ids.Nil, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "brand:write", "COMPANY", ids.Nil, false)
		if e != nil {
			return 0, nil, e
		}
		var id ids.ID
		var version int64
		e = tx.QueryRow(ctx, `SELECT id,version FROM brand_profiles WHERE organisation_id=$1 FOR UPDATE`, t.Organisation).Scan(&id, &version)
		switch {
		case errors.Is(e, pgx.ErrNoRows):
			if in.ExpectedVersion != 0 {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Brand profile does not exist.")
			}
			id, version = ids.New(), 1
			_, e = tx.Exec(ctx, `INSERT INTO brand_profiles(id,organisation_id,display_name,tagline,description,logo_media_id,cover_media_id,primary_color,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, t.Organisation, in.DisplayName, in.Tagline, in.Description, in.LogoMediaID, in.CoverMediaID, in.PrimaryColor, t.Account)
		case e == nil:
			if version != in.ExpectedVersion {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Brand profile version has changed.")
			}
			version++
			_, e = tx.Exec(ctx, `UPDATE brand_profiles SET display_name=$2,tagline=$3,description=$4,logo_media_id=$5,cover_media_id=$6,primary_color=$7,version=version+1,updated_at=now() WHERE id=$1`, id, in.DisplayName, in.Tagline, in.Description, in.LogoMediaID, in.CoverMediaID, in.PrimaryColor)
		}
		if e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM brand_profiles x WHERE id=$1`, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "brand_profile", "SAVED", "", "ACTIVE", businessEvents.StorefrontUpdated, version, map[string]any{"display_name": in.DisplayName, "logo_media_id": in.LogoMediaID})
	})
}

func (h *Handler) storefrontList(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "brand:read", `SELECT to_jsonb(x) FROM storefront_collections x WHERE organisation_id=$1 AND property_id=$2 ORDER BY created_at DESC,id LIMIT $3 OFFSET $4`)
}

func validIDList(list []ids.ID, min, max int) bool {
	if len(list) < min || len(list) > max {
		return false
	}
	seen := map[ids.ID]bool{}
	for _, id := range list {
		if id.ValidateV7() != nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// storefrontSupply verifies every referenced supply item is canonical,
// in-property and currently sellable.
func storefrontSupply(ctx context.Context, tx pgx.Tx, org, property ids.ID, listings, packages, promotions []ids.ID) error {
	var bad int
	if e := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM unnest($3::uuid[]) l(id) WHERE NOT EXISTS(SELECT 1 FROM listings x WHERE x.organisation_id=$1 AND x.property_id=$2 AND x.id=l.id AND x.state='PUBLISHED'))+(SELECT count(*) FROM unnest($4::uuid[]) p(id) WHERE NOT EXISTS(SELECT 1 FROM packages x WHERE x.organisation_id=$1 AND x.property_id=$2 AND x.id=p.id AND x.state='ACTIVE'))+(SELECT count(*) FROM unnest($5::uuid[]) p(id) WHERE NOT EXISTS(SELECT 1 FROM promotions x WHERE x.organisation_id=$1 AND x.property_id=$2 AND x.id=p.id AND x.state='ACTIVE'))`, org, property, listings, packages, promotions).Scan(&bad); e != nil {
		return e
	}
	if bad > 0 {
		return apperrors.Conflict("STOREFRONT_SUPPLY_NOT_SELLABLE", "Every listing must be published and every package/promotion active in this property.")
	}
	return nil
}

func (h *Handler) storefrontCreate(w http.ResponseWriter, r *http.Request) {
	var in StorefrontCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PackageIDs == nil {
		in.PackageIDs = []ids.ID{}
	}
	if in.PromotionIDs == nil {
		in.PromotionIDs = []ids.ID{}
	}
	if in.PropertyID.ValidateV7() != nil || !opsText(in.Name, 1, 200) || !validIDList(in.ListingIDs, 1, 100) || !validIDList(in.PackageIDs, 0, 50) || !validIDList(in.PromotionIDs, 0, 50) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, name, 1-100 listings, optional packages/promotions and reason required."))
		return
	}
	h.execute(w, r, "storefront:create:"+in.PropertyID.String(), in, "brand:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "brand:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if e = storefrontSupply(ctx, tx, t.Organisation, in.PropertyID, in.ListingIDs, in.PackageIDs, in.PromotionIDs); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO storefront_collections(id,organisation_id,property_id,name,listing_ids,package_ids,promotion_ids,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, in.PropertyID, in.Name, in.ListingIDs, in.PackageIDs, in.PromotionIDs, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[StorefrontCollection](ctx, tx, "storefront_collections", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "storefront_collection", "CREATED", "", "DRAFT", "", 1, in)
	})
}

func (h *Handler) storefrontAction(w http.ResponseWriter, r *http.Request, action string, from []string, to string, mfa bool) {
	var in StorefrontAction
	if !decode(w, r, &in) {
		return
	}
	capability := "brand:write"
	if action == "PUBLISH" {
		capability = "storefront:publish"
	}
	_, property, id, e := h.opsRecordProperty(r, "storefront_collections", capability, mfa)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "storefront:"+action+":"+id.String(), in, capability, "PROPERTY", property, mfa, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, capability, "PROPERTY", property, mfa)
		if e != nil {
			return 0, nil, e
		}
		c, e := opsLock[StorefrontCollection](ctx, tx, "storefront_collections", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != in.Version || !slices.Contains(from, c.State) {
			return 0, nil, apperrors.Conflict("INVALID_TRANSITION", "The storefront version or state does not allow this action.")
		}
		var approval any
		if action == "PUBLISH" {
			if e = h.commercialGate(ctx, tx, t, property); e != nil {
				return 0, nil, e
			}
			if e = storefrontSupply(ctx, tx, t.Organisation, property, c.ListingIDs, c.PackageIDs, c.PromotionIDs); e != nil {
				return 0, nil, e
			}
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_STOREFRONT_PUBLICATION", target.Scope, in.ApprovalID, map[string]any{"collection_id": c.ID, "version": c.Version, "listing_ids": c.ListingIDs, "package_ids": c.PackageIDs, "promotion_ids": c.PromotionIDs}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		}
		publication := c.PublicationVersion + 1
		snapshot := map[string]any{"collection_id": c.ID, "property_id": property, "name": c.Name, "listing_ids": c.ListingIDs, "package_ids": c.PackageIDs, "promotion_ids": c.PromotionIDs, "action": action, "publication_version": publication}
		if _, e = tx.Exec(ctx, `UPDATE storefront_collections SET state=$3,publication_version=$4,approval_id=coalesce($5,approval_id),marketplace_state='PROJECTION_PUBLISHED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, to, publication, approval); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO storefront_publications(id,organisation_id,collection_id,publication_version,action,snapshot,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, ids.New(), t.Organisation, id, publication, action, marshalCommercial(snapshot), t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[StorefrontCollection](ctx, tx, "storefront_collections", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		// The projection reaches Marketplace through the outbox; Marketplace
		// keeps customer-facing presentation, discovery and checkout authority.
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "storefront_collection", action, c.State, to, businessEvents.StorefrontUpdated, out.Version, snapshot)
	})
}

func (h *Handler) storefrontPublish(w http.ResponseWriter, r *http.Request) {
	h.storefrontAction(w, r, "PUBLISH", []string{"DRAFT", "PAUSED", "WITHDRAWN"}, "PUBLISHED", true)
}
func (h *Handler) storefrontPause(w http.ResponseWriter, r *http.Request) {
	h.storefrontAction(w, r, "PAUSE", []string{"PUBLISHED"}, "PAUSED", false)
}
func (h *Handler) storefrontWithdraw(w http.ResponseWriter, r *http.Request) {
	h.storefrontAction(w, r, "WITHDRAW", []string{"PUBLISHED", "PAUSED"}, "WITHDRAWN", false)
}

func (h *Handler) upsellOffers(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "products:read", `SELECT to_jsonb(x) FROM upsell_offers x WHERE organisation_id=$1 AND property_id=$2 ORDER BY created_at DESC,id LIMIT $3 OFFSET $4`)
}

var upsellCategories = []string{"BREAKFAST", "AIRPORT_TRANSFER", "SPA", "EVENT", "EARLY_CHECK_IN", "LATE_CHECKOUT", "CLEANING", "OTHER"}

func (h *Handler) upsellOfferCreate(w http.ResponseWriter, r *http.Request) {
	var in UpsellOfferCreate
	if !decode(w, r, &in) {
		return
	}
	if in.MinNights == 0 {
		in.MinNights = 1
	}
	if in.Sources == nil {
		in.Sources = []string{}
	}
	from, e1 := time.Parse(time.DateOnly, in.ValidFrom)
	to, e2 := time.Parse(time.DateOnly, in.ValidTo)
	if in.PropertyID.ValidateV7() != nil || in.AddOnID.ValidateV7() != nil || !slices.Contains(upsellCategories, in.Category) || in.MinNights < 1 || in.MinNights > 365 || len(in.Sources) > 10 || in.DailyCapacity != nil && (*in.DailyCapacity < 1 || *in.DailyCapacity > 10000) || e1 != nil || e2 != nil || to.Before(from) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, canonical add-on, category, eligibility, validity window and reason required."))
		return
	}
	h.execute(w, r, "upsell:create:"+in.PropertyID.String(), in, "products:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "products:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		var state string
		if e = tx.QueryRow(ctx, `SELECT state FROM add_ons WHERE organisation_id=$1 AND property_id=$2 AND id=$3`, t.Organisation, in.PropertyID, in.AddOnID).Scan(&state); e != nil {
			return 0, nil, opsNotFound(e)
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO upsell_offers(id,organisation_id,property_id,add_on_id,category,min_nights,sources,daily_capacity,valid_from,valid_to,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, t.Organisation, in.PropertyID, in.AddOnID, in.Category, in.MinNights, in.Sources, in.DailyCapacity, in.ValidFrom, in.ValidTo, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[UpsellOffer](ctx, tx, "upsell_offers", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "upsell_offer", "CREATED", "", "DRAFT", businessEvents.StorefrontUpdated, 1, in)
	})
}

func (h *Handler) upsellOfferState(w http.ResponseWriter, r *http.Request) {
	var in OpsStateCommand
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "upsell_offers", "products:write", false)
	if e != nil || in.Version < 1 || !slices.Contains([]string{"ACTIVE", "PAUSED", "RETIRED"}, in.State) || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version, ACTIVE/PAUSED/RETIRED and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "upsell:state:"+id.String(), in, "products:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "products:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		o, e := opsLock[UpsellOffer](ctx, tx, "upsell_offers", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if o.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Offer version has changed.")
		}
		if in.State == "ACTIVE" {
			var state string
			if e = tx.QueryRow(ctx, `SELECT state FROM add_ons WHERE organisation_id=$1 AND id=$2`, t.Organisation, o.AddOnID).Scan(&state); e != nil {
				return 0, nil, e
			}
			if state != "ACTIVE" {
				return 0, nil, apperrors.Conflict("ADD_ON_NOT_ACTIVE", "The canonical add-on must be active before the offer is.")
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE upsell_offers SET state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[UpsellOffer](ctx, tx, "upsell_offers", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "upsell_offer", "STATE_CHANGED", o.State, in.State, businessEvents.StorefrontUpdated, out.Version, map[string]any{"reason": in.Reason})
	})
}

// upsellAccept freezes the canonical Section 7 add-on price; payment and
// folio authority remain with PMS folios and Payment Service.
func (h *Handler) upsellAccept(w http.ResponseWriter, r *http.Request) {
	var in UpsellAcceptanceCreate
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "upsell_offers", "reservations:modify", false)
	day, de := time.Parse(time.DateOnly, in.ServiceDate)
	if e != nil || in.ReservationID.ValidateV7() != nil || de != nil || in.Quantity < 1 || in.Quantity > 100 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Reservation, service date, quantity and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "upsell:accept:"+id.String()+":"+in.ReservationID.String(), in, "reservations:modify", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:modify", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		o, e := opsLock[UpsellOffer](ctx, tx, "upsell_offers", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if o.State != "ACTIVE" || in.ServiceDate < o.ValidFrom || in.ServiceDate > o.ValidTo {
			return 0, nil, apperrors.Conflict("OFFER_NOT_AVAILABLE", "The offer is not active for this service date.")
		}
		var state, source string
		var from, to time.Time
		if e = tx.QueryRow(ctx, `SELECT state,source,date_from,date_to FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND id=$3 FOR SHARE`, t.Organisation, property, in.ReservationID).Scan(&state, &source, &from, &to); e != nil {
			return 0, nil, opsNotFound(e)
		}
		nights := int(to.Sub(from).Hours() / 24)
		if state != "CONFIRMED" && state != "CHECKED_IN" || nights < o.MinNights || len(o.Sources) > 0 && !slices.Contains(o.Sources, source) || day.Before(from) || day.After(to) {
			return 0, nil, apperrors.Conflict("GUEST_NOT_ELIGIBLE", "The reservation is not eligible for this offer (state, nights, source or service date).")
		}
		if o.DailyCapacity != nil {
			var used int
			if e = tx.QueryRow(ctx, `SELECT coalesce(sum(quantity),0) FROM upsell_acceptances WHERE organisation_id=$1 AND offer_id=$2 AND service_date=$3 AND state<>'CANCELLED'`, t.Organisation, id, in.ServiceDate).Scan(&used); e != nil {
				return 0, nil, e
			}
			if used+in.Quantity > *o.DailyCapacity {
				return 0, nil, apperrors.Conflict("OFFER_CAPACITY_EXHAUSTED", "Daily capacity is exhausted for this service date.")
			}
		}
		var addOn revenue.AddOn
		if addOn, e = typedJSON[revenue.AddOn](ctx, tx, `SELECT to_jsonb(x) FROM add_ons x WHERE organisation_id=$1 AND id=$2 AND state='ACTIVE'`, t.Organisation, o.AddOnID); e != nil {
			return 0, nil, apperrors.Conflict("ADD_ON_NOT_ACTIVE", "The canonical add-on is not active.")
		}
		if in.Quantity > addOn.Definition.MaxQuantity {
			return 0, nil, apperrors.Conflict("QUANTITY_EXCEEDS_ADD_ON", "Quantity exceeds the canonical add-on limit.")
		}
		aid := ids.New()
		amount := addOn.Definition.AmountMinor * int64(in.Quantity)
		if _, e = tx.Exec(ctx, `INSERT INTO upsell_acceptances(id,organisation_id,property_id,offer_id,reservation_id,service_date,quantity,add_on_version,unit_amount_minor,amount_minor,currency,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, aid, t.Organisation, property, id, in.ReservationID, in.ServiceDate, in.Quantity, addOn.Version, addOn.Definition.AmountMinor, amount, addOn.Definition.Currency, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[UpsellAcceptance](ctx, tx, "upsell_acceptances", t.Organisation, aid)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, property, aid, "upsell_acceptance", "ACCEPTED", "", "ACCEPTED_PENDING_FOLIO", businessEvents.StorefrontUpdated, 1, map[string]any{"offer_id": id, "reservation_id": in.ReservationID, "amount_minor": amount, "currency": addOn.Definition.Currency, "paid": false})
	})
}

func (h *Handler) upsellAcceptances(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "reservations:read", `SELECT to_jsonb(x) FROM upsell_acceptances x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR reservation_id::text=$3) ORDER BY created_at DESC,id LIMIT $4 OFFSET $5`, r.URL.Query().Get("reservation_id"))
}

// upsellFolioLink binds the acceptance to the actual PMS folio posting of the
// same amount on the same reservation's folio.
func (h *Handler) upsellFolioLink(w http.ResponseWriter, r *http.Request) {
	var in UpsellFolioLink
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "upsell_acceptances", "folios:write", false)
	if e != nil || in.Version < 1 || in.FolioItemID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version, folio item and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "upsell:folio:"+id.String(), in, "folios:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		a, e := opsLock[UpsellAcceptance](ctx, tx, "upsell_acceptances", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if a.Version != in.Version || a.State != "ACCEPTED_PENDING_FOLIO" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Acceptance version or state has changed.")
		}
		var ok bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folio_items i JOIN folios f ON f.organisation_id=i.organisation_id AND f.id=i.folio_id WHERE i.organisation_id=$1 AND i.id=$2 AND f.reservation_id=$3 AND i.amount_minor=$4 AND i.currency=$5)`, t.Organisation, in.FolioItemID, a.ReservationID, a.AmountMinor, a.Currency).Scan(&ok); e != nil {
			return 0, nil, e
		}
		if !ok {
			return 0, nil, apperrors.Conflict("FOLIO_POSTING_MISMATCH", "No folio posting of the exact amount exists for this reservation.")
		}
		if _, e = tx.Exec(ctx, `UPDATE upsell_acceptances SET state='FOLIO_POSTED',folio_item_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.FolioItemID); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[UpsellAcceptance](ctx, tx, "upsell_acceptances", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "upsell_acceptance", "FOLIO_LINKED", a.State, "FOLIO_POSTED", businessEvents.StorefrontUpdated, out.Version, map[string]any{"folio_item_id": in.FolioItemID, "settlement_authority": "PAYMENT_SERVICE"})
	})
}

func (h *Handler) attributionTouchpoints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.opsList(w, r, "leads:read", `SELECT to_jsonb(x) FROM attribution_touchpoints x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR campaign_id::text=$3) ORDER BY observed_at DESC,id LIMIT $4 OFFSET $5`, q.Get("campaign_id"))
}

var funnelStages = []string{"IMPRESSION", "LISTING_VIEW", "INQUIRY", "HOLD", "RESERVATION", "CHECK_IN", "COMPLETED_STAY", "REVIEW", "REPEAT"}

// conversionReport returns stage counts and rates. A rate whose denominator
// is unsourced or zero is null; nothing is inferred.
func (h *Handler) conversionReport(w http.ResponseWriter, r *http.Request) {
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A property_id scope is required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "analytics:read", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	from, to, e := reputationWindow(r, h.Auth.Service.Now().UTC())
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if e = h.projectConversion(r.Context(), t.Organisation, property); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	counts := map[string]int64{}
	sourced := map[string]bool{}
	rows, e := h.Auth.Service.Pool.Query(r.Context(), `SELECT stage,sum(quantity) FROM funnel_observations WHERE organisation_id=$1 AND property_id=$2 AND observed_at>=$3 AND observed_at<$4 AND ($5='' OR campaign_id::text=$5) GROUP BY stage`, t.Organisation, property, from, to, r.URL.Query().Get("campaign_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	for rows.Next() {
		var stage string
		var n int64
		if e = rows.Scan(&stage, &n); e != nil {
			rows.Close()
			reply(w, r, 0, nil, e)
			return
		}
		counts[stage], sourced[stage] = n, true
	}
	rows.Close()
	stages := []map[string]any{}
	for _, s := range funnelStages {
		var v any
		if sourced[s] {
			v = counts[s]
		}
		stages = append(stages, map[string]any{"stage": s, "count": v})
	}
	rate := func(num, den string) any {
		if !sourced[den] || counts[den] == 0 {
			return nil
		}
		return counts[num] * 10000 / counts[den]
	}
	var leads, responded, breached int
	var avgResponse *float64
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT count(*),count(first_responded_at),count(*) FILTER (WHERE sla_breached),avg(extract(epoch FROM first_responded_at-created_at))::float8 FROM leads WHERE organisation_id=$1 AND property_id=$2 AND created_at>=$3 AND created_at<$4`, t.Organisation, property, from, to).Scan(&leads, &responded, &breached, &avgResponse); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	reply(w, r, 200, map[string]any{"property_id": property, "date_from": from.Format(time.DateOnly), "stages": stages,
		"rates_bps": map[string]any{"listing_view_rate": rate("LISTING_VIEW", "IMPRESSION"), "inquiry_rate": rate("INQUIRY", "LISTING_VIEW"), "inquiry_to_hold": rate("HOLD", "INQUIRY"), "hold_to_reservation": rate("RESERVATION", "HOLD"), "reservation_to_completed_stay": rate("COMPLETED_STAY", "RESERVATION"), "review_rate": rate("REVIEW", "COMPLETED_STAY"), "repeat_stay_rate": rate("REPEAT", "COMPLETED_STAY")},
		"lead_response": map[string]any{"leads": leads, "responded": responded, "sla_breached": breached, "average_first_response_seconds": avgResponse},
		"basis":         "SOURCED_STAGES_ONLY_UNKNOWN_DENOMINATORS_NULL"}, nil)
}

// projectConversion idempotently projects canonical funnel stages from
// canonical records (unique source references prevent duplicates).
func (h *Handler) projectConversion(ctx context.Context, org, property ids.ID) error {
	for _, q := range []string{
		`INSERT INTO funnel_observations(id,organisation_id,property_id,listing_id,stage,source,source_reference,channel,campaign_id,lead_id,observed_at) SELECT business_crm_uuidv7(),l.organisation_id,l.property_id,l.listing_id,'INQUIRY','CANONICAL','lead:'||l.id,l.channel,l.campaign_id,l.id,l.created_at FROM leads l WHERE l.organisation_id=$1 AND l.property_id=$2 AND l.state<>'MERGED' ON CONFLICT DO NOTHING`,
		`INSERT INTO funnel_observations(id,organisation_id,property_id,stage,source,source_reference,channel,observed_at) SELECT business_crm_uuidv7(),h.organisation_id,h.property_id,'HOLD','CANONICAL','hold:'||h.id,h.channel,h.created_at FROM inventory_holds h WHERE h.organisation_id=$1 AND h.property_id=$2 ON CONFLICT DO NOTHING`,
		`INSERT INTO funnel_observations(id,organisation_id,property_id,stage,source,source_reference,channel,campaign_id,lead_id,reservation_id,observed_at) SELECT business_crm_uuidv7(),r.organisation_id,r.property_id,'RESERVATION','CANONICAL','reservation:'||r.id,r.channel,l.campaign_id,l.id,r.id,r.created_at FROM reservations r LEFT JOIN leads l ON l.organisation_id=r.organisation_id AND l.converted_reservation_id=r.id WHERE r.organisation_id=$1 AND r.property_id=$2 AND r.state NOT IN ('HOLD','INQUIRY','REJECTED','EXPIRED') ON CONFLICT DO NOTHING`,
		`INSERT INTO funnel_observations(id,organisation_id,property_id,stage,source,source_reference,reservation_id,observed_at) SELECT business_crm_uuidv7(),s.organisation_id,s.property_id,'CHECK_IN','CANONICAL','check-in:'||s.id,s.reservation_id,s.checked_in_at FROM stay_records s WHERE s.organisation_id=$1 AND s.property_id=$2 ON CONFLICT DO NOTHING`,
		`INSERT INTO funnel_observations(id,organisation_id,property_id,stage,source,source_reference,reservation_id,observed_at) SELECT business_crm_uuidv7(),s.organisation_id,s.property_id,'COMPLETED_STAY','CANONICAL','completed:'||s.id,s.reservation_id,s.checked_out_at FROM stay_records s WHERE s.organisation_id=$1 AND s.property_id=$2 AND s.state='DEPARTED' ON CONFLICT DO NOTHING`,
		`INSERT INTO funnel_observations(id,organisation_id,property_id,stage,source,source_reference,reservation_id,observed_at) SELECT business_crm_uuidv7(),v.organisation_id,v.property_id,'REVIEW','CANONICAL','review:'||v.id,v.reservation_id,v.reviewed_at FROM review_refs v WHERE v.organisation_id=$1 AND v.property_id=$2 AND v.state='ACTIVE' AND v.verification_status='VERIFIED_STAY' ON CONFLICT DO NOTHING`,
		`INSERT INTO funnel_observations(id,organisation_id,property_id,stage,source,source_reference,reservation_id,observed_at) SELECT business_crm_uuidv7(),r.organisation_id,r.property_id,'REPEAT','CANONICAL','repeat:'||r.id,r.id,r.created_at FROM reservations r JOIN reservation_guests g ON g.organisation_id=r.organisation_id AND g.reservation_id=r.id AND g.guest_id IS NOT NULL WHERE r.organisation_id=$1 AND r.property_id=$2 AND r.state NOT IN ('HOLD','INQUIRY','REJECTED','EXPIRED','CANCELLED') AND EXISTS(SELECT 1 FROM stay_records s WHERE s.organisation_id=r.organisation_id AND s.property_id=r.property_id AND s.guest_id=g.guest_id AND s.state='DEPARTED' AND s.checked_out_at<r.created_at) ON CONFLICT DO NOTHING`,
	} {
		if _, e := h.Auth.Service.Pool.Exec(ctx, q, org, property); e != nil {
			return e
		}
	}
	return nil
}

// internalFunnelObservation accepts signed Marketplace exposure facts that
// only Marketplace can source (impressions/views/inquiries).
func (h *Handler) internalFunnelObservation(w http.ResponseWriter, r *http.Request) {
	var in FunnelObservationCommand
	if !decode(w, r, &in) {
		return
	}
	p, ok := serviceauth.PrincipalFrom(r.Context())
	if !ok || p.Service != platform.MarketplaceService || p.Delegation.OrganizationID != in.OrganisationID {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Signed Marketplace organisation delegation required."))
		return
	}
	now := h.Auth.Service.Now().UTC()
	if in.OrganisationID.ValidateV7() != nil || in.PropertyID.ValidateV7() != nil || !slices.Contains([]string{"IMPRESSION", "LISTING_VIEW", "INQUIRY"}, in.Stage) || !opsText(in.SourceReference, 3, 300) || in.Quantity < 1 || in.Quantity > 10000000 || in.ObservedAt.IsZero() || in.ObservedAt.After(now.Add(5*time.Minute)) || len(in.Channel) > 50 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Owning-source stage, unique source reference, quantity and observation time required."))
		return
	}
	tag, e := h.Auth.Service.Pool.Exec(r.Context(), `INSERT INTO funnel_observations(id,organisation_id,property_id,listing_id,stage,source,source_reference,channel,campaign_id,quantity,observed_at) SELECT $1,$2,$3,$4,$5,'MARKETPLACE',$6,$7,$8,$9,$10 WHERE EXISTS(SELECT 1 FROM properties WHERE organisation_id=$2 AND id=$3) AND ($4::uuid IS NULL OR EXISTS(SELECT 1 FROM listings WHERE organisation_id=$2 AND property_id=$3 AND id=$4)) ON CONFLICT DO NOTHING`, ids.New(), in.OrganisationID, in.PropertyID, in.ListingID, in.Stage, in.SourceReference, in.Channel, in.CampaignID, in.Quantity, in.ObservedAt.UTC())
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	reply(w, r, 202, map[string]any{"accepted": tag.RowsAffected() == 1, "duplicate": tag.RowsAffected() == 0}, nil)
}

// ProcessMarketing runs Section 14 durable workers: scheduled launches,
// consent-aware campaign dispatch, completion and lead follow-up/SLA.
func (h *Handler) ProcessMarketing(ctx context.Context, limit int) error {
	return errors.Join(h.ProcessCampaigns(ctx, limit), h.ProcessLeadFollowUps(ctx, limit))
}

// ProcessCampaigns materialises members of running campaigns once, with the
// sponsor's current authority and each guest's current marketing consent.
func (h *Handler) ProcessCampaigns(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM campaigns WHERE state='RUNNING' OR state='SCHEDULED' AND scheduled_at<=$1 ORDER BY updated_at,id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if e = rows.Scan(&r.org, &r.id); e != nil {
			rows.Close()
			return e
		}
		refs = append(refs, r)
	}
	rows.Close()
	var failures []error
	for _, r := range refs {
		if e := h.campaignDispatch(ctx, r.org, r.id); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) campaignDispatch(ctx context.Context, org, id ids.ID) error {
	return withServiceTx(ctx, h, func(ctx context.Context, tx pgx.Tx) error {
		c, e := opsLock[Campaign](ctx, tx, "campaigns", org, id)
		if e != nil {
			return e
		}
		t := workerTenant(org)
		now := h.Auth.Service.Now().UTC()
		if c.State == "SCHEDULED" {
			if c.ScheduledAt == nil || c.ScheduledAt.After(now) {
				return nil
			}
			if _, e = tx.Exec(ctx, `UPDATE campaigns SET state='RUNNING',launched_at=now(),version=version+1,updated_at=now() WHERE id=$1`, id); e != nil {
				return e
			}
			if e = h.opsCommit(ctx, tx, t, c.PropertyID, id, "campaign", "SCHEDULED_START", "SCHEDULED", "RUNNING", businessEvents.CampaignUpdated, c.Version+1, map[string]any{}); e != nil {
				return e
			}
			c.State, c.Version = "RUNNING", c.Version+1
		}
		if c.State != "RUNNING" {
			return nil
		}
		ok, e := h.currentRecordedPMSAuthority(ctx, tx, org, c.PropertyID, *c.SponsorMembershipID, *c.SponsorRoleID, "campaigns:write")
		if e != nil {
			return e
		}
		if !ok {
			// A formerly authorised campaign never continues after revocation.
			if _, e = tx.Exec(ctx, `UPDATE campaigns SET state='PAUSED',version=version+1,updated_at=now() WHERE id=$1`, id); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE guest_notification_jobs j SET state='CANCELLED',outcome_reason='CAMPAIGN_SPONSOR_REVOKED',version=version+1,updated_at=now() FROM campaign_members m WHERE m.id=j.source_id AND j.source_kind='CAMPAIGN_MEMBER' AND m.campaign_id=$1 AND j.state IN ('PENDING','RETRY')`, id); e != nil {
				return e
			}
			return h.opsCommit(ctx, tx, t, c.PropertyID, id, "campaign", "SPONSOR_AUTHORITY_REVOKED", "RUNNING", "PAUSED", businessEvents.CampaignUpdated, c.Version+1, map[string]any{})
		}
		rows, e := tx.Query(ctx, `SELECT DISTINCT c.guest_id FROM segments s CROSS JOIN LATERAL `+currentSegmentMembers+` c WHERE s.organisation_id=$1 AND s.id=$2 AND NOT EXISTS(SELECT 1 FROM campaign_members x WHERE x.campaign_id=$3 AND x.guest_id=c.guest_id) ORDER BY c.guest_id LIMIT 500`, org, c.SegmentID, id)
		if e != nil {
			return e
		}
		guests := []ids.ID{}
		for rows.Next() {
			var g ids.ID
			if e = rows.Scan(&g); e != nil {
				rows.Close()
				return e
			}
			guests = append(guests, g)
		}
		rows.Close()
		for _, g := range guests {
			allowed, e := crm.ConsentAllowed(ctx, tx, org, g, c.PropertyID, "MARKETING", now)
			if e != nil {
				return e
			}
			eligibility := "ELIGIBLE"
			if !allowed {
				eligibility = "SUPPRESSED_NO_CONSENT"
			}
			member := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO campaign_members(id,organisation_id,property_id,campaign_id,guest_id,eligibility) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, member, org, c.PropertyID, id, g, eligibility); e != nil {
				return e
			}
			if eligibility != "ELIGIBLE" {
				continue
			}
			job := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO guest_notification_jobs(id,organisation_id,property_id,recipient_kind,guest_id,source_kind,source_id,kind,purpose,consent_purposes,variables,expires_at) VALUES($1,$2,$3,'GUEST',$4,'CAMPAIGN_MEMBER',$5,'CAMPAIGN_MESSAGE','MARKETING',ARRAY['MARKETING'],$6,$7)`, job, org, c.PropertyID, g, member, marshalCommercial(map[string]any{"campaign_id": id, "campaign_name": c.Name}), now.Add(72*time.Hour)); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE campaign_members SET notification_job_id=$2,version=version+1,updated_at=now() WHERE id=$1`, member, job); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO attribution_touchpoints(id,organisation_id,property_id,guest_id,campaign_id,channel,touch_kind,source_reference,observed_at) VALUES($1,$2,$3,$4,$5,$6,'CAMPAIGN_SEND',$7,$8) ON CONFLICT DO NOTHING`, ids.New(), org, c.PropertyID, g, id, c.ChannelCategory, "campaign-member:"+member.String(), now); e != nil {
				return e
			}
		}
		if len(guests) == 0 {
			var open int
			if e = tx.QueryRow(ctx, `SELECT count(*) FROM campaign_members m JOIN guest_notification_jobs j ON j.id=m.notification_job_id WHERE m.campaign_id=$1 AND j.state IN ('PENDING','RUNNING','RETRY')`, id).Scan(&open); e != nil {
				return e
			}
			var members int
			if e = tx.QueryRow(ctx, `SELECT count(*) FROM campaign_members WHERE campaign_id=$1`, id).Scan(&members); e != nil {
				return e
			}
			if open == 0 && members > 0 {
				if _, e = tx.Exec(ctx, `UPDATE campaigns SET state='COMPLETED',completed_at=now(),version=version+1,updated_at=now() WHERE id=$1`, id); e != nil {
					return e
				}
				return h.opsCommit(ctx, tx, t, c.PropertyID, id, "campaign", "COMPLETED", "RUNNING", "COMPLETED", businessEvents.CampaignUpdated, c.Version+1, map[string]any{"members": members})
			}
		}
		return nil
	})
}

// ProcessLeadFollowUps flags first-response SLA breaches and due follow-ups.
func (h *Handler) ProcessLeadFollowUps(ctx context.Context, limit int) error {
	return withServiceTx(ctx, h, func(ctx context.Context, tx pgx.Tx) error {
		now := h.Auth.Service.Now().UTC()
		rows, e := tx.Query(ctx, `SELECT organisation_id,property_id,id,state,version,first_responded_at IS NULL AND first_response_due_at<$1 AND NOT sla_breached,follow_up_at IS NOT NULL AND follow_up_at<=$1 AND follow_up_notified_at IS NULL FROM leads WHERE state NOT IN ('CONVERTED','MERGED','DISQUALIFIED','LOST') AND ((first_responded_at IS NULL AND first_response_due_at<$1 AND NOT sla_breached) OR (follow_up_at<=$1 AND follow_up_notified_at IS NULL)) ORDER BY first_response_due_at LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if e != nil {
			return e
		}
		type item struct {
			org, property, id ids.ID
			state             string
			version           int64
			breach, follow    bool
		}
		items := []item{}
		for rows.Next() {
			var i item
			if e = rows.Scan(&i.org, &i.property, &i.id, &i.state, &i.version, &i.breach, &i.follow); e != nil {
				rows.Close()
				return e
			}
			items = append(items, i)
		}
		rows.Close()
		for _, i := range items {
			if _, e = tx.Exec(ctx, `UPDATE leads SET sla_breached=sla_breached OR $2,follow_up_notified_at=CASE WHEN $3 THEN now() ELSE follow_up_notified_at END,version=version+1,updated_at=now() WHERE id=$1`, i.id, i.breach, i.follow); e != nil {
				return e
			}
			if i.follow {
				if _, e = tx.Exec(ctx, `INSERT INTO lead_activities(id,organisation_id,property_id,lead_id,kind,summary,occurred_at) VALUES($1,$2,$3,$4,'FOLLOW_UP_DUE','Scheduled follow-up is due',now())`, ids.New(), i.org, i.property, i.id); e != nil {
					return e
				}
			}
			if e = h.opsCommit(ctx, tx, workerTenant(i.org), i.property, i.id, "lead", "FOLLOW_UP_OR_SLA", i.state, i.state, businessEvents.LeadUpdated, i.version+1, map[string]any{"sla_breached": i.breach, "follow_up_due": i.follow}); e != nil {
				return e
			}
		}
		return nil
	})
}
