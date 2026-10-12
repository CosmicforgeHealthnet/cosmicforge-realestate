//go:build financial_cross_service

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	js "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/nats-io/nats.go/jetstream"
)

func TestFinancialCoreConsumerContract(t *testing.T) {
	base := os.Getenv("CF_PAYMENT_E2E_URL")
	if base == "" {
		t.Fatal("Run through Payment TestActualOwnerFinancialCoreConsumer; requires actual Financial Core")
	}
	private, e := base64.StdEncoding.DecodeString(os.Getenv("CF_PAYMENT_E2E_KEY"))
	if e != nil || len(private) != ed25519.PrivateKeySize {
		t.Fatal("ephemeral fixture signing key required")
	}
	signer, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.IndividualOwnerService, Key: jwt.SigningKey{KID: "owner-cross-service-ephemeral", PrivateKey: ed25519.PrivateKey(private)}})
	if e != nil {
		t.Fatal(e)
	}
	client, e := payment.NewWalletProvisioner(base, signer, "cosmicforge-ng", "NGN")
	if e != nil {
		t.Fatal(e)
	}
	h := newHarness(t, withPayment)
	h.app.Service.SetWalletRegistration(client)
	pending := h.register(false)
	h.expect(h.get(pending, "/financial-wallet"), 422, "WALLET_ACTIVATION_PENDING")
	o := h.register(true)
	h.verifyIdentity(o)
	p := h.createProperty(o, "Cross Service Contract Street")
	l := h.liveOf(o, p, "RENT_LONG_TERM", map[string]any{"min_term_months": 12, "furnishing": "UNFURNISHED"}, map[string]any{"currency": "NGN", "base_amount_minor": 360000000, "base_period": "YEAR", "payment_cycle": "ANNUAL", "deposit_amount_minor": 50000000, "fees": []map[string]any{{"code": "service_charge", "category": "SERVICE_CHARGE", "label": "Accepted recurring fee", "calculation": "FIXED", "amount_minor": 20000000, "frequency": "PER_CYCLE", "mandatory": true, "payable_to": "OWNER"}}})
	var snap payment.WalletSnapshot
	h.get(o, "/financial-wallet").data(t, &snap)
	h.get(o, "/financial-wallet").data(t, &snap)
	other := h.register(true)
	var separate payment.WalletSnapshot
	h.get(other, "/financial-wallet").data(t, &separate)
	if snap.Wallet.WalletID == separate.Wallet.WalletID {
		t.Fatal("wallet isolation violated")
	}
	for _, b := range snap.Balances {
		if b.AmountMinor != "0" {
			t.Fatal("opening balance invented", b)
		}
	}
	c := customer()
	h.acceptedOffer(o, l, c, day(h.clock.now(), 1))
	agreement := h.agreementOf(o, l.PropertyID, "RENTAL_OFFER")
	agreement.CurrentVersion.Terms.RentCollection = "PLATFORM"
	agreement = h.prepareDraft(o, agreement, h.agreementTemplate("RESIDENTIAL_LEASE"), nil)
	agreement = h.signOffline(o, c, agreement)
	h.post(o, "/agreements/"+agreement.ID.String()+"/activate", nil).data(t, &agreement)
	var obligations []usecases.RentObligationView
	h.get(o, "/tenancies/"+agreement.TenancyID.String()+"/rent-obligations").data(t, &obligations)
	if len(obligations) == 0 {
		t.Fatal("no existing rent schedule")
	}
	obligation := obligations[0]
	fixture := func(path string, in any, out any) {
		t.Helper()
		b, _ := json.Marshal(in)
		r, e := http.Post(base+"/test-fixtures/"+path, "application/json", bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("fixture status %d", r.StatusCode)
		}
		if out != nil {
			if e = json.NewDecoder(r.Body).Decode(out); e != nil {
				t.Fatal(e)
			}
		}
	}
	var q payment.AcceptedQuote
	fixture("quote", map[string]any{"obligation_id": obligation.ID, "owner_id": o.id, "amount_minor": obligation.AmountMinor, "rent_minor": h.tenancy(o, *agreement.TenancyID).RentMinor, "customer_id": c.ActorID, "agreement_ref": agreement.ID.String(), "agreement_version": strconv.Itoa(int(agreement.CurrentVersion.VersionNo))}, &q)
	path := "/rent-obligations/" + obligation.ID.String() + "/financial-collection"
	body := map[string]any{"quote_id": q.ID, "payer_email": "tenant@example.invalid"}
	h.expect(h.post(other, path, body), 404, "")
	var intent payment.CollectionIntent
	h.post(o, path, body).data(t, &intent)
	var retry payment.CollectionIntent
	h.post(o, path, body).data(t, &retry)
	if intent.ID != retry.ID || intent.AmountMinor != q.AmountMinor || intent.State != "INITIALIZED" {
		t.Fatal("collection context/retry", intent.State)
	}
	fixture("verify", map[string]any{"intent_id": intent.ID}, nil)
	conn, e := js.Connect(js.Config{URL: os.Getenv("CF_PAYMENT_E2E_NATS"), Name: "owner-cross-consumer", Logger: logging.Discard()})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	stream, e := conn.JetStream().Stream(context.Background(), "CROSS_FINANCE")
	if e != nil {
		t.Fatal(e)
	}
	consumer, e := stream.CreateOrUpdateConsumer(context.Background(), jetstream.ConsumerConfig{AckPolicy: jetstream.AckExplicitPolicy, FilterSubject: "cross.finance.payment.collected.v1"})
	if e != nil {
		t.Fatal(e)
	}
	batch, e := consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	var event events.Envelope
	for m := range batch.Messages() {
		event, e = events.Unmarshal(m.Data())
		if e != nil {
			t.Fatal(e)
		}
		for range 2 {
			if e = h.app.Service.ApplyPaymentEvent(context.Background(), event); e != nil {
				t.Fatal(e)
			}
		}
		if e = m.Ack(); e != nil {
			t.Fatal(e)
		}
	}
	if event.EventID == ids.Nil || h.count(`SELECT count(*) FROM processed_events WHERE consumer=$1 AND event_id=$2`, usecases.ConsumerPayment, event.EventID) != 1 {
		t.Fatal("durable exactly-once consumer")
	}
	forged := event
	forged.EventID = ids.New()
	forged.Producer = string(platform.MarketplaceService)
	if e = h.app.Service.ApplyPaymentEvent(context.Background(), forged); !events.IsPermanent(e) {
		t.Fatal("forged producer accepted", e)
	}
	var observed payment.CollectionIntent
	h.get(o, path).data(t, &observed)
	if observed.State != "VERIFIED_SUCCESS" || observed.SettlementState != "PENDING" {
		t.Fatal("collection projection", observed)
	}
	h.expect(h.get(other, path), 404, "")
	if h.count(`SELECT count(*) FROM rent_obligations WHERE id=$1 AND paid_minor=0 AND refunded_minor=0 AND status<>'PAID'`, obligation.ID) != 1 {
		t.Fatal("collection inferred paid rent")
	}
	h.get(o, "/financial-wallet").data(t, &snap)
	for _, b := range snap.Balances {
		if b.AmountMinor != "0" {
			t.Fatal("collection invented beneficiary balance", b)
		}
	}
	fixture("allocate", map[string]any{"intent_id": intent.ID, "owner_id": o.id}, nil)
	h.get(o, "/financial-wallet").data(t, &snap)
	pendingEarnings := false
	for _, b := range snap.Balances {
		if b.Kind == "PROTECTED" && b.AmountMinor == strconv.FormatInt(obligation.AmountMinor, 10) {
			pendingEarnings = true
		}
		if b.Kind == "AVAILABLE" && b.AmountMinor != "0" {
			t.Fatal("protected allocation became available", b)
		}
	}
	if !pendingEarnings {
		t.Fatal("actual protected beneficiary earnings not read", snap.Balances)
	}
	h.get(other, "/financial-wallet").data(t, &separate)
	for _, b := range separate.Balances {
		if b.AmountMinor != "0" {
			t.Fatal("beneficiary earnings leaked to another owner", b)
		}
	}
	fixture("freeze", map[string]any{"owner_id": o.id}, nil)
	h.get(o, "/financial-wallet").data(t, &snap)
	if snap.Wallet.State != "RESTRICTED" {
		t.Fatal("restricted financial wallet disappeared", snap.Wallet)
	}
	if h.count(`SELECT count(*) FROM financial_core_collections WHERE obligation_id=$1 AND observed_state='VERIFIED_SUCCESS' AND settlement_state='PENDING'`, obligation.ID) != 1 {
		t.Fatal("canonical event observation lost")
	}
	fixture("fund", map[string]any{"intent_id": intent.ID}, nil)
	certifyAdditionalCoreJourneys(t, client, base)

}
