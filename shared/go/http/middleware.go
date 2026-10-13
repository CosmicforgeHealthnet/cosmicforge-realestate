package httpx

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
)

// WithLogger stores the logger in each request context.
func WithLogger(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(logging.WithLogger(r.Context(), l)))
		})
	}
}

// Recoverer converts panics into INTERNAL_ERROR without leaking details.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logging.FromContext(r.Context()).ErrorContext(r.Context(), "panic recovered",
					"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
				WriteError(w, r, apperrors.Internal(fmt.Errorf("panic: %v", rec)))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// MaxBody rejects bodies larger than n bytes (declared or streamed).
func MaxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				WriteError(w, r, apperrors.PayloadTooLarge())
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// StatusRecorder captures status and size for logging/metrics.
type StatusRecorder struct {
	http.ResponseWriter
	Status int
	Bytes  int
}

func (s *StatusRecorder) WriteHeader(code int) {
	if s.Status == 0 {
		s.Status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *StatusRecorder) Write(b []byte) (int, error) {
	if s.Status == 0 {
		s.Status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.Bytes += n
	return n, err
}

// Unwrap supports http.ResponseController.
func (s *StatusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog logs one structured line per request (no headers, no bodies, no
// query strings, which may carry tokens or signed URLs).
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &StatusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			return
		}
		status := rec.Status
		if status == 0 {
			status = http.StatusOK
		}
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		} else if status >= 400 {
			level = slog.LevelWarn
		}
		logging.FromContext(r.Context()).Log(r.Context(), level, "http request",
			"method", r.Method, "route", observability.Route(r), "status", status,
			"bytes", rec.Bytes, "duration_ms", time.Since(start).Milliseconds())
	})
}
