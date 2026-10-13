package app

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func (a *App) schemaReady(ctx context.Context) error {
	var version int64
	if err := a.Pool.QueryRow(ctx, `SELECT coalesce(max(version_id),0) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		return err
	}
	if version < 23 {
		return errors.New("required Owner migration not applied")
	}
	return nil
}

func (a *App) requiredConsumer(c usecases.ConsumerSpec) bool {
	switch c.Stream {
	case "payment":
		return a.Cfg.PaymentWalletServiceURL != ""
	case "verification":
		return a.Cfg.VerificationServiceURL != ""
	case "media":
		return a.Cfg.MediaServiceURL != ""
	}
	return false // Future producers must not make the whole process unready.
}

func (a *App) consumerReady(ctx context.Context, conn *jetstream.Conn) error {
	for _, c := range a.Service.Consumers(a.Cfg.SubjectPrefix) {
		if a.requiredConsumer(c) {
			if running, ok := a.consumers.Load(c.Name); !ok || !running.(bool) {
				return errors.New("required consumer not running")
			}
			consumer, err := conn.JetStream().Consumer(ctx, jetstream.StreamName(a.Cfg.SubjectPrefix, c.Stream), c.Name)
			if err != nil {
				return errors.New("required consumer unavailable")
			}
			if _, err = consumer.Info(ctx); err != nil {
				return errors.New("required consumer unavailable")
			}
		}
	}
	return nil
}

func (a *App) registerConsumerMetrics(conn *jetstream.Conn) error {
	meter := otel.Meter("individual-owner-service")
	available, err := meter.Int64ObservableGauge("owner_consumer_available")
	if err != nil {
		return err
	}
	pending, err := meter.Int64ObservableGauge("owner_consumer_pending", metric.WithDescription("Actual broker pending/ack-pending; absent when unavailable"))
	if err != nil {
		return err
	}
	reg, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		for _, c := range a.Service.Consumers(a.Cfg.SubjectPrefix) {
			attrs := metric.WithAttributes(attribute.String("consumer", c.Name))
			consumer, err := conn.JetStream().Consumer(ctx, jetstream.StreamName(a.Cfg.SubjectPrefix, c.Stream), c.Name)
			if err != nil {
				o.ObserveInt64(available, 0, attrs)
				continue
			}
			info, err := consumer.Info(ctx)
			if err != nil {
				o.ObserveInt64(available, 0, attrs)
				continue
			}
			running, ok := a.consumers.Load(c.Name)
			if !ok || !running.(bool) {
				o.ObserveInt64(available, 0, attrs)
				continue
			}
			o.ObserveInt64(available, 1, attrs)
			o.ObserveInt64(pending, int64(info.NumPending)+int64(info.NumAckPending), attrs)
		}
		return nil
	}, available, pending)
	if err == nil {
		a.consumerMetrics = reg
	}
	return err
}
