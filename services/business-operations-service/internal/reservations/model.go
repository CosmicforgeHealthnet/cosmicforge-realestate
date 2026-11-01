// Package reservations owns canonical bookings, source lineage and stock commitments.
// Payment and verification outcomes remain authoritative in their owning services.
package reservations

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"slices"
	"strings"
	"time"
)

type Policy struct {
	AllowedPaymentPolicyIDs []ids.ID `json:"allowed_payment_policy_ids,omitempty"`
	AcceptanceTimezone      string   `json:"acceptance_timezone,omitempty"`
	AllowSourceCancellation bool     `json:"allow_source_cancellation"`
	MaxGuaranteeMinor       int64    `json:"max_guarantee_minor"`
	AllowedSources          []string `json:"allowed_sources"`
	AllowedGuarantees       []string `json:"allowed_guarantees"`
	HoldSeconds             int      `json:"hold_seconds"`
	RequireApproval         bool     `json:"require_approval"`
	RequireRiskClear        bool     `json:"require_risk_clear"`
	CancellationHours       int      `json:"cancellation_hours"`
	CancellationPenaltyBPS  int64    `json:"cancellation_penalty_bps"`
	NoShowGraceHours        int      `json:"no_show_grace_hours"`
	NoShowRetainNights      int      `json:"no_show_retain_nights"`
	CheckInLocal            string   `json:"check_in_local"`
	CheckOutLocal           string   `json:"check_out_local"`
	PolicyReference         string   `json:"policy_reference"`
}

func (p Policy) Validate() error {
	if len(p.AllowedPaymentPolicyIDs) > 50 || p.MaxGuaranteeMinor < 0 || len(p.AllowedSources) == 0 || len(p.AllowedSources) > 8 || len(p.AllowedGuarantees) > 3 || p.HoldSeconds < 60 || p.HoldSeconds > 900 || p.CancellationHours < 0 || p.CancellationHours > 8760 || p.CancellationPenaltyBPS < 0 || p.CancellationPenaltyBPS > 10000 || p.NoShowGraceHours < 0 || p.NoShowGraceHours > 72 || p.NoShowRetainNights < 0 || p.NoShowRetainNights > 30 || len(p.PolicyReference) < 1 || len(p.PolicyReference) > 200 || strings.ContainsRune(p.PolicyReference, 0) {
		return apperrors.InvalidRequest("Invalid reservation policy bounds.")
	}
	seenPolicies := map[ids.ID]bool{}
	for _, id := range p.AllowedPaymentPolicyIDs {
		if id.ValidateV7() != nil || seenPolicies[id] {
			return apperrors.InvalidRequest("Unique real Payment Service policy references must be UUIDv7.")
		}
		seenPolicies[id] = true
	}
	for _, source := range p.AllowedSources {
		if !ValidSource(source) {
			return apperrors.InvalidRequest("Unsupported reservation source.")
		}
	}
	for _, guarantee := range p.AllowedGuarantees {
		if !slices.Contains([]string{"PAY_AT_PROPERTY", "CORPORATE_CREDIT", "CHANNEL_COLLECT"}, guarantee) {
			return apperrors.InvalidRequest("Unsupported guarantee policy.")
		}
	}
	for _, clock := range []string{p.CheckInLocal, p.CheckOutLocal} {
		v, e := time.Parse("15:04", clock)
		if e != nil || v.Format("15:04") != clock {
			return apperrors.InvalidRequest("Property-local check-in/out times must be HH:MM.")
		}
	}
	return nil
}
func ValidSource(s string) bool {
	return slices.Contains([]string{"DIRECT", "MARKETPLACE", "OTA", "WALK_IN", "CORPORATE", "GROUP", "LEGACY", "INTERNAL"}, s)
}
func Transition(from, to string) bool {
	allowed := map[string][]string{
		"INQUIRY":          {"HOLD", "PENDING_PAYMENT", "PENDING_APPROVAL", "REJECTED", "CANCELLED", "EXPIRED"},
		"HOLD":             {"PENDING_PAYMENT", "PENDING_APPROVAL", "CONFIRMED", "CANCELLED", "REJECTED", "EXPIRED"},
		"PENDING_PAYMENT":  {"PENDING_APPROVAL", "CONFIRMED", "CANCELLED", "REJECTED", "EXPIRED", "DISPUTED"},
		"PENDING_APPROVAL": {"PENDING_PAYMENT", "CONFIRMED", "CANCELLED", "REJECTED", "EXPIRED", "DISPUTED"},
		"CONFIRMED":        {"CHECKED_IN", "CANCELLED", "NO_SHOW", "DISPUTED"},
		"CHECKED_IN":       {"CHECKED_OUT", "DISPUTED"}, "CHECKED_OUT": {"COMPLETED", "DISPUTED"},
		"COMPLETED": {"DISPUTED"}, "DISPUTED": {"PENDING_PAYMENT", "CONFIRMED", "CHECKED_IN", "CHECKED_OUT", "COMPLETED", "CANCELLED"},
	}
	return slices.Contains(allowed[from], to)
}

