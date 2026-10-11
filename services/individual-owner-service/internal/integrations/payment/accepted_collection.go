package payment

import (
	"context"
	"fmt"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// AcceptedQuote is the published Financial Core locked-quote consumer contract.
type AcceptedQuote struct {
	ID           ids.ID `json:"quote_id"`
	Domain       string `json:"domain"`
	BusinessRef  string `json:"business_ref"`
	WalletID     ids.ID `json:"payer_wallet_id"`
	QuoteVersion string `json:"quote_version"`
	PolicyID     ids.ID `json:"policy_id"`
	Currency     string `json:"currency"`
	Jurisdiction string `json:"jurisdiction"`
	AmountMinor  int64  `json:"amount_minor"`
	Components   []struct {
		Kind        string `json:"kind"`
		Recurrence  string `json:"recurrence"`
		AmountMinor int64  `json:"amount_minor"`
	} `json:"components"`
}
type CollectionPolicy struct {
	Key             string `json:"policy_key"`
	Version         string `json:"version"`
	RequiresCustody bool   `json:"requires_custody"`
}
type CollectionIntent struct {
	ID              ids.ID  `json:"intent_id"`
	WalletID        ids.ID  `json:"wallet_id"`
	AmountMinor     int64   `json:"amount_minor"`
	Currency        string  `json:"currency"`
	LegalEntity     string  `json:"legal_entity"`
	ProviderID      ids.ID  `json:"provider_account_id"`
	PoolID          ids.ID  `json:"pool_id"`
	Reference       string  `json:"reference"`
	State           string  `json:"state"`
	SettlementState string  `json:"settlement_state"`
	CheckoutURL     *string `json:"checkout_url,omitempty"`
}

func (p *WalletProvisioner) AcceptedQuote(ctx context.Context, id ids.ID) (AcceptedQuote, error) {
	var out struct {
		Data AcceptedQuote `json:"data"`
	}
	e := p.Client.Do(ctx, httpx.Request{Method: "GET", Path: "/internal/v1/locked-quotes/" + id.String()}, &out)
	if e == nil && out.Data.ID != id {
		e = fmt.Errorf("quote response context mismatch")
	}
	return out.Data, e
}
func (p *WalletProvisioner) CreateAcceptedCollection(ctx context.Context, account ids.ID, quote AcceptedQuote, email string) (CollectionIntent, error) {
	ctx = requestcontext.WithActor(ctx, requestcontext.Actor{Domain: "individual-owner", ID: account})
	var policy struct {
		Data CollectionPolicy `json:"data"`
	}
	if e := p.Client.Do(ctx, httpx.Request{Method: "GET", Path: "/internal/v1/financial-policies/" + quote.PolicyID.String()}, &policy); e != nil {
		return CollectionIntent{}, e
	}
	var out struct {
		Data CollectionIntent `json:"data"`
	}
	e := p.Client.Do(ctx, httpx.Request{Method: "POST", Path: "/internal/v1/payment-intents", IdempotencyKey: "owner-accepted:" + quote.BusinessRef, Body: map[string]any{
		"locked_quote_id": quote.ID, "wallet_id": quote.WalletID, "amount_minor": quote.AmountMinor, "currency": quote.Currency, "country": quote.Jurisdiction, "category": policy.Data.Key, "evidence_ref": quote.QuoteVersion, "policy_version": policy.Data.Version, "email": email, "requires_custody": policy.Data.RequiresCustody, "required_liquidity_minor": int64(0)}}, &out)
	if e == nil && (out.Data.ID.ValidateV7() != nil || out.Data.WalletID != quote.WalletID || out.Data.AmountMinor != quote.AmountMinor || out.Data.Currency != quote.Currency || out.Data.LegalEntity != p.LegalEntity) {
		e = fmt.Errorf("collection response context mismatch")
	}
	return out.Data, e
}
func (p *WalletProvisioner) Collection(ctx context.Context, id ids.ID) (CollectionIntent, error) {
	var out struct {
		Data CollectionIntent `json:"data"`
	}
	e := p.Client.Do(ctx, httpx.Request{Method: "GET", Path: "/internal/v1/payment-intents/" + id.String()}, &out)
	if e == nil && out.Data.ID != id {
		e = fmt.Errorf("collection response context mismatch")
	}
	return out.Data, e
}
