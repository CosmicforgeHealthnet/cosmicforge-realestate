package ratelimit

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestParsePolicy(t *testing.T) {
	p, err := ParsePolicy("10/1m:burst=5")
	if err != nil || p.Rate != 10 || p.Period != time.Minute || p.Burst != 5 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{"", "10", "x/1m", "10/x", "0/1m", "10/1m:burst=0", "10/1m:foo=1"} {
		if _, err := ParsePolicy(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func redisClient(t *testing.T) redis.UniversalClient {
	if addr := os.Getenv("CF_TEST_REDIS_ADDR"); addr != "" {
		c := redis.NewClient(&redis.Options{Addr: addr})
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	if os.Getenv(testkit.EnvRequireIntegration) == "1" {
		t.Fatal("CF_TEST_REDIS_ADDR required")
	}
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// Two limiter instances (two gateway replicas) share one budget.
func TestDistributedBudgetAcrossReplicas(t *testing.T) {
	rdb := redisClient(t)
	prefix := "test:" + ids.New().String()
	a, b := New(rdb, prefix, logging.Discard()), New(rdb, prefix, logging.Discard())
	p := Policy{Rate: 10, Period: time.Minute, Burst: 10}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		lim := a
		if i%2 == 1 {
			lim = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := lim.Allow(context.Background(), "authenticated", "actor:x", p)
			if err != nil {
				t.Error(err)
			}
			if d.Allowed {
				allowed.Add(1)
			}
			if d.Degraded {
				t.Error("unexpected degraded decision")
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("allowed %d, want exactly burst 10 across replicas", allowed.Load())
	}
	d, _ := a.Allow(context.Background(), "authenticated", "actor:x", p)
	if d.Allowed || d.RetryAfter <= 0 || d.RetryAfter > 7*time.Second {
		t.Fatalf("retry-after %v", d.RetryAfter)
	}
}

func TestRedisUnavailable(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	defer rdb.Close()
	l := New(rdb, "x", logging.Discard())
	if _, err := l.Allow(context.Background(), "auth-sensitive", "ip:1", Policy{Rate: 5, Period: time.Minute, Burst: 5, FailClosed: true}); err != ErrUnavailable {
		t.Fatalf("fail-closed policy: %v", err)
	}
	p := Policy{Rate: 2, Period: time.Minute, Burst: 2}
	for i := 0; i < 2; i++ {
		if d, err := l.Allow(context.Background(), "public", "ip:1", p); err != nil || !d.Allowed || !d.Degraded {
			t.Fatalf("fallback %d: %+v %v", i, d, err)
		}
	}
	if d, _ := l.Allow(context.Background(), "public", "ip:1", p); d.Allowed {
		t.Fatal("local fallback must still limit")
	}
	if l.Ping(context.Background()) == nil {
		t.Fatal("ping should fail")
	}
}
