// Package config loads marketplace-service configuration.
package config

import (
	"time"

	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Config is the runtime configuration of the api process.
type Config struct {
	Environment     string
	LogLevel        string
	HTTPAddr        string
	ShutdownTimeout time.Duration
	DatabaseURL     security.Secret
	DBMaxConns      int32
	ConnectionTTL   time.Duration
	OTLPEndpoint    string
	OTLPInsecure    bool
	SampleRatio     float64
	Identity        *serviceauth.Identity
}

// Load reads and validates configuration.
func Load(l *sharedcfg.Loader) (*Config, error) {
	c := &Config{}
	c.Environment = l.Environment()
	c.LogLevel = l.String("LOG_LEVEL", "info")
	c.HTTPAddr = l.String("HTTP_ADDR", ":8080")
	c.ShutdownTimeout = l.Duration("SHUTDOWN_TIMEOUT", 20*time.Second)
	c.DatabaseURL = l.Secret("DATABASE_URL", true)
	c.DBMaxConns = int32(l.Int("DB_MAX_CONNS", 10))
	c.ConnectionTTL = l.Duration("SUPPLY_CONNECTION_VALIDATION_TTL", 24*time.Hour)
	c.OTLPEndpoint = l.String("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	c.OTLPInsecure = l.Bool("OTEL_EXPORTER_OTLP_INSECURE", false)
	c.SampleRatio = l.Float("OTEL_TRACES_SAMPLER_RATIO", 0.1)
	if err := l.Err(); err != nil {
		return nil, err
	}
	id, err := serviceauth.LoadIdentity(l, platform.MarketplaceService)
	if err != nil {
		return nil, err
	}
	c.Identity = id
	return c, nil
}
