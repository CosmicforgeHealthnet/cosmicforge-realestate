package e2e

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
)

// Local SMTP recipient fixture only. The REAL Notification SMTP adapter
// renders and sends messages; no real email delivery is certified here.
type smtpCapture struct {
	mu       sync.Mutex
	messages map[string]string
	addr     string
}

func startSMTPCapture(t *testing.T) *smtpCapture {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &smtpCapture{messages: map[string]string{}, addr: l.Addr().String()}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go c.receive(conn)
		}
	}()
	return c
}

func (c *smtpCapture) receive(conn net.Conn) {
	defer conn.Close()
	p := textproto.NewConn(conn)
	_ = p.PrintfLine("220 localhost TEST SMTP")
	var recipient string
	for {
		line, err := p.ReadLine()
		if err != nil {
			return
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			_ = p.PrintfLine("250 localhost")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			_ = p.PrintfLine("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			recipient = strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>")
			_ = p.PrintfLine("250 OK")
		case upper == "DATA":
			_ = p.PrintfLine("354 Send TEST message")
			raw, err := p.ReadDotBytes()
			if err != nil {
				return
			}
			msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
			if err != nil {
				return
			}
			var reader io.Reader = msg.Body
			content, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
			encoding := msg.Header.Get("Content-Transfer-Encoding")
			if strings.HasPrefix(content, "multipart/") {
				part, err := multipart.NewReader(msg.Body, params["boundary"]).NextPart()
				if err != nil {
					return
				}
				defer part.Close()
				reader = part
				encoding = part.Header.Get("Content-Transfer-Encoding")
			}
			if strings.EqualFold(encoding, "base64") {
				reader = base64.NewDecoder(base64.StdEncoding, reader)
			}
			body, err := io.ReadAll(reader)
			if err != nil {
				return
			}
			c.mu.Lock()
			c.messages[recipient] = string(body)
			c.mu.Unlock()
			_ = p.PrintfLine("250 TEST accepted")
		case upper == "QUIT":
			_ = p.PrintfLine("221 Bye")
			return
		default:
			_ = p.PrintfLine("250 OK")
		}
	}
}

func (c *smtpCapture) message(destination string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.messages[destination]
}
