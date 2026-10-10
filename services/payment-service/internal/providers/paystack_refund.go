package providers

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

type refundWire struct {
	ID           int64           `json:"id"`
	Integration  int64           `json:"integration"`
	Domain       string          `json:"domain"`
	Currency     string          `json:"currency"`
	Amount       int64           `json:"amount"`
	Status       string          `json:"status"`
	MerchantNote string          `json:"merchant_note"`
	Transaction  json.RawMessage `json:"transaction"`
}

func (p *Paystack) refundOriginalID(ctx context.Context, in Refund) (int64, error) {
	var w chargeWire
	if e := p.call(ctx, "GET", "/transaction/verify/"+url.PathEscape(in.ChargeReference), nil, &w); e != nil {
		return 0, e
	}
	v, e := p.normalize(ctx, w)
	if e != nil {
		return 0, e
	}

	if w.ID <= 0 || v.Reference != in.ChargeReference || v.Currency != in.Currency || v.AmountMinor < in.AmountMinor {
		return 0, ErrMismatch
	}
	return w.ID, nil
}
func (p *Paystack) normalizeRefund(ctx context.Context, w refundWire, in Refund) (Refund, error) {
	if in.ID != "" && strconv.FormatInt(w.ID, 10) != in.ID {
		return Refund{}, ErrMismatch
	}
	if w.ID <= 0 || w.Integration != p.merchant || w.Domain != p.mode || w.Currency != in.Currency || w.Amount != in.AmountMinor || w.MerchantNote != in.Reference {
		return Refund{}, ErrMismatch
	}
	// Fetch/list responses may contain an ID or expanded transaction. The
	// marker and original-transaction filter identify the durable attempt.
	if len(w.Transaction) > 0 && w.Transaction[0] == '{' {
		var t struct {
			Reference string `json:"reference"`
		}
		if json.Unmarshal(w.Transaction, &t) != nil || t.Reference != in.ChargeReference {
			return Refund{}, ErrMismatch
		}
	} else {
		var original int64
		if json.Unmarshal(w.Transaction, &original) != nil {
			return Refund{}, ErrMismatch
		}
		id, e := p.refundOriginalID(ctx, in)
		if e != nil {
			return Refund{}, e
		}
		if original != id {
			return Refund{}, ErrMismatch
		}
	}
	in.ID = strconv.FormatInt(w.ID, 10)
	in.Mode = w.Domain
	in.MerchantID = w.Integration
	switch w.Status {
	case "processed":
		in.State = "CONFIRMED_REFUNDED"
	case "pending", "processing":
		in.State = "PROCESSING"
	case "needs-attention":
		in.State = "REVIEW_REQUIRED"
	default:
		in.State = "UNKNOWN_OUTCOME"
	}
	// A generic failed response is not authoritative proof of no economic effect.
	return in, nil
}
func (p *Paystack) InitiateRefund(ctx context.Context, in Refund) (Refund, error) {
	if in.AmountMinor <= 0 || in.Currency == "" || in.Reference == "" || in.ChargeReference == "" {
		return Refund{}, ErrMismatch
	}
	var w refundWire
	e := p.call(ctx, "POST", "/refund", map[string]any{"transaction": in.ChargeReference, "amount": in.AmountMinor, "currency": in.Currency, "merchant_note": in.Reference}, &w)
	if e != nil {
		return Refund{}, e
	}
	return p.normalizeRefund(ctx, w, in)
}
func (p *Paystack) VerifyRefund(ctx context.Context, in Refund) (Refund, error) {
	if in.ID != "" {
		if _, e := strconv.ParseInt(in.ID, 10, 64); e != nil {
			return Refund{}, ErrMismatch
		}
		var w refundWire
		if e := p.call(ctx, "GET", "/refund/"+url.PathEscape(in.ID), nil, &w); e != nil {
			return Refund{}, e
		}
		return p.normalizeRefund(ctx, w, in)
	}
	// Bounded complete pagination. No match, duplicates or truncated discovery
	// preserve UNKNOWN and require operator review; none authorizes resubmission.
	var found *refundWire
	original, e := p.refundOriginalID(ctx, in)
	if e != nil {
		return Refund{}, e
	}
	for page := 1; page <= 100; page++ {
		var xs []refundWire
		path := "/refund?transaction=" + strconv.FormatInt(original, 10) + "&perPage=100&page=" + strconv.Itoa(page)
		if e := p.call(ctx, "GET", path, nil, &xs); e != nil {
			return Refund{}, e
		}
		for _, w := range xs {
			if w.MerchantNote == in.Reference {
				if found != nil {
					return Refund{}, ErrMismatch
				}
				v := w
				found = &v
			}
		}
		if len(xs) < 100 {
			if found == nil {
				return Refund{}, ErrUnknown
			}
			return p.normalizeRefund(ctx, *found, in)
		}
	}
	return Refund{}, ErrUnknown
}
