package app

import (
	"net/http"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/go-chi/chi/v5"
)

func NewRouter(h *health.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(requestcontext.Middleware)
	r.Use(httpx.Recoverer)
	r.Use(httpx.MaxBody(1 << 20))
	r.Use(httpx.AccessLog)
	h.Register(r)
	// No business endpoints are exposed until their sections are implemented.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, apperrors.NotFound(apperrors.CodeRouteNotFound, "Route not found."))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, apperrors.New(http.StatusMethodNotAllowed, apperrors.CodeMethodNotAllowed, "Method not allowed."))
	})
	return r
}
