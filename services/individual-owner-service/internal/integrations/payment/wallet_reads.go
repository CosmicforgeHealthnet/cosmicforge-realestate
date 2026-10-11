package payment

import (
	"context"
	"fmt"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Consumer DTOs mirror the published Financial Core API, not its internal models.
type Wallet struct {
	WalletID    ids.ID `json:"wallet_id"`
	IdentityID  ids.ID `json:"identity_id"`
	Domain      string `json:"domain"`
	Role        string `json:"role"`
	LegalEntity string `json:"legal_entity"`
	Currency    string `json:"currency"`
	State       string `json:"state"`
}
type WalletBalance struct {
	Kind         string `json:"kind"`
	PoolID       ids.ID `json:"pool_id"`
	AmountMinor  string `json:"amount_minor"`
	Withdrawable bool   `json:"withdrawable"`
}
type WalletSnapshot struct {
	Wallet            Wallet          `json:"wallet"`
	Balances          []WalletBalance `json:"balances"`
	HistoricalWallets []Wallet        `json:"historical_wallets,omitempty"`
}

func (p *WalletProvisioner) Snapshot(ctx context.Context, account ids.ID) (WalletSnapshot, error) {
	// The caller is the authenticated Owner use case. Never take account from a body.
	ctx = requestcontext.WithActor(ctx, requestcontext.Actor{Domain: "individual-owner", ID: account})
	var existing struct {
		Data []Wallet `json:"data"`
	}
	e := p.Client.Do(ctx, httpx.Request{Method: "GET", Path: "/internal/v1/wallets/me"}, &existing)
	if e != nil {
		return WalletSnapshot{}, e
	}
	var out struct {
		Data Wallet `json:"data"`
	}
	for _, w := range existing.Data {
		if w.Domain == "individual-owner" && w.LegalEntity == p.LegalEntity && w.Currency == p.Currency && (w.Role == "OWNER" || w.Role == "CO_OWNER") {
			var profile struct {
				Data struct {
					Purpose string `json:"purpose"`
					Wallet  Wallet `json:"wallet"`
				} `json:"data"`
			}
			if e = p.Client.Do(ctx, httpx.Request{Method: "GET", Path: "/internal/v1/wallets/" + w.WalletID.String() + "/profile"}, &profile); e != nil {
				return WalletSnapshot{}, e
			}
			if profile.Data.Wallet.WalletID != w.WalletID {
				return WalletSnapshot{}, fmt.Errorf("wallet profile context mismatch")
			}
			if profile.Data.Purpose == "EARNINGS" {
				out.Data = w
				break
			}
		}
	}
	if out.Data.WalletID == ids.Nil {
		ref := "owner-registration:" + account.String()
		e = p.Client.Do(ctx, httpx.Request{Method: "POST", Path: "/internal/v1/wallets/ensure", IdempotencyKey: ref, Body: map[string]any{"domain": "individual-owner", "account_id": account, "role": "OWNER", "party_kind": "PERSON", "legal_entity": p.LegalEntity, "currency": p.Currency, "registration_ref": ref}}, &out)
		if e != nil {
			return WalletSnapshot{}, e
		}
	}
	if out.Data.WalletID.ValidateV7() != nil || out.Data.IdentityID.ValidateV7() != nil || out.Data.Domain != "individual-owner" || out.Data.LegalEntity != p.LegalEntity || out.Data.Currency != p.Currency {
		return WalletSnapshot{}, fmt.Errorf("wallet response context mismatch")
	}
	var balances struct {
		Data []WalletBalance `json:"data"`
	}
	if e = p.Client.Do(ctx, httpx.Request{Method: "GET", Path: "/internal/v1/wallets/" + out.Data.WalletID.String() + "/balances"}, &balances); e != nil {
		return WalletSnapshot{}, e
	}
	history := []Wallet{}
	for _, w := range existing.Data {
		if w.WalletID != out.Data.WalletID && w.LegalEntity == p.LegalEntity && w.Currency == p.Currency {
			history = append(history, w)
		}
	}
	return WalletSnapshot{Wallet: out.Data, Balances: balances.Data, HistoricalWallets: history}, nil
}
