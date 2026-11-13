package cfmeet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// ContractVersion is the CF Meet public Messaging contract this client targets.
const ContractVersion = "messaging/v1.16.0"

// Namespace is the opaque CF Meet ContextRef namespace for Business contexts.
const Namespace = "cosmicforge-realestate-business"

var (
	namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	referencePattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,256}$`)
	uuidPattern      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	contextClasses   = map[string]bool{"TRANSACTION": true, "SUPPORT": true, "ENTERPRISE": true, "GROUP": true}
)

// Principals maps a Real Estate account to its CF Meet (Platform Access)
// principal in the configured environment. CF Meet's Platform Access owns
// principal issuance; Business Operations never creates principals.
type Principals interface {
	Principal(ctx context.Context, account ids.ID) (principalID string, ok bool, err error)
}

// Config configures the authenticated CF Meet Messaging client.
type Config struct {
	BaseURL string
	// Credential is the CF Meet API credential issued by Platform Access for the
	// Business Operations service account (scopes conversations.create and
	// conversations.manage). It is a secret and is never logged.
	Credential string
	// ActingPrincipal is the BOT_OR_SERVICE principal that owns Business
	// conversation contexts.
	ActingPrincipal string
	ContextClass    string
	Principals      Principals
	HTTP            *http.Client
	AllowInsecure   bool
}

// Client implements API against CF Meet's public Messaging contract.
type Client struct {
	base       *url.URL
	credential string
	acting     string
	class      string
	principals Principals
	http       *http.Client
}

// NewClient validates configuration; plain HTTP is accepted only for loopback
// hosts when AllowInsecure is set (tests and local process E2E).
func NewClient(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || u.Host == "" {
		return nil, errors.New("cfmeet: invalid base URL")
	}
	if u.Scheme != "https" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !cfg.AllowInsecure || u.Scheme != "http" || !(host == "localhost" || ip != nil && ip.IsLoopback()) {
			return nil, errors.New("cfmeet: base URL must use https")
		}
	}
	if strings.TrimSpace(cfg.Credential) == "" {
		return nil, errors.New("cfmeet: API credential required")
	}
	if !uuidPattern.MatchString(cfg.ActingPrincipal) {
		return nil, errors.New("cfmeet: acting principal must be a UUID")
	}
	class := cfg.ContextClass
	if class == "" {
		class = "ENTERPRISE"
	}
	if !contextClasses[class] {
		return nil, fmt.Errorf("cfmeet: context class %q not allowed", class)
	}
	h := cfg.HTTP
	if h == nil {
		h = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{base: u, credential: cfg.Credential, acting: strings.ToLower(cfg.ActingPrincipal), class: class, principals: cfg.Principals, http: h}, nil
}

type contextRef struct {
	Namespace string `json:"namespace"`
	Reference string `json:"reference"`
}

type createConversation struct {
	ContextClass string     `json:"contextClass"`
	Context      contextRef `json:"context"`
}

type conversation struct {
	ConversationID string `json:"conversationId"`
	LifecycleState string `json:"lifecycleState"`
}

// Reference returns the opaque ContextRef reference for a Business context.
func Reference(contextID ids.ID) string { return "communication-context:" + contextID.String() }

// EnsureContext creates (or replays) the CF Meet conversation for a Business
// communication context and admits the participants that have principals.
func (c *Client) EnsureContext(ctx context.Context, in CFMeetContextRequest) (CFMeetContext, error) {
	if in.ContextID.IsZero() {
		return CFMeetContext{}, fmt.Errorf("%w: context id required", ErrRejected)
	}
	ref := contextRef{Namespace: Namespace, Reference: Reference(in.ContextID)}
	if !namespacePattern.MatchString(ref.Namespace) || !referencePattern.MatchString(ref.Reference) {
		return CFMeetContext{}, fmt.Errorf("%w: invalid context reference", ErrRejected)
	}
	var conv conversation
	headers := map[string]string{"CF-Acting-Principal": c.acting, "Idempotency-Key": "business-context-" + in.ContextID.String()}
	// Subject, property and reservation stay in Business Operations: CF Meet
	// receives only the opaque context reference.
	if err := c.do(ctx, http.MethodPost, "/v1/conversations", headers, createConversation{ContextClass: c.class, Context: ref}, &conv); err != nil {
		return CFMeetContext{}, err
	}
	if !uuidPattern.MatchString(conv.ConversationID) {
		return CFMeetContext{}, errors.New("cfmeet: conversation readback missing conversationId")
	}
	out := CFMeetContext{ConversationRef: conv.ConversationID, State: conv.LifecycleState}
	for _, account := range in.Participants {
		principal, ok, err := c.principal(ctx, account)
		if err != nil {
			return CFMeetContext{}, err
		}
		if !ok {
			out.PendingParticipants = append(out.PendingParticipants, account)
			continue
		}
		err = c.do(ctx, http.MethodPost, "/v1/conversations/"+url.PathEscape(conv.ConversationID)+"/members", map[string]string{"CF-Acting-Principal": c.acting}, map[string]string{"principalId": principal, "role": "MEMBER"}, nil)
		if err != nil && !isConflict(err) {
			return CFMeetContext{}, err
		}
	}
	return out, nil
}

// ReadContext reads the authoritative conversation state.
func (c *Client) ReadContext(ctx context.Context, conversationRef string) (CFMeetContext, error) {
	if !uuidPattern.MatchString(conversationRef) {
		return CFMeetContext{}, fmt.Errorf("%w: invalid conversation reference", ErrRejected)
	}
	var conv conversation
	if err := c.do(ctx, http.MethodGet, "/v1/conversations/"+url.PathEscape(conversationRef), map[string]string{"CF-Acting-Principal": c.acting}, nil, &conv); err != nil {
		return CFMeetContext{}, err
	}
	return CFMeetContext{ConversationRef: conv.ConversationID, State: conv.LifecycleState}, nil
}

// RevokeParticipant removes a participant whose Business authorization ended.
func (c *Client) RevokeParticipant(ctx context.Context, conversationRef string, account ids.ID) error {
	if !uuidPattern.MatchString(conversationRef) {
		return fmt.Errorf("%w: invalid conversation reference", ErrRejected)
	}
	principal, ok, err := c.principal(ctx, account)
	if err != nil {
		return err
	}
	if !ok {
		return nil // never admitted, nothing to revoke
	}
	err = c.do(ctx, http.MethodDelete, "/v1/conversations/"+url.PathEscape(conversationRef)+"/members/"+url.PathEscape(principal)+"?reason=BUSINESS_ACCESS_REVOKED", map[string]string{"CF-Acting-Principal": c.acting}, nil, nil)
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) principal(ctx context.Context, account ids.ID) (string, bool, error) {
	if c.principals == nil {
		return "", false, nil
	}
	p, ok, err := c.principals.Principal(ctx, account)
	if err != nil || !ok {
		return "", false, err
	}
	if !uuidPattern.MatchString(p) {
		return "", false, fmt.Errorf("%w: principal for account is not a UUID", ErrRejected)
	}
	return strings.ToLower(p), true, nil
}

// StatusError is a CF Meet error envelope response.
type StatusError struct {
	Status int
	Code   string
	Reason string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("cfmeet: HTTP %d %s %s", e.Status, e.Code, e.Reason)
}

// Unwrap classifies definite refusals as ErrRejected; 429/5xx stay transient.
func (e *StatusError) Unwrap() error {
	if e.Status >= 400 && e.Status < 500 && e.Status != http.StatusTooManyRequests && e.Status != http.StatusRequestTimeout {
		return ErrRejected
	}
	return nil
}

func isConflict(err error) bool {
	var s *StatusError
	return errors.As(err, &s) && s.Status == http.StatusConflict
}

func isNotFound(err error) bool {
	var s *StatusError
	return errors.As(err, &s) && s.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, headers map[string]string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	target := *c.base
	p, q, _ := strings.Cut(path, "?")
	target.Path = c.base.Path + p
	target.RawQuery = q
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cfmeet: transport: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("cfmeet: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var envelope struct {
			Error struct {
				Code   string `json:"code"`
				Reason string `json:"reason"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return &StatusError{Status: resp.StatusCode, Code: envelope.Error.Code, Reason: envelope.Error.Reason}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("cfmeet: decode response: %w", err)
	}
	return nil
}
