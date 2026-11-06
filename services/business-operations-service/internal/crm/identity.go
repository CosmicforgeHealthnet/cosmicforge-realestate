// Package crm owns business guest/corporate records and purpose-bound projections.
// Marketplace remains authority for its customer accounts. No contact match is
// accepted as proof that two source identities describe the same person.
package crm

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/jackc/pgx/v5"
)

var contactPhone = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
var languageTag = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)

type ProtectedStore interface {
	KeyID() string
	Seal([]byte, []byte) []byte
	Open(string, []byte, []byte) ([]byte, error)
}
type Profile struct {
	DisplayName string `json:"display_name"`
	Language    string `json:"language"`
}
type PrivateProfile struct {
	Email             string   `json:"email,omitempty"`
	Phone             string   `json:"phone,omitempty"`
	ContactPreference string   `json:"contact_preference,omitempty"`
	RoomPreferences   []string `json:"room_preferences,omitempty"`
	Accessibility     []string `json:"accessibility,omitempty"`
}

func (p Profile) Validate() error {
	if !Text(p.DisplayName, 1, 200) || !Text(p.Language, 2, 35) || !languageTag.MatchString(p.Language) {
		return apperrors.InvalidRequest("Guest display name and language required.")
	}
	return nil
}
func (p PrivateProfile) Validate() error {
	if p.Email != "" {
		v, e := mail.ParseAddress(p.Email)
		if e != nil || v.Address != p.Email || len(p.Email) > 254 {
			return apperrors.InvalidRequest("Invalid contact email.")
		}
	}
	if p.Phone != "" && !contactPhone.MatchString(p.Phone) {
		return apperrors.InvalidRequest("Invalid contact phone reference.")
	}
	if !slices.Contains([]string{"", "NONE", "EMAIL", "SMS", "PUSH"}, p.ContactPreference) || len(p.RoomPreferences) > 20 || len(p.Accessibility) > 20 {
		return apperrors.InvalidRequest("Invalid protected guest preferences.")
	}
	for _, items := range [][]string{p.RoomPreferences, p.Accessibility} {
		for _, s := range items {
			if !Text(s, 1, 300) {
				return apperrors.InvalidRequest("Invalid preference value.")
			}
		}
	}
	return nil
}
func Text(s string, min, max int) bool {
	return len(s) >= min && len(s) <= max && len(strings.TrimSpace(s)) >= min && !strings.ContainsAny(s, "\x00\r\n")
}
func OpaqueReference(s string) bool { return Text(s, 1, 300) && !strings.ContainsAny(s, "@\t ") }
func LocalSource(s string) bool {
	return slices.Contains([]string{"DIRECT", "WALK_IN", "LEGACY", "CORPORATE"}, s)
}
func ConsentPurpose(s string) bool {
	return slices.Contains([]string{"OPERATIONAL_CONTACT", "MARKETING", "PREFERENCES", "ACCESSIBILITY", "LOYALTY"}, s)
}

type Guest struct {
	ID                    ids.ID    `json:"id"`
	OrganisationID        ids.ID    `json:"organisation_id"`
	PropertyID            ids.ID    `json:"property_id"`
	State                 string    `json:"state"`
	RegistrationReference string    `json:"registration_reference"`
	Version               int64     `json:"version"`
	MergedInto            *ids.ID   `json:"merged_into,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}
type GuestView struct {
	Guest   Guest   `json:"guest"`
	Profile Profile `json:"profile"`
}

func Load[T any](ctx context.Context, q database.Querier, sql string, args ...any) (T, error) {
	var v T
	var raw []byte
	e := q.QueryRow(ctx, sql, args...).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, apperrors.NotFound("CRM_RECORD_NOT_FOUND", "CRM record not found.")
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(raw, &v)
	return v, e
}
func GetGuest(ctx context.Context, q database.Querier, org, id ids.ID) (Guest, error) {
	raw, e := db.New().GetCRMGuest(ctx, q, db.GetCRMGuestParams{OrganisationID: org, ID: id})
	return decodeRecord[Guest](raw, e)
}

func decodeRecord[T any](raw []byte, e error) (T, error) {
	var v T
	if errors.Is(e, pgx.ErrNoRows) {
		return v, apperrors.NotFound("CRM_RECORD_NOT_FOUND", "CRM record not found.")
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(raw, &v)
	return v, e
}
func ProfileAAD(org, guest ids.ID) []byte {
	return []byte("business:crm:profile:" + org.String() + ":" + guest.String())
}
func PreferencesAAD(org, guest, property ids.ID) []byte {
	return []byte("business:crm:preferences:" + org.String() + ":" + guest.String() + ":" + property.String())
}
func ReadProfile(ctx context.Context, q database.Querier, store ProtectedStore, org, guest ids.ID) (Profile, error) {
	var out Profile
	var ciphertext []byte
	var key string
	if store == nil {
		return out, apperrors.Unavailable("Protected guest storage is unavailable.")
	}
	if e := q.QueryRow(ctx, `SELECT encrypted_profile,profile_key_id FROM guest_profiles WHERE organisation_id=$1 AND id=$2`, org, guest).Scan(&ciphertext, &key); e != nil {
		return out, e
	}
	plain, e := store.Open(key, ciphertext, ProfileAAD(org, guest))
	if e != nil {
		return out, apperrors.Unavailable("Protected guest profile cannot be read.")
	}
	e = json.Unmarshal(plain, &out)
	return out, e
}
func ConsentAllowed(ctx context.Context, q database.Querier, org, guest, property ids.ID, purpose string, now time.Time) (bool, error) {
	var allowed bool
	e := q.QueryRow(ctx, `SELECT coalesce((SELECT state='GRANTED' AND captured_at<=$5 AND (expires_at IS NULL OR expires_at>$5) FROM consents WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 AND purpose=$4 ORDER BY version DESC LIMIT 1),false)`, org, guest, property, purpose, now).Scan(&allowed)
	return allowed, e
}

// RequireGuest is the owning contract used by reservations/PMS. A UUID alone
// never proves ownership, registration or current property eligibility.
func RequireGuest(ctx context.Context, q database.Querier, org, property, guest ids.ID) error {
	var valid bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_profiles g JOIN guest_property_scopes s ON s.organisation_id=g.organisation_id AND s.guest_id=g.id WHERE g.organisation_id=$1 AND g.id=$2 AND g.state='ACTIVE' AND s.property_id=$3 AND s.state='ACTIVE' AND length(g.registration_reference)>=5)`, org, guest, property).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return apperrors.PermissionDenied("Current registered guest ownership and property scope required.")
	}
	return nil
}

