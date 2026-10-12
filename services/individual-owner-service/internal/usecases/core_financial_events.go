package usecases

import (
	"context"
	"encoding/json"
	"fmt"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

type coreEventReference struct {
	IntentID     ids.ID `json:"intent_id"`
	LineID       ids.ID `json:"line_id"`
	AllocationID ids.ID `json:"allocation_id"`
	ObligationID ids.ID `json:"obligation_id"`
	RefundID     ids.ID `json:"refund_id"`
	CaseID       ids.ID `json:"case_id"`
	DepositID    ids.ID `json:"deposit_id"`
	PayoutID     ids.ID `json:"payout_id"`
	WalletID     ids.ID `json:"wallet_id"`
	PoolID       ids.ID `json:"pool_id"`
	ProviderID   ids.ID `json:"provider_account_id"`
	RunID        ids.ID `json:"run_id"`
	ReversalID   ids.ID `json:"reversal_id"`
}

func (s *Service) coreBindingByIntent(ctx context.Context, id ids.ID) (CoreBinding, error) {
	var kind string
	var business ids.ID
	e := s.Pool.QueryRow(ctx, `SELECT business_type,business_id FROM (SELECT 'rent_obligation'::text business_type,obligation_id business_id,intent_id FROM financial_core_collections UNION ALL SELECT business_type,business_id,intent_id FROM financial_core_business_bindings) b WHERE intent_id=$1`, id).Scan(&kind, &business)
	if isNoRows(e) {
		return CoreBinding{}, notFound("core_business_binding")
	}
	if e != nil {
		return CoreBinding{}, e
	}
	return s.coreBinding(ctx, kind, business)
}
func (s *Service) applyCanonicalCoreEvent(ctx context.Context, event events.Envelope) error {
	var ref coreEventReference
	if e := json.Unmarshal(event.Payload, &ref); e != nil {
		return events.Permanent(e)
	}
	aggregate := ""
	var id ids.ID
	resourceType := ""
	switch event.EventType {
	case "finance.payment.collected.v1":
		aggregate, id = "payment_intent", ref.IntentID
	case "finance.rent_payment_verified.v1":
		aggregate, id = "rent_obligation", ref.ObligationID
	case "finance.allocation_created.v1", "finance.wallet_spent.v1":
		aggregate, id = "allocation", ref.AllocationID
	case "finance.allocation_released.v1":
		aggregate, id = "allocation_line", ref.LineID
	case "finance.deposit_registered.v1":
		aggregate, id = "deposit", ref.DepositID
		resourceType = "DEPOSIT"
	case "finance.case_opened.v1", "finance.case_decided.v1":
		aggregate, id = "financial_case", ref.CaseID
		resourceType = "CASE"
	case "finance.refund_reserved.v1", "finance.refund_state_changed.v1":
		aggregate, id = "refund", ref.RefundID
		resourceType = "REFUND"
	case "finance.payout_initiated.v1", "finance.payout_confirmed.v1", "finance.payout_failed.v1", "finance.payout_resolution_required.v1":
		aggregate, id = "payout", ref.PayoutID
	case "finance.financial_reversed.v1":
		aggregate, id = "reversal", ref.ReversalID
	case "finance.reconciliation_completed.v1":
		aggregate, id = "reconciliation", ref.RunID
	case "finance.reconciliation.exception_detected.v1":
		if event.AggregateType == "payment_intent" {
			aggregate, id = "payment_intent", ref.IntentID
		} else {
			aggregate, id = "custody_pool", ref.PoolID
		}
	default:
		return events.Permanent(fmt.Errorf("unsupported canonical financial event %s", event.EventType))
	}
	if e := checkUpstream(event, platform.PaymentService, aggregate); e != nil {
		return e
	}
	if id.ValidateV7() != nil || event.AggregateID != id {
		return events.Permanent(fmt.Errorf("canonical aggregate/reference mismatch"))
	}
	var processed bool
	if e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM processed_events WHERE consumer=$1 AND event_id=$2)`, ConsumerPayment, event.EventID).Scan(&processed); e != nil {
		return e
	}
	if processed {
		return nil
	}
	c, e := s.coreClient()
	if e != nil {
		return e
	}
	// Event payloads are hints only. Signed readback supplies current financial
	// truth, so a stale or duplicated callback cannot manufacture paid money.
	if aggregate == "payout" {
		var owner ids.ID
		if e = s.Pool.QueryRow(ctx, `SELECT owner_account_id FROM financial_core_wallet_links WHERE wallet_id=$1`, ref.WalletID).Scan(&owner); isNoRows(e) {
			return notFound("core_wallet_binding")
		}
		if e != nil {
			return e
		}
		actual, e := c.Payout(ctx, owner, id, false)
		if e != nil {
			return downstream("payment-service", e)
		}
		if actual.WalletID != ref.WalletID {
			return events.Permanent(fmt.Errorf("payout wallet mismatch"))
		}
		return s.InTx(ctx, func(ctx context.Context) error {
			first, e := events.MarkProcessed(ctx, mustTx(ctx), ConsumerPayment, event)
			if e != nil || !first {
				return e
			}
			if e = s.record(ctx, "financial.core_payout_observed", "account", owner, map[string]any{"payout_id": actual.ID, "state": actual.State, "reference": actual.Reference, "provider_account_id": actual.ProviderID, "pool_id": actual.PoolID}, nil); e != nil {
				return e
			}
			return s.completeCoreEvent(ctx, event)
		})
	}
	if aggregate == "reconciliation" || aggregate == "custody_pool" {
		rows, e := s.Pool.Query(ctx, `SELECT intent_id FROM (SELECT intent_id,snapshot FROM financial_core_collections UNION ALL SELECT intent_id,intent_snapshot snapshot FROM financial_core_business_bindings) b WHERE ($1::uuid IS NOT NULL AND snapshot->>'pool_id'=$1::text) OR ($2::uuid IS NOT NULL AND snapshot->>'provider_account_id'=$2::text)`, ref.PoolID, ref.ProviderID)
		if e != nil {
			return e
		}
		var idsToRead []ids.ID
		for rows.Next() {
			var x ids.ID
			if e = rows.Scan(&x); e != nil {
				rows.Close()
				return e
			}
			idsToRead = append(idsToRead, x)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, x := range idsToRead {
			b, e := s.coreBindingByIntent(ctx, x)
			if e != nil {
				return e
			}
			if _, e = s.reconcileCoreBinding(ctx, b); e != nil {
				return e
			}
		}
		return s.InTx(ctx, func(ctx context.Context) error { return s.completeCoreEvent(ctx, event) })
	}
	if resourceType == "CASE" && ref.IntentID == ids.Nil {
		actual, e := c.Case(ctx, id)
		if e != nil {
			return downstream("payment-service", e)
		}
		ref.IntentID = actual.IntentID
	}
	if ref.IntentID == ids.Nil && ref.LineID != ids.Nil {
		if e = s.Pool.QueryRow(ctx, `SELECT original_intent_id FROM financial_core_allocation_lines WHERE line_id=$1`, ref.LineID).Scan(&ref.IntentID); isNoRows(e) {
			return notFound("core_allocation_line")
		}
		if e != nil {
			return e
		}
	}
	b, e := s.coreBindingByIntent(ctx, ref.IntentID)
	if e != nil && aggregate == "allocation" {
		if err := s.Pool.QueryRow(ctx, `SELECT original_intent_id FROM financial_core_allocation_lines WHERE allocation_id=$1 LIMIT 1`, ref.AllocationID).Scan(&ref.IntentID); err == nil {
			b, e = s.coreBindingByIntent(ctx, ref.IntentID)
		}
	}
	if e != nil {
		return e
	}
	if resourceType != "" {
		proof, e := s.readCoreResource(ctx, b, resourceType, id)
		if e != nil {
			return e
		}
		if e = s.InTx(ctx, func(ctx context.Context) error { return s.storeCoreResource(ctx, proof) }); e != nil {
			return e
		}
	}
	if _, e = s.reconcileCoreBinding(ctx, b); e != nil {
		if code := apperrors.CodeOf(e); code == "CORE_READBACK_IN_PROGRESS" || code == "STALE_CORE_READBACK" {
			return e
		}
		return s.consumerOutcome(ctx, ConsumerPayment, event, e)
	}
	return s.InTx(ctx, func(ctx context.Context) error { return s.completeCoreEvent(ctx, event) })
}
func (s *Service) completeCoreEvent(ctx context.Context, e events.Envelope) error {
	_, err := mustTx(ctx).Exec(ctx, `INSERT INTO processed_events(consumer,event_id,event_type,processed_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, ConsumerPayment, e.EventID, e.EventType, s.now())
	return err
}
