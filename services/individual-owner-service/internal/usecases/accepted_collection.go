package usecases

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"math"
)

type AcceptedCollectionInput struct {
	QuoteID    ids.ID `json:"quote_id" v:"required"`
	PayerEmail string `json:"payer_email" v:"required,format=email,max=254"`
}
type AcceptedCollectionEvidence struct {
	Quote  payment.AcceptedQuote
	Intent payment.CollectionIntent
	Policy payment.CollectionPolicy
}
type CollectionClient interface {
	AcceptedQuote(context.Context, ids.ID) (payment.AcceptedQuote, error)
	CreateAcceptedCollection(context.Context, ids.ID, payment.AcceptedQuote, string) (payment.CollectionIntent, error)
	Collection(context.Context, ids.ID) (payment.CollectionIntent, error)
}

func (s *Service) PrepareAcceptedRentCollection(ctx context.Context, subj authz.Subject, id ids.ID, in AcceptedCollectionInput) (AcceptedCollectionEvidence, error) {
	var out AcceptedCollectionEvidence
	o, e := s.q.GetRentObligation(ctx, s.Pool, id)
	if isNoRows(e) {
		return out, notFound(AggRentObligation)
	}
	if e != nil {
		return out, e
	}
	if _, e = s.authorize(ctx, subj, authz.Do(authz.CapTenancyManage), o.PropertyID); e != nil {
		return out, e
	}
	if o.Kind != "RENT" || o.Status == "WAIVED" || o.Status == "PAID" || o.RefundedMinor > 0 || o.PaidMinor > 0 {
		return out, precondition("ACCEPTED_RENT_CONTEXT_REQUIRED", "Use the existing adjustment workflow for paid, waived or non-rent obligations.")
	}
	client, ok := s.walletRegistration.(CollectionClient)
	if !ok {
		return out, unavailable("Accepted collection", "payment-service")
	}
	if o.PaymentIntentRef != nil {
		var bound ids.ID
		e = s.Pool.QueryRow(ctx, `SELECT quote_id FROM financial_core_collections WHERE obligation_id=$1`, id).Scan(&bound)
		if e != nil || bound != in.QuoteID {
			return out, precondition("PAYMENT_INTENT_ALREADY_BOUND", "Use the existing bound quote and intent.")
		}
	}
	q, e := client.AcceptedQuote(ctx, in.QuoteID)
	if e != nil {
		return out, downstream("payment-service", e)
	}
	if q.Domain != "individual-owner" || q.BusinessRef != "rent_obligation:"+id.String() || q.Currency != o.Currency {
		return out, precondition("LOCKED_QUOTE_CONTEXT_MISMATCH", "Accepted quote does not match this rent obligation.")
	}
	core, ok := s.walletRegistration.(FinancialCoreClient)
	if !ok {
		return out, unavailable("Accepted rent policy context", "payment-service")
	}
	receipt, e := core.Receipt(ctx, q.ID)
	if e != nil {
		return out, downstream("payment-service", e)
	}
	business, e := s.OwnedCoreBusiness(ctx, subj, AggRentObligation, id)
	if e != nil {
		return out, e
	}
	if e = validateCorePayer(business, q, receipt.PayerContext); e != nil {
		return out, e
	}
	policy, e := core.Policy(ctx, q.PolicyID)
	if e != nil {
		return out, downstream("payment-service", e)
	}
	if e = s.acceptedRentContext(ctx, o, q, policy); e != nil {
		return out, e
	}
	p, e := s.q.GetProperty(ctx, s.Pool, o.PropertyID)
	if e != nil {
		return out, e
	}
	var intent payment.CollectionIntent
	if receipt.IntentID != nil {
		intent, e = client.Collection(coreOwnerContext(ctx, p.OwnerAccountID), *receipt.IntentID)
	} else {
		intent, e = client.CreateAcceptedCollection(ctx, p.OwnerAccountID, q, in.PayerEmail)
	}
	if e == nil && (intent.WalletID != q.WalletID || intent.AmountMinor != q.AmountMinor || intent.Currency != q.Currency) {
		return out, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Existing intent does not match the accepted quote.")
	}
	if e != nil {
		return out, downstream("payment-service", e)
	}
	return AcceptedCollectionEvidence{Quote: q, Intent: intent, Policy: policy}, nil
}
func (s *Service) RecordAcceptedRentCollection(ctx context.Context, subj authz.Subject, id ids.ID, ev AcceptedCollectionEvidence) (payment.CollectionIntent, error) {
	o, e := s.obligationFor(ctx, subj, id)
	if e != nil {
		return ev.Intent, e
	}
	if ev.Quote.BusinessRef != "rent_obligation:"+id.String() || o.Currency != ev.Intent.Currency || ev.Quote.ID.ValidateV7() != nil {
		return ev.Intent, precondition("LOCKED_QUOTE_CONTEXT_MISMATCH", "Accepted quote context changed.")
	}
	if o.Kind != "RENT" || o.Status == "WAIVED" || o.Status == "PAID" || o.PaidMinor != 0 || o.RefundedMinor != 0 {
		return ev.Intent, precondition("ACCEPTED_RENT_CONTEXT_REQUIRED", "Business obligation changed; retain and reconcile the original Financial Core intent.")
	}
	if e = s.acceptedRentContext(ctx, o, ev.Quote, ev.Policy); e != nil {
		return ev.Intent, e
	}
	ref := ev.Intent.ID.String()
	if o.PaymentIntentRef != nil && *o.PaymentIntentRef != ref {
		return ev.Intent, precondition("PAYMENT_INTENT_ALREADY_BOUND", "Use the existing bound intent.")
	}
	raw, e := json.Marshal(ev.Intent)
	if e != nil {
		return ev.Intent, e
	}
	b, e := s.OwnedCoreBusiness(ctx, subj, AggRentObligation, id)
	if e != nil {
		return ev.Intent, e
	}
	br, _ := json.Marshal(b)
	qr, _ := json.Marshal(ev.Quote)
	tag, e := mustTx(ctx).Exec(ctx, `INSERT INTO financial_core_collections(obligation_id,quote_id,intent_id,amount_minor,currency,snapshot,observed_state,settlement_state,owner_account_id,business_context,quote_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(obligation_id) DO NOTHING`, id, ev.Quote.ID, ev.Intent.ID, ev.Intent.AmountMinor, ev.Intent.Currency, string(raw), ev.Intent.State, ev.Intent.SettlementState, b.OwnerID, string(br), string(qr))
	if e != nil {
		return ev.Intent, e
	}
	if tag.RowsAffected() == 0 {
		var match bool
		e = mustTx(ctx).QueryRow(ctx, `SELECT quote_id=$2 AND intent_id=$3 FROM financial_core_collections WHERE obligation_id=$1`, id, ev.Quote.ID, ev.Intent.ID).Scan(&match)
		if e != nil {
			return ev.Intent, e
		}
		if !match {
			return ev.Intent, precondition("PAYMENT_INTENT_ALREADY_BOUND", "Quote is immutable once bound.")
		}
		return ev.Intent, nil
	}
	// Core collection success is neither allocation/funding nor paid rent.
	status := "FINANCIAL_CORE:" + ev.Intent.State
	e = s.q.SetObligationIntent(ctx, mustTx(ctx), db.SetObligationIntentParams{ID: id, PaymentIntentRef: &ref, LastPaymentStatus: &status, LastCheckedAt: ptr(s.now())})
	if e != nil {
		return ev.Intent, e
	}
	e = s.record(ctx, "rent.financial_collection_bound", AggRentObligation, id, map[string]any{"quote_id": ev.Quote.ID, "intent_id": ev.Intent.ID, "state": ev.Intent.State}, nil)
	return ev.Intent, e
}
func (s *Service) FinancialRentCollection(ctx context.Context, subj authz.Subject, id ids.ID) (payment.CollectionIntent, error) {
	o, e := s.q.GetRentObligation(ctx, s.Pool, id)
	if isNoRows(e) {
		return payment.CollectionIntent{}, notFound(AggRentObligation)
	}
	if e != nil {
		return payment.CollectionIntent{}, e
	}
	if _, e = s.authorize(ctx, subj, authz.Read(authz.CapRentView), o.PropertyID); e != nil {
		return payment.CollectionIntent{}, e
	}
	var raw []byte
	e = s.Pool.QueryRow(ctx, `SELECT snapshot||jsonb_build_object('state',observed_state,'settlement_state',settlement_state) FROM financial_core_collections WHERE obligation_id=$1`, id).Scan(&raw)
	if isNoRows(e) {
		return payment.CollectionIntent{}, notFound("financial_collection")
	}
	if e != nil {
		return payment.CollectionIntent{}, e
	}
	var out payment.CollectionIntent
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	client, ok := s.walletRegistration.(CollectionClient)
	if !ok {
		return out, unavailable("Financial collection readback", "payment-service")
	}
	authoritative, e := client.Collection(ctx, out.ID)
	if e != nil {
		return out, downstream("payment-service", e)
	}
	if authoritative.ID != out.ID || authoritative.WalletID != out.WalletID || authoritative.AmountMinor != out.AmountMinor || authoritative.Currency != out.Currency || authoritative.LegalEntity != out.LegalEntity {
		return out, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Financial Core terms differ from the accepted binding.")
	}
	return authoritative, nil
}

// Owner schedules already include the accepted recurring fees. Never count
// them again or relabel the entire obligation as rent principal. Initial
// one-time charges and refundable deposits require their distinct workflows.
func acceptedRentComponents(q payment.AcceptedQuote, expected int64) bool {
	if expected <= 0 || q.AmountMinor != expected || len(q.Components) == 0 {
		return false
	}
	var sum int64
	rent := false
	for _, c := range q.Components {
		if c.AmountMinor <= 0 || sum > math.MaxInt64-c.AmountMinor {
			return false
		}
		switch c.Kind {
		case "RENT":
			rent = true
		case "SERVICE", "UTILITY", "TAX", "PROCESSING":
		default:
			return false
		}
		if c.Recurrence != "RECURRING" && !(c.Kind == "TAX" && c.Recurrence == "TAX") && !(c.Kind == "PROCESSING" && c.Recurrence == "PROVIDER_PROCESSING") {
			return false
		}
		sum += c.AmountMinor
	}
	return rent && sum == expected
}
