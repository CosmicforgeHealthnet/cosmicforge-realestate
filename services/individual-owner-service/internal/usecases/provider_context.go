package usecases

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"time"
)

// providerContext bounds neutral adapters as well as their HTTP implementation.
// Operation names are code constants; provider requests never become attributes.
func providerContext(ctx context.Context, operation string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	ctx, span := observability.StartSpan(ctx, "individual-owner-service", operation)
	return ctx, func() { span.End(); cancel() }
}

func providerOutcome(ctx context.Context, err error) error {
	if err != nil {
		trace.SpanFromContext(ctx).SetStatus(codes.Error, "External dependency failure")
	}
	return err
}
