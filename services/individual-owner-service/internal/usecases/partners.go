package usecases

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/financing"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Rent financing / payment plans (§13.28) and insurance (§13.29). Both are
// partner-integrated through provider-neutral adapters: the owner domain
// shares listing-level context only after the owner's consent, stores
// partner references, disclosures and the owner's choices, and never the
// applicant's financial details. Without a partner adapter these features
// answer 503 FEATURE_UNAVAILABLE; owners can still record the insurance
// policies they hold and track claims with evidence.

// TemplateInsuranceRenewal reminds the owner of an expiring policy.
const TemplateInsuranceRenewal = "owner.insurance_renewal" // insurer, ends_on

const policyRenewalLead = 30 * 24 * time.Hour

// hasConsent reports whether the account currently grants a purpose.
func (s *Service) hasConsent(ctx context.Context, account ids.ID, purpose string) (db.Consent, bool, error) {
	c, err := s.q.CurrentConsent(ctx, s.dbq(ctx), db.CurrentConsentParams{AccountID: account, Purpose: purpose})
	if isNoRows(err) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	return c, s.consentView(c).Decision == "GRANTED", nil
}

// FinanceOfferView is a partner offer with its cost disclosure.
type FinanceOfferView struct {
	ID                    ids.ID          `json:"id"`
	ListingID             *ids.ID         `json:"listing_id"`
	ProductType           string          `json:"product_type"`
	PartnerRef            string          `json:"partner_ref"`
	Status                string          `json:"status"`
	OwnerPayoutPreference *string         `json:"owner_payout_preference"`
	Disclosure            json.RawMessage `json:"disclosure" doc:"Total cost, fees, term and conditions as disclosed by the partner"`
	ExpiresAt             *time.Time      `json:"expires_at"`
	DecidedAt             *time.Time      `json:"decided_at"`
}

func financeView(o db.FinanceOffer) FinanceOfferView {
	return FinanceOfferView{ID: o.ID, ListingID: o.ListingID, ProductType: o.ProductType, PartnerRef: o.PartnerRef, Status: o.Status,
		OwnerPayoutPreference: o.OwnerPayoutPreference, Disclosure: o.Disclosure, ExpiresAt: o.ExpiresAt, DecidedAt: o.DecidedAt}
}

// FinancingOptions asks financing partners for offers on a listing (needs
// the owner's PARTNER_FINANCING consent and a partner adapter).
func (s *Service) FinancingOptions(ctx context.Context, subj authz.Subject, listingID ids.ID) ([]FinanceOfferView, error) {
	if listingID.IsZero() {
		return nil, apperrors.InvalidRequest("listing_id is required.")
	}
	if _, err := s.authorize(ctx, subj, authz.ActFinancingConsent, ids.Nil); err != nil {
		return nil, err
	}
	l, err := s.q.GetListing(ctx, s.dbq(ctx), listingID)
	if isNoRows(err) {
		return nil, notFound(AggListing)
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapListingManage), l.PropertyID); err != nil {
		if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
			return nil, notFound(AggListing)
		}
		return nil, err
	}
	if s.Financing == nil {
		return nil, unavailable("Financing options", "a financing partner")
	}
	consent, ok, err := s.hasConsent(ctx, accountOf(subj), "PARTNER_FINANCING")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, precondition("CONSENT_REQUIRED", "Consent to share listing details with financing partners first (POST /financing/consents).")
	}
	req := financing.OptionsRequest{ListingID: l.ID, TransactionType: l.TransactionType}
	if err := s.dbq(ctx).QueryRow(ctx, `SELECT pv.currency, pv.base_amount_minor, pv.base_period, a.country_code, a.city FROM price_schedules ps
		JOIN pricing_versions pv ON pv.id = ps.active_pricing_version_id JOIN property_addresses a ON a.property_id = $2 WHERE ps.listing_id = $1`,
		l.ID, l.PropertyID).Scan(&req.Currency, &req.PriceMinor, &req.BasePeriod, &req.CountryCode, &req.City); isNoRows(err) {
		return nil, precondition("PRICING_NOT_SET", "Price the listing first.")
	} else if err != nil {
		return nil, err
	}
	offers, err := s.Financing.Options(ctx, req, "financing_options:"+l.ID.String()+":"+s.now().Format("2006010215"))
	if err != nil {
		return nil, downstream("financing-partner", err)
	}
	var out []FinanceOfferView
	err = s.InTx(ctx, func(ctx context.Context) error {
		now := s.now()
		for _, o := range offers {
			if !slices.Contains([]string{"RENT_INSTALMENT", "RENT_ADVANCE", "MORTGAGE_BUYER", "SELLER_PLAN"}, o.ProductType) || len(o.Disclosure) == 0 {
				s.Logger.WarnContext(ctx, "financing offer without a valid product type or disclosure ignored", "partner", o.PartnerRef)
				continue
			}
			raw, _ := json.Marshal(o.Disclosure)
			row, err := s.q.UpsertFinanceOffer(ctx, mustTx(ctx), db.UpsertFinanceOfferParams{ID: ids.New(), AccountID: accountOf(subj), ListingID: &l.ID,
				ProductType: o.ProductType, PartnerRef: o.PartnerRef, ExternalOfferRef: o.ExternalOfferRef, Disclosure: raw, ConsentID: consent.ID,
				ExpiresAt: o.ExpiresAt, CreatedAt: now})
			if isNoRows(err) {
				continue // already decided
			}
			if err != nil {
				return err
			}
			out = append(out, financeView(row))
		}
		return s.record(ctx, "financing.options", AggListing, l.ID, map[string]any{"offers": len(out)}, nil)
	})
	return out, err
}

