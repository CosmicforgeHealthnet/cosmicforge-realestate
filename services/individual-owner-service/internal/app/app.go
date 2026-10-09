// Package app wires individual-owner-service components for the api and
// worker processes (and for integration tests).
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/credentials"
	ohttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/calendarsync"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/customernotify"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/esign"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/financing"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/insurance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/oauth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/smartaccess"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
)

// App holds wired components.
type App struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Pool    *pgxpool.Pool
	Service *usecases.Service
	Idem    *idempotency.Store
	Outbox  *outbox.Store
	Health  *health.Handler
	Router  http.Handler
	JWKS    jwt.JWKS
}

// Overrides lets tests substitute collaborators.
type Overrides struct {
	Notify       notification.Sender
	Now          func() time.Time
	Passwords    *passwords.Hasher
	ClosureHolds usecases.ClosureHoldChecker
	Verification verification.API
	Media        media.API
	Payment      payment.API
	ESign        esign.API
	CalendarSync calendarsync.API
	Financing    financing.API
	Insurance    insurance.API
	SmartAccess  smartaccess.API
	// CustomerNotify asks a recipient's own domain to notify its customer.
	CustomerNotify customernotify.API
}

// Build wires the shared components (database, use cases, HTTP router).
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger, ov Overrides) (*App, error) {
	pool, err := database.Open(ctx, database.Config{URL: cfg.DatabaseURL, ApplicationName: "individual-owner-service", MaxConns: cfg.DBMaxConns})
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	a := &App{Cfg: cfg, Log: log, Pool: pool}
	fail := func(err error) (*App, error) { pool.Close(); return nil, err }

	now := ov.Now
	if now == nil {
		now = time.Now
	}
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Issuer: cfg.TokenIssuer, Audience: cfg.TokenAudience, TTL: cfg.AccessTokenTTL, Key: cfg.SigningKey, Now: now})
	if err != nil {
		return fail(err)
	}
	keys := jwt.NewStaticKeys().Add(cfg.TokenIssuer, cfg.SigningKey.KID, cfg.SigningKey.PublicKey())
	a.JWKS.Keys = append(a.JWKS.Keys, jwt.PublicJWK(cfg.SigningKey.KID, cfg.SigningKey.PublicKey()))
	for kid, pub := range cfg.RetiredKeys {
		pk, _ := security.ParseEd25519PublicKey(pub)
		keys.Add(cfg.TokenIssuer, kid, pk)
		a.JWKS.Keys = append(a.JWKS.Keys, jwt.PublicJWK(kid, pk))
	}
	// Only this service's own tokens are accepted on owner routes.
	verifier, err := auth.NewVerifier(auth.VerifierConfig{Audience: cfg.TokenAudience, Now: now,
		Issuers: []auth.TrustedIssuer{{Issuer: cfg.TokenIssuer, Domain: platform.DomainIndividualOwner, Keys: keys}}})
	if err != nil {
		return fail(err)
	}
	pw := ov.Passwords
	if pw == nil {
		if pw, err = passwords.NewHasher(passwords.DefaultParams, cfg.PasswordWorkers); err != nil {
			return fail(err)
		}
	}
	hasher, err := credentials.NewHasher([]byte(cfg.CodePepper.Reveal()))
	if err != nil {
		return fail(err)
	}
	sealer, err := credentials.NewSealer(cfg.MFAKeyID, cfg.MFAKey, cfg.MFARetiredKeys)
	if err != nil {
		return fail(err)
	}
	notify := ov.Notify
	if notify == nil {
		if notify, err = notification.New(cfg.NotificationServiceURL, cfg.Identity.Signer, cfg.NotificationTimeout); err != nil {
			return fail(err)
		}
	}
	a.Outbox = outbox.NewStore(pool, cfg.SubjectPrefix)
	a.Idem = idempotency.NewStore(pool, 30*time.Second, 24*time.Hour)
	engine := authz.New(authz.Config{Rules: authz.AllRules(), StepUpWindow: cfg.StepUpWindow, Now: now,
		Resolver: usecases.PropertyAuthority{Pool: pool, Now: now}})
	verif, med := ov.Verification, ov.Media
	if verif == nil && cfg.VerificationServiceURL != "" {
		c, err := verification.New(cfg.VerificationServiceURL, cfg.Identity.Signer, cfg.DownstreamTimeout)
		if err != nil {
			return fail(err)
		}
		verif = c
	}
	if med == nil && cfg.MediaServiceURL != "" {
		c, err := media.New(cfg.MediaServiceURL, cfg.Identity.Signer, cfg.DownstreamTimeout)
		if err != nil {
			return fail(err)
		}
		med = c
	}
	a.Service, err = usecases.New(usecases.Deps{
		Pool: pool, Outbox: a.Outbox, Passwords: pw, Issuer: issuer, Verifier: verifier,
		Sessions: sessions.Config{MaxSessionLifetime: cfg.SessionLifetime, TokenIdleTTL: cfg.RefreshIdleTTL},
		Hasher:   hasher, Sealer: sealer, TOTP: credentials.TOTP{Issuer: cfg.TOTPIssuer}, Notify: notify, OAuth: oauth.NewRegistry(),
		Authz: engine, Policy: cfg.Policy, Logger: log, Now: now, ClosureHolds: ov.ClosureHolds,
		Verification: verif, Media: med, Payment: ov.Payment, ESign: ov.ESign, CalendarSync: ov.CalendarSync, Financing: ov.Financing, Insurance: ov.Insurance, SmartAccess: ov.SmartAccess, CustomerNotify: ov.CustomerNotify,
	})
	if err != nil {
		return fail(err)
	}
	a.Health = health.New(string(platform.IndividualOwnerService), cfg.Environment, 2*time.Second,
		health.Check{Name: "postgres", Fn: database.Ping(pool)})
	a.Router = ohttp.NewRouter(ohttp.Config{Service: a.Service, Idempotency: a.Idem, Verifier: cfg.Identity.Verifier, Health: a.Health, Logger: log, JWKS: a.JWKS})
	return a, nil
}

