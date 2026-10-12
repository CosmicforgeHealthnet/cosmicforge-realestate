package usecases

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"slices"
	"strconv"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// A financial command's context is derived from an existing accepted Owner
// aggregate. It is not a generic caller-supplied amount or approval.
type CoreBusinessContext struct {
	AcceptedCost      *domain.Cost `json:"accepted_cost,omitempty"`
	Country           string       `json:"country"`
	Type              string       `json:"business_type"`
	ID                ids.ID       `json:"business_id"`
	PropertyID        ids.ID       `json:"property_id"`
	OwnerID           ids.ID       `json:"owner_account_id"`
	AmountMinor       int64        `json:"amount_minor"`
	Currency          string       `json:"currency"`
	PayerRef          string       `json:"payer_source_ref"`
	PayerTypes        []string     `json:"payer_types"`
	Families          []string     `json:"policy_families"`
	AgreementRef      string       `json:"agreement_ref"`
	AgreementVersion  string       `json:"agreement_version"`
	ReceivingPartyRef string       `json:"receiving_party_ref,omitempty"`
	Status            string       `json:"status"`
	Version           int64        `json:"version"`
	IntentRef         *string      `json:"payment_intent_ref,omitempty"`
}

func (s *Service) coreBusiness(ctx context.Context, kind string, id ids.ID) (CoreBusinessContext, authz.Capability, error) {
	b := CoreBusinessContext{Type: kind, ID: id}
	var payer *ids.ID
	var cap authz.Capability
	var contract ids.ID
	var contractVersion int64
	var e error
	var cost []byte
	switch kind {
	case AggRentObligation:
		cap = authz.CapTenancyManage
		var agreementKind string
		e = s.dbq(ctx).QueryRow(ctx, `SELECT o.property_id,o.amount_minor,o.currency,o.status,o.version,o.payment_intent_ref,t.customer_ref_id,a.id,a.current_version_no,a.agreement_kind FROM rent_obligations o JOIN tenancies t ON t.id=o.tenancy_id JOIN agreements a ON a.id=t.agreement_id WHERE o.id=$1 AND o.kind='RENT'`, id).Scan(&b.PropertyID, &b.AmountMinor, &b.Currency, &b.Status, &b.Version, &b.IntentRef, &payer, &contract, &contractVersion, &agreementKind)
		b.PayerTypes = []string{"TENANT"}
		b.Families = []string{"RESIDENTIAL_INITIAL", "RESIDENTIAL_SUBSEQUENT", "TENANCY_RENEWAL"}
		if agreementKind == "COMMERCIAL_LEASE" {
			b.PayerTypes = []string{"LESSEE", "CORPORATE_LESSEE"}
			b.Families = []string{"COMMERCIAL_LEASE"}
		}
		if agreementKind == "LAND_LEASE" {
			b.PayerTypes = []string{"LESSEE"}
			b.Families = []string{"LAND_LEASE"}
		}
	case "deposit_obligation":
		cap = authz.CapTenancyManage
		e = s.dbq(ctx).QueryRow(ctx, `SELECT d.property_id,d.amount_minor,d.currency,d.status,d.version,d.payment_intent_ref,t.customer_ref_id,a.id,a.current_version_no FROM deposit_obligations d JOIN tenancies t ON t.id=d.tenancy_id JOIN agreements a ON a.id=t.agreement_id WHERE d.id=$1 AND t.deposit_handling='PLATFORM'`, id).Scan(&b.PropertyID, &b.AmountMinor, &b.Currency, &b.Status, &b.Version, &b.IntentRef, &payer, &contract, &contractVersion)
		b.PayerTypes = []string{"TENANT"}
		b.Families = []string{"RESIDENTIAL_DEPOSIT"}
	case "closing_milestone":
		cap = authz.CapSaleManage
		var saleKind string
		e = s.dbq(ctx).QueryRow(ctx, `SELECT s.property_id,m.amount_minor,s.currency,CASE WHEN s.status IN ('CANCELLED','FAILED','DISPUTED') THEN s.status ELSE m.status END,m.version,m.payment_intent_ref,s.buyer_ref_id,s.id,s.version,s.sale_kind FROM closing_milestones m JOIN sale_transactions s ON s.id=m.sale_id WHERE m.id=$1 AND m.kind='PAYMENT' AND m.settlement='PLATFORM' AND s.status NOT IN ('INQUIRY','OFFERING')`, id).Scan(&b.PropertyID, &b.AmountMinor, &b.Currency, &b.Status, &b.Version, &b.IntentRef, &payer, &contract, &contractVersion, &saleKind)
		b.PayerTypes = []string{"BUYER"}
		b.Families = []string{"PROPERTY_PURCHASE"}
		if saleKind == "LAND" {
			b.Families = []string{"LAND_PURCHASE"}
		}
	case AggBooking:
		cap = authz.CapShortletBookings
		var pricing ids.ID
		e = s.dbq(ctx).QueryRow(ctx, `SELECT property_id,total_minor,currency,status,version,payment_intent_ref,customer_ref_id,pricing_version_id,cost_snapshot FROM shortlet_bookings WHERE id=$1 AND payment_collection='PLATFORM'`, id).Scan(&b.PropertyID, &b.AmountMinor, &b.Currency, &b.Status, &b.Version, &b.IntentRef, &payer, &pricing, &cost)
		contract = id
		b.AgreementVersion = pricing.String()
		b.PayerTypes = []string{"GUEST"}
		b.Families = []string{"SHORT_LET"}
	case aggExtension:
		cap = authz.CapShortletBookings
		var pricing ids.ID
		e = s.dbq(ctx).QueryRow(ctx, `SELECT e.property_id,e.amount_minor,e.currency,e.status,e.version,e.payment_intent_ref,b.customer_ref_id,e.pricing_version_id,e.cost_snapshot FROM shortlet_booking_extensions e JOIN shortlet_bookings b ON b.id=e.booking_id WHERE e.id=$1 AND e.payment_collection='PLATFORM'`, id).Scan(&b.PropertyID, &b.AmountMinor, &b.Currency, &b.Status, &b.Version, &b.IntentRef, &payer, &pricing, &cost)
		contract = id
		b.AgreementVersion = pricing.String()
		b.PayerTypes = []string{"GUEST"}
		b.Families = []string{"SHORT_LET"}
	case "maintenance_job":
		cap = authz.CapMaintenanceApprove
		var provider *ids.ID
		e = s.dbq(ctx).QueryRow(ctx, `SELECT r.property_id,j.amount_minor,j.currency,j.status,j.version,j.payment_intent_ref,j.provider_ref,j.quote_id FROM maintenance_jobs j JOIN maintenance_requests r ON r.id=j.request_id WHERE j.id=$1 AND j.payment_collection='PLATFORM'`, id).Scan(&b.PropertyID, &b.AmountMinor, &b.Currency, &b.Status, &b.Version, &b.IntentRef, &provider, &contract)
		if provider != nil {
			b.ReceivingPartyRef = "artisan:" + provider.String()
		}
		b.PayerTypes = []string{"OWNER"}
		b.Families = []string{"MAINTENANCE"}
		b.AgreementVersion = contract.String()
	case AggAppointment:
		cap = authz.CapProfessionals
		var provider ids.ID
		e = s.dbq(ctx).QueryRow(ctx, `SELECT COALESCE(a.property_id,s.property_id),a.quote_amount_minor,a.currency,a.status,a.version,a.payment_intent_ref,a.professional_ref FROM professional_appointments a LEFT JOIN sale_transactions s ON s.id=a.sale_id WHERE a.id=$1 AND a.fee_collection='PLATFORM' AND a.status NOT IN ('REQUESTED','QUOTING')`, id).Scan(&b.PropertyID, &b.AmountMinor, &b.Currency, &b.Status, &b.Version, &b.IntentRef, &provider)
		b.ReceivingPartyRef = "professional-services:" + provider.String()
		b.PayerTypes = []string{"OWNER"}
		b.Families = []string{"PROFESSIONAL_SERVICE"}
		contract = id
		contractVersion = b.Version
	default:
		return b, cap, precondition("FINANCIAL_BUSINESS_TYPE_UNSUPPORTED", "Use a supported accepted Owner business workflow.")
	}
	if isNoRows(e) {
		return b, cap, notFound(kind)
	}
	if e != nil {
		return b, cap, e
	}
	if kind == AggRentObligation {
		var raw []byte
		read := s.dbq(ctx).QueryRow(ctx, `SELECT o.cost_snapshot FROM agreements a JOIN rental_offers o ON o.id=a.offer_id WHERE a.id=$1`, contract).Scan(&raw)
		if read != nil && !isNoRows(read) {
			return b, cap, read
		}
		if read == nil {
			cost = raw
		}
	}
	p, e := s.q.GetProperty(ctx, s.dbq(ctx), b.PropertyID)
	if e != nil {
		return b, cap, e
	}
	if len(cost) > 0 {
		var accepted domain.Cost
		if e = json.Unmarshal(cost, &accepted); e != nil {
			return b, cap, e
		}
		b.AcceptedCost = &accepted
	}
	b.OwnerID = p.OwnerAccountID
	if e = s.dbq(ctx).QueryRow(ctx, `SELECT country_code FROM property_addresses WHERE property_id=$1`, b.PropertyID).Scan(&b.Country); e != nil {
		return b, cap, e
	}
	b.AgreementRef = contract.String()
	if b.AgreementVersion == "" {
		b.AgreementVersion = strconv.FormatInt(contractVersion, 10)
	}
	if payer != nil {
		b.PayerRef = "marketplace:" + payer.String()
	} else if kind == "maintenance_job" || kind == AggAppointment {
		b.PayerRef = "individual-owner:" + b.OwnerID.String()
	}
	return b, cap, nil
}

