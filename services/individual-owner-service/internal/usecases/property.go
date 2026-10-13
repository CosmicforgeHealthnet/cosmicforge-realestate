package usecases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

// Aggregates (event aggregate_type).
const (
	AggProperty  = "property"
	AggOwnership = "ownership_record"
)

// AddressInput is a property address. Exact coordinates are owner-provided
// (geocode_source OWNER) until a maps provider is configured (§12).
type AddressInput struct {
	Line1       string   `json:"line1" v:"required,max=200"`
	Line2       *string  `json:"line2,omitempty" v:"max=200"`
	City        string   `json:"city" v:"required,max=100"`
	StateRegion string   `json:"state_region" v:"required,max=100"`
	PostalCode  *string  `json:"postal_code,omitempty" v:"max=20"`
	CountryCode string   `json:"country_code" v:"required,format=country" ex:"NG"`
	Latitude    *float64 `json:"latitude,omitempty" v:"min=-90,max=90"`
	Longitude   *float64 `json:"longitude,omitempty" v:"min=-180,max=180"`
	Visibility  string   `json:"visibility,omitempty" v:"enum=EXACT|APPROXIMATE|HIDDEN" doc:"Public address precision; default APPROXIMATE"`
	AreaLabel   *string  `json:"area_label,omitempty" v:"max=120" doc:"Public neighbourhood label"`
}

// FeatureInput is one amenity/utility/accessibility feature.
type FeatureInput struct {
	Key      string  `json:"key" v:"required,pattern=[a-z][a-z0-9_]{1,59}" ex:"borehole"`
	Category string  `json:"category" v:"required,enum=AMENITY|UTILITY|ACCESSIBILITY|FACILITY|CONDITION|SECURITY"`
	Value    *string `json:"value,omitempty" v:"max=200"`
}

// CreatePropertyInput creates a persistent property record.
type CreatePropertyInput struct {
	Category     string         `json:"category" v:"required,enum=RESIDENTIAL|COMMERCIAL|LAND|MIXED_USE"`
	PropertyType string         `json:"property_type" v:"required,pattern=[A-Z][A-Z0-9_]{1,39}" ex:"APARTMENT"`
	Title        string         `json:"title" v:"required,min=3,max=160"`
	Description  *string        `json:"description,omitempty" v:"max=10000"`
	Timezone     string         `json:"timezone,omitempty" v:"max=64,pattern=[A-Za-z_]+(/[A-Za-z0-9_+-]+){0,2}" doc:"IANA zone; default Africa/Lagos"`
	Bedrooms     *int32         `json:"bedrooms,omitempty" v:"min=0,max=500"`
	Bathrooms    *int32         `json:"bathrooms,omitempty" v:"min=0,max=500"`
	FloorAreaSqm *int32         `json:"floor_area_sqm,omitempty" v:"min=1,max=100000000"`
	YearBuilt    *int32         `json:"year_built,omitempty" v:"min=1600,max=2200"`
	ExternalRef  *string        `json:"external_ref,omitempty" v:"max=128" doc:"Owner's own reference; unique per owner (import matching key)"`
	ClaimType    string         `json:"claim_type,omitempty" v:"enum=SOLE|JOINT|FAMILY|INHERITED|PROBATE|POWER_OF_ATTORNEY|SUBLETTING_AUTHORITY|LEASEHOLD" doc:"Default SOLE"`
	Address      AddressInput   `json:"address" v:"required"`
	Features     []FeatureInput `json:"features,omitempty" v:"max=100"`
}

