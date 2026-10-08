package requestcontext

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func serve(t *testing.T, req *http.Request) (Values, *httptest.ResponseRecorder) {
	t.Helper()
	var got Values
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = From(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return got, rec
}

func TestGeneratesRequestAndTraceID(t *testing.T) {
	v, rec := serve(t, httptest.NewRequest(http.MethodGet, "/", nil))
	if _, err := ids.ParseV7(v.RequestID); err != nil {
		t.Fatalf("expected UUIDv7 request id, got %q", v.RequestID)
	}
	if len(v.TraceID) != 32 {
		t.Fatalf("expected generated trace id, got %q", v.TraceID)
	}
	if rec.Header().Get(HeaderRequestID) != v.RequestID {
		t.Fatal("request id not echoed")
	}
}

func TestHonoursValidInboundHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set(HeaderRequestID, "client-req-123")
	req.Header.Set(HeaderTraceParent, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set(HeaderIdempotencyKey, "key-abc")
	v, _ := serve(t, req)
	if v.RequestID != "client-req-123" {
		t.Fatalf("request id %q", v.RequestID)
	}
	if v.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id %q", v.TraceID)
	}
	if v.IdempotencyKey != "key-abc" {
		t.Fatalf("idempotency key %q", v.IdempotencyKey)
	}
}

func TestRejectsMalformedInboundValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "bad id with spaces\n")
	req.Header.Set(HeaderIdempotencyKey, strings.Repeat("k", 300))
	v, _ := serve(t, req)
	if v.RequestID == "bad id with spaces\n" {
		t.Fatal("malformed request id accepted")
	}
	if v.IdempotencyKey != "" {
		t.Fatal("oversized idempotency key accepted")
	}
}

func TestActorHeadersAreNotTrusted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Actor-Domain", "admin")
	req.Header.Set("X-Actor-Id", ids.New().String())
	req.Header.Set("X-Calling-Service", "payment-service")
	v, _ := serve(t, req)
	if !v.Actor.IsZero() || v.CallingService != "" {
		t.Fatalf("identity must never come from plain headers: %+v", v)
	}
}

func TestSettersAndGetters(t *testing.T) {
	ctx := With(context.Background(), Values{RequestID: "r1"})
	actor := Actor{Domain: "artisan", ID: ids.New()}
	org := ids.New()
	ctx = WithActor(ctx, actor)
	ctx = WithCallingService(ctx, "api-gateway")
	ctx = WithOrganization(ctx, org)
	ctx = WithIdempotencyKey(ctx, "k")
	v := From(ctx)
	if v.RequestID != "r1" || v.Actor != actor || v.CallingService != "api-gateway" || v.OrganizationID != org || v.IdempotencyKey != "k" {
		t.Fatalf("values %+v", v)
	}
	if a, ok := ActorFrom(ctx); !ok || a != actor {
		t.Fatal("ActorFrom")
	}
	if _, ok := ActorFrom(context.Background()); ok {
		t.Fatal("no actor expected")
	}
}

func TestInjectPropagatesDownstream(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	ctx = With(ctx, Values{RequestID: "req-9", Actor: Actor{Domain: "admin", ID: ids.New()}})
	h := http.Header{}
	Inject(ctx, h)
	if h.Get(HeaderRequestID) != "req-9" {
		t.Fatal("request id not propagated")
	}
	if !strings.Contains(h.Get(HeaderTraceParent), sc.TraceID().String()) {
		t.Fatalf("traceparent %q", h.Get(HeaderTraceParent))
	}
	if h.Get("X-Actor-Id") != "" || h.Get("X-Actor-Domain") != "" {
		t.Fatal("actor must not be propagated as plain header")
	}
	// Downstream extraction yields the same trace.
	down := propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(h))
	if trace.SpanContextFromContext(down).TraceID() != sc.TraceID() {
		t.Fatal("downstream trace mismatch")
	}
}

func TestInjectWithoutSpanUsesTraceID(t *testing.T) {
	ctx := With(context.Background(), Values{RequestID: "r", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736"})
	h := http.Header{}
	Inject(ctx, h)
	if !strings.HasPrefix(h.Get(HeaderTraceParent), "00-4bf92f3577b34da6a3ce929d0e0e4736-") {
		t.Fatalf("traceparent %q", h.Get(HeaderTraceParent))
	}
}

func TestDetachKeepsValuesDropsCancel(t *testing.T) {
	parent, cancel := context.WithCancel(With(context.Background(), Values{RequestID: "r2"}))
	cancel()
	d := Detach(parent)
	if d.Err() != nil || RequestID(d) != "r2" {
		t.Fatal("detach failed")
	}
}
