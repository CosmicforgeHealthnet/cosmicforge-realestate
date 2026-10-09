package usecases

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// ---- Units (§13.5: separate property root, units and parcels) ----

// UnitView is a unit/room/floor/block of a property.
type UnitView struct {
	ID           ids.ID    `json:"id"`
	PropertyID   ids.ID    `json:"property_id"`
	ParentUnitID *ids.ID   `json:"parent_unit_id"`
	UnitType     string    `json:"unit_type"`
	Label        string    `json:"label"`
	FloorNumber  *int32    `json:"floor_number"`
	Bedrooms     *int32    `json:"bedrooms"`
	Bathrooms    *int32    `json:"bathrooms"`
	FloorAreaSqm *int32    `json:"floor_area_sqm"`
	Status       string    `json:"status"`
	Version      int64     `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// UnitInput creates a unit.
type UnitInput struct {
	ParentUnitID *ids.ID `json:"parent_unit_id,omitempty"`
	UnitType     string  `json:"unit_type" v:"required,enum=BUILDING|BLOCK|FLOOR|UNIT|ROOM|SHOP|OFFICE|WAREHOUSE|OTHER"`
	Label        string  `json:"label" v:"required,max=80" ex:"Flat 2B"`
	FloorNumber  *int32  `json:"floor_number,omitempty" v:"min=-10,max=300"`
	Bedrooms     *int32  `json:"bedrooms,omitempty" v:"min=0,max=100"`
	Bathrooms    *int32  `json:"bathrooms,omitempty" v:"min=0,max=100"`
	FloorAreaSqm *int32  `json:"floor_area_sqm,omitempty" v:"min=1,max=10000000"`
}

// UnitPatch updates a unit (omitted fields unchanged).
type UnitPatch struct {
	Label        *string `json:"label,omitempty" v:"max=80"`
	FloorNumber  *int32  `json:"floor_number,omitempty" v:"min=-10,max=300"`
	Bedrooms     *int32  `json:"bedrooms,omitempty" v:"min=0,max=100"`
	Bathrooms    *int32  `json:"bathrooms,omitempty" v:"min=0,max=100"`
	FloorAreaSqm *int32  `json:"floor_area_sqm,omitempty" v:"min=1,max=10000000"`
	Status       *string `json:"status,omitempty" v:"enum=ACTIVE|ARCHIVED" doc:"ARCHIVED retires the unit (blocked while it has active child units or business)"`
}

func unitView(u db.PropertyUnit) UnitView {
	return UnitView{ID: u.ID, PropertyID: u.PropertyID, ParentUnitID: u.ParentUnitID, UnitType: u.UnitType, Label: u.Label, FloorNumber: u.FloorNumber,
		Bedrooms: u.Bedrooms, Bathrooms: u.Bathrooms, FloorAreaSqm: u.FloorAreaSqm, Status: u.Status, Version: u.Version, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

// ListUnits lists a property's units.
func (s *Service) ListUnits(ctx context.Context, subj authz.Subject, propertyID ids.ID, includeArchived bool) ([]UnitView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListUnits(ctx, s.dbq(ctx), db.ListUnitsParams{PropertyID: propertyID, IncludeArchived: includeArchived})
	out := make([]UnitView, 0, len(rows))
	for _, u := range rows {
		out = append(out, unitView(u))
	}
	return out, err
}

// CreateUnit adds a unit (parent must be an active unit of the same property).
func (s *Service) CreateUnit(ctx context.Context, subj authz.Subject, propertyID ids.ID, in UnitInput) (UnitView, error) {
	if err := schema.Validate(in); err != nil {
		return UnitView{}, err
	}
	var out UnitView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapUnitsManage), propertyID); err != nil {
			return err
		}
		p, err := s.requireMutable(ctx, propertyID)
		if err != nil {
			return err
		}
		if p.Category == "LAND" {
			return precondition("UNITS_NOT_APPLICABLE", "Land properties use parcels, not units.")
		}
		tx := mustTx(ctx)
		if in.ParentUnitID != nil {
			parent, err := s.q.GetUnit(ctx, tx, db.GetUnitParams{ID: *in.ParentUnitID, PropertyID: propertyID})
			if isNoRows(err) || (err == nil && parent.Status != "ACTIVE") {
				return precondition("PARENT_UNIT_INVALID", "The parent unit does not exist on this property or is archived.")
			}
			if err != nil {
				return err
			}
		}
		u, err := s.q.InsertUnit(ctx, tx, db.InsertUnitParams{ID: ids.New(), PropertyID: propertyID, ParentUnitID: in.ParentUnitID, UnitType: in.UnitType,
			Label: strings.TrimSpace(in.Label), FloorNumber: in.FloorNumber, Bedrooms: in.Bedrooms, Bathrooms: in.Bathrooms, FloorAreaSqm: in.FloorAreaSqm, CreatedAt: s.now()})
		if isUnique(err, "property_units_label_uniq") {
			return conflict("UNIT_LABEL_TAKEN", "Another active unit at this level has the same label.")
		}
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "UNIT_ADDED", "Unit added: "+u.Label, "property_unit", u.ID); err != nil {
			return err
		}
		out = unitView(u)
		return s.record(ctx, "property.unit.create", "property_unit", u.ID, map[string]any{"unit_type": u.UnitType}, nil)
	})
	return out, err
}

// unitArchiveChecks are registered by contexts that occupy units (tenancies, bookings, listings).
var unitArchiveChecks = []struct{ name, sql string }{}

// UpdateUnit patches a unit with If-Match.
func (s *Service) UpdateUnit(ctx context.Context, subj authz.Subject, propertyID, unitID ids.ID, expected int64, in UnitPatch) (UnitView, error) {
	if err := schema.Validate(in); err != nil {
		return UnitView{}, err
	}
	var out UnitView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapUnitsManage), propertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, propertyID); err != nil {
			return err
		}
		tx := mustTx(ctx)
		u, err := s.q.GetUnit(ctx, tx, db.GetUnitParams{ID: unitID, PropertyID: propertyID})
		if isNoRows(err) {
			return notFound("unit")
		}
		if err != nil {
			return err
		}
		if err := checkVersion(expected, u.Version); err != nil {
			return err
		}
		label, status := u.Label, u.Status
		if in.Label != nil {
			label = strings.TrimSpace(*in.Label)
		}
		if in.Status != nil && *in.Status != u.Status {
			status = *in.Status
			if status == "ARCHIVED" {
				n, err := s.q.CountActiveChildUnits(ctx, tx, &unitID)
				if err != nil {
					return err
				}
				if n > 0 {
					return conflict("UNIT_HAS_CHILDREN", "Archive the unit's active child units first.")
				}
				for _, c := range unitArchiveChecks {
					var busy bool
					if err := tx.QueryRow(ctx, c.sql, unitID).Scan(&busy); err != nil {
						return err
					}
					if busy {
						return conflict("UNIT_IN_USE", "The unit has an active "+c.name+".")
					}
				}
			}
		}
		pick := func(cur, nv *int32) *int32 {
			if nv != nil {
				return nv
			}
			return cur
		}
		nu, err := s.q.UpdateUnit(ctx, tx, db.UpdateUnitParams{ID: unitID, PropertyID: propertyID, Label: label, FloorNumber: pick(u.FloorNumber, in.FloorNumber),
			Bedrooms: pick(u.Bedrooms, in.Bedrooms), Bathrooms: pick(u.Bathrooms, in.Bathrooms), FloorAreaSqm: pick(u.FloorAreaSqm, in.FloorAreaSqm),
			Status: status, UpdatedAt: s.now(), Version: u.Version})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if isUnique(err, "property_units_label_uniq") {
			return conflict("UNIT_LABEL_TAKEN", "Another active unit at this level has the same label.")
		}
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "UNIT_UPDATED", "Unit updated: "+nu.Label, "property_unit", unitID); err != nil {
			return err
		}
		out = unitView(nu)
		return s.record(ctx, "property.unit.update", "property_unit", unitID, map[string]any{"status": status}, nil)
	})
	return out, err
}

// ---- Land parcels ----

// ParcelView is a land parcel.
type ParcelView struct {
	ID          ids.ID          `json:"id"`
	PropertyID  ids.ID          `json:"property_id"`
	ParcelRef   string          `json:"parcel_ref"`
	TitleType   *string         `json:"title_type"`
	AreaSqm     *int32          `json:"area_sqm"`
	Zoning      *string         `json:"zoning"`
	AccessNotes *string         `json:"access_notes"`
	Boundary    json.RawMessage `json:"boundary_geojson" doc:"GeoJSON Polygon/MultiPolygon (owner-provided)"`
	Status      string          `json:"status"`
	Version     int64           `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ParcelInput creates or replaces a parcel.
type ParcelInput struct {
	ParcelRef   string          `json:"parcel_ref" v:"required,max=120" doc:"Survey plan / registry reference"`
	TitleType   *string         `json:"title_type,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,39}" ex:"CERTIFICATE_OF_OCCUPANCY"`
	AreaSqm     *int32          `json:"area_sqm,omitempty" v:"min=1,max=2000000000"`
	Zoning      *string         `json:"zoning,omitempty" v:"max=120"`
	AccessNotes *string         `json:"access_notes,omitempty" v:"max=2000"`
	Boundary    json.RawMessage `json:"boundary_geojson,omitempty"`
	Status      string          `json:"status,omitempty" v:"enum=ACTIVE|ARCHIVED"`
}

func parcelView(p db.LandParcel) ParcelView {
	b := json.RawMessage(p.BoundaryGeojson)
	if len(b) == 0 {
		b = json.RawMessage("null")
	}
	return ParcelView{ID: p.ID, PropertyID: p.PropertyID, ParcelRef: p.ParcelRef, TitleType: p.TitleType, AreaSqm: p.AreaSqm, Zoning: p.Zoning,
		AccessNotes: p.AccessNotes, Boundary: b, Status: p.Status, Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

func validBoundary(b json.RawMessage) ([]byte, error) {
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	if len(b) > 64<<10 {
		return nil, apperrors.InvalidRequest("boundary_geojson is too large (max 64 KiB).")
	}
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(b, &g); err != nil || (g.Type != "Polygon" && g.Type != "MultiPolygon") || len(g.Coordinates) == 0 {
		return nil, apperrors.InvalidRequest("boundary_geojson must be a GeoJSON Polygon or MultiPolygon.",
			apperrors.Detail{Field: "boundary_geojson", Code: "INVALID_FORMAT", Message: "Polygon or MultiPolygon"})
	}
	return b, nil
}

// ListParcels lists a property's parcels.
func (s *Service) ListParcels(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]ParcelView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListParcels(ctx, s.dbq(ctx), propertyID)
	out := make([]ParcelView, 0, len(rows))
	for _, p := range rows {
		out = append(out, parcelView(p))
	}
	return out, err
}

// CreateParcel adds a land parcel (LAND and MIXED_USE properties).
func (s *Service) CreateParcel(ctx context.Context, subj authz.Subject, propertyID ids.ID, in ParcelInput) (ParcelView, error) {
	if err := schema.Validate(in); err != nil {
		return ParcelView{}, err
	}
	boundary, err := validBoundary(in.Boundary)
	if err != nil {
		return ParcelView{}, err
	}
	var out ParcelView
	err = s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyEdit), propertyID); err != nil {
			return err
		}
		p, err := s.requireMutable(ctx, propertyID)
		if err != nil {
			return err
		}
		if p.Category != "LAND" && p.Category != "MIXED_USE" {
			return precondition("PARCELS_NOT_APPLICABLE", "Parcels apply to LAND and MIXED_USE properties.")
		}
		row, err := s.q.InsertParcel(ctx, mustTx(ctx), db.InsertParcelParams{ID: ids.New(), PropertyID: propertyID, ParcelRef: strings.TrimSpace(in.ParcelRef),
			TitleType: in.TitleType, AreaSqm: in.AreaSqm, Zoning: in.Zoning, AccessNotes: in.AccessNotes, BoundaryGeojson: boundary, CreatedAt: s.now()})
		if isUnique(err, "land_parcels_ref_uniq") {
			return conflict("PARCEL_REF_TAKEN", "An active parcel with this reference exists.")
		}
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "PARCEL_ADDED", "Parcel added: "+row.ParcelRef, "land_parcel", row.ID); err != nil {
			return err
		}
		out = parcelView(row)
		return s.record(ctx, "property.parcel.create", "land_parcel", row.ID, nil, nil)
	})
	return out, err
}

