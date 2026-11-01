package businesshttp

import (
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"net/http"
	"time"
)

func (h *Handler) reservationCommandCenter(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	if view == "" || view == "reservations" {
		h.reservationList(w, r)
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	property, branch, day := r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"), r.URL.Query().Get("day")
	for _, id := range []string{property, branch} {
		if id != "" {
			if _, e = ids.ParseV7(id); e != nil {
				reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid property/branch filter."))
				return
			}
		}
	}
	if day != "" {
		if _, e = time.Parse(time.DateOnly, day); e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("A civil day is required."))
			return
		}
	}
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "reservations:read", property, branch)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	// All SQL fragments are a closed server-owned catalogue. Omitted day means
	// today in each property's own timezone, never the workstation/server date.
	base := `SELECT to_jsonb(r) FROM reservations r JOIN inventory_nodes n ON n.organisation_id=r.organisation_id AND n.id=r.property_id WHERE r.organisation_id=$1 AND r.property_id=ANY($2::uuid[]) AND `
	date := `coalesce(NULLIF($3,'')::date,($4::timestamptz AT TIME ZONE n.timezone)::date)`
	predicate := ""
	switch view {
	case "arrivals":
		predicate = `r.allocation_state='ACTIVE' AND r.state IN ('HOLD','PENDING_PAYMENT','PENDING_APPROVAL','CONFIRMED') AND EXISTS(SELECT 1 FROM reservation_rooms rr WHERE rr.organisation_id=r.organisation_id AND rr.reservation_id=r.id AND rr.state='ACTIVE' AND rr.date_from=` + date + `)`
	case "departures":
		predicate = `r.allocation_state='ACTIVE' AND r.state IN ('CONFIRMED','CHECKED_IN') AND EXISTS(SELECT 1 FROM reservation_rooms rr WHERE rr.organisation_id=r.organisation_id AND rr.reservation_id=r.id AND rr.state='ACTIVE' AND rr.date_to=` + date + `)`
	case "pending":
		predicate = `r.allocation_state='ACTIVE' AND r.state IN ('INQUIRY','HOLD','PENDING_PAYMENT','PENDING_APPROVAL')`
	case "groups":
		predicate = `r.group_id IS NOT NULL`
	case "cancellations":
		predicate = `r.state IN ('CANCELLED','REJECTED','EXPIRED')`
	case "no-shows":
		predicate = `r.state='NO_SHOW'`
	case "exceptions":
		predicate = `(r.state='DISPUTED' OR r.risk_status IN ('REVIEW','RESTRICTED') OR r.financial_state IN ('DISPUTED','UNKNOWN','FAILED') OR EXISTS(SELECT 1 FROM reservation_financial_requests j WHERE j.organisation_id=r.organisation_id AND j.reservation_id=r.id AND j.state IN ('DEPENDENCY_BLOCKED','QUARANTINED')))`
	case "waitlist":
		base = `SELECT to_jsonb(r) FROM waitlist_entries r WHERE r.organisation_id=$1 AND r.property_id=ANY($2::uuid[]) AND `
		predicate = `r.state IN ('WAITING','OFFERED')`
	case "conflicts":
		base = `SELECT to_jsonb(r)-'evidence' FROM reservation_conflicts r WHERE r.organisation_id=$1 AND r.property_id=ANY($2::uuid[]) AND `
		predicate = `r.state IN ('OPEN','RETRY')`
	case "modifications":
		base = `SELECT to_jsonb(r)-'change_context' FROM reservation_modifications r WHERE r.organisation_id=$1 AND r.property_id=ANY($2::uuid[]) AND `
		predicate = `true`
	default:
		reply(w, r, 0, nil, apperrors.InvalidRequest("Unsupported reservation command-center view."))
		return
	}
	args := []any{t.Organisation, properties}
	paging := ` ORDER BY r.created_at DESC,r.id DESC LIMIT $3 OFFSET $4`
	if view == "arrivals" || view == "departures" {
		args = append(args, day, h.Auth.Service.Now().UTC())
		paging = ` ORDER BY r.created_at DESC,r.id DESC LIMIT $5 OFFSET $6`
	}
	args = append(args, limit, offset)
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, base+predicate+paging, args...)
	reply(w, r, 200, map[string]any{"view": view, "items": items, "limit": limit, "offset": offset, "next_offset": offset + len(items)}, e)
}
