package lifecycle

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
)

type ReversalRequest struct {
	IntentID          ids.ID   `json:"intent_id"`
	ProviderReference string   `json:"provider_reference"`
	AuthorityRef      string   `json:"authority_ref"`
	Approvers         []string `json:"approvers"`
}
type ReversalRecord struct {
	ID                ids.ID  `json:"reversal_id"`
	IntentID          *ids.ID `json:"intent_id,omitempty"`
	PayoutID          *ids.ID `json:"payout_id,omitempty"`
	RefundID          *ids.ID `json:"refund_id,omitempty"`
	AmountMinor       int64   `json:"amount_minor"`
	Currency          string  `json:"currency"`
	EconomicReference string  `json:"economic_reference"`
	RecoverableMinor  int64   `json:"recoverable_minor"`
	JournalID         ids.ID  `json:"journal_id"`
}

func (s *Service) ReverseCollection(ctx context.Context, in ReversalRequest) (ReversalRecord, error) {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		if e = core.Require(ctx, platform.PaymentService); e != nil {
			return ReversalRecord{}, e
		}
	}
	if in.AuthorityRef == "" || in.ProviderReference == "" || !dual(in.Approvers) {
		return ReversalRecord{}, fail("REVERSAL_AUTHORITY_REQUIRED")
	}
	intent, e := s.GetIntent(trusted(ctx), in.IntentID)
	if e != nil {
		return ReversalRecord{}, e
	}
	adapter, e := s.Adapter(intent.ProviderAccountID)
	if e != nil {
		return ReversalRecord{}, e
	}
	if adapter.Reversal == nil {
		return ReversalRecord{}, core.Fail(503, "REVERSAL_READBACK_UNAVAILABLE")
	}
	obs, e := adapter.Reversal.VerifyReversal(ctx, in.ProviderReference)
	if e != nil {
		return ReversalRecord{}, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	var cfg finance.ProviderConfig
	var b []byte
	if e = s.DB.QueryRow(ctx, `SELECT route_snapshot FROM payment_intents WHERE intent_id=$1`, intent.ID).Scan(&b); e != nil {
		return ReversalRecord{}, e
	}
	if e = decode(b, &cfg); e != nil {
		return ReversalRecord{}, e
	}
	if !obs.Confirmed || obs.Reference != in.ProviderReference || obs.EconomicReference == "" || obs.ChargeReference != intent.Reference || obs.AmountMinor <= 0 || obs.AmountMinor > intent.AmountMinor || obs.Currency != intent.Currency || obs.Mode != cfg.Mode || formatID(obs.MerchantID) != cfg.MerchantRef {
		return ReversalRecord{}, core.Fail(409, "REVERSAL_CONTEXT_MISMATCH")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return ReversalRecord{}, e
	}
	defer tx.Rollback(ctx)
	var locked ids.ID
	if e = tx.QueryRow(ctx, `SELECT intent_id FROM payment_intents WHERE intent_id=$1 FOR UPDATE`, intent.ID).Scan(&locked); e != nil {
		return ReversalRecord{}, e
	}
	if e = lock(ctx, tx, "economic-reversal:"+intent.ProviderAccountID.String()+":"+obs.EconomicReference); e != nil {
		return ReversalRecord{}, e
	}
	var old ReversalRecord
	// load has a single ID parameter; use the economic key explicitly.
	var prior []byte
	e = tx.QueryRow(ctx, `SELECT snapshot FROM financial_reversals WHERE provider_account_id=$1 AND external_ref=$2`, intent.ProviderAccountID, obs.EconomicReference).Scan(&prior)
	if e == nil {
		if e = decode(prior, &old); e != nil {
			return old, e
		}
		if old.IntentID == nil || *old.IntentID != intent.ID || old.AmountMinor != obs.AmountMinor {
			return old, core.Fail(409, "REVERSAL_CONTEXT_MISMATCH")
		}
		return old, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return old, e
	}
	var refunds, rev int64
	e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT sum(amount_minor) FROM refund_attempts WHERE intent_id=$1 AND state NOT IN ('CONFIRMED_FAILED','CANCELLED')),0)::bigint,COALESCE((SELECT sum(amount_minor) FROM financial_reversals WHERE intent_id=$1),0)::bigint`, intent.ID).Scan(&refunds, &rev)
	if e != nil {
		return old, e
	}
	if obs.AmountMinor > intent.AmountMinor-refunds-rev {
		// Preserve authoritative evidence even when an ambiguous refund overlaps.
		if _, e = tx.Exec(ctx, `INSERT INTO provider_observations(observation_id,provider_account_id,operation,external_ref,outcome,metadata) VALUES($1,$2,'REVERSAL',$3,'OVERLAPPING_REMEDY_REVIEW',$4)`, ids.New(), intent.ProviderAccountID, obs.EconomicReference, raw(map[string]any{"intent_id": intent.ID, "amount_minor": obs.AmountMinor, "currency": obs.Currency})); e != nil {
			return old, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reconciliation_exceptions(exception_id,pool_id,reason,expected_minor,observed_minor) VALUES($1,$2,'REFUND_REVERSAL_OVERLAP',$3,$4)`, ids.New(), intent.PoolID, intent.AmountMinor-refunds-rev, obs.AmountMinor); e != nil {
			return old, e
		}
		if _, e = tx.Exec(ctx, `UPDATE custody_pools SET restricted=true WHERE pool_id=$1`, intent.PoolID); e != nil {
			return old, e
		}
		if e = tx.Commit(ctx); e != nil {
			return old, e
		}
		return old, core.Fail(409, "REFUND_REVERSAL_REQUIRES_REVIEW")
	}
	var allocation Allocation
	var allocationID ids.ID
	e = tx.QueryRow(ctx, `SELECT allocation_id FROM allocation_snapshots WHERE intent_id=$1 ORDER BY version DESC LIMIT 1`, intent.ID).Scan(&allocationID)
	entries := []core.Entry{}
	left := obs.AmountMinor
	recoverable := int64(0)
	if e == nil {
		allocation, e = s.allocation(ctx, tx, allocationID)
		if e != nil {
			return old, e
		}
		for _, line := range allocation.Lines {
			var protected ids.ID
			var available *ids.ID
			if e = tx.QueryRow(ctx, `SELECT protected_account_id,available_account_id FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, line.ID).Scan(&protected, &available); e != nil {
				return old, e
			}
			pn, e := accountBalance(ctx, tx, protected)
			if e != nil {
				return old, e
			}
			take := min(left, pn)
			if take > 0 {
				entries = append(entries, core.Entry{AccountID: protected, Side: "DEBIT", AmountMinor: take})
				left -= take
			}
			if left > 0 && available != nil {
				an, e := accountBalance(ctx, tx, *available)
				if e != nil {
					return old, e
				}
				take = min(left, an)
				if take > 0 {
					entries = append(entries, core.Entry{AccountID: *available, Side: "DEBIT", AmountMinor: take})
					left -= take
				}
			}
			// In-flight payouts and prior paid proceeds cannot be invented back into cash.
			if _, e = tx.Exec(ctx, `UPDATE allocation_lines SET state='REVIEW_REQUIRED' WHERE line_id=$1`, line.ID); e != nil {
				return old, e
			}
		}
	} else if errors.Is(e, pgx.ErrNoRows) {
		pending, e := s.Account(trusted(ctx), tx, &intent.WalletID, intent.PoolID, "PAYMENT_PENDING", intent.LegalEntity, intent.Currency, "")
		if e != nil {
			return old, e
		}
		n, e := accountBalance(ctx, tx, pending)
		if e != nil {
			return old, e
		}
		take := min(left, n)
		if take > 0 {
			entries = append(entries, core.Entry{AccountID: pending, Side: "DEBIT", AmountMinor: take})
			left -= take
		}
		if left > 0 {
			var funded bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customer_topups t JOIN customer_funding f USING(topup_id) WHERE t.intent_id=$1)`, intent.ID).Scan(&funded); e != nil {
				return old, e
			}
			if funded {
				available, e := s.Account(trusted(ctx), tx, &intent.WalletID, intent.PoolID, "AVAILABLE", intent.LegalEntity, intent.Currency, "")
				if e != nil {
					return old, e
				}
				n, e := accountBalance(ctx, tx, available)
				if e != nil {
					return old, e
				}
				take := min(left, n)
				if take > 0 {
					entries = append(entries, core.Entry{AccountID: available, Side: "DEBIT", AmountMinor: take})
					left -= take
				}
			}
		}
	} else {
		return old, e
	}
	out := ReversalRecord{ID: ids.New(), IntentID: &intent.ID, AmountMinor: obs.AmountMinor, Currency: intent.Currency, EconomicReference: obs.EconomicReference}
	if left > 0 {
		recoverable = left
		asset, e := s.Account(trusted(ctx), tx, nil, intent.PoolID, "RECOVERABLE", intent.LegalEntity, intent.Currency, "reversal:"+out.ID.String())
		if e != nil {
			return out, e
		}
		entries = append(entries, core.Entry{AccountID: asset, Side: "DEBIT", AmountMinor: left})
	}
	kind := "PROVIDER_RECEIVABLE"
	if intent.SettlementState == "SETTLED" {
		kind = "SETTLED_CASH"
	}
	cash, e := s.Account(trusted(ctx), tx, nil, intent.PoolID, kind, intent.LegalEntity, intent.Currency, "")
	if e != nil {
		return out, e
	}
	balance, e := accountBalance(ctx, tx, cash)
	if e != nil {
		return out, e
	}
	cashTake := min(balance, obs.AmountMinor)
	if cashTake > 0 {
		entries = append(entries, core.Entry{AccountID: cash, Side: "CREDIT", AmountMinor: cashTake})
	}
	if cashTake < obs.AmountMinor {
		payable, e := s.Account(trusted(ctx), tx, nil, intent.PoolID, "PROVIDER_PAYABLE", intent.LegalEntity, intent.Currency, "")
		if e != nil {
			return out, e
		}
		entries = append(entries, core.Entry{AccountID: payable, Side: "CREDIT", AmountMinor: obs.AmountMinor - cashTake})
	}
	j, e := s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: "provider-reversal:" + intent.ProviderAccountID.String() + ":" + obs.EconomicReference, PoolID: intent.PoolID, LegalEntity: intent.LegalEntity, Currency: intent.Currency, PolicyVersion: "confirmed-provider-reversal-v1", Authorization: map[string]string{"evidence_ref": in.AuthorityRef, "approved_by": raw(in.Approvers), "confirmed_reversal_ref": obs.EconomicReference}, Entries: entries})
	if e != nil {
		return out, e
	}
	out.JournalID = j
	out.RecoverableMinor = recoverable
	_, e = tx.Exec(ctx, `INSERT INTO financial_reversals(reversal_id,intent_id,provider_account_id,external_ref,amount_minor,snapshot,journal_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, out.ID, intent.ID, intent.ProviderAccountID, obs.EconomicReference, out.AmountMinor, raw(out), j)
	if e != nil {
		return out, e
	}
	if recoverable > 0 {
		_, e = tx.Exec(ctx, `INSERT INTO financial_recoverables(recoverable_id,reversal_id,wallet_id,amount_minor,reason,snapshot) VALUES($1,$2,NULL,$3,'RELEASED_OR_PAID_PROCEEDS',$4)`, ids.New(), out.ID, recoverable, raw(map[string]any{"allocation_id": allocationID, "authority_ref": in.AuthorityRef, "manual_liability_assignment_required": true}))
		if e != nil {
			return out, e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE custody_pools SET restricted=true WHERE pool_id=$1`, intent.PoolID); e != nil {
		return out, e
	}
	if e = s.Audit(ctx, tx, "collection.reversed", out.ID.String(), out); e != nil {
		return out, e
	}
	if e = s.Emit(ctx, tx, "finance.financial_reversed.v1", "reversal", out.ID, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Service) ReversePaidPayout(ctx context.Context, id ids.ID) (Payout, error) {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		if e = core.Require(ctx, platform.AdminService); e != nil {
			return Payout{}, e
		}
	}
	out, e := s.payout(ctx, s.DB, id)
	if e != nil {
		return out, e
	}
	if out.State != "CONFIRMED_PAID" {
		return out, core.Fail(409, "PAID_PAYOUT_REQUIRED")
	}
	adapter, e := s.Adapter(out.ProviderID)
	if e != nil {
		return out, e
	}
	if adapter.Payout == nil {
		return out, core.Fail(503, "PAYOUT_PROVIDER_UNAVAILABLE")
	}
	obs, e := adapter.Payout.VerifyTransfer(ctx, out.Reference)
	if e != nil {
		return out, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	var cfg finance.ProviderConfig
	var b []byte
	if e = s.DB.QueryRow(ctx, `SELECT route_snapshot FROM payout_attempts WHERE payout_id=$1`, id).Scan(&b); e != nil {
		return out, e
	}
	if e = decode(b, &cfg); e != nil {
		return out, e
	}
	if (obs.State != "REVERSED" && !obs.Reversed) || obs.Reference != out.Reference || obs.AmountMinor != out.AmountMinor || obs.Currency != out.Currency || obs.Mode != cfg.Mode || formatID(obs.MerchantID) != cfg.MerchantRef || obs.RecipientRef != out.RecipientRef {
		return out, core.Fail(409, "PAYOUT_REVERSAL_CONTEXT_MISMATCH")
	}
	a, e := s.lineAllocation(ctx, out.LineID)
	if e != nil {
		return out, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "payout-reversal:"+id.String()); e != nil {
		return out, e
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM financial_reversals WHERE payout_id=$1)`, id).Scan(&exists); e != nil {
		return out, e
	}
	if exists {
		out.State = "REVERSED"
		return out, nil
	}
	var line ids.ID
	if e = tx.QueryRow(ctx, `SELECT line_id FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, out.LineID).Scan(&line); e != nil {
		return out, e
	}
	cash, e := s.Account(trusted(ctx), tx, nil, out.PoolID, "SETTLED_CASH", a.LegalEntity, out.Currency, "")
	if e != nil {
		return out, e
	}
	frozen, e := s.Account(trusted(ctx), tx, &out.WalletID, out.PoolID, "FROZEN", a.LegalEntity, out.Currency, "payout-reversal:"+id.String())
	if e != nil {
		return out, e
	}
	j, e := s.remedyPost(ctx, tx, a, "payout-reversal:"+id.String(), out.Reference, []core.Entry{{AccountID: cash, Side: "DEBIT", AmountMinor: out.AmountMinor}, {AccountID: frozen, Side: "CREDIT", AmountMinor: out.AmountMinor}})
	if e != nil {
		return out, e
	}
	r := ReversalRecord{ID: ids.New(), PayoutID: &id, AmountMinor: out.AmountMinor, Currency: out.Currency, EconomicReference: "payout:" + out.Reference, JournalID: j}
	_, e = tx.Exec(ctx, `INSERT INTO financial_reversals(reversal_id,payout_id,provider_account_id,external_ref,amount_minor,snapshot,journal_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, r.ID, id, out.ProviderID, r.EconomicReference, r.AmountMinor, raw(r), j)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE allocation_lines SET state='REVIEW_REQUIRED' WHERE line_id=$1`, out.LineID); e != nil {
		return out, e
	}
	if e = s.Audit(ctx, tx, "payout.reversed", id.String(), r); e != nil {
		return out, e
	}
	if e = s.Emit(ctx, tx, "finance.financial_reversed.v1", "reversal", r.ID, r); e != nil {
		return out, e
	}
	out.State = "REVERSED"
	return out, tx.Commit(ctx)
}