// UpdateParcel replaces a parcel with If-Match.
func (s *Service) UpdateParcel(ctx context.Context, subj authz.Subject, propertyID, parcelID ids.ID, expected int64, in ParcelInput) (ParcelView, error) {
	if err := schema.Validate(in); err != nil {
		return ParcelView{}, err
	}
	boundary, err := validBoundary(in.Boundary)
	if err != nil {
		return ParcelView{}, err
	}
	var out ParcelView
	err = s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyEdit), propertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, propertyID); err != nil {
			return err
		}
		tx := mustTx(ctx)
		cur, err := s.q.GetParcel(ctx, tx, db.GetParcelParams{ID: parcelID, PropertyID: propertyID})
		if isNoRows(err) {
			return notFound("parcel")
		}
		if err != nil {
			return err
		}
		if err := checkVersion(expected, cur.Version); err != nil {
			return err
		}
		status := in.Status
		if status == "" {
			status = cur.Status
		}
		row, err := s.q.UpdateParcel(ctx, tx, db.UpdateParcelParams{ID: parcelID, PropertyID: propertyID, ParcelRef: strings.TrimSpace(in.ParcelRef),
			TitleType: in.TitleType, AreaSqm: in.AreaSqm, Zoning: in.Zoning, AccessNotes: in.AccessNotes, BoundaryGeojson: boundary, Status: status,
			UpdatedAt: s.now(), Version: cur.Version})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if isUnique(err, "land_parcels_ref_uniq") {
			return conflict("PARCEL_REF_TAKEN", "An active parcel with this reference exists.")
		}
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "PARCEL_UPDATED", "Parcel updated: "+row.ParcelRef, "land_parcel", row.ID); err != nil {
			return err
		}
		out = parcelView(row)
		return s.record(ctx, "property.parcel.update", "land_parcel", row.ID, map[string]any{"status": status}, nil)
	})
	return out, err
}

