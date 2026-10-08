// Package config loads verification-service configuration from the
// environment and fails fast on invalid values.
package config

import (
	"errors"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/providers"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Mode selects which process is being configured.
type Mode string

const (
	ModeAPI    Mode = "api"
	ModeWorker Mode = "worker"
)

// Config is the validated configuration.
type Config struct {
	Environment     string
	LogLevel        string
	HTTPAddr        string
	ShutdownTimeout time.Duration

	DatabaseURL security.Secret
	DBMaxConns  int32

	Identity *serviceauth.Identity

	MediaServiceURL string
	MediaTimeout    time.Duration

	NATSURL       string
	SubjectPrefix string
	EnsureStream  bool

	Providers       []string
	DefaultProvider string
	SandboxSecret   security.Secret

	Validity        domain.Validity
	ProviderTimeout time.Duration
	MaxSubmitTries  int
	MaxPollTries    int
	PollInterval    time.Duration

	OutboxPollInterval   time.Duration
	DispatchPollInterval time.Duration
	ExpiryPollInterval   time.Duration

	OTLPEndpoint string
	OTLPInsecure bool
	SampleRatio  float64
}

// Load reads configuration for mode.
func Load(l *sharedcfg.Loader, mode Mode) (*Config, error) {
	c := &Config{}
	c.Environment = l.Environment()
	c.LogLevel = l.String("LOG_LEVEL", "info")
	defAddr := ":8080"
	if mode == ModeWorker {
		defAddr = ":8081"
	}
	c.HTTPAddr = l.String("HTTP_ADDR", defAddr)
	c.ShutdownTimeout = l.Duration("SHUTDOWN_TIMEOUT", 20*time.Second)
	c.DatabaseURL = l.Secret("DATABASE_URL", true)
	c.DBMaxConns = int32(l.Int("DB_MAX_CONNS", 10))
	c.MediaServiceURL = l.String("MEDIA_SERVICE_URL", "")
	c.MediaTimeout = l.Duration("MEDIA_SERVICE_TIMEOUT", 5*time.Second)
	c.SubjectPrefix = l.String("NATS_SUBJECT_PREFIX", events.DefaultSubjectPrefix)
	c.EnsureStream = l.Bool("NATS_ENSURE_STREAM", true)
	c.Providers = l.List("VERIFICATION_PROVIDERS", []string{providers.SandboxName})
	c.DefaultProvider = l.String("VERIFICATION_DEFAULT_PROVIDER", "")
	c.SandboxSecret = l.Secret("SANDBOX_KYC_CALLBACK_SECRET", false)
	c.ProviderTimeout = l.Duration("PROVIDER_TIMEOUT", 20*time.Second)
	c.MaxSubmitTries = l.Int("PROVIDER_MAX_SUBMIT_TRIES", 8)
	c.MaxPollTries = l.Int("PROVIDER_MAX_POLL_TRIES", 60)
	c.PollInterval = l.Duration("PROVIDER_POLL_INTERVAL", 30*time.Second)
	c.OutboxPollInterval = l.Duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond)
	c.DispatchPollInterval = l.Duration("DISPATCH_POLL_INTERVAL", time.Second)
	c.ExpiryPollInterval = l.Duration("EXPIRY_POLL_INTERVAL", time.Minute)
	c.OTLPEndpoint = l.String("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	c.OTLPInsecure = l.Bool("OTEL_EXPORTER_OTLP_INSECURE", false)
	c.SampleRatio = l.Float("OTEL_TRACES_SAMPLER_RATIO", 0.1)
	c.Validity = domain.Validity{}
	for _, t := range domain.Types() {
		c.Validity[t] = l.Duration("VERIFICATION_VALIDITY_"+string(t), domain.DefaultValidity[t])
	}
	if mode == ModeWorker {
		c.NATSURL = l.Required("NATS_URL")
	}
	// Evidence checks call media-document-service from both processes.
	c.MediaServiceURL = l.Required("MEDIA_SERVICE_URL")
	if err := l.Err(); err != nil {
		return nil, err
	}
	var problems []string
	if c.DefaultProvider == "" && len(c.Providers) > 0 {
		c.DefaultProvider = c.Providers[0]
	}
	for _, p := range c.Providers {
		switch p {
		case providers.SandboxName:
			if c.Environment == sharedcfg.EnvProduction {
				problems = append(problems, "VERIFICATION_PROVIDERS must not include the sandbox provider in production")
			}
			if c.SandboxSecret.IsEmpty() {
				problems = append(problems, "SANDBOX_KYC_CALLBACK_SECRET is required when the sandbox provider is enabled")
			}
		default:
			problems = append(problems, "VERIFICATION_PROVIDERS contains an unsupported provider")
		}
	}
	if len(c.Providers) == 0 {
		problems = append(problems, "VERIFICATION_PROVIDERS must enable at least one provider")
	}
	for t, d := range c.Validity {
		if d <= 0 {
			problems = append(problems, "VERIFICATION_VALIDITY_"+string(t)+" must be positive")
		}
	}
	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	id, err := serviceauth.LoadIdentity(l, platform.VerificationService)
	if err != nil {
		return nil, err
	}
	c.Identity = id
	return c, nil
}
