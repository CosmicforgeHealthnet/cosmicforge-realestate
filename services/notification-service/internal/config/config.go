// Package config loads notification-service configuration and fails fast.
package config

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Mode selects the process.
type Mode string

const (
	ModeAPI    Mode = "api"
	ModeWorker Mode = "worker"
)

// Provider names. Selection is configuration-driven per channel
// (NOTIFY_PUSH_PROVIDER, NOTIFY_EMAIL_PROVIDER, NOTIFY_SMS_PROVIDER); the
// application maps each name to an adapter factory, so adding a provider
// never touches notification logic.
//
//   - push:  fcm (selected provider), dev, disabled
//   - email: dev, disabled, smtp (optional adapter; no production email provider selected yet)
//   - sms:   dev, disabled, twilio (optional Twilio-compatible adapter; no production SMS provider selected yet)
//
// Defaults: dev (in-memory capture, no credentials) in development and test;
// in staging and production push defaults to fcm and email/SMS to disabled
// (channels without a provider are reported as CHANNEL_NOT_CONFIGURED).
const (
	ProviderDisabled = "disabled"
	ProviderDev      = "dev" // in-memory capture; refused in production
	ProviderFCM      = "fcm"
	ProviderSMTP     = "smtp"
	ProviderTwilio   = "twilio"
)

// FCMConfig holds Firebase Cloud Messaging settings (read only when selected).
type FCMConfig struct {
	ProjectID       string
	BaseURL         string
	CredentialsJSON security.Secret
}

// SMTPConfig holds optional SMTP adapter settings (read only when selected).
type SMTPConfig struct {
	Host       string
	Port       int
	Username   string
	Password   security.Secret
	From       string
	FromName   string
	RequireTLS bool
}

// TwilioConfig holds optional Twilio-compatible adapter settings (read only
// when selected).
type TwilioConfig struct {
	BaseURL             string
	AccountSID          string
	AuthToken           security.Secret
	From                string
	MessagingServiceSID string
}

// Config is the validated configuration.
type Config struct {
	Environment     string
	LogLevel        string
	HTTPAddr        string
	ShutdownTimeout time.Duration

	DatabaseURL security.Secret
	DBMaxConns  int32
	Identity    *serviceauth.Identity

	PushProvider  string
	EmailProvider string
	// DevCaptureFile appends dev-provider messages to a JSON-lines file
	// (local/E2E tooling; requires a dev provider, never production).
	DevCaptureFile string
	SMSProvider    string

	// Adapter settings; nil unless that adapter is selected.
	FCM    *FCMConfig
	SMTP   *SMTPConfig
	Twilio *TwilioConfig

	DispatchLease    time.Duration
	SendTimeout      time.Duration
	RetryBase        time.Duration
	RetryMax         time.Duration
	ContentRetention time.Duration
	DefaultLocale    string

	NATSURL       string
	SubjectPrefix string
	EnsureStream  bool

	OutboxPollInterval   time.Duration
	DispatchPollInterval time.Duration
	DispatchBatch        int
	PurgeInterval        time.Duration

	OTLPEndpoint string
	OTLPInsecure bool
	SampleRatio  float64
}