// ---- Asset register ----

// AssetView is an asset (appliance, system, fixture) of a property.
type AssetView struct {
	ID                  ids.ID    `json:"id"`
	PropertyID          ids.ID    `json:"property_id"`
	UnitID              *ids.ID   `json:"unit_id"`
	AssetType           string    `json:"asset_type"`
	Name                string    `json:"name"`
	Make                *string   `json:"make"`
	Model               *string   `json:"model"`
	SerialNumber        *string   `json:"serial_number"`
	InstalledOn         *string   `json:"installed_on" doc:"YYYY-MM-DD"`
	WarrantyUntil       *string   `json:"warranty_until"`
	ServiceIntervalDays *int32    `json:"service_interval_days"`
	LastServicedOn      *string   `json:"last_serviced_on"`
	NextServiceDue      *string   `json:"next_service_due"`
	Status              string    `json:"status"`
	Version             int64     `json:"version"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// AssetInput creates or replaces an asset.
type AssetInput struct {
	UnitID              *ids.ID `json:"unit_id,omitempty"`
	AssetType           string  `json:"asset_type" v:"required,pattern=[A-Z][A-Z0-9_]{1,39}" ex:"GENERATOR"`
	Name                string  `json:"name" v:"required,max=120"`
	Make                *string `json:"make,omitempty" v:"max=80"`
	Model               *string `json:"model,omitempty" v:"max=80"`
	SerialNumber        *string `json:"serial_number,omitempty" v:"max=80"`
	InstalledOn         *string `json:"installed_on,omitempty" v:"format=date"`
	WarrantyUntil       *string `json:"warranty_until,omitempty" v:"format=date"`
	ServiceIntervalDays *int32  `json:"service_interval_days,omitempty" v:"min=1,max=3650"`
	LastServicedOn      *string `json:"last_serviced_on,omitempty" v:"format=date"`
	Status              string  `json:"status,omitempty" v:"enum=ACTIVE|RETIRED"`
}

func dateStr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.Format(time.DateOnly)
	return &v
}

func assetView(a db.PropertyAsset) AssetView {
	v := AssetView{ID: a.ID, PropertyID: a.PropertyID, UnitID: a.UnitID, AssetType: a.AssetType, Name: a.Name, Make: a.Make, Model: a.Model,
		SerialNumber: a.SerialNumber, InstalledOn: dateStr(a.InstalledOn), WarrantyUntil: dateStr(a.WarrantyUntil), ServiceIntervalDays: a.ServiceIntervalDays,
		LastServicedOn: dateStr(a.LastServicedOn), Status: a.Status, Version: a.Version, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	if a.ServiceIntervalDays != nil {
		base := a.InstalledOn
		if a.LastServicedOn != nil {
			base = a.LastServicedOn
		}
		if base != nil {
			next := base.AddDate(0, 0, int(*a.ServiceIntervalDays))
			v.NextServiceDue = dateStr(&next)
		}
	}
	return v
}

// ListAssets lists a property's asset register.
func (s *Service) ListAssets(ctx context.Context, subj authz.Subject, propertyID ids.ID, includeRetired bool) ([]AssetView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapAssetsManage), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAssets(ctx, s.dbq(ctx), db.ListAssetsParams{PropertyID: propertyID, IncludeRetired: includeRetired})
	out := make([]AssetView, 0, len(rows))
	for _, a := range rows {
		out = append(out, assetView(a))
	}
	return out, err
}

func (s *Service) checkAssetUnit(ctx context.Context, propertyID ids.ID, unitID *ids.ID) error {
	if unitID == nil {
		return nil
	}
	u, err := s.q.GetUnit(ctx, mustTx(ctx), db.GetUnitParams{ID: *unitID, PropertyID: propertyID})
	if isNoRows(err) || (err == nil && u.Status != "ACTIVE") {
		return precondition("UNIT_INVALID", "The unit does not exist on this property or is archived.")
	}
	return err
}

// CreateAsset registers an asset.
func (s *Service) CreateAsset(ctx context.Context, subj authz.Subject, propertyID ids.ID, in AssetInput) (AssetView, error) {
	if err := schema.Validate(in); err != nil {
		return AssetView{}, err
	}
	var out AssetView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapAssetsManage), propertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, propertyID); err != nil {
			return err
		}
		if err := s.checkAssetUnit(ctx, propertyID, in.UnitID); err != nil {
			return err
		}
		a, err := s.q.InsertAsset(ctx, mustTx(ctx), db.InsertAssetParams{ID: ids.New(), PropertyID: propertyID, UnitID: in.UnitID, AssetType: in.AssetType,
			Name: strings.TrimSpace(in.Name), Make: in.Make, Model: in.Model, SerialNumber: in.SerialNumber, InstalledOn: schema.ParseDate(in.InstalledOn),
			WarrantyUntil: schema.ParseDate(in.WarrantyUntil), ServiceIntervalDays: in.ServiceIntervalDays, LastServicedOn: schema.ParseDate(in.LastServicedOn), CreatedAt: s.now()})
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "ASSET_ADDED", "Asset registered: "+a.Name, "property_asset", a.ID); err != nil {
			return err
		}
		out = assetView(a)
		return s.record(ctx, "property.asset.create", "property_asset", a.ID, map[string]any{"asset_type": a.AssetType}, nil)
	})
	return out, err
}

// UpdateAsset replaces an asset with If-Match.
func (s *Service) UpdateAsset(ctx context.Context, subj authz.Subject, propertyID, assetID ids.ID, expected int64, in AssetInput) (AssetView, error) {
	if err := schema.Validate(in); err != nil {
		return AssetView{}, err
	}
	var out AssetView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapAssetsManage), propertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, propertyID); err != nil {
			return err
		}
		tx := mustTx(ctx)
		cur, err := s.q.GetAsset(ctx, tx, db.GetAssetParams{ID: assetID, PropertyID: propertyID})
		if isNoRows(err) {
			return notFound("asset")
		}
		if err != nil {
			return err
		}
		if err := checkVersion(expected, cur.Version); err != nil {
			return err
		}
		if cur.AssetType != in.AssetType {
			return apperrors.InvalidRequest("asset_type cannot change; register a new asset.")
		}
		if err := s.checkAssetUnit(ctx, propertyID, in.UnitID); err != nil {
			return err
		}
		status := in.Status
		if status == "" {
			status = cur.Status
		}
		a, err := s.q.UpdateAsset(ctx, tx, db.UpdateAssetParams{ID: assetID, PropertyID: propertyID, UnitID: in.UnitID, Name: strings.TrimSpace(in.Name),
			Make: in.Make, Model: in.Model, SerialNumber: in.SerialNumber, InstalledOn: schema.ParseDate(in.InstalledOn), WarrantyUntil: schema.ParseDate(in.WarrantyUntil),
			ServiceIntervalDays: in.ServiceIntervalDays, LastServicedOn: schema.ParseDate(in.LastServicedOn), Status: status, UpdatedAt: s.now(), Version: cur.Version})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "ASSET_UPDATED", "Asset updated: "+a.Name, "property_asset", a.ID); err != nil {
			return err
		}
		out = assetView(a)
		return s.record(ctx, "property.asset.update", "property_asset", a.ID, map[string]any{"status": status}, nil)
	})
	return out, err
}

// ---- Property media (media-service IDs only) ----

// MediaLinkView is a property media reference.
type MediaLinkView struct {
	ID          ids.ID     `json:"id"`
	MediaID     ids.ID     `json:"media_id"`
	Purpose     string     `json:"purpose"`
	MediaStatus string     `json:"media_status" doc:"Usability projection from media-document-service: PENDING, READY, REJECTED, DELETED"`
	Position    int32      `json:"position"`
	Caption     *string    `json:"caption"`
	GeotagLat   *float64   `json:"geotag_lat" doc:"Client-supplied capture metadata (untrusted; informational)"`
	GeotagLon   *float64   `json:"geotag_lon"`
	CapturedAt  *time.Time `json:"captured_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// MediaLinkInput links an uploaded media file to a property.
type MediaLinkInput struct {
	MediaID    ids.ID     `json:"media_id" v:"required"`
	Purpose    string     `json:"purpose" v:"required,enum=PHOTO|FLOOR_PLAN|VIDEO|VIRTUAL_TOUR|INSPECTION|DOCUMENT"`
	Position   int32      `json:"position,omitempty" v:"min=0,max=1000"`
	Caption    *string    `json:"caption,omitempty" v:"max=200"`
	GeotagLat  *float64   `json:"geotag_lat,omitempty" v:"min=-90,max=90"`
	GeotagLon  *float64   `json:"geotag_lon,omitempty" v:"min=-180,max=180"`
	CapturedAt *time.Time `json:"captured_at,omitempty"`
}

func mediaLinkView(m db.PropertyMediaLink) MediaLinkView {
	return MediaLinkView{ID: m.ID, MediaID: m.MediaID, Purpose: m.Purpose, MediaStatus: m.MediaStatus, Position: m.Position, Caption: m.Caption,
		GeotagLat: m.GeotagLat, GeotagLon: m.GeotagLon, CapturedAt: m.CapturedAt, CreatedAt: m.CreatedAt}
}

var purposeClasses = map[string][]string{"PHOTO": {"IMAGE"}, "FLOOR_PLAN": {"IMAGE", "DOCUMENT"}, "VIDEO": {"VIDEO"},
	"VIRTUAL_TOUR": {"VIDEO", "IMAGE"}, "INSPECTION": {"IMAGE", "VIDEO", "DOCUMENT"}, "DOCUMENT": {"DOCUMENT"}}

// CheckPropertyMedia validates a media link outside the transaction.
func (s *Service) CheckPropertyMedia(ctx context.Context, subj authz.Subject, propertyID ids.ID, in MediaLinkInput) (media.Media, error) {
	if err := schema.Validate(in); err != nil {
		return media.Media{}, err
	}
	if _, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyMedia), propertyID); err != nil {
		return media.Media{}, err
	}
	if (in.GeotagLat == nil) != (in.GeotagLon == nil) {
		return media.Media{}, apperrors.InvalidRequest("geotag_lat and geotag_lon must be given together.")
	}
	return s.ownedMedia(ctx, in.MediaID, []ids.ID{s.actingAccount(ctx, subj, propertyID), propertyID}, purposeClasses[in.Purpose]...)
}

