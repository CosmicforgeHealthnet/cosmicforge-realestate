package businesshttp

import "github.com/go-chi/chi/v5"

// registerSections1224 mounts the Sections 12-24 bounded contexts behind the
// common account authentication middleware.
func (h *Handler) registerSections1224(r chi.Router) {
	h.registerCommunications(r)
	h.registerReputation(r)
	h.registerMarketing(r)
	h.registerFinance(r)
}
