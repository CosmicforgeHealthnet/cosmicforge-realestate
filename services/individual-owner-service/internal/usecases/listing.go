package usecases

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Listings (§13.7): one aggregate for every category with typed details
// validated per transaction type. Publication validates authority,
// verification, required media, pricing disclosure and availability; every
// publication or edit of a published listing creates an immutable version
// that is emitted to Marketplace.

// AggListing is the event aggregate of listings.
const AggListing = "listing"

// ListingDetails are the category-specific listing fields.
type ListingDetails struct {
	Furnishing        *string  `json:"furnishing,omitempty" v:"enum=UNFURNISHED|SEMI_FURNISHED|FURNISHED"`
	MinTermMonths     *int32   `json:"min_term_months,omitempty" v:"min=1,max=600" doc:"Leases: minimum term"`
	MaxOccupants      *int32   `json:"max_occupants,omitempty" v:"min=1,max=200"`
	PetsAllowed       *bool    `json:"pets_allowed,omitempty"`
	SmokingAllowed    *bool    `json:"smoking_allowed,omitempty"`
	ParkingSpaces     *int32   `json:"parking_spaces,omitempty" v:"min=0,max=1000"`
	TitleDocumentType *string  `json:"title_document_type,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,39}" doc:"Sale: title held (e.g. CERTIFICATE_OF_OCCUPANCY)"`
	MaxGuests         *int32   `json:"max_guests,omitempty" v:"min=1,max=100" doc:"Short-let"`
	MinNights         *int32   `json:"min_nights,omitempty" v:"min=1,max=365"`
	CheckInTime       *string  `json:"check_in_time,omitempty" v:"pattern=([01][0-9]|2[0-3]):[0-5][0-9]"`
	CheckOutTime      *string  `json:"check_out_time,omitempty" v:"pattern=([01][0-9]|2[0-3]):[0-5][0-9]"`
	HouseRules        *string  `json:"house_rules,omitempty" v:"max=4000"`
	InstantBook       *bool    `json:"instant_book,omitempty"`
	PermittedUse      *string  `json:"permitted_use,omitempty" v:"max=200" doc:"Commercial/land use"`
	FitOut            *string  `json:"fit_out,omitempty" v:"enum=SHELL|SEMI_FINISHED|FINISHED"`
	Fenced            *bool    `json:"fenced,omitempty"`
	Highlights        []string `json:"highlights,omitempty" v:"max=10"`
}

func listingDetails(l db.Listing) ListingDetails {
	var d ListingDetails
	_ = json.Unmarshal(l.Details, &d)
	return d
}

// requiredDetails lists the detail fields a transaction type needs to publish.
func requiredDetails(t string, d ListingDetails) []string {
	var missing []string
	need := func(name string, ok bool) {
		if !ok {
			missing = append(missing, name)
		}
	}
	switch t {
	case "RENT_LONG_TERM":
		need("furnishing", d.Furnishing != nil)
		need("min_term_months", d.MinTermMonths != nil)
	case "SHORT_LET":
		need("max_guests", d.MaxGuests != nil)
		need("min_nights", d.MinNights != nil)
		need("check_in_time", d.CheckInTime != nil)
		need("check_out_time", d.CheckOutTime != nil)
	case "SALE":
		need("title_document_type", d.TitleDocumentType != nil)
	case "COMMERCIAL_LEASE":
		need("permitted_use", d.PermittedUse != nil)
		need("min_term_months", d.MinTermMonths != nil)
	case "LAND_LEASE":
		need("permitted_use", d.PermittedUse != nil)
		need("min_term_months", d.MinTermMonths != nil)
	}
	return missing
}

var categoryTransactions = map[string][]string{
	"RESIDENTIAL": {"RENT_LONG_TERM", "SALE", "SHORT_LET"},
	"COMMERCIAL":  {"COMMERCIAL_LEASE", "SALE"},
	"LAND":        {"LAND_LEASE", "SALE"},
	"MIXED_USE":   {"RENT_LONG_TERM", "SALE", "SHORT_LET", "COMMERCIAL_LEASE", "LAND_LEASE"},
}

// ListingMediaInput selects property media for the listing.
type ListingMediaInput struct {
	MediaID  ids.ID `json:"media_id" v:"required"`
	Position int32  `json:"position,omitempty" v:"min=0,max=1000"`
	IsCover  bool   `json:"is_cover,omitempty"`
}

// CreateListingInput creates a DRAFT listing.
type CreateListingInput struct {
	PropertyID      ids.ID              `json:"property_id" v:"required"`
	UnitID          *ids.ID             `json:"unit_id,omitempty"`
	TransactionType string              `json:"transaction_type" v:"required,enum=RENT_LONG_TERM|SALE|SHORT_LET|COMMERCIAL_LEASE|LAND_LEASE"`
	Title           string              `json:"title" v:"required,min=5,max=160"`
	Description     *string             `json:"description,omitempty" v:"max=10000"`
	Details         ListingDetails      `json:"details"`
	Media           []ListingMediaInput `json:"media,omitempty" v:"max=50" doc:"Media already linked to the property (PHOTO, FLOOR_PLAN, VIDEO, VIRTUAL_TOUR)"`
	AvailableFrom   *string             `json:"available_from,omitempty" v:"format=date"`
}

// UpdateListingInput is a partial update.
type UpdateListingInput struct {
	Title       *string              `json:"title,omitempty" v:"min=5,max=160"`
	Description *string              `json:"description,omitempty" v:"max=10000"`
	Details     *ListingDetails      `json:"details,omitempty" doc:"Replaces all details"`
	Media       *[]ListingMediaInput `json:"media,omitempty" v:"max=50" doc:"Replaces the media selection"`
}

// ListingMediaView is a listing media reference.
type ListingMediaView struct {
	MediaID     ids.ID `json:"media_id"`
	Position    int32  `json:"position"`
	IsCover     bool   `json:"is_cover"`
	MediaStatus string `json:"media_status"`
}

// AvailabilityView is the listing availability (§13.9).
type AvailabilityView struct {
	Status              string     `json:"status"`
	AvailableFrom       *string    `json:"available_from"`
	AvailableUntil      *string    `json:"available_until"`
	Note                *string    `json:"note"`
	LastConfirmedAt     *time.Time `json:"last_confirmed_at"`
	NextConfirmationDue *time.Time `json:"next_confirmation_due"`
	AllowedNextStatuses []string   `json:"allowed_next_statuses"`
	Version             int64      `json:"version"`
}

