package channels

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// SMTPConfig configures the OPTIONAL SMTP email adapter (works with
// any SMTP relay). SMTP is not assumed to be the final production email
// provider; it is used only when NOTIFY_EMAIL_PROVIDER=smtp is configured.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password security.Secret
	From     string // address
	FromName string
	// RequireTLS refuses to send without STARTTLS (always true outside tests).
	RequireTLS bool
	Timeout    time.Duration
	TLSConfig  *tls.Config
}

// SMTP sends email.
type SMTP struct{ cfg SMTPConfig }

// NewSMTP validates configuration.
func NewSMTP(cfg SMTPConfig) (*SMTP, error) {
	if cfg.Host == "" || cfg.Port == 0 {
		return nil, errors.New("smtp: host and port required")
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		return nil, errors.New("smtp: invalid from address")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &SMTP{cfg: cfg}, nil
}

func (s *SMTP) Name() string { return "smtp" }

func (s *SMTP) build(m Message) ([]byte, error) {
	var buf bytes.Buffer
	from := (&mail.Address{Name: s.cfg.FromName, Address: s.cfg.From}).String()
	domainPart := s.cfg.From[strings.LastIndex(s.cfg.From, "@")+1:]
	h := textproto.MIMEHeader{}
	h.Set("From", from)
	h.Set("To", m.Destination)
	h.Set("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h.Set("Date", time.Now().UTC().Format(time.RFC1123Z))
	h.Set("Message-ID", fmt.Sprintf("<%s@%s>", m.DeliveryID, domainPart))
	h.Set("MIME-Version", "1.0")
	h.Set("Auto-Submitted", "auto-generated")
	mw := multipart.NewWriter(&buf)
	h.Set("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	var head bytes.Buffer
	for _, k := range []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "Auto-Submitted", "Content-Type"} {
		fmt.Fprintf(&head, "%s: %s\r\n", k, h.Get(k))
	}
	head.WriteString("\r\n")
	part := func(ct, body string) error {
		w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {ct + "; charset=utf-8"}, "Content-Transfer-Encoding": {"8bit"}})
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n")))
		return err
	}
	if err := part("text/plain", m.Body); err != nil {
		return nil, err
	}
	if m.HTML != "" {
		if err := part("text/html", m.HTML); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append(head.Bytes(), buf.Bytes()...), nil
}

// Send delivers one email.
func (s *SMTP) SendEmail(ctx context.Context, m Message) (Result, error) {
	addr, err := mail.ParseAddress(m.Destination)
	if err != nil {
		return Result{}, &Error{Kind: InvalidDestination, Code: "SMTP_INVALID_ADDRESS", Err: errors.New("invalid recipient address")}
	}
	msg, err := s.build(m)
	if err != nil {
		return Result{}, &Error{Kind: Permanent, Code: "SMTP_BUILD", Err: err}
	}
	d := net.Dialer{Timeout: s.cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.cfg.Host, fmt.Sprint(s.cfg.Port)))
	if err != nil {
		return Result{}, &Error{Kind: Transient, Code: "SMTP_CONNECT", Err: errors.New("connect failed")}
	}
	_ = conn.SetDeadline(time.Now().Add(s.cfg.Timeout))
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return Result{}, classifySMTP(err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		tc := s.cfg.TLSConfig
		if tc == nil {
			tc = &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
		}
		if err := c.StartTLS(tc); err != nil {
			return Result{}, &Error{Kind: Transient, Code: "SMTP_TLS", Err: errors.New("starttls failed")}
		}
	} else if s.cfg.RequireTLS {
		return Result{}, &Error{Kind: Permanent, Code: "SMTP_TLS_REQUIRED", Err: errors.New("server does not offer STARTTLS")}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password.Reveal(), s.cfg.Host)); err != nil {
			return Result{}, classifySMTP(err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return Result{}, classifySMTP(err)
	}
	if err := c.Rcpt(addr.Address); err != nil {
		e := classifySMTP(err)
		if e.Kind == Permanent && e.HTTPStatus >= 550 && e.HTTPStatus <= 553 {
			e.Kind = InvalidDestination
		}
		return Result{}, e
	}
	w, err := c.Data()
	if err != nil {
		return Result{}, classifySMTP(err)
	}
	if _, err := w.Write(msg); err != nil {
		return Result{}, &Error{Kind: Transient, Code: "SMTP_DATA", Err: errors.New("write failed")}
	}
	if err := w.Close(); err != nil {
		return Result{}, classifySMTP(err)
	}
	_ = c.Quit()
	return Result{ProviderMessageID: fmt.Sprintf("<%s>", m.DeliveryID), HTTPStatus: 250}, nil
}

func classifySMTP(err error) *Error {
	var tp *textproto.Error
	if errors.As(err, &tp) {
		e := &Error{Code: fmt.Sprintf("SMTP_%d", tp.Code), HTTPStatus: tp.Code, Err: errors.New("smtp rejected")}
		if tp.Code >= 400 && tp.Code < 500 {
			e.Kind = Transient
		} else {
			e.Kind = Permanent
		}
		return e
	}
	return &Error{Kind: Transient, Code: "SMTP_IO", Err: errors.New("smtp i/o failure")}
}
