// Package scanning abstracts malware scanning. Production uses ClamAV
// (clamd INSTREAM); development/test may use the EICAR-only scanner, which
// configuration refuses in production. A file is never READY without a
// CLEAN verdict.
package scanning

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Verdict is a scan outcome.
type Verdict struct {
	Clean     bool
	Signature string // malware signature when infected
}

// Scanner scans a byte stream.
type Scanner interface {
	Name() string
	Scan(ctx context.Context, r io.Reader) (Verdict, error)
}

// EICAR is the industry-standard antivirus test string.
const EICAR = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

// DevScanner detects only the EICAR test signature. Development/test only.
type DevScanner struct{}

func (DevScanner) Name() string { return "dev-eicar" }

func (DevScanner) Scan(ctx context.Context, r io.Reader) (Verdict, error) {
	needle := []byte(EICAR)
	buf := make([]byte, 64<<10)
	var carry []byte
	for {
		if err := ctx.Err(); err != nil {
			return Verdict{}, err
		}
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			if bytes.Contains(chunk, needle) {
				_, _ = io.Copy(io.Discard, r)
				return Verdict{Clean: false, Signature: "Eicar-Test-Signature"}, nil
			}
			if len(chunk) > len(needle) {
				carry = append([]byte(nil), chunk[len(chunk)-len(needle):]...)
			} else {
				carry = chunk
			}
		}
		if err == io.EOF {
			return Verdict{Clean: true}, nil
		}
		if err != nil {
			return Verdict{}, err
		}
	}
}

// ClamAV scans via a clamd TCP socket using the INSTREAM protocol.
type ClamAV struct {
	Addr    string
	Timeout time.Duration
}

func (c ClamAV) Name() string { return "clamav" }

// Scan streams r to clamd in chunks and parses the reply.
func (c ClamAV) Scan(ctx context.Context, r io.Reader) (Verdict, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return Verdict{}, fmt.Errorf("clamav dial: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return Verdict{}, err
	}
	buf := make([]byte, 32<<10)
	var size [4]byte
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			binary.BigEndian.PutUint32(size[:], uint32(n))
			if _, err := conn.Write(size[:]); err != nil {
				return Verdict{}, err
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return Verdict{}, err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return Verdict{}, rerr
		}
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return Verdict{}, err
	}
	reply, err := bufio.NewReader(conn).ReadString(0)
	if err != nil && !errors.Is(err, io.EOF) {
		return Verdict{}, err
	}
	reply = strings.TrimRight(reply, "\x00\n")
	switch {
	case strings.HasSuffix(reply, " OK"):
		return Verdict{Clean: true}, nil
	case strings.HasSuffix(reply, " FOUND"):
		sig := strings.TrimSuffix(strings.TrimPrefix(reply, "stream: "), " FOUND")
		return Verdict{Clean: false, Signature: sig}, nil
	}
	return Verdict{}, fmt.Errorf("clamav error: %s", reply)
}

// Ping checks clamd liveness.
func (c ClamAV) Ping(ctx context.Context) error {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("zPING\x00")); err != nil {
		return err
	}
	reply, err := bufio.NewReader(conn).ReadString(0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimRight(reply, "\x00\n") != "PONG" {
		return errors.New("clamav: unexpected ping reply")
	}
	return nil
}
