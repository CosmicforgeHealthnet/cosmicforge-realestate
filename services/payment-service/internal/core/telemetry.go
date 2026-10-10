package core

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"go.opentelemetry.io/otel/attribute"
	"log/slog"
)

var financialCommands = observability.NewCounter("payment-service", "payment.financial.commands", "Financial command outcomes without identity labels")

func recordCommand(ctx context.Context, operation string, err error) {
	outcome := "success"
	if err != nil {
		outcome = "rejected"
		slog.WarnContext(ctx, "financial command rejected", "operation", operation)
	}
	financialCommands.Add(ctx, 1, attribute.String("operation", operation), attribute.String("outcome", outcome))
}
