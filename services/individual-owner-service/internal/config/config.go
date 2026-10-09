// Package config loads individual-owner-service configuration from the
// environment and fails fast on invalid values. Secrets are read through
// the shared loader (VAR or VAR_FILE) and never logged.
package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
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

	TokenIssuer      string
	TokenAudience    string
	AccessTokenTTL   time.Duration
	SigningKey       jwt.SigningKey
	RetiredKeys      map[string]string // kid -> public key (rotation)
	SessionLifetime  time.Duration
	RefreshIdleTTL   time.Duration
	StepUpWindow     time.Duration
	PasswordWorkers  int
	CodePepper       security.Secret
	MFAKeyID         string
	MFAKey           []byte
	MFARetiredKeys   map[string][]byte
	TOTPIssuer       string
	Policy           usecases.Policy
	SessionRetention time.Duration

	NotificationServiceURL string
	NotificationTimeout    time.Duration

	// Sections 6–13 integrations; empty = not configured (features answer
	// 503 FEATURE_UNAVAILABLE).
	VerificationServiceURL string
	MediaServiceURL        string
	DownstreamTimeout      time.Duration
	ImportLease            time.Duration
	WorkerInterval         time.Duration

	NATSURL       string
	SubjectPrefix string
	EnsureStream  bool

	OutboxPollInterval   time.Duration
	DispatchPollInterval time.Duration
	ClosureInterval      time.Duration

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

	c.TokenIssuer = l.String("OWNER_TOKEN_ISSUER", string(platform.IndividualOwnerService))
	c.TokenAudience = l.String("OWNER_TOKEN_AUDIENCE", sharedauth.DefaultAudience)
	c.AccessTokenTTL = l.Duration("OWNER_ACCESS_TOKEN_TTL", sharedauth.DefaultAccessTTL)
	kid := l.Required("OWNER_ACCESS_TOKEN_KEY_ID")
	// OWNER_AUTH_PRIVATE_KEY[_FILE] is the specification's name (§19); the
	// original OWNER_ACCESS_TOKEN_PRIVATE_KEY remains accepted.
	priv := l.Secret("OWNER_AUTH_PRIVATE_KEY", false)
	if priv.Reveal() == "" {
		priv = l.Secret("OWNER_ACCESS_TOKEN_PRIVATE_KEY", true)
	}
	retired := l.String("OWNER_ACCESS_TOKEN_RETIRED_KEYS", "")
	c.SessionLifetime = l.Duration("OWNER_SESSION_MAX_LIFETIME", 30*24*time.Hour)
	c.RefreshIdleTTL = l.Duration("OWNER_REFRESH_TOKEN_IDLE_TTL", 30*24*time.Hour)
	c.StepUpWindow = l.Duration("OWNER_STEP_UP_WINDOW", 5*time.Minute)
	c.PasswordWorkers = l.Int("OWNER_PASSWORD_HASH_CONCURRENCY", 4)
	c.CodePepper = l.Secret("OWNER_CODE_PEPPER", true)
	c.MFAKeyID = l.Required("OWNER_MFA_ENCRYPTION_KEY_ID")
	mfaKey := l.Secret("OWNER_MFA_ENCRYPTION_KEY", true)
	mfaRetired := l.Secret("OWNER_MFA_RETIRED_KEYS", false)
	c.TOTPIssuer = l.String("OWNER_TOTP_ISSUER", "CosmicForge Owner")
	c.SessionRetention = l.Duration("OWNER_ENDED_SESSION_RETENTION", 90*24*time.Hour)

	p := usecases.DefaultPolicy()
	p.Version = l.String("POLICY_CONFIG_VERSION", l.String("OWNER_SECURITY_POLICY_VERSION", p.Version))
	p.MaxLoginFailures = l.Int("OWNER_LOGIN_MAX_FAILURES", p.MaxLoginFailures)
	p.LockoutDuration = l.Duration("OWNER_LOGIN_LOCKOUT", p.LockoutDuration)
	p.CodeTTL = l.Duration("OWNER_CODE_TTL", p.CodeTTL)
	p.CodeMaxAttempts = l.Int("OWNER_CODE_MAX_ATTEMPTS", p.CodeMaxAttempts)
	p.CodeSendLimit = l.Int("OWNER_CODE_SEND_LIMIT", p.CodeSendLimit)
	p.CodeSendWindow = l.Duration("OWNER_CODE_SEND_WINDOW", p.CodeSendWindow)
	p.MFALoginTTL = l.Duration("OWNER_MFA_LOGIN_TTL", p.MFALoginTTL)
	p.MFAFallbackMethods = l.List("OWNER_MFA_FALLBACK_METHODS", p.MFAFallbackMethods)
	p.ClosureCoolingOff = l.Duration("OWNER_CLOSURE_COOLING_OFF", p.ClosureCoolingOff)
	p.Activation = usecases.ActivationRule(l.OneOf("OWNER_ACTIVATION_REQUIRES", string(p.Activation),
		string(usecases.ActivateOnEmail), string(usecases.ActivateOnPhone), string(usecases.ActivateOnEmailOrPhone), string(usecases.ActivateOnEmailAndPhn)))
	p.PasswordPolicy.MinLength = l.Int("OWNER_PASSWORD_MIN_LENGTH", p.PasswordPolicy.MinLength)
	d := &p.Domain
	d.IdentityRequiredFor = l.List("OWNER_IDENTITY_REQUIRED_FOR", d.IdentityRequiredFor)
	d.CoOwnerConsentThreshold = l.OneOf("OWNER_COOWNER_CONSENT_THRESHOLD", d.CoOwnerConsentThreshold, "ALL", "MAJORITY_SHARE")
	d.ConsentTTL = l.Duration("OWNER_CONSENT_TTL", d.ConsentTTL)
	d.PayoutCoolingOff = l.Duration("OWNER_PAYOUT_COOLING_OFF", d.PayoutCoolingOff)
	d.TransferInviteTTL = l.Duration("OWNER_TRANSFER_INVITE_TTL", d.TransferInviteTTL)
	c.Policy = p
	c.VerificationServiceURL = l.String("VERIFICATION_SERVICE_URL", "")
	c.MediaServiceURL = l.String("MEDIA_SERVICE_URL", "")
	c.DownstreamTimeout = l.Duration("DOWNSTREAM_TIMEOUT", 5*time.Second)
	c.ImportLease = l.Duration("PROPERTY_IMPORT_LEASE", 2*time.Minute)
	c.WorkerInterval = l.Duration("DOMAIN_WORKER_INTERVAL", 30*time.Second)
	paymentURL := l.String("PAYMENT_SERVICE_URL", "")
	esignProvider := l.String("ESIGN_PROVIDER", "")
	calendarProvider := l.String("EXTERNAL_CALENDAR_PROVIDER", "")
	partnerProviders := map[string]string{"FINANCING_PROVIDER": l.String("FINANCING_PROVIDER", ""), "INSURANCE_PROVIDER": l.String("INSURANCE_PROVIDER", ""),
		"SMART_ACCESS_PROVIDER": l.String("SMART_ACCESS_PROVIDER", "")}

	oauthProviders := l.List("OWNER_OAUTH_PROVIDERS", nil)
	c.NotificationServiceURL = l.Required("NOTIFICATION_SERVICE_URL")
	c.NotificationTimeout = l.Duration("NOTIFICATION_SERVICE_TIMEOUT", 5*time.Second)
	c.SubjectPrefix = l.String("NATS_SUBJECT_PREFIX", events.DefaultSubjectPrefix)
	c.EnsureStream = l.Bool("NATS_ENSURE_STREAM", true)
	c.OutboxPollInterval = l.Duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond)
	c.DispatchPollInterval = l.Duration("DISPATCH_POLL_INTERVAL", time.Second)
	c.ClosureInterval = l.Duration("CLOSURE_POLL_INTERVAL", time.Minute)
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
	pk, err := security.ParseEd25519PrivateKey(priv.Reveal())
	if err != nil {
		problems = append(problems, "OWNER_ACCESS_TOKEN_PRIVATE_KEY is not a valid Ed25519 key")
	}
	c.SigningKey = jwt.SigningKey{KID: kid, PrivateKey: pk}
	c.RetiredKeys = map[string]string{}
	if retired != "" {
		var specs []sharedauth.PublicKeySpec
		if err := json.Unmarshal([]byte(retired), &specs); err != nil {
			problems = append(problems, "OWNER_ACCESS_TOKEN_RETIRED_KEYS must be a JSON array of {kid, public_key}")
		}
		for _, s := range specs {
			if _, err := security.ParseEd25519PublicKey(s.PublicKey); err != nil || s.KID == "" || s.KID == kid {
				problems = append(problems, "OWNER_ACCESS_TOKEN_RETIRED_KEYS contains an invalid key")
				continue
			}
			c.RetiredKeys[s.KID] = s.PublicKey
		}
	}
	if c.AccessTokenTTL <= 0 || c.AccessTokenTTL > sharedauth.MaxAccessTTL {
		problems = append(problems, fmt.Sprintf("OWNER_ACCESS_TOKEN_TTL must be in (0, %s]", sharedauth.MaxAccessTTL))
	}
	if len(c.CodePepper.Reveal()) < credentials.MinPepperBytes {
		problems = append(problems, "OWNER_CODE_PEPPER must be at least 32 bytes")
	}
	if c.MFAKey, err = decodeKey(mfaKey.Reveal()); err != nil {
		problems = append(problems, "OWNER_MFA_ENCRYPTION_KEY must be 32 bytes (base64)")
	}
	c.MFARetiredKeys = map[string][]byte{}
	if v := mfaRetired.Reveal(); v != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			problems = append(problems, "OWNER_MFA_RETIRED_KEYS must be a JSON object of key_id -> base64 key")
		}
		for id, k := range m {
			b, err := decodeKey(k)
			if err != nil || id == c.MFAKeyID {
				problems = append(problems, "OWNER_MFA_RETIRED_KEYS contains an invalid key")
				continue
			}
			c.MFARetiredKeys[id] = b
		}
	}
	for _, m := range p.MFAFallbackMethods {
		if m != usecases.MFASMS && m != usecases.MFAEmail {
			problems = append(problems, "OWNER_MFA_FALLBACK_METHODS accepts only SMS and EMAIL")
		}
	}
	if paymentURL != "" {
		// payment-service exposes no implemented contract yet; refusing the
		// setting prevents believing payouts are live.
		problems = append(problems, "PAYMENT_SERVICE_URL must be empty: payment-service has no implemented contract (Appendix D)")
	}
	if esignProvider != "" {
		// No electronic-signature provider adapter is implemented; owners
		// sign OFFLINE until one is selected and built (spec §12).
		problems = append(problems, "ESIGN_PROVIDER must be empty: no e-sign provider adapter is implemented")
	}
	if calendarProvider != "" {
		// No external calendar (iCal/channel manager) adapter is implemented;
		// short-let availability is managed on the platform only (spec §12).
		problems = append(problems, "EXTERNAL_CALENDAR_PROVIDER must be empty: no calendar-sync adapter is implemented")
	}
	for _, k := range []string{"FINANCING_PROVIDER", "INSURANCE_PROVIDER", "SMART_ACCESS_PROVIDER"} {
		if partnerProviders[k] != "" {
			// No partner/device adapter is implemented (spec §12); the
			// dependent features answer 503 FEATURE_UNAVAILABLE.
			problems = append(problems, k+" must be empty: no adapter is implemented")
		}
	}
	for _, g := range d.IdentityRequiredFor {
		switch g {
		case usecases.GateListingPublish, usecases.GatePayout, usecases.GateSale, usecases.GateTransfer:
		default:
			problems = append(problems, "OWNER_IDENTITY_REQUIRED_FOR accepts LISTING_PUBLISH, PAYOUT, SALE, OWNERSHIP_TRANSFER")
		}
	}
	if d.ConsentTTL <= 0 || d.PayoutCoolingOff < 0 || d.TransferInviteTTL <= 0 {
		problems = append(problems, "consent TTL, payout cooling-off and transfer invitation TTL must be positive")
	}
	if len(oauthProviders) > 0 {
		// No OAuth/OIDC adapter is implemented or certified (Appendix D).
		problems = append(problems, "OWNER_OAUTH_PROVIDERS must be empty: no OAuth provider adapter is available")
	}
	if p.MaxLoginFailures < 1 || p.CodeMaxAttempts < 1 || p.CodeMaxAttempts > 10 || p.CodeSendLimit < 1 || p.ClosureCoolingOff <= 0 {
		problems = append(problems, "security policy values are out of range")
	}
	if c.SessionLifetime <= 0 || c.RefreshIdleTTL <= 0 || c.StepUpWindow <= 0 || c.StepUpWindow > 30*time.Minute {
		problems = append(problems, "session lifetime, refresh idle TTL and step-up window (<=30m) must be positive")
	}
	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	id, err := serviceauth.LoadIdentity(l, platform.IndividualOwnerService)
	if err != nil {
		return nil, err
	}
	c.Identity = id
	return c, nil
}

func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("invalid key")
}
