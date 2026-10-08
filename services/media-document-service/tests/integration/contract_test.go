package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestOpenAPIContractsMatchHandlers(t *testing.T) {
	h := newHarness(t)
	var routes []testkit.Route
	_ = chi.Walk(h.app.Router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, testkit.Route{Method: method, Path: route})
		return nil
	})
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/internal/openapi/media-document-service.yaml", routes, "/internal/")
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/public/openapi/media.yaml", routes, "/api/")
}

// The metadata shape consumed by verification-service's media client.
func TestMetadataContractForVerificationService(t *testing.T) {
	h := newHarness(t)
	id := h.ready(ids.New(), "VERIFICATION_DOCUMENT", "application/pdf", "id.pdf", pdfBytes, "")
	r := h.do(call{method: "GET", path: "/internal/v1/media/" + id.String(), as: platform.VerificationService})
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(r.body, &env)
	for _, k := range []string{"media_id", "owner_domain", "owner_type", "owner_id", "status", "mime_type", "size_bytes", "checksum_sha256", "access_classification"} {
		if _, ok := env.Data[k]; !ok {
			t.Errorf("metadata missing %s", k)
		}
	}
	d := h.do(call{method: "POST", path: "/internal/v1/media/" + id.String() + "/download-url", as: platform.VerificationService,
		key: "dl-1", body: map[string]any{"purpose": "VERIFICATION_PROVIDER_SUBMISSION"}})
	if d.status != 200 {
		t.Fatalf("verification download-url: %d %s", d.status, d.body)
	}
}
