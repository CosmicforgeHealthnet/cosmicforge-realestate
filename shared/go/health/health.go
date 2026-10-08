// Package health implements the platform health contract required on every
// deployable component:
//
//	GET /health   process liveness (no dependency checks)
//	GET /ready    able to accept traffic: required dependencies reachable and
//	              not shutting down; 503 otherwise
//	GET /version  service name, version, commit, build time, environment
//
// Readiness responses list check names and pass/fail only; dependency error
// details are logged, never returned.
package health

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
)

// Check is a readiness dependency check.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Handler serves the health endpoints.
type Handler struct {
	service     string
	environment string
	checks      []Check
	timeout     time.Duration
	shutting    atomic.Bool
}

// New creates a handler. timeout bounds all readiness checks (default 2s).
func New(service, environment string, timeout time.Duration, checks ...Check) *Handler {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Handler{service: service, environment: environment, checks: checks, timeout: timeout}
}

// SetShuttingDown flips readiness to 503 so load balancers drain the instance.
func (h *Handler) SetShuttingDown() { h.shutting.Store(true) }

// Register mounts the three endpoints on mux-like routers.
func (h *Handler) Register(mux interface {
	Get(pattern string, fn http.HandlerFunc)
}) {
	mux.Get("/health", h.Health)
	mux.Get("/ready", h.Ready)
	mux.Get("/version", h.Version)
}

// Health reports liveness.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": h.service})
}

type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Ready runs all checks concurrently within the timeout.
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.shutting.Load() {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "shutting_down", "service": h.service})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	results := make([]checkResult, len(h.checks))
	var wg sync.WaitGroup
	var failed atomic.Bool
	for i, c := range h.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := checkResult{Name: c.Name, Status: "ok"}
			if err := c.Fn(ctx); err != nil {
				failed.Store(true)
				res.Status = "fail"
				logging.FromContext(r.Context()).WarnContext(r.Context(), "readiness check failed", "check", c.Name, "error", err.Error())
			}
			results[i] = res
		}()
	}
	wg.Wait()
	status, code := "ready", http.StatusOK
	if failed.Load() {
		status, code = "not_ready", http.StatusServiceUnavailable
	}
	httpx.WriteJSON(w, code, map[string]any{"status": status, "service": h.service, "checks": results})
}

// VersionInfo is the /version payload.
type VersionInfo struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	BuildTime   string `json:"build_time,omitempty"`
	GoVersion   string `json:"go_version"`
	Environment string `json:"environment"`
}

// Version reports build metadata.
func (h *Handler) Version(w http.ResponseWriter, r *http.Request) {
	bi := buildinfo.Get()
	httpx.WriteJSON(w, http.StatusOK, VersionInfo{
		Service: h.service, Version: bi.Version, Commit: bi.Commit, BuildTime: bi.BuildTime,
		GoVersion: bi.GoVersion, Environment: h.environment,
	})
}

// Probe performs a liveness/readiness GET against a local endpoint and
// returns an error on any non-200 response. It lets minimal (distroless)
// images implement container health checks without curl:
//
//	/app/api healthcheck
func Probe(url string) error {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health probe %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// RunProbeIfRequested handles the "healthcheck" sub-command. It returns true
// (after exiting) only when the process was invoked as a probe.
func RunProbeIfRequested(args []string, addr string) {
	if len(args) < 2 || args[1] != "healthcheck" {
		return
	}
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	if err := Probe("http://" + host + "/health"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
