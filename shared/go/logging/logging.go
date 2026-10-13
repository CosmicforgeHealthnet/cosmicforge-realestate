// Package logging configures structured slog JSON logging.
//
// Every record contains timestamp, level, service, environment and message,
// plus request_id / trace_id / span_id / calling_service / actor_domain /
// actor_id when the record is emitted with a request context
// (logger.InfoContext(ctx, ...)).
//
// Attributes whose key denotes a secret (password, token, authorization,
// private_key, ...) are replaced with "[REDACTED]" regardless of value, and
// security.Secret values always render redacted. This is a safety net; code
// must still never pass raw passwords, refresh tokens, raw JWTs, signing keys,
// provider secrets or raw verification documents to a logger.
package logging

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// Config configures a logger.
type Config struct {
	Service     string
	Environment string
	Version     string
	Level       string // debug|info|warn|error (default info)
	Output      io.Writer
}

// ParseLevel converts a level name; unknown names map to info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// New builds the platform logger.
func New(cfg Config) *slog.Logger {
	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}
	base := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level:       ParseLevel(cfg.Level),
		ReplaceAttr: replaceAttr,
	})
	attrs := []slog.Attr{
		slog.String("service", cfg.Service),
		slog.String("environment", cfg.Environment),
	}
	if cfg.Version != "" {
		attrs = append(attrs, slog.String("version", cfg.Version))
	}
	return slog.New(&contextHandler{Handler: base.WithAttrs(attrs)})
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 {
		switch a.Key {
		case slog.TimeKey:
			a.Key = "timestamp"
			a.Value = slog.StringValue(a.Value.Time().UTC().Format("2006-01-02T15:04:05.000000Z07:00"))
			return a
		case slog.MessageKey:
			a.Key = "message"
			return a
		case slog.LevelKey:
			a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			return a
		}
	}
	if security.IsSensitiveKey(a.Key) || a.Key == "error" || a.Key == "panic" {
		return slog.String(a.Key, security.Redacted)
	}
	if a.Value.Kind() == slog.KindAny {
		// Structured payloads do not pass through ReplaceAttr recursively.
		// Convert to JSON data, then apply the same policy to every nested key.
		if b, err := json.Marshal(a.Value.Any()); err == nil {
			var v any
			if json.Unmarshal(b, &v) == nil {
				a.Value = slog.AnyValue(redactNested(v))
			}
		}
	}
	return a
}

func redactNested(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if security.IsSensitiveKey(k) || k == "error" || k == "panic" {
				x[k] = security.Redacted
			} else {
				x[k] = redactNested(item)
			}
		}
	case []any:
		for i := range x {
			x[i] = redactNested(x[i])
		}
	}
	return v
}

// contextHandler appends request-scoped fields from ctx.
type contextHandler struct{ slog.Handler }

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		v := requestcontext.From(ctx)
		if v.RequestID != "" {
			r.AddAttrs(slog.String("request_id", v.RequestID))
		}
		if v.TraceID != "" {
			r.AddAttrs(slog.String("trace_id", v.TraceID))
		}
		if sc := trace.SpanContextFromContext(ctx); sc.HasSpanID() {
			r.AddAttrs(slog.String("span_id", sc.SpanID().String()))
		}
		if v.CallingService != "" {
			r.AddAttrs(slog.String("calling_service", v.CallingService))
		}
		if !v.Actor.IsZero() {
			r.AddAttrs(slog.String("actor_domain", v.Actor.Domain), slog.String("actor_id", v.Actor.ID.String()))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}

type loggerKey struct{}

// WithLogger stores a logger in ctx.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// FromContext returns the logger in ctx or slog.Default().
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// Discard returns a logger that drops everything (tests).
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
