package integration

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"sync"
	"testing"
	"time"
)

func TestDVACollectionMatchingExactlyOnceAndRevokedVerification(t *testing.T) {
	f := setup(t)
	in := finance.DVAInput{WalletID: f.w.WalletID, ProviderAccountID: f.cfg.ID, VerificationRef: "kyc-dva", ConsentRef: "consent-v1", ConsentVersion: "v1", ConsentAt: time.Now().Add(-time.Minute), ConsentExpiresAt: time.Now().Add(time.Hour), Customer: providers.CustomerRequest{Email: "fixture@example.invalid", FirstName: "Test", LastName: "Fixture"}, Validation: providers.Validation{Country: "NG", Type: "bank_account", AccountNumber: "0123456789", BVN: "fixture-only", BankCode: "001"}}
	verifier := ctx(platform.VerificationService, "", ids.Nil)
	if e := f.s.Verification(verifier, f.w.IdentityID, in.VerificationRef, "VERIFIED", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	f.fake.identified = true
	if x, e := f.s.RequestDVA(f.user, in); e != nil || x.State != "ACTIVE" {
		t.Fatal(x, e)
	}
	f.fake.mu.Lock()
	f.fake.charge = providers.Charge{Reference: "dva-charge", State: "VERIFIED_SUCCESS", Currency: "NGN", Mode: "test", AmountMinor: 3000, MerchantID: 1001, CustomerRef: "CUS_fixture"}
	f.fake.mu.Unlock()
	var wg sync.WaitGroup
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := f.s.MatchDVACharge(system(), f.cfg.ID, "dva-charge"); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	var count int
	if e := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM collection_receipts`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	bs, e := f.s.Balances(f.user, f.w.WalletID)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range bs {
		if b.Kind == "PAYMENT_PENDING" && b.AmountMinor != "3000" {
			t.Fatal(b)
		}
		if b.Withdrawable {
			t.Fatal(b)
		}
	}
	if e = f.s.Verification(verifier, f.w.IdentityID, in.VerificationRef, "REVOKED", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	f.fake.mu.Lock()
	f.fake.charge.Reference = "dva-charge-after-revocation"
	f.fake.mu.Unlock()
	if e = f.s.MatchDVACharge(system(), f.cfg.ID, "dva-charge-after-revocation"); e == nil {
		t.Fatal("revoked verification credited")
	}
	claimed, e := f.s.Outbox.Claim(context.Background(), "schema-dva", 100, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range claimed {
		body, _ := json.Marshal(row.Envelope)
		testkit.ValidateJSONSchema(t, "../../../../contracts/events/payments/"+row.Envelope.EventType+".schema.json", body)
	}
}
func TestReversedCollectionRestrictsPoolAndRetainsJournal(t *testing.T) {
	f := setup(t)
	i, _ := f.intent("reversal")
	f.fake.success(i)
	if _, e := f.s.VerifyIntent(f.user, i.ID); e != nil {
		t.Fatal(e)
	}
	f.fake.mu.Lock()
	f.fake.charge.State = "VERIFIED_FAILURE"
	f.fake.mu.Unlock()
	updated, e := f.s.VerifyIntent(f.user, i.ID)
	if e != nil || updated.State != "RECONCILIATION_EXCEPTION" || updated.SettlementState != "EXCEPTION" {
		t.Fatal(updated, e)
	}
	coverage, e := f.s.Coverage(ctx(platform.AdminService, "", ids.Nil), f.cfg.PoolID)
	if e != nil || !coverage.Restricted || coverage.ReceivableMinor != "10000" {
		t.Fatal(coverage, e)
	}
	f.fake.success(i)
	if _, e = f.s.VerifyIntent(f.user, i.ID); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM journals`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