func (s *Service) OwnedCoreBusiness(ctx context.Context, subj authz.Subject, kind string, id ids.ID) (CoreBusinessContext, error) {
	b, cap, e := s.coreBusiness(ctx, kind, id)
	if e != nil {
		return b, e
	}
	if _, e = s.authorize(ctx, subj, authz.Do(cap), b.PropertyID); e != nil {
		return b, e
	}
	// Managers/representatives may retain their existing domain capabilities,
	// but cannot choose the payer, recipient or payout of a financial command.
	if accountOf(subj) != b.OwnerID {
		return b, precondition("FINANCIAL_OWNER_AUTHORITY_REQUIRED", "The recorded Owner must authorize this financial workflow.")
	}
	return b, nil
}

func validateCoreQuote(b CoreBusinessContext, q payment.AcceptedQuote, p payment.CollectionPolicy) error {
	if q.ID.ValidateV7() != nil || q.Domain != "individual-owner" || q.BusinessRef != b.Type+":"+b.ID.String() || q.Currency != b.Currency || !strings.HasPrefix(q.Jurisdiction, b.Country) || q.AmountMinor != b.AmountMinor || q.AgreementRef != b.AgreementRef || q.AgreementVersion != b.AgreementVersion || b.PayerRef == "" || q.PayerConsentRef == "" || !slices.Contains(b.PayerTypes, q.PayerType) || !slices.Contains(b.Families, p.Key) {
		return precondition("LOCKED_QUOTE_CONTEXT_MISMATCH", "Quote, accepted contract, payer and policy must match the authoritative business record.")
	}
	var total int64
	for _, c := range q.Components {
		if c.AmountMinor <= 0 || c.AmountMinor > q.AmountMinor-total || c.PayerRef != q.LegalPartyRef {
			return precondition("LOCKED_QUOTE_CONTEXT_MISMATCH", "Invalid accepted component context.")
		}
		total += c.AmountMinor
	}
	if total != q.AmountMinor {
		return precondition("LOCKED_QUOTE_CONTEXT_MISMATCH", "Accepted components must reconcile exactly.")
	}
	if b.AcceptedCost != nil && (b.Type == AggBooking || b.Type == aggExtension) {
		var principal, deposit int64
		expected := map[string]domain.LineItem{}
		for _, line := range b.AcceptedCost.Lines {
			if line.AmountMinor <= 0 {
				continue
			}
			if line.Category == "ACCOMMODATION" {
				principal += line.AmountMinor
			} else if line.Refundable {
				deposit += line.AmountMinor
			} else if line.Mandatory {
				expected[line.Code] = line
			}
		}
		var quotedPrincipal, quotedDeposit int64
		for _, c := range q.Components {
			switch c.Kind {
			case "ACCOMMODATION":
				quotedPrincipal += c.AmountMinor
			case "DEPOSIT":
				if c.Recurrence != "REFUNDABLE_DEPOSIT" {
					return precondition("SEPARATE_DEPOSIT_REQUIRED", "A refundable stay deposit cannot become revenue.")
				}
				quotedDeposit += c.AmountMinor
			default:
				line, ok := expected[c.Ref]
				if !ok || line.AmountMinor != c.AmountMinor {
					return precondition("UNDISCLOSED_FINANCIAL_FEE", "Every stay fee must match its accepted price line.")
				}
				delete(expected, c.Ref)
			}
		}
		if principal != quotedPrincipal || deposit != quotedDeposit || len(expected) != 0 {
			return precondition("LOCKED_QUOTE_CONTEXT_MISMATCH", "Accommodation, fees and refundable deposits must retain their accepted separation.")
		}
	}
	if b.Type == "deposit_obligation" {
		for _, c := range q.Components {
			if c.Kind != "DEPOSIT" || c.Recurrence != "REFUNDABLE_DEPOSIT" {
				return precondition("SEPARATE_DEPOSIT_REQUIRED", "Residential deposit is a distinct protected obligation.")
			}
		}
	}
	return nil
}

