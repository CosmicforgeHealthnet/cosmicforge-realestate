package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/telemetry"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	js "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSensitiveActionsRequireCurrentSessionMFA(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.verifyIdentity(o)
	path := "/payout-accounts"
	in := map[string]any{"bank_code": "058", "bank_name": "TEST Bank", "account_name": "TEST Owner", "account_number": "0123456789", "currency": "NGN"}
	h.expect(h.post(o, path, in), 403, "MFA_REQUIRED")
	h.secure(o)
	// Passing the MFA boundary does not fabricate an absent beneficiary provider.
	h.expect(h.post(o, path, in), 503, "FEATURE_UNAVAILABLE")
	original := o.access
	r := h.do(call{method: "POST", path: api + "/auth/step-up/challenge", token: o.access, body: map[string]any{"method": "EMAIL"}})
	h.expect(r, 202, "")
	var challenge usecases.ChallengeView
	r.data(t, &challenge)
	code := h.notify.lastCode(t, o.email)
	_, second := h.login(o, "TEST second device")
	o.apply(second)
	h.expect(h.do(call{method: "POST", path: api + "/auth/step-up", token: o.access, body: map[string]any{"method": "EMAIL", "challenge_id": challenge.ChallengeID, "code": code}}), 422, "INVALID_VERIFICATION_CODE")
	h.clock.advance(6 * time.Minute)
	h.expect(h.post(o, path, in), 403, "STEP_UP_REQUIRED")
	h.expect(h.do(call{method: "POST", path: api + "/auth/step-up", token: o.access, body: map[string]any{"method": "PASSWORD", "password": o.password}}), 422, "STEP_UP_METHOD_UNAVAILABLE")
	h.secure(o)
	h.expect(h.do(call{method: "POST", path: api + "/auth/logout-all", token: o.access}), 200, "")
	h.expect(h.do(call{method: "GET", path: api + "/financial-wallet", token: original}), 401, "SESSION_REVOKED")
	h.expect(h.post(o, path, in), 401, "SESSION_REVOKED")
}

