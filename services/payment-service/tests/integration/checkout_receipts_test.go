package integration

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"testing"
)

func TestCheckoutReceiptAtomicCreditAndReconciliation(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	for n := 0; n < 4; n++ {
		if _, e := f.s.VerifyIntent(f.user, f.intent.ID); e != nil {
			t.Fatal(e)
		}
	}
	var receipts int
	var amount int64
	var currency string
	var sameJournal bool
	e := f.db.Pool.QueryRow(system(), `SELECT count(*),min(r.amount_minor),min(r.currency),bool_and(r.journal_id=i.credited_journal_id) FROM collection_receipts r JOIN payment_intents i ON i.provider_account_id=r.provider_account_id AND i.provider_reference=r.external_ref WHERE i.intent_id=$1`, f.intent.ID).Scan(&receipts, &amount, &currency, &sameJournal)
	if e != nil || receipts != 1 || amount != 10000 || currency != "NGN" || !sameJournal {
		t.Fatal("checkout receipt/journal lost canonical context", e)
	}
	if _, e = f.db.Pool.Exec(system(), `UPDATE collection_receipts SET amount_minor=1 WHERE external_ref=$1`, f.intent.Reference); e == nil {
		t.Fatal("mutable original receipt")
	}
	r, e := f.l.Reconcile(admin(), lifecycle.ReconciliationRequest{ID: ids.New(), ProviderID: f.cfg.ID, AuthorityRef: "test-original-checkout", StaleAfterSeconds: 30})
	if e != nil {
		t.Fatal(e)
	}
	for _, finding := range r.Findings {
		if finding.Kind == "MISSING_COLLECTION" || finding.Kind == "COLLECTION_CONTEXT_MISMATCH" {
			t.Fatal("false checkout collection exception", finding)
		}
	}
	validateLifecycleOutbox(t, f)
}
