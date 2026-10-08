// Package server assembles the gateway HTTP handler: edge middleware, the
// route registry, user-token validation, distributed rate limiting and the
// reverse proxy to backend services.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	gwauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/middleware"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/ratelimit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/routing"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// strippedRequestHeaders are never accepted from clients: identity is derived
// only from verified credentials and re-asserted by the gateway.
var strippedRequestHeaders = []string{
	serviceauth.HeaderServiceAuthorization,
	"X-Actor-Domain", "X-Actor-Id", "X-Organization-Id", "X-Calling-Service",
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded",
	"Cookie",
}

// Gateway is the assembled edge.
type Gateway struct {
	cfg     *config.Config
	log     *slog.Logger
	auth    *gwauth.Authenticator
	limiter *ratelimit.Limiter
	health  *health.Handler
	proxies map[string]*httputil.ReverseProxy
	Handler http.Handler
}

// New builds a gateway from configuration and a Redis client.
func New(cfg *config.Config, rdb redis.UniversalClient, log *slog.Logger) (*Gateway, error) {
	v, err := sharedauth.NewVerifier(sharedauth.VerifierConfig{Issuers: cfg.TrustedIssuers, Audience: cfg.TokenAudience, Leeway: cfg.TokenLeeway})
	if err != nil {
		return nil, err
	}
	g := &Gateway{
		cfg:     cfg,
		log:     log,
		auth:    gwauth.New(v),
		limiter: ratelimit.New(rdb, "cfre:gw:rl", log),
		proxies: map[string]*httputil.ReverseProxy{},
	}
	// Redis is deliberately not a readiness gate: the edge degrades to a
	// local limiter (fail-closed for auth-sensitive routes) instead of
	// taking every replica out of rotation.
	g.health = health.New(string(platform.APIGateway), cfg.Environment, 2*time.Second)
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: cfg.UpstreamTimeout,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	for _, rt := range cfg.Routes {
		g.proxies[rt.Name] = g.newProxy(rt, observability.HTTPTransport(transport))
	}
	g.Handler = g.routes()
	return g, nil
}

// Health exposes the health handler (shutdown hook).
func (g *Gateway) Health() *health.Handler { return g.health }

func (g *Gateway) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(
		observability.HTTPMiddleware("api-gateway"),
		requestcontext.Middleware,
		httpx.WithLogger(g.log),
		httpx.Recoverer,
		httpx.AccessLog,
		security.Headers(security.HeadersConfig{HSTS: g.cfg.HSTS}),
		middleware.ClientIP(g.cfg.TrustedProxies),
		middleware.CORS(g.cfg.CORSAllowedOrigins),
		middleware.CanonicalPath,
	)
	r.NotFound(httpx.NotFoundHandler().ServeHTTP)
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)
	g.health.Register(r)
	// Internal service surfaces are never reachable through the edge.
	r.HandleFunc("/internal", g.notRouted)
	r.HandleFunc("/internal/*", g.notRouted)
	r.HandleFunc("/api/*", g.dispatch)
	r.HandleFunc("/webhooks/*", g.dispatch)
	return r
}

func (g *Gateway) notRouted(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, apperrors.NotFound(apperrors.CodeRouteNotFound, "The requested route does not exist."))
}

func (g *Gateway) dispatch(w http.ResponseWriter, r *http.Request) {
	route, ok := routing.Match(g.cfg.Routes, r.URL.Path)
	if !ok {
		g.notRouted(w, r)
		return
	}
	limit := g.cfg.MaxBodyBytes
	if route.MaxBodyBytes > 0 {
		limit = route.MaxBodyBytes
	}
	if r.ContentLength > limit {
		httpx.WriteError(w, r, apperrors.PayloadTooLarge())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	ctx, err := g.auth.Authenticate(r, route)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	r = r.WithContext(ctx)

	actor, authenticated := requestcontext.ActorFrom(ctx)
	cat := route.CategoryFor(r.Method, r.URL.Path, authenticated)
	key := "ip:" + middleware.ClientIPFrom(ctx)
	if authenticated && cat != routing.CategoryAuthSensitive && cat != routing.CategoryWebhook {
		key = "actor:" + actor.Domain + ":" + actor.ID.String()
	}
	if cat == routing.CategoryWebhook {
		key = "webhook:" + route.Name + ":" + middleware.ClientIPFrom(ctx)
	}
	policy := g.cfg.RateLimits[cat]
	dec, err := g.limiter.Allow(ctx, string(cat), key, policy)
	if errors.Is(err, ratelimit.ErrUnavailable) {
		httpx.WriteError(w, r, apperrors.Unavailable("Please retry shortly.").WithRetryAfter(1))
		return
	}
	if !dec.Allowed {
		secs := int((dec.RetryAfter + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		httpx.WriteError(w, r, apperrors.RateLimited(secs))
		return
	}
	tok, err := g.cfg.Signer.Token(ctx, string(route.Service))
	if err != nil {
		httpx.WriteError(w, r, apperrors.Internal(err))
		return
	}
	r = r.WithContext(context.WithValue(ctx, assertionKey{}, tok))
	g.proxies[route.Name].ServeHTTP(w, r)
}

// assertionKey carries the gateway's signed service token for the upstream.
type assertionKey struct{}

func (g *Gateway) newProxy(rt routing.Route, transport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(rt.Upstream)
			pr.Out.Host = rt.Upstream.Host
			for _, h := range strippedRequestHeaders {
				pr.Out.Header.Del(h)
			}
			if !rt.ForwardUserToken {
				pr.Out.Header.Del("Authorization")
			}
			ctx := pr.In.Context()
			if ip := middleware.ClientIPFrom(ctx); ip != "" {
				pr.Out.Header.Set("X-Forwarded-For", ip)
			}
			requestcontext.Inject(ctx, pr.Out.Header)
			tok, _ := ctx.Value(assertionKey{}).(string)
			pr.Out.Header.Set(serviceauth.HeaderServiceAuthorization, "Bearer "+tok)
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Server")
			resp.Header.Del("X-Powered-By")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(r.Context().Err(), context.Canceled) {
				return // client went away
			}
			msg := "The upstream service is unavailable."
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
				msg = "The upstream service timed out."
			}
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				httpx.WriteError(w, r, apperrors.PayloadTooLarge())
				return
			}
			logging.FromContext(r.Context()).WarnContext(r.Context(), "upstream request failed",
				"service", string(rt.Service), "error", err.Error())
			httpx.WriteError(w, r, apperrors.Unavailable(msg).WithCause(err))
		},
	}
}
