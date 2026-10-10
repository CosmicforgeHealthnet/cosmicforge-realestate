package providers

import (
	"context"
	"net/url"
	"strconv"
)

func (p *Paystack) VerifyBank(ctx context.Context, in Bank) (Bank, error) {
	var out struct {
		Number string `json:"account_number"`
		Name   string `json:"account_name"`
	}
	e := p.call(ctx, "GET", "/bank/resolve?account_number="+url.QueryEscape(in.AccountNumber)+"&bank_code="+url.QueryEscape(in.BankCode), nil, &out)
	if e != nil {
		return Bank{}, e
	}
	if out.Number != in.AccountNumber || out.Name == "" {
		return Bank{}, ErrMismatch
	}
	return Bank{AccountNumber: out.Number, Name: out.Name, BankCode: in.BankCode}, nil
}
func (p *Paystack) CreateRecipient(ctx context.Context, in Bank) (string, error) {
	var out struct {
		Reference string `json:"recipient_code"`
		Merchant  int64  `json:"integration"`
		Mode      string `json:"domain"`
	}
	e := p.call(ctx, "POST", "/transferrecipient", map[string]any{"type": "nuban", "name": in.Name, "account_number": in.AccountNumber, "bank_code": in.BankCode, "currency": "NGN"}, &out)
	if e != nil {
		return "", e
	}
	if out.Merchant != p.merchant || out.Mode != p.mode || out.Reference == "" {
		return "", ErrMismatch
	}
	return out.Reference, nil
}
func (p *Paystack) FetchRecipient(ctx context.Context, ref string) (Recipient, error) {
	var out struct {
		Reference string `json:"recipient_code"`
		Merchant  int64  `json:"integration"`
		Mode      string `json:"domain"`
		Currency  string `json:"currency"`
		Active    bool   `json:"active"`
	}
	e := p.call(ctx, "GET", "/transferrecipient/"+url.PathEscape(ref), nil, &out)
	if e != nil {
		return Recipient{}, e
	}
	if out.Reference != ref || out.Merchant != p.merchant || out.Mode != p.mode {
		return Recipient{}, ErrMismatch
	}
	return Recipient{Reference: out.Reference, MerchantID: out.Merchant, Mode: out.Mode, Currency: out.Currency, Active: out.Active}, nil
}

type transferWire struct {
	Fee       *int64 `json:"fee_charged"`
	Reference string `json:"reference"`
	Status    string `json:"status"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Mode      string `json:"domain"`
	Merchant  int64  `json:"integration"`
	Recipient struct {
		Reference string `json:"recipient_code"`
		Merchant  int64  `json:"integration"`
		Mode      string `json:"domain"`
	} `json:"recipient"`
}

func (p *Paystack) normalizeTransfer(x transferWire) (Transfer, error) {
	merchant := x.Merchant
	if merchant == 0 {
		merchant = x.Recipient.Merchant
	}
	if merchant != p.merchant || x.Mode != p.mode || (x.Recipient.Merchant != 0 && x.Recipient.Merchant != p.merchant) || (x.Recipient.Mode != "" && x.Recipient.Mode != p.mode) || x.Reference == "" || x.Recipient.Reference == "" || x.Amount <= 0 {
		return Transfer{}, ErrMismatch
	}
	t := Transfer{Reference: x.Reference, AmountMinor: x.Amount, Currency: x.Currency, RecipientRef: x.Recipient.Reference, MerchantID: merchant, Mode: x.Mode, State: "UNKNOWN_OUTCOME"}
	if x.Fee != nil {
		if *x.Fee < 0 {
			return Transfer{}, ErrMismatch
		}
		t.FeeMinor = *x.Fee
		t.FeeKnown = true
	}
	switch x.Status {
	case "success":
		t.State = "CONFIRMED_PAID"
	case "failed":
		t.State = "CONFIRMED_FAILED"
		t.NoEffectConfirmed = true
	case "pending", "processing":
		t.State = "PROCESSING"
	case "otp":
		t.State = "OTP_REQUIRED"
	case "reversed":
		t.State = "REVIEW_REQUIRED"
		t.Reversed = true
	}
	return t, nil
}
func (p *Paystack) InitiateTransfer(ctx context.Context, t Transfer) (Transfer, error) {
	// Submission acknowledgement is never enough to finalize the ledger.
	if t.Reference == "" || t.RecipientRef == "" || t.AmountMinor <= 0 {
		return Transfer{}, ErrMismatch
	}
	var out struct {
		Reference string `json:"reference"`
	}
	e := p.call(ctx, "POST", "/transfer", map[string]any{"source": "balance", "reference": t.Reference, "recipient": t.RecipientRef, "amount": t.AmountMinor, "currency": t.Currency, "reason": "CosmicForge authorized beneficiary withdrawal"}, &out)
	if e != nil {
		return Transfer{}, e
	}
	if out.Reference != t.Reference {
		return Transfer{}, ErrMismatch
	}
	t.State = "PROCESSING"
	return t, nil
}
func (p *Paystack) VerifyTransfer(ctx context.Context, ref string) (Transfer, error) {
	var x transferWire
	e := p.call(ctx, "GET", "/transfer/verify/"+url.PathEscape(ref), nil, &x)
	if e != nil {
		return Transfer{}, e
	}
	if x.Reference != ref {
		return Transfer{}, ErrMismatch
	}
	return p.normalizeTransfer(x)
}
func (p *Paystack) FinalizeOTP(ctx context.Context, ref, otp string) error {
	if ref == "" || otp == "" {
		return ErrMismatch
	}
	return p.call(ctx, "POST", "/transfer/finalize_transfer", map[string]string{"transfer_code": ref, "otp": otp}, nil)
}
func (p *Paystack) SettlementTransactions(ctx context.Context, ref string) ([]Charge, error) {
	if _, e := strconv.ParseInt(ref, 10, 64); e != nil {
		return nil, ErrMismatch
	}
	out := []Charge{}
	seen := map[string]bool{}
	for page := 1; page <= 1000; page++ {
		var xs []chargeWire
		if e := p.call(ctx, "GET", "/settlement/"+url.PathEscape(ref)+"/transactions?perPage=100&page="+strconv.Itoa(page), nil, &xs); e != nil {
			return nil, e
		}
		for _, x := range xs {
			c, e := p.normalize(ctx, x)
			if e != nil {
				return nil, e
			}
			if seen[c.Reference] {
				return nil, ErrMismatch
			}
			seen[c.Reference] = true
			out = append(out, c)
		}
		if len(xs) < 100 {
			return out, nil
		}
	}
	return nil, ErrUnknown
}

var _ Payout = (*Paystack)(nil)
var _ SettlementTransactions = (*Paystack)(nil)
