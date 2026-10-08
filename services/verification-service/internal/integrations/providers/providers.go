// Package providers defines the KYC/KYB/registry provider abstraction. Use
// cases depend only on these interfaces and normalized outcomes, never on a
// vendor's response structures.
package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Outcome is a normalized provider outcome.
type Outcome string

const (
	OutcomeVerified     Outcome = "VERIFIED"
	OutcomeRejected     Outcome = "REJECTED"
	OutcomeInconclusive Outcome = "INCONCLUSIVE" // needs a human decision
	OutcomePending      Outcome = "PENDING"      // accepted, no result yet
	// OutcomeResultAvailable: a thin callback; the result must be fetched.
	OutcomeResultAvailable Outcome = "RESULT_AVAILABLE"
)

// Final reports whether o is a decision-bearing outcome.
func (o Outcome) Final() bool {
	return o == OutcomeVerified || o == OutcomeRejected || o == OutcomeInconclusive
}

// Evidence is a reference to a stored document; adapters that need the
// bytes obtain a short-lived URL through EvidenceSource.
type Evidence struct {
	MediaID      ids.ID
	EvidenceType string
	Checksum     string
}

// EvidenceSource yields short-lived download URLs for evidence, authorized by
// media-document-service. Adapters must never persist these URLs.
type EvidenceSource interface {
	DownloadURL(ctx context.Context, mediaID ids.ID) (string, error)
}

// SubmitRequest is a provider-neutral submission.
type SubmitRequest struct {
	// ClientReference is our idempotency key at the provider (one per attempt).
	ClientReference string
	VerificationID  ids.ID
	Type            domain.Type
	Subject         domain.Subject
	Evidence        []Evidence
	EvidenceSource  EvidenceSource
	Metadata        map[string]string
}

// SubmitResult is the provider's acknowledgement.
type SubmitResult struct {
	ProviderReference string
	Outcome           Outcome // PENDING or a final outcome for synchronous providers
	ReasonCode        string
	EventID           string // provider-unique ID of a synchronous result
}

// StatusResult is a read-back.
type StatusResult struct {
	Found             bool
	ProviderReference string
	Outcome           Outcome
	ReasonCode        string
	EventID           string
}

// CallbackEvent is a verified provider callback.
type CallbackEvent struct {
	EventID           string
	ClientReference   string
	ProviderReference string
	Outcome           Outcome
	ReasonCode        string
}

// Provider is implemented by every adapter.
type Provider interface {
	Name() string
	Supports(t domain.Type) bool
	// Submit must be idempotent per ClientReference at the provider.
	Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error)
	// ReadStatus looks a submission up by our ClientReference (read-back).
	ReadStatus(ctx context.Context, clientReference string) (StatusResult, error)
	// VerifyCallback authenticates and parses a provider callback.
	VerifyCallback(ctx context.Context, h http.Header, body []byte) (CallbackEvent, error)
	// FetchResult retrieves a result by provider reference.
	FetchResult(ctx context.Context, providerReference string) (StatusResult, error)
}

// ErrorKind classifies provider failures for retry semantics.
type ErrorKind int

const (
	// KindRetryable: the request was definitely not processed (e.g. 429,
	// connection refused before sending); safe to retry the same call.
	KindRetryable ErrorKind = iota + 1
	// KindUnknown: the request may have been processed (timeout after
	// sending, 5xx). Never resubmit before a read-back.
	KindUnknown
	// KindPermanent: the provider rejected the request itself (bad input,
	// unsupported). Retrying will not help.
	KindPermanent
)

// Error is a classified provider failure. Message must be safe to log (no
// personal data, no secrets).
type Error struct {
	Kind ErrorKind
	Code string
	Err  error
}

func (e *Error) Error() string { return fmt.Sprintf("provider %s: %v", e.Code, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Classify returns the kind of err (KindUnknown for unclassified errors: the
// safe assumption).
func Classify(err error) ErrorKind {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return KindUnknown
}

// ErrInvalidCallback is returned by VerifyCallback for unauthenticated or
// malformed callbacks.
var ErrInvalidCallback = errors.New("providers: invalid callback")

// Registry holds enabled providers.
type Registry struct {
	byName      map[string]Provider
	defaultName string
}

// NewRegistry builds a registry; defaultName must be enabled.
func NewRegistry(defaultName string, ps ...Provider) (*Registry, error) {
	r := &Registry{byName: map[string]Provider{}, defaultName: defaultName}
	for _, p := range ps {
		r.byName[p.Name()] = p
	}
	if _, ok := r.byName[defaultName]; !ok {
		return nil, fmt.Errorf("providers: default provider %q not enabled", defaultName)
	}
	return r, nil
}

// Get returns a provider by name.
func (r *Registry) Get(name string) (Provider, bool) { p, ok := r.byName[name]; return p, ok }

// Select returns the requested provider (or the default) if it supports t.
func (r *Registry) Select(name string, t domain.Type) (Provider, error) {
	if name == "" {
		name = r.defaultName
	}
	p, ok := r.byName[name]
	if !ok || !p.Supports(t) {
		return nil, domain.ErrProviderUnavailable()
	}
	return p, nil
}

// Names lists enabled providers.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
