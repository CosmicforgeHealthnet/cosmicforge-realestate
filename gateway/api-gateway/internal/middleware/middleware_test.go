package middleware

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPTrustedProxies(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("10.0.0.0/8")
	var got string
	h := ClientIP([]*net.IPNet{trusted})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = ClientIPFrom(r.Context()) }))
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.5:1234", "1.1.1.1", "203.0.113.5"},                       // untrusted peer: XFF ignored
		{"10.0.0.2:1234", "198.51.100.7", "198.51.100.7"},                    // trusted proxy
		{"10.0.0.2:1234", "6.6.6.6, 198.51.100.7, 10.0.0.9", "198.51.100.7"}, // right-most untrusted
		{"10.0.0.2:1234", "", "10.0.0.2"},
		{"10.0.0.2:1234", "garbage", "10.0.0.2"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != c.want {
			t.Errorf("%s / %q -> %s want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestCanonicalPath(t *testing.T) {
	h := CanonicalPath(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for path, want := range map[string]int{
		"/api/v1/media/x": 204, "/api/v1/media/": 204, "/api/v1/media/../x": 404, "/api//x": 404, "/api/./x": 404,
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.URL.Path = path
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s -> %d", path, rec.Code)
		}
	}
}
