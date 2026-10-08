package integration

import (
	"net/http"
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
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/internal/openapi/verification-service.yaml", routes, "/internal/")
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/public/openapi/verification.yaml", routes, "/api/")
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/webhooks/verification-providers.yaml", routes, "/webhooks/")
}

func TestErrorResponsesMatchEnvelopeSchema(t *testing.T) {
	h := newHarness(t)
	for _, r := range []result{
		h.do(call{method: "GET", path: "/internal/v1/verifications/not-a-uuid"}),
		h.do(call{method: "GET", path: "/nope"}),
	} {
		testkit.ValidateJSONSchema(t, "../../../../contracts/schemas/error-envelope.schema.json", r.body)
	}
}
