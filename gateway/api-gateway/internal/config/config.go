// Package config loads and validates gateway configuration from the
// environment. Startup fails fast on any invalid or missing value.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/ratelimit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/routing"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Config is the validated gateway configuration.
type Config struct {
	Environment     string
	LogLevel        string
	HTTPAddr        string
	ShutdownTimeout time.Duration
	DrainDelay      time.Duration

	TrustedIssuers []auth.TrustedIssuer
	TokenAudience  string
	TokenLeeway    time.Duration

	Signer *serviceauth.Signer

	Routes          []routing.Route
	UpstreamTimeout time.Duration
	MaxBodyBytes    int64

	RedisAddr     string
	RedisPassword security.Secret
	RedisDB       int
	RedisTLS      bool
	RateLimits    map[routing.Category]ratelimit.Policy

	CORSAllowedOrigins []string
	TrustedProxies     []*net.IPNet
	HSTS               bool

	OTLPEndpoint string
	OTLPInsecure bool
	SampleRatio  float64
}

// upstreamEnv maps services to their URL variables.
var upstreamEnv = map[platform.Service]string{
	platform.VerificationService:         "GATEWAY_UPSTREAM_VERIFICATION_SERVICE_URL",
	platform.MediaDocumentService:        "GATEWAY_UPSTREAM_MEDIA_DOCUMENT_SERVICE_URL",
	platform.NotificationService:         "GATEWAY_UPSTREAM_NOTIFICATION_SERVICE_URL",
	platform.PaymentService:              "GATEWAY_UPSTREAM_PAYMENT_SERVICE_URL",
	platform.MarketplaceService:          "GATEWAY_UPSTREAM_MARKETPLACE_SERVICE_URL",
	platform.IndividualOwnerService:      "GATEWAY_UPSTREAM_INDIVIDUAL_OWNER_SERVICE_URL",
	platform.BusinessOperationsService:   "GATEWAY_UPSTREAM_BUSINESS_OPERATIONS_SERVICE_URL",
	platform.PropertyManagerService:      "GATEWAY_UPSTREAM_PROPERTY_MANAGER_SERVICE_URL",
	platform.ArtisanService:              "GATEWAY_UPSTREAM_ARTISAN_SERVICE_URL",
	platform.ProfessionalServicesService: "GATEWAY_UPSTREAM_PROFESSIONAL_SERVICES_SERVICE_URL",
	platform.AdminService:                "GATEWAY_UPSTREAM_ADMIN_SERVICE_URL",
}

// DefaultRateLimits are conservative edge defaults.
var DefaultRateLimits = map[routing.Category]string{
	routing.CategoryPublic:        "120/1m",
	routing.CategoryAuthenticated: "600/1m",
	routing.CategoryAuthSensitive: "10/1m",
	routing.CategoryUpload:        "30/1m",
	routing.CategoryWebhook:       "1200/1m",
}

