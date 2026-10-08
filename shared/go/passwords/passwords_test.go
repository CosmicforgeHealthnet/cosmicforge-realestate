package passwords

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// testParams keeps tests fast while exercising the same code paths.
var testParams = Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func hasher(t *testing.T, p Params) *Hasher {
	h, err := NewHasher(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHashVerify(t *testing.T) {
	h := hasher(t, testParams)
	ctx := context.Background()
	enc, err := h.Hash(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("encoding %s", enc)
	}
	if strings.Contains(enc, "correct horse") {
		t.Fatal("raw password in hash")
	}
	rehash, err := h.Verify(ctx, "correct horse battery staple", enc)
	if err != nil || rehash {
		t.Fatalf("verify: %v rehash=%v", err, rehash)
	}
	if _, err := h.Verify(ctx, "wrong", enc); !errors.Is(err, ErrMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

func TestUniqueSalt(t *testing.T) {
	h := hasher(t, testParams)
	a, _ := h.Hash(context.Background(), "same-password")
	b, _ := h.Hash(context.Background(), "same-password")
	if a == b {
		t.Fatal("identical hashes: salt not random")
	}
}

func TestRehashDetection(t *testing.T) {
	old := hasher(t, testParams)
	enc, _ := old.Hash(context.Background(), "pw-123456789")
	stronger := testParams
	stronger.Iterations = 2
	cur := hasher(t, stronger)
	if !cur.NeedsRehash(enc) {
		t.Fatal("expected rehash for older params")
	}
	rehash, err := cur.Verify(context.Background(), "pw-123456789", enc)
	if err != nil || !rehash {
		t.Fatalf("verify old hash with new hasher: %v %v", err, rehash)
	}
	if cur.NeedsRehash("garbage") != true {
		t.Fatal("garbage must need rehash")
	}
}

func TestDefaultParamsAccepted(t *testing.T) {
	if testing.Short() {
		t.Skip("default params are slow")
	}
	h := hasher(t, DefaultParams)
	enc, err := h.Hash(context.Background(), "pw-production-strength")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Verify(context.Background(), "pw-production-strength", enc); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidEncodings(t *testing.T) {
	h := hasher(t, testParams)
	for _, bad := range []string{
		"", "plain", "$argon2i$v=19$m=8192,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=18$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=8192,t=1,p=1$!!!$a2V5",
	} {
		if _, err := h.Verify(context.Background(), "x", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestPolicy(t *testing.T) {
	if DefaultPolicy.Validate("short") != ErrTooShort {
		t.Fatal("short")
	}
	if DefaultPolicy.Validate(strings.Repeat("a", 200)) != ErrTooLong {
		t.Fatal("long")
	}
	if DefaultPolicy.Validate("\xff\xfe invalid utf8 pw") != ErrInvalidEncoding {
		t.Fatal("utf8")
	}
	if DefaultPolicy.Validate("pässwörd-ünïcode") != nil {
		t.Fatal("unicode should be allowed")
	}
	h := hasher(t, testParams)
	if _, err := h.Hash(context.Background(), strings.Repeat("a", 5000)); !errors.Is(err, ErrTooLong) {
		t.Fatal("oversized input must be refused before hashing")
	}
}

func TestBoundedConcurrencyRespectsContext(t *testing.T) {
	h, _ := NewHasher(testParams, 1)
	h.sem <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "pw-123456789"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
}

func TestWeakParamsRejected(t *testing.T) {
	if _, err := NewHasher(Params{MemoryKiB: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}, 1); err == nil {
		t.Fatal("weak params accepted")
	}
}

func TestPasswordNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	h := hasher(t, testParams)
	_, err := h.Verify(context.Background(), "super-secret-pw", "$argon2id$bad")
	l.Error("verify failed", "err", err)
	if strings.Contains(buf.String(), "super-secret-pw") {
		t.Fatal("password leaked via error")
	}
}
