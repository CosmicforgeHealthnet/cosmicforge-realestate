package businesshttp

import (
	"context"
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type crmWithdrawalKey struct{}
type crmWithdrawalScope struct{ Organisation, Guest, Property ids.ID }

// Only consent withdrawal installs this private context. It cannot authorize a
// grant, read private data, revive a session, widen scope or bypass a deny.
func (h *Handler) crmWithdrawalRequest(r *http.Request) (*http.Request, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return r, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return r, e
	}
	guest, e := crm.GetGuest(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		return r, e
	}
	property := guest.PropertyID
	if raw := r.URL.Query().Get("property_id"); raw != "" {
		property, e = ids.ParseV7(raw)
		if e != nil {
			return r, e
		}
	}
	return r.WithContext(context.WithValue(r.Context(), crmWithdrawalKey{}, crmWithdrawalScope{t.Organisation, id, property})), nil
}
func crmWithdrawalAllowed(ctx context.Context, org ids.ID, capability, kind string, property ids.ID) bool {
	s, ok := ctx.Value(crmWithdrawalKey{}).(crmWithdrawalScope)
	return ok && !s.Guest.IsZero() && s.Organisation == org && s.Property == property && kind == "PROPERTY" && capability == "guest_privacy:write"
}
