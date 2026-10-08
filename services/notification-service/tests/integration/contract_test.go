package integration

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestOpenAPIContractsMatchHandlers(t *testing.T) {
	h := newHarness(t)
	var routes []testkit.Route
	_ = chi.Walk(h.app.Router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, testkit.Route{Method: method, Path: route})
		return nil
	})
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/internal/openapi/notification-service.yaml", routes, "/internal/")
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/public/openapi/notifications.yaml", routes, "/api/")
}
