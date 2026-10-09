package usecases

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Pricing (§13.8): every change creates an immutable pricing version; the
// price schedule points at the active one. Accepted versions are frozen by a
// database trigger. The all-in calculator (domain.Calculate) is shared by
// preview and acceptance.

// FeeInput is one disclosed fee.
type FeeInput struct {
	Code        string `json:"code" v:"required,pattern=[a-z][a-z0-9_]{1,39}" ex:"agency_fee"`
	Category    string `json:"category" v:"required,pattern=[A-Z][A-Z0-9_]{1,39}" ex:"AGENCY" doc:"Must be a configured fee category"`
	Label       string `json:"label" v:"required,max=120"`
	Calculation string `json:"calculation" v:"required,enum=FIXED|PERCENT_OF_BASE"`
	AmountMinor *int64 `json:"amount_minor,omitempty" v:"min=0"`
	RateBps     *int32 `json:"rate_bps,omitempty" v:"min=1,max=10000"`
	Frequency   string `json:"frequency" v:"required,enum=ONE_TIME|PER_CYCLE|PER_NIGHT|PER_STAY"`
	Mandatory   bool   `json:"mandatory"`
	Refundable  bool   `json:"refundable"`
	PayableTo   string `json:"payable_to" v:"required,enum=OWNER|MANAGER|THIRD_PARTY" doc:"Platform fees are defined by the platform, never by owners"`
}

// RuleInput is one pricing rule.
type RuleInput struct {
	RuleType string            `json:"rule_type" v:"required,enum=ESCALATION|DISCOUNT|SEASONAL|WEEKEND|LONG_STAY"`
	Params   domain.RuleParams `json:"params"`
}

// PricingInput replaces the listing's price model (a new version).
type PricingInput struct {
	Currency           string      `json:"currency" v:"required,format=currency" ex:"NGN"`
	BaseAmountMinor    int64       `json:"base_amount_minor" v:"required,min=1,max=100000000000000"`
	BasePeriod         string      `json:"base_period" v:"required,enum=MONTH|YEAR|NIGHT|TOTAL"`
	PaymentCycle       string      `json:"payment_cycle" v:"required,enum=MONTHLY|QUARTERLY|BIANNUAL|ANNUAL|PER_STAY|ONE_TIME"`
	DepositAmountMinor int64       `json:"deposit_amount_minor" v:"min=0,max=100000000000000"`
	Negotiable         bool        `json:"negotiable"`
	Fees               []FeeInput  `json:"fees,omitempty" v:"max=30"`
	Rules              []RuleInput `json:"rules,omitempty" v:"max=20"`
}

// PricingView is one pricing version with its fees and rules.
type PricingView struct {
	PricingVersionID   ids.ID        `json:"pricing_version_id"`
	VersionNo          int32         `json:"version_no"`
	Currency           string        `json:"currency"`
	BaseAmountMinor    int64         `json:"base_amount_minor"`
	BasePeriod         string        `json:"base_period"`
	PaymentCycle       string        `json:"payment_cycle"`
	DepositAmountMinor int64         `json:"deposit_amount_minor"`
	Negotiable         bool          `json:"negotiable"`
	Fees               []domain.Fee  `json:"fees"`
	Rules              []domain.Rule `json:"rules"`
	Active             bool          `json:"active"`
	Accepted           bool          `json:"accepted" doc:"Frozen: referenced by an accepted offer"`
	PolicyVersion      string        `json:"policy_version"`
	CreatedAt          time.Time     `json:"created_at"`
}

// ScheduleView is GET /listings/{id}/pricing.
type ScheduleView struct {
	ListingID ids.ID       `json:"listing_id"`
	Active    *PricingView `json:"active"`
	Version   int64        `json:"version" doc:"Price schedule version (If-Match); 0 before the first price is set"`
}

