package channels

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

func fcmServer(t *testing.T, handle func(w http.ResponseWriter, body map[string]any)) *FCM {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/proj-1/messages:send" || r.Header.Get("Authorization") != "Bearer at-123" {
			t.Errorf("unexpected request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		handle(w, body)
	}))
	t.Cleanup(srv.Close)
	f, err := NewFCM(FCMConfig{ProjectID: "proj-1", BaseURL: srv.URL, Tokens: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "at-123"})})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func writeFCMError(w http.ResponseWriter, status int, grpc, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	details := `[]`
	if code != "" {
		details = fmt.Sprintf(`[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":%q}]`, code)
	}
	fmt.Fprintf(w, `{"error":{"code":%d,"message":%q,"status":%q,"details":%s}}`, status, msg, grpc, details)
}

func TestFCMSuccessAndPayload(t *testing.T) {
	f := fcmServer(t, func(w http.ResponseWriter, body map[string]any) {
		m := body["message"].(map[string]any)
		n := m["notification"].(map[string]any)
		if m["token"] != "device-token-abcdef" || n["title"] != "Hi" || n["body"] != "Body" || m["data"].(map[string]any)["k"] != "v" {
			t.Errorf("payload %v", body)
		}
		_, _ = io.WriteString(w, `{"name":"projects/proj-1/messages/0:123"}`)
	})
	res, err := f.SendPush(context.Background(), Message{DeliveryID: ids.New(), Destination: "device-token-abcdef", Subject: "Hi", Body: "Body", Data: map[string]string{"k": "v"}})
	if err != nil || res.ProviderMessageID != "projects/proj-1/messages/0:123" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestFCMErrorClassification(t *testing.T) {
	cases := []struct {
		name               string
		status             int
		grpc, code, msg    string
		retryAfter         string
		want               Kind
		wantCode           string
		wantRetryAfterSecs int
	}{
		{"unregistered", 404, "NOT_FOUND", "UNREGISTERED", "Requested entity was not found.", "", InvalidDestination, "FCM_UNREGISTERED", 0},
		{"sender mismatch", 403, "PERMISSION_DENIED", "SENDER_ID_MISMATCH", "", "", InvalidDestination, "FCM_SENDER_ID_MISMATCH", 0},
		{"bad token", 400, "INVALID_ARGUMENT", "INVALID_ARGUMENT", "The registration token is not a valid FCM registration token", "", InvalidDestination, "FCM_INVALID_ARGUMENT", 0},
		{"bad payload", 400, "INVALID_ARGUMENT", "INVALID_ARGUMENT", "Invalid JSON payload", "", Permanent, "FCM_INVALID_ARGUMENT", 0},
		{"quota", 429, "RESOURCE_EXHAUSTED", "QUOTA_EXCEEDED", "", "30", Transient, "FCM_QUOTA_EXCEEDED", 30},
		{"unavailable", 503, "UNAVAILABLE", "UNAVAILABLE", "", "7", Transient, "FCM_UNAVAILABLE", 7},
		{"internal", 500, "INTERNAL", "INTERNAL", "", "", Transient, "FCM_INTERNAL", 0},
		{"apns auth", 401, "UNAUTHENTICATED", "THIRD_PARTY_AUTH_ERROR", "", "", Permanent, "FCM_THIRD_PARTY_AUTH_ERROR", 0},
		{"oauth expired", 401, "UNAUTHENTICATED", "", "", "", Transient, "FCM_UNAUTHENTICATED", 0},
		{"gateway", 502, "", "", "", "", Transient, "FCM_HTTP_502", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fcmServer(t, func(w http.ResponseWriter, _ map[string]any) {
				if c.retryAfter != "" {
					w.Header().Set("Retry-After", c.retryAfter)
				}
				if c.grpc == "" {
					w.WriteHeader(c.status)
					return
				}
				writeFCMError(w, c.status, c.grpc, c.code, c.msg)
			})
			_, err := f.SendPush(context.Background(), Message{DeliveryID: ids.New(), Destination: "device-token-abcdef"})
			e := Classify(err)
			if e.Kind != c.want || e.Code != c.wantCode || e.RetryAfter != time.Duration(c.wantRetryAfterSecs)*time.Second || e.HTTPStatus != c.status {
				t.Fatalf("got %+v", e)
			}
		})
	}
}

