package usecases

import (
	"context"
	"encoding/json"
	"math/big"
	"reflect"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

type FinancialCoreClient interface {
	CollectionClient
	WalletSnapshotClient
	Policy(context.Context, ids.ID) (payment.CollectionPolicy, error)
	Receipt(context.Context, ids.ID) (payment.CoreReceipt, error)
	Authority(context.Context, ids.ID) (payment.CoreAuthority, error)
	Allocate(context.Context, ids.ID, []payment.CoreLineInput, string) (payment.CoreAllocation, error)
	SubmitEvidence(context.Context, payment.CoreEvidence) error
	Release(context.Context, payment.CoreRelease, string) (ids.ID, error)
	Deposit(context.Context, ids.ID) (payment.CoreDeposit, error)
	OpenCase(context.Context, payment.CoreCaseRequest, string) (payment.CoreCase, error)
	Case(context.Context, ids.ID) (payment.CoreCase, error)
	Refund(context.Context, ids.ID) (payment.CoreRefund, error)
	Cancel(context.Context, payment.CoreCancellation, string) (payment.CollectionIntent, error)
	PayoutReferences(context.Context, ids.ID, ids.ID) ([]payment.CorePayoutReference, error)
	History(context.Context, ids.ID, ids.ID, ids.ID, int) ([]payment.CoreHistoryEntry, error)
	Availability(context.Context, ids.ID, ids.ID, ids.ID) (payment.CoreAvailability, error)
	Withdraw(context.Context, ids.ID, payment.CoreWithdrawal, string) (payment.CorePayout, error)
	Payout(context.Context, ids.ID, ids.ID, bool) (payment.CorePayout, error)
}

func (s *Service) coreClient() (FinancialCoreClient, error) {
	c, ok := s.walletRegistration.(FinancialCoreClient)
	if !ok {
		return nil, unavailable("Accepted financial workflow", "payment-service")
	}
	return c, nil
}
func (s *Service) hasCoreFinancial() bool { _, e := s.coreClient(); return e == nil }
func coreOwnerContext(ctx context.Context, account ids.ID) context.Context {
	return requestcontext.WithActor(ctx, requestcontext.Actor{Domain: "individual-owner", ID: account})
}

type CoreCollectionInput struct {
	BusinessType string `json:"business_type" v:"required,enum=rent_obligation|deposit_obligation|closing_milestone|shortlet_booking|shortlet_booking_extension|maintenance_job|professional_appointment"`
	AcceptedCollectionInput
}
type CoreCollectionEvidence struct {
	Business CoreBusinessContext
	Quote    payment.AcceptedQuote
	Intent   payment.CollectionIntent
	Policy   payment.CollectionPolicy
}
type CoreBinding struct {
	Business CoreBusinessContext      `json:"business"`
	Quote    payment.AcceptedQuote    `json:"quote"`
	QuoteID  ids.ID                   `json:"quote_id"`
	Intent   payment.CollectionIntent `json:"intent"`
}

func (s *Service) PrepareCoreCollection(ctx context.Context, subj authz.Subject, id ids.ID, in CoreCollectionInput) (CoreCollectionEvidence, error) {
	var out CoreCollectionEvidence
	if e := schema.Validate(in); e != nil {
		return out, e
	}
	if in.BusinessType == AggRentObligation {
		ev, e := s.PrepareAcceptedRentCollection(ctx, subj, id, in.AcceptedCollectionInput)
		if e != nil {
			return out, e
		}
		b, e := s.OwnedCoreBusiness(ctx, subj, AggRentObligation, id)
		return CoreCollectionEvidence{Business: b, Quote: ev.Quote, Intent: ev.Intent, Policy: ev.Policy}, e
	}
	b, e := s.OwnedCoreBusiness(ctx, subj, in.BusinessType, id)
	if e != nil {
		return out, e
	}
	if e = coreBusinessCanCollect(b); e != nil {
		return out, e
	}
	c, e := s.coreClient()
	if e != nil {
		return out, e
	}
	var bound ids.ID
	e = s.Pool.QueryRow(ctx, `SELECT quote_id FROM financial_core_business_bindings WHERE business_type=$1 AND business_id=$2`, b.Type, b.ID).Scan(&bound)
	if e == nil && bound != in.QuoteID {
		return out, precondition("PAYMENT_INTENT_ALREADY_BOUND", "Use the original accepted quote and read back its intent.")
	}
	if e != nil && !isNoRows(e) {
		return out, e
	}
	if b.IntentRef != nil && isNoRows(e) {
		return out, precondition("LEGACY_FINANCIAL_CONTEXT_REQUIRES_REVIEW", "Historical legacy references cannot be silently relabelled as Core intents.")
	}
	q, e := c.AcceptedQuote(coreOwnerContext(ctx, b.OwnerID), in.QuoteID)
	if e != nil {
		return out, downstream("payment-service", e)
	}
	receipt, e := c.Receipt(coreOwnerContext(ctx, b.OwnerID), q.ID)
	if e != nil {
		return out, downstream("payment-service", e)
	}
	if e = validateCorePayer(b, q, receipt.PayerContext); e != nil {
		return out, e
	}
	p, e := c.Policy(ctx, q.PolicyID)
	if e != nil {
		return out, downstream("payment-service", e)
	}
	if e = validateCoreQuote(b, q, p); e != nil {
		return out, e
	}
	var intent payment.CollectionIntent
	if receipt.IntentID != nil {
		intent, e = c.Collection(coreOwnerContext(ctx, b.OwnerID), *receipt.IntentID)
	} else {
		intent, e = c.CreateAcceptedCollection(ctx, b.OwnerID, q, in.PayerEmail)
	}
	if e != nil {
		return out, downstream("payment-service", e)
	}
	return CoreCollectionEvidence{Business: b, Quote: q, Intent: intent, Policy: p}, nil
}
func (s *Service) RecordCoreCollection(ctx context.Context, subj authz.Subject, id ids.ID, ev CoreCollectionEvidence) (payment.CollectionIntent, error) {
	if ev.Business.Type == AggRentObligation {
		return s.RecordAcceptedRentCollection(ctx, subj, id, AcceptedCollectionEvidence{Quote: ev.Quote, Intent: ev.Intent, Policy: ev.Policy})
	}
	b, e := s.OwnedCoreBusiness(ctx, subj, ev.Business.Type, id)
	if e != nil {
		return ev.Intent, e
	}
	if e = coreBusinessCanCollect(b); e != nil {
		return ev.Intent, e
	}
	// CommandPre made the external call. Recheck the exact source revision inside
	// the Owner transaction before binding; retain the original external key on retry.
	if b.Version != ev.Business.Version || b.AmountMinor != ev.Business.AmountMinor || b.AgreementRef != ev.Business.AgreementRef || b.AgreementVersion != ev.Business.AgreementVersion || b.PayerRef != ev.Business.PayerRef || b.OwnerID != ev.Business.OwnerID || ev.Intent.ID.ValidateV7() != nil || ev.Intent.AmountMinor != ev.Quote.AmountMinor || ev.Intent.Currency != b.Currency || ev.Intent.WalletID != ev.Quote.WalletID {
		return ev.Intent, precondition("ACCEPTED_FINANCIAL_CONTEXT_CHANGED", "Retain and reconcile the original Core intent; do not create a replacement charge.")
	}
	if b.IntentRef != nil && *b.IntentRef != ev.Intent.ID.String() {
		return ev.Intent, precondition("PAYMENT_INTENT_ALREADY_BOUND", "Existing payment reference retained.")
	}
	br, _ := json.Marshal(ev.Business)
	qr, _ := json.Marshal(ev.Quote)
	ir, _ := json.Marshal(ev.Intent)
	tag, e := mustTx(ctx).Exec(ctx, `INSERT INTO financial_core_business_bindings(business_type,business_id,property_id,owner_account_id,quote_id,intent_id,amount_minor,currency,business_context,quote_snapshot,intent_snapshot,observed_state,settlement_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(business_type,business_id) DO NOTHING`, b.Type, b.ID, b.PropertyID, b.OwnerID, ev.Quote.ID, ev.Intent.ID, b.AmountMinor, b.Currency, string(br), string(qr), string(ir), ev.Intent.State, ev.Intent.SettlementState)
	if e != nil {
		return ev.Intent, e
	}
	if tag.RowsAffected() == 0 {
		var same bool
		e = mustTx(ctx).QueryRow(ctx, `SELECT quote_id=$3 AND intent_id=$4 FROM financial_core_business_bindings WHERE business_type=$1 AND business_id=$2`, b.Type, b.ID, ev.Quote.ID, ev.Intent.ID).Scan(&same)
		if e != nil {
			return ev.Intent, e
		}
		if !same {
			return ev.Intent, precondition("PAYMENT_INTENT_ALREADY_BOUND", "Immutable Core binding already exists.")
		}
		return ev.Intent, nil
	}
	table := coreBusinessTable(b.Type)
	ref, status := ev.Intent.ID.String(), "FINANCIAL_CORE:"+ev.Intent.State
	_, e = mustTx(ctx).Exec(ctx, `UPDATE `+table+` SET payment_intent_ref=$2,last_payment_status=$3,last_checked_at=$4 WHERE id=$1`, b.ID, ref, status, s.now())
	if e != nil {
		return ev.Intent, e
	}
	e = s.record(ctx, "financial.core_collection_bound", b.Type, b.ID, map[string]any{"quote_id": ev.Quote.ID, "intent_id": ev.Intent.ID, "state": ev.Intent.State}, nil)
	return ev.Intent, e
}
func coreBusinessTable(kind string) string {
	return map[string]string{"deposit_obligation": "deposit_obligations", "closing_milestone": "closing_milestones", AggBooking: "shortlet_bookings", aggExtension: "shortlet_booking_extensions", "maintenance_job": "maintenance_jobs", AggAppointment: "professional_appointments"}[kind]
}
func (s *Service) coreBinding(ctx context.Context, kind string, id ids.ID) (CoreBinding, error) {
	var out CoreBinding
	var raw, quote []byte
	if kind == AggRentObligation {
		var business []byte
		e := s.dbq(ctx).QueryRow(ctx, `SELECT business_context,quote_id,quote_snapshot,snapshot||jsonb_build_object('state',observed_state,'settlement_state',settlement_state) FROM financial_core_collections WHERE obligation_id=$1 AND owner_account_id IS NOT NULL`, id).Scan(&business, &out.QuoteID, &quote, &raw)
		if isNoRows(e) {
			return out, precondition("HISTORICAL_CORE_BINDING_REVIEW_REQUIRED", "Preserved historical bindings need verified original issuer context; do not assign them to a later property owner.")
		}
		if e != nil {
			return out, e
		}
		if e = json.Unmarshal(business, &out.Business); e != nil {
			return out, e
		}
		if e = json.Unmarshal(quote, &out.Quote); e != nil {
			return out, e
		}
		e = json.Unmarshal(raw, &out.Intent)
		return out, e
	}
	var business []byte
	e := s.dbq(ctx).QueryRow(ctx, `SELECT business_context,quote_id,quote_snapshot,intent_snapshot||jsonb_build_object('state',observed_state,'settlement_state',settlement_state) FROM financial_core_business_bindings WHERE business_type=$1 AND business_id=$2`, kind, id).Scan(&business, &out.QuoteID, &quote, &raw)
	if isNoRows(e) {
		return out, notFound("financial_collection")
	}
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(business, &out.Business); e != nil {
		return out, e
	}
	if e = json.Unmarshal(quote, &out.Quote); e != nil {
		return out, e
	}
	e = json.Unmarshal(raw, &out.Intent)
	return out, e
}
func (s *Service) ownedCoreBinding(ctx context.Context, subj authz.Subject, kind string, id ids.ID) (CoreBinding, error) {
	b, e := s.coreBinding(ctx, kind, id)
	if e != nil {
		return b, e
	}
	if _, e = s.authorize(ctx, subj, authz.ActAccountRead, ids.Nil); e != nil {
		return b, e
	}
	if accountOf(subj) != b.Business.OwnerID {
		return CoreBinding{}, notFound("financial_collection")
	}
	return b, nil
}

func (s *Service) ownedCoreAction(ctx context.Context, subj authz.Subject, kind string, id ids.ID) (CoreBinding, error) {
	b, e := s.ownedCoreBinding(ctx, subj, kind, id)
	if e != nil {
		return b, e
	}
	current, e := s.OwnedCoreBusiness(ctx, subj, kind, id)
	if e != nil {
		return b, e
	}
	if current.OwnerID != b.Business.OwnerID {
		return b, precondition("FINANCIAL_OWNER_ASSIGNMENT_REQUIRED", "Original financial authority is retained after transfer; a verified assignment is required for new money commands.")
	}
	return b, nil
}

func validateCoreReceipt(b CoreBinding, r payment.CoreReceipt) error {
	if !reflect.DeepEqual(b.Quote, r.Quote) || r.IntentID == nil || *r.IntentID != b.Intent.ID || r.Quote.ID != b.QuoteID || r.Quote.WalletID != b.Intent.WalletID || r.Quote.BusinessRef != b.Business.Type+":"+b.Business.ID.String() || r.Quote.AmountMinor != b.Intent.AmountMinor || r.Quote.Currency != b.Intent.Currency {
		return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Core receipt differs from the immutable business binding.")
	}
	for _, a := range r.AwardAllocations {
		if a.PolicyID != r.Quote.PolicyID || a.PoolID != b.Intent.PoolID || a.Currency != b.Intent.Currency || a.LegalEntity != b.Intent.LegalEntity {
			return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Award must stay in its original policy, currency, entity and pool.")
		}
	}
	for _, a := range r.Allocations {
		if a.IntentID != b.Intent.ID || a.QuoteID != b.QuoteID || a.PolicyID != r.Quote.PolicyID || a.PoolID != b.Intent.PoolID || a.Currency != b.Intent.Currency || a.LegalEntity != b.Intent.LegalEntity {
			return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Allocation context changed.")
		}
	}
	return nil
}
func (s *Service) FinancialBusinessReceipt(ctx context.Context, subj authz.Subject, kind string, id ids.ID) (payment.CoreReceipt, error) {
	b, e := s.ownedCoreBinding(ctx, subj, kind, id)
	if e != nil {
		return payment.CoreReceipt{}, e
	}
	return s.readCoreReceipt(ctx, b)
}
func (s *Service) readCoreReceipt(ctx context.Context, b CoreBinding) (payment.CoreReceipt, error) {
	c, e := s.coreClient()
	if e != nil {
		return payment.CoreReceipt{}, e
	}
	r, e := c.Receipt(coreOwnerContext(ctx, b.Business.OwnerID), b.QuoteID)
	if e != nil {
		return r, downstream("payment-service", e)
	}
	return r, validateCoreReceipt(b, r)
}

type CoreAllocationRequest struct {
	BusinessType string                  `json:"business_type" v:"required"`
	Lines        []payment.CoreLineInput `json:"lines" v:"required,max=40"`
}

func (s *Service) AllocateCoreBusiness(ctx context.Context, subj authz.Subject, id ids.ID, in CoreAllocationRequest, key string) (payment.CoreAllocation, error) {
	b, e := s.ownedCoreAction(ctx, subj, in.BusinessType, id)
	if e != nil {
		return payment.CoreAllocation{}, e
	}
	c, e := s.coreClient()
	if e != nil {
		return payment.CoreAllocation{}, e
	}
	r, e := s.readCoreReceipt(ctx, b)
	if e != nil {
		return payment.CoreAllocation{}, e
	}
	if e = s.validateCoreBeneficiaries(ctx, b, r.Quote, in.Lines); e != nil {
		return payment.CoreAllocation{}, e
	}
	a, e := c.Allocate(coreOwnerContext(ctx, b.Business.OwnerID), b.Intent.ID, in.Lines, key)
	if e != nil {
		return a, downstream("payment-service", e)
	}
	if a.IntentID != b.Intent.ID || a.QuoteID != b.QuoteID || a.Currency != b.Intent.Currency || a.PoolID != b.Intent.PoolID || a.LegalEntity != b.Intent.LegalEntity {
		return a, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Allocation response mismatch.")
	}
	return a, nil
}
func (s *Service) validateCoreBeneficiaries(ctx context.Context, b CoreBinding, q payment.AcceptedQuote, lines []payment.CoreLineInput) error {
	c, e := s.coreClient()
	if e != nil {
		return e
	}
	if len(lines) == 0 {
		return precondition("APPROVED_BENEFICIARY_REQUIRED", "An exact authorized allocation is required.")
	}
	for _, l := range lines {
		a, e := c.Authority(coreOwnerContext(ctx, b.Business.OwnerID), l.AuthorityID)
		if e != nil {
			return downstream("payment-service", e)
		}
		if !a.Active || a.BusinessRef != q.BusinessRef || a.Currency != q.Currency || a.LegalEntity != b.Intent.LegalEntity || l.Currency != q.Currency || l.AmountMinor <= 0 || l.AmountMinor > a.MaxMinor {
			return precondition("APPROVED_BENEFICIARY_REQUIRED", "Authority does not match this business and amount.")
		}
		var component *payment.CoreComponent
		for i := range q.Components {
			if q.Components[i].Ref == l.ComponentRef {
				component = &q.Components[i]
			}
		}
		if component == nil || !slices.Contains(a.Components, component.Kind) && !(component.Kind == "RENT" && slices.Contains([]string{"MANAGER", "MANAGEMENT_COMPANY"}, a.Role) && slices.Contains(a.Components, "MANAGER_FEE")) {
			return precondition("APPROVED_BENEFICIARY_REQUIRED", "Authority lacks the accepted component.")
		}
		if b.Business.ReceivingPartyRef != "" && !slices.Contains([]string{"ARTISAN", "PROFESSIONAL", "PROFESSIONAL_FIRM"}, a.Role) {
			return precondition("PROVIDER_ENTITLEMENT_REQUIRED", "Accepted provider fees cannot be redirected to an owner, manager or platform.")
		}
		payableTo := ""
		if b.Business.AcceptedCost != nil {
			for _, fee := range b.Business.AcceptedCost.Lines {
				if fee.Code == component.Ref || fee.Category == "ACCOMMODATION" && component.Kind == "ACCOMMODATION" {
					payableTo = fee.PayableTo
				}
			}
		}
		if payableTo != "" && component.Kind != "DEPOSIT" && !(component.Kind == "RENT" && slices.Contains([]string{"MANAGER", "MANAGEMENT_COMPANY"}, a.Role)) {
			if payableTo == "OWNER" && a.Role != "OWNER" && a.Role != "CO_OWNER" || payableTo == "PLATFORM" && a.Role != "PLATFORM" || payableTo == "THIRD_PARTY" && (a.Role == "OWNER" || a.Role == "CO_OWNER" || a.Role == "PLATFORM") {
				return precondition("ACCEPTED_FEE_BENEFICIARY_REQUIRED", "The beneficiary must retain the accepted price line's disclosed recipient.")
			}
		}
		switch a.Role {
		case "OWNER", "CO_OWNER":
			party := a.BeneficiaryAccountID
			if a.Domain != "individual-owner" || a.BeneficiaryDomain != "individual-owner" || party.ValidateV7() != nil {
				return precondition("APPROVED_BENEFICIARY_REQUIRED", "Owner legal party does not match.")
			}
			var share *int32
			e = s.Pool.QueryRow(ctx, `SELECT p.share_bps FROM ownership_parties p JOIN ownership_records r ON r.id=p.ownership_record_id WHERE p.property_id=$1 AND p.account_id=$2 AND p.party_role IN ('OWNER','CO_OWNER','BENEFICIAL_OWNER') AND p.status='ACTIVE' AND r.status='VERIFIED' AND (r.expires_at IS NULL OR r.expires_at>$3) AND (p.valid_until IS NULL OR p.valid_until>$3) LIMIT 1`, b.Business.PropertyID, party, s.now()).Scan(&share)
			if (isNoRows(e) || e == nil && share == nil) && party == b.Business.OwnerID {
				var sole bool
				e = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ownership_records WHERE property_id=$1 AND is_primary AND status='VERIFIED' AND claim_type='SOLE' AND (expires_at IS NULL OR expires_at>$2)) AND NOT EXISTS(SELECT 1 FROM ownership_parties WHERE property_id=$1 AND account_id<>$3 AND status='ACTIVE' AND party_role IN ('OWNER','CO_OWNER','BENEFICIAL_OWNER') AND (valid_until IS NULL OR valid_until>$2))`, b.Business.PropertyID, s.now(), b.Business.OwnerID).Scan(&sole)
				if e == nil && sole {
					v := int32(10000)
					share = &v
				}
			}
			if e != nil || share == nil || int(*share) != a.OwnershipBPS {
				return precondition("VERIFIED_OWNERSHIP_REQUIRED", "Core percentages must match current verified Owner ownership records.")
			}
		case "CUSTOMER":
			if l.Kind != "DEPOSIT" || component.Kind != "DEPOSIT" || a.LegalPartyRef != q.LegalPartyRef || a.WalletID == nil || *a.WalletID != q.WalletID {
				return precondition("SEPARATE_DEPOSIT_REQUIRED", "Deposit must remain the payer's protected liability.")
			}
		case "ARTISAN", "PROFESSIONAL", "PROFESSIONAL_FIRM":
			if b.Business.ReceivingPartyRef == "" || a.BeneficiaryDomain+":"+a.BeneficiaryAccountID.String() != b.Business.ReceivingPartyRef {
				return precondition("PROVIDER_ENTITLEMENT_REQUIRED", "Beneficiary must be the accepted job or mandate provider.")
			}
		case "MANAGER", "MANAGEMENT_COMPANY":
			var feeType string
			var rate *int32
			var amount *int64
			var currency *string
			var mandate ids.ID
			e = s.Pool.QueryRow(ctx, `SELECT m.id,p.fee_type,p.fee_bps,p.fee_amount_minor,p.fee_currency FROM manager_assignments m JOIN manager_proposals p ON p.id=m.proposal_id WHERE m.property_id=$1 AND m.id::text=$2 AND m.status IN ('ACTIVE','EXPIRING') AND m.starts_at<=$3 AND m.ends_at>$3 AND m.manager_domain||':'||m.manager_ref_id::text=$4 AND p.status='ACCEPTED'`, b.Business.PropertyID, a.MandateRef, s.now(), a.BeneficiaryDomain+":"+a.BeneficiaryAccountID.String()).Scan(&mandate, &feeType, &rate, &amount, &currency)
			valid := false
			if e == nil && feeType == "PER_TASK" && component.Kind == "BOOKING" && amount != nil && currency != nil {
				valid = *amount == l.AmountMinor && *currency == l.Currency
			}
			if e == nil && feeType == "PERCENT_OF_RENT" && component.Kind == "RENT" && b.Business.Type == AggRentObligation && rate != nil {
				var covered bool
				e = s.Pool.QueryRow(ctx, `SELECT m.starts_at<=o.period_start AND m.ends_at>=o.period_end FROM rent_obligations o CROSS JOIN manager_assignments m WHERE o.id=$1 AND m.id=$2`, b.Business.ID, mandate).Scan(&covered)
				var principal int64
				for _, c := range q.Components {
					if c.Kind == "RENT" {
						principal += c.AmountMinor
					}
				}
				fee := new(big.Int).Mul(big.NewInt(principal), big.NewInt(int64(*rate)))
				remainder := new(big.Int)
				fee.QuoRem(fee, big.NewInt(10000), remainder)
				valid = e == nil && covered && fee.IsInt64() && remainder.Sign() == 0 && fee.Int64() == l.AmountMinor
			}
			if !valid {
				return precondition("MANAGER_FEE_MANDATE_REQUIRED", "The accepted fee, manager identity and full business service period must match; no operational delegation or unrecorded billing period grants financial entitlement.")
			}
		case "PLATFORM":
			if l.Kind != "PLATFORM" || !slices.Contains([]string{"PLATFORM", "ONBOARDING", "BOOKING", "PROCESSING"}, component.Kind) {
				return precondition("DISCLOSED_PLATFORM_FEE_REQUIRED", "No hidden owner-rent deduction is permitted.")
			}
		default:
			return precondition("BENEFICIARY_WORKFLOW_UNAVAILABLE", "This beneficiary requires its authoritative domain workflow.")
		}
	}
	return nil
}

type CoreResourceInput struct {
	BusinessType string `json:"business_type" v:"required"`
	Type         string `json:"resource_type" v:"required,enum=DEPOSIT|CASE|REFUND"`
	ID           ids.ID `json:"resource_id" v:"required"`
}
type CoreResourceEvidence struct {
	Binding  CoreBinding
	Type     string
	ID       ids.ID
	State    string
	Snapshot json.RawMessage
}

func (s *Service) PrepareCoreResource(ctx context.Context, subj authz.Subject, id ids.ID, in CoreResourceInput) (CoreResourceEvidence, error) {
	b, e := s.ownedCoreBinding(ctx, subj, in.BusinessType, id)
	if e != nil {
		return CoreResourceEvidence{}, e
	}
	return s.readCoreResource(ctx, b, in.Type, in.ID)
}
func (s *Service) readCoreResource(ctx context.Context, b CoreBinding, kind string, id ids.ID) (CoreResourceEvidence, error) {
	out := CoreResourceEvidence{Binding: b, Type: kind, ID: id}
	c, e := s.coreClient()
	if e != nil {
		return out, e
	}
	ctx = coreOwnerContext(ctx, b.Business.OwnerID)
	r, e := s.readCoreReceipt(ctx, b)
	if e != nil {
		return out, e
	}
	switch kind {
	case "DEPOSIT":
		d, e := c.Deposit(ctx, id)
		if e != nil {
			return out, downstream("payment-service", e)
		}
		if d.IntentID != b.Intent.ID || d.QuoteID != b.QuoteID || d.BusinessRef != r.Quote.BusinessRef || d.Currency != b.Intent.Currency {
			return out, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Deposit belongs to another business.")
		}
		out.State = d.State
		out.Snapshot, _ = json.Marshal(d)
	case "CASE":
		v, e := c.Case(ctx, id)
		if e != nil {
			return out, downstream("payment-service", e)
		}
		if v.IntentID != b.Intent.ID || v.QuoteID != b.QuoteID || v.BusinessRef != r.Quote.BusinessRef || v.Currency != b.Intent.Currency {
			return out, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Case belongs to another business.")
		}
		out.State = v.State
		out.Snapshot, _ = json.Marshal(v)
	case "REFUND":
		v, e := c.Refund(ctx, id)
		if e != nil {
			return out, downstream("payment-service", e)
		}
		if v.IntentID != b.Intent.ID || v.WalletID != b.Intent.WalletID || v.ProviderID != b.Intent.ProviderID || v.PoolID != b.Intent.PoolID || v.Currency != b.Intent.Currency || v.AmountMinor <= 0 {
			return out, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Refund must retain its original intent, payer and provider.")
		}
		var componentKind, componentRef string
		for _, a := range r.Allocations {
			for _, l := range a.Lines {
				if l.ID == v.LineID {
					for _, component := range r.Quote.Components {
						if component.Ref == l.ComponentRef {
							componentKind = component.Kind
							componentRef = component.Ref
						}
					}
				}
			}
		}
		if componentKind == "" {
			return out, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Refund line is not in the bound quote.")
		}
		out.State = v.State
		out.Snapshot, _ = json.Marshal(struct {
			payment.CoreRefund
			ComponentKind string `json:"component_kind"`
			ComponentRef  string `json:"component_ref"`
		}{v, componentKind, componentRef})
	default:
		return out, precondition("FINANCIAL_RESOURCE_UNSUPPORTED", "Use a published Core deposit, case or refund reference.")
	}
	return out, nil
}
func (s *Service) RecordCoreResource(ctx context.Context, subj authz.Subject, id ids.ID, ev CoreResourceEvidence) (CoreResourceEvidence, error) {
	b, e := s.ownedCoreBinding(ctx, subj, ev.Binding.Business.Type, id)
	if e != nil {
		return ev, e
	}
	if b.Intent.ID != ev.Binding.Intent.ID {
		return ev, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Binding changed.")
	}
	e = s.storeCoreResource(ctx, ev)
	return ev, e
}
func (s *Service) storeCoreResource(ctx context.Context, ev CoreResourceEvidence) error {
	var priorIntent ids.ID
	var prior []byte
	e := mustTx(ctx).QueryRow(ctx, `SELECT intent_id,snapshot FROM financial_core_resource_links WHERE resource_type=$1 AND resource_id=$2 FOR UPDATE`, ev.Type, ev.ID).Scan(&priorIntent, &prior)
	if e == nil {
		if priorIntent != ev.Binding.Intent.ID {
			return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Resource identity immutable.")
		}
		_, e = mustTx(ctx).Exec(ctx, `UPDATE financial_core_resource_links SET state=$3,updated_at=$4 WHERE resource_type=$1 AND resource_id=$2`, ev.Type, ev.ID, ev.State, s.now())
		return e
	}
	if !isNoRows(e) {
		return e
	}
	_, e = mustTx(ctx).Exec(ctx, `INSERT INTO financial_core_resource_links(resource_type,resource_id,intent_id,snapshot,state,updated_at) VALUES($1,$2,$3,$4,$5,$6)`, ev.Type, ev.ID, ev.Binding.Intent.ID, string(ev.Snapshot), ev.State, s.now())
	return e
}

type canonicalFinancialProof struct{}

func (s *Service) ReconcileCoreBusiness(ctx context.Context, subj authz.Subject, kind string, id ids.ID) (payment.CoreReceipt, error) {
	b, e := s.ownedCoreBinding(ctx, subj, kind, id)
	if e != nil {
		return payment.CoreReceipt{}, e
	}
	return s.reconcileCoreBinding(ctx, b)
}
func (s *Service) reconcileCoreBinding(ctx context.Context, b CoreBinding) (payment.CoreReceipt, error) {
	claim := ids.New()
	now := s.now()
	tag, e := s.Pool.Exec(ctx, `INSERT INTO financial_core_readback_leases(intent_id,claim_id,expires_at,last_attempt_at) VALUES($1,$2,$3,$4) ON CONFLICT(intent_id) DO UPDATE SET claim_id=EXCLUDED.claim_id,expires_at=EXCLUDED.expires_at,last_attempt_at=EXCLUDED.last_attempt_at WHERE financial_core_readback_leases.expires_at IS NULL OR financial_core_readback_leases.expires_at<=$4`, b.Intent.ID, claim, now.Add(2*time.Minute), now)
	if e != nil {
		return payment.CoreReceipt{}, e
	}
	if tag.RowsAffected() == 0 {
		return payment.CoreReceipt{}, precondition("CORE_READBACK_IN_PROGRESS", "Original readback is already running; retry without a new financial operation.")
	}
	defer func() {
		_, _ = s.Pool.Exec(context.WithoutCancel(ctx), `UPDATE financial_core_readback_leases SET expires_at=NULL WHERE intent_id=$1 AND claim_id=$2`, b.Intent.ID, claim)
	}()
	r, e := s.readCoreReceipt(ctx, b)
	if e != nil {
		return r, e
	}
	rows, e := s.Pool.Query(ctx, `SELECT resource_type,resource_id FROM financial_core_resource_links WHERE intent_id=$1 ORDER BY resource_type,resource_id`, b.Intent.ID)
	if e != nil {
		return r, e
	}
	type resource struct {
		kind string
		id   ids.ID
	}
	var links []resource
	for rows.Next() {
		var x resource
		if e = rows.Scan(&x.kind, &x.id); e != nil {
			rows.Close()
			return r, e
		}
		links = append(links, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return r, e
	}
	var evidence []CoreResourceEvidence
	for _, x := range links {
		ev, e := s.readCoreResource(ctx, b, x.kind, x.id)
		if e != nil {
			return r, e
		}
		evidence = append(evidence, ev)
	}
	e = s.InTx(ctx, func(ctx context.Context) error {
		var lease ids.ID
		if e := mustTx(ctx).QueryRow(ctx, `SELECT claim_id FROM financial_core_readback_leases WHERE intent_id=$1 FOR UPDATE`, b.Intent.ID).Scan(&lease); e != nil {
			return e
		}
		if lease != claim {
			return precondition("STALE_CORE_READBACK", "A later observation owns this readback.")
		}
		for _, ev := range evidence {
			if e := s.storeCoreResource(ctx, ev); e != nil {
				return e
			}
		}
		if b.Business.Type == AggRentObligation {
			_, e = mustTx(ctx).Exec(ctx, `UPDATE financial_core_collections SET observed_state=$2,settlement_state=$3,updated_at=$4 WHERE intent_id=$1`, b.Intent.ID, r.CollectionState, r.SettlementState, s.now())
		} else {
			_, e = mustTx(ctx).Exec(ctx, `UPDATE financial_core_business_bindings SET observed_state=$2,settlement_state=$3,updated_at=$4 WHERE intent_id=$1`, b.Intent.ID, r.CollectionState, r.SettlementState, s.now())
		}
		if e != nil {
			return e
		}
		if e = s.storeCoreLines(ctx, b, r); e != nil {
			return e
		}
		return s.projectCoreReceipt(ctx, b, r)
	})
	return r, e
}
func (s *Service) storeCoreLines(ctx context.Context, b CoreBinding, r payment.CoreReceipt) error {
	for _, group := range []struct {
		awards bool
		allocs []payment.CoreAllocation
	}{{false, r.Allocations}, {true, r.AwardAllocations}} {
		for _, a := range group.allocs {
			for _, line := range a.Lines {
				raw, _ := json.Marshal(line)
				_, e := mustTx(ctx).Exec(ctx, `INSERT INTO financial_core_allocation_lines(line_id,original_intent_id,allocation_id,award,snapshot) VALUES($1,$2,$3,$4,$5) ON CONFLICT(line_id) DO NOTHING`, line.ID, b.Intent.ID, a.ID, group.awards, string(raw))
				if e != nil {
					return e
				}
				var valid bool
				if e = mustTx(ctx).QueryRow(ctx, `SELECT original_intent_id=$2 AND allocation_id=$3 AND award=$4 FROM financial_core_allocation_lines WHERE line_id=$1`, line.ID, b.Intent.ID, a.ID, group.awards).Scan(&valid); e != nil {
					return e
				}
				if !valid {
					return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Line identity changed.")
				}
			}
		}
	}
	return nil
}
func (s *Service) projectCoreReceipt(ctx context.Context, b CoreBinding, r payment.CoreReceipt) error {
	// Readback plus exact matched settlement and allocation proof, never a
	// checkout callback or collected event alone. Partial captures are not
	// invented: the published Core currently verifies each locked amount in full.
	in := payment.Intent{Ref: b.Intent.ID.String(), AmountMinor: b.Business.AmountMinor, Status: payment.IntentProcessing, UpdatedAt: s.now().Truncate(time.Microsecond)}
	var prior time.Time
	e := mustTx(ctx).QueryRow(ctx, `SELECT source_updated_at FROM payment_intent_watermarks WHERE intent_ref=$1`, in.Ref).Scan(&prior)
	if e != nil && !isNoRows(e) {
		return e
	}
	if !in.UpdatedAt.After(prior) {
		in.UpdatedAt = prior.Add(time.Microsecond)
	}
	latest := payment.CoreAllocation{}
	for _, a := range r.Allocations {
		if a.Version > latest.Version {
			latest = a
		}
	}
	var total int64
	for _, l := range latest.Lines {
		if l.AmountMinor <= 0 || l.AmountMinor > r.Quote.AmountMinor-total {
			return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Allocation total invalid.")
		}
		total += l.AmountMinor
	}
	if r.CollectionState == "VERIFIED_SUCCESS" && r.SettlementState == "SETTLED" && r.FundingConfirmed && total == r.Quote.AmountMinor {
		in.Status = payment.IntentSucceeded
		in.PaidMinor = b.Business.AmountMinor
		if r.ReversedMinor < 0 || r.ReversedMinor > b.Intent.AmountMinor {
			return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Invalid reversed collection amount.")
		}
		if r.ReversedMinor > 0 {
			if b.Business.AmountMinor != b.Intent.AmountMinor {
				in.Status = payment.IntentRefundPending
				in.PaidMinor = 0
			} else {
				in.PaidMinor -= r.ReversedMinor
				if in.PaidMinor > 0 {
					in.Status = payment.IntentPartiallyPaid
				} else {
					in.Status = payment.IntentRefundPending
				}
			}
		}
		if b.Business.Type == "deposit_obligation" && r.ReversedMinor == 0 {
			in.Status = payment.IntentHeld
		}
	}
	if r.CollectionState == "VERIFIED_FAILURE" {
		in.Status = payment.IntentFailed
	}
	if r.CollectionState == "CANCELLED" {
		in.Status = payment.IntentCancelled
	}
	if e = mustTx(ctx).QueryRow(ctx, `SELECT COALESCE(sum((snapshot->>'amount_minor')::bigint) FILTER(WHERE state='CONFIRMED_REFUNDED' AND COALESCE(snapshot->>'component_kind','') NOT IN ('PLATFORM','ONBOARDING','LEGAL')),0)::bigint FROM financial_core_resource_links WHERE intent_id=$1 AND resource_type='REFUND'`, b.Intent.ID).Scan(&in.RefundedMinor); e != nil {
		return e
	}
	if in.RefundedMinor > in.AmountMinor {
		return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Refunds exceed the business obligation.")
	}
	ctx = context.WithValue(ctx, canonicalFinancialProof{}, true)
	switch b.Business.Type {
	case AggRentObligation:
		o, e := s.q.GetRentObligationForUpdate(ctx, mustTx(ctx), b.Business.ID)
		if e != nil {
			return e
		}
		if o.Status == "SCHEDULED" && in.PaidMinor > 0 {
			if e = s.obligationTo(ctx, o.ID, "DUE", "PAYMENT_READBACK", "", nil, nil); e != nil {
				return e
			}
			o.Status = "DUE"
		}
		return s.projectRentIntent(ctx, o, in, "PAYMENT_READBACK", nil)
	case "deposit_obligation":
		d, e := s.q.GetDepositForUpdate(ctx, mustTx(ctx), b.Business.ID)
		if e != nil {
			return e
		}
		if e = s.projectDepositIntent(ctx, d, in); e != nil {
			return e
		}
		released := in.RefundedMinor
		for _, a := range r.AwardAllocations {
			for _, line := range a.Lines {
				if line.ReleasedMinor < 0 || line.ReleasedMinor > line.AmountMinor || line.ReleasedMinor > b.Business.AmountMinor-released {
					return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Executed deposit remedies exceed the original liability.")
				}
				released += line.ReleasedMinor
			}
		}
		if released > b.Business.AmountMinor {
			return precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Deposit refund exceeds original liability.")
		}
		_, e = mustTx(ctx).Exec(ctx, `UPDATE deposit_obligations SET released_minor=$2,last_checked_at=$3 WHERE id=$1`, d.ID, released, s.now())
		if e != nil {
			return e
		}
		if released == d.AmountMinor && (d.Status == "HELD" || d.Status == "RELEASE_PENDING") {
			if d.Status == "HELD" {
				if e = s.depositTo(ctx, d.ID, "RELEASE_PENDING", "confirmed_core_remedy", ""); e != nil {
					return e
				}
			}
			return s.depositTo(ctx, d.ID, "RELEASED", "confirmed_core_remedy", "")
		}
		return nil
	case "closing_milestone":
		return s.projectMilestoneIntent(ctx, b.Business.ID, in)
	case AggBooking:
		if e := s.projectBookingIntent(ctx, b.Business.ID, in); e != nil {
			return e
		}
		// Recompute terminal refunds from immutable IDs: duplicates/out-of-order
		// events never increment the business refund twice; reversals remain visible.
		_, e = mustTx(ctx).Exec(ctx, `UPDATE shortlet_bookings SET refunded_minor=$2 WHERE id=$1`, b.Business.ID, in.RefundedMinor)
		return e
	case aggExtension:
		return s.projectExtensionIntent(ctx, b.Business.ID, in)
	case "maintenance_job":
		return s.projectJobIntent(ctx, b.Business.ID, in)
	case AggAppointment:
		return s.projectFeeIntent(ctx, b.Business.ID, in)
	}
	return nil
}
func (s *Service) RunCoreFinancialReadbacks(ctx context.Context, batch int32) error {
	if !s.hasCoreFinancial() {
		return nil
	}
	rows, e := s.Pool.Query(ctx, `SELECT q.business_type,q.business_id FROM (SELECT 'rent_obligation'::text business_type,obligation_id business_id,intent_id,updated_at FROM financial_core_collections WHERE owner_account_id IS NOT NULL UNION ALL SELECT business_type,business_id,intent_id,updated_at FROM financial_core_business_bindings) q LEFT JOIN financial_core_readback_leases l USING(intent_id) ORDER BY l.last_attempt_at NULLS FIRST,q.updated_at,q.business_id LIMIT $1`, batch)
	if e != nil {
		return e
	}
	type item struct {
		kind string
		id   ids.ID
	}
	var work []item
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.kind, &x.id); e != nil {
			rows.Close()
			return e
		}
		work = append(work, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, x := range work {
		b, e := s.coreBinding(ctx, x.kind, x.id)
		if e == nil {
			_, e = s.reconcileCoreBinding(ctx, b)
		}
		if e != nil {
			s.Logger.WarnContext(ctx, "Core financial readback deferred", "business_type", x.kind, "business_id", x.id, "error", e)
		}
	}
	return nil
}

// A legacy event cannot supply financial truth for a canonical Core binding.
func (s *Service) isCanonicalCoreIntent(ctx context.Context, ref string) (bool, error) {
	id, e := ids.ParseV7(ref)
	if e != nil {
		return false, nil
	}
	var found bool
	e = s.dbq(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM financial_core_collections WHERE intent_id=$1) OR EXISTS(SELECT 1 FROM financial_core_business_bindings WHERE intent_id=$1)`, id).Scan(&found)
	return found, e
}

func (s *Service) CoreBindingForOwner(ctx context.Context, subj authz.Subject, kind string, id ids.ID) (CoreBinding, error) {
	return s.ownedCoreBinding(ctx, subj, kind, id)
}
