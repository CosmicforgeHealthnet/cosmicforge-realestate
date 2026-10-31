// Package portfolio owns the canonical enterprise property identity graph.
// Availability, booking, leases, sales and media bytes remain outside this graph.
package portfolio

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

type Kind string

const (
	Portfolio   Kind = "PORTFOLIO"
	Development Kind = "DEVELOPMENT"
	Phase       Kind = "DEVELOPMENT_PHASE"
	Parcel      Kind = "PARCEL"
	Property    Kind = "PROPERTY"
	Building    Kind = "BUILDING"
	Block       Kind = "BLOCK"
	Floor       Kind = "FLOOR"
	Unit        Kind = "UNIT"
	RoomType    Kind = "ROOM_TYPE"
	Room        Kind = "ROOM"
	Facility    Kind = "FACILITY"
	CommonArea  Kind = "COMMON_AREA"
	Asset       Kind = "ASSET"
)

var Collections = map[string]Kind{"portfolios": Portfolio, "developments": Development, "development-phases": Phase, "parcels": Parcel, "properties": Property, "buildings": Building, "blocks": Block, "floors": Floor, "units": Unit, "room-types": RoomType, "rooms": Room, "facilities": Facility, "common-areas": CommonArea, "assets": Asset}

func (k Kind) Valid() bool {
	for _, v := range Collections {
		if k == v {
			return true
		}
	}
	return false
}
func (k Kind) RootAllowed() bool {
	return k == Portfolio || k == Development || k == Parcel || k == Property
}
func LegalEdge(parent, child Kind, relation string) bool {
	if relation == "MEMBERSHIP" {
		return parent == Portfolio && (child == Development || child == Parcel || child == Property)
	}
	if relation == "ASSOCIATES" {
		return parent == Parcel && child == Property
	}
	if relation != "CONTAINS" {
		return false
	}
	if child == Asset {
		return parent.Valid()
	}
	switch parent {
	case Development:
		return child == Phase || child == Parcel || child == Property
	case Phase:
		return child == Parcel || child == Property
	case Parcel:
		return child == Property
	case Property:
		return child == Building || child == Block || child == Floor || child == Unit || child == RoomType || child == Room || child == Facility || child == CommonArea
	case Building:
		return child == Block || child == Floor || child == Unit || child == Room || child == Facility || child == CommonArea
	case Block:
		return child == Floor || child == Unit || child == Room || child == Facility || child == CommonArea
	case Floor:
		return child == Unit || child == Room || child == Facility || child == CommonArea
	case Unit:
		return child == Room
	}
	return false
}

// Attributes is a bounded structural projection, never a business transaction,
// external URL, uploaded document, arbitrary provider payload or media blob.
type Attributes struct {
	Classification     string     `json:"classification,omitempty"`
	Address            string     `json:"address,omitempty"`
	City               string     `json:"city,omitempty"`
	Jurisdiction       string     `json:"jurisdiction,omitempty"`
	Latitude           *float64   `json:"latitude,omitempty"`
	Longitude          *float64   `json:"longitude,omitempty"`
	Geometry           *Geometry  `json:"geometry,omitempty"`
	AreaSquareMetres   string     `json:"area_square_metres,omitempty"`
	LengthMM           *int64     `json:"length_mm,omitempty"`
	WidthMM            *int64     `json:"width_mm,omitempty"`
	HeightMM           *int64     `json:"height_mm,omitempty"`
	FloorNumber        *int       `json:"floor_number,omitempty"`
	Ordering           int        `json:"ordering,omitempty"`
	Capacity           *int       `json:"capacity,omitempty"`
	Beds               *int       `json:"beds,omitempty"`
	Layout             string     `json:"layout,omitempty"`
	Manufacturer       string     `json:"manufacturer,omitempty"`
	Model              string     `json:"model,omitempty"`
	Serial             string     `json:"serial,omitempty"`
	InstalledAt        *time.Time `json:"installed_at,omitempty"`
	WarrantyUntil      *time.Time `json:"warranty_until,omitempty"`
	SurveyReference    string     `json:"survey_reference,omitempty"`
	CadastralReference string     `json:"cadastral_reference,omitempty"`
	WarrantyReference  string     `json:"warranty_reference,omitempty"`
}
type Node struct {
	NonSellable    bool                   `json:"non_sellable"`
	Parents        []ParentReference      `json:"parents"`
	PropertyID     *ids.ID                `json:"property_id,omitempty"`
	TreeVersion    int64                  `json:"tree_version"`
	RoomTypeID     *ids.ID                `json:"room_type_id,omitempty"`
	Verification   VerificationProjection `json:"verification"`
	ID             ids.ID                 `json:"id"`
	OrganisationID ids.ID                 `json:"organisation_id"`
	BranchID       ids.ID                 `json:"branch_id"`
	DepartmentID   *ids.ID                `json:"department_id,omitempty"`
	TeamID         *ids.ID                `json:"team_id,omitempty"`
	Kind           Kind                   `json:"kind"`
	Code           string                 `json:"code"`
	Name           string                 `json:"name"`
	State          string                 `json:"state"`
	Timezone       string                 `json:"timezone"`
	Currency       string                 `json:"currency"`
	PolicyCode     string                 `json:"policy_code"`
	PolicyVersion  int64                  `json:"policy_version"`
	CapacityCode   string                 `json:"capacity_code,omitempty"`
	Attributes     Attributes             `json:"attributes"`
	Version        int64                  `json:"version"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	ActivatedAt    *time.Time             `json:"activated_at,omitempty"`
	CreatedBy      ids.ID                 `json:"created_by"`
	UpdatedBy      ids.ID                 `json:"updated_by"`
}

type ParentReference struct {
	EdgeID   ids.ID `json:"edge_id"`
	ParentID ids.ID `json:"parent_id"`
	Relation string `json:"relation"`
}

var codePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)
var areaPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$`)
var ErrInvalid = errors.New("invalid canonical inventory structure")

