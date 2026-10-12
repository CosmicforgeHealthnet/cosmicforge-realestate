package usecases

import (
	"context"
	"math"
	"slices"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
)

func (s *Service) acceptedRentContext(ctx context.Context, o db.RentObligation, q payment.AcceptedQuote, p payment.CollectionPolicy) error {
	b, _, e := s.coreBusiness(ctx, AggRentObligation, o.ID)
	if e != nil {
		return e
	}
	if q.Domain != "individual-owner" || q.BusinessRef != AggRentObligation+":"+o.ID.String() || q.Currency != o.Currency || q.Jurisdiction != b.Country || q.AgreementRef != b.AgreementRef || q.AgreementVersion != b.AgreementVersion || q.PayerConsentRef == "" || b.PayerRef == "" || q.LegalPartyRef == "" || !slices.Contains(b.PayerTypes, q.PayerType) || !slices.Contains(b.Families, p.Key) {
		return precondition("LOCKED_QUOTE_CONTEXT_MISMATCH", "Use the signed business agreement and its actual tenant/payer and policy context.")
	}
	t, e := s.q.GetTenancy(ctx, s.dbq(ctx), o.TenancyID)
	if e != nil {
		return e
	}
	var acceptedOwner bool
	e = s.dbq(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agreement_participants WHERE agreement_id=$1 AND party_domain='individual-owner' AND party_ref_id=$2 AND role IN ('OWNER','LANDLORD','LESSOR','CO_OWNER'))`, t.AgreementID, b.OwnerID).Scan(&acceptedOwner)
	if e != nil {
		return e
	}
	if !acceptedOwner {
		return precondition("FINANCIAL_OWNER_ASSIGNMENT_REQUIRED", "Signed agreement must authorize the current owner; an ownership transfer cannot redirect an existing contract.")
	}
	if t.RentCollection != "PLATFORM" {
		return precondition("ACCEPTED_PLATFORM_TERMS_REQUIRED", "The signed agreement must authorize platform collection.")
	}
	a, e := s.q.GetAgreement(ctx, s.dbq(ctx), t.AgreementID)
	if e != nil {
		return e
	}
	if a.Status != "EFFECTIVE" || a.FullySignedAt == nil {
		return precondition("SIGNED_AGREEMENT_REQUIRED", "Payment cannot invent a tenant's agreement or acceptance.")
	}
	first := o.PeriodStart.Equal(t.StartDate) && a.Source != "RENEWAL"
	if !first && p.Key == "RESIDENTIAL_INITIAL" {
		return precondition("INITIAL_FEES_CANNOT_RECUR", "An existing tenancy installment cannot reuse an initial-fee policy.")
	}
	upfront := map[string]domain.LineItem{}
	recurringFees := map[string]domain.LineItem{}
	if b.AcceptedCost != nil {
		for _, line := range b.AcceptedCost.Lines {
			if !line.Mandatory || line.Refundable || line.Category == "RENT" || line.AmountMinor <= 0 {
				continue
			}
			if first && line.Timing == "UPFRONT" {
				upfront[line.Code] = line
			}
			if line.Timing == "RECURRING" {
				recurringFees[line.Code] = line
			}
		}
	}
	recurring := q
	recurring.Components = nil
	recurring.AmountMinor = o.AmountMinor
	seen := map[string]bool{}
	var principal, total int64
	for _, c := range q.Components {
		if c.Ref == "" || seen[c.Ref] || c.PayerRef != q.LegalPartyRef || c.AmountMinor <= 0 || c.AmountMinor > math.MaxInt64-total {
			return precondition("LOCKED_QUOTE_CONTEXT_MISMATCH", "Accepted components are invalid.")
		}
		seen[c.Ref] = true
		total += c.AmountMinor
		if c.Kind == "RENT" || p.Key == "LAND_LEASE" && c.Kind == "OCCUPATION" {
			principal += c.AmountMinor
		}
		switch c.Recurrence {
		case "ONE_TIME", "ACTUAL_SERVICE_ONLY":
			line, ok := upfront[c.Ref]
			if !first || !ok || line.AmountMinor != c.AmountMinor || !slices.Contains([]string{"LEGAL", "ONBOARDING", "PLATFORM", "SERVICE"}, c.Kind) || c.Kind == "LEGAL" && c.ServiceEvidenceRef == "" {
				return precondition("INITIAL_FEES_CANNOT_RECUR", "One-time fees require the initial accepted cost line; actual legal work needs its evidence.")
			}
			delete(upfront, c.Ref)
		default:
			if p.Key == "LAND_LEASE" && c.Kind == "OCCUPATION" && (c.Recurrence == "EVENT_DRIVEN" || c.Recurrence == "RECURRING") {
				c.Kind = "RENT"
				c.Recurrence = "RECURRING"
			}
			if c.Kind != "RENT" {
				line, ok := recurringFees[c.Ref]
				expectedKind := map[string]string{"SERVICE_CHARGE": "SERVICE", "UTILITY": "UTILITY", "TAX": "TAX", "PROCESSING": "PROCESSING"}[line.Category]
				if !ok || line.AmountMinor != c.AmountMinor || expectedKind != c.Kind {
					return precondition("ACCEPTED_RECURRING_FEE_DISCLOSURE_REQUIRED", "Recurring fees must retain their signed cost line, classification and amount.")
				}
				delete(recurringFees, c.Ref)
			}
			recurring.Components = append(recurring.Components, c)
		}
	}
	if total != q.AmountMinor || len(upfront) != 0 || len(recurringFees) != 0 || principal != o.AmountMinor-t.RecurringFeesMinor || !acceptedRentComponents(recurring, o.AmountMinor) {
		return precondition("LOCKED_QUOTE_CONTEXT_MISMATCH", "Rent principal, recurring fees and initial accepted charges must match exactly; deposit stays separate.")
	}
	return nil
}
