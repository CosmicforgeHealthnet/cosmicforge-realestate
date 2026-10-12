package integration

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"testing"
	"time"
)

func TestRestrictionRetentionAndVerifiedPayoutReferences(t *testing.T) {
	f := setup(t)
	admin := ctx(platform.AdminService, "", ids.Nil)
	verifier := ctx(platform.VerificationService, "", ids.Nil)
	expiry := time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	in := core.PayoutReferenceInput{WalletID: f.w.WalletID, BeneficiaryRef: "verified-owner-bank-reference", VerificationRef: "kyc-fixture", Jurisdiction: "NG", ExpiresAt: expiry}
	if _, e := f.s.RecordPayoutReference(f.user, in); e == nil {
		t.Fatal("domain could declare bank verified")
	}
	if _, e := f.s.RecordPayoutReference(verifier, in); e == nil {
		t.Fatal("missing authoritative KYC")
	}
	if e := f.s.Verification(verifier, f.w.IdentityID, in.VerificationRef, "VERIFIED", expiry.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	x, e := f.s.RecordPayoutReference(verifier, in)
	if e != nil || x.Eligible {
		t.Fatal(x, e)
	}
	again, e := f.s.RecordPayoutReference(verifier, in)
	if e != nil || x.ID != again.ID {
		t.Fatal(again, e)
	}
	in.Jurisdiction = "GB"
	if _, e = f.s.RecordPayoutReference(verifier, in); e == nil {
		t.Fatal("conflicting reference")
	}
	if _, e = f.s.PayoutReferences(ctx(platform.PropertyManagerService, platform.DomainPropertyManager, ids.New()), f.w.WalletID); e == nil {
		t.Fatal("manager read owner banking")
	}
	refs, e := f.s.PayoutReferences(f.user, f.w.WalletID)
	if e != nil || len(refs) != 1 || !refs[0].Eligible {
		t.Fatal(refs, e)
	}
	// Eligibility is a current verified reference, never a payout approval.
	i, _ := f.intent("restrict-collection")
	f.fake.success(i)
	control := core.IdentityControl{State: "RESTRICTED", EvidenceRef: "admin-case", PolicyVersion: "risk-v1"}
	if e = f.s.ControlIdentity(f.user, f.w.IdentityID, control); e == nil {
		t.Fatal("source could restrict identity")
	}
	if e = f.s.ControlIdentity(admin, f.w.IdentityID, control); e != nil {
		t.Fatal(e)
	}
	refs, e = f.s.PayoutReferences(f.user, f.w.WalletID)
	if e != nil || len(refs) != 1 || refs[0].Eligible {
		t.Fatal("restricted reference remained eligible", refs, e)
	}
	if _, e = f.s.VerifyIntent(f.user, i.ID); e != nil {
		t.Fatal(e)
	}
	bs, e := f.s.Balances(f.user, f.w.WalletID)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range bs {
		if b.Kind == "FROZEN" && b.AmountMinor != "10000" {
			t.Fatal(b)
		}
		if b.Withdrawable {
			t.Fatal(b)
		}
	}
	control.State = "CLOSED"
	if e = f.s.ControlIdentity(admin, f.w.IdentityID, control); e == nil {
		t.Fatal("closed outstanding money")
	}
	control.State = "ACTIVE"
	control.Approvers = []string{"same", "same"}
	if e = f.s.ControlIdentity(admin, f.w.IdentityID, control); e == nil {
		t.Fatal("single control")
	}
	control.Approvers = []string{"finance-1", "compliance-2"}
	if e = f.s.ControlIdentity(admin, f.w.IdentityID, control); e != nil {
		t.Fatal(e)
	}
	var audits int
	if e = f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM financial_audit WHERE action='identity.state_changed'`).Scan(&audits); e != nil || audits != 2 {
		t.Fatal(audits, e)
	}
}

func TestHistoryPaginationAndLedgerDimensions(t *testing.T) {
	f := setup(t)
	a := f.accounts("PROVIDER_RECEIVABLE", "PAYMENT_PENDING", "PROTECTED")
	entries := []core.Entry{{AccountID: a["PROVIDER_RECEIVABLE"], Side: "DEBIT", AmountMinor: 100}, {AccountID: a["PAYMENT_PENDING"], Side: "CREDIT", AmountMinor: 60}, {AccountID: a["PROTECTED"], Side: "CREDIT", AmountMinor: 40}}
	if _, e := f.post("history", entries); e != nil {
		t.Fatal(e)
	}
	balances, e := f.s.Balances(f.user, f.w.WalletID)
	if e != nil || len(balances) != 7 {
		t.Fatal("partial accounts lost zero views", balances, e)
	}
	if _, e := f.db.Pool.Exec(context.Background(), `TRUNCATE journals CASCADE`); e == nil {
		t.Fatal("history truncated")
	}
	tx, e := f.db.Pool.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Account(system(), tx, &f.w.WalletID, f.cfg.PoolID, "REVENUE", f.w.LegalEntity, "NGN", ""); e == nil {
		tx.Rollback(context.Background())
		t.Fatal("platform revenue became public wallet")
	}
	tx.Rollback(context.Background())
	first, e := f.s.History(f.user, f.w.WalletID, ids.Nil, 1)
	if e != nil || len(first) != 1 {
		t.Fatal(first, e)
	}
	second, e := f.s.History(f.user, f.w.WalletID, first[0].EntryID, 1)
	if e != nil || len(second) != 1 || second[0].EntryID == first[0].EntryID || second[0].JournalID != first[0].JournalID {
		t.Fatal(second, e)
	}
	last, e := f.s.History(f.user, f.w.WalletID, second[0].EntryID, 1)
	if e != nil || len(last) != 0 {
		t.Fatal(last, e)
	}
	for _, amount := range []int64{0, -1} {
		bad := append([]core.Entry(nil), entries...)
		bad[0].AmountMinor = amount
		if _, e = f.post("invalid", bad); e == nil {
			t.Fatal("invalid money")
		}
	}
	p := core.Posting{OperationRef: "wrong-entity", LegalEntity: "other", Currency: "NGN", PoolID: f.cfg.PoolID, PolicyVersion: "v1", Authorization: map[string]string{"evidence_ref": "fixture", "approved_by": "fixture"}, Entries: entries}
	if _, e = f.s.Post(system(), p); e == nil {
		t.Fatal("cross entity")
	}
	p.LegalEntity = f.w.LegalEntity
	p.Currency = "USD"
	if _, e = f.s.Post(system(), p); e == nil {
		t.Fatal("cross currency")
	}
}
