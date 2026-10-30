// Package config validates the configuration shared by API, worker and migrate.
package config

import (
	"errors"
	"strings"
	"time"

	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

type Mode string

const (
	ModeAPI    Mode = "api"
	ModeWorker Mode = "worker"
)

type Config struct {
	Environment, LogLevel, HTTPAddr string
	ShutdownTimeout                 time.Duration
	DatabaseURL                     security.Secret
	DBMaxConns                      int32
	NATSURL, SubjectPrefix          string
	NATSCredsFile, NATSToken        string
	OutboxPollInterval              time.Duration
	OTLPEndpoint                    string
	OTLPInsecure                    bool
	SampleRatio                     float64
}

func Load(l *sharedcfg.Loader, mode Mode) (*Config, error) {
	c := &Config{Environment: l.Environment(), LogLevel: l.String("LOG_LEVEL", "info"), HTTPAddr: l.String("HTTP_ADDR", ":8080"),
		ShutdownTimeout: l.Duration("SHUTDOWN_TIMEOUT", 20*time.Second), DatabaseURL: l.Secret("DATABASE_URL", true),
		DBMaxConns: int32(l.Int("DB_MAX_CONNS", 10)), SubjectPrefix: l.String("NATS_SUBJECT_PREFIX", events.DefaultSubjectPrefix),
		NATSCredsFile: l.String("NATS_CREDS_FILE", ""), NATSToken: l.Secret("NATS_TOKEN", false).Reveal(),
		OutboxPollInterval: l.Duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond), OTLPEndpoint: l.String("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OTLPInsecure: l.Bool("OTEL_EXPORTER_OTLP_INSECURE", false), SampleRatio: l.Float("OTEL_TRACES_SAMPLER_RATIO", .1)}
	if mode == ModeWorker {
		c.NATSURL = l.Required("NATS_URL")
	}
	if err := l.Err(); err != nil {
		return nil, err
	}
	var problems []string
	if err := ValidateOwnedDatabase(c.DatabaseURL.Reveal()); err != nil {
		problems = append(problems, "DATABASE_URL must connect to the service-owned business_operations_db")
	}
	if c.DBMaxConns < 1 || c.DBMaxConns > 100 {
		problems = append(problems, "DB_MAX_CONNS must be between 1 and 100")
	}
	if c.ShutdownTimeout <= 0 || c.ShutdownTimeout > 2*time.Minute {
		problems = append(problems, "SHUTDOWN_TIMEOUT must be within (0, 2m]")
	}
	if c.OutboxPollInterval < 50*time.Millisecond || c.OutboxPollInterval > time.Minute {
		problems = append(problems, "OUTBOX_POLL_INTERVAL must be within [50ms, 1m]")
	}
	if !validSubjectPrefix(c.SubjectPrefix) {
		problems = append(problems, "NATS_SUBJECT_PREFIX is invalid")
	}
	if c.SampleRatio < 0 || c.SampleRatio > 1 {
		problems = append(problems, "OTEL_TRACES_SAMPLER_RATIO must be between 0 and 1")
	}
	if mode != ModeAPI && mode != ModeWorker {
		problems = append(problems, "unknown process mode")
	}
	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return c, nil
}

func validSubjectPrefix(prefix string) bool {
	if len(prefix) < 1 || len(prefix) > 64 {
		return false
	}
	for i, r := range prefix {
		if i == 0 && (r < 'a' || r > 'z') {
			return false
		}
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
