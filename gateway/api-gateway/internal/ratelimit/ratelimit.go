// Package ratelimit implements distributed, Redis-backed rate limiting using
// the Generic Cell Rate Algorithm (GCRA). State lives in Redis so limits hold
// across all gateway replicas; Redis server time is used so replica clock
// skew does not matter.
//
// When Redis is unavailable, fail-closed categories (auth-sensitive) reject
// with 503, and other categories degrade to a conservative per-process
// limiter so the edge keeps serving without becoming unlimited.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// Policy is a limit: Rate requests per Period with Burst.
type Policy struct {
	Rate       int
	Period     time.Duration
	Burst      int
	FailClosed bool
}

// ParsePolicy parses "120/1m" or "10/1m:burst=5".
func ParsePolicy(s string) (Policy, error) {
	main, opts, _ := strings.Cut(s, ":")
	n, per, ok := strings.Cut(main, "/")
	if !ok {
		return Policy{}, fmt.Errorf("ratelimit: invalid policy %q", s)
	}
	r, err := strconv.Atoi(n)
	if err != nil || r <= 0 {
		return Policy{}, fmt.Errorf("ratelimit: invalid rate in %q", s)
	}
	d, err := time.ParseDuration(per)
	if err != nil || d <= 0 {
		return Policy{}, fmt.Errorf("ratelimit: invalid period in %q", s)
	}
	p := Policy{Rate: r, Period: d, Burst: r}
	if opts != "" {
		k, v, _ := strings.Cut(opts, "=")
		b, err := strconv.Atoi(v)
		if k != "burst" || err != nil || b <= 0 {
			return Policy{}, fmt.Errorf("ratelimit: invalid option in %q", s)
		}
		p.Burst = b
	}
	return p, nil
}

// emission is the interval between tokens.
func (p Policy) emission() time.Duration { return p.Period / time.Duration(p.Rate) }

// Decision is the outcome of a check.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
	// Degraded is true when Redis was unavailable and the local fallback decided.
	Degraded bool
}

// ErrUnavailable is returned for fail-closed policies when Redis is down.
var ErrUnavailable = errors.New("ratelimit: backend unavailable")

// Limiter checks keys against policies.
type Limiter struct {
	rdb    redis.UniversalClient
	prefix string
	log    *slog.Logger

	mu    sync.Mutex
	local map[string]*localEntry
}

type localEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// New returns a limiter. prefix namespaces Redis keys.
func New(rdb redis.UniversalClient, prefix string, log *slog.Logger) *Limiter {
	if log == nil {
		log = slog.Default()
	}
	return &Limiter{rdb: rdb, prefix: prefix, log: log, local: map[string]*localEntry{}}
}

// gcra returns {allowed(0/1), retry_after_ms}. Uses Redis TIME so all replicas
// share one clock.
var gcra = redis.NewScript(`
local key = KEYS[1]
local burst = tonumber(ARGV[1])
local emission = tonumber(ARGV[2])
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local tat = tonumber(redis.call('GET', key) or now)
if tat < now then tat = now end
local new_tat = tat + emission
local allow_at = new_tat - burst * emission
if now < allow_at then
  return {0, allow_at - now}
end
redis.call('SET', key, new_tat, 'PX', math.max(1, math.ceil(new_tat - now)))
return {1, 0}
`)

// Allow consumes one request for key under policy p.
func (l *Limiter) Allow(ctx context.Context, category, key string, p Policy) (Decision, error) {
	rkey := l.prefix + ":" + category + ":" + key
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	res, err := gcra.Run(ctx, l.rdb, []string{rkey}, p.Burst, p.emission().Milliseconds()).Int64Slice()
	if err == nil && len(res) == 2 {
		return Decision{Allowed: res[0] == 1, RetryAfter: time.Duration(res[1]) * time.Millisecond}, nil
	}
	if err == nil {
		err = errors.New("ratelimit: unexpected script result")
	}
	l.log.WarnContext(ctx, "rate limiter backend unavailable", "category", category, "fail_closed", p.FailClosed, "error", err.Error())
	if p.FailClosed {
		return Decision{}, ErrUnavailable
	}
	return l.allowLocal(rkey, p), nil
}

func (l *Limiter) allowLocal(key string, p Policy) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.local) > 100_000 {
		for k, e := range l.local {
			if now.Sub(e.seen) > 10*time.Minute {
				delete(l.local, k)
			}
		}
	}
	e, ok := l.local[key]
	if !ok {
		e = &localEntry{lim: rate.NewLimiter(rate.Every(p.emission()), p.Burst)}
		l.local[key] = e
	}
	e.seen = now
	r := e.lim.ReserveN(now, 1)
	if !r.OK() {
		return Decision{Allowed: false, RetryAfter: p.Period, Degraded: true}
	}
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return Decision{Allowed: false, RetryAfter: d, Degraded: true}
	}
	return Decision{Allowed: true, Degraded: true}
}

// Ping is a readiness check.
func (l *Limiter) Ping(ctx context.Context) error { return l.rdb.Ping(ctx).Err() }