// Load reads configuration for mode.
func Load(l *sharedcfg.Loader, mode Mode) (*Config, error) {
	c := &Config{}
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

	devDefault := c.Environment == sharedcfg.EnvDevelopment || c.Environment == sharedcfg.EnvTest
	pushDef, otherDef := ProviderFCM, ProviderDisabled
	if devDefault {
		pushDef, otherDef = ProviderDev, ProviderDev
	}
	c.PushProvider = l.OneOf("NOTIFY_PUSH_PROVIDER", pushDef, ProviderFCM, ProviderDev, ProviderDisabled)
	c.EmailProvider = l.OneOf("NOTIFY_EMAIL_PROVIDER", otherDef, ProviderDev, ProviderDisabled, ProviderSMTP)
	c.DevCaptureFile = l.String("NOTIFY_DEV_CAPTURE_FILE", "")
	c.SMSProvider = l.OneOf("NOTIFY_SMS_PROVIDER", otherDef, ProviderDev, ProviderDisabled, ProviderTwilio)
	if c.PushProvider == ProviderFCM {
		c.FCM = &FCMConfig{ProjectID: l.Required("FCM_PROJECT_ID"), CredentialsJSON: l.Secret("FCM_CREDENTIALS_JSON", true),
			BaseURL: l.String("FCM_BASE_URL", "https://fcm.googleapis.com")}
	}
	if c.EmailProvider == ProviderSMTP {
		c.SMTP = &SMTPConfig{Host: l.Required("SMTP_HOST"), Port: l.Int("SMTP_PORT", 587), Username: l.String("SMTP_USERNAME", ""),
			Password: l.Secret("SMTP_PASSWORD", false), From: l.Required("SMTP_FROM"),
			FromName: l.String("SMTP_FROM_NAME", "CosmicForge Real Estate"), RequireTLS: l.Bool("SMTP_REQUIRE_TLS", true)}
	}
	if c.SMSProvider == ProviderTwilio {
		c.Twilio = &TwilioConfig{BaseURL: l.String("TWILIO_BASE_URL", "https://api.twilio.com"), AccountSID: l.Required("TWILIO_ACCOUNT_SID"),
			AuthToken: l.Secret("TWILIO_AUTH_TOKEN", true), From: l.String("TWILIO_FROM", ""),
			MessagingServiceSID: l.String("TWILIO_MESSAGING_SERVICE_SID", "")}
	}

	c.DispatchLease = l.Duration("NOTIFY_DISPATCH_LEASE", 2*time.Minute)
	c.SendTimeout = l.Duration("NOTIFY_SEND_TIMEOUT", 15*time.Second)
	c.RetryBase = l.Duration("NOTIFY_RETRY_BASE", 30*time.Second)
	c.RetryMax = l.Duration("NOTIFY_RETRY_MAX", time.Hour)
	c.ContentRetention = l.Duration("NOTIFY_CONTENT_RETENTION", 7*24*time.Hour)
	c.DefaultLocale = l.String("NOTIFY_DEFAULT_LOCALE", "en")

	c.SubjectPrefix = l.String("NATS_SUBJECT_PREFIX", events.DefaultSubjectPrefix)
	c.EnsureStream = l.Bool("NATS_ENSURE_STREAM", true)
	c.OutboxPollInterval = l.Duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond)
	c.DispatchPollInterval = l.Duration("DISPATCH_POLL_INTERVAL", time.Second)
	c.DispatchBatch = l.Int("DISPATCH_BATCH", 20)
	c.PurgeInterval = l.Duration("PURGE_INTERVAL", time.Hour)
	c.OTLPEndpoint = l.String("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	c.OTLPInsecure = l.Bool("OTEL_EXPORTER_OTLP_INSECURE", false)
	c.SampleRatio = l.Float("OTEL_TRACES_SAMPLER_RATIO", 0.1)
	if mode == ModeWorker {
		c.NATSURL = l.Required("NATS_URL")
	}
	if err := l.Err(); err != nil {
		return nil, err
	}
	var problems []string
	if c.Environment == sharedcfg.EnvProduction {
		for name, p := range map[string]string{"NOTIFY_PUSH_PROVIDER": c.PushProvider, "NOTIFY_EMAIL_PROVIDER": c.EmailProvider, "NOTIFY_SMS_PROVIDER": c.SMSProvider} {
			if p == ProviderDev {
				problems = append(problems, name+"=dev is not allowed in production")
			}
		}
		if c.SMTP != nil && !c.SMTP.RequireTLS {
			problems = append(problems, "SMTP_REQUIRE_TLS=false is not allowed in production")
		}
	}
	// The capture file holds message bodies (one-time codes): local and
	// end-to-end tooling only, never staging or production.
	if c.DevCaptureFile != "" {
		switch {
		case !devDefault:
			problems = append(problems, "NOTIFY_DEV_CAPTURE_FILE is only allowed with APP_ENV=development or test (never in "+c.Environment+")")
		case !filepath.IsAbs(c.DevCaptureFile):
			problems = append(problems, "NOTIFY_DEV_CAPTURE_FILE must be an absolute path")
		case c.PushProvider != ProviderDev && c.EmailProvider != ProviderDev && c.SMSProvider != ProviderDev:
			problems = append(problems, "NOTIFY_DEV_CAPTURE_FILE requires a dev provider")
		}
	}
	if c.Twilio != nil && c.Twilio.From == "" && c.Twilio.MessagingServiceSID == "" {
		problems = append(problems, "TWILIO_FROM or TWILIO_MESSAGING_SERVICE_SID is required")
	}
	if c.SendTimeout <= 0 || c.SendTimeout*2 > c.DispatchLease {
		problems = append(problems, "NOTIFY_SEND_TIMEOUT must be positive and at most half of NOTIFY_DISPATCH_LEASE")
	}
	if c.RetryBase <= 0 || c.RetryMax < c.RetryBase {
		problems = append(problems, "NOTIFY_RETRY_BASE must be positive and <= NOTIFY_RETRY_MAX")
	}
	if c.DispatchBatch <= 0 || c.DispatchBatch > 500 {
		problems = append(problems, "DISPATCH_BATCH must be within (0, 500]")
	}
	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	id, err := serviceauth.LoadIdentity(l, platform.NotificationService)
	if err != nil {
		return nil, err
	}
	c.Identity = id
	return c, nil
}