type Reservation struct {
	ID                    ids.ID     `json:"id"`
	OrganisationID        ids.ID     `json:"organisation_id"`
	PropertyID            ids.ID     `json:"property_id"`
	Source                string     `json:"source"`
	Channel               string     `json:"channel"`
	SourceReference       string     `json:"source_reference"`
	State                 string     `json:"state"`
	PriorState            *string    `json:"prior_state,omitempty"`
	AllocationState       string     `json:"allocation_state"`
	Version               int64      `json:"version"`
	GroupID               *ids.ID    `json:"group_id,omitempty"`
	ParentReservationID   *ids.ID    `json:"parent_reservation_id,omitempty"`
	PolicyID              ids.ID     `json:"policy_id"`
	PolicySnapshot        Policy     `json:"policy_snapshot"`
	PaymentResponsibility string     `json:"payment_responsibility"`
	FinancialState        string     `json:"financial_state"`
	AmountMinor           int64      `json:"amount_minor"`
	Currency              string     `json:"currency"`
	DateFrom              string     `json:"date_from"`
	DateTo                string     `json:"date_to"`
	HoldExpiresAt         *time.Time `json:"hold_expires_at,omitempty"`
	ConfirmedAt           *time.Time `json:"confirmed_at,omitempty"`
	CancelledAt           *time.Time `json:"cancelled_at,omitempty"`
	CompletedAt           *time.Time `json:"completed_at,omitempty"`
	RiskStatus            string     `json:"risk_status"`
	CorporateID           *ids.ID    `json:"corporate_id,omitempty"`
	CorporateContractID   *ids.ID    `json:"corporate_contract_id,omitempty"`
	Reason                string     `json:"reason"`
	CreatedBy             *ids.ID    `json:"created_by"`
	CreatedByService      string     `json:"created_by_service,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}
type Room struct {
	ID                 ids.ID                `json:"id"`
	OrganisationID     ids.ID                `json:"organisation_id"`
	PropertyID         ids.ID                `json:"property_id"`
	ReservationID      ids.ID                `json:"reservation_id"`
	FinancialGroupID   ids.ID                `json:"financial_group_id"`
	ProductID          ids.ID                `json:"product_id"`
	PoolID             ids.ID                `json:"pool_id"`
	QuoteID            ids.ID                `json:"quote_id"`
	HoldID             *ids.ID               `json:"hold_id,omitempty"`
	DateFrom           string                `json:"date_from"`
	DateTo             string                `json:"date_to"`
	Quantity           int                   `json:"quantity"`
	Adults             int                   `json:"adults"`
	Children           int                   `json:"children"`
	AmountMinor        int64                 `json:"amount_minor"`
	Currency           string                `json:"currency"`
	AccommodationMinor *int64                `json:"accommodation_minor,omitempty"`
	Snapshot           revenue.QuoteSnapshot `json:"snapshot"`
	AcceptedAt         time.Time             `json:"accepted_at"`
	State              string                `json:"state"`
	Version            int64                 `json:"version"`
	PreviousRoomID     *ids.ID               `json:"previous_room_id,omitempty"`
	OverbookedQuantity int                   `json:"overbooked_quantity"`
}
type RoomCommand struct {
	ProductID      ids.ID  `json:"product_id"`
	QuoteID        ids.ID  `json:"quote_id"`
	HoldID         *ids.ID `json:"hold_id,omitempty"`
	HoldVersion    int64   `json:"hold_version,omitempty"`
	ProductVersion int64   `json:"product_version"`
	PoolVersion    int64   `json:"pool_version"`
	DateFrom       string  `json:"date_from"`
	DateTo         string  `json:"date_to"`
	Quantity       int     `json:"quantity"`
}
type GuestCommand struct {
	RoomID                *ids.ID  `json:"room_id,omitempty"`
	GuestID               *ids.ID  `json:"guest_id,omitempty"`
	SourceKind            string   `json:"source_kind"`
	SourceReference       string   `json:"source_reference"`
	IsPrimary             bool     `json:"is_primary"`
	RegistrationReference string   `json:"registration_reference,omitempty"`
	DocumentReferences    []ids.ID `json:"document_references,omitempty"`
}
type Create struct {
	PropertyID            ids.ID         `json:"property_id"`
	Source                string         `json:"source"`
	Channel               string         `json:"channel"`
	SourceReference       string         `json:"source_reference"`
	State                 string         `json:"state"`
	Rooms                 []RoomCommand  `json:"rooms"`
	Guests                []GuestCommand `json:"guests"`
	PaymentResponsibility string         `json:"payment_responsibility"`
	GroupID               *ids.ID        `json:"group_id,omitempty"`
	GroupName             string         `json:"group_name,omitempty"`
	CorporateID           *ids.ID        `json:"corporate_id,omitempty"`
	CorporateContractID   *ids.ID        `json:"corporate_contract_id,omitempty"`
	ApprovalID            ids.ID         `json:"approval_id,omitempty"`
	GuaranteeApprovalID   ids.ID         `json:"guarantee_approval_id,omitempty"`
	Reason                string         `json:"reason"`
}

func (c Create) Validate() error {
	if c.PropertyID.ValidateV7() != nil || !ValidSource(c.Source) || !inventory.ValidChannel(c.Channel) || len(c.SourceReference) > 300 || len(c.GroupName) > 200 || len(c.Rooms) < 1 || len(c.Rooms) > 50 || len(c.Guests) > 200 || len(c.Reason) < 5 || len(c.Reason) > 1000 || !slices.Contains([]string{"INQUIRY", "HOLD", "PENDING_PAYMENT", "PENDING_APPROVAL", "CONFIRMED"}, c.State) || !slices.Contains([]string{"PLATFORM_COLLECT", "PAY_AT_PROPERTY", "CORPORATE_CREDIT", "CHANNEL_COLLECT"}, c.PaymentResponsibility) {
		return apperrors.InvalidRequest("Valid canonical reservation source, policy, rooms, state and reason required.")
	}
	if strings.ContainsRune(c.SourceReference+c.GroupName+c.Reason, 0) || c.GroupName != "" && len(strings.TrimSpace(c.GroupName)) == 0 || (c.CorporateID == nil) != (c.CorporateContractID == nil) {
		return apperrors.InvalidRequest("Safe opaque source/group references and a complete corporate binding pair required.")
	}
	for _, id := range []*ids.ID{c.GroupID, c.CorporateID, c.CorporateContractID} {
		if id != nil && id.ValidateV7() != nil {
			return apperrors.InvalidRequest("Canonical group/corporate references must be UUIDv7.")
		}
	}
	for _, r := range c.Rooms {
		if r.ProductID.ValidateV7() != nil || r.QuoteID.ValidateV7() != nil || r.ProductVersion < 1 || r.PoolVersion < 1 || r.Quantity < 1 || r.Quantity > 1000 {
			return apperrors.InvalidRequest("Current canonical product/pool/quote and bounded quantity required.")
		}
		if _, e := (inventory.DateRange{DateFrom: r.DateFrom, DateTo: r.DateTo}).Days("UTC"); e != nil {
			return apperrors.InvalidRequest("Valid checkout-exclusive reservation dates required.")
		}
		if r.HoldID != nil && (c.State == "INQUIRY" || r.HoldID.ValidateV7() != nil || r.HoldVersion < 1) {
			return apperrors.InvalidRequest("Existing hold requires its current version.")
		}
	}
	for _, g := range c.Guests {
		if !slices.Contains([]string{"DIRECT", "MARKETPLACE", "OTA", "CORPORATE", "WALK_IN", "LEGACY"}, g.SourceKind) || len(g.SourceReference) < 1 || len(g.SourceReference) > 300 || len(g.RegistrationReference) > 300 || len(g.DocumentReferences) > 10 || strings.ContainsRune(g.SourceReference+g.RegistrationReference, 0) || g.RoomID != nil && g.RoomID.ValidateV7() != nil {
			return apperrors.InvalidRequest("Bounded typed guest references required.")
		}
		if g.GuestID != nil && g.GuestID.ValidateV7() != nil {
			return apperrors.InvalidRequest("Canonical guest identity must be UUIDv7.")
		}
		for _, id := range g.DocumentReferences {
			if id.ValidateV7() != nil {
				return apperrors.InvalidRequest("Guest documents are Media Service UUIDv7 references only.")
			}
		}
	}
	return nil
}

type OverbookingDay struct {
	RoomID              ids.ID `json:"room_id"`
	PoolID              ids.ID `json:"pool_id"`
	Day                 string `json:"day"`
	OutstandingQuantity int    `json:"outstanding_quantity"`
	PermitID            ids.ID `json:"permit_id"`
}
type Detail struct {
	OutstandingOverbooking []OverbookingDay     `json:"outstanding_overbooking"`
	Reservation            Reservation          `json:"reservation"`
	Rooms                  []Room               `json:"rooms"`
	Guests                 []GuestCommand       `json:"guests"`
	FinancialGroups        []FinancialGroupView `json:"financial_groups"`
	History                []json.RawMessage    `json:"history"`
}
type Command struct {
	Version             int64         `json:"version"`
	Reason              string        `json:"reason"`
	ApprovalID          ids.ID        `json:"approval_id,omitempty"`
	RoomIDs             []ids.ID      `json:"room_ids,omitempty"`
	TargetReservationID *ids.ID       `json:"target_reservation_id,omitempty"`
	TargetVersion       int64         `json:"target_version,omitempty"`
	Rooms               []RoomCommand `json:"rooms,omitempty"`
	DateTo              string        `json:"date_to,omitempty"`
	EvidenceReferences  []string      `json:"evidence_references,omitempty"`
}
type SourceCommand struct {
	OrganisationID             ids.ID  `json:"organisation_id"`
	Provider                   string  `json:"provider"`
	ConnectionID               *ids.ID `json:"connection_id,omitempty"`
	ExternalID                 string  `json:"external_id"`
	ExternalVersion            string  `json:"external_version"`
	ExpectedReservationVersion int64   `json:"expected_reservation_version,omitempty"`
	Action                     string  `json:"action"`
	ReceiptID                  *ids.ID `json:"receipt_id,omitempty"`
	Booking                    Create  `json:"booking"`
}
type Acceptance struct {
	ReservationID    ids.ID `json:"reservation_id"`
	Version          int64  `json:"version"`
	State            string `json:"state"`
	DurableReference string `json:"durable_reference"`
	Duplicate        bool   `json:"duplicate"`
}
type Intake interface {
	AcceptSource(context.Context, pgx.Tx, SourceCommand) (Acceptance, error)
}

func (r Reservation) Actor() ids.ID {
	if r.CreatedBy == nil {
		return ids.Nil
	}
	return *r.CreatedBy
}

// FinancialGroupView distinguishes a frozen original collection requirement
// from room allocations and amendment credits. It is not a wallet balance.
type FinancialGroupView struct {
	ID                     ids.ID `json:"id"`
	ReservationID          ids.ID `json:"reservation_id"`
	AmountMinor            int64  `json:"amount_minor"`
	RequiredAmountMinor    int64  `json:"required_amount_minor"`
	Currency               string `json:"currency"`
	PolicyReference        string `json:"policy_reference"`
	AgreementVersion       int64  `json:"agreement_version"`
	State                  string `json:"state"`
	Responsibility         string `json:"responsibility"`
	Version                int64  `json:"version"`
	AllocatedAmountMinor   int64  `json:"allocated_amount_minor"`
	CreditsAppliedMinor    int64  `json:"credits_applied_minor"`
	CreditsForwardedMinor  int64  `json:"credits_forwarded_minor"`
	AllocationRelationship string `json:"allocation_relationship"`
}
