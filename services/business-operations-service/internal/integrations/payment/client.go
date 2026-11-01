// Package payment consumes existing Payment-owned policies, locked quotes and
// collection intents. It owns no wallet, ledger, provider or settlement logic.
package payment

import (
	"context"
	"errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"net/http"
	"net/url"
	"time"
)

type Policy struct {
	ID              ids.ID     `json:"policy_id"`
	Key             string     `json:"policy_key"`
	Version         string     `json:"version"`
	Jurisdiction    string     `json:"jurisdiction"`
	Currency        string     `json:"currency"`
	ContractType    string     `json:"contract_type"`
	Product         string     `json:"product"`
	EffectiveFrom   time.Time  `json:"effective_from"`
	EffectiveUntil  *time.Time `json:"effective_until,omitempty"`
	Active          bool       `json:"active"`
	Schedules       []string   `json:"schedules"`
	RequiresCustody bool       `json:"requires_custody"`
}
type Component struct {
	Ref                string `json:"component_ref"`
	Kind               string `json:"kind"`
	AmountMinor        int64  `json:"amount_minor"`
	PayerRef           string `json:"payer_ref"`
	Recurrence         string `json:"recurrence"`
	ServiceEvidenceRef string `json:"service_evidence_ref,omitempty"`
}
type Quote struct {
	ID               ids.ID      `json:"quote_id"`
	Domain           string      `json:"domain"`
	BusinessRef      string      `json:"business_ref"`
	PayerWalletID    ids.ID      `json:"payer_wallet_id"`
	PayerType        string      `json:"payer_type"`
	PayerConsentRef  string      `json:"payer_consent_ref"`
	LegalPartyRef    string      `json:"legal_party_ref"`
	QuoteVersion     string      `json:"quote_version"`
	AgreementRef     string      `json:"agreement_ref"`
	AgreementVersion string      `json:"agreement_version"`
	PolicyID         ids.ID      `json:"policy_id"`
	Currency         string      `json:"currency"`
	Jurisdiction     string      `json:"jurisdiction"`
	Schedule         string      `json:"schedule"`
	AmountMinor      int64       `json:"amount_minor"`
	Components       []Component `json:"components"`
}
type IntentInput struct {
	LockedQuoteID          *ids.ID `json:"locked_quote_id,omitempty"`
	WalletID               ids.ID  `json:"wallet_id"`
	AmountMinor            int64   `json:"amount_minor"`
	Currency               string  `json:"currency"`
	Country                string  `json:"country"`
	Category               string  `json:"category"`
	EvidenceRef            string  `json:"evidence_ref"`
	PolicyVersion          string  `json:"policy_version"`
	Email                  string  `json:"email"`
	RequiresCustody        bool    `json:"requires_custody"`
	RequiredLiquidityMinor int64   `json:"required_liquidity_minor"`
}
type Intent struct {
	ID              ids.ID  `json:"intent_id"`
	WalletID        ids.ID  `json:"wallet_id"`
	AmountMinor     int64   `json:"amount_minor"`
	Currency        string  `json:"currency"`
	Reference       string  `json:"reference"`
	State           string  `json:"state"`
	SettlementState string  `json:"settlement_state"`
	CheckoutURL     *string `json:"checkout_url,omitempty"`
}
type Receipt struct {
	RemedyVersion        int     `json:"remedy_version"`
	RemedyReviewRequired bool    `json:"remedy_review_required"`
	Quote                Quote   `json:"quote"`
	IntentID             *ids.ID `json:"intent_id,omitempty"`
	CollectionState      string  `json:"collection_state"`
	SettlementState      string  `json:"settlement_state"`
	FundingConfirmed     bool    `json:"funding_confirmed"`
	ReversedMinor        int64   `json:"reversed_minor"`
	PayerContext         struct {
		WalletID          ids.ID `json:"wallet_id"`
		CanonicalPartyRef string `json:"canonical_party_ref"`
	} `json:"payer_context"`
}
type API interface {
	Policy(context.Context, ids.ID) (Policy, error)
	Receipt(context.Context, ids.ID) (Receipt, error)
	Lock(context.Context, Quote, string) (Quote, error)
	Collect(context.Context, IntentInput, string) (Intent, error)
	Intent(context.Context, ids.ID) (Intent, error)
}
type Client struct{ c *httpx.Client }

func New(base string, tokens httpx.TokenSource, timeout time.Duration) (*Client, error) {
	if tokens == nil {
		return nil, errors.New("payment: authenticated service identity required")
	}
	c, e := httpx.NewClient(httpx.ClientConfig{BaseURL: base, Audience: string(platform.PaymentService), Tokens: tokens, Timeout: timeout})
	if e != nil {
		return nil, e
	}
	return &Client{c}, nil
}

type envelope[T any] struct {
	Data T `json:"data"`
}

func (c *Client) Policy(ctx context.Context, id ids.ID) (Policy, error) {
	var out envelope[Policy]
	e := c.c.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/financial-policies/" + url.PathEscape(id.String())}, &out)
	return out.Data, e
}
func (c *Client) Lock(ctx context.Context, in Quote, key string) (Quote, error) {
	var out envelope[Quote]
	e := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/locked-quotes", Body: in, IdempotencyKey: key}, &out)
	return out.Data, e
}
func (c *Client) Collect(ctx context.Context, in IntentInput, key string) (Intent, error) {
	var out envelope[Intent]
	e := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/payment-intents", Body: in, IdempotencyKey: key}, &out)
	return out.Data, e
}
func (c *Client) Intent(ctx context.Context, id ids.ID) (Intent, error) {
	var out envelope[Intent]
	e := c.c.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/payment-intents/" + url.PathEscape(id.String())}, &out)
	return out.Data, e
}

func (c *Client) Receipt(ctx context.Context, id ids.ID) (Receipt, error) {
	var out envelope[Receipt]
	e := c.c.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/locked-quotes/" + url.PathEscape(id.String()) + "/receipt"}, &out)
	return out.Data, e
}