// Close waits for background deliveries and releases resources.
func (a *App) Close() {
	a.Service.Wait()
	a.Pool.Close()
}

// WorkerRunner builds the background runner around an outbox relay.
func (a *App) WorkerRunner(relay *outbox.Worker, owner string) *workers.Runner {
	return workers.New(workers.Config{Owner: owner, DispatchInterval: a.Cfg.DispatchPollInterval, ClosureInterval: a.Cfg.ClosureInterval,
		SessionRetention: a.Cfg.SessionRetention, DomainInterval: a.Cfg.WorkerInterval, ImportLease: a.Cfg.ImportLease}, a.Service, relay, a.Outbox, a.Idem, a.Log)
}

// Worker builds the background runner and its NATS connection.
func (a *App) Worker(ctx context.Context) (*workers.Runner, *jetstream.Conn, error) {
	conn, err := jetstream.Connect(jetstream.Config{URL: a.Cfg.NATSURL, Name: "individual-owner-service-worker", Logger: a.Log})
	if err != nil {
		return nil, nil, err
	}
	if a.Cfg.EnsureStream {
		if err := conn.EnsureStream(ctx, jetstream.StreamConfig{
			Name:     jetstream.StreamName(a.Cfg.SubjectPrefix, "owner"),
			Subjects: usecases.StreamSubjects(a.Cfg.SubjectPrefix),
		}); err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("ensure stream: %w", err)
		}
	}
	host, _ := os.Hostname()
	owner := fmt.Sprintf("%s-%s", host, ids.New().String()[24:])
	relay, err := outbox.NewWorker(a.Outbox, conn, outbox.WorkerConfig{Owner: owner, PollInterval: a.Cfg.OutboxPollInterval, Logger: a.Log})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	a.Health = health.New(string(platform.IndividualOwnerService), a.Cfg.Environment, 2*time.Second,
		health.Check{Name: "postgres", Fn: database.Ping(a.Pool)}, health.Check{Name: "nats", Fn: conn.Ready})
	a.StartConsumers(ctx, conn)
	return a.WorkerRunner(relay, owner), conn, nil
}

// StartConsumers starts the §10.1 durable consumers. A producer stream that
// does not exist yet (producer not deployed) is retried in the background
// instead of failing the worker; duplicates and redeliveries are absorbed by
// processed_events inside each handler's transaction.
func (a *App) StartConsumers(ctx context.Context, conn *jetstream.Conn) {
	for _, c := range a.Service.Consumers(a.Cfg.SubjectPrefix) {
		go func(c usecases.ConsumerSpec) {
			for {
				cons, err := conn.Consume(ctx, jetstream.ConsumerConfig{Stream: jetstream.StreamName(a.Cfg.SubjectPrefix, c.Stream), Durable: c.Name,
					FilterSubjects: c.Filters, MaxDeliver: 20, Backoff: []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}},
					func(ctx context.Context, d events.Delivery) error { return c.Handle(ctx, d.Envelope) })
				if err == nil {
					a.Log.Info("event consumer started", "consumer", c.Name)
					<-ctx.Done()
					cons.Stop()
					return
				}
				a.Log.Warn("event consumer not started; retrying", "consumer", c.Name, "error", err.Error())
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
			}
		}(c)
	}
}