func TestFCMNetworkAndAuthFailuresAreTransient(t *testing.T) {
	f, _ := NewFCM(FCMConfig{ProjectID: "p", BaseURL: "http://127.0.0.1:1", Tokens: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "x"}), Timeout: time.Second})
	if _, err := f.SendPush(context.Background(), Message{Destination: "device-token-abcdef"}); Classify(err).Kind != Transient {
		t.Fatalf("network: %v", err)
	}
	f, _ = NewFCM(FCMConfig{ProjectID: "p", Tokens: failingTokens{}})
	if _, err := f.SendPush(context.Background(), Message{Destination: "device-token-abcdef"}); Classify(err).Kind != Transient || strings.Contains(err.Error(), "secret") {
		t.Fatalf("token: %v", err)
	}
	if _, err := FCMTokenSourceFromJSON(context.Background(), []byte(`{"type":"nope","private_key":"secret"}`)); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid credentials must fail without echoing them: %v", err)
	}
}

type failingTokens struct{}

func (failingTokens) Token() (*oauth2.Token, error) { return nil, errors.New("secret material") }

func TestTwilio(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		_ = r.ParseForm()
		mu.Lock()
		got = append(got, r.Form.Get("To"))
		mu.Unlock()
		if u != "AC1" || p != "tok" || r.URL.Path != "/2010-04-01/Accounts/AC1/Messages.json" || r.Form.Get("From") != "+15550001111" {
			t.Errorf("bad request %s %s", r.URL.Path, u)
		}
		switch r.Form.Get("To") {
		case "+15550000001":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"sid":"SM123"}`)
		case "+15550000002":
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"code":21211,"message":"Invalid 'To' Phone Number"}`)
		case "+15550000003":
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"code":20429,"message":"Too Many Requests"}`)
		default:
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"code":20003,"message":"Authenticate"}`)
		}
	}))
	defer srv.Close()
	tw, err := NewTwilio(TwilioConfig{BaseURL: srv.URL, AccountSID: "AC1", AuthToken: security.NewSecret("tok"), From: "+15550001111"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if r, err := tw.SendSMS(ctx, Message{Destination: "+15550000001", Body: "hi"}); err != nil || r.ProviderMessageID != "SM123" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := tw.SendSMS(ctx, Message{Destination: "+15550000002"}); Classify(err).Kind != InvalidDestination || Classify(err).Code != "SMS_21211" {
		t.Fatalf("%v", err)
	}
	if _, err := tw.SendSMS(ctx, Message{Destination: "+15550000003"}); Classify(err).Kind != Transient || Classify(err).RetryAfter != 5*time.Second {
		t.Fatalf("%v", err)
	}
	if _, err := tw.SendSMS(ctx, Message{Destination: "+15550000004"}); Classify(err).Kind != Permanent || strings.Contains(err.Error(), "tok") {
		t.Fatalf("%v", err)
	}
	if _, err := NewTwilio(TwilioConfig{AccountSID: "AC1", AuthToken: security.NewSecret("t")}); err == nil {
		t.Fatal("sender required")
	}
}

// fakeSMTP is a minimal SMTP server for adapter tests.
type fakeSMTP struct {
	addr     string
	mu       sync.Mutex
	rcpt     []string
	data     []string
	rejectTo map[string]string // address -> reply line
}

func startSMTP(t *testing.T, starttls bool) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{addr: ln.Addr().String(), rejectTo: map[string]string{}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, starttls)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn, starttls bool) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			if starttls {
				w("250-fake")
				w("250 STARTTLS")
			} else {
				w("250-fake")
				w("250 8BITMIME")
			}
		case strings.HasPrefix(cmd, "MAIL FROM"):
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			addr := strings.Trim(strings.TrimSpace(line)[8:], "<> \r\n")
			f.mu.Lock()
			reply, bad := f.rejectTo[addr]
			if !bad {
				f.rcpt = append(f.rcpt, addr)
			}
			f.mu.Unlock()
			if bad {
				w(reply)
			} else {
				w("250 ok")
			}
		case cmd == "DATA":
			w("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = append(f.data, b.String())
			f.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestSMTP(t *testing.T) {
	srv := startSMTP(t, false)
	srv.rejectTo["nobody@example.com"] = "550 5.1.1 no such user"
	srv.rejectTo["busy@example.com"] = "451 4.3.0 try later"
	host, port, _ := net.SplitHostPort(srv.addr)
	var p int
	fmt.Sscan(port, &p)
	s, err := NewSMTP(SMTPConfig{Host: host, Port: p, From: "no-reply@cosmicforge.test", FromName: "CosmicForge", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := ids.New()
	res, err := s.SendEmail(ctx, Message{DeliveryID: id, Destination: "user@example.com", Subject: "Hello", Body: "Plain body", HTML: "<p>HTML</p>"})
	if err != nil || res.ProviderMessageID != "<"+id.String()+">" {
		t.Fatalf("%+v %v", res, err)
	}
	srv.mu.Lock()
	msg := srv.data[0]
	srv.mu.Unlock()
	for _, want := range []string{"Subject: Hello", "To: user@example.com", "multipart/alternative", "Plain body", "<p>HTML</p>", "Message-ID: <" + id.String() + "@cosmicforge.test>"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in\n%s", want, msg)
		}
	}
	if _, err := s.SendEmail(ctx, Message{Destination: "nobody@example.com", Body: "x"}); Classify(err).Kind != InvalidDestination {
		t.Fatalf("550 rcpt: %v", err)
	}
	if _, err := s.SendEmail(ctx, Message{Destination: "busy@example.com", Body: "x"}); Classify(err).Kind != Transient {
		t.Fatalf("451 rcpt: %v", err)
	}
	if _, err := s.SendEmail(ctx, Message{Destination: "not an address", Body: "x"}); Classify(err).Kind != InvalidDestination {
		t.Fatalf("bad address: %v", err)
	}
	strict, _ := NewSMTP(SMTPConfig{Host: host, Port: p, From: "no-reply@cosmicforge.test", RequireTLS: true})
	if _, err := strict.SendEmail(ctx, Message{Destination: "user@example.com", Body: "x"}); Classify(err).Kind != Permanent || Classify(err).Code != "SMTP_TLS_REQUIRED" {
		t.Fatalf("plaintext must be refused when TLS is required: %v", err)
	}
	down, _ := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "a@b.co", Timeout: time.Second})
	if _, err := down.SendEmail(ctx, Message{Destination: "user@example.com"}); Classify(err).Kind != Transient {
		t.Fatalf("connect failure: %v", err)
	}
}