// ListingView is the owner-facing listing.
type ListingView struct {
	ID                  ids.ID             `json:"id"`
	PropertyID          ids.ID             `json:"property_id"`
	UnitID              *ids.ID            `json:"unit_id"`
	TransactionType     string             `json:"transaction_type"`
	Status              string             `json:"status"`
	Title               string             `json:"title"`
	Description         *string            `json:"description"`
	Details             ListingDetails     `json:"details"`
	Media               []ListingMediaView `json:"media"`
	Pricing             *PricingView       `json:"pricing"`
	Availability        *AvailabilityView  `json:"availability"`
	CurrentVersionNo    int32              `json:"current_version_no"`
	PublishedVersionNo  *int32             `json:"published_version_no"`
	PublishedAt         *time.Time         `json:"published_at"`
	PauseSource         *string            `json:"pause_source"`
	PauseReason         *string            `json:"pause_reason"`
	QualityScore        *int32             `json:"quality_score"`
	AllowedNextStatuses []string           `json:"allowed_next_statuses"`
	CopyOfListingID     *ids.ID            `json:"copy_of_listing_id"`
	Version             int64              `json:"version"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

func availabilityView(a db.AvailabilityState) *AvailabilityView {
	return &AvailabilityView{Status: a.Status, AvailableFrom: dateStr(a.AvailableFrom), AvailableUntil: dateStr(a.AvailableUntil), Note: a.Note,
		LastConfirmedAt: a.LastConfirmedAt, NextConfirmationDue: a.NextConfirmationDue, AllowedNextStatuses: nonNil(domain.Availability.Targets(a.Status)),
		Version: a.Version}
}

func (s *Service) listingView(ctx context.Context, l db.Listing) (ListingView, error) {
	v := ListingView{ID: l.ID, PropertyID: l.PropertyID, UnitID: l.UnitID, TransactionType: l.TransactionType, Status: l.Status, Title: l.Title,
		Description: l.Description, Details: listingDetails(l), Media: []ListingMediaView{}, CurrentVersionNo: l.CurrentVersionNo,
		PublishedVersionNo: l.PublishedVersionNo, PublishedAt: l.PublishedAt, PauseSource: l.PauseSource, PauseReason: l.PauseReason,
		QualityScore: l.QualityScore, AllowedNextStatuses: nonNil(domain.Listing.Targets(l.Status)), CopyOfListingID: l.CopyOfListingID,
		Version: l.Version, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt}
	q := s.dbq(ctx)
	media, err := s.q.ListListingMedia(ctx, q, l.ID)
	if err != nil {
		return v, err
	}
	for _, m := range media {
		v.Media = append(v.Media, ListingMediaView{MediaID: m.MediaID, Position: m.Position, IsCover: m.IsCover, MediaStatus: m.MediaStatus})
	}
	if v.Pricing, _, err = s.activePricing(ctx, l); err != nil {
		return v, err
	}
	if a, err := s.q.GetAvailability(ctx, q, l.ID); err == nil {
		v.Availability = availabilityView(a)
	} else if !isNoRows(err) {
		return v, err
	}
	return v, nil
}

// actor returns the attributable actor (domain, id) of subj; a delegated
// service actor keeps its own identity.
func (s *Service) actor(ctx context.Context, subj authz.Subject, propertyID ids.ID) (string, ids.ID) {
	if subj.Account != nil {
		return string(platform.DomainIndividualOwner), subj.Account.AccountID
	}
	if !subj.ActorID.IsZero() {
		return string(subj.ActorDomain), subj.ActorID
	}
	return "system", s.actingAccount(ctx, subj, propertyID)
}

// listingFor loads a listing and authorizes action on its property.
func (s *Service) listingFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.Listing, authz.Decision, error) {
	l, err := s.q.GetListing(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return l, authz.Decision{}, notFound(AggListing)
	}
	if err != nil {
		return l, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, l.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggListing)
	}
	return l, d, err
}

func (s *Service) listingForUpdate(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.Listing, authz.Decision, error) {
	l, d, err := s.listingFor(ctx, subj, id, action)
	if err != nil {
		return l, d, err
	}
	l, err = s.q.GetListingForUpdate(ctx, mustTx(ctx), id)
	return l, d, err
}

// validateListingMedia checks every selected media is linked to the property.
func (s *Service) saveListingMedia(ctx context.Context, l db.Listing, media []ListingMediaInput) error {
	tx := mustTx(ctx)
	links, err := s.q.ListPropertyMedia(ctx, tx, l.PropertyID)
	if err != nil {
		return err
	}
	usable := map[ids.ID]bool{}
	for _, m := range links {
		if slices.Contains([]string{"PHOTO", "FLOOR_PLAN", "VIDEO", "VIRTUAL_TOUR"}, m.Purpose) {
			usable[m.MediaID] = true
		}
	}
	covers, seen := 0, map[ids.ID]bool{}
	for _, m := range media {
		if !usable[m.MediaID] {
			return precondition("MEDIA_NOT_LINKED", "Listing media must first be linked to the property as a photo, floor plan, video or tour.")
		}
		if seen[m.MediaID] {
			return apperrors.InvalidRequest("Duplicate media in the selection.")
		}
		seen[m.MediaID] = true
		if m.IsCover {
			covers++
		}
	}
	if covers > 1 {
		return apperrors.InvalidRequest("Only one cover image is allowed.")
	}
	if err := s.q.DeleteListingMedia(ctx, tx, l.ID); err != nil {
		return err
	}
	for _, m := range media {
		if err := s.q.InsertListingMedia(ctx, tx, db.InsertListingMediaParams{ListingID: l.ID, MediaID: m.MediaID, Position: m.Position, IsCover: m.IsCover,
			CreatedAt: s.now()}); err != nil {
			return err
		}
	}
	return nil
}

// CreateListing creates a DRAFT listing with AVAILABLE availability.
func (s *Service) CreateListing(ctx context.Context, subj authz.Subject, in CreateListingInput) (ListingView, error) {
	if err := schema.Validate(in); err != nil {
		return ListingView{}, err
	}
	var out ListingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		d, err := s.authorize(ctx, subj, authz.Do(authz.CapListingManage), in.PropertyID)
		if err != nil {
			return err
		}
		l, err := s.createListingTx(ctx, subj, d, in, nil)
		if err != nil {
			return err
		}
		out, err = s.listingView(ctx, l)
		return err
	})
	return out, err
}

func (s *Service) createListingTx(ctx context.Context, subj authz.Subject, d authz.Decision, in CreateListingInput, copyOf *ids.ID) (db.Listing, error) {
	p, err := s.requireMutable(ctx, in.PropertyID)
	if err != nil {
		return db.Listing{}, err
	}
	if !slices.Contains(categoryTransactions[p.Category], in.TransactionType) {
		return db.Listing{}, precondition("TRANSACTION_TYPE_NOT_ALLOWED", in.TransactionType+" is not available for "+p.Category+" properties.")
	}
	tx, now := mustTx(ctx), s.now()
	if in.UnitID != nil {
		u, err := s.q.GetUnit(ctx, tx, db.GetUnitParams{ID: *in.UnitID, PropertyID: in.PropertyID})
		if isNoRows(err) || (err == nil && u.Status != "ACTIVE") {
			return db.Listing{}, precondition("UNIT_INVALID", "The unit does not exist on this property or is archived.")
		}
		if err != nil {
			return db.Listing{}, err
		}
	}
	details, _ := json.Marshal(in.Details)
	dom, actor := s.actor(ctx, subj, in.PropertyID)
	l, err := s.q.InsertListing(ctx, tx, db.InsertListingParams{ID: ids.New(), PropertyID: in.PropertyID, UnitID: in.UnitID, TransactionType: in.TransactionType,
		Title: strings.TrimSpace(in.Title), Description: in.Description, Details: details, CopyOfListingID: copyOf, CreatedByActorDomain: dom,
		CreatedByActorID: actor, CreatedAt: now})
	if err != nil {
		return db.Listing{}, err
	}
	if err := s.saveListingMedia(ctx, l, in.Media); err != nil {
		return db.Listing{}, err
	}
	if _, err := s.q.InsertAvailability(ctx, tx, db.InsertAvailabilityParams{ID: l.ID, PropertyID: in.PropertyID, Status: "AVAILABLE",
		AvailableFrom: schema.ParseDate(in.AvailableFrom), UpdatedAt: now}); err != nil {
		return db.Listing{}, err
	}
	if err := domain.Listing.RecordCreation(ctx, tx, l.ID, &l.PropertyID, "DRAFT", now); err != nil {
		return db.Listing{}, err
	}
	if err := domain.Availability.RecordCreation(ctx, tx, l.ID, &l.PropertyID, "AVAILABLE", now); err != nil {
		return db.Listing{}, err
	}
	if err := s.propertyHistory(ctx, l.PropertyID, "LISTING_CREATED", in.TransactionType+" listing created", AggListing, l.ID); err != nil {
		return db.Listing{}, err
	}
	if err := s.noteDelegateActivity(ctx, d, l.PropertyID, "LISTING_CREATED", AggListing, l.ID, l.Title); err != nil {
		return db.Listing{}, err
	}
	return l, s.record(ctx, "listing.create", AggListing, l.ID, map[string]any{"transaction_type": in.TransactionType, "copy_of": copyOf}, nil)
}

// GetListing returns a listing.
func (s *Service) GetListing(ctx context.Context, subj authz.Subject, id ids.ID) (ListingView, error) {
	l, _, err := s.listingFor(ctx, subj, id, authz.Read(authz.CapListingManage))
	if err != nil {
		return ListingView{}, err
	}
	return s.listingView(ctx, l)
}

// ListListings lists listings across the caller's properties.
func (s *Service) ListListings(ctx context.Context, subj authz.Subject, propertyID ids.ID, status, txn, cursor string, limit int) (pagination.Response[ListingView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[ListingView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[ListingView]{}, err
	}
	rows, err := s.q.ListListingsForAccount(ctx, s.dbq(ctx), db.ListListingsForAccountParams{AccountID: accountOf(subj), Now: s.now(), PropertyID: idp(propertyID),
		Status: strp(status), TransactionType: strp(txn), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[ListingView]{}, err
	}
	out := make([]ListingView, 0, len(rows))
	for _, r := range rows {
		v, err := s.listingView(ctx, r)
		if err != nil {
			return pagination.Response[ListingView]{}, err
		}
		out = append(out, v)
	}
	return pagination.Build(out, limit, func(v ListingView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

// UpdateListing patches a listing (If-Match). A published listing must stay
// publishable; the edit creates and emits a new immutable version.
func (s *Service) UpdateListing(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in UpdateListingInput) (ListingView, error) {
	if err := schema.Validate(in); err != nil {
		return ListingView{}, err
	}
	var out ListingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, d, err := s.listingForUpdate(ctx, subj, id, authz.Do(authz.CapListingManage))
		if err != nil {
			return err
		}
		if l.Status == "WITHDRAWN" || l.Status == "ARCHIVED" {
			return conflict("LISTING_CLOSED", "The listing is "+l.Status+".")
		}
		if err := checkVersion(expected, l.Version); err != nil {
			return err
		}
		title, desc, details := l.Title, l.Description, l.Details
		if in.Title != nil {
			title = strings.TrimSpace(*in.Title)
		}
		if in.Description != nil {
			desc = strp(*in.Description)
		}
		if in.Details != nil {
			details, _ = json.Marshal(in.Details)
		}
		nl, err := s.q.UpdateListingContent(ctx, mustTx(ctx), db.UpdateListingContentParams{ID: id, Version: l.Version, Title: title, Description: desc,
			Details: details, UpdatedAt: s.now()})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		if in.Media != nil {
			if err := s.saveListingMedia(ctx, nl, *in.Media); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "listing.update", AggListing, id, map[string]any{"version": nl.Version}, nil); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, l.PropertyID, "LISTING_UPDATED", AggListing, id, title); err != nil {
			return err
		}
		if nl.Status == "PUBLISHED" {
			if problems, err := s.readiness(ctx, nl); err != nil {
				return err
			} else if len(problems) > 0 {
				return notPublishable(problems)
			}
			if err := s.republish(ctx, subj, id, "EDIT"); err != nil {
				return err
			}
		}
		nl, err = s.q.GetListing(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.listingView(ctx, nl)
		return err
	})
	return out, err
}

// Problem is one publication readiness failure.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func notPublishable(problems []Problem) error {
	e := apperrors.Unprocessable("LISTING_NOT_PUBLISHABLE", "The listing cannot be published: "+problems[0].Message)
	var details []apperrors.Detail
	for _, p := range problems {
		details = append(details, apperrors.Detail{Field: "listing", Code: p.Code, Message: p.Message})
	}
	return e.WithDetails(details...)
}

// readiness evaluates every publication requirement (§13.7).
func (s *Service) readiness(ctx context.Context, l db.Listing) ([]Problem, error) {
	q := s.dbq(ctx)
	var out []Problem
	add := func(code, msg string) { out = append(out, Problem{Code: code, Message: msg}) }
	p, err := s.q.GetProperty(ctx, q, l.PropertyID)
	if err != nil {
		return nil, err
	}
	if p.Status != "ACTIVE" {
		add("PROPERTY_NOT_ACTIVE", "The property must be ACTIVE (current: "+p.Status+").")
	}
	st, err := s.q.PropertyAuthorityState(ctx, q, db.PropertyAuthorityStateParams{Now: s.now(), PropertyID: l.PropertyID})
	if err != nil {
		return nil, err
	}
	if !st.AuthorityVerified {
		add("PROPERTY_AUTHORITY_UNVERIFIED", "Ownership authority must be verified.")
	}
	if l.UnitID != nil {
		if u, err := s.q.GetUnit(ctx, q, db.GetUnitParams{ID: *l.UnitID, PropertyID: l.PropertyID}); err != nil || u.Status != "ACTIVE" {
			add("UNIT_INVALID", "The listed unit is archived or missing.")
		}
	}
	if missing := requiredDetails(l.TransactionType, listingDetails(l)); len(missing) > 0 {
		add("DETAILS_INCOMPLETE", "Missing details: "+strings.Join(missing, ", ")+".")
	}
	media, err := s.q.ListListingMedia(ctx, q, l.ID)
	if err != nil {
		return nil, err
	}
	ready := 0
	for _, m := range media {
		switch m.MediaStatus {
		case "READY":
			ready++
		case "REJECTED", "DELETED":
			add("MEDIA_UNUSABLE", "Selected media "+m.MediaID.String()+" is "+m.MediaStatus+".")
		}
	}
	if need := s.Policy.Domain.MinListingPhotos[l.TransactionType]; ready < need {
		add("MEDIA_INSUFFICIENT", "At least "+itoa(need)+" ready photos are required ("+itoa(ready)+" ready).")
	}
	if pv, _, err := s.activePricing(ctx, l); err != nil {
		return nil, err
	} else if pv == nil {
		add("PRICING_NOT_SET", "A complete price with every mandatory fee must be disclosed.")
	}
	if a, err := s.q.GetAvailability(ctx, q, l.ID); err == nil {
		reletting := a.Status == "OCCUPIED" && a.AvailableFrom != nil && a.AvailableFrom.After(s.now())
		if slices.Contains([]string{"OCCUPIED", "OFF_MARKET", "SOLD", "WITHDRAWN", "OFFER_ACCEPTED"}, a.Status) && !reletting {
			add("NOT_AVAILABLE", "Availability is "+a.Status+".")
		}
	} else if !isNoRows(err) {
		return nil, err
	}
	return out, nil
}

func (s *Service) qualityScore(ctx context.Context, l db.Listing) int32 {
	score := int32(0)
	media, _ := s.q.ListListingMedia(ctx, s.dbq(ctx), l.ID)
	photos := int32(0)
	for _, m := range media {
		if m.MediaStatus == "READY" {
			photos++
		}
	}
	score += min(photos, 5) * 8
	if l.Description != nil && len(*l.Description) >= 200 {
		score += 20
	}
	if len(requiredDetails(l.TransactionType, listingDetails(l))) == 0 {
		score += 20
	}
	if fs, _ := s.q.ListPropertyFeatures(ctx, s.dbq(ctx), l.PropertyID); len(fs) >= 3 {
		score += 10
	}
	if st, err := s.q.PropertyAuthorityState(ctx, s.dbq(ctx), db.PropertyAuthorityStateParams{Now: s.now(), PropertyID: l.PropertyID}); err == nil && st.AuthorityVerified {
		score += 10
	}
	return min(score, 100)
}

// ListingSnapshot is the immutable public projection of a listing version.
type ListingSnapshot struct {
	ListingID         ids.ID             `json:"listing_id"`
	PropertyID        ids.ID             `json:"property_id"`
	UnitID            *ids.ID            `json:"unit_id"`
	UnitLabel         *string            `json:"unit_label"`
	VersionNo         int32              `json:"version_no"`
	TransactionType   string             `json:"transaction_type"`
	Category          string             `json:"category"`
	PropertyType      string             `json:"property_type"`
	Title             string             `json:"title"`
	Description       *string            `json:"description"`
	Details           ListingDetails     `json:"details"`
	Bedrooms          *int32             `json:"bedrooms"`
	Bathrooms         *int32             `json:"bathrooms"`
	FloorAreaSqm      *int32             `json:"floor_area_sqm"`
	City              string             `json:"city"`
	StateRegion       string             `json:"state_region"`
	CountryCode       string             `json:"country_code"`
	AreaLabel         *string            `json:"area_label"`
	Latitude          *float64           `json:"latitude" doc:"Only with EXACT address visibility"`
	Longitude         *float64           `json:"longitude"`
	Features          []FeatureView      `json:"features"`
	Media             []ListingMediaView `json:"media"`
	Pricing           *PricingView       `json:"pricing"`
	AllInCost         *domain.Cost       `json:"all_in_cost" doc:"Default scenario of the shared calculator"`
	Availability      *AvailabilityView  `json:"availability"`
	AuthorityVerified bool               `json:"authority_verified"`
	VerificationLevel string             `json:"verification_level"`
	QualityScore      int32              `json:"quality_score"`
}

func (s *Service) snapshot(ctx context.Context, l db.Listing, versionNo int32) (ListingSnapshot, *ids.ID, error) {
	q := s.dbq(ctx)
	sum, err := s.PublicSummaryOf(ctx, l.PropertyID)
	if err != nil {
		return ListingSnapshot{}, nil, err
	}
	p, err := s.q.GetProperty(ctx, q, l.PropertyID)
	if err != nil {
		return ListingSnapshot{}, nil, err
	}
	v, err := s.listingView(ctx, l)
	if err != nil {
		return ListingSnapshot{}, nil, err
	}
	fs, err := s.q.ListPropertyFeatures(ctx, q, l.PropertyID)
	if err != nil {
		return ListingSnapshot{}, nil, err
	}
	snap := ListingSnapshot{ListingID: l.ID, PropertyID: l.PropertyID, UnitID: l.UnitID, VersionNo: versionNo, TransactionType: l.TransactionType,
		Category: p.Category, PropertyType: p.PropertyType, Title: l.Title, Description: l.Description, Details: v.Details, Bedrooms: p.Bedrooms,
		Bathrooms: p.Bathrooms, FloorAreaSqm: p.FloorAreaSqm, City: sum.City, StateRegion: sum.StateRegion, CountryCode: sum.CountryCode,
		AreaLabel: sum.AreaLabel, Latitude: sum.Latitude, Longitude: sum.Longitude, Features: featureViews(fs), Media: v.Media, Pricing: v.Pricing,
		Availability: v.Availability, AuthorityVerified: sum.AuthorityVerified, VerificationLevel: sum.VerificationLevel, QualityScore: s.qualityScore(ctx, l)}
	if l.UnitID != nil {
		if u, err := s.q.GetUnit(ctx, q, db.GetUnitParams{ID: *l.UnitID, PropertyID: l.PropertyID}); err == nil {
			snap.UnitLabel = &u.Label
			snap.Bedrooms, snap.Bathrooms, snap.FloorAreaSqm = u.Bedrooms, u.Bathrooms, u.FloorAreaSqm
		}
	}
	var pricingID *ids.ID
	if v.Pricing != nil {
		pricingID = &v.Pricing.PricingVersionID
		if c, err := s.costPreview(ctx, l, CostPreviewInput{}); err == nil {
			snap.AllInCost = &c.Cost
		} else if l.TransactionType != "SHORT_LET" {
			return ListingSnapshot{}, nil, err
		}
	}
	return snap, pricingID, nil
}

// ListingEvent is the payload of listing.published.v1 / listing.updated.v1.
type ListingEvent struct {
	SchemaVersion int             `json:"schema_version"`
	ListingID     ids.ID          `json:"listing_id"`
	PropertyID    ids.ID          `json:"property_id"`
	Status        string          `json:"status"`
	VersionNo     int32           `json:"version_no" doc:"Immutable listing version; consumers discard lower versions"`
	ChangeReason  string          `json:"change_reason"`
	PublishedAt   time.Time       `json:"published_at"`
	Snapshot      ListingSnapshot `json:"snapshot"`
	OccurredAt    time.Time       `json:"occurred_at"`
}

// ListingStateEvent is the payload of listing.paused.v1, listing.withdrawn.v1
// and listing.availability_confirmed.v1.
type ListingStateEvent struct {
	SchemaVersion  int               `json:"schema_version"`
	ListingID      ids.ID            `json:"listing_id"`
	PropertyID     ids.ID            `json:"property_id"`
	Status         string            `json:"status"`
	VersionNo      int32             `json:"version_no"`
	Source         string            `json:"source" v:"enum=OWNER|MANAGER|SYSTEM|ADMIN"`
	ReasonCode     *string           `json:"reason_code"`
	Availability   *AvailabilityView `json:"availability"`
	ListingVersion int64             `json:"listing_version" doc:"Aggregate version (monotonic)"`
	OccurredAt     time.Time         `json:"occurred_at"`
}

func sourceOf(subj authz.Subject) string {
	switch {
	case subj.Kind == authz.KindSystem:
		return "SYSTEM"
	case subj.Kind == authz.KindService && subj.ActorDomain == platform.DomainAdmin:
		return "ADMIN"
	case subj.Kind == authz.KindService:
		return "MANAGER"
	}
	return "OWNER"
}

// writeVersion appends an immutable version and records the publication.
func (s *Service) writeVersion(ctx context.Context, subj authz.Subject, l db.Listing, reason, action string) (db.Listing, ListingSnapshot, error) {
	tx, now := mustTx(ctx), s.now()
	no := l.CurrentVersionNo + 1
	snap, pricingID, err := s.snapshot(ctx, l, no)
	if err != nil {
		return l, snap, err
	}
	raw, _ := json.Marshal(snap)
	dom, actor := s.actor(ctx, subj, l.PropertyID)
	if _, err := s.q.InsertListingVersion(ctx, tx, db.InsertListingVersionParams{ID: ids.New(), ListingID: l.ID, VersionNo: no, Snapshot: raw,
		PricingVersionID: pricingID, Reason: reason, ActorDomain: &dom, ActorID: &actor, CreatedAt: now}); err != nil {
		return l, snap, err
	}
	nl, err := s.q.SetListingPublication(ctx, tx, db.SetListingPublicationParams{ID: l.ID, CurrentVersionNo: no, PublishedAt: &now, QualityScore: &snap.QualityScore})
	if err != nil {
		return l, snap, err
	}
	if err := s.q.InsertListingPublication(ctx, tx, db.InsertListingPublicationParams{ID: ids.New(), ListingID: l.ID, VersionNo: &no, Action: action,
		Source: sourceOf(subj), OccurredAt: now}); err != nil {
		return l, snap, err
	}
	return nl, snap, nil
}

func (s *Service) emitListing(ctx context.Context, eventType string, l db.Listing, snap ListingSnapshot, reason string) error {
	return s.publish(ctx, eventType, AggListing, l.ID, ListingEvent{SchemaVersion: 1, ListingID: l.ID, PropertyID: l.PropertyID, Status: l.Status,
		VersionNo: snap.VersionNo, ChangeReason: reason, PublishedAt: deref(l.PublishedAt), Snapshot: snap, OccurredAt: s.now()})
}

func (s *Service) emitListingState(ctx context.Context, eventType string, l db.Listing, source string, reason *string) error {
	var av *AvailabilityView
	if a, err := s.q.GetAvailability(ctx, mustTx(ctx), l.ID); err == nil {
		av = availabilityView(a)
	}
	return s.publish(ctx, eventType, AggListing, l.ID, ListingStateEvent{SchemaVersion: 1, ListingID: l.ID, PropertyID: l.PropertyID, Status: l.Status,
		VersionNo: l.CurrentVersionNo, Source: source, ReasonCode: reason, Availability: av, ListingVersion: l.Version, OccurredAt: s.now()})
}

// republish emits a new version of an already published listing.
func (s *Service) republish(ctx context.Context, subj authz.Subject, id ids.ID, reason string) error {
	l, err := s.q.GetListingForUpdate(ctx, mustTx(ctx), id)
	if err != nil {
		return err
	}
	nl, snap, err := s.writeVersion(ctx, subj, l, reason, "UPDATED")
	if err != nil {
		return err
	}
	return s.emitListing(ctx, "listing.updated.v1", nl, snap, reason)
}

// PublishListing publishes (or resumes) a listing after every readiness
// check, authority verification, identity policy and co-owner consent.
func (s *Service) PublishListing(ctx context.Context, subj authz.Subject, id ids.ID) (ListingView, error) {
	var out ListingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, d, err := s.listingForUpdate(ctx, subj, id, authz.Do(authz.CapListingPublish))
		if err != nil {
			return err
		}
		owner := s.actingAccount(ctx, subj, l.PropertyID)
		if err := s.requireIdentity(ctx, owner, GateListingPublish); err != nil {
			return err
		}
		if err := s.requireConsent(ctx, d, l.PropertyID, ConsentListingPublish, l.ID, owner); err != nil {
			return err
		}
		if l.Status == "PAUSED" && deref(l.PauseSource) == "ADMIN" {
			return conflict("LISTING_ADMIN_PAUSED", "The listing was paused by platform review and cannot be resumed by the owner.")
		}
		if !slices.Contains([]string{"DRAFT", "READY_FOR_REVIEW", "PAUSED"}, l.Status) {
			return apperrors.InvalidStateTransition(l.Status, "PUBLISHED")
		}
		problems, err := s.readiness(ctx, l)
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			return notPublishable(problems)
		}
		resumed := l.Status == "PAUSED"
		if l.Status == "DRAFT" {
			if _, _, err := s.transition(ctx, domain.Listing, id, lifecycle.Change{To: "READY_FOR_REVIEW", Command: "publish_checks_passed"}); err != nil {
				return err
			}
		}
		if _, _, err := s.transition(ctx, domain.Listing, id, lifecycle.Change{To: "PUBLISHED", Command: "publish"}); err != nil {
			if isUnique(err, "listings_one_published_uniq") {
				return conflict("LISTING_ALREADY_PUBLISHED", "Another listing for this property/unit and transaction type is already published.")
			}
			return err
		}
		l, err = s.q.GetListingForUpdate(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		action, reason := "PUBLISHED", "PUBLISH"
		if resumed {
			action, reason = "RESUMED", "RESUME"
		}
		nl, snap, err := s.writeVersion(ctx, subj, l, reason, action)
		if err != nil {
			return err
		}
		if err := s.scheduleFreshness(ctx, nl, "SCHEDULED", nil); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, l.PropertyID, "LISTING_"+action, l.TransactionType+" listing "+lower(action), AggListing, id); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, l.PropertyID, "LISTING_"+action, AggListing, id, l.Title); err != nil {
			return err
		}
		if err := s.record(ctx, "listing."+lower(reason), AggListing, id, map[string]any{"version_no": snap.VersionNo}, nil); err != nil {
			return err
		}
		if err := s.emitListing(ctx, "listing.published.v1", nl, snap, reason); err != nil {
			return err
		}
		out, err = s.listingView(ctx, nl)
		return err
	})
	if isUnique(err, "listings_one_published_uniq") {
		err = conflict("LISTING_ALREADY_PUBLISHED", "Another listing for this property/unit and transaction type is already published.")
	}
	return out, err
}

// ListingReasonInput carries an optional reason code.
type ListingReasonInput struct {
	ReasonCode string `json:"reason_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
}

