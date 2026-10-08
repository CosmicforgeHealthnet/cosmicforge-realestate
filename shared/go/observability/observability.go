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
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
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
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}

// HTTPMiddleware creates a server span per request and extracts inbound
// trace context. Install it before requestcontext.Middleware.
func HTTPMiddleware(operation string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, operation,
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return r.Method + " " + r.URL.Path
			}),
			otelhttp.WithFilter(func(r *http.Request) bool {
				switch r.URL.Path {
				case "/health", "/ready", "/version":
					return false
				}
				return true
			}),
		)
	}
}

// HTTPTransport wraps an outbound transport with client spans and context injection.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
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