// UpdatePropertyInput is a partial update; omitted fields are unchanged.
type UpdatePropertyInput struct {
	PropertyType *string         `json:"property_type,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,39}"`
	Title        *string         `json:"title,omitempty" v:"min=3,max=160"`
	Description  *string         `json:"description,omitempty" v:"max=10000" doc:"Empty string clears"`
	Timezone     *string         `json:"timezone,omitempty" v:"max=64,pattern=[A-Za-z_]+(/[A-Za-z0-9_+-]+){0,2}"`
	Bedrooms     *int32          `json:"bedrooms,omitempty" v:"min=0,max=500"`
	Bathrooms    *int32          `json:"bathrooms,omitempty" v:"min=0,max=500"`
	FloorAreaSqm *int32          `json:"floor_area_sqm,omitempty" v:"min=1,max=100000000"`
	YearBuilt    *int32          `json:"year_built,omitempty" v:"min=1600,max=2200"`
	Address      *AddressInput   `json:"address,omitempty" doc:"Changing the address of a verified property requires re-verification"`
	Features     *[]FeatureInput `json:"features,omitempty" v:"max=100" doc:"Replaces the whole feature set"`
}

// AddressView is the owner-facing address.
type AddressView struct {
	Line1         string     `json:"line1"`
	Line2         *string    `json:"line2"`
	City          string     `json:"city"`
	StateRegion   string     `json:"state_region"`
	PostalCode    *string    `json:"postal_code"`
	CountryCode   string     `json:"country_code"`
	Latitude      *float64   `json:"latitude"`
	Longitude     *float64   `json:"longitude"`
	Visibility    string     `json:"visibility"`
	AreaLabel     *string    `json:"area_label"`
	GeocodeSource string     `json:"geocode_source"`
	GeocodedAt    *time.Time `json:"geocoded_at"`
}

// FeatureView is a property feature.
type FeatureView struct {
	Key      string  `json:"key"`
	Category string  `json:"category"`
	Value    *string `json:"value"`
}

