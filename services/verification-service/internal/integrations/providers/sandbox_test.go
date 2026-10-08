package providers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
)

func TestSandboxOutcomesAndIdempotency(t *testing.T) {
	s := NewSandbox("secret")
	ctx := context.Background()
	sub := func(ref, mode string) (SubmitResult, error) {
		return s.Submit(ctx, SubmitRequest{ClientReference: ref, Type: domain.TypeIdentity, Metadata: map[string]string{"sandbox_outcome": mode}})
	}
	r, err := sub("a", "")
	if err != nil || r.Outcome != OutcomeVerified {
		t.Fatalf("%+v %v", r, err)
	}
	r2, _ := sub("a", "REJECTED") // same client reference: idempotent
	if r2.ProviderReference != r.ProviderReference || r2.Outcome != OutcomeVerified {
		t.Fatal("submit must be idempotent per client reference")
	}
	if _, err := sub("t", "TIMEOUT"); Classify(err) != KindUnknown {
		t.Fatalf("timeout must be unknown outcome: %v", err)
	}
	st, _ := s.ReadStatus(ctx, "t")
	if !st.Found || st.Outcome != OutcomeVerified {
		t.Fatal("read-back must find the processed submission")
	}
	if st, _ := s.ReadStatus(ctx, "never"); st.Found {
		t.Fatal("unknown reference must be not found")
	}
	if _, err := sub("p", "PERMANENT"); Classify(err) != KindPermanent {
		t.Fatal("permanent")
	}
	if _, err := sub("r", "RETRYABLE"); Classify(err) != KindRetryable {
		t.Fatal("retryable")
	}
	if r, err := sub("r", "RETRYABLE"); err != nil || r.Outcome != OutcomeVerified {
		t.Fatal("retry should succeed")
	}
	pending, _ := sub("q", "PENDING")
	s.Complete("q", OutcomeRejected, "X")
	if fr, _ := s.FetchResult(ctx, pending.ProviderReference); fr.Outcome != OutcomeRejected {
		t.Fatal("fetch result")
	}
}

func TestSandboxCallbackVerification(t *testing.T) {
	s := NewSandbox("secret")
	body := []byte(`{"event_id":"e1","client_reference":"c1","outcome":"VERIFIED"}`)
	h := http.Header{}
	h.Set(SandboxSignatureHeader, s.SignCallback(body))
	ev, err := s.VerifyCallback(context.Background(), h, body)
	if err != nil || ev.EventID != "e1" || ev.Outcome != OutcomeVerified {
		t.Fatalf("%+v %v", ev, err)
	}
	h.Set(SandboxSignatureHeader, NewSandbox("other").SignCallback(body))
	if _, err := s.VerifyCallback(context.Background(), h, body); !errors.Is(err, ErrInvalidCallback) {
		t.Fatal("wrong secret accepted")
	}
	bad := []byte(`{"event_id":"e1","client_reference":"c1","outcome":"MAYBE"}`)
	h.Set(SandboxSignatureHeader, s.SignCallback(bad))
	if _, err := s.VerifyCallback(context.Background(), h, bad); !errors.Is(err, ErrInvalidCallback) {
		t.Fatal("invalid outcome accepted")
	}
	if _, err := NewSandbox("").VerifyCallback(context.Background(), h, body); !errors.Is(err, ErrInvalidCallback) {
		t.Fatal("empty secret must reject")
	}
}

func TestRegistry(t *testing.T) {
	s := NewSandbox("x")
	if _, err := NewRegistry("missing", s); err == nil {
		t.Fatal("default must exist")
	}
	r, _ := NewRegistry(SandboxName, s)
	if p, err := r.Select("", domain.TypeIdentity); err != nil || p.Name() != SandboxName {
		t.Fatal("default select")
	}
	if _, err := r.Select("acme", domain.TypeIdentity); err == nil {
		t.Fatal("unknown provider")
	}
}