func (n Node) Validate() error {
	if !n.Kind.Valid() || n.ID.ValidateV7() != nil || n.OrganisationID.ValidateV7() != nil || n.BranchID.ValidateV7() != nil || !codePattern.MatchString(n.Code) || len(strings.TrimSpace(n.Name)) < 1 || len(n.Name) > 200 || len(n.PolicyCode) < 1 || len(n.PolicyCode) > 80 || n.PolicyVersion < 1 {
		return ErrInvalid
	}
	if _, err := time.LoadLocation(n.Timezone); err != nil {
		return ErrInvalid
	}
	if _, err := money.ParseCurrency(n.Currency); err != nil {
		return ErrInvalid
	}
	a := n.Attributes
	for _, s := range []string{a.Classification, a.Address, a.City, a.Jurisdiction, a.Layout, a.Manufacturer, a.Model, a.Serial, a.SurveyReference, a.CadastralReference, a.WarrantyReference} {
		if len(s) > 500 || strings.ContainsRune(s, 0) {
			return ErrInvalid
		}
	}
	if a.Jurisdiction != "" && (len(a.Jurisdiction) != 2 || a.Jurisdiction != strings.ToUpper(a.Jurisdiction)) {
		return ErrInvalid
	}
	if a.AreaSquareMetres != "" && !areaPattern.MatchString(a.AreaSquareMetres) {
		return ErrInvalid
	}
	for _, v := range []*int64{a.LengthMM, a.WidthMM, a.HeightMM} {
		if v != nil && (*v < 0 || *v > 1000000000) {
			return ErrInvalid
		}
	}
	for _, v := range []*int{a.Capacity, a.Beds} {
		if v != nil && (*v < 0 || *v > 100000) {
			return ErrInvalid
		}
	}
	if a.Ordering < 0 || a.Ordering > 1000000 || a.FloorNumber != nil && (*a.FloorNumber < -100 || *a.FloorNumber > 10000) {
		return ErrInvalid
	}
	if (a.Latitude == nil) != (a.Longitude == nil) {
		return ErrInvalid
	}
	if a.Latitude != nil && (!finite(*a.Latitude) || !finite(*a.Longitude) || *a.Latitude < -90 || *a.Latitude > 90 || *a.Longitude < -180 || *a.Longitude > 180) {
		return ErrInvalid
	}
	if a.Geometry != nil && (n.Kind != Parcel || a.Geometry.Validate() != nil) {
		return ErrInvalid
	}
	if a.FloorNumber != nil && n.Kind != Floor {
		return ErrInvalid
	}
	if (a.Manufacturer != "" || a.Serial != "" || a.WarrantyUntil != nil || a.InstalledAt != nil) && n.Kind != Asset {
		return ErrInvalid
	}
	if a.Beds != nil && n.Kind != RoomType && n.Kind != Room && n.Kind != Unit {
		return ErrInvalid
	}
	if a.InstalledAt != nil && a.WarrantyUntil != nil && a.WarrantyUntil.Before(*a.InstalledAt) {
		return ErrInvalid
	}
	return nil
}
func Transition(from, to string) bool {
	switch from {
	case "DRAFT":
		return to == "ACTIVE" || to == "ARCHIVED"
	case "ACTIVE":
		return to == "INACTIVE" || to == "RETIRED"
	case "INACTIVE":
		return to == "ACTIVE" || to == "RETIRED" || to == "ARCHIVED"
	}
	return false
}

// Safety evidence is owned by actual downstream operations. No configured
// provider means an activated subtree cannot be moved/retired/reclassified.
type SafetyRequest struct {
	OrganisationID  ids.ID
	NodeIDs         []ids.ID
	Versions        []int64
	TreeVersions    []int64
	OperationDigest string
}
type SafetyAssertion struct {
	OrganisationID  ids.ID
	OperationDigest string
	Safe            bool
	Complete        bool
	ExpiresAt       time.Time
	Reference       string
}
type DependencyGuard interface {
	Check(context.Context, SafetyRequest) (SafetyAssertion, error)
}

// VerificationProjection is a status reference, never a local verification decision.
type VerificationProjection struct {
	CaseID      *ids.ID    `json:"case_id,omitempty"`
	Status      string     `json:"status"`
	NodeVersion *int64     `json:"node_version,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
}
