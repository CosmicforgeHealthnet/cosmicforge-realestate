// Package media is verification-service's client for media-document-service
// internal APIs. Evidence files are referenced by media_id; verification
// never stores document bytes.
package media

import (
	"context"
	"net/http"
	"time"

	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Media status values relevant to evidence.
const StatusReady = "READY"

// Info is the subset of media metadata verification relies on.
type Info struct {
	MediaID              ids.ID `json:"media_id"`
	OwnerDomain          string `json:"owner_domain"`
	OwnerType            string `json:"owner_type"`
	OwnerID              ids.ID `json:"owner_id"`
	Status               string `json:"status"`
	MimeType             string `json:"mime_type"`
	SizeBytes            int64  `json:"size_bytes"`
	ChecksumSHA256       string `json:"checksum_sha256"`
	AccessClassification string `json:"access_classification"`
}

// Client calls media-document-service.
type Client struct{ c *httpx.Client }

// New builds a client authenticated with tokens.
func New(baseURL string, tokens httpx.TokenSource, timeout time.Duration) (*Client, error) {
	c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: baseURL, Audience: string(platform.MediaDocumentService), Tokens: tokens, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return &Client{c: c}, nil
}

// Get returns media metadata. Errors are *httpx.RemoteError (e.g. 404
// MEDIA_NOT_FOUND) or transport failures.
func (c *Client) Get(ctx context.Context, mediaID ids.ID) (Info, error) {
	var out struct {
		Data Info `json:"data"`
	}
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/media/" + mediaID.String()}, &out)
	return out.Data, err
}

// DownloadURL obtains a short-lived signed URL authorized for this service.
func (c *Client) DownloadURL(ctx context.Context, mediaID ids.ID) (string, error) {
	var out struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/media/" + mediaID.String() + "/download-url",
		Body: map[string]string{"purpose": "VERIFICATION_PROVIDER_SUBMISSION"}, IdempotencyKey: "dl-" + ids.New().String()}, &out)
	return out.Data.URL, err
}