func TestOperationalSnapshotAndEventReviewRetention(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.createProperty(o, "TEST operational reference")
	ctx := context.Background()
	f := js.Failure{Consumer: usecases.ConsumerPayment, Stream: "REALESTATE_PAYMENT", Sequence: 9, Attempt: 20, Digest: security.SHA256Hex([]byte("TEST sensitive payload")), Class: "EXHAUSTED"}
	for i := 0; i < 2; i++ {
		if err := h.app.Service.RetainEventFailure(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if h.count(`SELECT count(*) FROM owner_event_review`) != 1 {
		t.Fatal("duplicate incident")
	}
	snapshot, err := telemetry.Snapshot(ctx, h.app.Pool)
	if err != nil {
		t.Fatal(err)
	}
	families := map[string]bool{}
	found := false
	for _, v := range snapshot {
		families[v.Family] = true
		if v.Kind == "review" && v.State == "EXHAUSTED" && v.Value == 1 {
			found = true
		}
	}
	for _, family := range []string{"events", "workers", "business", "security", "db"} {
		if !families[family] {
			t.Fatal("missing family", family)
		}
	}
	if !found {
		t.Fatal("terminal failure metric missing")
	}
	issues := h.do(call{method: "GET", path: "/internal/v1/owner/admin/reconciliation", as: platform.AdminService, delegate: adminActor()})
	h.expect(issues, 200, "")
	if !strings.Contains(string(issues.body), "EVENT_REVIEW_REQUIRED") {
		t.Fatal("review absent")
	}
	var retained string
	if err := h.db.Pool.QueryRow(ctx, `SELECT row_to_json(r)::text FROM owner_event_review r`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(retained, "TEST sensitive payload") {
		t.Fatal("raw payload copied")
	}
	var reviewID ids.ID
	if err := h.db.Pool.QueryRow(ctx, `SELECT id FROM owner_event_review`).Scan(&reviewID); err != nil {
		t.Fatal(err)
	}
	path := "/internal/v1/owner/admin/reconciliation/events/" + reviewID.String() + "/decision"
	body := map[string]any{"decision": "NO_ACTION", "reason_code": "TEST_REVIEWED", "evidence_ref": ids.New()}
	h.expect(h.do(call{method: "POST", path: path, as: platform.MarketplaceService, key: key(), body: body}), 403, "")
	h.expect(h.do(call{method: "POST", path: path, as: platform.AdminService, key: key(), body: body}), 403, "DELEGATED_ACTOR_REQUIRED")
	h.expect(h.do(call{method: "POST", path: path, as: platform.AdminService, delegate: adminActor(), key: key(), body: body}), 200, "")
	if h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='owner.event_review.decided' AND actor_domain='admin' AND actor_id IS NOT NULL`, reviewID) != 1 {
		t.Fatal("review is unattributed")
	}
	if _, err := h.db.Pool.Exec(ctx, `DELETE FROM owner_event_review_decisions`); err == nil {
		t.Fatal("review decision mutable")
	}
	for _, sql := range []string{"UPDATE owner_event_review SET failure_class='PERMANENT'", "DELETE FROM owner_event_review", "TRUNCATE owner_event_review"} {
		if _, err := h.db.Pool.Exec(ctx, sql); err == nil {
			t.Fatal("review history mutable")
		}
	}
	raw, err := fs.ReadFile(migrations.FS, "00023_event_review.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := strings.SplitN(string(raw), "-- +goose Down", 2)[1]
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, down); err == nil {
		t.Fatal("populated review rollback allowed")
	}
}

func TestPasswordHashUpgradePreservesSuccessfulAuthentication(t *testing.T) {
	h := newHarness(t)
	h.app.Service.Passwords, _ = passwords.NewHasher(passwords.DefaultParams, 1)
	o := h.register(true)
	params := passwords.DefaultParams
	params.MemoryKiB = 8192
	params.Iterations = 1
	params.Parallelism = 1
	legacy, err := passwords.NewHasher(params, 1)
	if err != nil {
		t.Fatal(err)
	}
	oldHash, err := legacy.Hash(context.Background(), o.password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Pool.Exec(context.Background(), `UPDATE owner_accounts SET password_hash=$2 WHERE account_id=$1`, o.id, oldHash); err != nil {
		t.Fatal(err)
	}
	r, a := h.login(o, o.device)
	h.expect(r, 200, "")
	if a.Tokens == nil {
		t.Fatal("rehash login failed")
	}
	var hash string
	if err := h.db.Pool.QueryRow(context.Background(), `SELECT password_hash FROM owner_accounts WHERE account_id=$1`, o.id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == oldHash {
		t.Fatal("legacy hash retained")
	}
	if upgrade, err := h.app.Service.Passwords.Verify(context.Background(), o.password, hash); err != nil || upgrade {
		t.Fatal("rehash did not reach current parameters", err)
	}
}

func TestConcurrentPasswordResetCannotBeOverwrittenByRehash(t *testing.T) {
	h := newHarness(t)
	h.app.Service.Passwords, _ = passwords.NewHasher(passwords.DefaultParams, 1)
	o := h.register(true)
	ctx := context.Background()
	params := passwords.DefaultParams
	params.MemoryKiB = 8192
	params.Iterations = 1
	params.Parallelism = 1
	legacy, _ := passwords.NewHasher(params, 1)
	oldHash, err := legacy.Hash(ctx, o.password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Pool.Exec(ctx, `UPDATE owner_accounts SET password_hash=$2 WHERE account_id=$1`, o.id, oldHash); err != nil {
		t.Fatal(err)
	}
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT account_id FROM owner_accounts WHERE account_id=$1 FOR UPDATE`, o.id); err != nil {
		t.Fatal(err)
	}
	done := make(chan result, 1)
	go func() { r, _ := h.login(o, o.device); done <- r }()
	testkit.Eventually(t, 5*time.Second, func() bool {
		return h.count(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`) > 0
	}, "login reaches reset serialization boundary")
	newHash, err := h.app.Service.Passwords.Hash(ctx, "TEST changed password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE owner_accounts SET password_hash=$2 WHERE account_id=$1`, o.id, newHash); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		h.expect(r, 401, "INVALID_CREDENTIALS")
	case <-time.After(5 * time.Second):
		t.Fatal("stale login did not finish")
	}
	if h.count(`SELECT count(*) FROM owner_accounts WHERE account_id=$1 AND password_hash=$2`, o.id, newHash) != 1 {
		t.Fatal("reset overwritten by login upgrade")
	}
}

func TestWalletRegistrationRetriesEndInReview(t *testing.T) {
	h := newHarness(t)
	o := h.register(false)
	client := &walletDeliveryFixture{fail: true}
	h.app.Service.SetWalletRegistration(client)
	ctx := context.Background()
	for i := 0; i < usecases.WalletRegistrationMaxAttempts+2; i++ {
		if _, err := h.db.Pool.Exec(ctx, `UPDATE wallet_registration_delivery SET next_attempt_at=now()-interval '1 second' WHERE account_id=$1`, o.id); err != nil {
			t.Fatal(err)
		}
		if err := h.app.Service.DeliverWalletRegistrations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if client.calls != usecases.WalletRegistrationMaxAttempts {
		t.Fatal("unbounded registration retry", client.calls)
	}
	if h.count(`SELECT count(*) FROM wallet_registration_delivery WHERE account_id=$1 AND last_error_code='RETRIES_EXHAUSTED' AND registration_ref<>''`, o.id) != 1 {
		t.Fatal("original exhausted registration lost")
	}
}

func TestTerminalJetStreamFailuresPersistBeforeTermination(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	broker := testkit.StartNATS(t)
	conn, err := js.Connect(js.Config{URL: broker.URL, Name: "TEST Owner event review", Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.EnsureStream(ctx, js.StreamConfig{Name: "REALESTATE_TEST_REVIEW", Subjects: []string{"realestate.testreview.>"}}); err != nil {
		t.Fatal(err)
	}
	consumer, err := conn.Consume(ctx, js.ConsumerConfig{Stream: "REALESTATE_TEST_REVIEW", Durable: "TEST-owner-review", MaxDeliver: 2, OnFailure: h.app.Service.RetainEventFailure}, func(ctx context.Context, d events.Delivery) error {
		if d.Envelope.EventType == "testreview.transient.v1" {
			return errors.New("TEST dependency down")
		}
		return events.Permanent(context.Canceled)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Stop()
	event, err := events.New(ctx, platform.PaymentService, "testreview.invalid.v1", "test", ids.New(), map[string]string{"private": "TEST not copied"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Publish(ctx, "realestate.testreview.invalid.v1", event); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.JetStream().Publish(ctx, "realestate.testreview.malformed.v1", []byte("TEST not JSON private secret")); err != nil {
		t.Fatal(err)
	}
	event.EventID = ids.New()
	event.EventType = "testreview.transient.v1"
	if _, err := conn.Publish(ctx, "realestate.testreview.transient.v1", event); err != nil {
		t.Fatal(err)
	}
	testkit.Eventually(t, 5*time.Second, func() bool {
		return h.count(`SELECT count(*) FROM owner_event_review WHERE consumer='TEST-owner-review'`) == 3
	}, "durable poison evidence")
}

func TestOperationalMetricsUnknownIsNotZero(t *testing.T) {
	previous := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); _ = provider.Shutdown(context.Background()) })
	h := newHarness(t)
	h.register(true)
	collect := func() metricdata.ResourceMetrics {
		var data metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	value := func(data metricdata.ResourceMetrics, name string) int64 {
		for _, scope := range data.ScopeMetrics {
			for _, m := range scope.Metrics {
				if m.Name == name {
					if g, ok := m.Data.(metricdata.Gauge[int64]); ok && len(g.DataPoints) > 0 {
						return g.DataPoints[0].Value
					}
				}
			}
		}
		return -1
	}
	healthy := collect()
	if value(healthy, "owner_operational_observation_available") != 1 {
		t.Fatal("valid snapshot unavailable")
	}
	h.app.Pool.Close()
	unknown := collect()
	if value(unknown, "owner_operational_observation_available") != 0 {
		t.Fatal("DB failure mislabeled as a healthy zero snapshot")
	}
	for _, scope := range unknown.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == "owner_operational_observation" {
				if g, ok := m.Data.(metricdata.Gauge[float64]); ok && len(g.DataPoints) > 0 {
					t.Fatal("unavailable snapshot emitted false values")
				}
			}
		}
	}
}

func TestOwnerAPIHealthOptionalOutageAndShutdown(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/health", "/ready", "/version"} {
		h.expect(h.do(call{method: "GET", path: path}), 200, "")
	}
	// No e-sign/calendar/smart provider is required by the API readiness contract.
	h.app.Health.SetShuttingDown()
	h.expect(h.do(call{method: "GET", path: "/ready"}), 503, "")
	h.expect(h.do(call{method: "GET", path: "/health"}), 200, "")
	raw, _ := json.Marshal(h.app.Cfg.DatabaseURL)
	if strings.Contains(string(raw), "postgres") {
		t.Fatal("DSN formatting leaked")
	}
	rec := httptest.NewRecorder()
	h.app.Health.Version(rec, httptest.NewRequest("GET", "/version", nil))
	if strings.Contains(rec.Body.String(), "postgres://") {
		t.Fatal("version leaks DSN")
	}
}

func TestWorkerRequiredDependenciesAndRecovery(t *testing.T) {
	h := newHarness(t)
	broker := testkit.StartNATS(t)
	h.app.Cfg.NATSURL = broker.URL
	h.app.Cfg.EnsureStream = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, conn, err := h.app.Worker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ready := func() int {
		r := httptest.NewRecorder()
		h.app.Health.Ready(r, httptest.NewRequest("GET", "/ready", nil))
		return r.Code
	}
	if ready() != 200 {
		t.Fatal("optional absent producers made worker unready")
	}
	broker.Shutdown()
	if ready() != 503 {
		t.Fatal("worker ready without broker")
	}
	broker.Restart()
	testkit.Eventually(t, 5*time.Second, func() bool { return ready() == 200 }, "broker recovery")
	// Exhaust actual assigned DB connections; health must time out safely and recover.
	held := []*pgxpool.Conn{}
	for i := int32(0); i < h.app.Pool.Config().MaxConns; i++ {
		c, err := h.app.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	if ready() != 503 {
		t.Fatal("worker ready without usable database")
	}
	for _, c := range held {
		c.Release()
	}
	if ready() != 200 {
		t.Fatal("database availability did not recover")
	}
	cancel()
	if ready() != 503 {
		t.Fatal("worker ready after shutdown")
	}
}
