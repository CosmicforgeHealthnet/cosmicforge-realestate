// Package usecases implements individual-owner-service commands and queries.
//
// Section 5 (authentication, owner roles and authorization) is implemented
// here: domain-owned accounts, credentials, sessions, devices, MFA, step-up,
// recovery and the account lifecycle. Every state change runs in one
// PostgreSQL transaction together with its audit record, owner-visible
// security event, durable notification request and outbox event.
//
// External calls (notification-service) never run inside a transaction.
package usecases

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/oauth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
)

// ActivationRule selects which verified contact activates an account.
type ActivationRule string

const (
	ActivateOnEmail        ActivationRule = "EMAIL"
	ActivateOnPhone        ActivationRule = "PHONE"
	ActivateOnEmailOrPhone ActivationRule = "EMAIL_OR_PHONE"
	ActivateOnEmailAndPhn  ActivationRule = "EMAIL_AND_PHONE"
)

// Policy is the versioned security policy. Its Version is recorded on audit
// records so decisions remain explainable after the policy changes.
type Policy struct {
	Version             string
	MaxLoginFailures    int
	LockoutDuration     time.Duration
	CodeTTL             time.Duration
	CodeMaxAttempts     int
	CodeSendLimit       int
	CodeSendWindow      time.Duration
	MFALoginTTL         time.Duration
	MFALoginMaxAttempts int
	MFAFallbackMethods  []string // SMS, EMAIL second factors when MFA is enabled
	ClosureCoolingOff   time.Duration
	Activation          ActivationRule
	RecoveryCodeCount   int
	PasswordPolicy      passwords.Policy
}

// DefaultPolicy is the baseline (policy version 2026-10-01).
func DefaultPolicy() Policy {
	return Policy{
		Version: "2026-10-01", MaxLoginFailures: 5, LockoutDuration: 15 * time.Minute,
		CodeTTL: 10 * time.Minute, CodeMaxAttempts: 5, CodeSendLimit: 5, CodeSendWindow: time.Hour,
		MFALoginTTL: 5 * time.Minute, MFALoginMaxAttempts: 5, MFAFallbackMethods: []string{MFASMS, MFAEmail},
		ClosureCoolingOff: 30 * 24 * time.Hour, Activation: ActivateOnEmail, RecoveryCodeCount: 10,
		PasswordPolicy: passwords.DefaultPolicy,
	}
}

// Deps are the collaborators of Service.
type Deps struct {
	Pool         *pgxpool.Pool
	Outbox       *outbox.Store
	Passwords    *passwords.Hasher
	Issuer       *auth.Issuer
	Verifier     *auth.Verifier
	PublicKeys   []jwt.JWK
	Sessions     sessions.Config
	Hasher       *credentials.Hasher
	Sealer       *credentials.Sealer
	TOTP         credentials.TOTP
	Notify       notification.Sender
	OAuth        *oauth.Registry
	Authz        *authz.Engine
	Policy       Policy
	Logger       *slog.Logger
	Now          func() time.Time
	ClosureHolds ClosureHoldChecker
}

// Service implements owner account use cases.
type Service struct {
	Deps
	q       *db.Queries
	audit   *audit.Recorder
	dummyPW string
	bg      sync.WaitGroup
}

// ClosureHoldChecker reports reasons an account closure must not finalize
// (open tenancies, disputes, legal or financial holds). Business aggregates
// that create holds register here as they are implemented.
type ClosureHoldChecker interface {
	Holds(ctx context.Context, tx pgx.Tx, accountID ids.ID) ([]string, error)
}

type noHolds struct{}

func (noHolds) Holds(context.Context, pgx.Tx, ids.ID) ([]string, error) { return nil, nil }

// New builds the service.
func New(d Deps) (*Service, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.ClosureHolds == nil {
		d.ClosureHolds = noHolds{}
	}
	if d.OAuth == nil {
		d.OAuth = oauth.NewRegistry()
	}
	d.Sessions.Now = d.Now
	s := &Service{Deps: d, q: db.New(), audit: audit.NewRecorder(string(platform.IndividualOwnerService))}
	// A real hash used to equalize timing when an account does not exist.
	h, err := d.Passwords.Hash(context.Background(), "timing-equalizer-"+ids.New().String())
	if err != nil {
		return nil, err
	}
	s.dummyPW = h
	return s, nil
}

// Wait blocks until background deliveries finish (shutdown and tests).
func (s *Service) Wait() { s.bg.Wait() }

func (s *Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func (s *Service) tx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return database.WithTx(ctx, s.Pool, database.TxOptions{}, fn)
}

func (s *Service) sessionManager(q database.Querier, create *db.CreateSessionParams) *sessions.Manager {
	return sessions.NewManager(&sessionStore{q: s.q, db: q, create: create}, s.Sessions)
}

