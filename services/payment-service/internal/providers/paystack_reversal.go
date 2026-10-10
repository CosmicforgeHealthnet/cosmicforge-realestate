package providers

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// A resolved dispute is not itself cash-debit proof. Only its associated,
// processed refund confirms the economic reversal in this adapter.
func (p *Paystack) VerifyReversal(ctx context.Context, ref string) (Reversal, error) {
	if !strings.HasPrefix(ref, "dispute:") {
		return Reversal{}, ErrUnsupported
	}
	id := strings.TrimPrefix(ref, "dispute:")
	if _, e := strconv.ParseInt(id, 10, 64); e != nil {
		return Reversal{}, ErrMismatch
	}
	var d struct {
		ID           int64  `json:"id"`
		Domain       string `json:"domain"`
		Currency     string `json:"currency"`
		Status       string `json:"status"`
		RefundAmount int64  `json:"refund_amount"`
		Transaction  struct {
			ID        int64  `json:"id"`
			Reference string `json:"reference"`
			Domain    string `json:"domain"`
			Customer  struct {
				Integration int64 `json:"integration"`
			} `json:"customer"`
		} `json:"transaction"`
	}
	if e := p.call(ctx, "GET", "/dispute/"+url.PathEscape(id), nil, &d); e != nil {
		return Reversal{}, e
	}
	if strconv.FormatInt(d.ID, 10) != id || d.Domain != p.mode || d.Transaction.Domain != p.mode || d.Transaction.Customer.Integration != p.merchant || d.Transaction.Reference == "" || d.Status != "resolved" || d.RefundAmount <= 0 {
		return Reversal{}, ErrUnknown
	}
	original, e := p.refundOriginalID(ctx, Refund{ChargeReference: d.Transaction.Reference, AmountMinor: d.RefundAmount, Currency: d.Currency})
	if e != nil {
		return Reversal{}, e
	}
	if original != d.Transaction.ID {
		return Reversal{}, ErrMismatch
	}
	var result *Reversal
	for page := 1; page <= 100; page++ {
		var xs []struct {
			refundWire
			Dispute int64 `json:"dispute"`
		}
		if e := p.call(ctx, "GET", "/refund?transaction="+strconv.FormatInt(d.Transaction.ID, 10)+"&perPage=100&page="+strconv.Itoa(page), nil, &xs); e != nil {
			return Reversal{}, e
		}
		for _, r := range xs {
			if r.Dispute == d.ID && r.Status == "processed" && r.Integration == p.merchant && r.Domain == p.mode && r.Amount == d.RefundAmount && r.Currency == d.Currency {
				if r.ID <= 0 || result != nil {
					return Reversal{}, ErrMismatch
				}
				var transactionID int64
				if len(r.Transaction) > 0 && r.Transaction[0] == '{' {
					var transaction struct {
						ID        int64  `json:"id"`
						Reference string `json:"reference"`
					}
					if json.Unmarshal(r.Transaction, &transaction) != nil || transaction.Reference != d.Transaction.Reference {
						return Reversal{}, ErrMismatch
					}
					transactionID = transaction.ID
				} else if json.Unmarshal(r.Transaction, &transactionID) != nil {
					return Reversal{}, ErrMismatch
				}
				if transactionID != original {
					return Reversal{}, ErrMismatch
				}
				v := Reversal{Reference: ref, EconomicReference: "refund:" + strconv.FormatInt(r.ID, 10), ChargeReference: d.Transaction.Reference, AmountMinor: r.Amount, Currency: r.Currency, Mode: p.mode, MerchantID: p.merchant, Confirmed: true}
				result = &v
			}
		}
		if len(xs) < 100 {
			if result != nil {
				return *result, nil
			}
			return Reversal{}, ErrUnknown
		}
	}
	return Reversal{}, ErrUnknown
}
