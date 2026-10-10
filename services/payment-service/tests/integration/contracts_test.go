package integration

import (
	"context"
	"encoding/json"
	phttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"github.com/go-chi/chi/v5"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenAPIAndPostmanRouteParity(t *testing.T) {
	f := setup(t)
	r := phttp.NewRouter(phttp.Config{Service: f.s, Health: health.New("payment-service", "test", time.Second), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	var routes []testkit.Route
	if e := chi.Walk(r.(chi.Routes), func(m, p string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, testkit.Route{Method: m, Path: p})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	root := "../../../../contracts/"
	testkit.AssertRoutesMatchOpenAPI(t, root+"internal/openapi/payment-service.yaml", routes, "/internal/")
	testkit.AssertRoutesMatchOpenAPI(t, root+"public/openapi/payments.yaml", routes, "/api/")
	testkit.AssertRoutesMatchOpenAPI(t, root+"webhooks/paystack.yaml", routes, "/webhooks/")
	b, e := os.ReadFile("../../../../postman/payment-service.postman_collection.json")
	if e != nil {
		t.Fatal(e)
	}
	var col struct {
		Item []struct {
			Name    string `json:"name"`
			Request struct {
				Method string `json:"method"`
				URL    string `json:"url"`
			} `json:"request"`
		} `json:"item"`
	}
	if e = json.Unmarshal(b, &col); e != nil {
		t.Fatal(e)
	}
	implemented := map[string]bool{}
	for _, route := range routes {
		if strings.HasPrefix(route.Path, "/api/") || strings.HasPrefix(route.Path, "/internal/") || strings.HasPrefix(route.Path, "/webhooks/") {
			implemented[route.Method+" "+route.Path] = true
		}
	}
	documented := map[string]bool{}
	for _, it := range col.Item {
		u := it.Request.URL
		for _, base := range []string{"{{payment_base_url}}", "{{gateway_base_url}}"} {
			u = strings.TrimPrefix(u, base)
		}
		u = strings.ReplaceAll(u, "{{entity_id}}", "{id}")
		u = strings.ReplaceAll(u, "{{pool_id}}", "{pool}")
		key := it.Request.Method + " " + u
		if !implemented[key] {
			t.Errorf("Postman stale %s", key)
		}
		if documented[key] {
			t.Errorf("duplicate %s", key)
		}
		documented[key] = true
	}
	for key := range implemented {
		if !documented[key] {
			t.Errorf("Postman missing %s", key)
		}
	}
}
func TestAuthoritativeResponsesAndOutboxEventSchemas(t *testing.T) {
	f := setup(t)
	i, _ := f.intent("schema")
	f.fake.success(i)
	if _, e := f.s.VerifyIntent(f.user, i.ID); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("../../../../contracts/internal/openapi/payment-service.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	bs, e := f.s.Balances(f.user, f.w.WalletID)
	if e != nil {
		t.Fatal(e)
	}
	hist, e := f.s.History(f.user, f.w.WalletID, ids.Nil, 100)
	if e != nil {
		t.Fatal(e)
	}
	instances := map[string]any{"Wallet": f.w, "Intent": i, "Balance": bs[0], "History": hist[0]}
	for name, value := range instances {
		t.Run(name, func(t *testing.T) {
			schema := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$ref": "#/components/schemas/" + name, "components": doc["components"]}
			raw, _ := json.Marshal(schema)
			path := filepath.Join(t.TempDir(), "schema.json")
			if e = os.WriteFile(path, raw, 0600); e != nil {
				t.Fatal(e)
			}
			body, _ := json.Marshal(value)
			testkit.ValidateJSONSchema(t, path, body)
		})
	}
	claimed, e := f.s.Outbox.Claim(context.Background(), "schema-worker", 100, time.Minute)
	if e != nil || len(claimed) != 6 {
		t.Fatal(len(claimed), e)
	}
	for _, row := range claimed {
		body, e := json.Marshal(row.Envelope)
		if e != nil {
			t.Fatal(e)
		}
		testkit.ValidateJSONSchema(t, "../../../../contracts/events/payments/"+row.Envelope.EventType+".schema.json", body)
		if row.Envelope.Producer != string(platform.PaymentService) {
			t.Fatal("source")
		}
	}
}

type recordingPublisher struct {
	fail      bool
	published []events.Envelope
}

func (p *recordingPublisher) Publish(_ context.Context, _ string, e events.Envelope) (events.PublishAck, error) {
	if p.fail {
		return events.PublishAck{}, io.ErrUnexpectedEOF
	}
	p.published = append(p.published, e)
	return events.PublishAck{Sequence: uint64(len(p.published))}, nil
}
func TestPaymentOutboxDurableRetryAndCrashLease(t *testing.T) {
	f := setup(t)
	pub := &recordingPublisher{fail: true}
	worker, e := outbox.NewWorker(f.s.Outbox, pub, outbox.WorkerConfig{Owner: "relay", BatchSize: 20, Lease: time.Second, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxAttempts: 3, Logger: f.s.Log})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = worker.RunOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	var published int
	if e = f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL`).Scan(&published); e != nil || published != 0 {
		t.Fatal(published, e)
	}
	pub.fail = false
	if _, e = f.db.Pool.Exec(context.Background(), `UPDATE outbox_events SET lease_owner='dead-worker',lease_expires_at=now()-interval '1 second',next_attempt_at=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	if _, e = worker.RunOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(pub.published) != 1 {
		t.Fatal(len(pub.published))
	}
	if _, e = worker.RunOnce(context.Background()); e != nil || len(pub.published) != 1 {
		t.Fatal("duplicate after ack", e)
	}
}
