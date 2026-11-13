package domainkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// MaxBody bounds command bodies on every domain route.
const MaxBody = 256 << 10

// Runtime holds the wired platform components of one domain service.
type Runtime struct {
	Cfg      *Config
	Log      *slog.Logger
	Pool     *pgxpool.Pool
	Idem     *idempotency.Store
	Outbox   *outbox.Store
	Audit    *audit.Recorder
	Health   *health.Handler
	Accounts *Accounts
	Business *httpx.Client
	Now      func() time.Time
}

// Open connects the database and wires shared components.
func Open(ctx context.Context, cfg *Config, log *slog.Logger) (*Runtime, error) {
	pool, err := database.Open(ctx, database.Config{URL: cfg.DatabaseURL, ApplicationName: string(cfg.Service), MaxConns: cfg.DBMaxConns})
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	rt := &Runtime{Cfg: cfg, Log: log, Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
	rt.Idem = idempotency.NewStore(pool, 30*time.Second, 24*time.Hour)
	rt.Outbox = outbox.NewStore(pool, cfg.SubjectPrefix)
	rt.Audit = audit.NewRecorder(string(cfg.Service))
	rt.Health = health.New(string(cfg.Service), cfg.Environment, 2*time.Second, health.Check{Name: "postgres", Fn: database.Ping(pool)})
	if cfg.TokenKey.PrivateKey != nil {
		rt.Accounts, err = NewAccounts(rt)
		if err != nil {
			pool.Close()
			return nil, err
		}
	}
	if cfg.BusinessURL != "" {
		rt.Business, err = httpx.NewClient(httpx.ClientConfig{BaseURL: cfg.BusinessURL, Audience: string(platform.BusinessOperationsService), Tokens: cfg.Identity.Signer, Timeout: 8 * time.Second, MaxAttempts: 3, MaxResponseBytes: 1 << 20})
		if err != nil {
			pool.Close()
			return nil, err
		}
	}
	return rt, nil
}

// Close releases resources.
func (rt *Runtime) Close() { rt.Pool.Close() }

// Router returns the base router: observability, request context, logging,
// recovery, security headers, health and (when the API owns accounts) the
// account routes under the domain prefix.
func (rt *Runtime) Router() chi.Router {
	r := chi.NewRouter()
	r.Use(observability.HTTPMiddleware(string(rt.Cfg.Service)), requestcontext.Middleware, httpx.WithLogger(rt.Log),
		httpx.Recoverer, httpx.AccessLog, security.Headers(security.HeadersConfig{}))
	r.NotFound(httpx.NotFoundHandler().ServeHTTP)
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)
	rt.Health.Register(r)
	return r
}

// ActorRoutes mounts the gateway-authenticated actor API at /api/v1/<domain>.
// Account routes (register/login/refresh) are mounted without an actor; all
// other routes require a delegated actor of this domain with an ACTIVE local
// account, which is placed in the context for handlers.
func (rt *Runtime) ActorRoutes(r chi.Router, mount func(r chi.Router)) {
	r.Route("/api/v1/"+string(rt.Cfg.Domain), func(r chi.Router) {
		r.Use(serviceauth.Authenticate(rt.Cfg.Identity.Verifier, serviceauth.HeaderServiceAuthorization),
			serviceauth.RequireCaller(platform.APIGateway), httpx.MaxBody(MaxBody))
		if rt.Accounts != nil {
			r.Post("/auth/register", rt.Accounts.register)
			r.Post("/auth/login", rt.Accounts.login)
			r.Post("/auth/refresh", rt.Accounts.refresh)
		}
		r.Group(func(r chi.Router) {
			r.Use(rt.requireAccount)
			if rt.Accounts != nil {
				r.Get("/me", rt.Accounts.me)
				r.Post("/auth/logout", rt.Accounts.logout)
			}
			mount(r)
		})
	})
}

// InternalRoutes mounts the service-to-service API at /internal/v1 with an
// explicit caller allow-list.
func (rt *Runtime) InternalRoutes(r chi.Router, callers []platform.Service, mount func(r chi.Router)) {
	r.Route("/internal/v1", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(rt.Cfg.Identity.Verifier, serviceauth.HeaderAuthorization),
			serviceauth.RequireCaller(callers...), httpx.MaxBody(MaxBody))
		mount(r)
	})
}

type accountKey struct{}

