package revenue

import (
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

type PromotionDefinition struct {
	Currency         string     `json:"currency"`
	Discount         Adjustment `json:"discount"`
	MaxDiscountBPS   int64      `json:"max_discount_bps"`
	MaxDiscountMinor int64      `json:"max_discount_minor"`
	DateFrom         string     `json:"date_from"`
	DateTo           string     `json:"date_to"`
	EffectiveAt      time.Time  `json:"effective_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	MinNights        int        `json:"min_nights"`
	MaxNights        int        `json:"max_nights"`
	Channels         []string   `json:"channels"`
	RatePlanIDs      []ids.ID   `json:"rate_plan_ids"`
	Stackable        bool       `json:"stackable"`
	Priority         int        `json:"priority"`
	PolicyReference  string     `json:"policy_reference"`
}

func (p PromotionDefinition) Validate() error {
	if _, e := money.ParseCurrency(p.Currency); e != nil {
		return e
	}
	if p.Discount.Validate() != nil || p.Discount.AmountMinor > 0 || p.Discount.BasisPoints > 0 || p.MaxDiscountBPS < 1 || p.MaxDiscountBPS > 10000 || p.MaxDiscountMinor < 0 || p.MinNights < 1 || p.MaxNights < p.MinNights || p.MaxNights > 366 || !p.ExpiresAt.After(p.EffectiveAt) || p.Priority < 0 || len(p.PolicyReference) < 1 || len(p.PolicyReference) > 200 || len(p.Channels) > 50 || len(p.RatePlanIDs) > 50 {
		return ErrPricing
	}
	if _, e := (inventory.DateRange{DateFrom: p.DateFrom, DateTo: p.DateTo}).Days("UTC"); e != nil {
		return e
	}
	for _, c := range p.Channels {
		if c == "" || !inventory.ValidChannel(c) {
			return ErrPricing
		}
	}
	for _, id := range p.RatePlanIDs {
		if id.ValidateV7() != nil {
			return ErrPricing
		}
	}
	return nil
}

type Promotion struct {
	ID             ids.ID              `json:"id"`
	OrganisationID ids.ID              `json:"organisation_id"`
	PropertyID     ids.ID              `json:"property_id"`
	Code           string              `json:"code"`
	Definition     PromotionDefinition `json:"definition"`
	State          string              `json:"state"`
	Version        int64               `json:"version"`
	ApprovalID     *ids.ID             `json:"approval_id,omitempty"`
}
type AddOnDefinition struct {
	Currency           string `json:"currency"`
	AmountMinor        int64  `json:"amount_minor"`
	PerNight           bool   `json:"per_night"`
	MaxQuantity        int    `json:"max_quantity"`
	ExecutionReference string `json:"execution_reference"`
}

func (d AddOnDefinition) Validate() error {
	if _, e := money.ParseCurrency(d.Currency); e != nil {
		return e
	}
	if d.AmountMinor < 0 || d.MaxQuantity < 1 || d.MaxQuantity > 100 || len(d.ExecutionReference) < 1 || len(d.ExecutionReference) > 200 {
		return ErrPricing
	}
	return nil
}

type AddOn struct {
	ApprovalID     *ids.ID         `json:"approval_id,omitempty"`
	ID             ids.ID          `json:"id"`
	OrganisationID ids.ID          `json:"organisation_id"`
	PropertyID     ids.ID          `json:"property_id"`
	Name           string          `json:"name"`
	Definition     AddOnDefinition `json:"definition"`
	State          string          `json:"state"`
	Version        int64           `json:"version"`
}
type PackageDefinition struct {
	MaxDiscountMinor int64      `json:"max_discount_minor"`
	MaxDiscountBPS   int64      `json:"max_discount_bps"`
	AllowPromotions  bool       `json:"allow_promotions"`
	Currency         string     `json:"currency"`
	Discount         Adjustment `json:"discount"`
	MinNights        int        `json:"min_nights"`
	MaxNights        int        `json:"max_nights"`
	Channels         []string   `json:"channels"`
	PolicyReference  string     `json:"policy_reference"`
}

func (d PackageDefinition) Validate() error {
	if _, e := money.ParseCurrency(d.Currency); e != nil {
		return e
	}
	if d.MaxDiscountMinor < 0 || d.MaxDiscountBPS < 1 || d.MaxDiscountBPS > 10000 || d.Discount.Validate() != nil || d.Discount.AmountMinor > 0 || d.Discount.BasisPoints > 0 || d.MinNights < 1 || d.MaxNights < d.MinNights || d.MaxNights > 366 || len(d.PolicyReference) < 1 || len(d.PolicyReference) > 200 || len(d.Channels) > 50 {
		return ErrPricing
	}
	for _, c := range d.Channels {
		if c == "" || !inventory.ValidChannel(c) {
			return ErrPricing
		}
	}
	return nil
}

type Package struct {
	ID             ids.ID            `json:"id"`
	OrganisationID ids.ID            `json:"organisation_id"`
	PropertyID     ids.ID            `json:"property_id"`
	ProductID      ids.ID            `json:"product_id"`
	Name           string            `json:"name"`
	Definition     PackageDefinition `json:"definition"`
	State          string            `json:"state"`
	Version        int64             `json:"version"`
	ApprovalID     *ids.ID           `json:"approval_id,omitempty"`
}