// RecoverRefunds never submits. Attempt schedules are configurable deployment
// concerns; this bounded sweep preserves original-reference recovery.
func (s *Service) RecoverRefunds(ctx context.Context) error {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		return e
	}
	rows, e := s.DB.Query(ctx, `SELECT refund_id FROM refund_attempts WHERE state NOT IN ('RESERVED','CONFIRMED_REFUNDED','CONFIRMED_FAILED','CANCELLED') AND NOT EXISTS(SELECT 1 FROM original_readback_jobs j WHERE j.kind='REFUND' AND j.aggregate_id=refund_id AND (j.state IN ('DEAD','COMPLETED') OR j.next_attempt_at>now() OR j.lease_until>now())) AND updated_at<now()-interval '15 seconds' ORDER BY updated_at LIMIT 20`)
	if e != nil {
		return e
	}
	var xs []ids.ID
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		xs = append(xs, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range xs {
		token, claimed, e := s.ClaimReadback(ctx, "REFUND", id)
		if e != nil {
			return e
		}
		if !claimed {
			continue
		}
		out, readErr := s.ResolveRefund(ctx, id)
		if e = s.FinishReadback(ctx, "REFUND", id, token, readErr == nil && has([]string{"CONFIRMED_REFUNDED", "CONFIRMED_FAILED", "CANCELLED", "REVERSED"}, out.State)); e != nil {
			return e
		}
	}
	return nil
}
