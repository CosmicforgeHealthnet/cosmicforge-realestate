package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func implementedRoutes(t *testing.T, h *harness) []testkit.Route {
	var routes []testkit.Route
	err := chi.Walk(h.app.Router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, testkit.Route{Method: method, Path: route})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

// The Section 5 documents are hand-written; the Sections 6–13 documents are
// generated from the route registry. Together they must document exactly
// the implemented routes, with no operation documented twice.
func TestOpenAPIContractsMatchHandlers(t *testing.T) {
	h := newHarness(t)
	routes := implementedRoutes(t, h)
	const root = "../../../../contracts/"
	assertRoutesMatch(t, routes, []string{root + "public/openapi/individual-owner.yaml", root + "public/openapi/individual-owner-domain.yaml"}, "/api/")
	assertRoutesMatch(t, routes, []string{root + "internal/openapi/individual-owner-service.yaml", root + "internal/openapi/individual-owner-service-domain.yaml"},
		"/internal/", "/.well-known/")
}

func assertRoutesMatch(t *testing.T, routes []testkit.Route, specs []string, prefixes ...string) {
	t.Helper()
	documented := map[string]bool{}
	for _, spec := range specs {
		for op := range testkit.OpenAPIOperations(t, spec) {
			if documented[op] {
				t.Errorf("%s documented twice", op)
			}
			documented[op] = true
		}
	}
	in := func(p string) bool {
		for _, pre := range prefixes {
			if strings.HasPrefix(p, pre) {
				return true
			}
		}
		return false
	}
	impl := map[string]bool{}
	for _, r := range routes {
		p := strings.TrimSuffix(r.Path, "/")
		if in(p) {
			impl[r.Method+" "+p] = true
			if !documented[r.Method+" "+p] {
				t.Errorf("implemented but undocumented: %s %s", r.Method, p)
			}
		}
	}
	for op := range documented {
		if in(strings.SplitN(op, " ", 2)[1]) && !impl[op] {
			t.Errorf("documented but not implemented: %s", op)
		}
	}
}

// Postman parity: every implemented owner route has a request in folder
// "02 Individual Owner" and every request there targets an implemented route.
func TestPostmanMatchesHandlers(t *testing.T) {
	h := newHarness(t)
	b, err := os.ReadFile("../../../../postman/CosmicForge_RealEstate.postman_collection.json")
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		Name    string `json:"name"`
		Item    []item `json:"item"`
		Request *struct {
			Method string `json:"method"`
			URL    struct {
				Path []string `json:"path"`
			} `json:"url"`
		} `json:"request"`
	}
	var col struct {
		Item []item `json:"item"`
	}
	if err := json.Unmarshal(b, &col); err != nil {
		t.Fatal(err)
	}
	var reqs []string
	var walk func([]item)
	walk = func(items []item) {
		for _, it := range items {
			if it.Request != nil {
				reqs = append(reqs, it.Request.Method+" /"+strings.Join(it.Request.URL.Path, "/"))
			}
			walk(it.Item)
		}
	}
	for _, f := range col.Item {
		if f.Name == "02 Individual Owner" {
			walk(f.Item)
		}
	}
	match := func(route, req string) bool {
		rs, qs := strings.Split(route, "/"), strings.Split(req, "/")
		if len(rs) != len(qs) {
			return false
		}
		for i := range rs {
			wild := strings.HasPrefix(rs[i], "{") || strings.HasPrefix(qs[i], "{{")
			if rs[i] != qs[i] && !wild {
				return false
			}
		}
		return true
	}
	var routes []string
	for _, r := range implementedRoutes(t, h) {
		if strings.HasPrefix(r.Path, "/api/v1/owner/") || strings.HasPrefix(r.Path, "/internal/v1/owner/") {
			routes = append(routes, r.Method+" "+strings.TrimSuffix(r.Path, "/"))
		}
	}
	for _, r := range routes {
		found := false
		for _, q := range reqs {
			found = found || match(r, q)
		}
		if !found {
			t.Errorf("implemented route without Postman request: %s", r)
		}
	}
	for _, q := range reqs {
		found := false
		for _, r := range routes {
			found = found || match(r, q)
		}
		if !found {
			t.Errorf("Postman request without implemented route: %s", q)
		}
	}
}