type Corporate struct {
	ID                    ids.ID `json:"id"`
	PropertyID            ids.ID `json:"property_id"`
	LegalName             string `json:"legal_name"`
	RegistrationReference string `json:"registration_reference"`
	State                 string `json:"state"`
	Version               int64  `json:"version"`
}
type CorporateContact struct {
	Name                    string `json:"name"`
	Email                   string `json:"email,omitempty"`
	Phone                   string `json:"phone,omitempty"`
	RepresentativeReference string `json:"representative_reference"`
}
type CorporateTerms struct {
	RatePlanIDs           []ids.ID `json:"rate_plan_ids"`
	AllowedSources        []string `json:"allowed_sources"`
	BillingResponsibility string   `json:"billing_responsibility"`
	BillingReference      string   `json:"billing_reference"`
	MaxBookingMinor       int64    `json:"max_booking_minor"`
	Currency              string   `json:"currency"`
	RequireNamedTravelers bool     `json:"require_named_travelers"`
	MaxNights             int      `json:"max_nights"`
}

func (d CorporateTerms) Validate() error {
	if len(d.RatePlanIDs) < 1 || len(d.RatePlanIDs) > 20 || len(d.AllowedSources) < 1 || len(d.AllowedSources) > 8 || !slices.Contains([]string{"CORPORATE_CREDIT", "PLATFORM_COLLECT", "PAY_AT_PROPERTY"}, d.BillingResponsibility) || !Text(d.BillingReference, 5, 300) || d.MaxBookingMinor < 1 || d.MaxBookingMinor > 1000000000000000 || d.MaxNights < 1 || d.MaxNights > 366 {
		return apperrors.InvalidRequest("Valid bounded corporate terms required.")
	}
	if _, e := money.ParseCurrency(d.Currency); e != nil {
		return apperrors.InvalidRequest("Valid corporate currency required.")
	}
	seen := map[ids.ID]bool{}
	for _, id := range d.RatePlanIDs {
		if id.ValidateV7() != nil || seen[id] {
			return apperrors.InvalidRequest("Unique canonical negotiated plan references required.")
		}
		seen[id] = true
	}
	for _, source := range d.AllowedSources {
		if !slices.Contains([]string{"DIRECT", "CORPORATE", "GROUP", "WALK_IN", "MARKETPLACE", "OTA", "LEGACY"}, source) {
			return apperrors.InvalidRequest("Invalid corporate booking source.")
		}
	}
	return nil
}

type CorporateContract struct {
	ID          ids.ID         `json:"id"`
	PropertyID  ids.ID         `json:"property_id"`
	CorporateID ids.ID         `json:"corporate_id"`
	ContractKey ids.ID         `json:"contract_key"`
	Version     int64          `json:"version"`
	State       string         `json:"state"`
	Definition  CorporateTerms `json:"definition"`
	EffectiveAt time.Time      `json:"effective_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
}

func GetCorporate(ctx context.Context, q database.Querier, org, id ids.ID) (Corporate, error) {
	raw, e := db.New().GetCRMCorporate(ctx, q, db.GetCRMCorporateParams{OrganisationID: org, ID: id})
	return decodeRecord[Corporate](raw, e)
}
func GetContract(ctx context.Context, q database.Querier, org, id ids.ID) (CorporateContract, error) {
	raw, e := db.New().GetCRMCorporateContract(ctx, q, db.GetCRMCorporateContractParams{OrganisationID: org, ID: id})
	return decodeRecord[CorporateContract](raw, e)
}

func ListStayHistoryRecords(ctx context.Context, q database.Querier, org, property, guest ids.ID, limit, offset int32) ([]HistoryRecord, error) {
	rows, e := db.New().ListCRMStayProjections(ctx, q, db.ListCRMStayProjectionsParams{OrganisationID: org, PropertyID: property, GuestID: guest, PageLimit: limit, PageOffset: offset})
	if e != nil {
		return nil, e
	}
	out := make([]HistoryRecord, 0, len(rows))
	for _, raw := range rows {
		v, e := decodeRecord[HistoryRecord](raw, nil)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
