package lifecycle

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// Hold moves only this beneficiary's liabilities. An in-flight external payout
// remains reserved; its final provider result is resolved independently.
func (s *Service) moveHold(ctx context.Context, tx pgx.Tx, a Allocation, line ids.ID, old, next, op, evidence string) (string, error) {
	var wallet *ids.ID
	var protected ids.ID
	var available *ids.ID
	var amount, released, reserved int64
	e := tx.QueryRow(ctx, `SELECT wallet_id,protected_account_id,available_account_id,amount_minor,released_minor,reserved_minor FROM allocation_lines WHERE line_id=$1`, line).Scan(&wallet, &protected, &available, &amount, &released, &reserved)
	if e != nil {
		return old, e
	}
	if wallet == nil {
		return next, nil
	} // Internal reserve/revenue account has no public spending capability.
	alreadyHeld := has([]string{"HELD", "DISPUTED", "REVIEW_REQUIRED"}, old)
	entries := []core.Entry{}
	for _, part := range []struct {
		name    string
		account *ids.ID
		amount  int64
	}{{"protected", &protected, amount - released}, {"available", available, released - reserved}} {
		if part.account == nil {
			continue
		}
		frozen, e := s.Account(trusted(ctx), tx, wallet, a.PoolID, "FROZEN", a.LegalEntity, a.Currency, "allocation:"+line.String()+":held-"+part.name)
		if e != nil {
			return old, e
		}
		if next == "PROTECTED" && alreadyHeld {
			var n int64
			e = tx.QueryRow(ctx, `SELECT balance_minor::bigint FROM account_balances WHERE account_id=$1`, frozen).Scan(&n)
			if e != nil {
				return old, e
			}
			if n > 0 {
				entries = append(entries, core.Entry{AccountID: frozen, Side: "DEBIT", AmountMinor: n}, core.Entry{AccountID: *part.account, Side: "CREDIT", AmountMinor: n})
			}
		}
		if next != "PROTECTED" && !alreadyHeld && part.amount > 0 {
			entries = append(entries, core.Entry{AccountID: *part.account, Side: "DEBIT", AmountMinor: part.amount}, core.Entry{AccountID: frozen, Side: "CREDIT", AmountMinor: part.amount})
		}
	}
	if len(entries) > 0 {
		_, e = s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: op, PoolID: a.PoolID, Currency: a.Currency, LegalEntity: a.LegalEntity, PolicyVersion: "allocation-hold-v1", Authorization: map[string]string{"evidence_ref": evidence, "approved_by": "authorized-domain-decision"}, Entries: entries})
		if e != nil {
			return old, e
		}
	}
	if next == "PROTECTED" && released > 0 {
		next = "AVAILABLE"
	}
	return next, nil
}
