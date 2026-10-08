// Package usecases implements notification-service behaviour: template
// rendering, preference and quiet-hours policy, queueing, bounded-retry
// dispatch with leases, push endpoints and preferences. Provider calls never
// run inside a database transaction.
package usecases

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/channels"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Events published by notification-service (one per delivery).
const (
	EventQueued = "notification.queued.v1"
	EventSent   = "notification.sent.v1"
	EventFailed = "notification.failed.v1"
)

// Options configures behaviour.
type Options struct {
	DispatchLease    time.Duration // visibility timeout of an in-flight provider call
	SendTimeout      time.Duration // per provider call; must be < DispatchLease
	RetryBase        time.Duration
	RetryMax         time.Duration
	ContentRetention time.Duration // rendered content is purged after this
	DefaultLocale    string
	Now              func() time.Time
	Jitter           func(time.Duration) time.Duration
}

func (o *Options) defaults() {
	if o.DispatchLease <= 0 {
		o.DispatchLease = 2 * time.Minute
	}
	if o.SendTimeout <= 0 || o.SendTimeout >= o.DispatchLease {
		o.SendTimeout = o.DispatchLease / 4
	}
	if o.RetryBase <= 0 {
		o.RetryBase = 30 * time.Second
	}
	if o.RetryMax <= 0 {
		o.RetryMax = time.Hour
	}
	if o.ContentRetention <= 0 {
		o.ContentRetention = 7 * 24 * time.Hour
	}
	if o.DefaultLocale == "" {
		o.DefaultLocale = "en"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Jitter == nil {
		o.Jitter = func(d time.Duration) time.Duration {
			if d < 10 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(d / 5))) // up to +20%
		}
	}
}

// Service is the notification application service.
type Service struct {
	db      database.DB
	q       *db.Queries
	outbox  *outbox.Store
	audit   *audit.Recorder
	senders channels.Registry
	opts    Options
	log     *slog.Logger
}

// New wires the service.
func New(pool database.DB, ob *outbox.Store, senders channels.Registry, opts Options, log *slog.Logger) *Service {
	opts.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: pool, q: db.New(), outbox: ob, audit: audit.NewRecorder(string(platform.NotificationService)),
		senders: senders, opts: opts, log: log}
}

func (s *Service) now() time.Time { return s.opts.Now().UTC().Truncate(time.Microsecond) }

// Principal is the authenticated caller (a service, possibly acting for a user).
type Principal struct {
	Service     platform.Service
	ActorDomain string
	ActorID     ids.ID
}

// HasActor reports a delegated end user.
func (p Principal) HasActor() bool { return p.ActorDomain != "" && !p.ActorID.IsZero() }

// PrincipalFrom builds the principal from the authenticated context.
func PrincipalFrom(ctx context.Context) Principal {
	sp, _ := serviceauth.PrincipalFrom(ctx)
	p := Principal{Service: sp.Service}
	if sp.Delegation.HasActor() {
		p.ActorDomain, p.ActorID = string(sp.Delegation.ActorDomain), sp.Delegation.ActorID
	}
	return p
}

// Recipient is a domain-aware user reference. notification-service never
// reads another domain's user tables; callers supply contact destinations.
type Recipient struct {
	Domain platform.Domain `json:"recipient_domain"`
	ID     ids.ID          `json:"recipient_id"`
}

// crossDomainSenders may address recipients in any domain.
var crossDomainSenders = map[platform.Service]bool{
	platform.AdminService: true, platform.PaymentService: true, platform.VerificationService: true,
}

// AuthorizeRecipient enforces recipient isolation: a user (via the gateway)
// manages only themself; a domain service addresses only its own domain's
// users; admin, payment and verification services may address any domain.
func AuthorizeRecipient(p Principal, r Recipient) error {
	if !platform.KnownDomain(string(r.Domain)) || r.ID.ValidateV7() != nil {
		return apperrors.InvalidRequest("recipient must have a known recipient_domain and a UUIDv7 recipient_id.")
	}
	if p.Service == platform.APIGateway {
		if !p.HasActor() || p.ActorDomain != string(r.Domain) || p.ActorID != r.ID {
			return apperrors.PermissionDenied("Users may only manage their own notification settings.")
		}
		return nil
	}
	if crossDomainSenders[p.Service] {
		return nil
	}
	if d, ok := platform.DomainForService(p.Service); ok && d == r.Domain {
		return nil
	}
	return apperrors.PermissionDenied("A domain service may only address recipients in its own domain.").
		WithDetails(apperrors.Detail{Code: domain.CodeRecipientDomain, Message: "recipient_domain must match the calling service's domain"})
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func notFound(err error, e *apperrors.Error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return e
	}
	return err
}

func (s *Service) withTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return database.WithTx(ctx, s.db, database.TxOptions{}, fn)
}
