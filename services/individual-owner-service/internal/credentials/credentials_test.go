package credentials

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B test vectors (SHA1, 8 digits) truncated to 6 digits.
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037"}
	var tp TOTP
	for unix, want := range vectors {
		if got := tp.Code(secret, Step(time.Unix(unix, 0))); got != want {
			t.Errorf("t=%d got %s want %s", unix, got, want)
		}
	}
}

func TestTOTPVerifyWindow(t *testing.T) {
	tp := TOTP{Issuer: "CosmicForge Owner"}
	secret := tp.NewSecret()
	now := time.Unix(1_800_000_000, 0)
	cur := Step(now)
	for d, ok := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		step, err := tp.Verify(secret, tp.Code(secret, cur+d), now)
		if ok != (err == nil) || (ok && step != cur+d) {
			t.Errorf("delta %d: step=%d err=%v", d, step, err)
		}
	}
	if _, err := tp.Verify(secret, "12345", now); err == nil {
		t.Fatal("short code accepted")
	}
	if u := tp.URI(secret, "a@b.co"); !strings.HasPrefix(u, "otpauth://totp/CosmicForge%20Owner:a@b.co?") {
		t.Fatalf("uri %s", u)
	}
}

func TestHasherDomainSeparation(t *testing.T) {
	h, err := NewHasher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if h.Hash("a", "bc") == h.Hash("ab", "c") {
		t.Fatal("length prefixing broken")
	}
	if !Equal(h.Hash("x"), h.Hash("x")) || Equal(h.Hash("x"), h.Hash("y")) {
		t.Fatal("equality broken")
	}
	if _, err := NewHasher([]byte("short")); err == nil {
		t.Fatal("short pepper accepted")
	}
}

func TestCodesAndRecovery(t *testing.T) {
	for i := 0; i < 200; i++ {
		if c := NumericCode(6); len(c) != 6 || strings.Trim(c, "0123456789") != "" {
			t.Fatalf("bad code %q", c)
		}
	}
	rc := RecoveryCode()
	if len(rc) != 11 || rc[5] != '-' {
		t.Fatalf("bad recovery code %q", rc)
	}
	if NormalizeRecoveryCode(" "+strings.ToUpper(rc)+" ") != strings.ReplaceAll(rc, "-", "") {
		t.Fatal("normalize")
	}
}

func TestSealerRoundTripRotationAndAAD(t *testing.T) {
	k1, k2 := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	s1, _ := NewSealer("k1", k1, nil)
	ct := s1.Seal([]byte("seed"), []byte("acct"))
	if _, err := s1.Open("k1", ct, []byte("other")); err == nil {
		t.Fatal("AAD not bound")
	}
	s2, err := NewSealer("k2", k2, map[string][]byte{"k1": k1})
	if err != nil {
		t.Fatal(err)
	}
	pt, err := s2.Open("k1", ct, []byte("acct"))
	if err != nil || string(pt) != "seed" {
		t.Fatalf("rotation open: %v", err)
	}
	if _, err := s2.Open("k9", ct, []byte("acct")); err == nil {
		t.Fatal("unknown key id accepted")
	}
	if _, err := NewSealer("k", []byte("short"), nil); err == nil {
		t.Fatal("short key accepted")
	}
}
