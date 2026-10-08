package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// FCMScope is the OAuth scope for the FCM HTTP v1 API.
const FCMScope = "https://www.googleapis.com/auth/firebase.messaging"

// FCMConfig configures the Firebase Cloud Messaging HTTP v1 adapter. Firebase
// is used only as a push transport; it is never a business data store.
type FCMConfig struct {
	ProjectID  string
	BaseURL    string // default https://fcm.googleapis.com
	Tokens     oauth2.TokenSource
	HTTPClient *http.Client
	Timeout    time.Duration
}

// FCM sends push notifications, one request per device token (FCM v1 has no
// multicast; per-token requests give exact per-device error handling).
type FCM struct{ cfg FCMConfig }

// NewFCM validates configuration.
func NewFCM(cfg FCMConfig) (*FCM, error) {
	if cfg.ProjectID == "" || cfg.Tokens == nil {
		return nil, errors.New("fcm: project and credentials required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://fcm.googleapis.com"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.Timeout}
	}
	return &FCM{cfg: cfg}, nil
}

// FCMTokenSourceFromJSON builds a token source from service-account JSON
// injected through secrets management.
func FCMTokenSourceFromJSON(ctx context.Context, credentialsJSON []byte) (oauth2.TokenSource, error) {
	creds, err := google.CredentialsFromJSONWithType(ctx, credentialsJSON, google.ServiceAccount, FCMScope)
	if err != nil {
		return nil, errors.New("fcm: invalid service account credentials")
	}
	return creds.TokenSource, nil
}

func (f *FCM) Name() string { return "fcm" }

type fcmMessage struct {
	Message struct {
		Token        string            `json:"token"`
		Notification map[string]string `json:"notification,omitempty"`
		Data         map[string]string `json:"data,omitempty"`
		Android      map[string]any    `json:"android,omitempty"`
		APNS         map[string]any    `json:"apns,omitempty"`
	} `json:"message"`
}

type fcmError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type      string `json:"@type"`
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

// Send delivers one push message.
func (f *FCM) SendPush(ctx context.Context, m Message) (Result, error) {
	var body fcmMessage
	body.Message.Token = m.Destination
	body.Message.Notification = map[string]string{"title": m.Subject, "body": m.Body}
	body.Message.Data = m.Data
	body.Message.Android = map[string]any{"priority": "high"}
	body.Message.APNS = map[string]any{"headers": map[string]string{"apns-priority": "10"}}
	payload, _ := json.Marshal(body)
	tok, err := f.cfg.Tokens.Token()
	if err != nil {
		return Result{}, &Error{Kind: Transient, Code: "FCM_AUTH", Err: errors.New("oauth token unavailable")}
	}
	ctx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
	defer cancel()
	url := strings.TrimRight(f.cfg.BaseURL, "/") + "/v1/projects/" + f.cfg.ProjectID + "/messages:send"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := f.cfg.HTTPClient.Do(req)
	if err != nil {
		return Result{}, &Error{Kind: Transient, Code: "FCM_NETWORK", Err: errors.New("request failed")}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		var ok struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(b, &ok)
		return Result{ProviderMessageID: ok.Name, HTTPStatus: resp.StatusCode}, nil
	}
	return Result{}, classifyFCM(resp, b)
}

func classifyFCM(resp *http.Response, body []byte) *Error {
	var fe fcmError
	_ = json.Unmarshal(body, &fe)
	code := fe.Error.Status
	for _, d := range fe.Error.Details {
		if strings.HasSuffix(d.Type, "FcmError") && d.ErrorCode != "" {
			code = d.ErrorCode
		}
	}
	if code == "" {
		code = "HTTP_" + strconv.Itoa(resp.StatusCode)
	}
	e := &Error{Code: "FCM_" + code, HTTPStatus: resp.StatusCode, Err: fmt.Errorf("fcm status %d", resp.StatusCode)}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			e.RetryAfter = time.Duration(n) * time.Second
		}
	}
	switch code {
	case "UNREGISTERED", "SENDER_ID_MISMATCH":
		e.Kind = InvalidDestination
	case "INVALID_ARGUMENT":
		e.Kind = Permanent
		if strings.Contains(strings.ToLower(fe.Error.Message), "registration token") {
			e.Kind = InvalidDestination
		}
	case "QUOTA_EXCEEDED", "UNAVAILABLE", "INTERNAL":
		e.Kind = Transient
	case "THIRD_PARTY_AUTH_ERROR":
		e.Kind = Permanent
	default:
		switch {
		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
			e.Kind = Transient
		default:
			e.Kind = Permanent
		}
	}
	return e
}
