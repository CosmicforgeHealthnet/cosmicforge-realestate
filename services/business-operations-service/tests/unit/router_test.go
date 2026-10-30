package unit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

func TestRouterHealthAndUnimplementedRoutes(t *testing.T) {
	r := app.NewRouter(health.New("business-operations-service", "test", time.Second))
	for _, tc := range []struct {
		path string
		want int
	}{{"/health", http.StatusOK}, {"/ready", http.StatusOK}, {"/version", http.StatusOK}, {"/api/v1/business-operations/organisations", http.StatusNotFound}, {"/internal/v1/anything", http.StatusNotFound}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("GET %s status=%d want=%d body=%s", tc.path, w.Code, tc.want, w.Body.String())
		}
		if !requestcontext.ValidRequestID(w.Header().Get(requestcontext.HeaderRequestID)) {
			t.Errorf("GET %s did not return a valid request ID", tc.path)
		}
	}
}