// PauseListing pauses a published listing (owner, co-owner or delegate).
func (s *Service) PauseListing(ctx context.Context, subj authz.Subject, id ids.ID, in ListingReasonInput) (ListingView, error) {
	if err := schema.Validate(in); err != nil {
		return ListingView{}, err
	}
	var out ListingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, d, err := s.listingForUpdate(ctx, subj, id, authz.Do(authz.CapListingPause))
		if err != nil {
			return err
		}
		reason := in.ReasonCode
		if reason == "" {
			reason = "OWNER_PAUSED"
		}
		if err := s.pauseTx(ctx, l, sourceOf(subj), reason); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, l.PropertyID, "LISTING_PAUSED", AggListing, id, reason); err != nil {
			return err
		}
		nl, err := s.q.GetListing(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.listingView(ctx, nl)
		return err
	})
	return out, err
}

func (s *Service) pauseTx(ctx context.Context, l db.Listing, source, reason string) error {
	if _, _, err := s.transition(ctx, domain.Listing, l.ID, lifecycle.Change{To: "PAUSED", Command: "pause", Reason: reason,
		Metadata: map[string]any{"source": source}}); err != nil {
		return err
	}
	tx, now := mustTx(ctx), s.now()
	ps := source
	if ps == "MANAGER" {
		ps = "OWNER"
	}
	if err := s.q.SetListingPause(ctx, tx, db.SetListingPauseParams{ID: l.ID, PauseSource: &ps, PauseReason: &reason, UpdatedAt: now}); err != nil {
		return err
	}
	if err := s.q.InsertListingPublication(ctx, tx, db.InsertListingPublicationParams{ID: ids.New(), ListingID: l.ID, VersionNo: &l.CurrentVersionNo,
		Action: "PAUSED", Source: source, Reason: &reason, OccurredAt: now}); err != nil {
		return err
	}
	if err := s.propertyHistory(ctx, l.PropertyID, "LISTING_PAUSED", "Listing paused ("+lower(source)+"): "+reason, AggListing, l.ID); err != nil {
		return err
	}
	if err := s.record(ctx, "listing.pause", AggListing, l.ID, map[string]any{"source": source, "reason_code": reason}, nil); err != nil {
		return err
	}
	nl, err := s.q.GetListing(ctx, tx, l.ID)
	if err != nil {
		return err
	}
	return s.emitListingState(ctx, "listing.paused.v1", nl, source, &reason)
}

