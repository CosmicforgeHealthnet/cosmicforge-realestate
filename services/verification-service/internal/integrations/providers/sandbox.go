package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// SandboxName is the development/test provider. It is rejected by
// configuration validation in production.
const SandboxName = "sandbox-kyc"

// SandboxSignatureHeader carries the HMAC-SHA256 (hex) of the callback body.
const SandboxSignatureHeader = "X-Sandbox-Signature"

// Sandbox is a deterministic in-process provider for development and tests.
// The outcome is chosen by Metadata["sandbox_outcome"]:
//
//	VERIFIED (default) | REJECTED | INCONCLUSIVE   synchronous result
//	PENDING                                        async: callback or Complete()
//	TIMEOUT                                        processed but the response is lost (read-back resolves VERIFIED)
//	RETRYABLE                                      first call rejected with a retryable error
//	PERMANENT                                      request rejected permanently
//
// Callbacks are authenticated with an HMAC secret.
type Sandbox struct {
	secret []byte
	mu     sync.Mutex
	subs   map[string]*sandboxSub
	calls  map[string]int
}

type sandboxSub struct {
	ref     string
	outcome Outcome
	reason  string
	eventID string
}

// NewSandbox returns a sandbox with a callback secret.
func NewSandbox(callbackSecret string) *Sandbox {
	return &Sandbox{secret: []byte(callbackSecret), subs: map[string]*sandboxSub{}, calls: map[string]int{}}
}

func (s *Sandbox) Name() string              { return SandboxName }
func (s *Sandbox) Supports(domain.Type) bool { return true }

func (s *Sandbox) Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error) {
	if err := ctx.Err(); err != nil {
		return SubmitResult{}, &Error{Kind: KindRetryable, Code: "CANCELLED", Err: err}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[req.ClientReference]++
	if sub, ok := s.subs[req.ClientReference]; ok { // idempotent per client reference
		return SubmitResult{ProviderReference: sub.ref, Outcome: sub.outcome, ReasonCode: sub.reason, EventID: sub.eventID}, nil
	}
	mode := strings.ToUpper(req.Metadata["sandbox_outcome"])
	ref := "sbx_" + security.SHA256Hex([]byte(req.ClientReference))[:24]
	sub := &sandboxSub{ref: ref, eventID: "sbx_evt_" + ref + "_1"}
	switch mode {
	case "", "VERIFIED":
		sub.outcome = OutcomeVerified
	case "REJECTED":
		sub.outcome, sub.reason = OutcomeRejected, "DOCUMENT_MISMATCH"
	case "INCONCLUSIVE":
		sub.outcome, sub.reason = OutcomeInconclusive, "LOW_CONFIDENCE"
	case "PENDING":
		sub.outcome = OutcomePending
	case "TIMEOUT":
		sub.outcome = OutcomeVerified
		s.subs[req.ClientReference] = sub
		return SubmitResult{}, &Error{Kind: KindUnknown, Code: "TIMEOUT", Err: context.DeadlineExceeded}
	case "RETRYABLE":
		if s.calls[req.ClientReference] == 1 {
			return SubmitResult{}, &Error{Kind: KindRetryable, Code: "RATE_LIMITED", Err: errors.New("sandbox throttled")}
		}
		sub.outcome = OutcomeVerified
	case "PERMANENT":
		return SubmitResult{}, &Error{Kind: KindPermanent, Code: "UNSUPPORTED_DOCUMENT", Err: errors.New("sandbox rejected request")}
	default:
		return SubmitResult{}, &Error{Kind: KindPermanent, Code: "INVALID_SANDBOX_OUTCOME", Err: errors.New("unknown sandbox_outcome")}
	}
	s.subs[req.ClientReference] = sub
	return SubmitResult{ProviderReference: sub.ref, Outcome: sub.outcome, ReasonCode: sub.reason, EventID: sub.eventID}, nil
}

func (s *Sandbox) ReadStatus(ctx context.Context, clientReference string) (StatusResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[clientReference]
	if !ok {
		return StatusResult{Found: false}, nil
	}
	return StatusResult{Found: true, ProviderReference: sub.ref, Outcome: sub.outcome, ReasonCode: sub.reason, EventID: sub.eventID}, nil
}

func (s *Sandbox) FetchResult(ctx context.Context, providerReference string) (StatusResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range s.subs {
		if sub.ref == providerReference {
			return StatusResult{Found: true, ProviderReference: sub.ref, Outcome: sub.outcome, ReasonCode: sub.reason, EventID: sub.eventID}, nil
		}
	}
	return StatusResult{Found: false}, nil
}

// Complete simulates the provider finishing an async (PENDING) check.
func (s *Sandbox) Complete(clientReference string, o Outcome, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub, ok := s.subs[clientReference]; ok {
		sub.outcome, sub.reason = o, reason
		sub.eventID = sub.ref + "_final"
	}
}

// Calls returns how many Submit calls a client reference received.
func (s *Sandbox) Calls(clientReference string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[clientReference]
}

type sandboxCallback struct {
	EventID           string `json:"event_id"`
	ClientReference   string `json:"client_reference"`
	ProviderReference string `json:"provider_reference"`
	Outcome           string `json:"outcome"`
	ReasonCode        string `json:"reason_code"`
}

// SignCallback returns the signature header value for body (tests/tools).
func (s *Sandbox) SignCallback(body []byte) string { return security.HMACSHA256Hex(s.secret, body) }

func (s *Sandbox) VerifyCallback(_ context.Context, h http.Header, body []byte) (CallbackEvent, error) {
	if len(s.secret) == 0 || !security.VerifyHMACSHA256Hex(s.secret, body, h.Get(SandboxSignatureHeader)) {
		return CallbackEvent{}, ErrInvalidCallback
	}
	var cb sandboxCallback
	if err := json.Unmarshal(body, &cb); err != nil || cb.EventID == "" || (cb.ClientReference == "" && cb.ProviderReference == "") {
		return CallbackEvent{}, ErrInvalidCallback
	}
	o := Outcome(strings.ToUpper(cb.Outcome))
	switch o {
	case OutcomeVerified, OutcomeRejected, OutcomeInconclusive, OutcomePending, OutcomeResultAvailable:
	default:
		return CallbackEvent{}, ErrInvalidCallback
	}
	return CallbackEvent{EventID: cb.EventID, ClientReference: cb.ClientReference, ProviderReference: cb.ProviderReference,
		Outcome: o, ReasonCode: cb.ReasonCode}, nil
}

// compile-time check
var _ Provider = (*Sandbox)(nil)