// AddPropertyMedia links checked media to the property (idempotent per media+purpose).
func (s *Service) AddPropertyMedia(ctx context.Context, subj authz.Subject, propertyID ids.ID, in MediaLinkInput, m media.Media) (MediaLinkView, error) {
	var out MediaLinkView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyMedia), propertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, propertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		link, err := s.q.InsertPropertyMedia(ctx, tx, db.InsertPropertyMediaParams{ID: ids.New(), PropertyID: propertyID, MediaID: in.MediaID, Purpose: in.Purpose,
			MediaStatus: media.Projection(m.Status), Position: in.Position, Caption: in.Caption, GeotagLat: in.GeotagLat, GeotagLon: in.GeotagLon,
			CapturedAt: in.CapturedAt, CreatedAt: now})
		if isNoRows(err) {
			return conflict("MEDIA_ALREADY_LINKED", "This media is already linked to the property for that purpose.")
		}
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "MEDIA_LINKED", in.Purpose+" media linked", "media", in.MediaID); err != nil {
			return err
		}
		out = mediaLinkView(link)
		return s.record(ctx, "property.media.link", "property_media_link", link.ID, map[string]any{"media_id": in.MediaID, "purpose": in.Purpose}, nil)
	})
	return out, err
}

// ListPropertyMedia lists media links.
func (s *Service) ListPropertyMedia(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]MediaLinkView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPropertyMedia(ctx, s.dbq(ctx), propertyID)
	out := make([]MediaLinkView, 0, len(rows))
	for _, m := range rows {
		out = append(out, mediaLinkView(m))
	}
	return out, err
}

