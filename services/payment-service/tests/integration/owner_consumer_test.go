package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	phttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// Separate processes/modules, separate real databases, actual signed HTTP and
// actual payment outbox -> JetStream -> Owner consumer. Provider is LOCAL ONLY.
func TestActualOwnerFinancialCoreConsumer(t *testing.T) {
	f := setup(t)
	l := lifecycle.New(f.s)
	f.s.Outbox = outbox.NewStore(f.db.Pool, "cross")
	cfg := f.cfg
	cfg.Categories = append(cfg.Categories, "RESIDENTIAL_INITIAL")
	if e := f.s.Register(system(), cfg, finance.Adapter{Collection: f.fake, DVA: f.fake, Settlement: f.fake}); e != nil {
		t.Fatal(e)
	}
	policy := makePolicy("RESIDENTIAL_INITIAL")
	if _, e := l.PutPolicy(admin(), policy); e != nil {
		t.Fatal(e)
	}
	key := testkit.SigningKey(t, "owner-cross-service-ephemeral")
	registry := serviceauth.NewRegistry()
	if e := registry.Add(platform.IndividualOwnerService, key.KID, key.PublicKey()); e != nil {
		t.Fatal(e)
	}
	v, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.PaymentService, Registry: registry})
	if e != nil {
		t.Fatal(e)
	}
	router := phttp.NewRouter(phttp.Config{Service: f.s, Verifier: v, Health: health.New("payment-service", "test", time.Second), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	broker := testkit.StartNATS(t)
	conn, e := jetstream.Connect(jetstream.Config{URL: broker.URL, Name: "financial-core-cross-test", Logger: logging.Discard()})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if e = conn.EnsureStream(context.Background(), jetstream.StreamConfig{Name: "CROSS_FINANCE", Subjects: []string{"cross.finance.>", "finance.>"}}); e != nil {
		t.Fatal(e)
	}
	relay, e := outbox.NewWorker(f.s.Outbox, conn, outbox.WorkerConfig{Owner: "cross-test", BatchSize: 100, Logger: logging.Discard()})
	if e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	mux.Handle("/", router)
	// Test-only fixture authority. Never registered on the application router.
	mux.HandleFunc("POST /test-fixtures/quote", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ObligationID ids.ID `json:"obligation_id"`
			OwnerID      ids.ID `json:"owner_id"`
			Amount       int64  `json:"amount_minor"`
			RentMinor    int64  `json:"rent_minor"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		payer := ids.New()
		pc := ctx(platform.MarketplaceService, platform.DomainMarketplace, payer)
		wallet, e := f.s.EnsureWallet(pc, core.Provision{Domain: "marketplace", AccountID: payer, Role: "TENANT", PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "test-tenant:" + payer.String()})
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		q := lifecycle.Quote{ID: ids.New(), Domain: "individual-owner", BusinessRef: "rent_obligation:" + in.ObligationID.String(), PayerWalletID: wallet.WalletID, PayerType: "TENANT", PayerConsentRef: "synthetic-accepted-consent", LegalPartyRef: "marketplace:" + payer.String(), QuoteVersion: "test-accepted-v1", AgreementRef: "test-lease", AgreementVersion: "v1", PolicyID: policy.ID, Currency: "NGN", Jurisdiction: "NG", Schedule: "ANNUAL", AmountMinor: in.Amount, Components: []lifecycle.Component{{Ref: "rent", Kind: "RENT", AmountMinor: in.RentMinor, PayerRef: "marketplace:" + payer.String(), Recurrence: "RECURRING"}}}
		if in.Amount > in.RentMinor {
			q.Components = append(q.Components, lifecycle.Component{Ref: "recurring-fee", Kind: "SERVICE", AmountMinor: in.Amount - in.RentMinor, PayerRef: q.LegalPartyRef, Recurrence: "RECURRING"})
		}

		if _, e = l.LockQuote(ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, in.OwnerID), q, "cross:"+q.ID.String()); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(q)
	})
	mux.HandleFunc("POST /test-fixtures/verify", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IntentID ids.ID `json:"intent_id"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		intent, e := f.s.GetIntent(system(), in.IntentID)
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		f.fake.success(intent)
		if _, e = f.s.VerifyIntent(system(), intent.ID); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		if _, e = relay.RunOnce(context.Background()); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	})

	mux.HandleFunc("POST /test-fixtures/allocate", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IntentID ids.ID `json:"intent_id"`
			OwnerID  ids.ID `json:"owner_id"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		source := ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, in.OwnerID)
		wallets, e := f.s.MyWallets(source)
		if e != nil || len(wallets) != 1 {
			t.Errorf("owned earnings context: %v", e)
			w.WriteHeader(500)
			return
		}
		wallet := wallets[0]
		var quoteID ids.ID
		if e = f.db.Pool.QueryRow(system(), `SELECT locked_quote_id FROM payment_intents WHERE intent_id=$1`, in.IntentID).Scan(&quoteID); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		q, e := l.Quote(source, quoteID)
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		if e = f.s.Verification(verifier(), wallet.IdentityID, "synthetic-owner-verification", "VERIFIED", time.Now().Add(time.Hour)); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		authority := lifecycle.Authority{ID: ids.New(), Domain: "individual-owner", BusinessRef: q.BusinessRef, WalletID: &wallet.WalletID, LegalPartyRef: "individual-owner:" + in.OwnerID.String(), Role: "OWNER", Currency: q.Currency, LegalEntity: wallet.LegalEntity, Components: []string{"RENT", "SERVICE"}, EvidenceRef: "synthetic-property-ownership", OwnershipBPS: 10000, MaxMinor: q.AmountMinor, ExpiresAt: time.Now().Add(time.Hour)}
		if _, e = l.PutAuthority(source, authority); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		lines := []lifecycle.LineInput{}
		for _, component := range q.Components {
			lines = append(lines, lifecycle.LineInput{AuthorityID: authority.ID, ComponentRef: component.Ref, Kind: "BENEFICIARY", AmountMinor: component.AmountMinor, Currency: q.Currency})
		}
		if _, e = l.Allocate(source, lifecycle.AllocationInput{IntentID: in.IntentID, Version: 1, Lines: lines}); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /test-fixtures/freeze", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			OwnerID ids.ID `json:"owner_id"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		wallets, e := f.s.MyWallets(ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, in.OwnerID))
		if e != nil || len(wallets) != 1 {
			t.Error("fixture ownership context", e)
			w.WriteHeader(500)
			return
		}
		if e = f.s.ControlWallet(admin(), wallets[0].WalletID, core.IdentityControl{State: "RESTRICTED", EvidenceRef: "synthetic-finance-review", PolicyVersion: "test-risk-v1"}); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "go", "test", "-race", "-count=1", "-tags", "financial_cross_service", "-run", "^TestFinancialCoreConsumerContract$", "./tests/integration")
	cmd.Dir = filepath.Clean("../../../individual-owner-service")
	cmd.Env = append(os.Environ(), "CF_PAYMENT_E2E_URL="+srv.URL, "CF_PAYMENT_E2E_NATS="+broker.URL, "CF_PAYMENT_E2E_KEY="+base64.StdEncoding.EncodeToString(key.PrivateKey))
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("Owner consumer failed: %v\n%s", e, output)
	}
	t.Log("Owner child race test passed: activation, lazy wallet, accepted quote, signed collection, real outbox/JetStream, once-only projection, protected beneficiary earnings, restricted readback, isolation; local provider fixture only")
	var receipts, pending, available int
	if e = f.db.Pool.QueryRow(system(), `SELECT (SELECT count(*) FROM collection_receipts),(SELECT count(*) FROM journal_entries e JOIN ledger_accounts a USING(account_id) WHERE a.kind='PROTECTED'),(SELECT count(*) FROM journal_entries e JOIN ledger_accounts a USING(account_id) WHERE a.kind='AVAILABLE')`).Scan(&receipts, &pending, &available); e != nil || receipts != 1 || pending != 2 || available != 0 {
		t.Fatal("collection invented availability", receipts, pending, available, e)
	}
}
