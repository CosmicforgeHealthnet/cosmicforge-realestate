// Package config loads component configuration from environment variables
// (and *_FILE indirection for secrets mounted by the deployment platform).
//
// Loading collects every problem and reports them together so a misconfigured
// deployment fails fast at startup with a complete list. Error messages name
// variables only; values are never echoed.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// Environment names.
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Loader reads typed values and accumulates errors.
type Loader struct {
	lookup func(string) (string, bool)
	errs   []string
}

// New returns a Loader over the process environment.
func New() *Loader { return &Loader{lookup: os.LookupEnv} }

// FromMap returns a Loader over a static map (tests).
func FromMap(m map[string]string) *Loader {
	return &Loader{lookup: func(k string) (string, bool) { v, ok := m[k]; return v, ok }}
}

func (l *Loader) fail(format string, args ...any) {
	l.errs = append(l.errs, fmt.Sprintf(format, args...))
}

func (l *Loader) get(key string) (string, bool) {
	v, ok := l.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

// String returns key or def.
func (l *Loader) String(key, def string) string {
	if v, ok := l.get(key); ok {
		return v
	}
	return def
}

// Required returns key or records an error.
func (l *Loader) Required(key string) string {
	v, ok := l.get(key)
	if !ok {
		l.fail("%s is required", key)
	}
	return v
}

// OneOf returns key (or def) and validates it against allowed values.
func (l *Loader) OneOf(key, def string, allowed ...string) string {
	v := l.String(key, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.fail("%s must be one of %s", key, strings.Join(allowed, ", "))
	return v
}

// Environment returns APP_ENV validated against the known environments.
func (l *Loader) Environment() string {
	return l.OneOf("APP_ENV", EnvDevelopment, EnvDevelopment, EnvTest, EnvStaging, EnvProduction)
}

// Int returns key as an int or def.
func (l *Loader) Int(key string, def int) int {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.fail("%s must be an integer", key)
		return def
	}
	return n
}

// Int64 returns key as an int64 or def.
func (l *Loader) Int64(key string, def int64) int64 {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.fail("%s must be an integer", key)
		return def
	}
	return n
}

// Float returns key as a float64 or def (for non-monetary ratios only).
func (l *Loader) Float(key string, def float64) float64 {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		l.fail("%s must be a number", key)
		return def
	}
	return f
}

// Bool returns key as a bool or def.
func (l *Loader) Bool(key string, def bool) bool {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail("%s must be a boolean", key)
		return def
	}
	return b
}

// Duration returns key as a time.Duration or def.
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail("%s must be a duration (e.g. 30s)", key)
		return def
	}
	return d
}

// List returns a comma-separated list (empty entries dropped).
func (l *Loader) List(key string, def []string) []string {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// URL returns key parsed as an absolute URL; required when required is true.
func (l *Loader) URL(key string, required bool) *url.URL {
	v, ok := l.get(key)
	if !ok {
		if required {
			l.fail("%s is required", key)
		}
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Host == "" {
		l.fail("%s must be an absolute URL", key)
		return nil
	}
	return u
}

// Secret reads KEY, or the file named by KEY_FILE. The value is wrapped so it
// is never printed.
func (l *Loader) Secret(key string, required bool) security.Secret {
	if v, ok := l.get(key); ok {
		return security.NewSecret(v)
	}
	if path, ok := l.get(key + "_FILE"); ok {
		b, err := os.ReadFile(path)
		if err != nil {
			l.fail("%s_FILE could not be read", key)
			return security.Secret{}
		}
		return security.NewSecret(strings.TrimSpace(string(b)))
	}
	if required {
		l.fail("%s (or %s_FILE) is required", key, key)
	}
	return security.Secret{}
}

// Check records err (if non-nil) as a configuration problem.
func (l *Loader) Check(cond bool, format string, args ...any) {
	if !cond {
		l.fail(format, args...)
	}
}

// Err returns all accumulated problems or nil.
func (l *Loader) Err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return errors.New("invalid configuration: " + strings.Join(l.errs, "; "))
}
