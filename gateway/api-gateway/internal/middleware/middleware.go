// Package middleware contains edge-only HTTP middleware: client IP
// resolution behind trusted proxies, CORS, and canonical-path enforcement.
package middleware

import (
	"context"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
)

type clientIPKey struct{}

// ClientIP resolves the real client IP. X-Forwarded-For is honoured only when
// the immediate peer is a trusted proxy; the right-most untrusted hop wins,
// so a client cannot spoof its address by sending its own XFF header.
func ClientIP(trusted []*net.IPNet) func(http.Handler) http.Handler {
	isTrusted := func(ip net.IP) bool {
		for _, n := range trusted {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			client := net.ParseIP(host)
			if client != nil && isTrusted(client) {
				hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
				for i := len(hops) - 1; i >= 0; i-- {
					ip := net.ParseIP(strings.TrimSpace(hops[i]))
					if ip == nil {
						break
					}
					client = ip
					if !isTrusted(ip) {
						break
					}
				}
			}
			ipStr := ""
			if client != nil {
				ipStr = client.String()
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ipStr)))
		})
	}
}

// ClientIPFrom returns the resolved client IP.
func ClientIPFrom(ctx context.Context) string {
	s, _ := ctx.Value(clientIPKey{}).(string)
	return s
}

// CORS implements an explicit-origin CORS policy for bearer-token APIs (no
// cookies, so credentials are not allowed).
func CORS(allowed []string) func(http.Handler) http.Handler {
	set := map[string]bool{}
	any := false
	for _, o := range allowed {
		if o == "*" {
			any = true
		}
		set[o] = true
	}
	const (
		methods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
		headers = "Authorization, Content-Type, Idempotency-Key, X-Request-ID, traceparent, tracestate"
		expose  = "X-Request-ID, Retry-After"
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Origin")
			ok := any || set[origin]
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
			if !ok {
				if preflight {
					httpx.WriteError(w, r, apperrors.PermissionDenied("Origin not allowed."))
					return
				}
				next.ServeHTTP(w, r) // browser enforces; no CORS headers emitted
				return
			}
			allowOrigin := origin
			if any && !set[origin] {
				allowOrigin = "*"
			}
			w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
			w.Header().Set("Access-Control-Expose-Headers", expose)
			if preflight {
				w.Header().Set("Access-Control-Allow-Methods", methods)
				w.Header().Set("Access-Control-Allow-Headers", headers)
				w.Header().Set("Access-Control-Max-Age", strconv.Itoa(600))
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CanonicalPath rejects non-canonical request paths (dot segments, duplicate
// slashes, encoded separators). This prevents path tricks such as
// /api/v1/media/../../internal/v1/... from reaching a non-public surface
// after upstream normalisation.
func CanonicalPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		raw := strings.ToLower(r.URL.RawPath + r.URL.EscapedPath())
		clean := path.Clean(p)
		if strings.HasSuffix(p, "/") && clean != "/" {
			clean += "/"
		}
		if p == "" || p[0] != '/' || clean != p || strings.Contains(p, "\\") ||
			strings.Contains(raw, "%2f") || strings.Contains(raw, "%2e") || strings.Contains(raw, "%5c") || strings.Contains(raw, "%00") {
			httpx.WriteError(w, r, apperrors.NotFound(apperrors.CodeRouteNotFound, "The requested route does not exist."))
			return
		}
		next.ServeHTTP(w, r)
	})
}
