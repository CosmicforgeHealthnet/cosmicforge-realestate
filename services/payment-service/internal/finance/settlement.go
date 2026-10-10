package finance

import (
	"context"
	"encoding/json"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// ObserveSettlement retains immutable external evidence. It does not release
// funds, fabricate settled cash or allocate a merchant balance to a user.
func (s *Service) ObserveSettlement(ctx context.Context, provider ids.ID, ref string) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	a, e := s.Adapter(provider)
	if e != nil {
		return e
	}
	if a.Settlement == nil {
		return core.Fail(503, "SETTLEMENT_UNAVAILABLE")
	}
	c, e := s.dvaConfig(ctx, provider)
	if e != nil {
		return e
	}
	v, e := a.Settlement.FetchSettlement(ctx, ref)
	if e != nil {
		return core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	if !contains(c.Currencies, v.Currency) || v.Reference != ref || v.AmountMinor < 0 || v.FeeMinor < 0 {
		return core.Fail(409, "SETTLEMENT_CONTEXT_MISMATCH")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	b, _ := json.Marshal(v)
	_, e = tx.Exec(ctx, `INSERT INTO settlement_observations(observation_id,pool_id,external_ref,amount_minor,fee_minor,status,evidence,evidence_fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(pool_id,external_ref,evidence_fingerprint) DO NOTHING`, ids.New(), c.PoolID, ref, v.AmountMinor, v.FeeMinor, v.State, string(b), core.Fingerprint(v))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "settlement.observed", ref, map[string]string{"provider_account_id": provider.String()}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type Coverage struct {
	PoolID           ids.ID `json:"pool_id"`
	SettledCashMinor string `json:"settled_cash_minor"`
	ReceivableMinor  string `json:"receivable_minor"`
	LiabilityMinor   string `json:"liability_minor"`
	ReserveMinor     int64  `json:"reserve_minor"`
	Restricted       bool   `json:"restricted"`
	CustodyApproved  bool   `json:"custody_approved"`
}

func (s *Service) Coverage(ctx context.Context, pool ids.ID) (x Coverage, e error) {
	if e = core.Require(ctx, platform.AdminService); e != nil {
		return
	}
	x.PoolID = pool
	e = s.DB.QueryRow(ctx, `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='SETTLED_CASH'),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind='PROVIDER_RECEIVABLE'),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT')),0)::text,p.reserve_minor,p.restricted,p.custody_approved FROM custody_pools p LEFT JOIN account_balances b USING(pool_id) WHERE p.pool_id=$1 GROUP BY p.pool_id`, pool).Scan(&x.SettledCashMinor, &x.ReceivableMinor, &x.LiabilityMinor, &x.ReserveMinor, &x.Restricted, &x.CustodyApproved)
	return
}
func (s *Service) ReconcileBalance(ctx context.Context, provider ids.ID) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	a, e := s.Adapter(provider)
	if e != nil {
		return e
	}
	if a.Settlement == nil {
		return core.Fail(503, "SETTLEMENT_UNAVAILABLE")
	}
	c, e := s.dvaConfig(ctx, provider)
	if e != nil {
		return e
	}
	balances, e := a.Settlement.FetchBalance(ctx)
	if e != nil {
		return core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	// A balance observation does not prove which individual charge is settled.
	matched := false
	for _, b := range balances {
		if !contains(c.Currencies, b.Currency) {
			continue
		}
		matched = true
		tx, e := s.DB.Begin(ctx)
		if e != nil {
			return e
		}
		var expected string
		e = tx.QueryRow(ctx, `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE pool_id=$1 AND kind='SETTLED_CASH'`, c.PoolID).Scan(&expected)
		if e != nil {
			tx.Rollback(ctx)
			return e
		}
		if expected != intToString(b.AmountMinor) {
			id := ids.New()
			_, e = tx.Exec(ctx, `INSERT INTO reconciliation_exceptions(exception_id,pool_id,reason,expected_minor,observed_minor) VALUES($1,$2,'MERCHANT_BALANCE_MISMATCH',$3,$4)`, id, c.PoolID, expected, b.AmountMinor)
			if e == nil {
				_, e = tx.Exec(ctx, `UPDATE custody_pools SET restricted=true WHERE pool_id=$1`, c.PoolID)
			}
			if e == nil {
				e = s.Emit(ctx, tx, "finance.reconciliation.exception_detected.v1", "custody_pool", c.PoolID, map[string]any{"exception_id": id, "pool_id": c.PoolID, "reason": "MERCHANT_BALANCE_MISMATCH"})
			}
		}
		if e == nil {
			e = s.Audit(ctx, tx, "balance.reconciled", c.PoolID.String(), map[string]any{"provider": provider.String(), "expected_minor": expected, "observed_minor": b.AmountMinor, "currency": b.Currency})
		}
		if e != nil {
			tx.Rollback(ctx)
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
	}
	if !matched {
		return core.Fail(503, "PROVIDER_BALANCE_UNAVAILABLE")
	}
	return nil
}
