package providers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"go.opentelemetry.io/otel/attribute"
)

type Paystack struct {
	base            string
	secret          security.Secret
	client          *http.Client
	merchant        int64
	mode            string
	webhookMu       sync.RWMutex
	webhookPrevious []security.Secret
}

func NewPaystack(base string, secret security.Secret, merchant int64, mode string, client *http.Client) (*Paystack, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(mode == "test" && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return nil, errors.New("paystack requires HTTPS (loopback contract fixtures only in test)")
	}
	if merchant <= 0 || (mode != "test" && mode != "live") || !strings.HasPrefix(secret.Reveal(), "sk_"+mode+"_") {
		return nil, errors.New("paystack credential/mode/merchant mismatch")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	copyClient := *client
	copyClient.Transport = observability.HTTPTransport(copyClient.Transport)
	if copyClient.Timeout <= 0 {
		copyClient.Timeout = 15 * time.Second
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Paystack{base: strings.TrimSuffix(base, "/"), secret: secret, client: &copyClient, merchant: merchant, mode: mode}, nil
}
func (p *Paystack) SetWebhookOverlap(keys []security.Secret) error {
	if len(keys) > 2 {
		return ErrMismatch
	}
	for _, key := range keys {
		if !strings.HasPrefix(key.Reveal(), "sk_"+p.mode+"_") {
			return ErrMismatch
		}
	}
	p.webhookMu.Lock()
	defer p.webhookMu.Unlock()
	p.webhookPrevious = append([]security.Secret(nil), keys...)
	return nil
}
func (p *Paystack) call(ctx context.Context, method, path string, in, out any) error {
	start := time.Now()
	err := p.doCall(ctx, method, path, in, out)
	outcome := "success"
	if err != nil {
		outcome = "unknown"
	}
	providerCalls.Add(ctx, 1, attribute.String("provider", "paystack"), attribute.String("outcome", outcome))
	providerDuration.Record(ctx, time.Since(start).Seconds(), attribute.String("provider", "paystack"))
	return err
}

var providerCalls = observability.NewCounter("payment-service", "payment.provider.requests", "Provider calls and unknown/error outcomes")
var providerDuration = observability.NewHistogram("payment-service", "payment.provider.duration", "Bounded provider request duration", "s")

func (p *Paystack) doCall(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if e != nil {
		return ErrUnknown
	}
	req.Header.Set("Authorization", "Bearer "+p.secret.Reveal())
	req.Header.Set("Content-Type", "application/json")
	if requestID := requestcontext.From(ctx).RequestID; requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	res, e := p.client.Do(req)
	if e != nil {
		return ErrUnknown
	}
	defer res.Body.Close()
	var envelope struct {
		Status bool            `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ErrUnknown
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&envelope); e != nil || !envelope.Status {
		return ErrUnknown
	}
	if out == nil {
		return nil
	}
	if e = json.Unmarshal(envelope.Data, out); e != nil {
		return ErrUnknown
	}
	return nil
}
func (p *Paystack) InitializeCharge(ctx context.Context, in ChargeRequest) (Initialized, error) {
	var out struct {
		Reference string `json:"reference"`
		URL       string `json:"authorization_url"`
	}
	e := p.call(ctx, "POST", "/transaction/initialize", map[string]any{"email": in.Email, "amount": strconv.FormatInt(in.AmountMinor, 10), "currency": in.Currency, "reference": in.Reference, "channels": in.Channels, "metadata": map[string]string{"intent_id": in.IntentID}}, &out)
	if e != nil {
		return Initialized{}, e
	}
	u, err := url.Parse(out.URL)
	if out.Reference != in.Reference || err != nil || u.Scheme != "https" || u.Host == "" {
		return Initialized{}, ErrMismatch
	}
	return Initialized{out.Reference, out.URL}, nil
}

type chargeWire struct {
	ID        int64           `json:"id"`
	Reference string          `json:"reference"`
	Status    string          `json:"status"`
	Currency  string          `json:"currency"`
	Domain    string          `json:"domain"`
	Amount    int64           `json:"amount"`
	Fees      int64           `json:"fees"`
	Metadata  json.RawMessage `json:"metadata"`
	Customer  struct {
		Integration int64  `json:"integration"`
		Code        string `json:"customer_code"`
	} `json:"customer"`
}

func (p *Paystack) normalize(ctx context.Context, in chargeWire) (Charge, error) {
	// Transaction responses may omit integration. Resolve it through an
	// authenticated customer read; never infer merchant ownership from a label.
	if in.Customer.Integration == 0 {
		if in.Customer.Code == "" {
			return Charge{}, ErrMismatch
		}
		customer, err := p.FetchCustomer(ctx, in.Customer.Code)
		if err != nil {
			return Charge{}, err
		}
		in.Customer.Integration = customer.MerchantID
	}
	c := Charge{Reference: in.Reference, Currency: in.Currency, Mode: in.Domain, AmountMinor: in.Amount, MerchantID: in.Customer.Integration, CustomerRef: in.Customer.Code, FeeMinor: in.Fees}
	var md struct {
		IntentID string `json:"intent_id"`
	}
	if len(in.Metadata) > 0 && in.Metadata[0] == '"' {
		var str string
		_ = json.Unmarshal(in.Metadata, &str)
		in.Metadata = []byte(str)
	}
	_ = json.Unmarshal(in.Metadata, &md)
	c.IntentID = md.IntentID
	if c.MerchantID != p.merchant || c.Mode != p.mode {
		return c, ErrMismatch
	}
	switch in.Status {
	case "success":
		c.State = "VERIFIED_SUCCESS"
	case "failed", "abandoned", "reversed":
		c.State = "VERIFIED_FAILURE"
	case "ongoing", "processing":
		c.State = "PROCESSING"
	case "pending", "queued":
		c.State = "PENDING"
	default:
		c.State = "UNKNOWN_OUTCOME"
	}
	return c, nil
}
func (p *Paystack) VerifyCharge(ctx context.Context, ref string) (Charge, error) {
	var c chargeWire
	e := p.call(ctx, "GET", "/transaction/verify/"+url.PathEscape(ref), nil, &c)
	if e != nil {
		return Charge{}, e
	}
	if c.Reference != ref {
		return Charge{}, ErrMismatch
	}
	return p.normalize(ctx, c)
}
func (p *Paystack) CancelCharge(context.Context, string) error { return ErrUnsupported }
func (p *Paystack) ReceiveWebhook(body []byte, signature string) (Callback, error) {
	sig, e := hex.DecodeString(signature)
	if e != nil {
		return Callback{}, ErrSignature
	}
	mac := hmac.New(sha512.New, []byte(p.secret.Reveal()))
	mac.Write(body)
	valid := hmac.Equal(mac.Sum(nil), sig)
	p.webhookMu.RLock()
	for _, key := range p.webhookPrevious {
		m := hmac.New(sha512.New, []byte(key.Reveal()))
		m.Write(body)
		valid = hmac.Equal(m.Sum(nil), sig) || valid
	}
	p.webhookMu.RUnlock()
	if !valid {
		return Callback{}, ErrSignature
	}
	var x struct {
		Event string `json:"event"`
		Data  struct {
			ID           int64  `json:"id"`
			MerchantNote string `json:"merchant_note"`
			Reference    string `json:"reference"`
			CustomerCode string `json:"customer_code"`
			Customer     struct {
				Code string `json:"customer_code"`
			} `json:"customer"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &x) != nil || x.Event == "" {
		return Callback{}, ErrSignature
	}
	ref := x.Data.Reference
	if strings.HasPrefix(x.Event, "refund.") {
		ref = x.Data.MerchantNote
		if ref == "" && x.Data.ID > 0 {
			ref = strconv.FormatInt(x.Data.ID, 10)
		}
	}
	if strings.HasPrefix(x.Event, "charge.dispute.") && x.Data.ID > 0 {
		ref = "dispute:" + strconv.FormatInt(x.Data.ID, 10)
	}
	if ref == "" {
		ref = x.Data.CustomerCode
	}
	if ref == "" {
		ref = x.Data.Customer.Code
	}
	return Callback{x.Event, ref}, nil
}
func (p *Paystack) CreateCustomer(ctx context.Context, in CustomerRequest) (Customer, error) {
	var x struct {
		Code        string `json:"customer_code"`
		Identified  bool   `json:"identified"`
		Integration int64  `json:"integration"`
		Domain      string `json:"domain"`
	}
	e := p.call(ctx, "POST", "/customer", in, &x)
	if e != nil {
		return Customer{}, e
	}
	if x.Code == "" || x.Integration != p.merchant || x.Domain != p.mode {
		return Customer{}, ErrMismatch
	}
	return Customer{x.Code, x.Identified, x.Integration, x.Domain}, nil
}
func (p *Paystack) FetchCustomer(ctx context.Context, code string) (Customer, error) {
	var x struct {
		Email       string `json:"email"`
		Code        string `json:"customer_code"`
		Identified  bool   `json:"identified"`
		Integration int64  `json:"integration"`
		Domain      string `json:"domain"`
	}
	e := p.call(ctx, "GET", "/customer/"+url.PathEscape(code), nil, &x)
	if e != nil {
		return Customer{}, e
	}
	if (x.Code != code && !strings.EqualFold(x.Email, code)) || x.Code == "" || x.Integration != p.merchant || x.Domain != p.mode {
		return Customer{}, ErrMismatch
	}
	return Customer{x.Code, x.Identified, x.Integration, x.Domain}, nil
}
func (p *Paystack) ValidateCustomer(ctx context.Context, code string, v Validation) error {
	return p.call(ctx, "POST", "/customer/"+url.PathEscape(code)+"/identification", v, nil)
}

type dvaWire struct {
	Assignment struct {
		Integration int64 `json:"integration"`
	} `json:"assignment"`
	ID       int64  `json:"id"`
	Active   bool   `json:"active"`
	Number   string `json:"account_number"`
	Currency string `json:"currency"`
	Bank     struct {
		Slug string `json:"slug"`
	} `json:"bank"`
	Customer struct {
		Code string `json:"customer_code"`
	} `json:"customer"`
}

func (p *Paystack) dva(x dvaWire) (VirtualAccount, error) {
	if x.ID <= 0 || x.Number == "" || x.Bank.Slug != "titan-paystack" || x.Customer.Code == "" || x.Currency != "NGN" || x.Assignment.Integration != p.merchant {
		return VirtualAccount{}, ErrMismatch
	}
	return VirtualAccount{ID: strconv.FormatInt(x.ID, 10), AccountNumber: x.Number, BankCode: x.Bank.Slug, CustomerRef: x.Customer.Code, Active: x.Active, MerchantID: p.merchant, Mode: p.mode}, nil
}
func (p *Paystack) AssignDVA(ctx context.Context, code string) (VirtualAccount, error) {
	var x dvaWire
	e := p.call(ctx, "POST", "/dedicated_account", map[string]string{"customer": code, "preferred_bank": "titan-paystack"}, &x)
	if e != nil {
		return VirtualAccount{}, e
	}
	if x.Customer.Code != code {
		return VirtualAccount{}, ErrMismatch
	}
	return p.dva(x)
}
func (p *Paystack) FetchDVA(ctx context.Context, id string) (VirtualAccount, error) {
	var x dvaWire
	e := p.call(ctx, "GET", "/dedicated_account/"+url.PathEscape(id), nil, &x)
	if e != nil {
		return VirtualAccount{}, e
	}
	if strconv.FormatInt(x.ID, 10) != id {
		return VirtualAccount{}, ErrMismatch
	}
	return p.dva(x)
}
func (p *Paystack) FindDVA(ctx context.Context, code string) (VirtualAccount, error) {
	var c struct {
		Code        string   `json:"customer_code"`
		Integration int64    `json:"integration"`
		Domain      string   `json:"domain"`
		DVA         *dvaWire `json:"dedicated_account"`
	}
	if e := p.call(ctx, "GET", "/customer/"+url.PathEscape(code), nil, &c); e != nil {
		return VirtualAccount{}, e
	}
	if c.Integration != p.merchant || c.Domain != p.mode || c.Code != code || c.DVA == nil {
		return VirtualAccount{}, ErrUnknown
	}
	c.DVA.Customer.Code = code
	return p.dva(*c.DVA)
}
func (p *Paystack) RequeryDVA(ctx context.Context, number, slug string) error {
	return p.call(ctx, "GET", "/dedicated_account/requery?account_number="+url.QueryEscape(number)+"&provider_slug="+url.QueryEscape(slug), nil, nil)
}
func (p *Paystack) DeactivateDVA(ctx context.Context, id string) error {
	return p.call(ctx, "DELETE", "/dedicated_account/"+url.PathEscape(id), nil, nil)
}
func (p *Paystack) FetchBalance(ctx context.Context) ([]MerchantBalance, error) {
	var x []struct {
		Currency string `json:"currency"`
		Balance  int64  `json:"balance"`
	}
	e := p.call(ctx, "GET", "/balance", nil, &x)
	if e != nil {
		return nil, e
	}
	bs := []MerchantBalance{}
	for _, b := range x {
		if b.Balance < 0 {
			return nil, ErrMismatch
		}
		bs = append(bs, MerchantBalance{b.Currency, b.Balance})
	}
	return bs, nil
}
func (p *Paystack) FetchSettlement(ctx context.Context, ref string) (Settlement, error) {
	var x []struct {
		ID       int64      `json:"id"`
		Merchant int64      `json:"integration"`
		Mode     string     `json:"domain"`
		Amount   int64      `json:"total_amount"`
		Fees     int64      `json:"total_fees"`
		Currency string     `json:"currency"`
		Status   string     `json:"status"`
		Date     *time.Time `json:"settlement_date"`
	}
	e := p.call(ctx, "GET", "/settlement?perPage=100", nil, &x)
	if e != nil {
		return Settlement{}, e
	}
	for _, r := range x {
		if strconv.FormatInt(r.ID, 10) == ref {
			if r.Merchant != p.merchant || r.Mode != p.mode {
				return Settlement{}, ErrMismatch
			}
			return Settlement{Reference: ref, AmountMinor: r.Amount, FeeMinor: r.Fees, Currency: r.Currency, State: r.Status, SettledAt: r.Date}, nil
		}
	}
	return Settlement{}, ErrUnknown
}
func (p *Paystack) ListTransactions(ctx context.Context, page string) ([]Charge, error) {
	if page == "" {
		page = "1"
	}
	if _, e := strconv.Atoi(page); e != nil {
		return nil, fmt.Errorf("invalid page")
	}
	var xs []chargeWire
	e := p.call(ctx, "GET", "/transaction?perPage=100&page="+url.QueryEscape(page), nil, &xs)
	if e != nil {
		return nil, e
	}
	out := []Charge{}
	for _, x := range xs {
		c, e := p.normalize(ctx, x)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, nil
}