// FinancingConsentInput grants or withdraws partner sharing.
type FinancingConsentInput struct {
	Decision      string `json:"decision" v:"required,enum=GRANTED|WITHDRAWN"`
	PolicyVersion string `json:"policy_version" v:"required,max=40"`
}

// RecordFinancingConsent records the PARTNER_FINANCING consent decision.
func (s *Service) RecordFinancingConsent(ctx context.Context, subj authz.Subject, in FinancingConsentInput) (PrivacyConsentView, error) {
	if err := schema.Validate(in); err != nil {
		return PrivacyConsentView{}, err
	}
	if _, err := s.authorize(ctx, subj, authz.ActFinancingConsent, ids.Nil); err != nil {
		return PrivacyConsentView{}, err
	}
	return s.RecordPrivacyConsent(ctx, subj, PrivacyConsentInput{Purpose: "PARTNER_FINANCING", Decision: in.Decision, PolicyVersion: in.PolicyVersion})
}

// ListFinanceOffers lists offers received.
func (s *Service) ListFinanceOffers(ctx context.Context, subj authz.Subject) ([]FinanceOfferView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActFinancingConsent, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListFinanceOffers(ctx, s.dbq(ctx), accountOf(subj))
	out := make([]FinanceOfferView, 0, len(rows))
	for _, r := range rows {
		out = append(out, financeView(r))
	}
	return out, err
}

// FinanceDecisionInput accepts or declines an offer.
type FinanceDecisionInput struct {
	Decision         string `json:"decision" v:"required,enum=ACCEPT|DECLINE"`
	PayoutPreference string `json:"payout_preference,omitempty" v:"enum=UPFRONT|AS_COLLECTED"`
}