// unlinkMediaChecks are registered by contexts that reference property media
// (e.g. a published listing using the photo).
var unlinkMediaChecks []func(s *Service, ctx context.Context, propertyID, mediaID ids.ID) error

// RemovePropertyMedia unlinks media (the file itself is owned by media-service).
func (s *Service) RemovePropertyMedia(ctx context.Context, subj authz.Subject, propertyID, linkID ids.ID) (Removed, error) {
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyMedia), propertyID); err != nil {
			return err
		}
		tx := mustTx(ctx)
		link, err := s.q.GetPropertyMedia(ctx, tx, db.GetPropertyMediaParams{ID: linkID, PropertyID: propertyID})
		if isNoRows(err) {
			return notFound("media_link")
		}
		if err != nil {
			return err
		}
		for _, c := range unlinkMediaChecks {
			if err := c(s, ctx, propertyID, link.MediaID); err != nil {
				return err
			}
		}
		if _, err := s.q.DeletePropertyMedia(ctx, tx, db.DeletePropertyMediaParams{ID: linkID, PropertyID: propertyID}); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "MEDIA_UNLINKED", link.Purpose+" media unlinked", "media", link.MediaID); err != nil {
			return err
		}
		return s.record(ctx, "property.media.unlink", "property_media_link", linkID, map[string]any{"media_id": link.MediaID}, nil)
	})
	return Removed{ID: linkID, Removed: err == nil}, err
}

