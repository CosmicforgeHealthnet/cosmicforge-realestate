// Package media is individual-owner-service's client for the
// media-document-service internal API (contracts/internal/openapi/media-document-service.yaml).
// Bytes never pass through or are stored by this service: it keeps media IDs
// and a usability projection (PENDING/READY/REJECTED/DELETED) only.
package media

import (
	"context"
	"net/http"
	"net/url"
	"time"

	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Media is the subset of media metadata used here.
type Media struct {
	MediaID              ids.ID `json:"media_id"`
	OwnerDomain          string `json:"owner_domain"`
	OwnerType            string `json:"owner_type"`
	OwnerID              ids.ID `json:"owner_id"`
	MediaClass           string `json:"media_class"`
	MimeType             string `json:"mime_type"`
	AccessClassification string `json:"access_classification"`
	Status               string `json:"status"`
	RejectionReason      string `json:"rejection_reason"`
}

// Projection maps a media-service status onto the owner-domain usability projection.
func Projection(status string) string {
	switch status {
	case "READY":
		return "READY"
	case "REJECTED":
		return "REJECTED"
	case "DELETE_PENDING", "DELETED":
		return "DELETED"
	}
	return "PENDING"
}

// Download is a short-lived signed URL.
type Download struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Grant is one explicit access grant.
type Grant struct {
	GranteeType string `json:"grantee_type"` // SERVICE | ACTOR
	Service     string `json:"service,omitempty"`
	Domain      string `json:"domain,omitempty"`
	ActorID     ids.ID `json:"actor_id,omitzero"`
}

// API is the media-document-service contract used by the owner domain.
type API interface {
	Get(ctx context.Context, id ids.ID) (Media, error)
	DownloadURL(ctx context.Context, id ids.ID, purpose string) (Download, error)
	ReplaceAccessPolicy(ctx context.Context, id ids.ID, classification string, grants []Grant, idemKey string) error
}

// Client implements API over HTTP.
type Client struct{ c *httpx.Client }

// New builds a client authenticated with service tokens.
func New(baseURL string, tokens httpx.TokenSource, timeout time.Duration) (*Client, error) {
	c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: baseURL, Audience: string(platform.MediaDocumentService), Tokens: tokens, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return &Client{c: c}, nil
}

// Get implements API.
func (c *Client) Get(ctx context.Context, id ids.ID) (Media, error) {
	var out struct {
		Data Media `json:"data"`
	}
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/media/" + url.PathEscape(id.String())}, &out)
	return out.Data, err
}

// DownloadURL implements API.
func (c *Client) DownloadURL(ctx context.Context, id ids.ID, purpose string) (Download, error) {
	var out struct {
		Data Download `json:"data"`
	}
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/media/" + url.PathEscape(id.String()) + "/download-url",
		Body: map[string]string{"purpose": purpose}}, &out)
	return out.Data, err
}

// ReplaceAccessPolicy implements API.
func (c *Client) ReplaceAccessPolicy(ctx context.Context, id ids.ID, classification string, grants []Grant, idemKey string) error {
	return c.c.Do(ctx, httpx.Request{Method: http.MethodPut, Path: "/internal/v1/media/" + url.PathEscape(id.String()) + "/access-policy",
		Body: map[string]any{"access_classification": classification, "grants": grants}, IdempotencyKey: idemKey}, nil)
}