func (s *Service) loadPricing(ctx context.Context, id ids.ID, activeID *ids.ID) (PricingView, domain.PriceModel, error) {
	q := s.dbq(ctx)
	v, err := s.q.GetPricingVersion(ctx, q, id)
	if err != nil {
		return PricingView{}, domain.PriceModel{}, err
	}
	fees, err := s.q.ListFeeDefinitions(ctx, q, id)
	if err != nil {
		return PricingView{}, domain.PriceModel{}, err
	}
	rules, err := s.q.ListPricingRules(ctx, q, id)
	if err != nil {
		return PricingView{}, domain.PriceModel{}, err
	}
	pv := PricingView{PricingVersionID: v.ID, VersionNo: v.VersionNo, Currency: v.Currency, BaseAmountMinor: v.BaseAmountMinor, BasePeriod: v.BasePeriod,
		PaymentCycle: v.PaymentCycle, DepositAmountMinor: v.DepositAmountMinor, Negotiable: v.Negotiable, Fees: []domain.Fee{}, Rules: []domain.Rule{},
		Active: activeID != nil && *activeID == v.ID, Accepted: v.AcceptedAt != nil, PolicyVersion: v.PolicyVersion, CreatedAt: v.CreatedAt}
	for _, f := range fees {
		pv.Fees = append(pv.Fees, domain.Fee{Code: f.Code, Category: f.Category, Label: f.Label, Calculation: f.Calculation, AmountMinor: f.AmountMinor,
			RateBps: f.RateBps, Frequency: f.Frequency, Mandatory: f.Mandatory, Refundable: f.Refundable, PayableTo: f.PayableTo})
	}
	for _, r := range rules {
		var p domain.RuleParams
		_ = json.Unmarshal(r.Params, &p)
		pv.Rules = append(pv.Rules, domain.Rule{Type: r.RuleType, Params: p})
	}
	m := domain.PriceModel{Currency: v.Currency, Base: v.BaseAmountMinor, BasePeriod: v.BasePeriod, Cycle: v.PaymentCycle, Deposit: v.DepositAmountMinor,
		Fees: pv.Fees, Rules: pv.Rules}
	return pv, m, nil
}

// activePricing returns the listing's active pricing (nil when unset).
func (s *Service) activePricing(ctx context.Context, l db.Listing) (*PricingView, *domain.PriceModel, error) {
	sch, err := s.q.GetPriceSchedule(ctx, s.dbq(ctx), l.ID)
	if isNoRows(err) || (err == nil && sch.ActivePricingVersionID == nil) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	pv, m, err := s.loadPricing(ctx, *sch.ActivePricingVersionID, sch.ActivePricingVersionID)
	if err != nil {
		return nil, nil, err
	}
	d := listingDetails(l)
	m.TransactionType, m.MinTermMonths, m.MinNights = l.TransactionType, deref(d.MinTermMonths), deref(d.MinNights)
	return &pv, &m, nil
}

// GetPricing returns the active price model.
func (s *Service) GetPricing(ctx context.Context, subj authz.Subject, listingID ids.ID) (ScheduleView, error) {
	l, _, err := s.listingFor(ctx, subj, listingID, authz.Read(authz.CapListingManage))
	if err != nil {
		return ScheduleView{}, err
	}
	v := ScheduleView{ListingID: l.ID}
	if sch, err := s.q.GetPriceSchedule(ctx, s.dbq(ctx), l.ID); err == nil {
		v.Version = sch.Version
	} else if !isNoRows(err) {
		return v, err
	}
	v.Active, _, err = s.activePricing(ctx, l)
	return v, err
}

// PriceHistory lists every pricing version (newest first).
func (s *Service) PriceHistory(ctx context.Context, subj authz.Subject, listingID ids.ID) ([]PricingView, error) {
	l, _, err := s.listingFor(ctx, subj, listingID, authz.Read(authz.CapListingManage))
	if err != nil {
		return nil, err
	}
	var active *ids.ID
	if sch, err := s.q.GetPriceSchedule(ctx, s.dbq(ctx), l.ID); err == nil {
		active = sch.ActivePricingVersionID
	}
	rows, err := s.q.ListPricingVersions(ctx, s.dbq(ctx), l.ID)
	if err != nil {
		return nil, err
	}
	out := make([]PricingView, 0, len(rows))
	for _, r := range rows {
		pv, _, err := s.loadPricing(ctx, r.ID, active)
		if err != nil {
			return nil, err
		}
		out = append(out, pv)
	}
	return out, nil
}

