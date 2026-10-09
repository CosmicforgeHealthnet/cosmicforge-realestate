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

func TestOpenAPIContractsMatchHandlers(t *testing.T) {
	h := newHarness(t)
	routes := implementedRoutes(t, h)
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/public/openapi/individual-owner.yaml", routes, "/api/")
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/internal/openapi/individual-owner-service.yaml", routes, "/internal/", "/.well-known/")
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
