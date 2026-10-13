package app

import (
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
)

// ProbeReadiness checks the process's authoritative dependency/shutdown state.
// Container startup dependencies must not use liveness as a readiness signal.
func ProbeReadiness(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("invalid readiness probe address")
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return health.Probe("http://" + net.JoinHostPort(host, port) + "/ready")
}

// RunProbeIfRequested preserves the existing liveness command and adds the
// explicit readiness command without requiring database/key config for a probe.
func RunProbeIfRequested(args []string, addr string) {
	if len(args) < 2 || args[1] != "readinesscheck" {
		health.RunProbeIfRequested(args, addr)
		return
	}
	if err := ProbeReadiness(addr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