// DecideFinanceOffer tells the partner the owner's choice, then records it.
func (s *Service) DecideFinanceOffer(ctx context.Context, subj authz.Subject, id ids.ID, in FinanceDecisionInput) (FinanceOfferView, error) {
	if err := schema.Validate(in); err != nil {
		return FinanceOfferView{}, err
	}
	if in.Decision == "ACCEPT" && in.PayoutPreference == "" {
		return FinanceOfferView{}, apperrors.InvalidRequest("payout_preference is required to accept.")
	}
	if _, err := s.authorize(ctx, subj, authz.ActFinancingConsent, ids.Nil); err != nil {
		return FinanceOfferView{}, err
	}
	o, err := s.q.GetFinanceOfferForUpdate(ctx, s.Pool, id)
	if isNoRows(err) || (err == nil && o.AccountID != accountOf(subj)) {
		return FinanceOfferView{}, notFound("finance_offer")
	}
	if err != nil {
		return FinanceOfferView{}, err
	}
	if o.Status != "OFFERED" || (o.ExpiresAt != nil && !o.ExpiresAt.After(s.now())) {
		return FinanceOfferView{}, conflict("OFFER_NOT_OPEN", "The offer is no longer open.")
	}
	if s.Financing == nil {
		return FinanceOfferView{}, unavailable("Financing decisions", "a financing partner")
	}
	if err := s.Financing.Decide(ctx, o.PartnerRef, o.ExternalOfferRef, in.Decision, in.PayoutPreference, "finance_offer:"+id.String()+":"+in.Decision); err != nil {
		return FinanceOfferView{}, downstream("financing-partner", err)
	}
	var out FinanceOfferView
	err = s.InTx(ctx, func(ctx context.Context) error {
		status := map[string]string{"ACCEPT": "ACCEPTED", "DECLINE": "DECLINED"}[in.Decision]
		if err := s.q.SetFinanceOfferDecision(ctx, mustTx(ctx), db.SetFinanceOfferDecisionParams{ID: id, Status: status, OwnerPayoutPreference: strp(in.PayoutPreference),
			DecidedAt: ptr(s.now())}); err != nil {
			return err
		}
		n, err := s.q.GetFinanceOfferForUpdate(ctx, mustTx(ctx), id)
		out = financeView(n)
		if err != nil {
			return err
		}
		return s.record(ctx, "financing.offer."+lower(in.Decision), "finance_offer", id, map[string]any{"payout_preference": in.PayoutPreference}, nil)
	})
	return out, err
}

// ---- insurance ----

// InsuranceProductView is a partner product.
type InsuranceProductView struct {
	PartnerRef    string         `json:"partner_ref"`
	ProductRef    string         `json:"product_ref"`
	ProductType   string         `json:"product_type"`
	CoverageMinor int64          `json:"coverage_minor"`
	PremiumMinor  int64          `json:"premium_minor"`
	Currency      string         `json:"currency"`
	Terms         map[string]any `json:"terms"`
}

// InsuranceOptions lists partner products for a property.
func (s *Service) InsuranceOptions(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]InsuranceProductView, error) {
	if propertyID.IsZero() {
		return nil, apperrors.InvalidRequest("property_id is required.")
	}
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), propertyID); err != nil {
		return nil, err
	}
	if s.Insurance == nil {
		return nil, unavailable("Insurance options", "an insurance partner")
	}
	if _, ok, err := s.hasConsent(ctx, accountOf(subj), "PARTNER_INSURANCE"); err != nil || !ok {
		if err == nil {
			err = precondition("CONSENT_REQUIRED", "Consent to share property details with insurance partners first.")
		}
		return nil, err
	}
	var ptype, cc string
	if err := s.dbq(ctx).QueryRow(ctx, `SELECT p.property_type, a.country_code FROM properties p JOIN property_addresses a ON a.property_id = p.id WHERE p.id = $1`,
		propertyID).Scan(&ptype, &cc); err != nil {
		return nil, err
	}
	ps, err := s.Insurance.Options(ctx, propertyID, ptype, cc)
	if err != nil {
		return nil, downstream("insurance-partner", err)
	}
	out := make([]InsuranceProductView, 0, len(ps))
	for _, p := range ps {
		out = append(out, InsuranceProductView(p))
	}
	return out, nil
}

