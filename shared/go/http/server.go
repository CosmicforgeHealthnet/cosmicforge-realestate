package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ServerConfig configures a hardened HTTP server.
type ServerConfig struct {
	Addr              string
	Handler           http.Handler
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	// BeforeShutdown runs when shutdown begins (e.g. mark not-ready so load
	// balancers drain traffic).
	BeforeShutdown func()
	// DrainDelay waits after BeforeShutdown so upstreams observe /ready=503.
	DrainDelay time.Duration
	Logger     *slog.Logger
}

// NewServer builds an *http.Server with platform timeouts.
func NewServer(cfg ServerConfig) *http.Server {
	def := func(d, v time.Duration) time.Duration {
		if v > 0 {
			return v
		}
		return d
	}
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           cfg.Handler,
		ReadHeaderTimeout: def(5*time.Second, cfg.ReadHeaderTimeout),
		ReadTimeout:       def(30*time.Second, cfg.ReadTimeout),
		WriteTimeout:      def(30*time.Second, cfg.WriteTimeout),
		IdleTimeout:       def(120*time.Second, cfg.IdleTimeout),
		MaxHeaderBytes:    64 << 10,
	}
}

// Serve runs srv on ln until ctx is cancelled, then shuts down gracefully:
// BeforeShutdown -> DrainDelay -> stop accepting -> wait for in-flight
// requests up to ShutdownTimeout.
func Serve(ctx context.Context, cfg ServerConfig, srv *http.Server, ln net.Listener) error {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", ln.Addr().String())
		errCh <- srv.Serve(ln)
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	logger.Info("http server shutting down")
	if cfg.BeforeShutdown != nil {
		cfg.BeforeShutdown()
	}
	if cfg.DrainDelay > 0 {
		time.Sleep(cfg.DrainDelay)
	}
	timeout := cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	sctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
		return err
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("http server stopped")
	return nil
}

// ListenAndServe listens on cfg.Addr and calls Serve.
func ListenAndServe(ctx context.Context, cfg ServerConfig) error {
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	return Serve(ctx, cfg, NewServer(cfg), ln)
}
