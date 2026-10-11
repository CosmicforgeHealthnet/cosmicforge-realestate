package lifecycle

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"time"
)

type poolAdapter struct {
	finance.Adapter
	ProviderID ids.ID
}

func (s *Service) AdapterForPool(ctx context.Context, pool ids.ID) (poolAdapter, error) {
	var id ids.ID
	e := s.DB.QueryRow(ctx, `SELECT provider_account_id FROM custody_pools WHERE pool_id=$1`, pool).Scan(&id)
	if e != nil {
		return poolAdapter{}, e
	}
	a, e := s.Adapter(id)
	return poolAdapter{a, id}, e
}

type DestinationChange struct {
	ID              ids.ID    `json:"version_id"`
	WalletID        ids.ID    `json:"wallet_id"`
	PayoutAccountID ids.ID    `json:"payout_account_id"`
	PredecessorID   *ids.ID   `json:"predecessor_id,omitempty"`
	PolicyID        ids.ID    `json:"policy_id"`
	StepUpRef       string    `json:"step_up_ref"`
	VerificationRef string    `json:"verification_ref"`
	OwnershipRef    string    `json:"ownership_ref"`
	RiskRef         string    `json:"risk_ref"`
	NotificationRef string    `json:"notification_ref"`
	HighRisk        bool      `json:"high_risk"`
	Approvers       []string  `json:"approvers"`
	ExpiresAt       time.Time `json:"expires_at"`
	EffectiveAt     time.Time `json:"effective_at"`
	CoolingEndsAt   time.Time `json:"cooling_ends_at"`
}

