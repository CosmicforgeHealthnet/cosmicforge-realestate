package security

import "net/http"

// HeadersConfig controls HTTP security headers for JSON APIs.
type HeadersConfig struct {
	// HSTS enables Strict-Transport-Security. Enable only when the component
	// is always served over TLS (staging/production behind Nginx/Cloudflare).
	HSTS bool
}

// Headers sets conservative security headers suitable for JSON APIs.
func Headers(cfg HeadersConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-site")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			if h.Get("Cache-Control") == "" {
				h.Set("Cache-Control", "no-store")
			}
			if cfg.HSTS {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
