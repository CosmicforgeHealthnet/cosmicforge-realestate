package lifecycle

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"strings"
)

type RecoverableAction struct {
	ID              ids.ID   `json:"action_id"`
	RecoverableID   ids.ID   `json:"recoverable_id"`
	WalletID        *ids.ID  `json:"wallet_id,omitempty"`
	SourceAccountID *ids.ID  `json:"source_account_id,omitempty"`
	Action          string   `json:"action"`
	AmountMinor     int64    `json:"amount_minor"`
	AuthorityRef    string   `json:"authority_ref"`
	RiskRef         string   `json:"risk_ref"`
	Approvers       []string `json:"approvers"`
	EvidenceIDs     []ids.ID `json:"evidence_ids"`
}

// Recovery never implies an external cash receipt. It reduces an existing
// recoverable using approved reserve or the liable party's retained proceeds.
func (s *Service) RecoverableAction(ctx context.Context, in RecoverableAction, key string) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if in.ID.ValidateV7() != nil || key == "" || len(key) > 128 || in.AuthorityRef == "" || in.RiskRef == "" || !dual(in.Approvers) || in.AmountMinor < 0 || !has([]string{"ASSIGN_BENEFICIARY", "PLATFORM_LIABILITY", "USE_RESERVE", "RECOVER_FROM_PROCEEDS", "LEGAL_ESCALATION"}, in.Action) {
		return fail("RECOVERY_AUTHORITY_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	op := "recoverable:" + in.RecoverableID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM recoverable_actions WHERE operation_key=$1`, op).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	var reversal, intent ids.ID
	e = tx.QueryRow(ctx, `SELECT r.reversal_id,v.intent_id FROM financial_recoverables r JOIN financial_reversals v USING(reversal_id) WHERE recoverable_id=$1 FOR UPDATE OF r`, in.RecoverableID).Scan(&reversal, &intent)
	if e != nil {
		return e
	}
	var allocationID ids.ID
	var a Allocation
	var q Quote
	var topupWallet *ids.ID
	e = tx.QueryRow(ctx, `SELECT allocation_id FROM allocation_snapshots WHERE intent_id=$1 ORDER BY version DESC LIMIT 1`, intent).Scan(&allocationID)
	if e == nil {
		a, e = s.allocation(ctx, tx, allocationID)
		if e != nil {
			return e
		}
		if e = load(ctx, tx, `SELECT snapshot FROM locked_quotes WHERE quote_id=$1`, a.QuoteID, &q); e != nil {
			return e
		}
	} else if errors.Is(e, pgx.ErrNoRows) {
		var topup ids.ID
		var wallet ids.ID
		e = tx.QueryRow(ctx, `SELECT t.topup_id,t.wallet_id,t.policy_id,i.pool_id,i.currency,i.legal_entity FROM customer_topups t JOIN payment_intents i USING(intent_id) WHERE i.intent_id=$1`, intent).Scan(&topup, &wallet, &a.PolicyID, &a.PoolID, &a.Currency, &a.LegalEntity)
		if e != nil {
			return core.Fail(409, "RECOVERY_CANONICAL_CONTEXT_REQUIRED")
		}
		a.IntentID = intent
		topupWallet = &wallet
		q.BusinessRef = "topup:" + topup.String()
	} else {
		return e
	}
	rp, e := s.remedyPolicy(ctx, tx, a.PolicyID)
	if e != nil {
		return e
	}
	if e = s.checkEvidence(ctx, tx, rp, q.BusinessRef, in.EvidenceIDs); e != nil {
		return e
	}
	if in.Action == "ASSIGN_BENEFICIARY" {
		if in.WalletID == nil || in.AmountMinor != 0 || in.SourceAccountID != nil {
			return fail("RECOVERY_ASSIGNMENT_REQUIRED")
		}
		var party bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM allocation_lines l JOIN ledger_accounts a ON a.account_id=l.protected_account_id WHERE allocation_id=$1 AND l.wallet_id=$2 AND a.kind='PROTECTED')`, allocationID, in.WalletID).Scan(&party)
		if e != nil {
			return e
		}
		if topupWallet != nil && *in.WalletID == *topupWallet {
			party = true
		}
		if !party {
			return core.Fail(403, "RECOVERY_PARTY_MISMATCH")
		}
		var assigned bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recoverable_actions WHERE recoverable_id=$1 AND action IN ('ASSIGN_BENEFICIARY','PLATFORM_LIABILITY'))`, in.RecoverableID).Scan(&assigned); e != nil {
			return e
		}
		if assigned {
			return core.Fail(409, "RECOVERY_LIABILITY_ALREADY_ASSIGNED")
		}
	}
	if in.Action == "PLATFORM_LIABILITY" {
		var assigned bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recoverable_actions WHERE recoverable_id=$1 AND action IN ('ASSIGN_BENEFICIARY','PLATFORM_LIABILITY'))`, in.RecoverableID).Scan(&assigned); e != nil {
			return e
		}
		if assigned {
			return core.Fail(409, "RECOVERY_LIABILITY_ALREADY_ASSIGNED")
		}
	}
	var journal *ids.ID
	if has([]string{"USE_RESERVE", "RECOVER_FROM_PROCEEDS"}, in.Action) {
		if in.AmountMinor <= 0 || in.SourceAccountID == nil {
			return fail("RECOVERY_SOURCE_REQUIRED")
		}
		asset, e := s.Account(trusted(ctx), tx, nil, a.PoolID, "RECOVERABLE", a.LegalEntity, a.Currency, "reversal:"+reversal.String())
		if e != nil {
			return e
		}
		var kind, dimension string
		var wallet *ids.ID
		var pool ids.ID
		e = tx.QueryRow(ctx, `SELECT kind,wallet_id,pool_id,dimension_ref FROM ledger_accounts WHERE account_id=$1`, in.SourceAccountID).Scan(&kind, &wallet, &pool, &dimension)
		if e != nil {
			return e
		}
		if pool != a.PoolID {
			return core.Fail(403, "RECOVERY_POOL_MISMATCH")
		}
		if in.Action == "USE_RESERVE" {
			if !rp.ReserveRecovery || wallet != nil || (kind != "TREASURY" && kind != "RESERVE") || !strings.HasPrefix(dimension, "approved-recovery-reserve:") {
				return core.Fail(403, "APPROVED_INTERNAL_RESERVE_REQUIRED")
			}
		}
		if in.Action == "RECOVER_FROM_PROCEEDS" {
			if !rp.RecoverFromProceeds || in.WalletID == nil || wallet == nil || *wallet != *in.WalletID || !has([]string{"AVAILABLE", "PROTECTED"}, kind) {
				return core.Fail(403, "RECOVERY_PROCEEDS_POLICY_REQUIRED")
			}
			var assigned bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recoverable_actions WHERE recoverable_id=$1 AND action='ASSIGN_BENEFICIARY' AND wallet_id=$2)`, in.RecoverableID, in.WalletID).Scan(&assigned); e != nil {
				return e
			}
			if !assigned {
				return core.Fail(403, "RECOVERY_LIABILITY_ASSIGNMENT_REQUIRED")
			}
			var eligible bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM allocation_lines l JOIN ledger_accounts a ON a.account_id=l.protected_account_id WHERE l.wallet_id=$1 AND a.kind='PROTECTED' AND $2 IN(protected_account_id,available_account_id))`, wallet, in.SourceAccountID).Scan(&eligible); e != nil {
				return e
			}
			if !eligible {
				return core.Fail(403, "RECOVERY_PROCEEDS_LINEAGE_REQUIRED")
			}
		}
		sourceBalance, e := accountBalance(ctx, tx, *in.SourceAccountID)
		if e != nil {
			return e
		}
		if in.AmountMinor > sourceBalance {
			return core.Fail(409, "INSUFFICIENT_RECOVERY_SOURCE")
		}
		n, e := accountBalance(ctx, tx, asset)
		if e != nil {
			return e
		}
		if in.AmountMinor > n {
			return core.Fail(409, "RECOVERY_CAP_EXCEEDED")
		}
		j, e := s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: op, PoolID: a.PoolID, LegalEntity: a.LegalEntity, Currency: a.Currency, PolicyVersion: rp.ApprovalRef, Authorization: map[string]string{"evidence_ref": in.AuthorityRef, "approved_by": raw(in.Approvers), "approved_recovery_ref": in.RiskRef}, Entries: []core.Entry{{AccountID: *in.SourceAccountID, Side: "DEBIT", AmountMinor: in.AmountMinor}, {AccountID: asset, Side: "CREDIT", AmountMinor: in.AmountMinor}}})
		if e != nil {
			return e
		}
		journal = &j
	} else if in.AmountMinor != 0 || in.SourceAccountID != nil {
		return fail("NONFINANCIAL_RECOVERY_AMOUNT_FORBIDDEN")
	}
	_, e = tx.Exec(ctx, `INSERT INTO recoverable_actions(action_id,recoverable_id,wallet_id,operation_key,fingerprint,action,amount_minor,snapshot,journal_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, in.ID, in.RecoverableID, in.WalletID, op, core.Fingerprint(in), in.Action, in.AmountMinor, raw(in), journal)
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "recoverable.action", in.RecoverableID.String(), in); e != nil {
		return e
	}
	if e = s.Emit(ctx, tx, "finance.recoverable_action.v1", "recoverable", in.RecoverableID, in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Service) outstandingRecovery(ctx context.Context, q query, wallet ids.ID) error {
	var outstanding bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recoverable_actions a JOIN financial_recoverables r USING(recoverable_id) JOIN ledger_accounts l ON l.dimension_ref='reversal:'||r.reversal_id::text JOIN account_balances b USING(account_id) WHERE a.action='ASSIGN_BENEFICIARY' AND a.wallet_id=$1 AND l.kind='RECOVERABLE' AND b.balance_minor>0)`, wallet).Scan(&outstanding)
	if e != nil {
		return e
	}
	if outstanding {
		return core.Fail(409, "RECOVERABLE_REVIEW_REQUIRED")
	}
	return nil
}