// WithdrawListing withdraws a listing from the market.
func (s *Service) WithdrawListing(ctx context.Context, subj authz.Subject, id ids.ID, in ListingReasonInput) (ListingView, error) {
	return s.closeListing(ctx, subj, id, "WITHDRAWN", in)
}

// ArchiveListing archives a listing (withdrawing it first if live).
func (s *Service) ArchiveListing(ctx context.Context, subj authz.Subject, id ids.ID, in ListingReasonInput) (ListingView, error) {
	return s.closeListing(ctx, subj, id, "ARCHIVED", in)
}

func (s *Service) closeListing(ctx context.Context, subj authz.Subject, id ids.ID, to string, in ListingReasonInput) (ListingView, error) {
	if err := schema.Validate(in); err != nil {
		return ListingView{}, err
	}
	var out ListingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, d, err := s.listingForUpdate(ctx, subj, id, authz.Do(authz.CapListingManage))
		if err != nil {
			return err
		}
		if err := s.closeListingTx(ctx, l, to, sourceOf(subj), in.ReasonCode); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, l.PropertyID, "LISTING_"+to, AggListing, id, l.Title); err != nil {
			return err
		}
		nl, err := s.q.GetListing(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.listingView(ctx, nl)
		return err
	})
	return out, err
}

func (s *Service) closeListingTx(ctx context.Context, l db.Listing, to, source, reason string) error {
	if reason == "" {
		reason = "OWNER_" + to
	}
	wasLive := l.Status == "PUBLISHED" || l.Status == "PAUSED"
	if _, _, err := s.transition(ctx, domain.Listing, l.ID, lifecycle.Change{To: to, Command: lower(to), Reason: reason}); err != nil {
		return err
	}
	tx, now := mustTx(ctx), s.now()
	if a, err := s.q.GetAvailability(ctx, tx, l.ID); err == nil && domain.Availability.Can(a.Status, "WITHDRAWN") {
		if _, _, err := s.transition(ctx, domain.Availability, l.ID, lifecycle.Change{To: "WITHDRAWN", Command: "listing_" + lower(to)}); err != nil {
			return err
		}
	}
	if _, err := s.q.ResolveFreshnessChecks(ctx, tx, db.ResolveFreshnessChecksParams{ListingID: l.ID, Status: "CANCELLED", ResolvedAt: &now}); err != nil {
		return err
	}
	action := "WITHDRAWN"
	if to == "ARCHIVED" {
		action = "ARCHIVED"
	}
	if err := s.q.InsertListingPublication(ctx, tx, db.InsertListingPublicationParams{ID: ids.New(), ListingID: l.ID, VersionNo: &l.CurrentVersionNo,
		Action: action, Source: source, Reason: &reason, OccurredAt: now}); err != nil {
		return err
	}
	if err := s.propertyHistory(ctx, l.PropertyID, "LISTING_"+to, "Listing "+lower(to), AggListing, l.ID); err != nil {
		return err
	}
	if err := s.record(ctx, "listing."+lower(to), AggListing, l.ID, map[string]any{"reason_code": reason}, nil); err != nil {
		return err
	}
	if wasLive {
		nl, err := s.q.GetListing(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		return s.emitListingState(ctx, "listing.withdrawn.v1", nl, source, &reason)
	}
	return nil
}

// CopyListingInput copies a listing into a new draft.
type CopyListingInput struct {
	UnitID          *ids.ID `json:"unit_id,omitempty" doc:"Target unit (e.g. another flat in the same building)"`
	TransactionType string  `json:"transaction_type,omitempty" v:"enum=RENT_LONG_TERM|SALE|SHORT_LET|COMMERCIAL_LEASE|LAND_LEASE"`
}

// CopyListing creates a DRAFT copy (content, media and pricing as a new version).
func (s *Service) CopyListing(ctx context.Context, subj authz.Subject, id ids.ID, in CopyListingInput) (ListingView, error) {
	if err := schema.Validate(in); err != nil {
		return ListingView{}, err
	}
	var out ListingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		src, d, err := s.listingFor(ctx, subj, id, authz.Do(authz.CapListingManage))
		if err != nil {
			return err
		}
		media, err := s.q.ListListingMedia(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		var sel []ListingMediaInput
		for _, m := range media {
			sel = append(sel, ListingMediaInput{MediaID: m.MediaID, Position: m.Position, IsCover: m.IsCover})
		}
		txn := src.TransactionType
		if in.TransactionType != "" {
			txn = in.TransactionType
		}
		unit := src.UnitID
		if in.UnitID != nil {
			unit = in.UnitID
		}
		nl, err := s.createListingTx(ctx, subj, d, CreateListingInput{PropertyID: src.PropertyID, UnitID: unit, TransactionType: txn, Title: src.Title,
			Description: src.Description, Details: listingDetails(src), Media: sel}, &src.ID)
		if err != nil {
			return err
		}
		if txn == src.TransactionType {
			if err := s.copyPricing(ctx, subj, src, nl); err != nil {
				return err
			}
		}
		out, err = s.listingView(ctx, nl)
		return err
	})
	return out, err
}

