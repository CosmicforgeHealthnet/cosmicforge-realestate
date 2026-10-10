package payment

import (
	"context"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"time"
)

// WalletProvisioner implements only the Section 2 registration contract. It
// does not claim support for the later payment.API release/refund/payout flows.
type WalletProvisioner struct {
	Client                *httpx.Client
	LegalEntity, Currency string
}

func NewWalletProvisioner(base string, tokens httpx.TokenSource, legalEntity, currency string) (*WalletProvisioner, error) {
	c, e := httpx.NewClient(httpx.ClientConfig{BaseURL: base, Audience: string(platform.PaymentService), Tokens: tokens, Timeout: 5 * time.Second})
	if e != nil {
		return nil, e
	}
	return &WalletProvisioner{c, legalEntity, currency}, nil
}
func (p *WalletProvisioner) Ensure(ctx context.Context, account ids.ID, registrationRef string) error {
	var out struct {
		Data struct {
			WalletID ids.ID `json:"wallet_id"`
		} `json:"data"`
	}
	e := p.Client.Do(ctx, httpx.Request{Method: "POST", Path: "/internal/v1/wallets/ensure", IdempotencyKey: registrationRef, Body: map[string]any{"domain": "individual-owner", "account_id": account, "role": "OWNER", "party_kind": "PERSON", "legal_entity": p.LegalEntity, "currency": p.Currency, "registration_ref": registrationRef}}, &out)
	if e != nil {
		return e
	}
	if out.Data.WalletID.ValidateV7() != nil {
		return ids.ErrInvalidFormat
	}
	return nil
}
