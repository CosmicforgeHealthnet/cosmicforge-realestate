// Package distribution owns channel delivery contracts, never verification,
// physical inventory, payment execution or canonical reservation acceptance.
package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"net/http"
	"net/url"
	"time"

	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

const Marketplace = "COSMICFORGE_MARKETPLACE"

var ErrProviderUnavailable = errors.New("channel provider operation unavailable or uncertified")
var ErrAmbiguous = errors.New("channel delivery outcome requires authoritative readback")

type Connection struct {
	ID                  ids.ID `json:"connection_id"`
	OrganisationID      ids.ID `json:"organisation_id"`
	PropertyID          ids.ID `json:"property_id"`
	ProviderAccountID   string `json:"provider_account_id"`
	CredentialReference string `json:"credential_reference"`
}
type Authorization struct {
	ProviderAccountID string    `json:"provider_account_id"`
	PropertyID        ids.ID    `json:"property_id"`
	Reference         string    `json:"validation_reference"`
	ExpiresAt         time.Time `json:"expires_at"`
	Capabilities      []string  `json:"capabilities"`
}
type Command struct {
	ContractVersion  string          `json:"contract_version"`
	JobID            ids.ID          `json:"job_id"`
	Connection       Connection      `json:"connection"`
	Kind             string          `json:"kind"`
	CanonicalVersion int64           `json:"canonical_version"`
	IdempotencyKey   string          `json:"idempotency_key"`
	PayloadHash      string          `json:"payload_hash"`
	Payload          json.RawMessage `json:"payload"`
}
type Acknowledgement struct {
	ProviderRequestID string    `json:"provider_request_id"`
	ConnectionID      ids.ID    `json:"connection_id"`
	CanonicalVersion  int64     `json:"canonical_version"`
	PayloadHash       string    `json:"payload_hash"`
	State             string    `json:"state"`
	Durable           bool      `json:"durable"`
	ObservedAt        time.Time `json:"observed_at"`
}
type Receipt struct {
	Booking                    *reservations.Create `json:"booking,omitempty"`
	ExpectedReservationVersion int64                `json:"expected_reservation_version,omitempty"`
	ProviderEventID            string               `json:"provider_event_id"`
	ExternalObjectID           string               `json:"external_object_id"`
	ExternalVersion            string               `json:"external_version"`
	Kind                       string               `json:"kind"`
	OccurredAt                 time.Time            `json:"occurred_at"`
	ProductID                  ids.ID               `json:"product_id"`
	DateFrom                   string               `json:"date_from"`
	DateTo                     string               `json:"date_to"`
	Quantity                   int                  `json:"quantity"`
	QuoteID                    *ids.ID              `json:"quote_id,omitempty"`
	HoldID                     *ids.ID              `json:"hold_id,omitempty"`
	Reference                  string               `json:"reference"`
}
type PollResult struct {
	Cursor     string    `json:"cursor"`
	NextCursor string    `json:"next_cursor"`
	Events     []Receipt `json:"events"`
}
type MappingReference struct {
	Kind                   string `json:"kind"`
	ExternalID             string `json:"external_id"`
	CanonicalID            ids.ID `json:"canonical_id"`
	CanonicalVersion       int64  `json:"canonical_version"`
	ExpectedMappingVersion int64  `json:"expected_mapping_version"`
}
type MappingProof struct {
	Kind               string `json:"kind"`
	ExternalID         string `json:"external_id"`
	PropertyExternalID string `json:"property_external_id"`
	ProviderAccountID  string `json:"provider_account_id"`
	Reference          string `json:"reference"`
}
type Adapter interface {
	Validate(context.Context, Connection) (Authorization, error)
	Apply(context.Context, Command) (Acknowledgement, error)
	Readback(context.Context, Command) (Acknowledgement, error)
	Poll(context.Context, Connection, string) (PollResult, error)
	InspectObject(context.Context, Connection, MappingReference) (MappingProof, error)
}

func ValidAcknowledgement(a Acknowledgement, c Command, now time.Time) bool {
	return a.Durable && a.ProviderRequestID != "" && len(a.ProviderRequestID) <= 200 && a.ConnectionID == c.Connection.ID && a.CanonicalVersion == c.CanonicalVersion && a.PayloadHash == c.PayloadHash && !a.ObservedAt.IsZero() && !a.ObservedAt.After(now.Add(time.Minute)) && now.Sub(a.ObservedAt) <= 5*time.Minute && (a.State == "PUBLISHED" || a.State == "PAUSED" || a.State == "WITHDRAWN" || a.State == "SYNCHRONIZED")
}