func coreBusinessCanCollect(b CoreBusinessContext) error {
	allowed := map[string][]string{
		"deposit_obligation": {"REQUIRED"}, "closing_milestone": {"PENDING"}, AggBooking: {"PENDING_PAYMENT"}, aggExtension: {"PAYMENT_PENDING"}, "maintenance_job": {"ASSIGNED", "IN_PROGRESS", "COMPLETION_SUBMITTED", "REWORK_REQUESTED", "ACCEPTED"}, AggAppointment: {"APPOINTED", "IN_PROGRESS", "REPORT_SUBMITTED", "ACCEPTED"},
	}
	if !slices.Contains(allowed[b.Type], b.Status) {
		return precondition("ACCEPTED_FINANCIAL_CONTEXT_REQUIRED", fmt.Sprintf("Collection is unavailable for %s in %s.", b.Type, b.Status))
	}
	if b.AmountMinor <= 0 || b.PayerRef == "" {
		return precondition("AUTHORITATIVE_PAYER_REQUIRED", "An accepted amount and authenticated payer reference are required.")
	}
	return nil
}

func validateCorePayer(b CoreBusinessContext, q payment.AcceptedQuote, p payment.CorePayerContext) error {
	if p.WalletID != q.WalletID || p.CanonicalPartyRef != q.LegalPartyRef || p.Domain+":"+p.AccountID.String() != b.PayerRef || b.PayerRef == "" {
		return precondition("AUTHORITATIVE_PAYER_CONTEXT_MISMATCH", "The Core wallet must belong to the actual business payer; canonical legal-party identity is read from Core, never invented from a role.")
	}
	return nil
}
