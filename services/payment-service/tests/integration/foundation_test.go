package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	phttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func ctx(service platform.Service, domain platform.Domain, account ids.ID) context.Context {
	return serviceauth.WithPrincipal(context.Background(), serviceauth.Principal{Service: service, Delegation: serviceauth.Delegation{ActorDomain: domain, ActorID: account}})
}
func system() context.Context { return ctx(platform.PaymentService, "", ids.Nil) }

type fixture struct {
	t       *testing.T
	db      *testkit.Database
	s       *finance.Service
	w       core.Wallet
	account ids.ID
	user    context.Context
	cfg     finance.ProviderConfig
	fake    *fakeProvider
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db := testkit.NewDatabase(t, migrations.FS)
	s := finance.New(core.New(db.Pool), "development")
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	account := ids.New()
	user := ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, account)
	w, e := s.EnsureWallet(user, core.Provision{Domain: "individual-owner", AccountID: account, Role: "OWNER", PartyKind: "PERSON", LegalEntity: "cosmicforge-ng", Currency: "NGN", RegistrationRef: "registered:" + account.String()})
	if e != nil {
		t.Fatal(e)
	}
	fake := &fakeProvider{}
	c := finance.ProviderConfig{ID: ids.New(), PoolID: ids.New(), Name: "contract-test", LegalEntity: w.LegalEntity, MerchantRef: "1001", Mode: "test", Countries: []string{"NG"}, Currencies: []string{"NGN"}, Categories: []string{"RENT"}, Capabilities: []string{"COLLECTION", "DVA", "SETTLEMENT"}, Approved: true, Operational: true, DVAApproved: true, MaxMinor: 1000000000, Version: "v1", ApprovalRef: "contract-fixture-only"}
	if e = s.Register(system(), c, finance.Adapter{Collection: fake, DVA: fake, Settlement: fake}); e != nil {
		t.Fatal(e)
	}
	return &fixture{t, db, s, w, account, user, c, fake}
}
func (f *fixture) intent(key string) (finance.Intent, finance.IntentInput) {
	f.t.Helper()
	in := finance.IntentInput{WalletID: f.w.WalletID, AmountMinor: 10000, Currency: "NGN", Country: "NG", Category: "RENT", EvidenceRef: "owner-obligation-accepted-quote", PolicyVersion: "v1", Email: "contract@example.invalid"}
	i, e := f.s.CreateIntent(f.user, in, key)
	if e != nil {
		f.t.Fatal(e)
	}
	return i, in
}
func (f *fixture) accounts(kinds ...string) map[string]ids.ID {
	f.t.Helper()
	tx, e := f.db.Pool.Begin(context.Background())
	if e != nil {
		f.t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	out := map[string]ids.ID{}
	for _, kind := range kinds {
		var wallet *ids.ID
		if kind != "SETTLED_CASH" && kind != "PROVIDER_RECEIVABLE" {
			wallet = &f.w.WalletID
		}
		id, e := f.s.Account(system(), tx, wallet, f.cfg.PoolID, kind, f.w.LegalEntity, "NGN", "")
		if e != nil {
			f.t.Fatal(e)
		}
		out[kind] = id
	}
	if e = tx.Commit(context.Background()); e != nil {
		f.t.Fatal(e)
	}
	return out
}
func (f *fixture) post(ref string, entries []core.Entry) (ids.ID, error) {
	return f.s.Post(system(), core.Posting{OperationRef: ref, LegalEntity: f.w.LegalEntity, Currency: "NGN", PoolID: f.cfg.PoolID, PolicyVersion: "fixture-v1", Authorization: map[string]string{"evidence_ref": "reconciled-test-cash", "approved_by": "contract-fixture"}, Entries: entries})
}
func TestMigrations(t *testing.T) { testkit.MigrationRoundTrip(t, migrations.FS) }
func TestWalletRolesConcurrencyAndIsolation(t *testing.T) {
	f := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for n := 0; n < 24; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, e := f.s.EnsureWallet(f.user, core.Provision{Domain: "individual-owner", AccountID: f.account, Role: "OWNER", PartyKind: "PERSON", LegalEntity: "cosmicforge-ng", Currency: "NGN", RegistrationRef: "duplicate"})
			if e == nil && w.WalletID != f.w.WalletID {
				e = errors.New("duplicate wallet")
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, test := range []struct {
		svc        platform.Service
		domain     platform.Domain
		role, kind string
	}{{platform.MarketplaceService, platform.DomainMarketplace, "CUSTOMER", "PERSON"}, {platform.BusinessOperationsService, platform.DomainBusinessOperations, "CORPORATE_OWNER", "ORGANIZATION"}, {platform.BusinessOperationsService, platform.DomainBusinessOperations, "DEVELOPER", "ORGANIZATION"}, {platform.BusinessOperationsService, platform.DomainBusinessOperations, "HOSPITALITY", "ORGANIZATION"}, {platform.PropertyManagerService, platform.DomainPropertyManager, "MANAGER", "PERSON"}, {platform.PropertyManagerService, platform.DomainPropertyManager, "MANAGEMENT_COMPANY", "ORGANIZATION"}, {platform.ArtisanService, platform.DomainArtisan, "ARTISAN", "PERSON"}, {platform.ArtisanService, platform.DomainArtisan, "SERVICE_COMPANY", "ORGANIZATION"}, {platform.ProfessionalServicesService, platform.DomainProfessionalServices, "PROFESSIONAL", "PERSON"}, {platform.ProfessionalServicesService, platform.DomainProfessionalServices, "PROFESSIONAL_FIRM", "ORGANIZATION"}} {
		a := ids.New()
		if _, e := f.s.EnsureWallet(ctx(test.svc, test.domain, a), core.Provision{Domain: string(test.domain), AccountID: a, Role: test.role, PartyKind: test.kind, LegalEntity: "cosmicforge-ng", Currency: "NGN", RegistrationRef: "registered"}); e != nil {
			t.Fatal(test.role, e)
		}
	}
	if _, e := f.s.OwnWallet(ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, ids.New()), f.w.WalletID); e == nil {
		t.Fatal("horizontal access")
	}
	if _, e := f.s.OwnWallet(ctx(platform.PropertyManagerService, platform.DomainIndividualOwner, f.account), f.w.WalletID); e == nil {
		t.Fatal("manager impersonation")
	}
	if _, e := f.s.OwnWallet(context.Background(), f.w.WalletID); e == nil {
		t.Fatal("unauthenticated access")
	}
	customer := ids.New()
	coOwner, e := f.s.EnsureWallet(f.user, core.Provision{Domain: "individual-owner", AccountID: f.account, Role: "CO_OWNER", PartyKind: "PERSON", LegalEntity: "cosmicforge-ng", Currency: "NGN", RegistrationRef: "registered-co-role"})
	if e != nil || coOwner.IdentityID != f.w.IdentityID || coOwner.WalletID == f.w.WalletID {
		t.Fatal("co-owner role isolation", coOwner, e)
	}
	if e := f.s.LinkAccount(ctx(platform.VerificationService, "", ids.Nil), f.w.IdentityID, "marketplace", customer, "verified-party-link"); e != nil {
		t.Fatal(e)
	}
	if e := f.s.LinkAccount(ctx(platform.VerificationService, "", ids.Nil), f.w.IdentityID, "marketplace", customer, "verified-party-link"); e != nil {
		t.Fatal("duplicate verified linkage", e)
	}
	w, e := f.s.EnsureWallet(ctx(platform.MarketplaceService, platform.DomainMarketplace, customer), core.Provision{Domain: "marketplace", AccountID: customer, Role: "CUSTOMER", PartyKind: "PERSON", LegalEntity: "cosmicforge-ng", Currency: "NGN", RegistrationRef: "customer-registration"})
	if e != nil || w.IdentityID != f.w.IdentityID {
		t.Fatal("cross-role identity", e)
	}
	if e = f.s.LinkAccount(f.user, f.w.IdentityID, "artisan", ids.New(), "made-up"); e == nil {
		t.Fatal("unauthorized link")
	}
}
func TestLedgerInvariantsImmutableAndReversal(t *testing.T) {
	f := setup(t)
	a := f.accounts("PROVIDER_RECEIVABLE", "PAYMENT_PENDING")
	es := []core.Entry{{AccountID: a["PROVIDER_RECEIVABLE"], Side: "DEBIT", AmountMinor: 100}, {AccountID: a["PAYMENT_PENDING"], Side: "CREDIT", AmountMinor: 100}}
	id, e := f.post("credit-1", es)
	if e != nil {
		t.Fatal(e)
	}
	id2, e := f.post("credit-1", es)
	if e != nil || id2 != id {
		t.Fatal("idempotency", e)
	}
	es[0].AmountMinor = 99
	if _, e = f.post("unbalanced", es); e == nil {
		t.Fatal("unbalanced accepted")
	}
	es[1].AmountMinor = 99
	if _, e = f.post("credit-1", es); e == nil {
		t.Fatal("fingerprint conflict")
	}
	for _, q := range []string{`UPDATE journals SET policy_version='tampered' WHERE journal_id=$1`, `DELETE FROM journals WHERE journal_id=$1`, `UPDATE journal_entries SET amount_minor=101 WHERE journal_id=$1`, `DELETE FROM journal_entries WHERE journal_id=$1`, `INSERT INTO journal_entries(entry_id,journal_id,account_id,side,amount_minor) SELECT gen_random_uuid(),journal_id,$2,'DEBIT',100 FROM journals WHERE journal_id=$1`} {
		args := []any{id}
		if bytes.Contains([]byte(q), []byte("$2")) {
			args = append(args, a["PROVIDER_RECEIVABLE"])
		}
		if _, e = f.db.Pool.Exec(context.Background(), q, args...); e == nil {
			t.Fatal("mutable history", q)
		}
	}
	if _, e = f.s.Reverse(system(), id, "correction-1", "correction-v1", "approved-correction", "finance-fixture"); e != nil {
		t.Fatal(e)
	}
	var balance string
	if e = f.db.Pool.QueryRow(context.Background(), `SELECT balance_minor::text FROM account_balances WHERE account_id=$1`, a["PAYMENT_PENDING"]).Scan(&balance); e != nil || balance != "0" {
		t.Fatal("reversal", balance, e)
	}
	if _, e = f.s.Post(f.user, core.Posting{}); e == nil {
		t.Fatal("raw posting authorization")
	}
}
func TestAvailableCoverageAndContendedReservations(t *testing.T) {
	f := setup(t)
	a := f.accounts("SETTLED_CASH", "AVAILABLE", "PROTECTED", "PROVIDER_RECEIVABLE")
	fund := []core.Entry{{AccountID: a["SETTLED_CASH"], Side: "DEBIT", AmountMinor: 100}, {AccountID: a["AVAILABLE"], Side: "CREDIT", AmountMinor: 100}}
	if _, e := f.post("blocked-release", fund); e == nil {
		t.Fatal("restricted pool funded release")
	}
	if _, e := f.db.Pool.Exec(context.Background(), `UPDATE custody_pools SET restricted=false WHERE pool_id=$1`, f.cfg.PoolID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.post("approved-fixture-funding", fund); e != nil {
		t.Fatal(e)
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, e := f.post(fmt.Sprintf("reserve-%d", n), []core.Entry{{AccountID: a["AVAILABLE"], Side: "DEBIT", AmountMinor: 10}, {AccountID: a["PROTECTED"], Side: "CREDIT", AmountMinor: 10}})
			if e == nil {
				success.Add(1)
			}
		}(n)
	}
	wg.Wait()
	if success.Load() != 10 {
		t.Fatalf("successful spends %d", success.Load())
	}
	var available string
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT balance_minor::text FROM account_balances WHERE account_id=$1`, a["AVAILABLE"]).Scan(&available)
	if available != "0" {
		t.Fatal(available)
	}
	if _, e := f.post("unfunded-receivable-release", []core.Entry{{AccountID: a["PROVIDER_RECEIVABLE"], Side: "DEBIT", AmountMinor: 1}, {AccountID: a["AVAILABLE"], Side: "CREDIT", AmountMinor: 1}}); e == nil {
		t.Fatal("unsettled receivable allowed available")
	}
}
func TestCollectionsExactlyOnceAndSettlementSeparation(t *testing.T) {
	f := setup(t)
	i, _ := f.intent("collect-1")
	f.fake.success(i)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.s.VerifyIntent(f.user, i.ID); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM journals`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	out, e := f.s.GetIntent(f.user, i.ID)
	if e != nil || out.SettlementState != "PENDING" || out.State != "VERIFIED_SUCCESS" {
		t.Fatal(out, e)
	}
	bs, e := f.s.Balances(f.user, f.w.WalletID)
	if e != nil {
		t.Fatal(e)
	}
	if len(bs) != 7 {
		t.Fatal("seven views", len(bs))
	}
	for _, b := range bs {
		if b.Withdrawable || (b.Kind == "AVAILABLE" && b.AmountMinor != "0") {
			t.Fatal("unfunded funds withdrawable", b)
		}
	}
	if e = f.s.ObserveSettlement(ctx(platform.AdminService, "", ids.Nil), f.cfg.ID, "batch-1"); e != nil {
		t.Fatal(e)
	}
	out, _ = f.s.GetIntent(f.user, i.ID)
	if out.SettlementState != "PENDING" {
		t.Fatal("settlement fabricated")
	}
	if e = f.s.ReconcileBalance(ctx(platform.AdminService, "", ids.Nil), f.cfg.ID); e != nil {
		t.Fatal(e)
	}
	var restricted bool
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT restricted FROM custody_pools WHERE pool_id=$1`, f.cfg.PoolID).Scan(&restricted)
	if !restricted {
		t.Fatal("reconciliation mismatch unrestricted")
	}
}
func TestRoutingAndUnknownOutcomeNeverRecharges(t *testing.T) {
	f := setup(t)
	f.fake.initializeUnknown = true
	i, in := f.intent("timeout")
	if i.State != "UNKNOWN_OUTCOME" {
		t.Fatal(i.State)
	}
	f.fake.success(i)
	f.cfg.Operational = false
	f.cfg.Version = "v2"
	if e := f.s.Register(system(), f.cfg, finance.Adapter{Collection: f.fake, DVA: f.fake, Settlement: f.fake}); e != nil {
		t.Fatal(e)
	}
	got, e := f.s.CreateIntent(f.user, in, "timeout")
	if e != nil || got.ProviderAccountID != i.ProviderAccountID || f.fake.initializes.Load() != 1 {
		t.Fatal("recharge/reroute", got, e)
	}
	if _, e = f.s.VerifyIntent(f.user, i.ID); e != nil {
		t.Fatal("original provider readback", e)
	}
	alternate := f.cfg
	alternate.ID = ids.New()
	alternate.PoolID = ids.New()
	alternate.MerchantRef = "1002"
	alternate.Operational = true
	alternate.Version = "new-route"
	p := &fakeProvider{}
	if e = f.s.Register(system(), alternate, finance.Adapter{Collection: p}); e != nil {
		t.Fatal(e)
	}
	newIntent, e := f.s.CreateIntent(f.user, in, "new-charge")
	if e != nil || newIntent.ProviderAccountID != alternate.ID {
		t.Fatal("new route", e)
	}
	if e = f.s.MigrateProvider(f.user, i.ID, alternate.ID); e == nil {
		t.Fatal("unapproved migration")
	}
	in.Currency = "USD"
	if _, e = f.s.CreateIntent(f.user, in, "wrong-currency"); e == nil {
		t.Fatal("currency mismatch")
	}
	in.Currency = "NGN"
	in.RequiresCustody = true
	if _, e = f.s.CreateIntent(f.user, in, "custody"); e == nil {
		t.Fatal("unsupported custody")
	}
}
func TestChargeContextMismatches(t *testing.T) {
	for _, field := range []string{"amount", "currency", "reference", "intent", "merchant", "mode"} {
		t.Run(field, func(t *testing.T) {
			f := setup(t)
			i, _ := f.intent("context")
			f.fake.success(i)
			f.fake.mu.Lock()
			switch field {
			case "amount":
				f.fake.charge.AmountMinor++
			case "currency":
				f.fake.charge.Currency = "USD"
			case "reference":
				f.fake.charge.Reference = "wrong"
			case "intent":
				f.fake.charge.IntentID = ids.New().String()
			case "merchant":
				f.fake.charge.MerchantID = 123
			case "mode":
				f.fake.charge.Mode = "live"
			}
			f.fake.mu.Unlock()
			if _, e := f.s.VerifyIntent(f.user, i.ID); e == nil {
				t.Fatal("mismatch accepted")
			}
			var n int
			_ = f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM journals`).Scan(&n)
			if n != 0 {
				t.Fatal("mismatch credit")
			}
		})
	}
}
func TestDurableWebhookRecoveryAndReplay(t *testing.T) {
	f := setup(t)
	i, _ := f.intent("webhook")
	gateway := ctx(platform.APIGateway, "", ids.Nil)
	body := []byte(`{"event":"charge.success","data":{"reference":"` + i.Reference + `"}}`)
	if e := f.s.IngestWebhook(gateway, f.cfg.ID, body, "bad"); e == nil {
		t.Fatal("bad signature")
	}
	for n := 0; n < 5; n++ {
		if e := f.s.IngestWebhook(gateway, f.cfg.ID, body, "valid-test-fixture"); e != nil {
			t.Fatal(e)
		}
	}
	f.fake.verifyUnavailable = true
	if _, e := f.s.ProcessInbox(system()); e != nil {
		t.Fatal(e)
	}
	var status string
	var count int
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM provider_inbox`).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate inbox", count)
	}
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT status FROM provider_inbox`).Scan(&status)
	if status != "PENDING" {
		t.Fatal("failed webhook not retryable", status)
	}
	f.fake.verifyUnavailable = false
	f.fake.success(i)
	_, _ = f.db.Pool.Exec(context.Background(), `UPDATE provider_inbox SET status='PROCESSING',lease_until=now()-interval '1 minute',next_attempt_at=now()`)
	if _, e := f.s.ProcessInbox(system()); e != nil {
		t.Fatal(e)
	}
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT status FROM provider_inbox`).Scan(&status)
	if status != "PROCESSED" {
		t.Fatal(status)
	}
	var inbox ids.ID
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT inbox_id FROM provider_inbox`).Scan(&inbox)
	if e := f.s.ReplayInbox(ctx(platform.AdminService, "", ids.Nil), inbox, "approved-replay"); e != nil {
		t.Fatal(e)
	}
	_, _ = f.s.ProcessInbox(system())
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM journals`).Scan(&count)
	if count != 1 {
		t.Fatal("replay double credit", count)
	}
}
func TestDVAConsentVerificationIdempotencyAndSync(t *testing.T) {
	f := setup(t)
	in := finance.DVAInput{WalletID: f.w.WalletID, ProviderAccountID: f.cfg.ID, VerificationRef: "kyc-ref", Customer: providers.CustomerRequest{Email: "dva@example.invalid", FirstName: "Fixture", LastName: "Person"}, Validation: providers.Validation{Country: "NG", Type: "bank_account", BVN: "fixture-only", BankCode: "001", AccountNumber: "0123456789"}}
	if _, e := f.s.RequestDVA(f.user, in); e == nil {
		t.Fatal("missing consent")
	}
	in.ConsentRef = "consent-v1"
	in.ConsentVersion = "v1"
	in.ConsentAt = time.Now().Add(-time.Minute)
	in.ConsentExpiresAt = time.Now().Add(time.Hour)
	if _, e := f.s.RequestDVA(f.user, in); e == nil {
		t.Fatal("missing verification")
	}
	if e := f.s.Verification(ctx(platform.VerificationService, "", ids.Nil), f.w.IdentityID, "kyc-ref", "VERIFIED", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	f.fake.identified = true
	x, e := f.s.RequestDVA(f.user, in)
	if e != nil || x.State != "ACTIVE" {
		t.Fatal(x, e)
	}
	again, e := f.s.RequestDVA(f.user, in)
	if e != nil || again.ID != x.ID || f.fake.assignments.Load() != 1 {
		t.Fatal("DVA duplicate", e)
	}
	if x.AccountMask == nil || *x.AccountMask != "******6789" {
		t.Fatal("mask", x)
	}
	f.fake.dvaInactive = true
	x, e = f.s.SyncDVA(f.user, x.ID, false)
	if e != nil || x.State != "INACTIVE" {
		t.Fatal("status sync", x, e)
	}
}
func TestHTTPAuthenticationAndOwnership(t *testing.T) {
	f := setup(t)
	reg := serviceauth.NewRegistry()
	key := testkit.SigningKey(t, "owner-test")
	_ = reg.Add(platform.IndividualOwnerService, key.KID, key.PublicKey())
	ver, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.PaymentService, Registry: reg})
	if e != nil {
		t.Fatal(e)
	}
	signer, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.IndividualOwnerService, Key: key})
	if e != nil {
		t.Fatal(e)
	}
	router := phttp.NewRouter(phttp.Config{Service: f.s, Verifier: ver, Logger: f.s.Log, Health: health.New("payment-service", "test", time.Second)})
	call := func(token string) int {
		r := httptest.NewRequest("GET", "/internal/v1/wallets/"+f.w.WalletID.String(), nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	if call("") != 401 || call("invalid") != 401 {
		t.Fatal("auth")
	}
	tok, _ := signer.TokenFor("payment-service", serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: f.account})
	if call(tok) != 200 {
		t.Fatal("valid auth")
	}
	tok, _ = signer.TokenFor("payment-service", serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: ids.New()})
	if call(tok) != 404 {
		t.Fatal("cross user")
	}
}

type fakeProvider struct {
	mu                                                            sync.Mutex
	charge                                                        providers.Charge
	initializes, assignments                                      atomic.Int32
	initializeUnknown, verifyUnavailable, identified, dvaInactive bool
}

func (p *fakeProvider) InitializeCharge(_ context.Context, in providers.ChargeRequest) (providers.Initialized, error) {
	p.initializes.Add(1)
	if p.initializeUnknown {
		return providers.Initialized{}, providers.ErrUnknown
	}
	return providers.Initialized{Reference: in.Reference, CheckoutURL: "https://checkout.example.invalid/fixture"}, nil
}
func (p *fakeProvider) success(i finance.Intent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.charge = providers.Charge{Reference: i.Reference, State: "VERIFIED_SUCCESS", Currency: i.Currency, Mode: "test", IntentID: i.ID.String(), AmountMinor: i.AmountMinor, MerchantID: 1001}
}
func (p *fakeProvider) VerifyCharge(context.Context, string) (providers.Charge, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.verifyUnavailable {
		return providers.Charge{}, providers.ErrUnknown
	}
	return p.charge, nil
}
func (p *fakeProvider) CancelCharge(context.Context, string) error { return providers.ErrUnsupported }
func (p *fakeProvider) ReceiveWebhook(b []byte, sig string) (providers.Callback, error) {
	if sig != "valid-test-fixture" {
		return providers.Callback{}, providers.ErrSignature
	}
	var x struct {
		Event string `json:"event"`
		Data  struct {
			Reference string `json:"reference"`
		} `json:"data"`
	}
	if e := json.Unmarshal(b, &x); e != nil {
		return providers.Callback{}, e
	}
	return providers.Callback{EventType: x.Event, Reference: x.Data.Reference}, nil
}
func (p *fakeProvider) CreateCustomer(context.Context, providers.CustomerRequest) (providers.Customer, error) {
	return providers.Customer{Code: "CUS_fixture", MerchantID: 1001, Mode: "test"}, nil
}
func (p *fakeProvider) FetchCustomer(context.Context, string) (providers.Customer, error) {
	return providers.Customer{Code: "CUS_fixture", Identified: p.identified, MerchantID: 1001, Mode: "test"}, nil
}
func (p *fakeProvider) ValidateCustomer(context.Context, string, providers.Validation) error {
	return nil
}
func (p *fakeProvider) AssignDVA(context.Context, string) (providers.VirtualAccount, error) {
	p.assignments.Add(1)
	return p.FetchDVA(context.Background(), "1")
}
func (p *fakeProvider) FetchDVA(context.Context, string) (providers.VirtualAccount, error) {
	return providers.VirtualAccount{ID: "1", CustomerRef: "CUS_fixture", AccountNumber: "0123456789", BankCode: "titan-paystack", Active: !p.dvaInactive, MerchantID: 1001, Mode: "test"}, nil
}
func (p *fakeProvider) FindDVA(context.Context, string) (providers.VirtualAccount, error) {
	return p.FetchDVA(context.Background(), "1")
}
func (p *fakeProvider) RequeryDVA(context.Context, string, string) error { return nil }
func (p *fakeProvider) DeactivateDVA(context.Context, string) error      { p.dvaInactive = true; return nil }
func (p *fakeProvider) FetchSettlement(context.Context, string) (providers.Settlement, error) {
	return providers.Settlement{Reference: "batch-1", AmountMinor: 10000, Currency: "NGN", State: "success"}, nil
}
func (p *fakeProvider) FetchBalance(context.Context) ([]providers.MerchantBalance, error) {
	return []providers.MerchantBalance{{Currency: "NGN", AmountMinor: 10000}}, nil
}
func (p *fakeProvider) ListTransactions(context.Context, string) ([]providers.Charge, error) {
	return nil, nil
}

var _ = http.MethodGet