// Load reads configuration from l.
func Load(l *sharedcfg.Loader) (*Config, error) {
	c := &Config{}
	c.Environment = l.Environment()
	c.LogLevel = l.String("LOG_LEVEL", "info")
	c.HTTPAddr = l.String("HTTP_ADDR", ":8080")
	c.ShutdownTimeout = l.Duration("SHUTDOWN_TIMEOUT", 20*time.Second)
	c.DrainDelay = l.Duration("SHUTDOWN_DRAIN_DELAY", 0)
	c.TokenAudience = l.String("GATEWAY_TOKEN_AUDIENCE", auth.DefaultAudience)
	c.TokenLeeway = l.Duration("GATEWAY_TOKEN_LEEWAY", jwt.DefaultLeeway)
	c.UpstreamTimeout = l.Duration("GATEWAY_UPSTREAM_TIMEOUT", 30*time.Second)
	c.MaxBodyBytes = l.Int64("GATEWAY_MAX_BODY_BYTES", 1<<20)
	c.RedisAddr = l.Required("REDIS_ADDR")
	c.RedisPassword = l.Secret("REDIS_PASSWORD", false)
	c.RedisDB = l.Int("REDIS_DB", 0)
	c.RedisTLS = l.Bool("REDIS_TLS", false)
	c.CORSAllowedOrigins = l.List("GATEWAY_CORS_ALLOWED_ORIGINS", nil)
	c.HSTS = l.Bool("GATEWAY_HSTS", c.Environment == sharedcfg.EnvProduction || c.Environment == sharedcfg.EnvStaging)
	c.OTLPEndpoint = l.String("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	c.OTLPInsecure = l.Bool("OTEL_EXPORTER_OTLP_INSECURE", false)
	c.SampleRatio = l.Float("OTEL_TRACES_SAMPLER_RATIO", 0.1)
	issuersJSON := l.Secret("GATEWAY_TRUSTED_ISSUERS", true)
	keyID := l.Required("SERVICE_AUTH_KEY_ID")
	privKey := l.Secret("SERVICE_AUTH_PRIVATE_KEY", true)
	proxies := l.List("GATEWAY_TRUSTED_PROXIES", nil)
	webhookSpec := l.List("GATEWAY_WEBHOOK_ROUTES", nil)

	upstreams := map[platform.Service]*url.URL{}
	for svc, env := range upstreamEnv {
		if u := l.URL(env, false); u != nil {
			upstreams[svc] = u
		}
	}
	c.RateLimits = map[routing.Category]ratelimit.Policy{}
	for cat, def := range DefaultRateLimits {
		env := "GATEWAY_RATE_LIMIT_" + strings.ToUpper(strings.ReplaceAll(string(cat), "-", "_"))
		p, err := ratelimit.ParsePolicy(l.String(env, def))
		l.Check(err == nil, "%s is invalid", env)
		if cat == routing.CategoryAuthSensitive {
			p.FailClosed = true
		}
		c.RateLimits[cat] = p
	}
	if err := l.Err(); err != nil {
		return nil, err
	}
	var problems []string

	if c.Environment == sharedcfg.EnvProduction || c.Environment == sharedcfg.EnvStaging {
		for _, o := range c.CORSAllowedOrigins {
			if o == "*" || strings.HasPrefix(o, "http://") {
				problems = append(problems, "GATEWAY_CORS_ALLOWED_ORIGINS must list explicit https origins outside development")
				break
			}
		}
	}
	for _, o := range c.CORSAllowedOrigins {
		if o == "*" {
			continue
		}
		if u, err := url.Parse(o); err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
			problems = append(problems, "GATEWAY_CORS_ALLOWED_ORIGINS contains an invalid origin")
			break
		}
	}
	for _, p := range proxies {
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			problems = append(problems, "GATEWAY_TRUSTED_PROXIES must be CIDRs")
			break
		}
		c.TrustedProxies = append(c.TrustedProxies, n)
	}
	webhooks := map[string]platform.Service{}
	for _, w := range webhookSpec {
		prov, svc, ok := strings.Cut(w, "=")
		if !ok || !platform.KnownService(svc) {
			problems = append(problems, "GATEWAY_WEBHOOK_ROUTES must be provider=service pairs")
			break
		}
		webhooks[prov] = platform.Service(svc)
	}
	if c.MaxBodyBytes <= 0 || c.MaxBodyBytes > 10<<20 {
		problems = append(problems, "GATEWAY_MAX_BODY_BYTES must be within (0, 10MiB]")
	}
	if c.UpstreamTimeout <= 0 || c.UpstreamTimeout > 2*time.Minute {
		problems = append(problems, "GATEWAY_UPSTREAM_TIMEOUT must be within (0, 2m]")
	}
	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}

	issuers, err := auth.ParseTrustedIssuers([]byte(issuersJSON.Reveal()), &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("GATEWAY_TRUSTED_ISSUERS: %w", err)
	}
	c.TrustedIssuers = issuers

	pk, err := security.ParseEd25519PrivateKey(privKey.Reveal())
	if err != nil {
		return nil, errors.New("SERVICE_AUTH_PRIVATE_KEY is not a valid Ed25519 key")
	}
	c.Signer, err = serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.APIGateway, Key: jwt.SigningKey{KID: keyID, PrivateKey: pk}})
	if err != nil {
		return nil, err
	}
	c.Routes, err = routing.Build(upstreams, webhooks)
	if err != nil {
		return nil, err
	}
	return c, nil
}