// ClientInfo is request metadata. It is informational and never used for
// authorization; the IP comes from the gateway's X-Forwarded-For only.
type ClientInfo struct {
	IP         *netip.Addr
	UserAgent  string
	DeviceKey  string // client-generated installation ID (X-Device-ID)
	DeviceName string
	Platform   string // IOS | ANDROID | WEB | OTHER
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func idp(id ids.ID) *ids.ID {
	if id.IsZero() {
		return nil
	}
	return &id
}

func jsonb(v any) []byte {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// withAccountActor attributes subsequent audit/event records to account.
func withAccountActor(ctx context.Context, accountID ids.ID) context.Context {
	return requestcontext.WithActor(ctx, requestcontext.Actor{Domain: string(platform.DomainIndividualOwner), ID: accountID})
}

func (s *Service) auditTx(ctx context.Context, tx pgx.Tx, action string, target ids.ID, result string, changes, md map[string]any) error {
	if md == nil {
		md = map[string]any{}
	}
	md["policy_version"] = s.Policy.Version
	_, err := s.audit.Record(ctx, tx, audit.Event{Action: action, TargetType: "owner_account", TargetID: target, Result: result, Changes: changes, Metadata: md})
	return err
}

// securityEvent records an owner-visible security history entry.
func (s *Service) securityEvent(ctx context.Context, tx pgx.Tx, accountID ids.ID, typ string, sessionID, deviceID ids.ID, ci ClientInfo, md map[string]any) error {
	return s.q.InsertSecurityEvent(ctx, tx, db.InsertSecurityEventParams{
		EventID: ids.New(), AccountID: accountID, EventType: typ, SessionID: idp(sessionID), DeviceID: idp(deviceID),
		IpAddress: ci.IP, UserAgent: strp(truncate(ci.UserAgent, 512)), Metadata: jsonb(md), OccurredAt: s.now(),
	})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Security alert texts (notification template account.security_alert).
var alertText = map[string]string{
	"NEW_DEVICE_LOGIN":    "sign-in from an unrecognized device",
	"PASSWORD_CHANGED":    "your password was changed",
	"PASSWORD_RESET":      "your password was reset",
	"MFA_ENABLED":         "two-step verification was turned on",
	"MFA_DISABLED":        "two-step verification was turned off",
	"RECOVERY_CODES":      "new recovery codes were generated",
	"EMAIL_CHANGED":       "your sign-in email was changed",
	"PHONE_CHANGED":       "your phone number was changed",
	"ACCOUNT_LOCKED":      "sign-in was temporarily locked after failed attempts",
	"TOKEN_REUSE":         "a sign-in session was ended after suspicious token reuse",
	"CLOSURE_REQUESTED":   "account closure was requested",
	"SESSIONS_REVOKED":    "all sessions were signed out",
	"ACCOUNT_RESTRICTION": "your account status changed",
}

// enqueueAlert records a durable security-alert request in tx. extraEmail
// lets an alert reach a previous address (e.g. after an email change).
func (s *Service) enqueueAlert(ctx context.Context, tx pgx.Tx, acc db.OwnerAccount, key string, extraEmail string) error {
	text, ok := alertText[key]
	if !ok {
		return errors.New("usecases: unknown alert " + key)
	}
	var email, phone *string
	channels := []string{notification.ChannelPush}
	if acc.EmailVerifiedAt != nil {
		email = &acc.Email
	}
	if extraEmail != "" {
		email = &extraEmail
	}
	if email != nil {
		channels = append(channels, notification.ChannelEmail)
	} else if acc.PhoneVerifiedAt != nil && acc.PhoneE164 != nil {
		phone = acc.PhoneE164
		channels = append(channels, notification.ChannelSMS)
	}
	return s.q.InsertDispatch(ctx, tx, db.InsertDispatchParams{
		DispatchID: ids.New(), AccountID: acc.AccountID, TemplateKey: notification.TemplateSecurityAlert,
		Channels: channels, Variables: jsonb(map[string]string{"event": text}),
		DestinationEmail: email, DestinationPhone: phone, NextAttemptAt: s.now(),
	})
}

// emit adds a domain event to the outbox inside tx.
func (s *Service) emit(ctx context.Context, tx pgx.Tx, eventType string, accountID ids.ID, payload any) error {
	e, err := events.New(ctx, platform.IndividualOwnerService, eventType, AggregateOwnerAccount, accountID, payload)
	if err != nil {
		return err
	}
	return s.Outbox.Add(ctx, tx, e)
}

// background runs fn after the response without the request's cancellation.
func (s *Service) background(ctx context.Context, fn func(ctx context.Context)) {
	dctx := requestcontext.Detach(ctx)
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		c, cancel := context.WithTimeout(dctx, 30*time.Second)
		defer cancel()
		fn(c)
	}()
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
