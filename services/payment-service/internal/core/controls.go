package core

import (
	"context"
	"errors"
	dbqueries "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"time"
)

type IdentityControl struct {
	State         string   `json:"state"`
	EvidenceRef   string   `json:"evidence_ref"`
	PolicyVersion string   `json:"policy_version"`
	Approvers     []string `json:"approvers"`
}

func (s *Service) ControlIdentity(ctx context.Context, id ids.ID, in IdentityControl) error {
	if e := Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if id.ValidateV7() != nil || in.EvidenceRef == "" || in.PolicyVersion == "" || (in.State != "ACTIVE" && in.State != "RESTRICTED" && in.State != "CLOSED") {
		return Fail(422, "CONTROL_EVIDENCE_REQUIRED")
	}
	if in.State == "ACTIVE" && (len(in.Approvers) != 2 || in.Approvers[0] == "" || in.Approvers[1] == "" || in.Approvers[0] == in.Approvers[1]) {
		return Fail(403, "DUAL_APPROVAL_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var old string
	if e = tx.QueryRow(ctx, `SELECT state FROM financial_identities WHERE identity_id=$1 FOR UPDATE`, id).Scan(&old); errors.Is(e, pgx.ErrNoRows) {
		return Fail(404, "IDENTITY_NOT_FOUND")
	} else if e != nil {
		return e
	}
	if old == "CLOSED" && in.State != "CLOSED" {
		return Fail(409, "CLOSED_IDENTITY_RETAINED")
	}
	if in.State == "CLOSED" {
		var obligations bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_balances b JOIN wallets w USING(wallet_id) WHERE w.identity_id=$1 AND b.balance_minor<>0) OR EXISTS(SELECT 1 FROM payment_intents p JOIN wallets w USING(wallet_id) WHERE w.identity_id=$1 AND p.state NOT IN ('VERIFIED_FAILURE','RECONCILIATION_EXCEPTION'))`, id).Scan(&obligations); e != nil {
			return e
		}
		if obligations {
			return Fail(409, "OUTSTANDING_FINANCIAL_OBLIGATIONS")
		}
	}
	if old == in.State {
		return nil
	}
	if _, e = tx.Exec(ctx, `UPDATE financial_identities SET state=$2 WHERE identity_id=$1`, id, in.State); e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "identity.state_changed", id.String(), in); e != nil {
		return e
	}
	if e = s.Emit(ctx, tx, "finance.identity.state_changed.v1", "financial_identity", id, map[string]any{"identity_id": id, "state": in.State, "evidence_ref": in.EvidenceRef, "policy_version": in.PolicyVersion}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type PayoutReferenceInput struct {
	WalletID        ids.ID    `json:"wallet_id"`
	BeneficiaryRef  string    `json:"beneficiary_ref"`
	VerificationRef string    `json:"verification_ref"`
	Jurisdiction    string    `json:"jurisdiction"`
	ExpiresAt       time.Time `json:"expires_at"`
}
type PayoutReference struct {
	ID ids.ID `json:"payout_account_id"`
	PayoutReferenceInput
	Eligible bool `json:"eligible"`
}

func (s *Service) RecordPayoutReference(ctx context.Context, in PayoutReferenceInput) (x PayoutReference, e error) {
	if e = Require(ctx, platform.VerificationService); e != nil {
		return
	}
	if in.WalletID.ValidateV7() != nil || in.BeneficiaryRef == "" || in.VerificationRef == "" || len(in.Jurisdiction) != 2 || !in.ExpiresAt.After(time.Now()) {
		return x, Fail(422, "INVALID_PAYOUT_REFERENCE")
	}
	in.ExpiresAt = in.ExpiresAt.Truncate(time.Microsecond)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return
	}
	defer tx.Rollback(ctx)
	var verified bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallets w JOIN verification_refs v USING(identity_id) JOIN financial_identities i USING(identity_id) WHERE w.wallet_id=$1 AND i.state='ACTIVE' AND v.verification_ref=$2 AND v.state='VERIFIED' AND v.expires_at>= $3)`, in.WalletID, in.VerificationRef, in.ExpiresAt).Scan(&verified)
	if e != nil {
		return
	}
	if !verified {
		return x, Fail(403, "VERIFIED_BENEFICIARY_REQUIRED")
	}
	x = PayoutReference{ID: ids.New(), PayoutReferenceInput: in}
	_, e = tx.Exec(ctx, `INSERT INTO payout_account_refs(payout_account_id,wallet_id,beneficiary_ref,verification_ref,jurisdiction,expires_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(wallet_id,beneficiary_ref) DO NOTHING`, x.ID, in.WalletID, in.BeneficiaryRef, in.VerificationRef, in.Jurisdiction, in.ExpiresAt)
	if e != nil {
		return
	}
	var saved PayoutReferenceInput
	e = tx.QueryRow(ctx, `SELECT payout_account_id,wallet_id,beneficiary_ref,verification_ref,jurisdiction,expires_at FROM payout_account_refs WHERE wallet_id=$1 AND beneficiary_ref=$2`, in.WalletID, in.BeneficiaryRef).Scan(&x.ID, &saved.WalletID, &saved.BeneficiaryRef, &saved.VerificationRef, &saved.Jurisdiction, &saved.ExpiresAt)
	if e != nil {
		return
	}
	if saved.VerificationRef != in.VerificationRef || saved.Jurisdiction != in.Jurisdiction || !saved.ExpiresAt.Equal(in.ExpiresAt) {
		return x, Fail(409, "PAYOUT_REFERENCE_CONFLICT")
	}
	if e = s.Audit(ctx, tx, "payout_account.recorded", x.ID.String(), map[string]string{"verification_ref": in.VerificationRef}); e != nil {
		return
	}
	e = tx.Commit(ctx)
	return
}
func (s *Service) PayoutReferences(ctx context.Context, wallet ids.ID) ([]PayoutReference, error) {
	if _, e := s.OwnWallet(ctx, wallet); e != nil {
		return nil, e
	}
	rows, e := dbqueries.New().ListPayoutReferences(ctx, s.DB, wallet)
	if e != nil {
		return nil, e
	}
	result := []PayoutReference{}
	for _, row := range rows {
		x := PayoutReference{ID: row.PayoutAccountID, PayoutReferenceInput: PayoutReferenceInput{WalletID: row.WalletID, BeneficiaryRef: row.BeneficiaryRef, VerificationRef: row.VerificationRef, Jurisdiction: row.Jurisdiction, ExpiresAt: row.ExpiresAt}}
		result = append(result, x)
	}
	return result, nil
}
