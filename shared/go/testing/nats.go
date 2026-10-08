package testkit

import (
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// NATSServer is an embedded NATS server with JetStream for tests. It needs no
// external infrastructure and supports restart to simulate broker outages.
type NATSServer struct {
	t      testing.TB
	opts   server.Options
	server *server.Server
	URL    string
}

// StartNATS starts an embedded JetStream server on a random port.
func StartNATS(t testing.TB) *NATSServer {
	t.Helper()
	opts := server.Options{
		Host:      "127.0.0.1",
		Port:      server.RANDOM_PORT,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	}
	n := &NATSServer{t: t, opts: opts}
	n.start()
	// Pin the chosen port so restarts reuse it (clients reconnect).
	n.opts.Port = n.server.Addr().(*net.TCPAddr).Port
	t.Cleanup(n.Shutdown)
	return n
}

func (n *NATSServer) start() {
	s, err := server.NewServer(&n.opts)
	if err != nil {
		n.t.Fatalf("nats server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		n.t.Fatal("nats server not ready")
	}
	n.server = s
	n.URL = s.ClientURL()
}

// Shutdown stops the server (simulates broker outage).
func (n *NATSServer) Shutdown() {
	if n.server != nil {
		n.server.Shutdown()
		n.server.WaitForShutdown()
		n.server = nil
	}
}

// Restart starts the server again on the same port and store directory.
func (n *NATSServer) Restart() {
	n.Shutdown()
	n.start()
}
