package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	phttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"github.com/jackc/pgx/v5/pgxpool"
	"io/fs"
	"math"
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

type transferFixture struct {
	*fixture
	l        *lifecycle.Service
	wallets  []core.Wallet
	actors   []context.Context
	accounts []ids.ID
	policy   lifecycle.WalletTransferPolicy
}

func newTransfers(t *testing.T) *transferFixture {
	f := setup(t)
	out := &transferFixture{fixture: f, l: lifecycle.New(f.s), wallets: []core.Wallet{f.w}, actors: []context.Context{f.user}}
	for n := 0; n < 2; n++ {
		a := ids.New()
		actor := ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, a)
		w, e := f.s.EnsureWallet(actor, core.Provision{Domain: "individual-owner", AccountID: a, Role: "OWNER", PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "authorized-synthetic-financial-principal"})
		if e != nil {
			t.Fatal(e)
		}
		out.wallets = append(out.wallets, w)
		out.actors = append(out.actors, actor)
	}
	for _, w := range out.wallets {
		if e := f.s.Verification(verifier(), w.IdentityID, "synthetic-current-verification", "VERIFIED", time.Now().Add(time.Hour)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := f.db.Pool.Exec(system(), `UPDATE custody_pools SET restricted=false WHERE pool_id=$1`, f.cfg.PoolID); e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.Pool.Begin(system())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(system())
	for _, w := range out.wallets {
		a, e := f.s.Account(system(), tx, &w.WalletID, f.cfg.PoolID, "AVAILABLE", w.LegalEntity, w.Currency, "")
		if e != nil {
			t.Fatal(e)
		}
		out.accounts = append(out.accounts, a)
	}
	cash, e := f.s.Account(system(), tx, nil, f.cfg.PoolID, "SETTLED_CASH", f.w.LegalEntity, "NGN", "")
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.PostTx(system(), tx, core.Posting{OperationRef: "isolated-synthetic-coverage", LegalEntity: f.w.LegalEntity, Currency: "NGN", PoolID: f.cfg.PoolID, PolicyVersion: "test-only", Authorization: map[string]string{"evidence_ref": "synthetic-cash-not-provider-balance", "approved_by": "fixture"}, Entries: []core.Entry{{AccountID: cash, Side: "DEBIT", AmountMinor: 12000000}, {AccountID: out.accounts[0], Side: "CREDIT", AmountMinor: 3000000}, {AccountID: out.accounts[1], Side: "CREDIT", AmountMinor: 1000000}, {AccountID: out.accounts[2], Side: "CREDIT", AmountMinor: 8000000}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(system()); e != nil {
		t.Fatal(e)
	}
	out.policy = lifecycle.WalletTransferPolicy{ID: ids.New(), PoolID: f.cfg.PoolID, LegalEntity: f.w.LegalEntity, Currency: "NGN", Jurisdiction: "NG", Version: "test-remittance-v1", SourceDomains: []string{"individual-owner"}, MaxMinor: 5000000, DailyMinor: 5000000, LocalTestOnly: true, EvidenceRef: "explicit-local-transfer-approval", Approvers: []string{"maker", "checker"}, EffectiveAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}
	if e = out.l.PutWalletTransferPolicy(admin(), out.policy); e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *transferFixture) request(amount int64) lifecycle.WalletTransferInput {
	return lifecycle.WalletTransferInput{ID: ids.New(), SourceWalletID: f.wallets[0].WalletID, DestinationWalletID: f.wallets[1].WalletID, PoolID: f.cfg.PoolID, PolicyID: f.policy.ID, Currency: "NGN", AmountMinor: amount, Purpose: "CUSTOMER_REMITTANCE", ConsentRef: "explicit-authenticated-test-command"}
}
func (f *transferFixture) balances() []int64 {
	out := []int64{}
	for _, a := range f.accounts {
		var n int64
		if e := f.db.Pool.QueryRow(system(), `SELECT balance_minor::bigint FROM account_balances WHERE account_id=$1`, a).Scan(&n); e != nil {
			f.t.Fatal(e)
		}
		out = append(out, n)
	}
	return out
}
func TestUniversalWalletPurposeIdentityAndActivation(t *testing.T) {
	f := setup(t)
	w, e := f.s.EnsureWallet(f.user, core.Provision{Domain: "individual-owner", AccountID: f.account, Role: "CO_OWNER", PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "co-owner-authority"})
	if e != nil || w.WalletID != f.w.WalletID {
		t.Fatal("role created another wallet", e)
	}
	profile, e := f.s.WalletProfile(f.user, w.WalletID)
	if e != nil || profile.Purpose != "EARNINGS" || len(profile.Roles) != 2 || profile.VerificationState != "PENDING" || len(profile.Accounts) != 0 {
		t.Fatal("structural identity fabricated KYC or funds", profile, e)
	}
	bad := core.Provision{Domain: "individual-owner", AccountID: ids.New(), Role: "OWNER", PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "forged-actor"}
	if _, e = f.s.EnsureWallet(f.user, bad); e == nil {
		t.Fatal("provisioned another delegated actor")
	}
	bad.AccountID = f.account
	bad.Role = "ADMIN"
	if _, e = f.s.EnsureWallet(f.user, bad); e == nil {
		t.Fatal("staff received wallet")
	}
	bad.Role = "OWNER"
	bad.LegalEntity = "another-permitted-entity"
	other, e := f.s.EnsureWallet(f.user, bad)
	if e != nil || other.WalletID == w.WalletID {
		t.Fatal("entity collapsed", e)
	}
	if e = f.s.ControlIdentity(admin(), f.w.IdentityID, core.IdentityControl{State: "RESTRICTED", EvidenceRef: "freeze", PolicyVersion: "v1"}); e != nil {
		t.Fatal(e)
	}
	bad.LegalEntity = f.w.LegalEntity
	bad.Currency = "USD"
	if _, e = f.s.EnsureWallet(f.user, bad); e == nil {
		t.Fatal("restricted identity provisioned currency wallet")
	}
}
func TestInternalTransferConservationAndConcurrentReplay(t *testing.T) {
	f := newTransfers(t)
	in := f.request(100000)
	v, e := f.l.RequestWalletTransfer(f.actors[0], in, "one-economic-transfer")
	if e != nil {
		t.Fatal(e)
	}
	if b := f.balances(); b[0] != 3000000 || b[1] != 1000000 {
		t.Fatal("request debited")
	}
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.l.RequestWalletTransfer(f.actors[0], in, "one-economic-transfer")
			if e != nil || r.ID != v.ID {
				t.Error("duplicate request", e)
			}
			r, e = f.l.ConfirmWalletTransfer(f.actors[0], v.ID)
			if e != nil || r.State != "POSTED" {
				t.Error("concurrent confirmation", e)
			}
		}()
	}
	wg.Wait()
	b := f.balances()
	if b[0] != 2900000 || b[1] != 1100000 || b[2] != 8000000 || b[0]+b[1]+b[2] != 12000000 {
		t.Fatal("money not conserved", b)
	}
	var journals, facts int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM journals WHERE operation_ref LIKE 'wallet-transfer:%'`).Scan(&journals); e != nil || journals != 1 {
		t.Fatal("duplicated posting", e)
	}
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM wallet_transfer_history WHERE transfer_id=$1`, v.ID).Scan(&facts); e != nil || facts != 2 {
		t.Fatal("duplicated history", e)
	}
	in.AmountMinor++
	if _, e = f.l.RequestWalletTransfer(f.actors[0], in, "one-economic-transfer"); e == nil {
		t.Fatal("fingerprint changed")
	}
	if _, e = f.l.ConfirmWalletTransfer(f.actors[1], v.ID); e == nil {
		t.Fatal("receiver can initiate debit")
	}
	if _, e = f.l.WalletTransfer(f.actors[2], v.ID); e == nil {
		t.Fatal("IDOR")
	}
	if _, e = f.l.WalletTransfer(f.actors[1], v.ID); e != nil {
		t.Fatal("receiver read denied", e)
	}
	for _, sql := range []string{`UPDATE wallet_transfers SET amount_minor=1 WHERE transfer_id=$1`, `DELETE FROM wallet_transfers WHERE transfer_id=$1`, `UPDATE wallet_transfer_history SET state='FORGED' WHERE transfer_id=$1`} {
		if _, e = f.db.Pool.Exec(system(), sql, v.ID); e == nil {
			t.Fatal("mutable transfer history")
		}
	}
	validateLifecycleOutbox(t, &lifeFixture{fixture: f.fixture, l: f.l})
}
func TestInternalTransferAvailabilityBoundariesAndRollback(t *testing.T) {
	f := newTransfers(t)
	// A cannot borrow from B/C or use pending/protected balances.
	in := f.request(3500000)
	v, e := f.l.RequestWalletTransfer(f.actors[0], in, "insufficient")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.ConfirmWalletTransfer(f.actors[0], v.ID); e == nil {
		t.Fatal("spent another wallet's balance")
	}
	b := f.balances()
	if b[0] != 3000000 || b[1] != 1000000 || b[2] != 8000000 {
		t.Fatal("failed transfer partially committed", b)
	}
	// Rollback after the journal is created must roll back both sides/outbox too.
	if _, e = f.db.Pool.Exec(system(), `CREATE FUNCTION fail_test_transfer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled precommit failure'; END $$; CREATE TRIGGER fail_transfer_test BEFORE UPDATE ON wallet_transfers FOR EACH ROW EXECUTE FUNCTION fail_test_transfer()`); e != nil {
		t.Fatal(e)
	}
	in = f.request(100000)
	v, e = f.l.RequestWalletTransfer(f.actors[0], in, "rollback")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.ConfirmWalletTransfer(f.actors[0], v.ID); e == nil {
		t.Fatal("fault injection did not fail")
	}
	if b = f.balances(); b[0] != 3000000 || b[1] != 1000000 {
		t.Fatal("rollback lost atomicity", b)
	}
	if _, e = f.db.Pool.Exec(system(), `DROP TRIGGER fail_transfer_test ON wallet_transfers; DROP FUNCTION fail_test_transfer()`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.ConfirmWalletTransfer(f.actors[0], v.ID); e != nil {
		t.Fatal("original retry did not recover", e)
	}
	for name, mutate := range map[string]func(*lifecycle.WalletTransferInput){"self": func(v *lifecycle.WalletTransferInput) { v.DestinationWalletID = v.SourceWalletID }, "currency": func(v *lifecycle.WalletTransferInput) { v.Currency = "USD" }, "purpose": func(v *lifecycle.WalletTransferInput) { v.Purpose = "RENT" }, "overflow": func(v *lifecycle.WalletTransferInput) { v.AmountMinor = math.MaxInt64 }, "invalid-destination": func(v *lifecycle.WalletTransferInput) { v.DestinationWalletID = ids.New() }} {
		t.Run(name, func(t *testing.T) {
			v := f.request(100)
			mutate(&v)
			if _, e := f.l.RequestWalletTransfer(f.actors[0], v, name); e == nil {
				t.Fatal("invalid transfer allowed")
			}
		})
	}
	if e = f.s.ControlWallet(admin(), f.wallets[1].WalletID, core.IdentityControl{State: "RESTRICTED", EvidenceRef: "freeze-recipient", PolicyVersion: "v1"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.RequestWalletTransfer(f.actors[0], f.request(100), "frozen-recipient"); e == nil {
		t.Fatal("disabled receiver")
	}
	f.s.Environment = "production"
	if _, e = f.l.RequestWalletTransfer(f.actors[0], f.request(100), "live"); e == nil {
		t.Fatal("live internal transfer enabled")
	}
}
func TestInternalTransferCompetingDebitsAndRevocation(t *testing.T) {
	f := newTransfers(t)
	var wg sync.WaitGroup
	var success atomic.Int32
	for n := 0; n < 2; n++ {
		in := f.request(2000000)
		v, e := f.l.RequestWalletTransfer(f.actors[0], in, in.ID.String())
		if e != nil {
			t.Fatal(e)
		}
		wg.Add(1)
		go func(id ids.ID) {
			defer wg.Done()
			if _, e := f.l.ConfirmWalletTransfer(f.actors[0], id); e == nil {
				success.Add(1)
			}
		}(v.ID)
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatal("concurrent double spend", success.Load())
	}
	b := f.balances()
	if b[0] != 1000000 || b[1] != 3000000 || b[2] != 8000000 {
		t.Fatal(b)
	}
	in := f.request(100)
	v, e := f.l.RequestWalletTransfer(f.actors[0], in, "verification-revoked")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Verification(verifier(), f.wallets[1].IdentityID, "revoked-current-verification", "REVOKED", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.ConfirmWalletTransfer(f.actors[0], v.ID); e == nil {
		t.Fatal("request-time verification reused after revocation")
	}
}
func TestCapabilityEvidenceCannotActivateProduction(t *testing.T) {
	f := setup(t)
	in := finance.CapabilityEvidence{ID: ids.New(), Capability: "INTERNAL_TRANSFER", Stage: "REGULATORY", LegalEntity: f.w.LegalEntity, ProviderID: &f.cfg.ID, Jurisdiction: "NG", Version: "synthetic-record-only", Status: "APPROVED", EvidenceRef: "synthetic-not-a-license", IssuerRef: "test-authority", Approvers: []string{"maker", "checker"}, EffectiveAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}
	if e := f.s.RecordCapabilityEvidence(f.user, in); e == nil {
		t.Fatal("unprivileged regulatory approval")
	}
	if e := f.s.RecordCapabilityEvidence(admin(), in); e != nil {
		t.Fatal(e)
	}
	if e := f.s.RecordCapabilityEvidence(admin(), in); e != nil {
		t.Fatal(e)
	}
	in.Status = "REVOKED"
	if e := f.s.RecordCapabilityEvidence(admin(), in); e == nil {
		t.Fatal("mutable approval evidence")
	}
	f.s.Environment = "production"
	out, e := f.s.EvaluateCapability(f.user, finance.CapabilityRequest{Capability: "INTERNAL_TRANSFER", WalletID: f.w.WalletID, ProviderID: f.cfg.ID, Jurisdiction: "NG", PolicyRef: "test-policy"})
	if e != nil || out.Allowed || out.Reason != "PRODUCTION_CAPABILITY_DISABLED" || out.Stages["REGULATORY"] != "APPROVED" || out.Stages["SAFEGUARDING"] != "NOT_RUN" {
		t.Fatal("approval collapsed independent gates", out, e)
	}
	if _, e = f.db.Pool.Exec(system(), `DELETE FROM financial_capability_evidence WHERE evidence_id=$1`, in.ID); e == nil {
		t.Fatal("deleted evidence")
	}
}

func TestRandomizedInternalTransfersPreserveEveryWallet(t *testing.T) {
	f := newTransfers(t)
	r := rand.New(rand.NewSource(1114))
	expected := []int64{3000000, 1000000, 8000000}
	for n := 0; n < 48; n++ {
		src := r.Intn(3)
		dst := (src + 1 + r.Intn(2)) % 3
		amount := int64(r.Intn(10000) + 1)
		in := f.request(amount)
		in.SourceWalletID = f.wallets[src].WalletID
		in.DestinationWalletID = f.wallets[dst].WalletID
		key := fmt.Sprintf("random-transfer-%d", n)
		tr, e := f.l.RequestWalletTransfer(f.actors[src], in, key)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.l.ConfirmWalletTransfer(f.actors[src], tr.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = f.l.ConfirmWalletTransfer(f.actors[src], tr.ID); e != nil {
			t.Fatal("replay", e)
		}
		expected[src] -= amount
		expected[dst] += amount
		got := f.balances()
		for k := range got {
			if got[k] != expected[k] || got[k] < 0 {
				t.Fatal(n, got, expected)
			}
		}
		if got[0]+got[1]+got[2] != 12000000 {
			t.Fatal("conservation", got)
		}
	}
	var bad int
	if e := f.db.Pool.QueryRow(system(), `SELECT count(*) FROM (SELECT journal_id FROM journal_entries GROUP BY journal_id HAVING sum(CASE WHEN side='DEBIT' THEN amount_minor ELSE -amount_minor END)<>0) j`).Scan(&bad); e != nil || bad != 0 {
		t.Fatal("unbalanced randomized journals", bad, e)
	}
}
func TestInternalTransferLimitsAndPoolEntityBoundaries(t *testing.T) {
	f := newTransfers(t)
	policy := f.policy
	policy.ID = ids.New()
	policy.MaxMinor = 1500000
	policy.DailyMinor = 1500000
	if e := f.l.PutWalletTransferPolicy(admin(), policy); e != nil {
		t.Fatal(e)
	}
	for n := 0; n < 2; n++ {
		in := f.request(1000000)
		in.PolicyID = policy.ID
		tr, e := f.l.RequestWalletTransfer(f.actors[0], in, fmt.Sprint("daily", n))
		if e != nil {
			t.Fatal(e)
		}
		_, e = f.l.ConfirmWalletTransfer(f.actors[0], tr.ID)
		if (n == 0 && e != nil) || (n == 1 && e == nil) {
			t.Fatal("daily limit", n, e)
		}
	}
	in := f.request(1000)
	in.PoolID = ids.New()
	if _, e := f.l.RequestWalletTransfer(f.actors[0], in, "foreign-pool"); e == nil {
		t.Fatal("foreign pool")
	}
	actor := ids.New()
	ctx := ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, actor)
	w, e := f.s.EnsureWallet(ctx, core.Provision{Domain: "individual-owner", AccountID: actor, Role: "OWNER", PartyKind: "PERSON", LegalEntity: "other-ng", Currency: "NGN", RegistrationRef: "other-entity"})
	if e != nil {
		t.Fatal(e)
	}
	in = f.request(1000)
	in.DestinationWalletID = w.WalletID
	if _, e = f.l.RequestWalletTransfer(f.actors[0], in, "foreign-entity"); e == nil {
		t.Fatal("foreign entity")
	}
	if e = f.s.ControlWallet(admin(), f.wallets[0].WalletID, core.IdentityControl{State: "RESTRICTED", EvidenceRef: "review", PolicyVersion: "risk-v1"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.RequestWalletTransfer(f.actors[0], f.request(1000), "frozen-source"); e == nil {
		t.Fatal("frozen source")
	}
	if _, e = f.s.WalletProfile(f.actors[0], f.wallets[0].WalletID); e != nil {
		t.Fatal("restricted wallet read lost", e)
	}
}

func TestFutureDomainSignedWalletContractStubs(t *testing.T) {
	f := setup(t)
	registry := serviceauth.NewRegistry()
	cases := []struct{ domain, role, kind string }{
		{"marketplace", "TENANT", "PERSON"}, {"marketplace", "AGENT", "PERSON"}, {"business-operations", "CORPORATE_OWNER", "ORGANIZATION"}, {"property-manager", "MANAGER", "PERSON"}, {"artisan", "ARTISAN", "PERSON"}, {"professional-services", "PROFESSIONAL", "PERSON"}}
	signers := map[string]*serviceauth.Signer{}
	for _, c := range cases {
		if signers[c.domain] != nil {
			continue
		}
		svc, _ := platform.ServiceForDomain(platform.Domain(c.domain))
		key := testkit.SigningKey(t, "future-"+c.domain)
		if e := registry.Add(svc, key.KID, key.PublicKey()); e != nil {
			t.Fatal(e)
		}
		signer, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: svc, Key: key})
		if e != nil {
			t.Fatal(e)
		}
		signers[c.domain] = signer
	}
	v, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.PaymentService, Registry: registry})
	if e != nil {
		t.Fatal(e)
	}
	router := phttp.NewRouter(phttp.Config{Service: f.s, Verifier: v, Health: health.New("payment-service", "test", time.Second), Logger: f.s.Log})
	for _, c := range cases {
		t.Run(c.domain+"_"+c.role, func(t *testing.T) {
			id := ids.New()
			in := core.Provision{Domain: c.domain, AccountID: id, Role: c.role, PartyKind: c.kind, LegalEntity: "cosmicforge-ng", Currency: "NGN", RegistrationRef: "stub-" + id.String()}
			call := func(method, path string, body any) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(body)
				r := httptest.NewRequest(method, path, bytes.NewReader(raw))
				r.Header.Set("Content-Type", "application/json")
				token, e := signers[c.domain].TokenFor("payment-service", serviceauth.Delegation{ActorDomain: platform.Domain(c.domain), ActorID: id})
				if e != nil {
					t.Fatal(e)
				}
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				return w
			}
			first := call("POST", "/internal/v1/wallets/ensure", in)
			if first.Code != 200 {
				t.Fatal(first.Code, first.Body.String())
			}
			var result struct {
				Data core.Wallet `json:"data"`
			}
			if e = json.Unmarshal(first.Body.Bytes(), &result); e != nil {
				t.Fatal(e)
			}
			replay := call("POST", "/internal/v1/wallets/ensure", in)
			var again struct {
				Data core.Wallet `json:"data"`
			}
			_ = json.Unmarshal(replay.Body.Bytes(), &again)
			if replay.Code != 200 || again.Data.WalletID != result.Data.WalletID {
				t.Fatal("provision replay")
			}
			own := call("GET", "/internal/v1/wallets/"+result.Data.WalletID.String()+"/profile", nil)
			if own.Code != 200 {
				t.Fatal(own.Code)
			}
			if got := call("GET", "/internal/v1/wallets/"+f.w.WalletID.String()+"/balances", nil); got.Code != 404 {
				t.Fatal("foreign financial balance visible", got.Code)
			}
			in.AccountID = ids.New()
			if got := call("POST", "/internal/v1/wallets/ensure", in); got.Code != 403 {
				t.Fatal("body forged account", got.Code)
			}
			in.Domain = "individual-owner"
			in.Role = "OWNER"
			in.PartyKind = "PERSON"
			if got := call("POST", "/internal/v1/wallets/ensure", in); got.Code != 403 {
				t.Fatal("forged source domain", got.Code)
			}
		})
	}
}
func TestAcceptedTermsInvoiceImmutableAndNoPaymentClaim(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	invoice, e := f.l.Invoice(f.user, f.quote.ID)
	if e != nil {
		t.Fatal(e)
	}
	if invoice.ID != f.quote.ID || invoice.Kind != "ACCEPTED_TERMS" || invoice.AmountMinor != 10000 || invoice.Revision != f.quote.QuoteVersion {
		t.Fatal("invoice terms")
	}
	b, e := os.ReadFile("../../../../contracts/internal/openapi/payment-service.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	schema := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$ref": "#/components/schemas/Invoice", "components": doc["components"]}
	raw, _ := json.Marshal(schema)
	path := filepath.Join(t.TempDir(), "invoice-schema.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(invoice)
	testkit.ValidateJSONSchema(t, path, body)
	if _, e = f.db.Pool.Exec(system(), `UPDATE locked_quotes SET snapshot=jsonb_set(snapshot,'{amount_minor}','9999') WHERE quote_id=$1`, f.quote.ID); e == nil {
		t.Fatal("invoice revision rewritten")
	}
	if _, e = f.l.Invoice(ctx(platform.APIGateway, platform.DomainIndividualOwner, ids.New()), f.quote.ID); e == nil {
		t.Fatal("invoice payer IDOR")
	}
	f.s.Environment = "production"
	a := f.allocate(t)
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: a.Lines[0].ID, AmountMinor: 1, PolicyVersion: f.policy.Version}, "production-release"); e == nil {
		t.Fatal("production release enabled")
	}
}

func TestHistoricalRoleWalletMigrationPreservesBalancesAndJournals(t *testing.T) {
	legacy := fstest.MapFS{}
	entries, e := fs.ReadDir(migrations.FS, ".")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") || strings.HasPrefix(entry.Name(), "00007_") {
			continue
		}
		raw, e := fs.ReadFile(migrations.FS, entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		legacy[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	db := testkit.NewDatabase(t, legacy)
	identity, account, a, b, provider, pool, asset, aa, ba, journal := ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	tx, e := db.Pool.Begin(system())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(system())
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO financial_identities(identity_id,legal_party_ref,party_kind) VALUES($1,$2,'PERSON')`, []any{identity, "individual-owner:" + account.String()}},
		{`INSERT INTO identity_bindings(domain,account_id,identity_id,registration_ref) VALUES('individual-owner',$1,$2,'historical')`, []any{account, identity}},
		{`INSERT INTO wallets(wallet_id,identity_id,domain,role,legal_entity,currency,created_at) VALUES($1,$2,'individual-owner','OWNER','cosmicforge-ng','NGN',now()-interval '1 hour'),($3,$2,'individual-owner','CO_OWNER','cosmicforge-ng','NGN',now())`, []any{a, identity, b}},
		{`INSERT INTO provider_accounts(provider_account_id,provider,legal_entity,merchant_ref,mode,config,config_version) VALUES($1,'historical-test','cosmicforge-ng','fixture','test','{}','v1')`, []any{provider}},
		{`INSERT INTO custody_pools(pool_id,provider_account_id,legal_entity,currency,label) VALUES($1,$2,'cosmicforge-ng','NGN','historical-test')`, []any{pool, provider}},
		{`INSERT INTO ledger_accounts(account_id,wallet_id,pool_id,legal_entity,currency,kind,normal_side) VALUES($1,NULL,$2,'cosmicforge-ng','NGN','PROVIDER_RECEIVABLE','DEBIT'),($3,$4,$2,'cosmicforge-ng','NGN','PENDING_EARNINGS','CREDIT'),($5,$6,$2,'cosmicforge-ng','NGN','PENDING_EARNINGS','CREDIT')`, []any{asset, pool, aa, a, ba, b}},
		{`INSERT INTO journals(journal_id,operation_ref,fingerprint,legal_entity,currency,pool_id,origin_service,policy_version,authorization_snapshot) VALUES($1,'historical-fixture','test','cosmicforge-ng','NGN',$2,'payment-service','v1','{}')`, []any{journal, pool}},
		{`INSERT INTO journal_entries(entry_id,journal_id,account_id,side,amount_minor) VALUES($1,$2,$3,'DEBIT',4000000),($4,$2,$5,'CREDIT',3000000),($6,$2,$7,'CREDIT',1000000)`, []any{ids.New(), journal, asset, ids.New(), aa, ids.New(), ba}},
	}
	for _, st := range statements {
		if _, e = tx.Exec(system(), st.sql, st.args...); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(system()); e != nil {
		t.Fatal(e)
	}
	sqlDB, e := migrate.Open(db.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer sqlDB.Close()
	if e = migrate.Up(system(), sqlDB, migrations.FS); e != nil {
		t.Fatal(e)
	}
	svc := core.New(db.Pool)
	actor := ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, account)
	canonical, e := svc.EnsureWallet(actor, core.Provision{Domain: "individual-owner", AccountID: account, Role: "CO_OWNER", PartyKind: "PERSON", LegalEntity: "cosmicforge-ng", Currency: "NGN", RegistrationRef: "post-migration-role"})
	if e != nil || canonical.WalletID != a {
		t.Fatal("historical canonical wallet", canonical, e)
	}
	var wallets, journals int
	var aBalance, bBalance string
	e = db.Pool.QueryRow(system(), `SELECT (SELECT count(*) FROM wallets),(SELECT count(*) FROM journals),(SELECT balance_minor::text FROM account_balances WHERE account_id=$1),(SELECT balance_minor::text FROM account_balances WHERE account_id=$2)`, aa, ba).Scan(&wallets, &journals, &aBalance, &bBalance)
	if e != nil || wallets != 2 || journals != 1 || aBalance != "3000000" || bBalance != "1000000" {
		t.Fatal("migration merged financial history", wallets, journals, aBalance, bBalance, e)
	}
	p, e := svc.WalletProfile(actor, b)
	if e != nil || !strings.HasPrefix(p.Purpose, "LEGACY_RETAINED:") {
		t.Fatal("historical wallet inaccessible", p, e)
	}
	if e = migrate.DownTo(system(), sqlDB, migrations.FS, 6); e == nil {
		t.Fatal("funded history rollback accepted")
	}
}

func TestCapabilityLocalEligibilityRequiresActualContext(t *testing.T) {
	f := newTransfers(t)
	in := finance.CapabilityRequest{Capability: "INTERNAL_TRANSFER", WalletID: f.w.WalletID, ProviderID: f.cfg.ID, Jurisdiction: "NG", PolicyRef: f.policy.ID.String()}
	out, e := f.s.EvaluateCapability(f.user, in)
	if e != nil || !out.Allowed || out.Reason != "LOCAL_ELIGIBLE_WORKFLOW_AUTHORIZATION_REQUIRED" || out.Stages["PRODUCTION"] != "NOT_RUN" {
		t.Fatal("local eligibility", out, e)
	}
	in.PolicyRef = ids.New().String()
	out, e = f.s.EvaluateCapability(f.user, in)
	if e != nil || out.Allowed {
		t.Fatal("invented policy", out, e)
	}
	in.PolicyRef = f.policy.ID.String()
	in.Jurisdiction = "UNKNOWN"
	if _, e = f.s.EvaluateCapability(f.user, in); e == nil {
		t.Fatal("invented jurisdiction")
	}
	in.Jurisdiction = "NG"
	in.WalletID = f.wallets[1].WalletID
	if _, e = f.s.EvaluateCapability(f.user, in); e == nil {
		t.Fatal("eligibility IDOR")
	}
}

func TestInternalTransferSmallPoolConcurrencyCompletes(t *testing.T) {
	f := newTransfers(t)
	cfg, e := pgxpool.ParseConfig(f.db.URL)
	if e != nil {
		t.Fatal(e)
	}
	cfg.MaxConns = 2
	pool, e := pgxpool.NewWithConfig(system(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	f.s.Service = core.New(pool) // retain adapter registry; only replace owned Core pool
	command := f.request(100000)
	transfer, e := f.l.RequestWalletTransfer(f.actors[0], command, "small-pool")
	if e != nil {
		t.Fatal(e)
	}
	bounded, cancel := context.WithTimeout(f.actors[0], 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 12)
	for range 12 {
		go func() { <-start; _, e := f.l.ConfirmWalletTransfer(bounded, transfer.ID); errs <- e }()
	}
	close(start)
	for range 12 {
		if e = <-errs; e != nil {
			t.Fatal("small pool deadlock or confirmation failure", e)
		}
	}
	got := f.balances()
	if got[0] != 2900000 || got[1] != 1100000 || got[2] != 8000000 {
		t.Fatal("small pool economic replay", got)
	}
}

func TestLockedCollectionPolicyRevocationDoesNotResubmit(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	q := f.quote
	q.ID = ids.New()
	q.BusinessRef = "another-accepted-obligation"
	if _, e := f.l.LockQuote(f.user, q, "quote-before-deactivation"); e != nil {
		t.Fatal(e)
	}
	if e := f.l.DeactivatePolicy(admin(), lifecycle.PolicyDeactivation{ID: ids.New(), PolicyID: f.policy.ID, EffectiveAt: time.Now().Add(-time.Second), EvidenceRef: "synthetic-policy-revocation"}); e != nil {
		t.Fatal(e)
	}
	in := finance.IntentInput{LockedQuoteID: &q.ID, WalletID: q.PayerWalletID, AmountMinor: q.AmountMinor, Currency: q.Currency, Country: q.Jurisdiction, Category: f.policy.Key, EvidenceRef: q.QuoteVersion, PolicyVersion: f.policy.Version, Email: "tenant@example.invalid"}
	before := f.fake.initializes.Load()
	if _, e := f.s.CreateIntent(f.user, in, "new-disabled-policy-charge"); e == nil {
		t.Fatal("deactivated policy submitted")
	}
	if f.fake.initializes.Load() != before {
		t.Fatal("disabled policy reached provider")
	}
	in.LockedQuoteID = &f.quote.ID
	replay, e := f.s.CreateIntent(f.user, in, "economic-intent")
	if e != nil || replay.ID != f.intent.ID || f.fake.initializes.Load() != before {
		t.Fatal("revocation recreated or lost historical intent", replay, e)
	}
}

func TestCollectionCreationSmallPoolAndRestrictedWallet(t *testing.T) {
	f := setup(t)
	cfg, e := pgxpool.ParseConfig(f.db.URL)
	if e != nil {
		t.Fatal(e)
	}
	cfg.MaxConns = 2
	pool, e := pgxpool.NewWithConfig(system(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	f.s.Service = core.New(pool)
	bounded, cancel := context.WithTimeout(f.user, 5*time.Second)
	defer cancel()
	in := finance.IntentInput{WalletID: f.w.WalletID, AmountMinor: 10000, Currency: "NGN", Country: "NG", Category: "RENT", EvidenceRef: "test-accepted-obligation", PolicyVersion: "v1", Email: "fixture@example.invalid"}
	start := make(chan struct{})
	errs := make(chan error, 12)
	for n := 0; n < 12; n++ {
		go func(n int) {
			<-start
			_, e := f.s.CreateIntent(bounded, in, fmt.Sprintf("small-pool-collection-%d", n))
			errs <- e
		}(n)
	}
	close(start)
	for range 12 {
		if e = <-errs; e != nil {
			t.Fatal("collection pool starvation", e)
		}
	}
	if f.fake.initializes.Load() != 12 {
		t.Fatal("lost or duplicate collection calls")
	}
	if e = f.s.ControlWallet(admin(), f.w.WalletID, core.IdentityControl{State: "RESTRICTED", EvidenceRef: "test-risk-review", PolicyVersion: "v1"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CreateIntent(f.user, in, "restricted-new-collection"); e == nil {
		t.Fatal("restricted wallet acquired new collection obligation")
	}
	if f.fake.initializes.Load() != 12 {
		t.Fatal("restricted collection reached provider")
	}
}
