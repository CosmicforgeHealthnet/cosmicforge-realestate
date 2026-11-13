package cfmeet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// pinned is the subset of CF Meet's published Messaging contract the client
// relies on (contracts/external/cf-meet/messaging-v1.subset.json).
type pinned struct {
	Source struct {
		Version string `json:"version"`
	} `json:"source"`
	Operations map[string]struct {
		Method      string         `json:"method"`
		Path        string         `json:"path"`
		RequestBody map[string]any `json:"requestBody"`
		Parameters  []struct {
			Name     string         `json:"name"`
			In       string         `json:"in"`
			Required bool           `json:"required"`
			Schema   map[string]any `json:"schema"`
		} `json:"parameters"`
		Security []string `json:"security"`
	} `json:"operations"`
}

func loadPinned(t *testing.T) pinned {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "contracts", "external", "cf-meet", "messaging-v1.subset.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p pinned
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if "messaging/v"+p.Source.Version != ContractVersion {
		t.Fatalf("pinned contract %s does not match client %s", p.Source.Version, ContractVersion)
	}
	return p
}

// validate is a small JSON Schema subset validator covering the keywords used
// by the pinned operations (type, required, properties, additionalProperties,
// enum, const, pattern, oneOf, anyOf, allOf, not, if/then/else).
func validate(schema map[string]any, v any) error {
	if schema == nil {
		return nil
	}
	if typ, ok := schema["type"].(string); ok {
		switch typ {
		case "object":
			if _, ok := v.(map[string]any); !ok {
				return fmt.Errorf("want object")
			}
		case "string":
			if _, ok := v.(string); !ok {
				return fmt.Errorf("want string")
			}
		case "integer", "number":
			if _, ok := v.(float64); !ok {
				return fmt.Errorf("want number")
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("want boolean")
			}
		}
	}
	if c, ok := schema["const"]; ok && c != v {
		return fmt.Errorf("want const %v", c)
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			found = found || e == v
		}
		if !found {
			return fmt.Errorf("%v not in enum", v)
		}
	}
	if p, ok := schema["pattern"].(string); ok {
		if s, ok := v.(string); ok && !regexp.MustCompile(p).MatchString(s) {
			return fmt.Errorf("%q does not match %s", s, p)
		}
	}
	if obj, ok := v.(map[string]any); ok {
		props, _ := schema["properties"].(map[string]any)
		for _, r := range asList(schema["required"]) {
			if _, ok := obj[r.(string)]; !ok {
				return fmt.Errorf("missing %s", r)
			}
		}
		for k, val := range obj {
			sub, known := props[k].(map[string]any)
			if !known {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("property %s not allowed", k)
				}
				continue
			}
			if err := validate(sub, val); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	}
	for _, s := range asList(schema["allOf"]) {
		if err := validate(s.(map[string]any), v); err != nil {
			return err
		}
	}
	if list := asList(schema["oneOf"]); len(list) > 0 {
		n := 0
		for _, s := range list {
			if validate(s.(map[string]any), v) == nil {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("oneOf matched %d", n)
		}
	}
	if list := asList(schema["anyOf"]); len(list) > 0 {
		ok := false
		for _, s := range list {
			ok = ok || validate(s.(map[string]any), v) == nil
		}
		if !ok {
			return fmt.Errorf("anyOf matched none")
		}
	}
	if not, ok := schema["not"].(map[string]any); ok && validate(not, v) == nil {
		return fmt.Errorf("matched not")
	}
	if cond, ok := schema["if"].(map[string]any); ok {
		branch := "else"
		if validate(cond, v) == nil {
			branch = "then"
		}
		if s, ok := schema[branch].(map[string]any); ok {
			if err := validate(s, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func asList(v any) []any { l, _ := v.([]any); return l }

// contractServer is a contract double: it enforces the pinned CF Meet schema
// and idempotency semantics. It is not CF Meet and does not count as process
// E2E evidence.
type contractServer struct {
	t       *testing.T
	p       pinned
	mu      sync.Mutex
	byKey   map[string]string
	members map[string]map[string]bool
	fail    int
	creates int
}

func (s *contractServer) op(name string, r *http.Request, body any) {
	o := s.p.Operations[name]
	if r.Method != o.Method {
		s.t.Errorf("%s: method %s", name, r.Method)
	}
	if len(o.Security) == 0 || o.Security[0] != "customerCredential" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		s.t.Errorf("%s: customerCredential bearer required", name)
	}
	for _, prm := range o.Parameters {
		if prm.In != "header" {
			continue
		}
		val := r.Header.Get(prm.Name)
		if prm.Required && val == "" {
			s.t.Errorf("%s: missing header %s", name, prm.Name)
		}
		if val != "" {
			if err := validate(prm.Schema, val); err != nil {
				s.t.Errorf("%s: header %s: %v", name, prm.Name, err)
			}
		}
	}
	if o.RequestBody != nil {
		if err := validate(o.RequestBody, body); err != nil {
			s.t.Errorf("%s: body violates CF Meet contract: %v", name, err)
		}
	}
}

func (s *contractServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != 0 {
		w.WriteHeader(s.fail)
		_, _ = io.WriteString(w, `{"error":{"code":"PRINCIPAL_NOT_AUTHORIZED","message":"no","requestId":null}}`)
		return
	}
	var body any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	conv := func(id, state string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"conversationId": id, "lifecycleState": state, "contextClass": "ENTERPRISE"})
	}
	switch {
	case r.Method == http.MethodPost && len(parts) == 2:
		s.op("createConversation", r, body)
		key := r.Header.Get("Idempotency-Key")
		if id, ok := s.byKey[key]; ok {
			w.WriteHeader(http.StatusOK)
			conv(id, "ACTIVE")
			return
		}
		s.creates++
		id := strings.ToLower(ids.New().String())
		s.byKey[key] = id
		s.members[id] = map[string]bool{}
		w.WriteHeader(http.StatusCreated)
		conv(id, "ACTIVE")
	case r.Method == http.MethodGet && len(parts) == 3:
		s.op("getConversation", r, nil)
		if _, ok := s.members[parts[2]]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		conv(parts[2], "ACTIVE")
	case r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "members":
		s.op("addMember", r, body)
		p := body.(map[string]any)["principalId"].(string)
		if s.members[parts[2]][p] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		s.members[parts[2]][p] = true
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"principalId":"`+p+`","state":"ACTIVE"}`)
	case r.Method == http.MethodDelete && len(parts) == 5:
		s.op("removeMember", r, nil)
		if reason := r.URL.Query().Get("reason"); reason != "" && !regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,47}$`).MatchString(reason) {
			s.t.Errorf("removeMember: reason %q violates contract", reason)
		}
		if !s.members[parts[2]][parts[4]] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(s.members[parts[2]], parts[4])
		_, _ = io.WriteString(w, `{"principalId":"`+parts[4]+`","state":"REMOVED"}`)
	default:
		s.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

type mapPrincipals map[ids.ID]string

func (m mapPrincipals) Principal(_ context.Context, a ids.ID) (string, bool, error) {
	p, ok := m[a]
	return p, ok, nil
}

func TestClientSpeaksPinnedCFMeetMessagingContract(t *testing.T) {
	srv := &contractServer{t: t, p: loadPinned(t), byKey: map[string]string{}, members: map[string]map[string]bool{}}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	mapped, unmapped := ids.New(), ids.New()
	principal := strings.ToLower(ids.New().String())
	c, err := NewClient(Config{BaseURL: ts.URL, Credential: "test-credential", ActingPrincipal: strings.ToLower(ids.New().String()), Principals: mapPrincipals{mapped: principal}, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req := CFMeetContextRequest{ContextID: ids.New(), OrganisationID: ids.New(), PropertyID: ids.New(), Subject: "Guest arrival — private subject", Participants: []ids.ID{mapped, unmapped}}
	got, err := c.EnsureContext(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConversationRef == "" || got.State != "ACTIVE" || len(got.PendingParticipants) != 1 || got.PendingParticipants[0] != unmapped {
		t.Fatalf("ensure = %+v", got)
	}
	// Idempotent replay: same conversation, membership conflict tolerated.
	again, err := c.EnsureContext(ctx, req)
	if err != nil || again.ConversationRef != got.ConversationRef || srv.creates != 1 {
		t.Fatalf("replay = %+v err=%v creates=%d", again, err, srv.creates)
	}
	read, err := c.ReadContext(ctx, got.ConversationRef)
	if err != nil || read.ConversationRef != got.ConversationRef {
		t.Fatalf("read = %+v err=%v", read, err)
	}
	if err := c.RevokeParticipant(ctx, got.ConversationRef, mapped); err != nil || srv.members[got.ConversationRef][principal] {
		t.Fatalf("revoke err=%v", err)
	}
	if err := c.RevokeParticipant(ctx, got.ConversationRef, mapped); err != nil {
		t.Fatalf("second revoke must be idempotent: %v", err)
	}
	if err := c.RevokeParticipant(ctx, got.ConversationRef, unmapped); err != nil {
		t.Fatalf("unmapped revoke: %v", err)
	}
	srv.fail = http.StatusForbidden
	if _, err := c.EnsureContext(ctx, CFMeetContextRequest{ContextID: ids.New()}); !errors.Is(err, ErrRejected) {
		t.Fatalf("403 must be a definite rejection, got %v", err)
	}
	srv.fail = http.StatusServiceUnavailable
	if _, err := c.EnsureContext(ctx, CFMeetContextRequest{ContextID: ids.New()}); err == nil || errors.Is(err, ErrRejected) {
		t.Fatalf("503 must stay retryable, got %v", err)
	}
}

func TestClientConfigurationFailsClosed(t *testing.T) {
	principal := strings.ToLower(ids.New().String())
	for name, cfg := range map[string]Config{
		"plain http":       {BaseURL: "http://cfmeet.example.com", Credential: "x", ActingPrincipal: principal},
		"insecure remote":  {BaseURL: "http://cfmeet.example.com", Credential: "x", ActingPrincipal: principal, AllowInsecure: true},
		"no credential":    {BaseURL: "https://api.cfmeet.com", ActingPrincipal: principal},
		"bad principal":    {BaseURL: "https://api.cfmeet.com", Credential: "x", ActingPrincipal: "bot"},
		"forbidden class":  {BaseURL: "https://api.cfmeet.com", Credential: "x", ActingPrincipal: principal, ContextClass: "DIRECT"},
		"invalid base url": {BaseURL: "::", Credential: "x", ActingPrincipal: principal},
	} {
		if _, err := NewClient(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewClient(Config{BaseURL: "https://api.cfmeet.com", Credential: "x", ActingPrincipal: principal}); err != nil {
		t.Fatal(err)
	}
}