// PutPricing creates and activates a new pricing version (If-Match on the
// price schedule; "0" for the first price). Changing the price of a
// published listing needs co-owner consent (PRICE_CHANGE, target = listing)
// and republishes a new listing version.
func (s *Service) PutPricing(ctx context.Context, subj authz.Subject, listingID ids.ID, expected int64, in PricingInput) (ScheduleView, error) {
	if err := schema.Validate(in); err != nil {
		return ScheduleView{}, err
	}
	var out ScheduleView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, d, err := s.listingForUpdate(ctx, subj, listingID, authz.Do(authz.CapPricingManage))
		if err != nil {
			return err
		}
		if l.Status == "WITHDRAWN" || l.Status == "ARCHIVED" {
			return conflict("LISTING_CLOSED", "The listing is "+l.Status+".")
		}
		if l.Status == "PUBLISHED" || l.Status == "PAUSED" {
			if err := s.requireConsent(ctx, d, l.PropertyID, ConsentPriceChange, l.ID, s.actingAccount(ctx, subj, l.PropertyID)); err != nil {
				return err
			}
		}
		det := listingDetails(l)
		model := domain.PriceModel{TransactionType: l.TransactionType, Currency: in.Currency, Base: in.BaseAmountMinor, BasePeriod: in.BasePeriod,
			Cycle: in.PaymentCycle, Deposit: in.DepositAmountMinor, MinTermMonths: deref(det.MinTermMonths), MinNights: deref(det.MinNights)}
		for _, f := range in.Fees {
			model.Fees = append(model.Fees, domain.Fee{Code: f.Code, Category: f.Category, Label: strings.TrimSpace(f.Label), Calculation: f.Calculation,
				AmountMinor: f.AmountMinor, RateBps: f.RateBps, Frequency: f.Frequency, Mandatory: f.Mandatory, Refundable: f.Refundable, PayableTo: f.PayableTo})
		}
		for _, r := range in.Rules {
			model.Rules = append(model.Rules, domain.Rule{Type: r.RuleType, Params: r.Params})
		}
		if err := domain.ValidatePriceModel(model, s.Policy.Domain.FeeCategories); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		sch, err := s.q.GetPriceScheduleForUpdate(ctx, tx, l.ID)
		exists := err == nil
		if err != nil && !isNoRows(err) {
			return err
		}
		if (exists && expected != sch.Version) || (!exists && expected != 0) {
			return lifecycle.ErrVersionConflict()
		}
		no, err := s.q.NextPricingVersionNo(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		dom, actor := s.actor(ctx, subj, l.PropertyID)
		pv, err := s.q.InsertPricingVersion(ctx, tx, db.InsertPricingVersionParams{ID: ids.New(), ListingID: l.ID, VersionNo: no, Currency: in.Currency,
			BaseAmountMinor: in.BaseAmountMinor, BasePeriod: in.BasePeriod, PaymentCycle: in.PaymentCycle, DepositAmountMinor: in.DepositAmountMinor,
			Negotiable: in.Negotiable, PolicyVersion: s.Policy.Version, CreatedByActorDomain: dom, CreatedByActorID: actor, CreatedAt: now})
		if err != nil {
			return err
		}
		for _, f := range model.Fees {
			if err := s.q.InsertFeeDefinition(ctx, tx, db.InsertFeeDefinitionParams{ID: ids.New(), PricingVersionID: pv.ID, Code: f.Code, Category: f.Category,
				Label: f.Label, Calculation: f.Calculation, AmountMinor: f.AmountMinor, RateBps: f.RateBps, Frequency: f.Frequency, Mandatory: f.Mandatory,
				Refundable: f.Refundable, PayableTo: f.PayableTo}); err != nil {
				return err
			}
		}
		for i, r := range model.Rules {
			params, _ := json.Marshal(r.Params)
			if err := s.q.InsertPricingRule(ctx, tx, db.InsertPricingRuleParams{ID: ids.New(), PricingVersionID: pv.ID, RuleType: r.Type, Position: int32(i), Params: params}); err != nil {
				return err
			}
		}
		if exists {
			_, err = s.q.UpdatePriceSchedule(ctx, tx, db.UpdatePriceScheduleParams{ListingID: l.ID, Version: sch.Version, ActivePricingVersionID: &pv.ID,
				Currency: in.Currency, UpdatedAt: now})
		} else {
			_, err = s.q.InsertPriceSchedule(ctx, tx, db.InsertPriceScheduleParams{ListingID: l.ID, ActivePricingVersionID: &pv.ID, Currency: in.Currency, UpdatedAt: now})
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "listing.pricing.update", "listing", l.ID, map[string]any{"pricing_version": no, "base_amount_minor": in.BaseAmountMinor,
			"currency": in.Currency}, nil); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, l.PropertyID, "PRICING_CHANGED", "listing", l.ID, "Pricing version "+itoa(int(no))); err != nil {
			return err
		}
		if l.Status == "PUBLISHED" {
			if err := s.republish(ctx, subj, l.ID, "PRICE_CHANGE"); err != nil {
				return err
			}
		}
		out = ScheduleView{ListingID: l.ID}
		nsch, err := s.q.GetPriceSchedule(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		out.Version = nsch.Version
		nl, err := s.q.GetListing(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		out.Active, _, err = s.activePricing(ctx, nl)
		return err
	})
	return out, err
}

