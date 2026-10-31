package app

import (
	"net/http"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/go-chi/chi/v5"
)

func NewRouter(h *health.Handler, business ...http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(observability.HTTPMiddleware("business-operations-service"))
	r.Use(requestcontext.Middleware)
	r.Use(httpx.Recoverer)
	r.Use(httpx.MaxBody(1 << 20))
	r.Use(httpx.AccessLog)
	h.Register(r)
	if len(business) > 0 && business[0] != nil {
		r.Mount("/api/v1/business", business[0])
		r.Mount("/api/v1/business-operations", business[0])
	}
	if len(business) > 1 && business[1] != nil {
		r.Mount("/internal/v1/business-supply", business[1])
	}
	if len(business) > 2 && business[2] != nil {
		r.Mount("/webhooks/v1/business-channels", business[2])
	}
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, apperrors.NotFound(apperrors.CodeRouteNotFound, "Route not found."))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, apperrors.New(http.StatusMethodNotAllowed, apperrors.CodeMethodNotAllowed, "Method not allowed."))
	})
	return r
}
