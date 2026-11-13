// Package domainkit is the common runtime of a narrow CosmicForge user-domain
// service (property manager, artisan, professional services, admin and the
// Marketplace customer surface). It composes the existing platform primitives
// — service identity (serviceauth), user access tokens (auth), refresh
// sessions (sessions), password hashing (passwords), idempotency, audit,
// transactional outbox and JetStream — without introducing a new identity
// system. Every service keeps its own database: the account, session,
// idempotency, audit, outbox and processed-event tables are created by that
// service's own migrations (see the SQL in each service's 00001 migration).
package domainkit

import (
	"errors"
	"fmt"
	"strings"
	"time"

	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Config is the runtime configuration shared by a domain service's processes.
type Config struct {
	Service platform.Service
	Domain  platform.Domain
	// Area is the event namespace (event types and stream subjects).
	Area               string
	Environment        string
	LogLevel           string
	HTTPAddr           string
	ShutdownTimeout    time.Duration
	DatabaseURL        security.Secret
	DBMaxConns         int32
	NATSURL            string
	SubjectPrefix      string
	OutboxPollInterval time.Duration
	OTLPEndpoint       string
	OTLPInsecure       bool
	SampleRatio        float64
	Identity           *serviceauth.Identity
	// User access tokens issued by this domain's account authority.
	TokenKey   jwt.SigningKey
	AccessTTL  time.Duration
	SessionTTL time.Duration
	// BootstrapRoles maps lower-case emails to roles granted at registration.
	// Only used by services whose roles are not self-assignable (admin).
	BootstrapRoles map[string][]string
	// BusinessURL is the business-operations-service internal base URL.
	BusinessURL string
	// Extra is service-specific configuration read through Loader.
	Loader *sharedcfg.Loader
}

// Areas maps a domain to its event namespace.
var Areas = map[platform.Domain]string{
	platform.DomainPropertyManager:      "property_manager",
	platform.DomainArtisan:              "artisan",
	platform.DomainProfessionalServices: "professional",
	platform.DomainAdmin:                "admin",
	platform.DomainMarketplace:          "marketplace",
}

// Mode selects the process.
type Mode string

const (
	ModeAPI    Mode = "api"
	ModeWorker Mode = "worker"
)

// Load reads the standard configuration for svc.
func Load(l *sharedcfg.Loader, svc platform.Service, mode Mode) (*Config, error) {
	domain, ok := platform.DomainForService(svc)
	if !ok {
		return nil, fmt.Errorf("domainkit: %s is not a user-domain service", svc)
	}
	c := &Config{Service: svc, Domain: domain, Area: Areas[domain], Loader: l}
	c.Environment = l.Environment()
	c.LogLevel = l.String("LOG_LEVEL", "info")
	def := ":8080"
	if mode == ModeWorker {
		def = ":8081"
	}
	c.HTTPAddr = l.String("HTTP_ADDR", def)
	c.ShutdownTimeout = l.Duration("SHUTDOWN_TIMEOUT", 20*time.Second)
	c.DatabaseURL = l.Secret("DATABASE_URL", true)
	c.DBMaxConns = int32(l.Int("DB_MAX_CONNS", 10))
	c.SubjectPrefix = l.String("NATS_SUBJECT_PREFIX", events.DefaultSubjectPrefix)
	c.OutboxPollInterval = l.Duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond)
	c.OTLPEndpoint = l.String("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	c.OTLPInsecure = l.Bool("OTEL_EXPORTER_OTLP_INSECURE", false)
	c.SampleRatio = l.Float("OTEL_TRACES_SAMPLER_RATIO", 0.1)
	c.AccessTTL = l.Duration("USER_ACCESS_TOKEN_TTL", 15*time.Minute)
	c.SessionTTL = l.Duration("USER_SESSION_TTL", 30*24*time.Hour)
	c.BusinessURL = l.String("BUSINESS_OPERATIONS_SERVICE_URL", "")
	if mode == ModeWorker {
		c.NATSURL = l.Required("NATS_URL")
	}
	var kid string
	var key security.Secret
	if mode == ModeAPI {
		kid = l.Required("USER_TOKEN_KEY_ID")
		key = l.Secret("USER_TOKEN_PRIVATE_KEY", true)
	}
	c.BootstrapRoles = map[string][]string{}
	for _, entry := range l.List("BOOTSTRAP_ROLE_GRANTS", nil) {
		// email=ROLE|ROLE
		email, roles, ok := strings.Cut(entry, "=")
		if !ok || !strings.Contains(email, "@") || roles == "" {
			return nil, errors.New("BOOTSTRAP_ROLE_GRANTS entries must be email=ROLE|ROLE")
		}
		c.BootstrapRoles[strings.ToLower(strings.TrimSpace(email))] = strings.Split(roles, "|")
	}
	if err := l.Err(); err != nil {
		return nil, err
	}
	var problems []string
	if c.DBMaxConns < 1 || c.DBMaxConns > 100 {
		problems = append(problems, "DB_MAX_CONNS must be between 1 and 100")
	}
	if c.AccessTTL <= 0 || c.AccessTTL > time.Hour {
		problems = append(problems, "USER_ACCESS_TOKEN_TTL must be within (0, 1h]")
	}
	if c.SessionTTL < time.Hour || c.SessionTTL > 90*24*time.Hour {
		problems = append(problems, "USER_SESSION_TTL must be within [1h, 90d]")
	}
	if mode == ModeAPI {
		priv, err := security.ParseEd25519PrivateKey(key.Reveal())
		if err != nil {
			problems = append(problems, "USER_TOKEN_PRIVATE_KEY must be an Ed25519 key")
		} else {
			c.TokenKey = jwt.SigningKey{KID: kid, PrivateKey: priv}
		}
	}
	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	id, err := serviceauth.LoadIdentity(l, svc)
	if err != nil {
		return nil, err
	}
	c.Identity = id
	return c, nil
}
