package observability

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizedHTTPSpansExcludePrivateURLsAndPropagate(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	previousProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() {
		otel.SetTracerProvider(previous)
		otel.SetTextMapPropagator(previousProp)
		_ = tp.Shutdown(context.Background())
	}()
	var traceparent string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
		w.WriteHeader(204)
	}))
	defer downstream.Close()
	router := chi.NewRouter()
	router.Use(HTTPMiddleware("owner"))
	router.Get("/properties/{property_id}", func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), "GET", downstream.URL+"/TEST-private-document?signature=TEST-secret", nil)
		req.Header.Set("Authorization", "Bearer TEST-token")
		resp, err := (&http.Client{Transport: HTTPTransport(nil), Timeout: time.Second}).Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		_ = resp.Body.Close()
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("GET", "/properties/TEST-personal-id?code=TEST-otp", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	router.ServeHTTP(httptest.NewRecorder(), req)
	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("spans: %d", len(spans))
	}
	for _, span := range spans {
		if span.SpanContext.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
			t.Fatal("trace not continued")
		}
		for _, leak := range []string{"TEST-personal-id", "TEST-private-document", "TEST-secret", "TEST-token", "TEST-otp"} {
			if strings.Contains(span.Name+fmt.Sprint(span.Attributes), leak) {
				t.Fatalf("trace leak: %s", leak)
			}
		}
	}
	if !strings.Contains(traceparent, "4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Fatal("outbound propagation missing")
	}
	if spans[1].Name != "GET /properties/{property_id}" {
		t.Fatal(spans[1].Name)
	}
}

func TestUnavailableExporterDoesNotBlockHTTP(t *testing.T) {
	previous := otel.GetTracerProvider()
	previousMeter := otel.GetMeterProvider()
	previousProp := otel.GetTextMapPropagator()
	defer func() {
		otel.SetTracerProvider(previous)
		otel.SetMeterProvider(previousMeter)
		otel.SetTextMapPropagator(previousProp)
	}()
	shutdown, err := Setup(context.Background(), Config{ServiceName: "test-owner", SampleRatio: 1, OTLPEndpoint: "127.0.0.1:1", OTLPInsecure: true, MetricInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	HTTPMiddleware("test-owner")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(rec, httptest.NewRequest("GET", "/example", nil))
	if rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = shutdown(ctx)
}