func (s *Service) copyPricing(ctx context.Context, subj authz.Subject, src, dst db.Listing) error {
	pv, _, err := s.activePricing(ctx, src)
	if err != nil || pv == nil {
		return err
	}
	in := PricingInput{Currency: pv.Currency, BaseAmountMinor: pv.BaseAmountMinor, BasePeriod: pv.BasePeriod, PaymentCycle: pv.PaymentCycle,
		DepositAmountMinor: pv.DepositAmountMinor, Negotiable: pv.Negotiable}
	for _, f := range pv.Fees {
		in.Fees = append(in.Fees, FeeInput{Code: f.Code, Category: f.Category, Label: f.Label, Calculation: f.Calculation, AmountMinor: f.AmountMinor,
			RateBps: f.RateBps, Frequency: f.Frequency, Mandatory: f.Mandatory, Refundable: f.Refundable, PayableTo: f.PayableTo})
	}
	for _, r := range pv.Rules {
		in.Rules = append(in.Rules, RuleInput{RuleType: r.Type, Params: r.Params})
	}
	_, err = s.PutPricing(ctx, subj, dst.ID, 0, in)
	return err
}

// PreviewView is what customers would see plus the readiness checklist.
type PreviewView struct {
	Publishable bool            `json:"publishable"`
	Problems    []Problem       `json:"problems"`
	Snapshot    ListingSnapshot `json:"snapshot"`
}

