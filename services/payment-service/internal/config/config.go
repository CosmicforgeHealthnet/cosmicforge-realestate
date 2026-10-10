package config

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

type Config struct {
	Environment, HTTPAddr, NATSURL, SubjectPrefix, ProviderBaseURL string
	DatabaseURL                                                    security.Secret
	Identity                                                       *serviceauth.Identity
	Providers                                                      []finance.ProviderConfig
	ProviderSecrets                                                map[string]security.Secret
	ShutdownTimeout                                                time.Duration
	EnsureStream                                                   bool
	Worker                                                         bool
}

func Load(l *sharedcfg.Loader, worker bool) (*Config, error) {
	c := &Config{Environment: l.Environment(), HTTPAddr: l.String("HTTP_ADDR", ":8080"), DatabaseURL: l.Secret("DATABASE_URL", true), NATSURL: l.String("NATS_URL", ""), SubjectPrefix: l.String("NATS_SUBJECT_PREFIX", "cf.realestate"), ProviderBaseURL: l.String("PAYSTACK_BASE_URL", "https://api.paystack.co"), ShutdownTimeout: 20 * time.Second, EnsureStream: l.Bool("NATS_ENSURE_STREAM", true), ProviderSecrets: map[string]security.Secret{}}
	if worker {
		c.Worker = true
		c.HTTPAddr = l.String("HTTP_ADDR", ":8081")
		c.NATSURL = l.Required("NATS_URL")
	}
	if l.Bool("PAYMENT_LIVE_CUSTODY_ENABLED", false) || l.Bool("PAYMENT_LIVE_TRANSFERS_ENABLED", false) || l.Bool("PAYMENT_LIVE_COLLECTIONS_ENABLED", false) {
		return nil, errors.New("production financial capabilities are disabled pending certification")
	}
	raw := l.String("PAYMENT_PROVIDER_CONFIG", "[]")
	if json.Unmarshal([]byte(raw), &c.Providers) != nil {
		return nil, errors.New("invalid PAYMENT_PROVIDER_CONFIG")
	}
	for _, p := range c.Providers {
		if p.Mode != "test" || c.Environment == sharedcfg.EnvProduction || p.CredentialsRef == "" || p.Name != "paystack" {
			return nil, errors.New("provider mode/credentials/approval unsupported")
		}
		c.ProviderSecrets[p.CredentialsRef] = l.Secret(p.CredentialsRef, true)
	}
	if e := l.Err(); e != nil {
		return nil, e
	}
	id, e := serviceauth.LoadIdentity(l, platform.PaymentService)
	if e != nil {
		return nil, e
	}
	c.Identity = id
	return c, nil
}