// PolicyInput records a policy the owner holds.
type PolicyInput struct {
	PropertyID      ids.ID `json:"property_id" v:"required"`
	ProductType     string `json:"product_type" v:"required,enum=BUILDING|CONTENTS|LANDLORD_LIABILITY|RENT_GUARANTEE|SHORTLET_COVER|WORKMANSHIP|FIDELITY|OTHER"`
	InsurerName     string `json:"insurer_name" v:"required,max=120"`
	PolicyRef       string `json:"policy_ref" v:"required,max=128"`
	CoverageMinor   *int64 `json:"coverage_minor,omitempty" v:"min=1"`
	PremiumMinor    *int64 `json:"premium_minor,omitempty" v:"min=0"`
	Currency        string `json:"currency" v:"required,pattern=[A-Z]{3}"`
	StartsOn        string `json:"starts_on" v:"required,format=date"`
	EndsOn          string `json:"ends_on" v:"required,format=date"`
	DocumentMediaID ids.ID `json:"document_media_id" v:"required" doc:"Policy schedule/certificate"`
}

// MediaRefs lists referenced media.
func (in PolicyInput) MediaRefs() []ids.ID { return []ids.ID{in.DocumentMediaID} }

// PolicyView is an insurance policy.
type PolicyView struct {
	ID              ids.ID  `json:"id"`
	PropertyID      ids.ID  `json:"property_id"`
	Source          string  `json:"source"`
	ProductType     string  `json:"product_type"`
	InsurerName     string  `json:"insurer_name"`
	PolicyRef       string  `json:"policy_ref"`
	CoverageMinor   *int64  `json:"coverage_minor"`
	PremiumMinor    *int64  `json:"premium_minor"`
	Currency        string  `json:"currency"`
	StartsOn        string  `json:"starts_on"`
	EndsOn          string  `json:"ends_on"`
	Status          string  `json:"status"`
	DocumentMediaID *ids.ID `json:"document_media_id"`
	Version         int64   `json:"version"`
}

func policyView(p db.InsurancePolicy) PolicyView {
	return PolicyView{ID: p.ID, PropertyID: p.PropertyID, Source: p.Source, ProductType: p.ProductType, InsurerName: p.InsurerName, PolicyRef: p.PolicyRef,
		CoverageMinor: p.CoverageMinor, PremiumMinor: p.PremiumMinor, Currency: p.Currency, StartsOn: p.StartsOn.Format(time.DateOnly),
		EndsOn: p.EndsOn.Format(time.DateOnly), Status: p.Status, DocumentMediaID: p.DocumentMediaID, Version: p.Version}
}

// RecordPolicy stores a policy the owner holds (with its document).
func (s *Service) RecordPolicy(ctx context.Context, subj authz.Subject, in PolicyInput, ev Evidence) (PolicyView, error) {
	if err := schema.Validate(in); err != nil {
		return PolicyView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return PolicyView{}, err
	}
	start, end := *schema.ParseDate(&in.StartsOn), *schema.ParseDate(&in.EndsOn)
	if !end.After(start) {
		return PolicyView{}, apperrors.InvalidRequest("ends_on must be after starts_on.")
	}
	var out PolicyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyEdit), in.PropertyID); err != nil {
			return err
		}
		now := s.now()
		status := "ACTIVE"
		switch today := dayOf(now); {
		case end.Before(today):
			status = "EXPIRED"
		case end.Before(today.Add(policyRenewalLead)):
			status = "EXPIRING"
		}
		p, err := s.q.InsertPolicy(ctx, mustTx(ctx), db.InsertPolicyParams{ID: ids.New(), AccountID: accountOf(subj), PropertyID: in.PropertyID, Source: "OWNER_RECORDED",
			ProductType: in.ProductType, InsurerName: in.InsurerName, PolicyRef: in.PolicyRef, CoverageMinor: in.CoverageMinor, PremiumMinor: in.PremiumMinor,
			Currency: in.Currency, StartsOn: start, EndsOn: end, Status: status, DocumentMediaID: &in.DocumentMediaID, CreatedAt: now})
		if isUnique(err, "insurance_policies_ref_uniq") {
			return conflict("POLICY_EXISTS", "This policy is already recorded for the property.")
		}
		if err != nil {
			return err
		}
		out = policyView(p)
		if err := s.propertyHistory(ctx, in.PropertyID, "INSURANCE_POLICY_RECORDED", in.ProductType+" with "+in.InsurerName, "insurance_policy", p.ID); err != nil {
			return err
		}
		return s.record(ctx, "insurance.policy.record", "insurance_policy", p.ID, map[string]any{"product_type": in.ProductType, "ends_on": in.EndsOn}, nil)
	})
	return out, err
}

