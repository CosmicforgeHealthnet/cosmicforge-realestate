package config

import (
	"encoding/json"
	"math"
	"net"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func validAddress(s string) bool {
	_, p, err := net.SplitHostPort(s)
	n, parse := strconv.Atoi(p)
	return err == nil && parse == nil && n > 0 && n <= 65535
}

func validateRuntime(l *sharedcfg.Loader, c *Config, mode Mode) {
	l.Check(mode == ModeAPI || mode == ModeWorker, "invalid Owner process mode")
	l.OneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error")
	l.Check(validAddress(c.HTTPAddr), "HTTP_ADDR must be a host:port with port 1..65535")
	_, err := pgxpool.ParseConfig(c.DatabaseURL.Reveal())
	l.Check(err == nil, "DATABASE_URL must be a valid PostgreSQL connection configuration")
	for name, value := range map[string]time.Duration{
		"SHUTDOWN_TIMEOUT": c.ShutdownTimeout, "DOWNSTREAM_TIMEOUT": c.DownstreamTimeout,
		"NOTIFICATION_SERVICE_TIMEOUT": c.NotificationTimeout, "PROPERTY_IMPORT_LEASE": c.ImportLease,
		"DOMAIN_WORKER_INTERVAL": c.WorkerInterval, "OUTBOX_POLL_INTERVAL": c.OutboxPollInterval,
		"DISPATCH_POLL_INTERVAL": c.DispatchPollInterval, "CLOSURE_POLL_INTERVAL": c.ClosureInterval,
		"OWNER_ENDED_SESSION_RETENTION": c.SessionRetention, "OWNER_LOGIN_LOCKOUT": c.Policy.LockoutDuration,
		"OWNER_CODE_TTL": c.Policy.CodeTTL, "OWNER_CODE_SEND_WINDOW": c.Policy.CodeSendWindow,
		"OWNER_MFA_LOGIN_TTL": c.Policy.MFALoginTTL,
	} {
		l.Check(value > 0, "%s must be positive", name)
	}
	l.Check(c.ShutdownTimeout <= 25*time.Second, "SHUTDOWN_TIMEOUT must fit the 30s deployment grace period (maximum 25s)")
	l.Check(!math.IsNaN(c.SampleRatio) && !math.IsInf(c.SampleRatio, 0) && c.SampleRatio >= 0 && c.SampleRatio <= 1, "OTEL_TRACES_SAMPLER_RATIO must be between 0 and 1")
	if c.OTLPEndpoint != "" {
		l.Check(validAddress(c.OTLPEndpoint), "OTEL_EXPORTER_OTLP_ENDPOINT must be collector host:port")
	}
	l.Check(!strings.ContainsAny(c.SubjectPrefix, " \t\r\n*>") && !strings.HasPrefix(c.SubjectPrefix, ".") && !strings.HasSuffix(c.SubjectPrefix, ".") && !strings.Contains(c.SubjectPrefix, ".."), "NATS_SUBJECT_PREFIX must be a literal subject prefix")
	if mode == ModeWorker {
		for _, s := range strings.Split(c.NATSURL, ",") {
			u, err := url.Parse(strings.TrimSpace(s))
			valid := err == nil && u.Hostname() != "" && (u.Scheme == "nats" || u.Scheme == "tls") && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
			if valid && u.Port() != "" {
				valid = validAddress(u.Host)
			}
			l.Check(valid, "NATS_URL must contain valid nats:// or tls:// broker URLs")
		}
	}
	for name, value := range map[string]string{"VERIFICATION_SERVICE_URL": c.VerificationServiceURL, "MEDIA_SERVICE_URL": c.MediaServiceURL, "NOTIFICATION_SERVICE_URL": c.NotificationServiceURL, "PAYMENT_WALLET_SERVICE_URL": c.PaymentWalletServiceURL} {
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		l.Check(err == nil && u.Hostname() != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == "" && u.Fragment == "", "%s must be an HTTP(S) service base URL without credentials/query/fragment", name)
	}
	// These adapters are absent. A selector or endpoint must not suggest activation.
	for _, name := range []string{"CFMEET_PROVIDER", "CFMEET_SERVICE_URL", "CFMEET_BASE_URL", "MAPS_PROVIDER", "MAPS_API_URL", "MAPS_BASE_URL", "ESIGN_SERVICE_URL", "ESIGN_BASE_URL", "SMART_ACCESS_SERVICE_URL", "SMART_ACCESS_BASE_URL", "EXTERNAL_LISTING_PROVIDERS", "MARKETPLACE_SERVICE_URL", "MANAGER_SERVICE_URL", "ARTISAN_SERVICE_URL", "PROFESSIONAL_SERVICE_URL"} {
		l.Check(l.String(name, "") == "", "%s must be empty: no runtime adapter is implemented", name)
	}
	l.Check(l.Duration("SERVICE_AUTH_TOKEN_TTL", 2*time.Minute) > 0, "SERVICE_AUTH_TOKEN_TTL must be positive")
	var peers []serviceauth.RegistrySpec
	err = json.Unmarshal([]byte(l.Secret("SERVICE_AUTH_TRUSTED_KEYS", true).Reveal()), &peers)
	l.Check(err == nil && len(peers) > 0, "SERVICE_AUTH_TRUSTED_KEYS must be a nonempty valid peer registry")
	seen := map[string]bool{}
	for _, peer := range peers {
		for _, key := range peer.Keys {
			pair := string(peer.Service) + "/" + key.KID
			l.Check(!seen[pair], "SERVICE_AUTH_TRUSTED_KEYS contains duplicate peer key IDs")
			seen[pair] = true
		}
	}
	for _, name := range []string{"DATABASE_URL", "NATS_URL", "SERVICE_AUTH_PRIVATE_KEY", "SERVICE_AUTH_TRUSTED_KEYS", "OWNER_ACCESS_TOKEN_PRIVATE_KEY", "OWNER_AUTH_PRIVATE_KEY", "OWNER_ACCESS_TOKEN_RETIRED_KEYS", "OWNER_CODE_PEPPER", "OWNER_MFA_ENCRYPTION_KEY", "OWNER_MFA_RETIRED_KEYS"} {
		path := l.String(name+"_FILE", "")
		if path == "" {
			continue
		}
		l.Check(l.String(name, "") == "", "%s and its _FILE definition must not coexist", name)
		info, err := os.Stat(path)
		l.Check(err == nil && info != nil && info.Mode().IsRegular(), "%s_FILE must be a readable regular file", name)
		if err == nil && runtime.GOOS != "windows" && (c.Environment == sharedcfg.EnvProduction || c.Environment == sharedcfg.EnvStaging) {
			l.Check(info.Mode().Perm()&0007 == 0 && info.Mode().Perm()&0022 == 0, "%s_FILE must not be world-accessible or group-writable", name)
		}
	}
}
