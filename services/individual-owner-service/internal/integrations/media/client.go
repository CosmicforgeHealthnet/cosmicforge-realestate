// Package media is individual-owner-service's client for the
// media-document-service internal API (contracts/internal/openapi/media-document-service.yaml).
// Bytes never pass through or are stored by this service: it keeps media IDs
// and a usability projection (PENDING/READY/REJECTED/DELETED) only.
package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
	// Upload stores a service-generated document (reports, exports) owned by
	// an owner account: initiate, PUT to the signed URL, complete.
	Upload(ctx context.Context, req UploadRequest, idemKey string) (Media, error)
}

// UploadRequest is a generated document to store.
type UploadRequest struct {
	OwnerAccountID ids.ID
	Filename       string
	MimeType       string // application/pdf
	DocumentType   string
	Title          string
	Content        []byte
}

// Client implements API over HTTP.
type Client struct {
	c       *httpx.Client
	storage *http.Client
}

// New builds a client authenticated with service tokens.
func New(baseURL string, tokens httpx.TokenSource, timeout time.Duration) (*Client, error) {
	c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: baseURL, Audience: string(platform.MediaDocumentService), Tokens: tokens, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return &Client{c: c, storage: &http.Client{Timeout: 4 * timeout}}, nil
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

// Upload implements API.
func (c *Client) Upload(ctx context.Context, req UploadRequest, idemKey string) (Media, error) {
	sum := sha256.Sum256(req.Content)
	var init struct {
		Data struct {
			Media  Media `json:"media"`
			Upload struct {
				URL     string            `json:"url"`
				Method  string            `json:"method"`
				Headers map[string]string `json:"headers"`
			} `json:"upload"`
		} `json:"data"`
	}
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/media/uploads", IdempotencyKey: idemKey + ":initiate",
		Body: map[string]any{"owner": map[string]any{"owner_domain": string(platform.DomainIndividualOwner), "owner_type": "USER", "owner_id": req.OwnerAccountID},
			"media_class": "DOCUMENT", "filename": req.Filename, "mime_type": req.MimeType, "size_bytes": len(req.Content),
			"checksum_sha256": hex.EncodeToString(sum[:]), "access_classification": "PRIVATE", "document_type": req.DocumentType, "title": req.Title}}, &init)
	if err != nil {
		return Media{}, err
	}
	m := init.Data.Media
	if m.Status == "" || m.Status == "INITIATED" || m.Status == "UPLOAD_PENDING" { // not yet uploaded (a replayed initiation may be further along)
		method := init.Data.Upload.Method
		if method == "" {
			method = http.MethodPut
		}
		put, err := http.NewRequestWithContext(ctx, method, init.Data.Upload.URL, bytes.NewReader(req.Content))
		if err != nil {
			return Media{}, err
		}
		put.ContentLength = int64(len(req.Content))
		for k, v := range init.Data.Upload.Headers {
			put.Header.Set(k, v)
		}
		resp, err := c.storage.Do(put)
		if err != nil {
			return Media{}, fmt.Errorf("media upload: %w", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return Media{}, fmt.Errorf("media upload: storage answered %d", resp.StatusCode)
		}
	}
	var done struct {
		Data Media `json:"data"`
	}
	err = c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/media/" + url.PathEscape(m.MediaID.String()) + "/complete",
		IdempotencyKey: idemKey + ":complete", Body: map[string]any{}}, &done)
	return done.Data, err
}
