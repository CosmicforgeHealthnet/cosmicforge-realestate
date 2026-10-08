package sessions

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func setup() (*Manager, *MemoryStore, *clock) {
	c := &clock{t: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}
	st := NewMemoryStore()
	return NewManager(st, Config{Now: c.now}), st, c
}

func TestStartAndRotate(t *testing.T) {
	m, st, c := setup()
	ctx := context.Background()
	iss, err := m.Start(ctx, ids.New(), Device{Platform: "android"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(iss.RawToken, TokenPrefix) || len(iss.RawToken) < 40 {
		t.Fatalf("token %q", iss.RawToken)
	}
	if iss.Session.ExpiresAt.Sub(iss.Session.CreatedAt) != 30*24*time.Hour {
		t.Fatal("default lifetime must be 30 days")
	}
	// Stored hash, never raw.
	if _, ok := st.byHash[iss.RawToken]; ok {
		t.Fatal("raw token stored")
	}
	c.t = c.t.Add(time.Hour)
	next, err := m.Refresh(ctx, iss.RawToken)
	if err != nil {
		t.Fatal(err)
	}
	if next.RawToken == iss.RawToken || next.Session.ID != iss.Session.ID {
		t.Fatal("rotation must issue a new token in the same family")
	}
	if _, err := m.Refresh(ctx, next.RawToken); err != nil {
		t.Fatalf("new token should work: %v", err)
	}
}

func TestReuseRevokesFamily(t *testing.T) {
	m, st, _ := setup()
	ctx := context.Background()
	iss, _ := m.Start(ctx, ids.New(), Device{})
	next, _ := m.Refresh(ctx, iss.RawToken)
	// Attacker replays the old token.
	if _, err := m.Refresh(ctx, iss.RawToken); !errors.Is(err, ErrTokenReuse) {
		t.Fatalf("expected reuse, got %v", err)
	}
	s, _ := st.Session(iss.Session.ID)
	if s.RevokedAt == nil || s.RevokeReason != ReasonTokenReuse {
		t.Fatal("session not revoked on reuse")
	}
	// Legitimate holder's newest token is now dead too.
	if _, err := m.Refresh(ctx, next.RawToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("expected revoked, got %v", err)
	}
}

func TestConcurrentRefreshSameTokenOnlyOneWins(t *testing.T) {
	m, st, _ := setup()
	ctx := context.Background()
	iss, _ := m.Start(ctx, ids.New(), Device{})
	var ok, reuse atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := m.Refresh(ctx, iss.RawToken)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrTokenReuse), errors.Is(err, ErrSessionRevoked):
				reuse.Add(1)
			default:
				t.Errorf("unexpected %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() > 1 {
		t.Fatalf("%d concurrent refreshes succeeded", ok.Load())
	}
	if s, _ := st.Session(iss.Session.ID); ok.Load()+reuse.Load() != 16 || (reuse.Load() > 0 && s.RevokedAt == nil) {
		t.Fatal("concurrent reuse must revoke the session")
	}
}

func TestExpiryAndInvalid(t *testing.T) {
	m, _, c := setup()
	ctx := context.Background()
	iss, _ := m.Start(ctx, ids.New(), Device{})
	if _, err := m.Refresh(ctx, "cfrt_unknown"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("unknown token")
	}
	if _, err := m.Refresh(ctx, "not-a-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("malformed token")
	}
	c.t = c.t.Add(31 * 24 * time.Hour)
	if _, err := m.Refresh(ctx, iss.RawToken); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected expiry, got %v", err)
	}
}

func TestConfigurableLifetimeAndIdleTTL(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := NewManager(NewMemoryStore(), Config{MaxSessionLifetime: 7 * 24 * time.Hour, TokenIdleTTL: 24 * time.Hour, Now: c.now})
	iss, _ := m.Start(context.Background(), ids.New(), Device{})
	if iss.Session.ExpiresAt.Sub(iss.Session.CreatedAt) != 7*24*time.Hour {
		t.Fatal("lifetime not applied")
	}
	c.t = c.t.Add(25 * time.Hour)
	if _, err := m.Refresh(context.Background(), iss.RawToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("idle-expired token should be invalid, got %v", err)
	}
}

func TestRevocation(t *testing.T) {
	m, _, _ := setup()
	ctx := context.Background()
	subject := ids.New()
	a, _ := m.Start(ctx, subject, Device{DeviceID: "a"})
	b, _ := m.Start(ctx, subject, Device{DeviceID: "b"})
	if err := m.Revoke(ctx, a.Session.ID, ReasonLogout); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(ctx, a.RawToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatal("logout not enforced")
	}
	if _, err := m.Refresh(ctx, b.RawToken); err != nil {
		t.Fatal("other session should be unaffected")
	}
	if err := m.RevokeAll(ctx, subject, ReasonPasswordChange); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(ctx, b.RawToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatal("revoke-all not enforced")
	}
	if _, err := m.Start(ctx, ids.Nil, Device{}); !errors.Is(err, ErrInvalidSubject) {
		t.Fatal("nil subject accepted")
	}
}
