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
	"regexp"
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
	TypeOperatingCapacity = "OPERATING_CAPACITY"
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
	CallerRef      string     `json:"caller_reference"`
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

var capacityCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// SupportsCapacity implements CapacityAuthority: the Verification Service
// verifies any well-formed capacity code as an OPERATING_CAPACITY case.
func (c *Client) SupportsCapacity(code string) bool { return capacityCode.MatchString(code) }

// CapacityReference binds an OPERATING_CAPACITY case to one capacity code.
func CapacityReference(code string) string { return "capacity:" + code }

// Capacity implements CapacityAuthority by reading the authoritative case and
// checking that it is this organisation's case for exactly this capacity.
func (c *Client) Capacity(ctx context.Context, org ids.ID, code string, caseID ids.ID) (CapacityAssertion, error) {
	v, err := c.Get(ctx, caseID)
	if err != nil {
		return CapacityAssertion{}, err
	}
	if v.VerificationID != caseID || v.Type != TypeOperatingCapacity || v.Subject.Type != SubjectBusiness || v.Subject.Domain != string(platform.DomainBusinessOperations) || v.Subject.ID != org || v.CallerRef != CapacityReference(code) {
		return CapacityAssertion{}, errors.New("verification: case is not this organisation's capacity case")
	}
	status := "PENDING"
	switch v.Status {
	case "VERIFIED":
		status = "VERIFIED"
		if v.ExpiresAt != nil && !time.Now().Before(*v.ExpiresAt) {
			status = "EXPIRED"
		}
	case "REJECTED":
		status = "REJECTED"
	case "EXPIRED":
		status = "EXPIRED"
	}
	return CapacityAssertion{OrganisationID: org, Code: code, CaseID: caseID, Status: status, ExpiresAt: v.ExpiresAt, UpdatedAt: v.UpdatedAt}, nil
}
