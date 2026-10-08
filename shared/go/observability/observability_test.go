package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

func TestSetupProvidesTraceIDsAndPropagation(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{ServiceName: "test-svc", Environment: "test", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	var inboundTrace string
	var downstream http.Header
	h := HTTPMiddleware("test")(requestcontext.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inboundTrace = requestcontext.TraceID(r.Context())
		downstream = http.Header{}
		requestcontext.Inject(r.Context(), downstream)
	})))

	// No inbound context: a new trace is created.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/x", nil))
	if len(inboundTrace) != 32 || inboundTrace == "00000000000000000000000000000000" {
		t.Fatalf("trace id %q", inboundTrace)
	}

	// Inbound traceparent is continued and propagated downstream.
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if inboundTrace != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace not continued: %q", inboundTrace)
	}
	if got := downstream.Get("traceparent"); len(got) < 36 || got[3:35] != inboundTrace {
		t.Fatalf("downstream traceparent %q", got)
	}
}

func TestSpansAndMetricsHelpers(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{ServiceName: "t", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	ctx, span := StartSpan(context.Background(), "t", "op", attribute.String("k", "v"))
	if !trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("span context invalid")
	}
	span.End()
	NewCounter("t", "test_total", "d").Add(ctx, 1, attribute.String("result", "ok"))
	NewHistogram("t", "test_seconds", "d", "s").Record(ctx, 0.2)
}

func TestInvalidSampleRatio(t *testing.T) {
	if _, err := Setup(context.Background(), Config{ServiceName: "t", SampleRatio: 2}); err == nil {
		t.Fatal("expected error")
	}
}