func (s *Service) ChangeDestination(ctx context.Context, in DestinationChange) (DestinationChange, error) {
	if e := core.Require(ctx, platform.VerificationService); e != nil {
		return in, e
	}
	if in.ID.ValidateV7() != nil || in.StepUpRef == "" || in.VerificationRef == "" || in.OwnershipRef == "" || in.RiskRef == "" || in.NotificationRef == "" || in.HighRisk && !dual(in.Approvers) {
		return in, core.Fail(403, "DESTINATION_CHANGE_APPROVAL_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return in, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "destination-wallet:"+in.WalletID.String()); e != nil {
		return in, e
	}
	var prior DestinationChange
	e = load(ctx, tx, `SELECT snapshot FROM destination_versions WHERE version_id=$1`, in.ID, &prior)
	if e == nil {
		copy := prior
		copy.EffectiveAt = in.EffectiveAt
		copy.CoolingEndsAt = in.CoolingEndsAt
		if core.Fingerprint(copy) != core.Fingerprint(in) {
			return in, core.Fail(409, "DESTINATION_VERSION_IMMUTABLE")
		}
		return prior, nil
	}
	rp, e := s.remedyPolicy(ctx, tx, in.PolicyID)
	if e != nil {
		return in, e
	}
	policy, e := s.Policy(ctx, in.PolicyID)
	if e != nil {
		return in, e
	}
	in.EffectiveAt = time.Now().Add(time.Duration(rp.CoolingSeconds) * time.Second)
	in.CoolingEndsAt = in.EffectiveAt
	if !in.ExpiresAt.After(in.EffectiveAt) {
		return in, fail("DESTINATION_EXPIRY_AFTER_COOLING_REQUIRED")
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT w.currency=$5 AND b.jurisdiction=$6 AND i.state='ACTIVE' AND v.state='VERIFIED' AND v.verification_ref=$3 AND b.verification_ref=$3 AND v.expires_at>=$4 AND b.expires_at>=$4 FROM payout_account_refs b JOIN wallets w USING(wallet_id) JOIN financial_identities i USING(identity_id) JOIN verification_refs v USING(identity_id) WHERE b.payout_account_id=$1 AND b.wallet_id=$2 FOR SHARE OF v`, in.PayoutAccountID, in.WalletID, in.VerificationRef, in.ExpiresAt, policy.Currency, policy.Jurisdiction).Scan(&valid)
	if e != nil {
		return in, e
	}
	if !valid {
		return in, core.Fail(403, "VERIFIED_DESTINATION_REQUIRED")
	}
	var latest ids.ID
	e = tx.QueryRow(ctx, `SELECT version_id FROM destination_versions WHERE wallet_id=$1 ORDER BY created_at DESC,version_id DESC LIMIT 1`, in.WalletID).Scan(&latest)
	if e == nil && (in.PredecessorID == nil || *in.PredecessorID != latest) {
		return in, core.Fail(409, "DESTINATION_PREDECESSOR_REQUIRED")
	}
	if errors.Is(e, pgx.ErrNoRows) && in.PredecessorID != nil {
		return in, core.Fail(409, "DESTINATION_PREDECESSOR_REQUIRED")
	}
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return in, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO destination_versions(version_id,wallet_id,payout_account_id,predecessor_id,effective_at,expires_at,block_during_cooling,snapshot,fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, in.ID, in.WalletID, in.PayoutAccountID, in.PredecessorID, in.EffectiveAt, in.ExpiresAt, in.HighRisk, raw(in), core.Fingerprint(in))
	if e != nil {
		return in, e
	}
	if e = s.Audit(ctx, tx, "payout.destination_change_requested", in.ID.String(), map[string]any{"version_id": in.ID, "wallet_id": in.WalletID, "risk_ref": in.RiskRef, "notification_ref": in.NotificationRef, "effective_at": in.EffectiveAt}); e != nil {
		return in, e
	}
	if e = s.Emit(ctx, tx, "finance.destination_changed.v1", "destination", in.ID, in); e != nil {
		return in, e
	}
	return in, tx.Commit(ctx)
}
func (s *Service) RevokeDestination(ctx context.Context, id ids.ID, ref string) error {
	if e := core.Require(ctx, platform.VerificationService); e != nil {
		if e = core.Require(ctx, platform.AdminService); e != nil {
			return e
		}
	}
	if ref == "" {
		return fail("REVOCATION_EVIDENCE_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var wallet ids.ID
	if e = tx.QueryRow(ctx, `SELECT wallet_id FROM destination_versions WHERE version_id=$1`, id).Scan(&wallet); e != nil {
		return e
	}
	if e = lock(ctx, tx, "destination-wallet:"+wallet.String()); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO destination_revocations(version_id,evidence_ref) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, ref)
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "payout.destination_revoked", id.String(), map[string]string{"evidence_ref": ref}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) currentDestination(ctx context.Context, tx pgx.Tx, wallet, dest ids.ID) error {
	if e := lock(ctx, tx, "destination-wallet:"+wallet.String()); e != nil {
		return e
	}
	var blocked bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM destination_versions d WHERE wallet_id=$1 AND effective_at>now() AND block_during_cooling AND NOT EXISTS(SELECT 1 FROM destination_revocations r WHERE r.version_id=d.version_id))`, wallet).Scan(&blocked)
	if e != nil {
		return e
	}
	if blocked {
		return core.Fail(403, "DESTINATION_HIGH_RISK_COOLING")
	}
	var current ids.ID
	e = tx.QueryRow(ctx, `SELECT payout_account_id FROM destination_versions d WHERE wallet_id=$1 AND effective_at<=now() ORDER BY created_at DESC,version_id DESC LIMIT 1`, wallet).Scan(&current)
	if errors.Is(e, pgx.ErrNoRows) {
		// The baseline's first attested bank remains canonical until an approved
		// change cools off. Merely recording a second bank cannot bypass this rule.
		e = tx.QueryRow(ctx, `SELECT payout_account_id FROM payout_account_refs WHERE wallet_id=$1 ORDER BY created_at,payout_account_id LIMIT 1`, wallet).Scan(&current)
	} else if e == nil {
		var active bool
		e = tx.QueryRow(ctx, `SELECT expires_at>now() AND NOT EXISTS(SELECT 1 FROM destination_revocations r WHERE r.version_id=d.version_id) FROM destination_versions d WHERE wallet_id=$1 AND effective_at<=now() ORDER BY created_at DESC,version_id DESC LIMIT 1`, wallet).Scan(&active)
		if e != nil {
			return e
		}
		if !active {
			return core.Fail(403, "DESTINATION_REVOKED_OR_EXPIRED")
		}
	}
	if e != nil {
		return e
	}
	if current != dest {
		return core.Fail(403, "DESTINATION_NOT_CURRENT_OR_COOLING")
	}
	return nil
}

type Availability struct {
	WalletID               ids.ID   `json:"wallet_id"`
	PoolID                 ids.ID   `json:"pool_id"`
	Currency               string   `json:"currency"`
	AvailableMinor         string   `json:"available_minor"`
	ProtectedMinor         string   `json:"protected_minor"`
	PayoutPendingMinor     string   `json:"payout_pending_minor"`
	SettlementPendingMinor string   `json:"settlement_pending_minor"`
	ReservedMinor          string   `json:"reserved_minor"`
	WithdrawableNow        bool     `json:"withdrawable_now"`
	Status                 []string `json:"status"`
}

func (s *Service) Availability(ctx context.Context, wallet, pool ids.ID) (Availability, error) {
	w, e := s.OwnWallet(ctx, wallet)
	if e != nil {
		return Availability{}, e
	}
	out := Availability{WalletID: wallet, PoolID: pool, Currency: w.Currency, Status: []string{}}
	var rail, restricted bool
	var amount string
	e = s.DB.QueryRow(ctx, `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='AVAILABLE'),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind IN ('PROTECTED','DEPOSIT')),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind='WITHDRAWAL_PENDING'),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind IN ('PAYMENT_PENDING','PENDING_EARNINGS')),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind IN ('FROZEN','REFUND_PENDING','RESERVE')),0)::text,p.restricted,COALESCE((a.config->>'operational')::boolean,false) AND COALESCE((a.config->>'payout_approved')::boolean,false) AND COALESCE((a.config->>'approved')::boolean,false) FROM custody_pools p JOIN provider_accounts a USING(provider_account_id) LEFT JOIN account_balances b ON b.pool_id=p.pool_id AND b.wallet_id=$1 WHERE p.pool_id=$2 AND p.currency=$3 AND p.legal_entity=$4 GROUP BY p.restricted,a.config`, wallet, pool, w.Currency, w.LegalEntity).Scan(&out.AvailableMinor, &out.ProtectedMinor, &out.PayoutPendingMinor, &out.SettlementPendingMinor, &out.ReservedMinor, &restricted, &rail)
	if e != nil {
		return out, e
	}
	amount = out.AvailableMinor
	if amount != "0" {
		out.Status = append(out.Status, "AVAILABLE")
	}
	if out.PayoutPendingMinor != "0" {
		out.Status = append(out.Status, "PAYOUT_PENDING")
	}
	if out.SettlementPendingMinor != "0" {
		out.Status = append(out.Status, "SETTLEMENT_PENDING")
	}
	if out.ReservedMinor != "0" {
		out.Status = append(out.Status, "RESERVED")
	}
	if restricted || w.State != "ACTIVE" {
		out.Status = append(out.Status, "RESTRICTED")
	}
	adapter, ae := s.AdapterForPool(ctx, pool)
	if !rail || ae != nil || adapter.Payout == nil {
		out.Status = append(out.Status, "RAIL_UNAVAILABLE")
		rail = false
	}
	// Customer cash-out is outside the activated product. For beneficiaries,
	// eligibility also requires current bank, KYC, authority and unused approval.
	if amount != "0" && rail && !restricted && w.State == "ACTIVE" && w.Role != "CUSTOMER" && w.Role != "TENANT" {
		var approved []WithdrawalApproval
		rows, e := s.DB.Query(ctx, `SELECT snapshot FROM lifecycle_decisions d WHERE action='WITHDRAWAL_APPROVAL' AND snapshot->>'wallet_id'=$1 AND (snapshot->>'expires_at')::timestamptz>now() AND NOT EXISTS(SELECT 1 FROM payout_attempts p WHERE p.authorization_snapshot->>'approval_id'=d.decision_id::text) LIMIT 100`, wallet.String())
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e != nil {
				rows.Close()
				return out, e
			}
			var a WithdrawalApproval
			if e = decode(b, &a); e != nil {
				rows.Close()
				return out, e
			}
			approved = append(approved, a)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		tx, e := s.DB.Begin(ctx)
		if e != nil {
			return out, e
		}
		defer tx.Rollback(ctx)
		for _, ap := range approved {
			if e = s.outstandingRecovery(ctx, tx, wallet); e != nil {
				break
			}
			if e = s.payoutPartyControl(ctx, tx, ap); e != nil {
				continue
			}
			var cfg finance.ProviderConfig
			if e = load(ctx, tx, `SELECT config FROM provider_accounts WHERE provider_account_id=$1`, adapter.ProviderID, &cfg); e != nil {
				return out, e
			}
			if !cfg.PayoutFeeApproved || !has(cfg.Capabilities, "TRANSFER") || cfg.Mode != "test" || cfg.PayoutApprovalRef == "" {
				continue
			}
			var covered bool
			e = tx.QueryRow(ctx, `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='SETTLED_CASH'),0)-p.reserve_minor-COALESCE(sum(balance_minor) FILTER(WHERE kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','SETTLEMENT_CONTROL','RESERVE','PROVIDER_PAYABLE')),0)>=$2+COALESCE((SELECT sum((route_snapshot->>'payout_fee_minor')::numeric) FROM payout_attempts WHERE pool_id=$1 AND state NOT IN ('CONFIRMED_PAID','CONFIRMED_FAILED')),0) FROM account_balances JOIN custody_pools p USING(pool_id) WHERE pool_id=$1 GROUP BY p.reserve_minor`, pool, cfg.PayoutFeeMinor).Scan(&covered)
			if e != nil {
				return out, e
			}
			if !covered {
				continue
			}
			if e = s.currentDestination(ctx, tx, wallet, ap.PayoutAccountID); e != nil {
				continue
			}
			var eligible bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM allocation_lines l JOIN account_balances b ON b.account_id=l.available_account_id JOIN beneficiary_authorities a USING(authority_id) JOIN payout_account_refs d ON d.payout_account_id=$2 JOIN wallets w ON w.wallet_id=l.wallet_id JOIN verification_refs v USING(identity_id) WHERE l.line_id=$1 AND b.pool_id=$3 AND l.state IN ('AVAILABLE','WITHDRAWAL_RESERVED') AND l.released_minor-l.reserved_minor>=$4 AND b.balance_minor>=$4 AND NOT a.revoked AND a.expires_at>now() AND d.expires_at>now() AND v.state='VERIFIED' AND v.expires_at>now() AND v.verification_ref=d.verification_ref AND EXISTS(SELECT 1 FROM payout_destination_bindings pb WHERE pb.payout_account_id=d.payout_account_id AND pb.provider_account_id=$5 AND pb.expires_at>now()))`, ap.LineID, ap.PayoutAccountID, pool, ap.AmountMinor, adapter.ProviderID).Scan(&eligible)
			if e != nil {
				return out, e
			}
			if eligible {
				out.WithdrawableNow = true
				break
			}
		}
	}
	if out.WithdrawableNow {
		out.Status = append(out.Status, "WITHDRAWABLE_NOW")
	}
	return out, nil
}
