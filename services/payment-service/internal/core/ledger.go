package core

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sort"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
)

var Views = []string{"PAYMENT_PENDING", "PENDING_EARNINGS", "PROTECTED", "AVAILABLE", "FROZEN", "WITHDRAWAL_PENDING", "REFUND_PENDING"}

type Entry struct {
	AccountID   ids.ID `json:"account_id"`
	Side        string `json:"side"`
	AmountMinor int64  `json:"amount_minor"`
}
type Posting struct {
	OperationRef  string            `json:"operation_ref"`
	LegalEntity   string            `json:"legal_entity"`
	Currency      string            `json:"currency"`
	PoolID        ids.ID            `json:"pool_id"`
	PolicyVersion string            `json:"policy_version"`
	Authorization map[string]string `json:"authorization"`
	Entries       []Entry           `json:"entries"`
	ReversesID    *ids.ID           `json:"reverses_id,omitempty"`
}

// Post is an internal trusted primitive, deliberately not an HTTP endpoint.
// Payment service alone may call it after workflow/evidence authorization.
func (s *Service) Post(ctx context.Context, p Posting) (ids.ID, error) {
	if e := Require(ctx, platform.PaymentService); e != nil {
		return ids.ID{}, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return ids.ID{}, e
	}
	defer tx.Rollback(ctx)
	id, e := s.PostTx(ctx, tx, p)
	if e != nil {
		return ids.ID{}, e
	}
	return id, tx.Commit(ctx)
}
func (s *Service) PostTx(ctx context.Context, tx pgx.Tx, p Posting) (ids.ID, error) {
	id, e := s.postTx(ctx, tx, p)
	recordCommand(ctx, "journal.post", e)
	return id, e
}
func (s *Service) postTx(ctx context.Context, tx pgx.Tx, p Posting) (ids.ID, error) {
	if e := Require(ctx, platform.PaymentService); e != nil {
		return ids.ID{}, e
	}
	if p.OperationRef == "" || p.PolicyVersion == "" || p.Authorization["evidence_ref"] == "" || p.Authorization["approved_by"] == "" || !validCurrency(p.Currency) || len(p.Entries) < 2 {
		return ids.ID{}, Fail(422, "INVALID_POSTING")
	}
	total := new(big.Int)
	keys := make([]string, 0, len(p.Entries))
	for _, e := range p.Entries {
		if e.AccountID.ValidateV7() != nil || e.AmountMinor <= 0 || (e.Side != "DEBIT" && e.Side != "CREDIT") {
			return ids.ID{}, Fail(422, "INVALID_POSTING")
		}
		n := big.NewInt(e.AmountMinor)
		if e.Side == "CREDIT" {
			n.Neg(n)
		}
		total.Add(total, n)
		keys = append(keys, e.AccountID.String())
	}
	if total.Sign() != 0 {
		return ids.ID{}, Fail(422, "UNBALANCED_JOURNAL")
	}
	fp := Fingerprint(p)
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.OperationRef)
	if err != nil {
		return ids.ID{}, err
	}
	var existing ids.ID
	var fingerprint string
	err = tx.QueryRow(ctx, `SELECT journal_id,fingerprint FROM journals WHERE operation_ref=$1`, p.OperationRef).Scan(&existing, &fingerprint)
	if err == nil {
		if fp != fingerprint {
			return ids.ID{}, Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.ID{}, err
	}
	// Pool lock protects coverage and sorted account locks prevent overspend.
	var restricted bool
	var entity, currency string
	err = tx.QueryRow(ctx, `SELECT restricted,legal_entity,currency FROM custody_pools WHERE pool_id=$1 FOR UPDATE`, p.PoolID).Scan(&restricted, &entity, &currency)
	if err != nil {
		return ids.ID{}, err
	}
	if entity != p.LegalEntity || currency != p.Currency {
		return ids.ID{}, Fail(422, "LEDGER_DIMENSION_MISMATCH")
	}
	sort.Strings(keys)
	for _, key := range keys {
		var id ids.ID
		err = tx.QueryRow(ctx, `SELECT account_id FROM ledger_accounts WHERE account_id=$1 FOR UPDATE`, key).Scan(&id)
		if err != nil {
			return ids.ID{}, err
		}
	}
	var restrictedWallet bool
	// Serialize identity restrictions with wallet postings. Every posting takes
	// the identity lock, including pending liabilities and closure checks.
	identityRows, lockErr := tx.Query(ctx, `SELECT i.identity_id FROM financial_identities i WHERE i.identity_id IN (SELECT w.identity_id FROM wallets w JOIN ledger_accounts a USING(wallet_id) WHERE a.account_id=ANY($1::uuid[])) ORDER BY i.identity_id FOR UPDATE`, keys)
	if lockErr != nil {
		return ids.ID{}, lockErr
	}
	for identityRows.Next() {
		var locked ids.ID
		if lockErr = identityRows.Scan(&locked); lockErr != nil {
			identityRows.Close()
			return ids.ID{}, lockErr
		}
	}
	lockErr = identityRows.Err()
	identityRows.Close()
	if lockErr != nil {
		return ids.ID{}, lockErr
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ledger_accounts a JOIN wallets w USING(wallet_id) JOIN financial_identities i USING(identity_id) WHERE a.account_id=ANY($1::uuid[]) AND a.kind='AVAILABLE' AND i.state<>'ACTIVE')`, keys).Scan(&restrictedWallet)
	if err != nil {
		return ids.ID{}, err
	}
	compensatingDebit := p.Authorization["confirmed_reversal_ref"] != "" || p.Authorization["approved_recovery_ref"] != ""
	for _, entry := range p.Entries {
		var kind string
		if err = tx.QueryRow(ctx, `SELECT kind FROM ledger_accounts WHERE account_id=$1`, entry.AccountID).Scan(&kind); err != nil {
			return ids.Nil, err
		}
		if kind == "AVAILABLE" && entry.Side != "DEBIT" {
			compensatingDebit = false
		}
	}
	if restrictedWallet && !compensatingDebit {
		return ids.ID{}, Fail(403, "FINANCIAL_IDENTITY_RESTRICTED")
	}
	if restricted {
		for _, e := range p.Entries {
			var kind string
			if err = tx.QueryRow(ctx, `SELECT kind FROM ledger_accounts WHERE account_id=$1`, e.AccountID).Scan(&kind); err != nil {
				return ids.ID{}, err
			}
			if kind == "AVAILABLE" && !compensatingDebit {
				return ids.ID{}, Fail(409, "UNFUNDED_RELEASE")
			}
		}
	}
	id := ids.New()
	auth, _ := json.Marshal(p.Authorization)
	_, err = tx.Exec(ctx, `INSERT INTO journals(journal_id,operation_ref,fingerprint,legal_entity,currency,pool_id,origin_service,policy_version,authorization_snapshot,reverses_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, p.OperationRef, fp, p.LegalEntity, p.Currency, p.PoolID, platform.PaymentService, p.PolicyVersion, string(auth), p.ReversesID)
	if err != nil {
		return ids.ID{}, err
	}
	for _, e := range p.Entries {
		_, err = tx.Exec(ctx, `INSERT INTO journal_entries(entry_id,journal_id,account_id,side,amount_minor) VALUES($1,$2,$3,$4,$5)`, ids.New(), id, e.AccountID, e.Side, e.AmountMinor)
		if err != nil {
			return ids.ID{}, err
		}
	}
	var invalid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_balances WHERE pool_id=$1 AND kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','SETTLEMENT_CONTROL','RESERVE','PROVIDER_PAYABLE','RECOVERABLE','SETTLED_CASH','PROVIDER_RECEIVABLE') AND balance_minor<0)`, p.PoolID).Scan(&invalid)
	if err != nil {
		return ids.ID{}, err
	}
	if invalid {
		return ids.ID{}, Fail(409, "INSUFFICIENT_FUNDS")
	}
	var newAvailable bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journal_entries e JOIN ledger_accounts a USING(account_id) WHERE e.journal_id=$1 AND a.kind IN ('AVAILABLE','REVENUE') AND e.side='CREDIT')`, id).Scan(&newAvailable)
	if err != nil {
		return ids.ID{}, err
	}
	if newAvailable {
		var covered bool
		err = tx.QueryRow(ctx, `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='SETTLED_CASH'),0)-p.reserve_minor >= COALESCE(sum(balance_minor) FILTER(WHERE kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','AVAILABLE','PROTECTED','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','SETTLEMENT_CONTROL','RESERVE','PROVIDER_PAYABLE')),0) FROM account_balances JOIN custody_pools p USING(pool_id) WHERE pool_id=$1 GROUP BY p.reserve_minor`, p.PoolID).Scan(&covered)
		if err != nil {
			return ids.ID{}, err
		}
		if restricted || !covered {
			return ids.ID{}, Fail(409, "UNFUNDED_RELEASE")
		}
	}
	if err = s.Emit(ctx, tx, "finance.journal.posted.v1", "journal", id, map[string]any{"journal_id": id, "operation_ref": p.OperationRef, "currency": p.Currency, "legal_entity": p.LegalEntity}); err != nil {
		return ids.ID{}, err
	}
	return id, nil
}
func (s *Service) Account(ctx context.Context, tx pgx.Tx, wallet *ids.ID, pool ids.ID, kind, entity, currency, dimension string) (ids.ID, error) {
	side := "CREDIT"
	if kind == "PROVIDER_RECEIVABLE" || kind == "SETTLED_CASH" || kind == "PROCESSING_EXPENSE" || kind == "RECOVERABLE" {
		side = "DEBIT"
	}
	id := ids.New()
	_, e := tx.Exec(ctx, `INSERT INTO ledger_accounts(account_id,wallet_id,pool_id,kind,legal_entity,currency,normal_side,dimension_ref) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, id, wallet, pool, kind, entity, currency, side, dimension)
	if e != nil {
		return ids.ID{}, e
	}
	e = tx.QueryRow(ctx, `SELECT account_id FROM ledger_accounts WHERE wallet_id IS NOT DISTINCT FROM $1 AND pool_id=$2 AND kind=$3 AND dimension_ref=$4`, wallet, pool, kind, dimension).Scan(&id)
	return id, e
}

// Reverse loads immutable original entries; caller cannot supply replacement amounts.
func (s *Service) Reverse(ctx context.Context, id ids.ID, ref, policy, evidence, approvedBy string) (ids.ID, error) {
	if e := Require(ctx, platform.PaymentService); e != nil {
		return ids.ID{}, e
	}
	p := Posting{OperationRef: ref, PolicyVersion: policy, Authorization: map[string]string{"evidence_ref": evidence, "approved_by": approvedBy}, ReversesID: &id}
	e := s.DB.QueryRow(ctx, `SELECT legal_entity,currency,pool_id FROM journals WHERE journal_id=$1`, id).Scan(&p.LegalEntity, &p.Currency, &p.PoolID)
	if e != nil {
		return ids.ID{}, e
	}
	rows, e := s.DB.Query(ctx, `SELECT account_id,CASE WHEN side='DEBIT' THEN 'CREDIT' ELSE 'DEBIT' END,amount_minor FROM journal_entries WHERE journal_id=$1 ORDER BY entry_id`, id)
	if e != nil {
		return ids.ID{}, e
	}
	defer rows.Close()
	for rows.Next() {
		var x Entry
		if e = rows.Scan(&x.AccountID, &x.Side, &x.AmountMinor); e != nil {
			return ids.ID{}, e
		}
		p.Entries = append(p.Entries, x)
	}
	if e = rows.Err(); e != nil {
		return ids.ID{}, e
	}
	return s.Post(ctx, p)
}
