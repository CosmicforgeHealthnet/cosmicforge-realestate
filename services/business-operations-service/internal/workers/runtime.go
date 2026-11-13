package workers

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/cfmeet"
	"log/slog"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/payment"
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
	CRM                  *crm.HistoryProjector
	crmConsumer          *jetstream.Consumer
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
	// Guest CRM notifications reuse the same signed Notification client.
	inventory.Notification = sender
	// Commercial gates in channel sync and collections re-check the same
	// Verification-owned operating-capacity assertion as the API.
	if verify != nil {
		inventory.CapacityAuthority = verify
	}
	if endpoint := l.String("PAYMENT_SERVICE_URL", ""); endpoint != "" {
		client, e := payment.New(endpoint, identity.Signer, 8*time.Second)
		if e != nil {
			return nil, e
		}
		inventory.Payment = client
	}
	meet, err := cfmeet.Load(l)
	if err != nil {
		consumer.Stop()
		return nil, err
	}
	inventory.CFMeet = meet
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
	history := &crm.HistoryProjector{Pool: pool, Outbox: outbox.NewStore(pool, prefix), Now: time.Now}
	historyConsumer, err := conn.Consume(ctx, jetstream.ConsumerConfig{Stream: businessEvents.StreamConfig(prefix).Name, Durable: crm.HistoryConsumer, FilterSubjects: []string{prefix + ".business.reservation.>", prefix + ".business.hospitality.stay.>", prefix + ".business.hospitality.folio.updated.v1"}, AckWait: 30 * time.Second, HandlerTimeout: 20 * time.Second, MaxDeliver: 12, OnFailure: worker.Quarantine}, history.Handle)
	if err != nil {
		consumer.Stop()
		return nil, err
	}
	return &Runtime{CRM: history, crmConsumer: historyConsumer, Inventory: inventory, InventoryReferences: refs, Notifications: worker, Lifecycle: &Lifecycle{Pool: pool, Outbox: outbox.NewStore(pool, prefix)}, consumer: consumer, log: log, conn: conn, prefix: prefix, Verification: &Verification{Inventory: projection, Projection: organisation.Projection{Sealer: sealer, Pool: pool, Outbox: outbox.NewStore(pool, prefix), Now: time.Now}, API: verify}}, nil
}
func (r *Runtime) Stop() {
	if r != nil && r.crmConsumer != nil {
		r.crmConsumer.Stop()
	}
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
			call, cancel = context.WithTimeout(ctx, 30*time.Second)
			err = r.Inventory.ProcessReservationFinance(call, 1)
			cancel()
			if err != nil && ctx.Err() == nil {
				r.log.Error("reservation financial reconciliation failed", "error", err.Error())
			}
			// Durable leased RMS automation jobs: schedule, capture, forecast,
			// recommend, approval submission/monitoring, application and monitoring.
			// Section 12: CF Meet contexts, provider inbox sync, scheduled
			// reply dispatch with readback, SLA escalation (leased, fenced).
			call, cancel = context.WithTimeout(ctx, 20*time.Second)
			err = r.Inventory.ProcessCommunications(call, 20)
			cancel()
			if err != nil && ctx.Err() == nil {
				r.log.Error("communication workers failed", "error", err.Error())
			}
			call, cancel = context.WithTimeout(ctx, 30*time.Second)
			err = r.Inventory.ProcessRMSAutomation(call, 10)
			cancel()
			if err != nil && ctx.Err() == nil {
				r.log.Error("rms automation batch failed", "error", err.Error())
			}
		}
		if !time.Now().Before(nextMaintenance) {
			nextMaintenance = time.Now().Add(time.Minute)
			if r.Inventory != nil {
				for _, task := range []struct {
					name string
					run  func(context.Context, int) error
				}{
					{"reservation", r.Inventory.ProcessReservationMaintenance},
					{"reservation-waitlist-automation", r.Inventory.ProcessWaitlistAutomation},
					{"commercial", r.Inventory.ProcessCommercialMaintenance},
					{"rms", r.Inventory.ProcessRMSMaintenance},
					{"hospitality", r.Inventory.ProcessHospitalityMaintenance},
					{"hospitality-room-safety", r.Inventory.ProcessPMSRooms},
					{"hospitality-access", r.Inventory.ProcessPMSAccess},
					{"hospitality-arrivals", r.Inventory.ProcessPMSArrivals},
					{"hospitality-day-automation", r.Inventory.ProcessPMSDayAutomation},
					{"crm-data-retention", r.Inventory.ProcessDataRetention},
					{"crm-notifications", r.Inventory.ProcessGuestNotifications},
					{"reputation", r.Inventory.ProcessReputation},
					{"marketing", r.Inventory.ProcessMarketing},
					{"finance", r.Inventory.ProcessFinance},
					{"operations", r.Inventory.ProcessOperations},
					{"analytics-reports", r.Inventory.ProcessReports},
				} {
					call, cancel := context.WithTimeout(ctx, 30*time.Second)
					err := task.run(call, 50)
					cancel()
					if err != nil && ctx.Err() == nil {
						r.log.Error("domain maintenance failed", "domain", task.name, "error", err.Error())
					}
				}
			}
			if r.CRM != nil {
				call, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := r.CRM.Tick(call, 50)
				cancel()
				if err != nil && ctx.Err() == nil {
					r.log.Error("guest history reconciliation failed", "error", err.Error())
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