// PreviewListing renders the customer view without publishing.
func (s *Service) PreviewListing(ctx context.Context, subj authz.Subject, id ids.ID) (PreviewView, error) {
	l, _, err := s.listingFor(ctx, subj, id, authz.Read(authz.CapListingManage))
	if err != nil {
		return PreviewView{}, err
	}
	problems, err := s.readiness(ctx, l)
	if err != nil {
		return PreviewView{}, err
	}
	snap, _, err := s.snapshot(ctx, l, l.CurrentVersionNo+1)
	if err != nil && apperrors.CodeOf(err) != "TERM_BELOW_MINIMUM" {
		return PreviewView{}, err
	}
	if problems == nil {
		problems = []Problem{}
	}
	return PreviewView{Publishable: len(problems) == 0, Problems: problems, Snapshot: snap}, nil
}

// ListingVersionView is one immutable listing version.
type ListingVersionView struct {
	VersionNo        int32           `json:"version_no"`
	Reason           string          `json:"reason"`
	PricingVersionID *ids.ID         `json:"pricing_version_id"`
	Snapshot         json.RawMessage `json:"snapshot"`
	CreatedAt        time.Time       `json:"created_at"`
}

// ListingVersions lists immutable versions (newest first).
func (s *Service) ListingVersions(ctx context.Context, subj authz.Subject, id ids.ID) ([]ListingVersionView, error) {
	if _, _, err := s.listingFor(ctx, subj, id, authz.Read(authz.CapListingManage)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListListingVersions(ctx, s.dbq(ctx), id)
	out := make([]ListingVersionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ListingVersionView{VersionNo: r.VersionNo, Reason: r.Reason, PricingVersionID: r.PricingVersionID, Snapshot: r.Snapshot, CreatedAt: r.CreatedAt})
	}
	return out, err
}