// DisabledAdapter is explicit capability gating, not a simulated provider. An
// approved provider adapter replaces it only after its actual contract and
// certification evidence are available. It never performs network requests.
type DisabledAdapter struct{ Provider string }

func (DisabledAdapter) Validate(context.Context, Connection) (Authorization, error) {
	return Authorization{}, ErrProviderUnavailable
}
func (DisabledAdapter) Apply(context.Context, Command) (Acknowledgement, error) {
	return Acknowledgement{}, ErrProviderUnavailable
}
func (DisabledAdapter) Readback(context.Context, Command) (Acknowledgement, error) {
	return Acknowledgement{}, ErrProviderUnavailable
}
func (DisabledAdapter) Poll(context.Context, Connection, string) (PollResult, error) {
	return PollResult{}, ErrProviderUnavailable
}
func (DisabledAdapter) InspectObject(context.Context, Connection, MappingReference) (MappingProof, error) {
	return MappingProof{}, ErrProviderUnavailable
}

// MarketplaceClient is the Business supply producer contract. Its destination
// is deployment-owned configuration; no browser request can supply a URL. The
// shared client signs the Marketplace audience, propagates correlation, rejects
// redirects and bounds responses/timeouts. Durable retries belong to sync jobs.
type MarketplaceClient struct{ http *httpx.Client }

func NewMarketplace(baseURL string, tokens httpx.TokenSource) (*MarketplaceClient, error) {
	u, e := url.Parse(baseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "http" && u.Scheme != "https" || tokens == nil {
		return nil, errors.New("trusted Marketplace service URL and authentication required")
	}
	c, e := httpx.NewClient(httpx.ClientConfig{BaseURL: baseURL, Audience: string(platform.MarketplaceService), Tokens: tokens, Timeout: 5 * time.Second, MaxAttempts: 1, MaxResponseBytes: 512 << 10})
	if e != nil {
		return nil, e
	}
	return &MarketplaceClient{http: c}, nil
}
func (c *MarketplaceClient) Validate(ctx context.Context, in Connection) (Authorization, error) {
	ctx = requestcontext.WithOrganization(ctx, in.OrganisationID)
	var out struct {
		Data Authorization `json:"data"`
	}
	e := c.http.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/business-supply/connections/validate", Body: in, IdempotencyKey: "validate:" + in.ID.String() + ":" + in.CredentialReference}, &out)
	return out.Data, e
}
func (c *MarketplaceClient) Apply(ctx context.Context, in Command) (Acknowledgement, error) {
	ctx = requestcontext.WithOrganization(ctx, in.Connection.OrganisationID)
	var out struct {
		Data Acknowledgement `json:"data"`
	}
	e := c.http.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/business-supply/commands", Body: in, IdempotencyKey: in.IdempotencyKey}, &out)
	return out.Data, e
}
func (c *MarketplaceClient) Readback(ctx context.Context, in Command) (Acknowledgement, error) {
	ctx = requestcontext.WithOrganization(ctx, in.Connection.OrganisationID)
	var out struct {
		Data Acknowledgement `json:"data"`
	}
	e := c.http.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/business-supply/commands/" + url.PathEscape(in.IdempotencyKey), Query: url.Values{"connection_id": {in.Connection.ID.String()}}}, &out)
	return out.Data, e
}
func (c *MarketplaceClient) Poll(ctx context.Context, in Connection, cursor string) (PollResult, error) {
	ctx = requestcontext.WithOrganization(ctx, in.OrganisationID)
	var out struct {
		Data PollResult `json:"data"`
	}
	e := c.http.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/business-supply/connections/" + in.ID.String() + "/events", Query: url.Values{"cursor": {cursor}}}, &out)
	return out.Data, e
}
func (c *MarketplaceClient) InspectObject(ctx context.Context, in Connection, ref MappingReference) (MappingProof, error) {
	ctx = requestcontext.WithOrganization(ctx, in.OrganisationID)
	var out struct {
		Data MappingProof `json:"data"`
	}
	e := c.http.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/business-supply/connections/" + in.ID.String() + "/mapping-inspection", Body: ref}, &out)
	return out.Data, e
}
