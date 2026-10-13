package database

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"time"
)

// queryTelemetry deliberately exports neither SQL nor bound parameters.
type queryTelemetry struct{}
type queryTiming struct {
	start time.Time
	span  trace.Span
}
type timingKey struct{}

func (queryTelemetry) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	ctx, span := observability.StartSpan(ctx, "database", "DB query")
	return context.WithValue(ctx, timingKey{}, queryTiming{time.Now(), span})
}
func (queryTelemetry) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	t, ok := ctx.Value(timingKey{}).(queryTiming)
	if !ok {
		return
	}
	defer t.span.End()
	result := "ok"
	if d.Err != nil {
		result = "error"
		t.span.SetStatus(codes.Error, "Database operation failed")
	}
	attrs := []attribute.KeyValue{attribute.String("result", result), attribute.String("sqlstate", SQLState(d.Err))}
	observability.NewHistogram("database", "db_query_duration_seconds", "Database query duration", "s").Record(ctx, time.Since(t.start).Seconds(), attrs...)
	if d.Err != nil {
		observability.NewCounter("database", "db_operation_failures_total", "Database failures").Add(ctx, 1, attrs...)
	}
}