// InternalListingView is the authoritative marketplace read-back (§9).
type InternalListingView struct {
	ListingID          ids.ID            `json:"listing_id"`
	PropertyID         ids.ID            `json:"property_id"`
	Status             string            `json:"status"`
	PublishedVersionNo *int32            `json:"published_version_no"`
	PublishedAt        *time.Time        `json:"published_at"`
	PauseSource        *string           `json:"pause_source"`
	Availability       *AvailabilityView `json:"availability"`
	AuthorityVerified  bool              `json:"authority_verified"`
	Bookable           bool              `json:"bookable" doc:"PUBLISHED, verified authority and available"`
	Snapshot           json.RawMessage   `json:"snapshot" doc:"The published immutable version"`
	ReadAt             time.Time         `json:"read_at"`
}

// InternalGetListing returns the authoritative state of a listing that has
// been published at least once (drafts are never exposed).
func (s *Service) InternalGetListing(ctx context.Context, subj authz.Subject, id ids.ID) (InternalListingView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalListingRead, authz.Resource{}); err != nil {
		return InternalListingView{}, err
	}
	q := s.dbq(ctx)
	l, err := s.q.GetListing(ctx, q, id)
	if isNoRows(err) || (err == nil && l.PublishedVersionNo == nil) {
		return InternalListingView{}, notFound(AggListing)
	}
	if err != nil {
		return InternalListingView{}, err
	}
	v, err := s.q.GetListingVersion(ctx, q, db.GetListingVersionParams{ListingID: id, VersionNo: l.CurrentVersionNo})
	if err != nil {
		return InternalListingView{}, err
	}
	st, err := s.q.PropertyAuthorityState(ctx, q, db.PropertyAuthorityStateParams{Now: s.now(), PropertyID: l.PropertyID})
	if err != nil {
		return InternalListingView{}, err
	}
	out := InternalListingView{ListingID: l.ID, PropertyID: l.PropertyID, Status: l.Status, PublishedVersionNo: &l.CurrentVersionNo, PublishedAt: l.PublishedAt,
		PauseSource: l.PauseSource, AuthorityVerified: st.AuthorityVerified, Snapshot: v.Snapshot, ReadAt: s.now()}
	if a, err := s.q.GetAvailability(ctx, q, id); err == nil {
		out.Availability = availabilityView(a)
		out.Bookable = l.Status == "PUBLISHED" && st.AuthorityVerified && st.Status == "ACTIVE" &&
			slices.Contains([]string{"AVAILABLE", "APPLICATION_UNDER_REVIEW"}, a.Status)
	}
	return out, nil
}

