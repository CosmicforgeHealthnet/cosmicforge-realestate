package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"net/http"
)

type reservationExitKey struct{}
type reservationExitScope struct{ ReservationID, PropertyID ids.ID }

// Only the cancellation transport sets this private context. It permits a
// protective exit under organisation restriction; all live account, session,
// membership, explicit capability, scope, deny and effective-date checks remain.
// Client headers/body fields cannot construct this context or authorise growth.
func (h *Handler) reservationExitRequest(r *http.Request) (*http.Request, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return r, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return r, e
	}
	v, e := reservations.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		return r, e
	}
	return r.WithContext(context.WithValue(r.Context(), reservationExitKey{}, reservationExitScope{v.ID, v.PropertyID})), nil
}
func reservationExitAllowed(ctx context.Context, capability, kind string, id ids.ID) bool {
	scope, ok := ctx.Value(reservationExitKey{}).(reservationExitScope)
	return ok && !scope.ReservationID.IsZero() && capability == "reservations:cancel" && kind == "PROPERTY" && id == scope.PropertyID
}
