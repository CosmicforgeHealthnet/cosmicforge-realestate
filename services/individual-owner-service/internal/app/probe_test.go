package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadinessProbeCannotSucceedOnLivenessAlone(t *testing.T) {
	var ready atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			t.Error("readiness probe called another endpoint", r.URL.Path)
			w.WriteHeader(200)
			return
		}
		if !ready.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	if ProbeReadiness(addr) == nil {
		t.Fatal("unready server accepted")
	}
	ready.Store(true)
	if err := ProbeReadiness(addr); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(addr)
	if err := ProbeReadiness(":" + port); err != nil {
		t.Fatal("wildcard address probe failed", err)
	}
	if ProbeReadiness("invalid") == nil {
		t.Fatal("invalid address accepted")
	}
}
