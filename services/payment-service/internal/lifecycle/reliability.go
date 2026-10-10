package lifecycle

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"strconv"
	"strings"
	"time"
)

type ReconciliationRequest struct {
	ID                ids.ID `json:"run_id"`
	ProviderID        ids.ID `json:"provider_account_id"`
	AuthorityRef      string `json:"authority_ref"`
	StaleAfterSeconds int64  `json:"stale_after_seconds"`
}
type ReconciliationFinding struct {
	Kind         string `json:"kind"`
	AggregateRef string `json:"aggregate_ref"`
	Expected     string `json:"expected"`
	Observed     string `json:"observed"`
}
type ReconciliationRun struct {
	ReconciliationRequest
	Findings []ReconciliationFinding `json:"findings"`
	Status   string                  `json:"status"`
}

// Reconcile is manually runnable and safe to invoke from an externally
// configured job. It records exceptions, never silently rewrites the ledger.
func (s *Service) Reconcile(ctx context.Context, in ReconciliationRequest) (ReconciliationRun, error) {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return ReconciliationRun{}, e
	}
	if in.ID.ValidateV7() != nil || in.AuthorityRef == "" || in.StaleAfterSeconds <= 0 || in.StaleAfterSeconds > 315360000 {
		return ReconciliationRun{}, fail("RECONCILIATION_PARAMETERS_REQUIRED")
	}
	out := ReconciliationRun{ReconciliationRequest: in, Findings: []ReconciliationFinding{}, Status: "MATCHED"}
	var prior ReconciliationRun
	if e := load(ctx, s.DB, `SELECT snapshot FROM reconciliation_runs WHERE run_id=$1`, in.ID, &prior); e == nil {
		if core.Fingerprint(prior.ReconciliationRequest) != core.Fingerprint(in) {
			return out, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return prior, nil
	}
	adapter, e := s.Adapter(in.ProviderID)
	if e != nil {
		return out, e
	}
	if adapter.Settlement == nil {
		return out, core.Fail(503, "SETTLEMENT_UNAVAILABLE")
	}
	balances, e := adapter.Settlement.FetchBalance(ctx)
	if e != nil {
		return out, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	var cfg finance.ProviderConfig
	if e = load(ctx, s.DB, `SELECT config FROM provider_accounts WHERE provider_account_id=$1`, in.ProviderID, &cfg); e != nil {
		return out, e
	}
	var pool ids.ID
	var currency string
	if e = s.DB.QueryRow(ctx, `SELECT pool_id,currency FROM custody_pools WHERE provider_account_id=$1`, in.ProviderID).Scan(&pool, &currency); e != nil {
		return out, e
	}
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "reconciliation:"+in.ID.String()); e != nil {
		return out, e
	}
	add := func(kind, ref, expected, observed string) {
		out.Findings = append(out.Findings, ReconciliationFinding{kind, ref, expected, observed})
	}
	var cash string
	if e = tx.QueryRow(ctx, `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE pool_id=$1 AND kind='SETTLED_CASH'`, pool).Scan(&cash); e != nil {
		return out, e
	}
	restrictPool := false
	seen := false
	for _, v := range balances {
		if v.Currency != currency {
			continue
		}
		if seen {
			add("DUPLICATE_BALANCE", pool.String(), "one currency balance", "duplicate")
		}
		seen = true
		if v.AmountMinor < 0 || strconv.FormatInt(v.AmountMinor, 10) != cash {
			add("BALANCE_MISMATCH", pool.String(), cash, strconv.FormatInt(v.AmountMinor, 10))
			ledgerCash, parseErr := strconv.ParseInt(cash, 10, 64)
			if parseErr != nil {
				return out, parseErr
			}
			if v.AmountMinor < ledgerCash {
				restrictPool = true
			}
		}
	}
	if !seen {
		add("MISSING_BALANCE", pool.String(), currency, "missing")
	}
	checks := []struct{ kind, sql string }{
		{"MISSING_COLLECTION", `SELECT intent_id::text FROM payment_intents i WHERE provider_account_id=$1 AND state='VERIFIED_SUCCESS' AND NOT route_snapshot ? 'funding_source' AND NOT EXISTS(SELECT 1 FROM collection_receipts r WHERE r.provider_account_id=i.provider_account_id AND r.external_ref=i.provider_reference)`},
		{"COLLECTION_CONTEXT_MISMATCH", `SELECT i.intent_id::text FROM payment_intents i JOIN collection_receipts r ON r.provider_account_id=i.provider_account_id AND r.external_ref=i.provider_reference WHERE i.provider_account_id=$1 AND (r.amount_minor<>i.amount_minor OR r.currency<>i.currency OR r.wallet_id<>i.wallet_id)`},
		{"SETTLEMENT_MISMATCH", `SELECT intent_id::text FROM payment_intents i WHERE provider_account_id=$1 AND ((settlement_state='SETTLED') IS DISTINCT FROM EXISTS(SELECT 1 FROM transaction_funding f WHERE f.intent_id=i.intent_id))`},
		{"STALE_UNKNOWN_COLLECTION", `SELECT intent_id::text FROM payment_intents WHERE provider_account_id=$1 AND state IN ('SUBMITTING','UNKNOWN_OUTCOME','RECONCILIATION_EXCEPTION') AND updated_at<now()-$2::interval`},
		{"STALE_UNKNOWN_PAYOUT", `SELECT payout_id::text FROM payout_attempts WHERE provider_account_id=$1 AND state NOT IN ('CONFIRMED_PAID','CONFIRMED_FAILED') AND updated_at<now()-$2::interval`},
		{"STALE_UNKNOWN_REFUND", `SELECT refund_id::text FROM refund_attempts WHERE provider_account_id=$1 AND state NOT IN ('CONFIRMED_REFUNDED','CONFIRMED_FAILED','CANCELLED') AND updated_at<now()-$2::interval`},
		{"MISSING_PAYOUT_JOURNAL", `SELECT payout_id::text FROM payout_attempts WHERE provider_account_id=$1 AND state IN ('CONFIRMED_PAID','CONFIRMED_FAILED') AND terminal_journal_id IS NULL`},
		{"MISSING_REFUND_JOURNAL", `SELECT refund_id::text FROM refund_attempts WHERE provider_account_id=$1 AND state IN ('CONFIRMED_REFUNDED','CONFIRMED_FAILED','CANCELLED') AND terminal_journal_id IS NULL`},
		{"UNMATCHED_DVA_CREDIT", `SELECT credit_id::text FROM unmatched_dva_credits c WHERE provider_account_id=$1 AND NOT EXISTS(SELECT 1 FROM unmatched_dva_resolutions r WHERE r.credit_id=c.credit_id)`},
		{"DEAD_LETTER", `SELECT inbox_id::text FROM provider_inbox WHERE provider_account_id=$1 AND status='DEAD'`},
		{"NEGATIVE_CONTROLLED_BALANCE", `SELECT account_id::text FROM account_balances WHERE pool_id=(SELECT pool_id FROM custody_pools WHERE provider_account_id=$1) AND kind NOT IN ('TREASURY','PROCESSING_EXPENSE','REVENUE') AND balance_minor<0`},
		{"CASH_COVERAGE_DEFICIT", `SELECT p.pool_id::text FROM custody_pools p JOIN account_balances b USING(pool_id) WHERE provider_account_id=$1 GROUP BY p.pool_id HAVING COALESCE(sum(balance_minor) FILTER(WHERE kind='SETTLED_CASH'),0)-p.reserve_minor<COALESCE(sum(balance_minor) FILTER(WHERE kind IN ('AVAILABLE','PROTECTED','DEPOSIT','FROZEN','REFUND_PENDING','WITHDRAWAL_PENDING','RESERVE','SETTLEMENT_CONTROL','PROVIDER_PAYABLE')),0)`},
		{"UNASSIGNED_RECOVERABLE", `SELECT r.recoverable_id::text FROM financial_recoverables r JOIN financial_reversals v USING(reversal_id) WHERE v.provider_account_id=$1 AND r.wallet_id IS NULL AND NOT EXISTS(SELECT 1 FROM recoverable_actions a WHERE a.recoverable_id=r.recoverable_id AND a.action IN ('ASSIGN_BENEFICIARY','PLATFORM_LIABILITY'))`},
	}
	for _, c := range checks {
		args := []any{in.ProviderID}
		if has([]string{"STALE_UNKNOWN_COLLECTION", "STALE_UNKNOWN_PAYOUT", "STALE_UNKNOWN_REFUND"}, c.kind) {
			args = append(args, (time.Duration(in.StaleAfterSeconds) * time.Second).String())
		}
		rows, e := tx.Query(ctx, c.sql, args...)
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var ref string
			if e = rows.Scan(&ref); e != nil {
				rows.Close()
				return out, e
			}
			add(c.kind, ref, "canonical matched evidence", "requires review")
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
	}
	// Provider read-back compares every original reference; observations never
	// mutate completion state or release a reservation through reconciliation.
	rows, e := tx.Query(ctx, `SELECT provider_reference,state,amount_minor,currency,intent_id::text FROM payment_intents WHERE provider_account_id=$1 AND NOT route_snapshot ? 'funding_source' ORDER BY created_at LIMIT 1000`, in.ProviderID)
	if e != nil {
		return out, e
	}
	type external struct {
		ref, state, currency, id string
		amount                   int64
	}
	var payments []external
	for rows.Next() {
		var v external
		if e = rows.Scan(&v.ref, &v.state, &v.amount, &v.currency, &v.id); e != nil {
			rows.Close()
			return out, e
		}
		payments = append(payments, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, v := range payments {
		if adapter.Collection == nil {
			add("MISSING_PROVIDER_COLLECTION", v.id, v.ref, "unavailable")
			continue
		}
		r, e := adapter.Collection.VerifyCharge(ctx, v.ref)
		if e != nil {
			add("MISSING_PROVIDER_COLLECTION", v.id, v.ref, "readback unavailable")
		} else if r.Reference != v.ref || r.AmountMinor != v.amount || r.Currency != v.currency || r.Mode != cfg.Mode || formatID(r.MerchantID) != cfg.MerchantRef || r.IntentID != v.id {
			add("PROVIDER_REFERENCE_MISMATCH", v.id, v.ref, r.Reference)
		} else if v.state == "VERIFIED_SUCCESS" && r.State != "VERIFIED_SUCCESS" {
			add("CHARGEBACK_OR_REVERSAL_REVIEW", v.id, v.state, r.State)
		}
	}
	rows, e = tx.Query(ctx, `SELECT payout_id,provider_reference,state,amount_minor,currency,recipient_ref FROM payout_attempts WHERE provider_account_id=$1 ORDER BY created_at LIMIT 1000`, in.ProviderID)
	if e != nil {
		return out, e
	}
	var payouts []Payout
	for rows.Next() {
		var p Payout
		if e = rows.Scan(&p.ID, &p.Reference, &p.State, &p.AmountMinor, &p.Currency, &p.RecipientRef); e != nil {
			rows.Close()
			return out, e
		}
		payouts = append(payouts, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, p := range payouts {
		if adapter.Payout == nil {
			add("MISSING_PROVIDER_PAYOUT", p.ID.String(), p.Reference, "unavailable")
			continue
		}
		v, e := adapter.Payout.VerifyTransfer(ctx, p.Reference)
		if e != nil {
			add("MISSING_PROVIDER_PAYOUT", p.ID.String(), p.Reference, "readback unavailable")
		} else if v.Reference != p.Reference || v.AmountMinor != p.AmountMinor || v.Currency != p.Currency || v.Mode != "test" || v.RecipientRef != p.RecipientRef || formatID(v.MerchantID) != cfg.MerchantRef {
			add("PAYOUT_CONTEXT_MISMATCH", p.ID.String(), p.Reference, v.Reference)
		} else if v.State != p.State && !has([]string{"SUBMITTING", "UNKNOWN_OUTCOME", "RESERVED"}, p.State) {
			var reversed bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM financial_reversals WHERE payout_id=$1)`, p.ID).Scan(&reversed); e != nil {
				return out, e
			}
			if !reversed {
				add("PAYOUT_STATE_MISMATCH", p.ID.String(), p.State, v.State)
			}
		}
	}
	rows, e = tx.Query(ctx, `SELECT refund_id FROM refund_attempts WHERE provider_account_id=$1 AND snapshot->>'destination_strategy'='ORIGINAL_INSTRUMENT' AND state<>'CANCELLED' ORDER BY created_at LIMIT 1000`, in.ProviderID)
	if e != nil {
		return out, e
	}
	var refundIDs []ids.ID
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		refundIDs = append(refundIDs, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, id := range refundIDs {
		r, e := s.refundRow(ctx, tx, id)
		if e != nil {
			return out, e
		}
		if adapter.Refund == nil {
			add("MISSING_PROVIDER_REFUND", id.String(), r.AttemptReference, "unavailable")
			continue
		}
		req := refundProviderRequest(r)
		v, e := adapter.Refund.VerifyRefund(ctx, req)
		if e != nil {
			add("MISSING_PROVIDER_REFUND", id.String(), r.AttemptReference, "readback unavailable")
		} else if v.Reference != r.AttemptReference || v.ChargeReference != r.OriginalReference || v.AmountMinor != r.AmountMinor || v.Currency != r.Currency || v.Mode != cfg.Mode || formatID(v.MerchantID) != cfg.MerchantRef {
			add("REFUND_CONTEXT_MISMATCH", id.String(), r.AttemptReference, v.Reference)
		} else if v.State != r.State && !has([]string{"SUBMITTED", "UNKNOWN_OUTCOME", "RESERVED"}, r.State) {
			add("REFUND_STATE_MISMATCH", id.String(), r.State, v.State)
		}
	}
	if len(payments) == 1000 || len(payouts) == 1000 || len(refundIDs) == 1000 {
		add("RECONCILIATION_BATCH_LIMIT", pool.String(), "complete observation", "continue bounded range")
	}
	fundingFindings, e := s.reconcileFunding(ctx, tx, in.ProviderID, adapter.Settlement)
	if e != nil {
		return out, e
	}
	out.Findings = append(out.Findings, fundingFindings...)
	if restrictPool {
		if _, e = tx.Exec(ctx, `UPDATE custody_pools SET restricted=true WHERE pool_id=$1`, pool); e != nil {
			return out, e
		}
	}
	if len(out.Findings) > 0 {
		out.Status = "EXCEPTIONS"
	}
	_, e = tx.Exec(ctx, `INSERT INTO reconciliation_runs(run_id,provider_account_id,snapshot) VALUES($1,$2,$3)`, in.ID, in.ProviderID, raw(out))
	if e != nil {
		return out, e
	}
	for _, f := range out.Findings {
		_, e = tx.Exec(ctx, `INSERT INTO reconciliation_findings(finding_id,run_id,kind,aggregate_ref,snapshot) VALUES($1,$2,$3,$4,$5)`, ids.New(), in.ID, f.Kind, f.AggregateRef, raw(f))
		if e != nil {
			return out, e
		}
	}
	if e = s.Audit(ctx, tx, "reconciliation.completed", in.ID.String(), map[string]any{"status": out.Status, "findings": len(out.Findings)}); e != nil {
		return out, e
	}
	if e = s.Emit(ctx, tx, "finance.reconciliation_completed.v1", "reconciliation", in.ID, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

func (s *Service) OperationalMetrics(ctx context.Context) (map[string]string, error) {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return nil, e
	}
	queries := map[string]string{
		"deposit_balance_minor":     `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE kind='DEPOSIT'`,
		"open_disputes":             `SELECT count(*)::text FROM financial_cases WHERE state<>'CLOSED'`,
		"disputed_minor":            `SELECT COALESCE(sum(contested_minor),0)::text FROM financial_cases WHERE state<>'CLOSED'`,
		"oldest_claim_age_seconds":  `SELECT COALESCE(max(extract(epoch from now()-created_at)),0)::bigint::text FROM financial_cases WHERE state<>'CLOSED'`,
		"refund_reservations_minor": `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE kind='REFUND_PENDING'`,
		"unknown_refunds":           `SELECT count(*)::text FROM refund_attempts WHERE state IN ('UNKNOWN_OUTCOME','SUBMITTED','REVIEW_REQUIRED')`,
		"chargeback_minor":          `SELECT COALESCE(sum(amount_minor),0)::text FROM financial_reversals WHERE intent_id IS NOT NULL`,
		"recoverables_minor":        `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE kind='RECOVERABLE'`,
		"destination_changes":       `SELECT count(*)::text FROM destination_versions`,
		"cooling_destinations":      `SELECT count(*)::text FROM destination_versions WHERE effective_at>now()`,
		"unknown_payouts":           `SELECT count(*)::text FROM payout_attempts WHERE state IN ('SUBMITTING','UNKNOWN_OUTCOME','REVIEW_REQUIRED')`,
		"topups_pending":            `SELECT count(*)::text FROM customer_topups t WHERE NOT EXISTS(SELECT 1 FROM customer_funding f WHERE f.topup_id=t.topup_id)`,
		"unmatched_credits":         `SELECT count(*)::text FROM unmatched_dva_credits c WHERE NOT EXISTS(SELECT 1 FROM unmatched_dva_resolutions r WHERE r.credit_id=c.credit_id)`,
		"reconciliation_exceptions": `SELECT count(*)::text FROM reconciliation_findings f WHERE NOT EXISTS(SELECT 1 FROM reconciliation_resolutions r WHERE r.finding_id=f.finding_id)`,
		"dead_readbacks":            `SELECT count(*)::text FROM original_readback_jobs WHERE state='DEAD'`,
		"dead_outbox":               `SELECT count(*)::text FROM outbox_events WHERE dead_at IS NOT NULL`,
		"dead_letters":              `SELECT count(*)::text FROM provider_inbox WHERE status='DEAD'`,
		"wallet_payment_failures":   `SELECT count(*)::text FROM financial_audit WHERE action='customer.wallet_spend_rejected'`,
	}
	out := map[string]string{}
	rows, e := s.DB.Query(ctx, `SELECT DISTINCT currency FROM custody_pools ORDER BY currency`)
	if e != nil {
		return nil, e
	}
	var currencies []string
	for rows.Next() {
		var currency string
		if e = rows.Scan(&currency); e != nil {
			rows.Close()
			return nil, e
		}
		currencies = append(currencies, currency)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out["money_currency"] = "MIXED"
	if len(currencies) == 1 {
		out["money_currency"] = currencies[0]
	}

	for k, sql := range queries {
		if strings.HasSuffix(k, "_minor") {
			expression := "currency"
			if k == "disputed_minor" || k == "chargeback_minor" {
				expression = "snapshot->>'currency'"
			}
			for _, currency := range currencies {
				var value string
				if e := s.DB.QueryRow(ctx, sql+" AND "+expression+"=$1", currency).Scan(&value); e != nil {
					return nil, e
				}
				out[k+"."+currency] = value
			}
			if len(currencies) != 1 {
				continue
			}
		}

		var v string
		if e := s.DB.QueryRow(ctx, sql).Scan(&v); e != nil {
			return nil, e
		}
		out[k] = v
	}
	return out, nil
}
func (s *Service) ResolveUnmatchedDVA(ctx context.Context, id ids.ID, ref string, approvers []string) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if ref == "" || !dual(approvers) {
		return fail("UNMATCHED_CREDIT_AUTHORITY_REQUIRED")
	}
	var provider ids.ID
	var original string
	if e := s.DB.QueryRow(ctx, `SELECT provider_account_id,external_ref FROM unmatched_dva_credits WHERE credit_id=$1`, id).Scan(&provider, &original); e != nil {
		return e
	}
	// Resolution succeeds only once the normal verified mapping matches. No
	// administrator-selected wallet or sender-name attribution is accepted.
	if e := s.MatchDVACharge(trusted(ctx), provider, original); e != nil {
		return e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO unmatched_dva_resolutions(credit_id,snapshot) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, raw(map[string]any{"authority_ref": ref, "approvers": approvers}))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "dva.unmatched_resolved", id.String(), map[string]string{"authority_ref": ref}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