func TestCapture(t *testing.T) {
	c := NewCapture("PUSH")
	c.FailNext(&Error{Kind: Transient, Code: "X"})
	if _, err := c.Send(context.Background(), Message{}); Classify(err).Code != "X" {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), Message{Body: "b"}); err != nil || len(c.Sent()) != 1 {
		t.Fatal("capture")
	}
	if Classify(errors.New("plain")).Kind != Transient {
		t.Fatal("unclassified errors are transient")
	}
}

func TestChannelAdaptersRouteToProviderInterfaces(t *testing.T) {
	c := NewCapture("SMS")
	for _, s := range []Sender{Push(c), Email(c), SMS(c)} {
		if _, err := s.Send(context.Background(), Message{Body: "x"}); err != nil || s.Name() != "capture-sms" {
			t.Fatalf("%s: %v", s.Name(), err)
		}
	}
	if Push(c).Channel() != "PUSH" || Email(c).Channel() != "EMAIL" || SMS(c).Channel() != "SMS" || len(c.Sent()) != 3 {
		t.Fatal("adapter channels")
	}
}

func TestCaptureFileAppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	c := &Capture{ChannelName: "EMAIL", File: path}
	for _, body := range []string{"Your code is 123456", "second"} {
		if _, err := c.SendEmail(context.Background(), Message{DeliveryID: ids.New(), Destination: "a@example.com", Subject: "s", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var first map[string]string
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || first["body"] != "Your code is 123456" || first["destination"] != "a@example.com" {
		t.Fatalf("capture file %q", raw)
	}
	if len(c.Sent()) != 2 {
		t.Fatal("in-memory capture")
	}
}

func TestCaptureFileIsPrivateAndNeverFollowsLinks(t *testing.T) {
	dir := t.TempDir()
	send := func(path string) error {
		c := &Capture{ChannelName: "EMAIL", File: path}
		_, err := c.SendEmail(context.Background(), Message{DeliveryID: ids.New(), Destination: "a@example.com", Body: "Your code is 654321"})
		return err
	}
	fresh := filepath.Join(dir, "fresh.jsonl")
	if err := PrepareCaptureFile(fresh); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("created mode %v (%v)", fi.Mode().Perm(), err)
	}
	if err := send(fresh); err != nil {
		t.Fatal(err)
	}

	// An existing file readable by others is refused, not silently used.
	wide := filepath.Join(dir, "wide.jsonl")
	if err := os.WriteFile(wide, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wide, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareCaptureFile(wide); err == nil || !strings.Contains(err.Error(), "group or others") {
		t.Fatalf("world-readable file accepted: %v", err)
	}
	if err := send(wide); err == nil {
		t.Fatal("send wrote to a world-readable file")
	}

	// A symbolic link (e.g. planted in a shared directory) is never followed.
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := PrepareCaptureFile(link); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink accepted: %v", err)
	}
	if err := send(link); err == nil {
		t.Fatal("send followed a symlink")
	}
	if raw, _ := os.ReadFile(target); string(raw) != "keep" {
		t.Fatalf("link target modified: %q", raw)
	}

	// Not a regular file.
	if err := PrepareCaptureFile(dir); err == nil {
		t.Fatal("directory accepted")
	}
	// Errors never carry the message body.
	if err := send(link); err != nil && strings.Contains(err.Error(), "654321") {
		t.Fatalf("error leaks the body: %v", err)
	}
}