// ---- Bulk import (§13.5: asynchronous, validated, idempotent row matching) ----

// ImportRow is one import row; external_ref is the idempotent matching key.
type ImportRow struct {
	CreatePropertyInput
	ExternalRef string `json:"external_ref" v:"required,max=128"`
}

// ImportInput submits a bulk import.
type ImportInput struct {
	Rows []ImportRow `json:"rows" v:"required,max=500,shallow" doc:"Rows are validated during processing; failures are reported per row"`
}

// ImportError reports one failed row.
type ImportError struct {
	Row     int    `json:"row"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ImportView is an import job.
type ImportView struct {
	ID           ids.ID        `json:"id"`
	Status       string        `json:"status"`
	TotalRows    int32         `json:"total_rows"`
	CreatedCount int32         `json:"created_count"`
	MatchedCount int32         `json:"matched_count" doc:"Rows whose external_ref matched an existing property (not duplicated)"`
	ErrorCount   int32         `json:"error_count"`
	Errors       []ImportError `json:"errors"`
	CreatedAt    time.Time     `json:"created_at"`
	CompletedAt  *time.Time    `json:"completed_at"`
}

func importView(i db.PropertyImport) ImportView {
	v := ImportView{ID: i.ID, Status: i.Status, TotalRows: i.TotalRows, CreatedCount: i.CreatedCount, MatchedCount: i.MatchedCount,
		ErrorCount: i.ErrorCount, Errors: []ImportError{}, CreatedAt: i.CreatedAt, CompletedAt: i.CompletedAt}
	if len(i.Errors) > 0 {
		_ = json.Unmarshal(i.Errors, &v.Errors)
	}
	return v
}

// CreateImport stores an import for asynchronous processing.
func (s *Service) CreateImport(ctx context.Context, subj authz.Subject, in ImportInput) (ImportView, error) {
	if len(in.Rows) > s.Policy.Domain.MaxImportRows {
		return ImportView{}, apperrors.InvalidRequest("Too many rows.")
	}
	if err := schema.Validate(in); err != nil {
		return ImportView{}, err
	}
	if _, err := s.authorize(ctx, subj, authz.ActPropertyCreate, ids.Nil); err != nil {
		return ImportView{}, err
	}
	raw, err := json.Marshal(in.Rows)
	if err != nil {
		return ImportView{}, err
	}
	var out ImportView
	err = s.InTx(ctx, func(ctx context.Context) error {
		i, err := s.q.InsertPropertyImport(ctx, mustTx(ctx), db.InsertPropertyImportParams{ID: ids.New(), AccountID: accountOf(subj), Rows: raw,
			TotalRows: int32(len(in.Rows)), CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = importView(i)
		return s.record(ctx, "property.import.create", "property_import", i.ID, map[string]any{"rows": len(in.Rows)}, nil)
	})
	return out, err
}

// GetImport returns an import job of the caller.
func (s *Service) GetImport(ctx context.Context, subj authz.Subject, id ids.ID) (ImportView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return ImportView{}, err
	}
	i, err := s.q.GetPropertyImport(ctx, s.dbq(ctx), db.GetPropertyImportParams{ID: id, AccountID: accountOf(subj)})
	if isNoRows(err) {
		return ImportView{}, notFound("property_import")
	}
	return importView(i), err
}

// ProcessImports processes one leased import job (worker). Each row runs in
// its own transaction; rows whose external_ref already exists are matched,
// so a crashed-and-retried job never duplicates properties.
func (s *Service) ProcessImports(ctx context.Context, owner string, lease time.Duration, maxAttempts int32) (bool, error) {
	now := s.now()
	until := now.Add(lease)
	job, err := s.q.ClaimPropertyImport(ctx, s.Pool, db.ClaimPropertyImportParams{Owner: &owner, LeaseUntil: &until, Now: now, MaxAttempts: maxAttempts})
	if isNoRows(err) {
		_, err = s.q.FailExhaustedPropertyImports(ctx, s.Pool, db.FailExhaustedPropertyImportsParams{Attempts: maxAttempts, CompletedAt: &now})
		return false, err
	}
	if err != nil {
		return false, err
	}
	var rows []ImportRow
	if err := json.Unmarshal(job.Rows, &rows); err != nil {
		return true, err
	}
	ctx = withAccountActor(ctx, job.AccountID)
	var created, matched int32
	errs := []ImportError{}
	seen := map[string]bool{}
	for i, r := range rows {
		r.CreatePropertyInput.ExternalRef = &r.ExternalRef
		if seen[r.ExternalRef] {
			errs = append(errs, ImportError{Row: i + 1, Code: "DUPLICATE_EXTERNAL_REF", Message: "external_ref repeated in this import"})
			continue
		}
		seen[r.ExternalRef] = true
		if err := schema.Validate(r); err != nil {
			errs = append(errs, ImportError{Row: i + 1, Code: apperrors.CodeOf(err), Message: err.Error()})
			continue
		}
		existing, err := s.q.GetPropertyByExternalRef(ctx, s.Pool, db.GetPropertyByExternalRefParams{OwnerAccountID: job.AccountID, ExternalRef: &r.ExternalRef})
		if err == nil && existing.ID != ids.Nil {
			matched++
			continue
		}
		jobID := job.ID
		err = s.InTx(ctx, func(ctx context.Context) error {
			acc, err := s.q.GetAccount(ctx, mustTx(ctx), job.AccountID)
			if err != nil {
				return err
			}
			if acc.Status != "ACTIVE" {
				return precondition("ACCOUNT_NOT_ACTIVE", "The account is not active.")
			}
			_, err = s.createPropertyTx(ctx, job.AccountID, r.CreatePropertyInput, &jobID)
			return err
		})
		switch {
		case err == nil:
			created++
		case apperrors.CodeOf(err) == "PROPERTY_EXTERNAL_REF_EXISTS":
			matched++
		default:
			ae := apperrors.From(err)
			if ae.Status >= 500 {
				return true, err // transient: lease expires, the job is retried and matching skips done rows
			}
			errs = append(errs, ImportError{Row: i + 1, Code: ae.Code, Message: ae.Message})
		}
	}
	status := "COMPLETED"
	if len(errs) > 0 {
		status = "COMPLETED_WITH_ERRORS"
		if created+matched == 0 {
			status = "FAILED"
		}
	}
	raw, _ := json.Marshal(errs)
	done := s.now()
	_, err = s.q.CompletePropertyImport(ctx, s.Pool, db.CompletePropertyImportParams{ID: job.ID, LeaseOwner: &owner, Status: status, CreatedCount: created,
		MatchedCount: matched, ErrorCount: int32(len(errs)), Errors: raw, CompletedAt: &done})
	return true, err
}