// CostPreviewInput describes the customer scenario.
type CostPreviewInput struct {
	TermMonths int32      `json:"term_months,omitempty" v:"min=1,max=600" doc:"Leases; default = minimum term or 12"`
	CheckIn    *time.Time `json:"check_in,omitempty" doc:"Short-let"`
	CheckOut   *time.Time `json:"check_out,omitempty"`
}

// CostPreviewView is the all-in cost with the pricing version it used.
type CostPreviewView struct {
	ListingID        ids.ID      `json:"listing_id"`
	PricingVersionID ids.ID      `json:"pricing_version_id"`
	Cost             domain.Cost `json:"cost"`
}

func (s *Service) costPreview(ctx context.Context, l db.Listing, in CostPreviewInput) (CostPreviewView, error) {
	pv, m, err := s.activePricing(ctx, l)
	if err != nil {
		return CostPreviewView{}, err
	}
	if pv == nil {
		return CostPreviewView{}, precondition("PRICING_NOT_SET", "The listing has no price yet.")
	}
	sc := domain.Scenario{TermMonths: in.TermMonths}
	if in.CheckIn != nil {
		sc.CheckIn = *in.CheckIn
	}
	if in.CheckOut != nil {
		sc.CheckOut = *in.CheckOut
	}
	c, err := domain.Calculate(*m, sc)
	return CostPreviewView{ListingID: l.ID, PricingVersionID: pv.PricingVersionID, Cost: c}, err
}

// CostPreview is the owner's all-in cost preview.
func (s *Service) CostPreview(ctx context.Context, subj authz.Subject, listingID ids.ID, in CostPreviewInput) (CostPreviewView, error) {
	if err := schema.Validate(in); err != nil {
		return CostPreviewView{}, err
	}
	l, _, err := s.listingFor(ctx, subj, listingID, authz.Read(authz.CapListingManage))
	if err != nil {
		return CostPreviewView{}, err
	}
	return s.costPreview(ctx, l, in)
}

// InternalCostPreview is the marketplace (customer-facing) all-in cost of a
// published listing, computed by the same calculator as acceptance.
func (s *Service) InternalCostPreview(ctx context.Context, subj authz.Subject, listingID ids.ID, in CostPreviewInput) (CostPreviewView, error) {
	if err := schema.Validate(in); err != nil {
		return CostPreviewView{}, err
	}
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalListingRead, authz.Resource{}); err != nil {
		return CostPreviewView{}, err
	}
	l, err := s.q.GetListing(ctx, s.dbq(ctx), listingID)
	if isNoRows(err) || (err == nil && l.Status != "PUBLISHED") {
		return CostPreviewView{}, notFound("listing")
	}
	if err != nil {
		return CostPreviewView{}, err
	}
	return s.costPreview(ctx, l, in)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

var _ = apperrors.CodeOf
