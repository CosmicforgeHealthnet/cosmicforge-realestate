package totp

import (
	"bytes"
	"testing"
	"time"
)

// RFC 6238 Appendix B SHA1 vectors (8-digit values truncated to 6).
func TestRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := Code(secret, ts/30); got != want {
			t.Fatalf("t=%d got %s want %s", ts, got, want)
		}
	}
	now := time.Unix(1234567890, 0)
	if step, err := Verify(secret, "005924", now); err != nil || step != Step(now) {
		t.Fatalf("verify %d %v", step, err)
	}
	if _, err := Verify(secret, "000000", now); err == nil {
		t.Fatal("wrong code accepted")
	}
}

func TestSealerBindsAAD(t *testing.T) {
	s, err := NewSealer("k1", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ct := s.Seal([]byte("seed"), []byte("acct-1"))
	if pt, err := s.Open("k1", ct, []byte("acct-1")); err != nil || string(pt) != "seed" {
		t.Fatalf("open %q %v", pt, err)
	}
	if _, err := s.Open("k1", ct, []byte("acct-2")); err == nil {
		t.Fatal("aad not bound")
	}
	if _, err := NewSealer("k1", []byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
}
