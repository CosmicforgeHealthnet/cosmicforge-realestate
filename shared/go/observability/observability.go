// Package observability configures OpenTelemetry tracing/metrics and exposes
// small, vendor-neutral helpers so business code never imports an exporter or
// vendor SDK directly.
//
// A tracer provider is always installed so every request receives a real
// trace ID; spans are exported only when an OTLP endpoint is configured
// (OTEL_EXPORTER_OTLP_ENDPOINT). Metrics are exported over OTLP to the
// collector, which feeds Prometheus/Grafana.
package observability

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"strconv"
)

// Config configures telemetry.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	// OTLPEndpoint is host:port of an OTLP/HTTP collector; empty disables export.
	OTLPEndpoint string
	OTLPInsecure bool
	// SampleRatio is the root-span sampling ratio in [0,1]. Parent decisions are honoured.
	SampleRatio    float64
	MetricInterval time.Duration
}

// Setup installs global tracer/meter providers and the W3C propagator.
// The returned shutdown flushes exporters and must be called on exit.
func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	res := resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		semconv.DeploymentEnvironment(cfg.Environment),
	)
	ratio := cfg.SampleRatio
	if ratio < 0 || ratio > 1 {
		return nil, errors.New("observability: sample ratio must be within [0,1]")
	}
	topts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	}
	mopts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if cfg.OTLPEndpoint != "" {
		texpOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.OTLPEndpoint)}
		mexpOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(cfg.OTLPEndpoint)}
		if cfg.OTLPInsecure {
			texpOpts = append(texpOpts, otlptracehttp.WithInsecure())
			mexpOpts = append(mexpOpts, otlpmetrichttp.WithInsecure())
		}
		texp, err := otlptracehttp.New(ctx, texpOpts...)
		if err != nil {
			return nil, err
		}
		mexp, err := otlpmetrichttp.New(ctx, mexpOpts...)
		if err != nil {
			return nil, err
		}
		interval := cfg.MetricInterval
		if interval <= 0 {
			interval = 30 * time.Second
		}
		topts = append(topts, sdktrace.WithBatcher(texp))
		mopts = append(mopts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp, sdkmetric.WithInterval(interval))))
	}
	tp := sdktrace.NewTracerProvider(topts...)
	mp := sdkmetric.NewMeterProvider(mopts...)
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}

// Route returns a bounded router template; unmatched URLs never become labels.
func Route(r *http.Request) string {
	if c := chi.RouteContext(r.Context()); c != nil {
		if route := c.RoutePattern(); route != "" {
			return route
		}
	}
	return "unmatched"
}

type responseStatus struct {
	http.ResponseWriter
	code int
}

func (w *responseStatus) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *responseStatus) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	return w.ResponseWriter.Write(b)
}
func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// HTTPMiddleware measures normalized routes without headers, raw URLs or bodies.
func HTTPMiddleware(operation string) func(http.Handler) http.Handler {
	requests := NewCounter(operation, "http_requests_total", "Completed HTTP requests")
	duration := NewHistogram(operation, "http_request_duration_seconds", "HTTP request duration", "s")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" || r.URL.Path == "/ready" || r.URL.Path == "/version" {
				next.ServeHTTP(w, r)
				return
			}
			parent := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := Tracer(operation).Start(parent, "HTTP", trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()
			start := time.Now()
			rec := &responseStatus{ResponseWriter: w}
			r = r.WithContext(ctx)
			next.ServeHTTP(rec, r)
			route := Route(r)
			status := rec.code
			if status == 0 {
				status = 200
			}
			method := r.Method
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
			default:
				method = "OTHER"
			}
			attrs := []attribute.KeyValue{attribute.String("http.route", route), attribute.String("http.request.method", method), attribute.Int("http.response.status_code", status)}
			span.SetName(method + " " + route)
			span.SetAttributes(attrs...)
			if status >= 500 {
				span.SetStatus(codes.Error, "HTTP server failure")
			}
			requests.Add(ctx, 1, attrs...)
			duration.Record(ctx, time.Since(start).Seconds(), attrs...)
		})
	}
}

type httpTransport struct{ base http.RoundTripper }

func (t httpTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, span := Tracer("service-http").Start(r.Context(), "HTTP outbound", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	req := r.Clone(ctx)
	req.Header = r.Header.Clone()
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		span.SetStatus(codes.Error, "HTTP transport failure")
		kind := "transport"
		var ne net.Error
		if errors.Is(err, context.Canceled) {
			kind = "cancelled"
		} else if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			kind = "timeout"
		}
		NewCounter("service-http", "external_http_failures_total", "Failed bounded service HTTP calls").Add(ctx, 1, attribute.String("kind", kind))
	} else {
		span.SetAttributes(attribute.String("http.response.status_code", strconv.Itoa(resp.StatusCode)))
		if resp.StatusCode >= 500 {
			span.SetStatus(codes.Error, "HTTP dependency failure")
		}
	}
	return resp, err
}

// HTTPTransport preserves caller timeouts and propagates traces without exporting URLs or credentials.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return httpTransport{base}
}

// Tracer returns a named tracer from the global provider.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// StartSpan starts an internal span; callers must End() it.
func StartSpan(ctx context.Context, tracer, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(tracer).Start(ctx, name, trace.WithAttributes(attrs...))
}

// Counter is a monotonic counter.
type Counter interface {
	Add(ctx context.Context, n int64, attrs ...attribute.KeyValue)
}

// Histogram records distributions (durations in seconds, sizes in bytes).
type Histogram interface {
	Record(ctx context.Context, v float64, attrs ...attribute.KeyValue)
}

type counter struct{ c metric.Int64Counter }

func (c counter) Add(ctx context.Context, n int64, attrs ...attribute.KeyValue) {
	c.c.Add(ctx, n, metric.WithAttributes(attrs...))
}

type histogram struct{ h metric.Float64Histogram }

func (h histogram) Record(ctx context.Context, v float64, attrs ...attribute.KeyValue) {
	h.h.Record(ctx, v, metric.WithAttributes(attrs...))
}

// NewCounter returns a counter from the global meter provider. Instrument
// creation errors fall back to a no-op so telemetry never breaks business code.
func NewCounter(meter, name, description string) Counter {
	c, err := otel.Meter(meter).Int64Counter(name, metric.WithDescription(description))
	if err != nil {
		return noopCounter{}
	}
	return counter{c}
}

// NewHistogram returns a histogram from the global meter provider.
func NewHistogram(meter, name, description, unit string) Histogram {
	h, err := otel.Meter(meter).Float64Histogram(name, metric.WithDescription(description), metric.WithUnit(unit))
	if err != nil {
		return noopHistogram{}
	}
	return histogram{h}
}

type noopCounter struct{}

func (noopCounter) Add(context.Context, int64, ...attribute.KeyValue) {}

type noopHistogram struct{}

func (noopHistogram) Record(context.Context, float64, ...attribute.KeyValue) {}
