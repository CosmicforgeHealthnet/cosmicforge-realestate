// Package verification is business-operations-service's client for the
// verification-service internal API (contracts/internal/openapi/verification-service.yaml).
// verification-service owns IDENTITY and PROPERTY_AUTHORITY decisions; this
// service stores only verification IDs and status projections and never
// raw KYC documents or provider payloads.
package verification

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Verification types and subject types used by the business domain.
const (
	TypeBusiness          = "BUSINESS"
	SubjectBusiness       = "BUSINESS"
	TypeIdentity          = "IDENTITY"
	TypePropertyAuthority = "PROPERTY_AUTHORITY"
	SubjectUser           = "USER"
	SubjectProperty       = "PROPERTY"
)

// Subject identifies what is verified.
type Subject struct {
	Type   string `json:"subject_type"`
	Domain string `json:"subject_domain"`
	ID     ids.ID `json:"subject_id"`
}

// Case is the subset of the verification case used here.
type Case struct {
	SupersedesID   *ids.ID    `json:"supersedes_verification_id,omitempty"`
	VerificationID ids.ID     `json:"verification_id"`
	Type           string     `json:"verification_type"`
	Subject        Subject    `json:"subject"`
	Status         string     `json:"status"`
	ReasonCode     string     `json:"reason_code"`
	DecidedAt      *time.Time `json:"decided_at"`
	ExpiresAt      *time.Time `json:"expires_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// RenewalAPI is supported by the existing Verification Service reverify
// operation. Its result must be read back and preserve subject/supersedes IDs.
type RenewalAPI interface {
	Reverify(context.Context, ids.ID, string, string) (Case, error)
}

func (c *Client) Reverify(ctx context.Context, id ids.ID, reference, key string) (Case, error) {
	var out envelope[Case]
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/verifications/" + url.PathEscape(id.String()) + "/reverify", Body: map[string]string{"caller_reference": reference}, IdempotencyKey: key}, &out)
	return out.Data, err
}

// Evidence is an attached media reference.
type Evidence struct {
	EvidenceID   ids.ID `json:"evidence_id"`
	MediaID      ids.ID `json:"media_id"`
	EvidenceType string `json:"evidence_type"`
}

// API is the verification-service contract used by the business domain.
type API interface {
	Create(ctx context.Context, typ string, subject Subject, callerRef, idemKey string) (Case, error)
	AttachEvidence(ctx context.Context, id, mediaID ids.ID, evidenceType, idemKey string) (Evidence, error)
	Submit(ctx context.Context, id ids.ID, idemKey string) (Case, error)
	Process(ctx context.Context, id ids.ID, idemKey string) (Case, error)
	Get(ctx context.Context, id ids.ID) (Case, error)
}

// Client implements API over HTTP.
type Client struct{ c *httpx.Client }

// New builds a client authenticated with service tokens.
func New(baseURL string, tokens httpx.TokenSource, timeout time.Duration) (*Client, error) {
	if tokens == nil {
		return nil, errors.New("verification: service authentication is required")
	}
	c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: baseURL, Audience: string(platform.VerificationService), Tokens: tokens, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return &Client{c: c}, nil
}

type envelope[T any] struct {
	Data T `json:"data"`
}

// Create implements API.
func (c *Client) Create(ctx context.Context, typ string, subject Subject, callerRef, idemKey string) (Case, error) {
	var out envelope[Case]
	body := map[string]any{"verification_type": typ, "subject": subject}
	if callerRef != "" {
		body["caller_reference"] = callerRef
	}
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/verifications", Body: body, IdempotencyKey: idemKey}, &out)
	return out.Data, err
}

// AttachEvidence implements API.
func (c *Client) AttachEvidence(ctx context.Context, id, mediaID ids.ID, evidenceType, idemKey string) (Evidence, error) {
	var out envelope[Evidence]
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/verifications/" + url.PathEscape(id.String()) + "/evidence",
		Body: map[string]any{"media_id": mediaID, "evidence_type": evidenceType}, IdempotencyKey: idemKey}, &out)
	return out.Data, err
}

// Submit implements API.
func (c *Client) Submit(ctx context.Context, id ids.ID, idemKey string) (Case, error) {
	var out envelope[Case]
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/verifications/" + url.PathEscape(id.String()) + "/submit",
		Body: map[string]any{}, IdempotencyKey: idemKey}, &out)
	return out.Data, err
}

// Process implements API.
func (c *Client) Process(ctx context.Context, id ids.ID, idemKey string) (Case, error) {
	var out envelope[Case]
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/verifications/" + url.PathEscape(id.String()) + "/process",
		Body: map[string]any{}, IdempotencyKey: idemKey}, &out)
	return out.Data, err
}

// Get implements API.
func (c *Client) Get(ctx context.Context, id ids.ID) (Case, error) {
	var out envelope[Case]
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/verifications/" + url.PathEscape(id.String())}, &out)
	return out.Data, err
}
