package core

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"time"
)

type WalletAccount struct {
	AccountID ids.ID `json:"account_id"`
	PoolID    ids.ID `json:"pool_id"`
	Kind      string `json:"kind"`
}
type WalletProfile struct {
	Wallet            Wallet          `json:"wallet"`
	CanonicalPartyRef string          `json:"canonical_party_ref"`
	Purpose           string          `json:"purpose"`
	VerificationState string          `json:"verification_state"`
	Roles             []string        `json:"roles"`
	Accounts          []WalletAccount `json:"accounts"`
	CreatedAt         time.Time       `json:"created_at"`
}

func (s *Service) WalletProfile(ctx context.Context, id ids.ID) (WalletProfile, error) {
	w, e := s.OwnWallet(ctx, id)
	if e != nil {
		return WalletProfile{}, e
	}
	out := WalletProfile{Wallet: w, Roles: []string{}, Accounts: []WalletAccount{}}
	e = s.DB.QueryRow(ctx, `SELECT i.legal_party_ref,p.purpose,CASE WHEN v.state='VERIFIED' AND v.expires_at>now() THEN 'VERIFIED' WHEN v.state='REVOKED' THEN 'REVOKED' WHEN v.state='VERIFIED' THEN 'EXPIRED' ELSE 'PENDING' END,w.created_at FROM wallets w JOIN financial_identities i USING(identity_id) JOIN wallet_purposes p USING(wallet_id) LEFT JOIN verification_refs v ON v.identity_id=i.identity_id WHERE w.wallet_id=$1`, id).Scan(&out.CanonicalPartyRef, &out.Purpose, &out.VerificationState, &out.CreatedAt)
	if e != nil {
		return out, e
	}
	rows, e := s.DB.Query(ctx, `SELECT role FROM wallet_roles WHERE wallet_id=$1 ORDER BY role`, id)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var role string
		if e = rows.Scan(&role); e != nil {
			rows.Close()
			return out, e
		}
		out.Roles = append(out.Roles, role)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = s.DB.Query(ctx, `SELECT account_id,pool_id,kind FROM ledger_accounts WHERE wallet_id=$1 ORDER BY account_id`, id)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var a WalletAccount
		if e = rows.Scan(&a.AccountID, &a.PoolID, &a.Kind); e != nil {
			return out, e
		}
		out.Accounts = append(out.Accounts, a)
	}
	return out, rows.Err()
}

// Wallet controls change authorization only. They never edit ledger balances.
func (s *Service) ControlWallet(ctx context.Context, id ids.ID, in IdentityControl) error {
	if e := Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if id.ValidateV7() != nil || in.EvidenceRef == "" || in.PolicyVersion == "" || (in.State != "ACTIVE" && in.State != "RESTRICTED" && in.State != "CLOSED") {
		return Fail(422, "INVALID_WALLET_CONTROL")
	}
	if in.State == "ACTIVE" && (len(in.Approvers) != 2 || in.Approvers[0] == "" || in.Approvers[1] == "" || in.Approvers[0] == in.Approvers[1]) {
		return Fail(403, "DUAL_APPROVAL_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	// Same identity lock taken by financial posting; a freeze cannot race past a debit.
	var state string
	if e = tx.QueryRow(ctx, `SELECT i.state FROM wallets w JOIN financial_identities i USING(identity_id) WHERE wallet_id=$1 FOR UPDATE OF i`, id).Scan(&state); e != nil {
		return e
	}
	if in.State == "ACTIVE" && state != "ACTIVE" {
		return Fail(403, "FINANCIAL_IDENTITY_RESTRICTED")
	}
	var prior string
	if e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT state FROM wallet_controls WHERE wallet_id=$1),'ACTIVE')`, id).Scan(&prior); e != nil {
		return e
	}
	if prior == "CLOSED" && in.State != "CLOSED" {
		return Fail(403, "WALLET_CLOSED")
	}
	if in.State == "CLOSED" {
		var pending bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_balances WHERE wallet_id=$1 AND balance_minor<>0) OR EXISTS(SELECT 1 FROM wallet_transfers WHERE (source_wallet_id=$1 OR destination_wallet_id=$1) AND state='REQUESTED') OR EXISTS(SELECT 1 FROM payment_intents WHERE wallet_id=$1 AND state NOT IN ('VERIFIED_FAILURE','CANCELLED'))`, id).Scan(&pending)
		if e != nil {
			return e
		}
		if pending {
			return Fail(409, "WALLET_HAS_OBLIGATIONS")
		}
	}
	if prior == in.State {
		return nil
	}
	_, e = tx.Exec(ctx, `INSERT INTO wallet_controls(wallet_id,state) VALUES($1,$2) ON CONFLICT(wallet_id) DO UPDATE SET state=EXCLUDED.state,updated_at=now()`, id, in.State)
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "wallet.state_changed", id.String(), in); e != nil {
		return e
	}
	if e = s.Emit(ctx, tx, "finance.wallet.state_changed.v1", "wallet", id, map[string]any{"wallet_id": id, "state": in.State, "evidence_ref": in.EvidenceRef, "policy_version": in.PolicyVersion}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
