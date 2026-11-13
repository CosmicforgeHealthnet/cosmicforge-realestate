package cfmeet

import (
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
)

// Load returns the authenticated CF Meet client when CFMEET_API_BASE_URL is
// configured, otherwise Unavailable (contexts stay PENDING_AUTHORITY). The API
// credential and the acting service principal are issued by CF Meet Platform
// Access; they are external activation inputs, never committed values.
// Account-to-principal mapping is not configured here: until CF Meet issues
// principals for Real Estate accounts, participants are reported as pending.
func Load(l *config.Loader) (API, error) {
	base := l.String("CFMEET_API_BASE_URL", "")
	if base == "" {
		return Unavailable{}, nil
	}
	env := l.Environment()
	cfg := Config{
		BaseURL:         base,
		Credential:      l.Required("CFMEET_API_CREDENTIAL"),
		ActingPrincipal: l.Required("CFMEET_ACTING_PRINCIPAL_ID"),
		ContextClass:    l.OneOf("CFMEET_CONTEXT_CLASS", "ENTERPRISE", "ENTERPRISE", "SUPPORT", "TRANSACTION", "GROUP"),
		AllowInsecure:   env == config.EnvDevelopment || env == config.EnvTest,
	}
	if err := l.Err(); err != nil {
		return nil, err
	}
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	client.http.Timeout = 8 * time.Second
	return client, nil
}
