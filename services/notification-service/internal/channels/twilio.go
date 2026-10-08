package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// TwilioConfig configures the OPTIONAL Twilio-compatible SMS adapter. Twilio
// is not a selected production provider; this adapter is used only when
// NOTIFY_SMS_PROVIDER=twilio is configured explicitly.
type TwilioConfig struct {
	BaseURL             string // default https://api.twilio.com
	AccountSID          string
	AuthToken           security.Secret
	From                string // sender number or alphanumeric ID
	MessagingServiceSID string // alternative to From
	HTTPClient          *http.Client
	Timeout             time.Duration
}

// Twilio sends SMS.
type Twilio struct{ cfg TwilioConfig }

// NewTwilio validates configuration.
func NewTwilio(cfg TwilioConfig) (*Twilio, error) {
	if cfg.AccountSID == "" || cfg.AuthToken.IsEmpty() || (cfg.From == "" && cfg.MessagingServiceSID == "") {
		return nil, errors.New("twilio: account sid, auth token and sender required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.twilio.com"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.Timeout}
	}
	return &Twilio{cfg: cfg}, nil
}

func (t *Twilio) Name() string { return "twilio" }

// Send delivers one SMS.
func (t *Twilio) SendSMS(ctx context.Context, m Message) (Result, error) {
	form := url.Values{"To": {m.Destination}, "Body": {m.Body}}
	if t.cfg.MessagingServiceSID != "" {
		form.Set("MessagingServiceSid", t.cfg.MessagingServiceSID)
	} else {
		form.Set("From", t.cfg.From)
	}
	ctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	u := strings.TrimRight(t.cfg.BaseURL, "/") + "/2010-04-01/Accounts/" + url.PathEscape(t.cfg.AccountSID) + "/Messages.json"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(t.cfg.AccountSID, t.cfg.AuthToken.Reveal())
	resp, err := t.cfg.HTTPClient.Do(req)
	if err != nil {
		return Result{}, &Error{Kind: Transient, Code: "SMS_NETWORK", Err: errors.New("request failed")}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var out struct {
		SID     string `json:"sid"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		return Result{ProviderMessageID: out.SID, HTTPStatus: resp.StatusCode}, nil
	}
	e := &Error{Code: fmt.Sprintf("SMS_%d", out.Code), HTTPStatus: resp.StatusCode, Err: fmt.Errorf("sms status %d", resp.StatusCode)}
	if out.Code == 0 {
		e.Code = "SMS_HTTP_" + strconv.Itoa(resp.StatusCode)
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			e.RetryAfter = time.Duration(n) * time.Second
		}
	}
	switch {
	case out.Code == 21211 || out.Code == 21614 || out.Code == 21610 || out.Code == 21612:
		e.Kind = InvalidDestination // invalid / non-mobile / unsubscribed / unreachable number
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		e.Kind = Transient
	default:
		e.Kind = Permanent
	}
	return Result{}, e
}
