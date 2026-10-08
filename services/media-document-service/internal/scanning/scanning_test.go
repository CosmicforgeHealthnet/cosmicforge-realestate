package scanning

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
)

func TestDevScanner(t *testing.T) {
	v, err := DevScanner{}.Scan(context.Background(), strings.NewReader("clean pdf content"))
	if err != nil || !v.Clean {
		t.Fatal("clean")
	}
	// EICAR split across read boundaries is still detected.
	payload := strings.Repeat("a", 64<<10-10) + EICAR + "tail"
	v, err = DevScanner{}.Scan(context.Background(), strings.NewReader(payload))
	if err != nil || v.Clean || v.Signature == "" {
		t.Fatalf("eicar not detected: %+v %v", v, err)
	}
}

// fakeClamd implements enough of the clamd protocol for tests.
func fakeClamd(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				cmd, _ := r.ReadString(0)
				if cmd == "zPING\x00" {
					c.Write([]byte("PONG\x00"))
					return
				}
				var data bytes.Buffer
				for {
					var size [4]byte
					if _, err := io.ReadFull(r, size[:]); err != nil {
						return
					}
					n := binary.BigEndian.Uint32(size[:])
					if n == 0 {
						break
					}
					io.CopyN(&data, r, int64(n))
				}
				if bytes.Contains(data.Bytes(), []byte(EICAR)) {
					c.Write([]byte("stream: Win.Test.EICAR_HDB-1 FOUND\x00"))
				} else {
					c.Write([]byte("stream: OK\x00"))
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestClamAVProtocol(t *testing.T) {
	addr := fakeClamd(t)
	c := ClamAV{Addr: addr}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, err := c.Scan(context.Background(), strings.NewReader(strings.Repeat("x", 100<<10)))
	if err != nil || !v.Clean {
		t.Fatalf("clean: %+v %v", v, err)
	}
	v, err = c.Scan(context.Background(), strings.NewReader("prefix"+EICAR))
	if err != nil || v.Clean || v.Signature != "Win.Test.EICAR_HDB-1" {
		t.Fatalf("infected: %+v %v", v, err)
	}
	if _, err := (ClamAV{Addr: "127.0.0.1:1"}).Scan(context.Background(), strings.NewReader("x")); err == nil {
		t.Fatal("unreachable scanner must error, never report clean")
	}
}