// Account is the authenticated local account of the delegated actor.
type Account struct {
	ID          ids.ID   `json:"id"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
	Status      string   `json:"status"`
}

// HasRole reports whether the account holds role.
func (a Account) HasRole(role string) bool { return slices.Contains(a.Roles, role) }

// AccountFrom returns the authenticated account.
func AccountFrom(ctx context.Context) (Account, bool) {
	a, ok := ctx.Value(accountKey{}).(Account)
	return a, ok
}

// WithAccount attaches an account (tests and internal composition).
func WithAccount(ctx context.Context, a Account) context.Context {
	return context.WithValue(ctx, accountKey{}, a)
}

func (rt *Runtime) requireAccount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requestcontext.ActorFrom(r.Context())
		if !ok || actor.Domain != string(rt.Cfg.Domain) || actor.ID.IsZero() {
			httpx.WriteError(w, r, apperrors.AuthenticationRequired())
			return
		}
		var a Account
		err := rt.Pool.QueryRow(r.Context(), `SELECT id,email,display_name,roles,status FROM domain_accounts WHERE id=$1`, actor.ID).Scan(&a.ID, &a.Email, &a.DisplayName, &a.Roles, &a.Status)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && a.Status != "ACTIVE" {
			httpx.WriteError(w, r, apperrors.PermissionDenied("The account is not active."))
			return
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithAccount(r.Context(), a)))
	})
}

// RequireRole returns middleware enforcing one of roles.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a, ok := AccountFrom(r.Context())
			if !ok || !slices.ContainsFunc(roles, a.HasRole) {
				httpx.WriteError(w, r, apperrors.PermissionDenied("The account does not hold the required role."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Change is what a command produces inside its transaction.
type Change struct {
	Status int
	Body   any
	// Audit is recorded in the same transaction (TargetID required).
	Audit *audit.Event
	// Events are added to the transactional outbox.
	Events []events.Envelope
}

// Command decodes the JSON body into in, then executes fn exactly once per
// Idempotency-Key (replaying the stored response), committing state, audit
// and outbox events atomically.
func (rt *Runtime) Command(w http.ResponseWriter, r *http.Request, scope string, in any, fn func(ctx context.Context, tx pgx.Tx) (Change, error)) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.WriteError(w, r, apperrors.InvalidRequest("Unreadable request body."))
		return
	}
	if in != nil {
		if err = httpx.DecodeJSONBytes(body, in); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	rt.Idem.ServeCommand(w, r, scope, body, nil, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		c, err := fn(ctx, tx)
		if err != nil {
			return idempotency.Result{}, err
		}
		if err = rt.Commit(ctx, tx, c); err != nil {
			return idempotency.Result{}, err
		}
		if c.Status == 0 {
			c.Status = http.StatusOK
		}
		return idempotency.Result{Status: c.Status, Body: httpx.DataEnvelope{Data: c.Body}}, nil
	})
}

// Commit records audit and outbox events of c inside tx.
func (rt *Runtime) Commit(ctx context.Context, tx pgx.Tx, c Change) error {
	if c.Audit != nil {
		if _, err := rt.Audit.Record(ctx, tx, *c.Audit); err != nil {
			return err
		}
	}
	for _, e := range c.Events {
		if err := rt.Outbox.Add(ctx, tx, e); err != nil {
			return err
		}
	}
	return nil
}

// Event builds an outbox envelope produced by this service.
func (rt *Runtime) Event(ctx context.Context, eventType, aggregate string, id ids.ID, payload any) (events.Envelope, error) {
	return events.New(ctx, rt.Cfg.Service, eventType, aggregate, id, payload)
}

// Handler processes one consumed event inside a transaction; the delivery is
// recorded in processed_events in the same transaction (exactly-once effect).
type Handler func(ctx context.Context, tx pgx.Tx, e events.Envelope) error

// Consume subscribes durable to subjects on stream with exactly-once effects.
func (rt *Runtime) Consume(ctx context.Context, conn *jetstream.Conn, stream, durable string, subjects []string, h Handler) (*jetstream.Consumer, error) {
	return conn.Consume(ctx, jetstream.ConsumerConfig{Stream: stream, Durable: durable, FilterSubjects: subjects, AckWait: 30 * time.Second, HandlerTimeout: 20 * time.Second, MaxDeliver: 20},
		func(ctx context.Context, d events.Delivery) error {
			return database.WithTx(ctx, rt.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				fresh, err := events.MarkProcessed(ctx, tx, durable, d.Envelope)
				if err != nil || !fresh {
					return err
				}
				return h(ctx, tx, d.Envelope)
			})
		})
}

// Relay runs the transactional outbox publisher until ctx ends.
func (rt *Runtime) Relay(ctx context.Context, conn *jetstream.Conn) error {
	host, _ := os.Hostname()
	w, err := outbox.NewWorker(rt.Outbox, conn, outbox.WorkerConfig{Owner: fmt.Sprintf("%s-%s-%s", rt.Cfg.Service, host, ids.New().String()[24:]), PollInterval: rt.Cfg.OutboxPollInterval, Logger: rt.Log})
	if err != nil {
		return err
	}
	return w.Run(ctx)
}

// Data writes {"data": v}.
func Data(w http.ResponseWriter, status int, v any) { httpx.WriteData(w, status, v) }

// Fail writes err.
func Fail(w http.ResponseWriter, r *http.Request, err error) { httpx.WriteError(w, r, err) }

// JSON decodes a raw payload into a map (helper for event handlers).
func JSON(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}
