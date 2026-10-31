package workers

import (
	"context"
	"log/slog"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/organisation"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Runtime struct {
	Inventory            *businesshttp.Handler
	InventoryReferences  *InventoryReferences
	Notifications        *Notifications
	Lifecycle            *Lifecycle
	consumer             *jetstream.Consumer
	verificationConsumer *jetstream.Consumer
	Verification         *Verification
	conn                 *jetstream.Conn
	prefix               string
	log                  *slog.Logger
}

func Start(ctx context.Context, pool *pgxpool.Pool, conn *jetstream.Conn, prefix string, l *config.Loader, log *slog.Logger) (*Runtime, error) {
	identity, err := serviceauth.LoadIdentity(l, platform.BusinessOperationsService)
	if err != nil {
		return nil, err
	}
	sealer, err := credentials.LoadSealer(l)
	if err != nil {
		return nil, err
	}
	url := l.Required("NOTIFICATION_SERVICE_URL")
	if err := l.Err(); err != nil {
		return nil, err
	}
	sender, err := notification.New(url, identity.Signer, 5*time.Second)
	if err != nil {
		return nil, err
	}
	verify, err := verification.New(l.Required("VERIFICATION_SERVICE_URL"), identity.Signer, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if err = l.Err(); err != nil {
		return nil, err
	}
	worker := &Notifications{Pool: pool, Sealer: sealer, Sender: sender}
	consumer, err := conn.Consume(ctx, jetstream.ConsumerConfig{Stream: businessEvents.StreamConfig(prefix).Name, Durable: NotificationConsumer, FilterSubjects: []string{events.Subject(prefix, businessEvents.NotificationRequested)}, AckWait: 30 * time.Second, HandlerTimeout: 10 * time.Second, MaxDeliver: 12, OnFailure: worker.Quarantine}, worker.Handle)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	projection := &portfolio.VerificationProjector{Pool: pool, Outbox: outbox.NewStore(pool, prefix), Now: time.Now}
	inventory := &businesshttp.Handler{Auth: &businessauth.Handler{Service: &businessauth.Service{Pool: pool, Outbox: outbox.NewStore(pool, prefix), Sealer: sealer, Now: time.Now}}}
	inventory.ChannelAdapters = map[string]distribution.Adapter{"BOOKING_COM": distribution.DisabledAdapter{Provider: "BOOKING_COM"}, "AIRBNB": distribution.DisabledAdapter{Provider: "AIRBNB"}}
	if endpoint := l.String("MARKETPLACE_SERVICE_URL", ""); endpoint != "" {
		adapter, e := distribution.NewMarketplace(endpoint, identity.Signer)
		if e != nil {
			consumer.Stop()
			return nil, e
		}
		inventory.ChannelAdapters[distribution.Marketplace] = adapter
	}
	refs := &InventoryReferences{Projection: *projection, Verification: verify}
	if mediaURL := l.String("MEDIA_SERVICE_URL", ""); mediaURL != "" {
		client, err := media.New(mediaURL, identity.Signer, 5*time.Second)
		if err != nil {
			consumer.Stop()
			return nil, err
		}
		refs.Media = client
		inventory.Media = client
	}
	return &Runtime{Inventory: inventory, InventoryReferences: refs, Notifications: worker, Lifecycle: &Lifecycle{Pool: pool, Outbox: outbox.NewStore(pool, prefix)}, consumer: consumer, log: log, conn: conn, prefix: prefix, Verification: &Verification{Inventory: projection, Projection: organisation.Projection{Sealer: sealer, Pool: pool, Outbox: outbox.NewStore(pool, prefix), Now: time.Now}, API: verify}}, nil
}
func (r *Runtime) Stop() {
	if r != nil && r.consumer != nil {
		r.consumer.Stop()
		if r.verificationConsumer != nil {
			r.verificationConsumer.Stop()
		}
	}
}
func (r *Runtime) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	var nextMaintenance time.Time
	defer ticker.Stop()
	for {
		if r.Inventory != nil {
			call, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := r.Inventory.ProcessInventoryImports(call, 20)
			cancel()
			if err != nil && ctx.Err() == nil {
				r.log.Error("inventory import batch failed", "error", err.Error())
			}
			call, cancel = context.WithTimeout(ctx, 20*time.Second)
			err = r.Inventory.ProcessCommercialRates(call, 5)
			cancel()
			if err != nil && ctx.Err() == nil {
				r.log.Error("commercial rate batch failed", "error", err.Error())
			}
			call, cancel = context.WithTimeout(ctx, 20*time.Second)
			err = r.Inventory.ProcessChannelSync(call, 5)
			cancel()
			if err != nil && ctx.Err() == nil {
				r.log.Error("channel sync batch failed", "error", err.Error())
			}
		}
		if !time.Now().Before(nextMaintenance) {
			nextMaintenance = time.Now().Add(time.Minute)
			if r.Inventory != nil {
				call, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := r.Inventory.ProcessCommercialMaintenance(call, 50)
				cancel()
				if err != nil && ctx.Err() == nil {
					r.log.Error("commercial supply maintenance failed", "error", err.Error())
				}
			}
			if r.verificationConsumer == nil {
				consumer, err := r.conn.Consume(ctx, jetstream.ConsumerConfig{Stream: jetstream.StreamName(r.prefix, "verification"), Durable: VerificationConsumer, FilterSubjects: []string{r.prefix + ".verification.>"}, AckWait: 30 * time.Second, HandlerTimeout: 15 * time.Second, MaxDeliver: 12, OnFailure: r.Notifications.Quarantine}, r.Verification.Handle)
				if err == nil {
					r.verificationConsumer = consumer
				} else if ctx.Err() == nil {
					r.log.Warn("verification event stream unavailable; authenticated reconciliation remains active")
				}
			}
			verifyCtx, verifyCancel := context.WithTimeout(ctx, 20*time.Second)
			verifyErr := r.Verification.Tick(verifyCtx)
			verifyCancel()
			if err := verifyErr; err != nil && ctx.Err() == nil {
				r.log.Error("verification reconciliation failed", "error", err.Error())
			}
			if err := r.Lifecycle.Tick(ctx); err != nil && ctx.Err() == nil {
				r.log.Error("business lifecycle reconciliation failed", "error", err.Error())
			}
			if err := r.Notifications.RequeueUnaccepted(ctx); err != nil && ctx.Err() == nil {
				r.log.Error("notification source reconciliation failed", "error", err.Error())
			}
			if r.InventoryReferences != nil {
				call, cancel := context.WithTimeout(ctx, 20*time.Second)
				err := r.InventoryReferences.Tick(call)
				cancel()
				if err != nil && ctx.Err() == nil {
					r.log.Error("inventory reference reconciliation failed", "error", err.Error())
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
