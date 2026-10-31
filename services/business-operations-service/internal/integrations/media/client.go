// Package media reads authenticated Media & Document metadata and purpose-bound
// access URLs. Business Operations never uploads, downloads or stores bytes.
package media

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

type Metadata struct {
	ID             ids.ID `json:"media_id"`
	OwnerDomain    string `json:"owner_domain"`
	OwnerType      string `json:"owner_type"`
	OwnerID        ids.ID `json:"owner_id"`
	Class          string `json:"media_class"`
	MimeType       string `json:"mime_type"`
	Classification string `json:"access_classification"`
	Status         string `json:"status"`
}
type Access struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}
type API interface {
	Get(context.Context, ids.ID) (Metadata, error)
	DownloadURL(context.Context, ids.ID, string) (Access, error)
}
type Client struct{ http *httpx.Client }

func New(baseURL string, tokens httpx.TokenSource, timeout time.Duration) (*Client, error) {
	if tokens == nil {
		return nil, errors.New("media service authentication required")
	}
	client, err := httpx.NewClient(httpx.ClientConfig{BaseURL: baseURL, Audience: string(platform.MediaDocumentService), Tokens: tokens, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return &Client{http: client}, nil
}
func (c *Client) Get(ctx context.Context, id ids.ID) (Metadata, error) {
	var response struct {
		Data Metadata `json:"data"`
	}
	err := c.http.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/media/" + url.PathEscape(id.String())}, &response)
	return response.Data, err
}
func (c *Client) DownloadURL(ctx context.Context, id ids.ID, purpose string) (Access, error) {
	var response struct {
		Data Access `json:"data"`
	}
	err := c.http.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/media/" + url.PathEscape(id.String()) + "/download-url", Body: map[string]string{"purpose": purpose}}, &response)
	return response.Data, err
}