// OwnershipSummary is the primary ownership record state.
type OwnershipSummary struct {
	RecordID   ids.ID     `json:"record_id"`
	ClaimType  string     `json:"claim_type"`
	Status     string     `json:"status"`
	Verified   bool       `json:"verified"`
	VerifiedAt *time.Time `json:"verified_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Conflict   bool       `json:"conflict" doc:"Another owner's record conflicts with this claim; admin review required"`
}

// PropertyView is the owner-facing property.
type PropertyView struct {
	ID                ids.ID            `json:"id"`
	Status            string            `json:"status"`
	Category          string            `json:"category"`
	PropertyType      string            `json:"property_type"`
	Title             string            `json:"title"`
	Description       *string           `json:"description"`
	Timezone          string            `json:"timezone"`
	Bedrooms          *int32            `json:"bedrooms"`
	Bathrooms         *int32            `json:"bathrooms"`
	FloorAreaSqm      *int32            `json:"floor_area_sqm"`
	YearBuilt         *int32            `json:"year_built"`
	TransactionReady  bool              `json:"transaction_ready"`
	RestrictionReason *string           `json:"restriction_reason"`
	ExternalRef       *string           `json:"external_ref"`
	Address           *AddressView      `json:"address"`
	Features          []FeatureView     `json:"features"`
	Ownership         *OwnershipSummary `json:"ownership"`
	MyRole            string            `json:"my_role" doc:"Caller's relationship: OWNER, CO_OWNER, AUTHORIZED_REPRESENTATIVE or DELEGATED_MANAGER"`
	Version           int64             `json:"version"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// PropertySummary is a portfolio list row.
type PropertySummary struct {
	ID           ids.ID    `json:"id"`
	Status       string    `json:"status"`
	Category     string    `json:"category"`
	PropertyType string    `json:"property_type"`
	Title        string    `json:"title"`
	OwnerAccount bool      `json:"is_owner" doc:"The caller is the primary owner"`
	Version      int64     `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// PropertyEvent is the payload of property.created.v1 / property.updated.v1
// (Marketplace/Admin projections). It never contains the exact address or
// any owner personal data.
type PropertyEvent struct {
	SchemaVersion   int       `json:"schema_version" v:"required"`
	PropertyID      ids.ID    `json:"property_id"`
	Status          string    `json:"status"`
	Category        string    `json:"category"`
	PropertyType    string    `json:"property_type"`
	Title           string    `json:"title"`
	City            string    `json:"city"`
	StateRegion     string    `json:"state_region"`
	CountryCode     string    `json:"country_code"`
	AreaLabel       *string   `json:"area_label"`
	Bedrooms        *int32    `json:"bedrooms"`
	Bathrooms       *int32    `json:"bathrooms"`
	FloorAreaSqm    *int32    `json:"floor_area_sqm"`
	ChangedFields   []string  `json:"changed_fields"`
	PropertyVersion int64     `json:"property_version" doc:"Monotonic; consumers discard stale versions"`
	OccurredAt      time.Time `json:"occurred_at"`
}

func featureViews(fs []db.PropertyFeature) []FeatureView {
	out := make([]FeatureView, 0, len(fs))
	for _, f := range fs {
		out = append(out, FeatureView{Key: f.FeatureKey, Category: f.Category, Value: f.Value})
	}
	return out
}

func addressView(a db.PropertyAddress) *AddressView {
	return &AddressView{Line1: a.Line1, Line2: a.Line2, City: a.City, StateRegion: a.StateRegion, PostalCode: a.PostalCode,
		CountryCode: a.CountryCode, Latitude: a.Latitude, Longitude: a.Longitude, Visibility: a.Visibility, AreaLabel: a.AreaLabel,
		GeocodeSource: a.GeocodeSource, GeocodedAt: a.GeocodedAt}
}

func ownershipSummary(r db.OwnershipRecord, now time.Time) *OwnershipSummary {
	return &OwnershipSummary{RecordID: r.ID, ClaimType: r.ClaimType, Status: r.Status, VerifiedAt: r.VerifiedAt, ExpiresAt: r.ExpiresAt,
		Verified: r.Status == "VERIFIED" && (r.ExpiresAt == nil || r.ExpiresAt.After(now)), Conflict: r.Status == "CONFLICT"}
}

// AddressFingerprint normalizes an address (case, punctuation, whitespace)
// for duplicate-claim detection.
func AddressFingerprint(a AddressInput) string {
	norm := func(s string) string {
		var b strings.Builder
		space := false
		for _, r := range strings.ToLower(s) {
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r):
				b.WriteRune(r)
				space = false
			case !space:
				b.WriteByte(' ')
				space = true
			}
		}
		return strings.TrimSpace(b.String())
	}
	parts := []string{norm(a.Line1), norm(deref(a.Line2)), norm(a.City), norm(a.StateRegion), norm(deref(a.PostalCode)), strings.ToUpper(a.CountryCode)}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func validateCategory(category string, bedrooms, bathrooms *int32) error {
	if category == "LAND" && (bedrooms != nil || bathrooms != nil) {
		return apperrors.InvalidRequest("Land properties have no bedrooms or bathrooms.",
			apperrors.Detail{Field: "bedrooms", Code: "NOT_APPLICABLE", Message: "not applicable to LAND"})
	}
	return nil
}

func validateFeatures(fs []FeatureInput) error {
	seen := map[string]bool{}
	for _, f := range fs {
		if seen[f.Key] {
			return apperrors.InvalidRequest("Duplicate feature key "+f.Key+".", apperrors.Detail{Field: "features", Code: "DUPLICATE", Message: f.Key})
		}
		seen[f.Key] = true
	}
	return nil
}

// CreateProperty creates a persistent property with its address, features
// and primary ownership claim (DRAFT). A claim on an address another owner
// already holds is recorded as a CONFLICT for admin/verification review —
// the other owner is never disclosed.
func (s *Service) CreateProperty(ctx context.Context, subj authz.Subject, in CreatePropertyInput) (PropertyView, error) {
	if err := schema.Validate(in); err != nil {
		return PropertyView{}, err
	}
	if _, err := s.authorize(ctx, subj, authz.ActPropertyCreate, ids.Nil); err != nil {
		return PropertyView{}, err
	}
	var out PropertyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		p, err := s.createPropertyTx(ctx, accountOf(subj), in, nil)
		if err != nil {
			return err
		}
		out, err = s.propertyView(ctx, p.ID, string(authz.RoleOwner))
		return err
	})
	return out, err
}

func (s *Service) createPropertyTx(ctx context.Context, accountID ids.ID, in CreatePropertyInput, importID *ids.ID) (db.Property, error) {
	if err := validateCategory(in.Category, in.Bedrooms, in.Bathrooms); err != nil {
		return db.Property{}, err
	}
	if err := validateFeatures(in.Features); err != nil {
		return db.Property{}, err
	}
	tx, now := mustTx(ctx), s.now()
	if in.ExternalRef != nil {
		if existing, err := s.q.GetPropertyByExternalRef(ctx, tx, db.GetPropertyByExternalRefParams{OwnerAccountID: accountID, ExternalRef: in.ExternalRef}); err == nil {
			return db.Property{}, apperrors.Conflict("PROPERTY_EXTERNAL_REF_EXISTS", "A property with this external_ref already exists.").
				WithDetails(apperrors.Detail{Field: "property_id", Code: "EXISTING", Message: existing.ID.String()})
		} else if !isNoRows(err) {
			return db.Property{}, err
		}
	}
	id := ids.New()
	fp := AddressFingerprint(in.Address)
	dups, err := s.q.FindPropertiesByFingerprint(ctx, tx, db.FindPropertiesByFingerprintParams{Fingerprint: fp, ID: id})
	if err != nil {
		return db.Property{}, err
	}
	var conflictWith *ids.ID
	for _, d := range dups {
		if d.OwnerAccountID == accountID {
			return db.Property{}, apperrors.Conflict("PROPERTY_DUPLICATE", "You already have a property at this address.").
				WithDetails(apperrors.Detail{Field: "property_id", Code: "EXISTING", Message: d.ID.String()})
		}
		if conflictWith == nil {
			if rec, err := s.q.GetPrimaryOwnershipRecord(ctx, tx, d.ID); err == nil {
				conflictWith = &rec.ID
			} else if !isNoRows(err) {
				return db.Property{}, err
			}
		}
	}
	tz := in.Timezone
	if tz == "" {
		tz = "Africa/Lagos"
	}
	if err := validateTimezone(tz); err != nil {
		return db.Property{}, err
	}
	p, err := s.q.InsertProperty(ctx, tx, db.InsertPropertyParams{ID: id, OwnerAccountID: accountID, Category: in.Category, PropertyType: in.PropertyType,
		Title: strings.TrimSpace(in.Title), Description: in.Description, Timezone: tz, Bedrooms: in.Bedrooms, Bathrooms: in.Bathrooms,
		FloorAreaSqm: in.FloorAreaSqm, YearBuilt: in.YearBuilt, ExternalRef: in.ExternalRef, ImportID: importID, CreatedAt: now})
	if isUnique(err, "properties_external_ref_uniq") {
		return db.Property{}, apperrors.Conflict("PROPERTY_EXTERNAL_REF_EXISTS", "A property with this external_ref already exists.")
	}
	if err != nil {
		return db.Property{}, err
	}
	if err := s.saveAddress(ctx, id, in.Address, fp); err != nil {
		return db.Property{}, err
	}
	if err := s.replaceFeatures(ctx, id, in.Features); err != nil {
		return db.Property{}, err
	}
	claim := in.ClaimType
	if claim == "" {
		claim = "SOLE"
	}
	status := "DRAFT"
	if conflictWith != nil {
		status = "CONFLICT"
	}
	rec, err := s.q.InsertOwnershipRecord(ctx, tx, db.InsertOwnershipRecordParams{ID: ids.New(), PropertyID: id, ClaimantAccountID: accountID,
		ClaimType: claim, Status: status, IsPrimary: true, ConflictWithRecordID: conflictWith, CreatedAt: now})
	if err != nil {
		return db.Property{}, err
	}
	var share *int32
	if claim == "SOLE" {
		share = ptr(int32(10000))
	}
	if _, err := s.q.InsertOwnershipParty(ctx, tx, db.InsertOwnershipPartyParams{ID: ids.New(), OwnershipRecordID: rec.ID, PropertyID: id,
		PartyRole: "OWNER", AccountID: &accountID, DisplayName: "Primary owner", ShareBps: share, Capabilities: []string{}, Status: "ACTIVE",
		ValidFrom: &now, CreatedAt: now}); err != nil {
		return db.Property{}, err
	}
	if err := domain.Property.RecordCreation(ctx, tx, id, &id, "DRAFT", now); err != nil {
		return db.Property{}, err
	}
	if err := domain.Ownership.RecordCreation(ctx, tx, rec.ID, &id, status, now); err != nil {
		return db.Property{}, err
	}
	if err := s.propertyHistory(ctx, id, "PROPERTY_CREATED", "Property record created", "property", id); err != nil {
		return db.Property{}, err
	}
	if conflictWith != nil {
		if err := s.propertyHistory(ctx, id, "OWNERSHIP_CONFLICT", "Ownership claim conflicts with an existing record; awaiting review", AggOwnership, rec.ID); err != nil {
			return db.Property{}, err
		}
	}
	if err := s.record(ctx, "property.create", AggProperty, id, map[string]any{"category": in.Category, "ownership_status": status},
		map[string]any{"import_id": importID}); err != nil {
		return db.Property{}, err
	}
	return p, s.emitProperty(ctx, "property.created.v1", p, nil)
}

func (s *Service) saveAddress(ctx context.Context, propertyID ids.ID, a AddressInput, fp string) error {
	vis := a.Visibility
	if vis == "" {
		vis = "APPROXIMATE"
	}
	if (a.Latitude == nil) != (a.Longitude == nil) {
		return apperrors.InvalidRequest("latitude and longitude must be given together.",
			apperrors.Detail{Field: "address.latitude", Code: "INCOMPLETE", Message: "latitude and longitude must be given together"})
	}
	_, err := s.q.UpsertPropertyAddress(ctx, mustTx(ctx), db.UpsertPropertyAddressParams{PropertyID: propertyID, Line1: strings.TrimSpace(a.Line1),
		Line2: a.Line2, City: strings.TrimSpace(a.City), StateRegion: strings.TrimSpace(a.StateRegion), PostalCode: a.PostalCode,
		CountryCode: a.CountryCode, Latitude: a.Latitude, Longitude: a.Longitude, Visibility: vis, AreaLabel: a.AreaLabel,
		Fingerprint: fp, GeocodeSource: "OWNER", UpdatedAt: s.now()})
	return err
}

func (s *Service) replaceFeatures(ctx context.Context, propertyID ids.ID, fs []FeatureInput) error {
	tx := mustTx(ctx)
	if err := s.q.DeletePropertyFeatures(ctx, tx, propertyID); err != nil {
		return err
	}
	for _, f := range fs {
		if err := s.q.InsertPropertyFeature(ctx, tx, db.InsertPropertyFeatureParams{PropertyID: propertyID, FeatureKey: f.Key, Category: f.Category,
			Value: f.Value, UpdatedAt: s.now()}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) emitProperty(ctx context.Context, eventType string, p db.Property, changed []string) error {
	a, err := s.q.GetPropertyAddress(ctx, mustTx(ctx), p.ID)
	if err != nil {
		return err
	}
	if changed == nil {
		changed = []string{}
	}
	return s.publish(ctx, eventType, AggProperty, p.ID, PropertyEvent{SchemaVersion: 1, PropertyID: p.ID, Status: p.Status, Category: p.Category,
		PropertyType: p.PropertyType, Title: p.Title, City: a.City, StateRegion: a.StateRegion, CountryCode: a.CountryCode, AreaLabel: a.AreaLabel,
		Bedrooms: p.Bedrooms, Bathrooms: p.Bathrooms, FloorAreaSqm: p.FloorAreaSqm, ChangedFields: changed, PropertyVersion: p.Version, OccurredAt: s.now()})
}

func (s *Service) propertyView(ctx context.Context, id ids.ID, role string) (PropertyView, error) {
	q := s.dbq(ctx)
	p, err := s.q.GetProperty(ctx, q, id)
	if isNoRows(err) {
		return PropertyView{}, notFound(AggProperty)
	}
	if err != nil {
		return PropertyView{}, err
	}
	v := PropertyView{ID: p.ID, Status: p.Status, Category: p.Category, PropertyType: p.PropertyType, Title: p.Title, Description: p.Description,
		Timezone: p.Timezone, Bedrooms: p.Bedrooms, Bathrooms: p.Bathrooms, FloorAreaSqm: p.FloorAreaSqm, YearBuilt: p.YearBuilt,
		TransactionReady: p.TransactionReady, RestrictionReason: p.RestrictionReason, ExternalRef: p.ExternalRef, MyRole: role,
		Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if a, err := s.q.GetPropertyAddress(ctx, q, id); err == nil {
		v.Address = addressView(a)
	} else if !isNoRows(err) {
		return PropertyView{}, err
	}
	fs, err := s.q.ListPropertyFeatures(ctx, q, id)
	if err != nil {
		return PropertyView{}, err
	}
	v.Features = featureViews(fs)
	if r, err := s.q.GetPrimaryOwnershipRecord(ctx, q, id); err == nil {
		v.Ownership = ownershipSummary(r, s.now())
	} else if !isNoRows(err) {
		return PropertyView{}, err
	}
	return v, nil
}

func viaRole(d authz.Decision) string {
	if d.Via == nil {
		return ""
	}
	return string(d.Via.Role)
}

// GetProperty returns one property the caller has authority over.
func (s *Service) GetProperty(ctx context.Context, subj authz.Subject, id ids.ID) (PropertyView, error) {
	d, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), id)
	if err != nil {
		return PropertyView{}, err
	}
	return s.propertyView(ctx, id, viaRole(d))
}

type timeCursor struct {
	At time.Time `json:"t"`
	ID ids.ID    `json:"i"`
}

func decodeTimeCursor(c string) (*time.Time, *ids.ID, error) {
	var tc timeCursor
	ok, err := pagination.Decode(c, &tc)
	if err != nil || !ok {
		return nil, nil, err
	}
	return &tc.At, &tc.ID, nil
}

// ListProperties lists the caller's portfolio (owned, co-owned and
// represented properties).
func (s *Service) ListProperties(ctx context.Context, subj authz.Subject, status, category, cursor string, limit int) (pagination.Response[PropertySummary], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[PropertySummary]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[PropertySummary]{}, err
	}
	rows, err := s.q.ListAccessibleProperties(ctx, s.dbq(ctx), db.ListAccessiblePropertiesParams{AccountID: accountOf(subj), Now: s.now(),
		Status: strp(status), Category: strp(category), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[PropertySummary]{}, err
	}
	out := make([]PropertySummary, 0, len(rows))
	for _, p := range rows {
		out = append(out, PropertySummary{ID: p.ID, Status: p.Status, Category: p.Category, PropertyType: p.PropertyType, Title: p.Title,
			OwnerAccount: p.OwnerAccountID == accountOf(subj), Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt})
	}
	return pagination.Build(out, limit, func(p PropertySummary) any { return timeCursor{At: p.CreatedAt, ID: p.ID} })
}

// UpdateProperty applies a partial update with If-Match.
func (s *Service) UpdateProperty(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in UpdatePropertyInput) (PropertyView, error) {
	if err := schema.Validate(in); err != nil {
		return PropertyView{}, err
	}
	if in.Features != nil {
		if err := validateFeatures(*in.Features); err != nil {
			return PropertyView{}, err
		}
	}
	var out PropertyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		d, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyEdit), id)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if _, err := s.requireMutable(ctx, id); err != nil {
			return err
		}
		p, err := s.q.GetPropertyForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := checkVersion(expected, p.Version); err != nil {
			return err
		}
		var changed []string
		set := func(name string, cond bool) {
			if cond {
				changed = append(changed, name)
			}
		}
		pt, title, tz := p.PropertyType, p.Title, p.Timezone
		if in.PropertyType != nil {
			set("property_type", *in.PropertyType != pt)
			pt = *in.PropertyType
		}
		if in.Title != nil {
			set("title", strings.TrimSpace(*in.Title) != title)
			title = strings.TrimSpace(*in.Title)
		}
		if in.Timezone != nil {
			tz = *in.Timezone
			if err := validateTimezone(tz); err != nil {
				return err
			}
		}
		desc := p.Description
		if in.Description != nil {
			set("description", deref(p.Description) != *in.Description)
			desc = strp(*in.Description)
		}
		pick := func(name string, cur, nv *int32) *int32 {
			if nv == nil {
				return cur
			}
			set(name, cur == nil || *cur != *nv)
			return nv
		}
		beds, baths := pick("bedrooms", p.Bedrooms, in.Bedrooms), pick("bathrooms", p.Bathrooms, in.Bathrooms)
		area, year := pick("floor_area_sqm", p.FloorAreaSqm, in.FloorAreaSqm), pick("year_built", p.YearBuilt, in.YearBuilt)
		if err := validateCategory(p.Category, beds, baths); err != nil {
			return err
		}
		np, err := s.q.UpdatePropertyDetails(ctx, tx, db.UpdatePropertyDetailsParams{ID: id, Version: p.Version, PropertyType: pt, Title: title,
			Description: desc, Timezone: tz, Bedrooms: beds, Bathrooms: baths, FloorAreaSqm: area, YearBuilt: year, UpdatedAt: now})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		if in.Address != nil {
			fp := AddressFingerprint(*in.Address)
			old, err := s.q.GetPropertyAddress(ctx, tx, id)
			if err != nil {
				return err
			}
			if old.Fingerprint != fp {
				changed = append(changed, "address")
				if err := s.addressChanged(ctx, id, fp); err != nil {
					return err
				}
			}
			if err := s.saveAddress(ctx, id, *in.Address, fp); err != nil {
				return err
			}
		}
		if in.Features != nil {
			changed = append(changed, "features")
			if err := s.replaceFeatures(ctx, id, *in.Features); err != nil {
				return err
			}
		}
		if err := s.propertyHistory(ctx, id, "PROPERTY_UPDATED", "Property details updated: "+strings.Join(changed, ", "), AggProperty, id); err != nil {
			return err
		}
		if err := s.record(ctx, "property.update", AggProperty, id, map[string]any{"changed": changed, "version": np.Version}, nil); err != nil {
			return err
		}
		if len(changed) > 0 {
			if err := s.emitProperty(ctx, "property.updated.v1", np, changed); err != nil {
				return err
			}
		}
		out, err = s.propertyView(ctx, id, viaRole(d))
		return err
	})
	return out, err
}

// addressChanged handles a new address: duplicate detection and
// re-verification of a verified authority.
func (s *Service) addressChanged(ctx context.Context, id ids.ID, fp string) error {
	tx := mustTx(ctx)
	p, err := s.q.GetProperty(ctx, tx, id)
	if err != nil {
		return err
	}
	dups, err := s.q.FindPropertiesByFingerprint(ctx, tx, db.FindPropertiesByFingerprintParams{Fingerprint: fp, ID: id})
	if err != nil {
		return err
	}
	for _, d := range dups {
		if d.OwnerAccountID == p.OwnerAccountID {
			return apperrors.Conflict("PROPERTY_DUPLICATE", "You already have a property at this address.")
		}
	}
	if len(dups) > 0 {
		// Moving onto another owner's address is a conflicting claim.
		return precondition("PROPERTY_ADDRESS_CONFLICT", "This address is held by another ownership record; contact support to resolve the conflict.")
	}
	rec, err := s.q.GetPrimaryOwnershipRecordForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if rec.Status == "VERIFIED" {
		if _, _, err := s.transition(ctx, domain.Ownership, rec.ID, lifecycle.Change{To: "REVERIFICATION_REQUIRED", Command: "address_changed",
			Reason: "ADDRESS_CHANGED"}); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, id, "REVERIFICATION_REQUIRED", "Address changed; ownership authority must be re-verified", AggOwnership, rec.ID); err != nil {
			return err
		}
		return s.emitVerificationChanged(ctx, id)
	}
	return nil
}

// ArchiveProperty archives a property without active business.
func (s *Service) ArchiveProperty(ctx context.Context, subj authz.Subject, id ids.ID, reason string) (PropertyView, error) {
	var out PropertyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		d, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyArchive), id)
		if err != nil {
			return err
		}
		blockers, err := s.archiveBlockers(ctx, id)
		if err != nil {
			return err
		}
		if len(blockers) > 0 {
			return conflict("PROPERTY_HAS_ACTIVE_BUSINESS", "The property has active business: "+strings.Join(blockers, ", ")+".")
		}
		if _, _, err := s.transition(ctx, domain.Property, id, lifecycle.Change{To: "ARCHIVED", Command: "archive", Reason: reason}); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, id, "PROPERTY_ARCHIVED", "Property archived", AggProperty, id); err != nil {
			return err
		}
		if err := s.record(ctx, "property.archive", AggProperty, id, map[string]any{"status": "ARCHIVED"}, map[string]any{"reason": reason}); err != nil {
			return err
		}
		if err := s.emitProperty(ctx, "property.updated.v1", p, []string{"status"}); err != nil {
			return err
		}
		out, err = s.propertyView(ctx, id, viaRole(d))
		return err
	})
	return out, err
}

// archiveBlockerChecks are registered by the business contexts that hold a
// property in use (listings, tenancies, sales, bookings, assignments ...).
var archiveBlockerChecks = []struct{ name, sql string }{
	{"open ownership transfer", `SELECT EXISTS (SELECT 1 FROM ownership_change_cases WHERE property_id = $1 AND status IN ('OPEN','EVIDENCE_PENDING','UNDER_REVIEW','APPROVED'))`},
	{"active manager assignment", `SELECT EXISTS (SELECT 1 FROM manager_assignments WHERE property_id = $1 AND status IN ('PROPOSED','ACCEPTED','ACTIVE','SUSPENDED','EXPIRING'))`},
}

func (s *Service) archiveBlockers(ctx context.Context, id ids.ID) ([]string, error) {
	var out []string
	for _, c := range archiveBlockerChecks {
		var b bool
		if err := mustTx(ctx).QueryRow(ctx, c.sql, id).Scan(&b); err != nil {
			return nil, err
		}
		if b {
			out = append(out, c.name)
		}
	}
	return out, nil
}

// HistoryView is an owner-visible property history entry.
type HistoryView struct {
	ID          ids.ID    `json:"id"`
	EventType   string    `json:"event_type"`
	Summary     string    `json:"summary"`
	RefType     *string   `json:"ref_type"`
	RefID       *ids.ID   `json:"ref_id"`
	ActorDomain *string   `json:"actor_domain"`
	ActorID     *ids.ID   `json:"actor_id"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// PropertyHistory pages the immutable property history.
func (s *Service) PropertyHistory(ctx context.Context, subj authz.Subject, id ids.ID, cursor string, limit int) (pagination.Response[HistoryView], error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), id); err != nil {
		return pagination.Response[HistoryView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[HistoryView]{}, err
	}
	rows, err := s.q.ListPropertyHistory(ctx, s.dbq(ctx), db.ListPropertyHistoryParams{PropertyID: id, CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[HistoryView]{}, err
	}
	out := make([]HistoryView, 0, len(rows))
	for _, h := range rows {
		out = append(out, HistoryView{ID: h.ID, EventType: h.EventType, Summary: h.Summary, RefType: h.RefType, RefID: h.RefID,
			ActorDomain: h.ActorDomain, ActorID: h.ActorID, OccurredAt: h.OccurredAt})
	}
	return pagination.Build(out, limit, func(h HistoryView) any { return timeCursor{At: h.OccurredAt, ID: h.ID} })
}

// requireMutable loads a property for a write and rejects archived or
// transfer-locked properties.
func (s *Service) requireMutable(ctx context.Context, id ids.ID) (db.Property, error) {
	p, err := s.q.GetProperty(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return db.Property{}, notFound(AggProperty)
	}
	if err != nil {
		return db.Property{}, err
	}
	switch p.Status {
	case "ARCHIVED":
		return p, conflict("PROPERTY_ARCHIVED", "Archived properties cannot be changed.")
	case "TRANSFER_PENDING":
		return p, conflict("PROPERTY_TRANSFER_PENDING", "The property is locked while an ownership transfer is pending.")
	}
	return p, nil
}