// ListPolicies lists policies across the caller's properties.
func (s *Service) ListPolicies(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]PolicyView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPolicies(ctx, s.dbq(ctx), db.ListPoliciesParams{AccountID: accountOf(subj), Now: s.now(), PropertyID: idp(propertyID)})
	out := make([]PolicyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, policyView(r))
	}
	return out, err
}

// InsuranceClaimInput files a claim.
type InsuranceClaimInput struct {
	PolicyID         ids.ID   `json:"policy_id" v:"required"`
	IncidentDate     string   `json:"incident_date" v:"required,format=date"`
	Description      string   `json:"description" v:"required,max=4000"`
	ClaimedMinor     int64    `json:"claimed_minor" v:"required,min=1"`
	InsurerClaimRef  *string  `json:"insurer_claim_ref,omitempty" v:"max=128" doc:"Reference given by the insurer (owner-recorded policies)"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids" v:"required,max=40"`
	RelatedRefType   *string  `json:"related_ref_type,omitempty" v:"enum=maintenance_request|damage_claim|tenancy"`
	RelatedRefID     *ids.ID  `json:"related_ref_id,omitempty"`
}

// MediaRefs lists referenced media.
func (in InsuranceClaimInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// InsuranceClaimView is a claim.
type InsuranceClaimView struct {
	ID               ids.ID    `json:"id"`
	PolicyID         ids.ID    `json:"policy_id"`
	PropertyID       ids.ID    `json:"property_id"`
	IncidentDate     string    `json:"incident_date"`
	Description      string    `json:"description"`
	ClaimedMinor     int64     `json:"claimed_minor"`
	Currency         string    `json:"currency"`
	Status           string    `json:"status"`
	InsurerClaimRef  *string   `json:"insurer_claim_ref"`
	ApprovedMinor    *int64    `json:"approved_minor"`
	PaidMinor        int64     `json:"paid_minor" doc:"Insurer payout as evidenced by the owner (off-platform)"`
	EvidenceMediaIDs []ids.ID  `json:"evidence_media_ids"`
	RelatedRefType   *string   `json:"related_ref_type"`
	RelatedRefID     *ids.ID   `json:"related_ref_id"`
	StatusNote       *string   `json:"status_note"`
	Version          int64     `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
}

func claimInsView(c db.InsuranceClaim) InsuranceClaimView {
	return InsuranceClaimView{ID: c.ID, PolicyID: c.PolicyID, PropertyID: c.PropertyID, IncidentDate: c.IncidentDate.Format(time.DateOnly),
		Description: c.Description, ClaimedMinor: c.ClaimedMinor, Currency: c.Currency, Status: c.Status, InsurerClaimRef: c.InsurerClaimRef,
		ApprovedMinor: c.ApprovedMinor, PaidMinor: c.PaidMinor, EvidenceMediaIDs: nonNilIDs(c.EvidenceMediaIds), RelatedRefType: c.RelatedRefType,
		RelatedRefID: c.RelatedRefID, StatusNote: c.StatusNote, Version: c.Version, CreatedAt: c.CreatedAt}
}

// FileInsuranceClaim records a claim on a policy the owner holds. Claims on
// partner policies need a partner claims contract (503 until one exists).
func (s *Service) FileInsuranceClaim(ctx context.Context, subj authz.Subject, in InsuranceClaimInput, ev Evidence) (InsuranceClaimView, error) {
	if err := schema.Validate(in); err != nil {
		return InsuranceClaimView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return InsuranceClaimView{}, err
	}
	if (in.RelatedRefType == nil) != (in.RelatedRefID == nil) {
		return InsuranceClaimView{}, apperrors.InvalidRequest("related_ref_type and related_ref_id go together.")
	}
	incident := *schema.ParseDate(&in.IncidentDate)
	var out InsuranceClaimView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		p, err := s.q.GetPolicy(ctx, tx, in.PolicyID)
		if isNoRows(err) {
			return notFound("insurance_policy")
		}
		if err != nil {
			return err
		}
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyEdit), p.PropertyID); err != nil {
			if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
				return notFound("insurance_policy")
			}
			return err
		}
		switch {
		case p.Source == "PARTNER":
			return unavailable("Partner claim submission", "an insurance partner claims contract")
		case incident.Before(p.StartsOn) || incident.After(p.EndsOn) || incident.After(dayOf(s.now())):
			return precondition("INCIDENT_OUTSIDE_COVER", "The incident date is outside the policy period.")
		case p.CoverageMinor != nil && in.ClaimedMinor > *p.CoverageMinor:
			return precondition("ABOVE_COVERAGE", "The claim exceeds the policy coverage.")
		}
		if in.RelatedRefType != nil {
			if pid, err := s.scopeProperty(ctx, map[string]string{"maintenance_request": ScopeMaintenance, "damage_claim": ScopeDamageClaim,
				"tenancy": ScopeTenancy}[*in.RelatedRefType], *in.RelatedRefID); err != nil || pid != p.PropertyID {
				return apperrors.InvalidRequest("The related record does not belong to the policy's property.")
			}
		}
		c, err := s.q.InsertInsuranceClaim(ctx, tx, db.InsertInsuranceClaimParams{ID: ids.New(), PolicyID: p.ID, PropertyID: p.PropertyID, IncidentDate: incident,
			Description: in.Description, ClaimedMinor: in.ClaimedMinor, Currency: p.Currency, InsurerClaimRef: in.InsurerClaimRef,
			EvidenceMediaIds: in.EvidenceMediaIDs, RelatedRefType: in.RelatedRefType, RelatedRefID: in.RelatedRefID, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = claimInsView(c)
		return s.record(ctx, "insurance.claim.file", "insurance_claim", c.ID, map[string]any{"policy_id": p.ID, "claimed_minor": in.ClaimedMinor}, nil)
	})
	return out, err
}

// ListInsuranceClaims lists claims across the caller's properties.
func (s *Service) ListInsuranceClaims(ctx context.Context, subj authz.Subject, policyID ids.ID) ([]InsuranceClaimView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListInsuranceClaims(ctx, s.dbq(ctx), db.ListInsuranceClaimsParams{AccountID: accountOf(subj), Now: s.now(), PolicyID: idp(policyID)})
	out := make([]InsuranceClaimView, 0, len(rows))
	for _, r := range rows {
		out = append(out, claimInsView(r))
	}
	return out, err
}

// InsuranceClaimUpdate records the insurer's progress (If-Match).
type InsuranceClaimUpdate struct {
	Status           string   `json:"status" v:"required,enum=UNDER_REVIEW|APPROVED|PARTIALLY_APPROVED|REJECTED|PAID|WITHDRAWN|COMPLAINT"`
	InsurerClaimRef  *string  `json:"insurer_claim_ref,omitempty" v:"max=128"`
	ApprovedMinor    *int64   `json:"approved_minor,omitempty" v:"min=0"`
	PaidMinor        *int64   `json:"paid_minor,omitempty" v:"min=0"`
	Note             *string  `json:"note,omitempty" v:"max=2000"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=10" doc:"Insurer decision letter / payout proof (required for decisions and payment)"`
}

// MediaRefs lists referenced media.
func (in InsuranceClaimUpdate) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

var claimEdges = map[string][]string{
	"SUBMITTED":          {"UNDER_REVIEW", "APPROVED", "PARTIALLY_APPROVED", "REJECTED", "WITHDRAWN"},
	"UNDER_REVIEW":       {"APPROVED", "PARTIALLY_APPROVED", "REJECTED", "WITHDRAWN"},
	"APPROVED":           {"PAID", "COMPLAINT"},
	"PARTIALLY_APPROVED": {"PAID", "COMPLAINT"},
	"REJECTED":           {"COMPLAINT"},
	"COMPLAINT":          {"APPROVED", "PARTIALLY_APPROVED", "REJECTED", "WITHDRAWN"},
}

// UpdateInsuranceClaim records claim progress on owner-recorded policies.
func (s *Service) UpdateInsuranceClaim(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in InsuranceClaimUpdate, ev Evidence) (InsuranceClaimView, error) {
	if err := schema.Validate(in); err != nil {
		return InsuranceClaimView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return InsuranceClaimView{}, err
	}
	var out InsuranceClaimView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		c, err := s.q.GetInsuranceClaimForUpdate(ctx, tx, id)
		if isNoRows(err) {
			return notFound("insurance_claim")
		}
		if err != nil {
			return err
		}
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyEdit), c.PropertyID); err != nil {
			if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
				return notFound("insurance_claim")
			}
			return err
		}
		if !slices.Contains(claimEdges[c.Status], in.Status) {
			return apperrors.InvalidStateTransition(c.Status, in.Status)
		}
		decision := slices.Contains([]string{"APPROVED", "PARTIALLY_APPROVED", "REJECTED", "PAID"}, in.Status)
		if decision && len(in.EvidenceMediaIDs) == 0 {
			return precondition("EVIDENCE_REQUIRED", "Attach the insurer's decision or payout proof.")
		}
		approved, paid := c.ApprovedMinor, c.PaidMinor
		switch in.Status {
		case "APPROVED", "PARTIALLY_APPROVED":
			if in.ApprovedMinor == nil || *in.ApprovedMinor > c.ClaimedMinor || (in.Status == "APPROVED") != (*in.ApprovedMinor == c.ClaimedMinor) {
				return apperrors.InvalidRequest("approved_minor is required: the full claim for APPROVED, less for PARTIALLY_APPROVED.")
			}
			approved = in.ApprovedMinor
		case "PAID":
			if in.PaidMinor == nil || approved == nil || *in.PaidMinor > *approved {
				return apperrors.InvalidRequest("paid_minor is required and cannot exceed the approved amount.")
			}
			paid = *in.PaidMinor
		}
		n, err := s.q.UpdateInsuranceClaim(ctx, tx, db.UpdateInsuranceClaimParams{ID: id, Version: expected, Status: in.Status, InsurerClaimRef: in.InsurerClaimRef,
			ApprovedMinor: approved, PaidMinor: paid, EvidenceMediaIds: append(nonNilIDs(c.EvidenceMediaIds), in.EvidenceMediaIDs...), StatusNote: in.Note, Now: s.now()})
		if isNoRows(err) {
			return apperrors.Conflict("VERSION_CONFLICT", "The claim changed; reload and retry.")
		}
		if err != nil {
			return err
		}
		out = claimInsView(n)
		return s.record(ctx, "insurance.claim.update", "insurance_claim", id, map[string]any{"status": in.Status, "approved_minor": approved, "paid_minor": paid},
			map[string]any{"attested_by": "OWNER"})
	})
	return out, err
}

// RunInsuranceJobs reminds owners of policies expiring within 30 days and
// expires lapsed policies.
func (s *Service) RunInsuranceJobs(ctx context.Context, batch int32) error {
	now := s.now()
	due, err := s.q.PoliciesToRemind(ctx, s.Pool, db.PoliciesToRemindParams{Horizon: dayOf(now).Add(policyRenewalLead), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, p := range due {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			if err := s.q.MarkPolicyExpiring(ctx, mustTx(ctx), db.MarkPolicyExpiringParams{ID: p.ID, RenewalRemindedAt: &now}); err != nil {
				return err
			}
			return s.notifyOwner(ctx, p.AccountID, TemplateInsuranceRenewal, map[string]string{"insurer": p.InsurerName, "ends_on": p.EndsOn.Format(time.DateOnly)})
		}); err != nil {
			return err
		}
	}
	_, err = s.q.ExpirePolicies(ctx, s.Pool, db.ExpirePoliciesParams{Now: now, Today: dayOf(now)})
	return err
}