func init() {
	produces("listing.published.v1", AggListing, "A listing was published or resumed; the payload is the immutable published version.", "Marketplace", ListingEvent{})
	produces("listing.updated.v1", AggListing, "A published listing changed (edit, price change); the payload is the new immutable version.", "Marketplace", ListingEvent{})
	produces("listing.paused.v1", AggListing, "A listing was paused by the owner, a delegate, the system or platform review.", "Marketplace", ListingStateEvent{})
	produces("listing.withdrawn.v1", AggListing, "A live listing was withdrawn or archived; remove it from discovery.", "Marketplace", ListingStateEvent{})
	produces("listing.availability_confirmed.v1", AggListing, "Availability was confirmed or changed (freshness).", "Marketplace", ListingStateEvent{})

	// Property-level hooks.
	archiveBlockerChecks = append(archiveBlockerChecks, struct{ name, sql string }{"live listing",
		`SELECT EXISTS (SELECT 1 FROM listings WHERE property_id = $1 AND status IN ('READY_FOR_REVIEW','PUBLISHED','PAUSED'))`})
	unitArchiveChecks = append(unitArchiveChecks, struct{ name, sql string }{"listing",
		`SELECT EXISTS (SELECT 1 FROM listings WHERE unit_id = $1 AND status IN ('READY_FOR_REVIEW','PUBLISHED','PAUSED'))`})
	pauseAll := func(reason string) func(s *Service, ctx context.Context, propertyID ids.ID) error {
		return func(s *Service, ctx context.Context, propertyID ids.ID) error {
			live, err := s.q.LiveListingsForProperty(ctx, mustTx(ctx), propertyID)
			if err != nil {
				return err
			}
			for _, l := range live {
				if l.Status == "PUBLISHED" {
					if err := s.pauseTx(ctx, l, "SYSTEM", reason); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	authorityLostHooks = append(authorityLostHooks, func(s *Service, ctx context.Context, propertyID ids.ID, status string) error {
		return pauseAll("AUTHORITY_"+status)(s, ctx, propertyID)
	})
	transferLockHooks = append(transferLockHooks, pauseAll("OWNERSHIP_TRANSFER_PENDING"))
	ownerChangedHooks = append(ownerChangedHooks, func(s *Service, ctx context.Context, propertyID, _, _ ids.ID) error {
		live, err := s.q.LiveListingsForProperty(ctx, mustTx(ctx), propertyID)
		if err != nil {
			return err
		}
		for _, l := range live {
			if err := s.closeListingTx(ctx, l, "WITHDRAWN", "SYSTEM", "OWNERSHIP_TRANSFERRED"); err != nil {
				return err
			}
		}
		return nil
	})
	unlinkMediaChecks = append(unlinkMediaChecks, func(s *Service, ctx context.Context, propertyID, mediaID ids.ID) error {
		rows, err := s.q.ListingsUsingMedia(ctx, mustTx(ctx), mediaID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Status == "PUBLISHED" || r.Status == "PAUSED" {
				return conflict("MEDIA_IN_USE", "The media is used by a live listing; change the listing's media first.")
			}
		}
		return nil
	})
	// Media becoming unusable re-checks published listings that use it.
	mediaStatusHooks = append(mediaStatusHooks, func(s *Service, ctx context.Context, mediaID ids.ID, status string) error {
		if status == "READY" {
			return nil
		}
		rows, err := s.q.ListingsUsingMedia(ctx, mustTx(ctx), mediaID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Status != "PUBLISHED" {
				continue
			}
			l, err := s.q.GetListingForUpdate(ctx, mustTx(ctx), r.ID)
			if err != nil {
				return err
			}
			if problems, err := s.readiness(ctx, l); err != nil {
				return err
			} else if len(problems) > 0 {
				if err := s.pauseTx(ctx, l, "SYSTEM", "MEDIA_"+status); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// RunListingMediaAccess keeps marketplace read access in step with listing
// state: media-document-service keeps listing photos PRIVATE to the owner
// domain, so while a live (PUBLISHED or PAUSED) listing uses a file
// marketplace-service is granted read access, and the grant is removed once
// no live listing uses it. Media calls are made outside any transaction;
// each outcome is recorded so the job is idempotent and retried on failure.
func (s *Service) RunListingMediaAccess(ctx context.Context, batch int32) error {
	if s.Media == nil {
		return nil
	}
	grant, err := s.q.MediaNeedingMarketplaceGrant(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	revoke, err := s.q.MediaNeedingMarketplaceRevoke(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, set := range []struct {
		ids   []ids.ID
		grant bool
	}{{grant, true}, {revoke, false}} {
		for _, id := range set.ids {
			if err := s.syncMarketplaceAccess(ctx, id, set.grant); err != nil {
				s.Logger.WarnContext(ctx, "listing media access deferred", "media_id", id, "grant", set.grant, "error", err)
			}
		}
	}
	return nil
}

func (s *Service) syncMarketplaceAccess(ctx context.Context, id ids.ID, grant bool) error {
	m, err := s.Media.Get(ctx, id)
	if err != nil {
		return err
	}
	grants := []media.Grant{}
	if grant {
		grants = append(grants, media.Grant{GranteeType: "SERVICE", Service: string(platform.MarketplaceService)})
	}
	// PUBLIC files are readable by any authenticated service already.
	if m.AccessClassification != "PUBLIC" {
		if err := s.Media.ReplaceAccessPolicy(ctx, id, m.AccessClassification, grants, "listing-media-access:"+ids.New().String()); err != nil {
			return err
		}
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		if err := s.q.SetMarketplaceAccess(ctx, mustTx(ctx), db.SetMarketplaceAccessParams{MediaID: id, Granted: grant, SyncedAt: s.now()}); err != nil {
			return err
		}
		return s.record(ctx, "listing.media.marketplace_access", "media", id, map[string]any{"granted": grant, "classification": m.AccessClassification}, nil)
	})
}
