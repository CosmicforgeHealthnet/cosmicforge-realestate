package integration

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStartupWithoutProvidersAndRealJetStreamPublication(t *testing.T) {
	db := testkit.NewDatabase(t, migrations.FS)
	log := logging.Discard()
	ver, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.PaymentService, Registry: serviceauth.NewRegistry()})
	if e != nil {
		t.Fatal(e)
	}
	broker := testkit.StartNATS(t)
	cfg := &config.Config{Environment: "test", DatabaseURL: security.NewSecret(db.URL), Identity: &serviceauth.Identity{Verifier: ver}, SubjectPrefix: "paymenttest", NATSURL: broker.URL, EnsureStream: true}
	a, e := app.Build(context.Background(), cfg, log)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	ready := httptest.NewRecorder()
	a.Router.ServeHTTP(ready, httptest.NewRequest("GET", "/ready", nil))
	if ready.Code != 200 {
		t.Fatal(ready.Body.String())
	}
	id := ids.New()
	w, e := a.Service.EnsureWallet(ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, id), core.Provision{Domain: "individual-owner", AccountID: id, Role: "OWNER", PartyKind: "PERSON", LegalEntity: "cosmicforge-ng", Currency: "NGN", RegistrationRef: "runtime-registration"})
	if e != nil || w.WalletID.IsZero() {
		t.Fatal(w, e)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.RunWorker(runCtx) }()
	defer cancel()
	conn, e := jetstream.Connect(jetstream.Config{URL: broker.URL, Name: "runtime-observer", Logger: log})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	testkit.Eventually(t, 10*time.Second, func() bool {
		stream, e := conn.JetStream().Stream(context.Background(), jetstream.StreamName(cfg.SubjectPrefix, "finance"))
		if e != nil {
			return false
		}
		info, e := stream.Info(context.Background())
		return e == nil && info.State.Msgs == 1
	}, "committed wallets over real JetStream")
	var acked int
	if e = db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL AND subject LIKE 'paymenttest.%'`).Scan(&acked); e != nil || acked != 1 {
		t.Fatal(acked, e)
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not shut down")
	}
}
