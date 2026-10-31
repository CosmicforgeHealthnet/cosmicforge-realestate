package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type CommercialCalendarCommand struct {
	PoolID           ids.ID           `json:"pool_id"`
	DateFrom         string           `json:"date_from"`
	DateTo           string           `json:"date_to"`
	Capacity         *int             `json:"capacity,omitempty"`
	Blocked          *int             `json:"blocked,omitempty"`
	ExpectedVersions map[string]int64 `json:"expected_versions"`
	Reason           string           `json:"reason"`
}

func (h *Handler) calendarSet(w http.ResponseWriter, r *http.Request) {
	var in CommercialCalendarCommand
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pool, e := inventory.GetPool(r.Context(), h.Auth.Service.Pool, t.Organisation, in.PoolID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var timezone string
	e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, pool.PropertyID).Scan(&timezone)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	days, e := (inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}).Days(timezone)
	if e != nil || !validStaffReason(in.Reason) || len(in.ExpectedVersions) > 366 || in.Capacity != nil && (*in.Capacity < 0 || *in.Capacity > pool.Capacity) || in.Blocked != nil && *in.Blocked < 0 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid dates, bounded approved capacity, versions and block reason are required."))
		return
	}
	for day := range in.ExpectedVersions {
		if !slices.Contains(days, day) {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Expected versions must refer only to selected civil dates."))
			return
		}
	}
	h.execute(w, r, "commercial:calendar:set:"+pool.ID.String(), in, "calendar:write", "PROPERTY", pool.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "calendar:write", "PROPERTY", pool.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := inventory.GetPool(ctx, tx, t.Organisation, pool.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.State == "RETIRED" {
			return 0, nil, apperrors.Conflict("POOL_RETIRED", "Retired stock cannot be configured.")
		}
		if in.Capacity != nil && *in.Capacity > current.Capacity {
			return 0, nil, apperrors.PermissionDenied("Calendar cannot create additional physical stock.")
		}
		for _, day := range days {
			var capacity, blocked, committed int
			var version int64
			e = tx.QueryRow(ctx, `SELECT capacity,blocked,committed,version FROM inventory_calendar WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date FOR UPDATE`, t.Organisation, pool.ID, day).Scan(&capacity, &blocked, &committed, &version)
			if errors.Is(e, pgx.ErrNoRows) {
				if in.ExpectedVersions[day] != 0 {
					return 0, nil, apperrors.Conflict("CALENDAR_VERSION_CONFLICT", "Expected calendar day does not exist.")
				}
				capacity = current.Capacity
				if in.Capacity != nil {
					capacity = *in.Capacity
				}
				if in.Blocked != nil {
					blocked = *in.Blocked
				}
				if blocked > capacity {
					return 0, nil, apperrors.InvalidRequest("Blocked stock cannot exceed capacity.")
				}
				if _, e = tx.Exec(ctx, `INSERT INTO inventory_calendar(organisation_id,pool_id,day,capacity,blocked,reason,updated_by) VALUES($1,$2,$3::date,$4,$5,$6,$7)`, t.Organisation, pool.ID, day, capacity, blocked, in.Reason, t.Account); e != nil {
					return 0, nil, e
				}
				continue
			}
			if e != nil {
				return 0, nil, e
			}
			if in.ExpectedVersions[day] != version {
				return 0, nil, apperrors.Conflict("CALENDAR_VERSION_CONFLICT", "Every existing day requires its exact current version.")
			}
			next, e := time.Parse(time.DateOnly, day)
			if e != nil {
				return 0, nil, e
			}
			stock, e := inventory.Calendar(ctx, tx, t.Organisation, pool.ID, inventory.DateRange{DateFrom: day, DateTo: next.AddDate(0, 0, 1).Format(time.DateOnly)}, timezone, "", h.Auth.Service.Now().UTC(), false)
			if e != nil {
				return 0, nil, e
			}
			reserved := capacity - blocked - committed - stock[0].Available
			if in.Capacity != nil {
				capacity = *in.Capacity
			}
			if in.Blocked != nil {
				blocked = *in.Blocked
			}
			if capacity-blocked-committed < reserved {
				return 0, nil, apperrors.Conflict("STOCK_IN_USE", "Calendar changes cannot invalidate existing holds, allotments or commitments.")
			}
			if _, e = tx.Exec(ctx, `UPDATE inventory_calendar SET capacity=$4,blocked=$5,reason=$6,version=version+1,updated_by=$7,updated_at=now() WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date`, t.Organisation, pool.ID, day, capacity, blocked, in.Reason, t.Account); e != nil {
				return 0, nil, e
			}
		}
		result, e := inventory.Calendar(ctx, tx, t.Organisation, pool.ID, inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}, timezone, "", h.Auth.Service.Now().UTC(), false)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, pool.PropertyID, pool.ID, "inventory_pool", "CALENDAR_CONFIGURED", businessEvents.InventoryChanged, current.Version, map[string]any{"pool_id": pool.ID, "days": result, "reason": in.Reason})
		return 200, map[string]any{"pool_id": pool.ID, "days": result, "date_semantics": "PROPERTY_LOCAL_CHECKOUT_EXCLUSIVE"}, e
	})
}
func (h *Handler) calendarGet(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := queryCommercialID(r, "pool_id")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pool, e := inventory.GetPool(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, e = h.commercialScope(r, "calendar:read", pool.PropertyID, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var timezone string
	e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, pool.PropertyID).Scan(&timezone)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	channel := r.URL.Query().Get("channel")
	days, e := inventory.Calendar(r.Context(), h.Auth.Service.Pool, t.Organisation, id, inventory.DateRange{DateFrom: r.URL.Query().Get("date_from"), DateTo: r.URL.Query().Get("date_to")}, timezone, channel, h.Auth.Service.Now().UTC(), false)
	reply(w, r, 200, map[string]any{"pool_id": id, "pool_version": pool.Version, "timezone": timezone, "channel": channel, "days": days, "as_of": h.Auth.Service.Now().UTC(), "commercial_eligibility_checked": false}, e)
}
func positiveQueryInt(r *http.Request, key string, fallback int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	v, e := strconv.Atoi(raw)
	if e != nil || v < 0 || v > 100000 {
		return 0, apperrors.InvalidRequest("Invalid " + key + " query parameter.")
	}
	return v, nil
}
func (h *Handler) availabilityGet(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	adults, e := positiveQueryInt(r, "adults", 1)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	children, e := positiveQueryInt(r, "children", 0)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if adults < 1 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("At least one adult is required."))
		return
	}
	rangeDates := inventory.DateRange{DateFrom: r.URL.Query().Get("date_from"), DateTo: r.URL.Query().Get("date_to")}
	channel := r.URL.Query().Get("channel")
	if _, e = rangeDates.Days("UTC"); e != nil || !inventory.ValidChannel(channel) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid checkout-exclusive dates and approved channel code are required."))
		return
	}
	products := []inventory.Product{}
	if raw := r.URL.Query().Get("product_id"); raw != "" {
		id, e := ids.ParseV7(raw)
		if e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid product ID."))
			return
		}
		p, e := inventory.GetProduct(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		products = append(products, p)
	} else {
		property, e := queryCommercialID(r, "property_id")
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		if _, e = h.commercialScope(r, "calendar:read", property, false); e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		limit, offset, e := pagination(r)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		limit = min(limit, 20)
		node := r.URL.Query().Get("node_id")
		if node != "" {
			if _, e = ids.ParseV7(node); e != nil {
				reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid canonical node filter."))
				return
			}
		}
		rows, e := h.Auth.Service.Pool.Query(r.Context(), `SELECT to_jsonb(p) FROM sellable_products p WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR node_id::text=$3) ORDER BY id LIMIT $4 OFFSET $5`, t.Organisation, property, node, limit, offset)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		for rows.Next() {
			var p inventory.Product
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				break
			}
			if e = json.Unmarshal(raw, &p); e != nil {
				break
			}
			products = append(products, p)
		}
		rowErr := rows.Err()
		rows.Close()
		if e == nil {
			e = rowErr
		}
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
	}
	items := []any{}
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		for _, p := range products {
			current, _, e := h.authorize(ctx, tx, "calendar:read", "PROPERTY", p.PropertyID, false)
			if e != nil {
				return e
			}
			if e = h.commercialGate(ctx, tx, current, p.PropertyID); e != nil {
				return e
			}
			result, e := h.productAvailability(ctx, tx, current, p, rangeDates, channel, adults, children)
			if e != nil {
				return e
			}
			items = append(items, result)
		}
		return nil
	})
	reply(w, r, 200, map[string]any{"items": items, "as_of": h.Auth.Service.Now().UTC(), "date_semantics": "PROPERTY_LOCAL_CHECKOUT_EXCLUSIVE"}, e)
}
func (h *Handler) productAvailability(ctx context.Context, tx pgx.Tx, t tenant, p inventory.Product, r inventory.DateRange, channel string, adults, children int) (any, error) {
	pool, e := inventory.GetPool(ctx, tx, t.Organisation, p.PoolID)
	if e != nil {
		return nil, e
	}
	var timezone string
	if e = tx.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, p.PropertyID).Scan(&timezone); e != nil {
		return nil, e
	}
	stock, e := inventory.Calendar(ctx, tx, t.Organisation, p.PoolID, r, timezone, channel, h.Auth.Service.Now().UTC(), false)
	if e != nil {
		return nil, e
	}
	quantity := 100000
	reason := ""
	for _, d := range stock {
		quantity = min(quantity, d.Available)
	}
	if p.State != "ACTIVE" || adults+children < p.MinGuests || adults+children > p.MaxGuests {
		reason = "PRODUCT_OR_OCCUPANCY_INELIGIBLE"
	}
	if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
		reason = "STOCK_AUTHORITY_STALE"
	}
	if e = h.commercialProductEligibility(ctx, tx, t, p, adults, children, channel); e != nil {
		reason = commercialErrorCode(e)
	}
	if e = h.commercialChannel(ctx, tx, t, p, channel); e != nil {
		reason = "CHANNEL_INELIGIBLE"
	}
	if e = h.channelSaleControls(ctx, tx, p, channel, len(stock)); e != nil {
		reason = commercialErrorCode(e)
	}
	var planVersion int64
	if p.RatePlanID == nil {
		reason = "RATE_REQUIRED"
	} else {
		plan, e := revenue.GetPlan(ctx, tx, t.Organisation, *p.RatePlanID)
		if e != nil {
			return nil, e
		}
		planVersion = plan.Version
		if plan.State != "ACTIVE" {
			reason = "RATE_INACTIVE"
		}
		records, e := revenue.Restrictions(ctx, tx, p, *p.RatePlanID, r, channel)
		if e != nil {
			return nil, e
		}
		if e = revenue.CheckRestrictions(records, r, timezone, adults, children, h.Auth.Service.Now().UTC()); e != nil {
			reason = commercialErrorCode(e)
		}
		for _, d := range stock {
			v, e := revenue.VersionForDay(ctx, tx, t.Organisation, *p.RatePlanID, d.Day)
			if e != nil {
				reason = "RATE_DATE_UNCOVERED"
				continue
			}
			base, _, e := revenue.ResolveBase(ctx, tx, t.Organisation, v, d.Day)
			if e != nil {
				reason = "RATE_DEPENDENCY_INVALID"
				continue
			}
			if _, e = revenue.Nightly(v.Definition, base, d.Day, channel, len(stock), adults, children); e != nil {
				reason = "RATE_OCCUPANCY_INELIGIBLE"
			}
		}
	}
	if reason != "" {
		quantity = 0
	}
	return map[string]any{"product_id": p.ID, "property_id": p.PropertyID, "pool_id": pool.ID, "product_version": p.Version, "pool_version": pool.Version, "rate_plan_version": planVersion, "available_quantity": quantity, "eligible": reason == "", "ineligible_reason": reason, "days": stock, "timezone": timezone}, nil
}
func commercialErrorCode(err error) string {
	var app *apperrors.Error
	if errors.As(err, &app) {
		return app.Code
	}
	return "POLICY_RECONCILIATION_REQUIRED"
}
func (h *Handler) commercialChannel(ctx context.Context, q database.Querier, t tenant, p inventory.Product, channel string) error {
	if !inventory.ValidChannel(channel) || len(p.Channels) > 0 && !slices.Contains(p.Channels, channel) {
		return apperrors.PermissionDenied("Product is not eligible for this sales channel.")
	}
	if channel == "" {
		return nil
	}
	var enabled bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channel_connections c JOIN channel_providers p ON p.id=c.provider_id JOIN channel_capabilities cap ON cap.provider_id=p.id AND cap.capability='ARI' WHERE c.organisation_id=$1 AND c.property_id=$2 AND p.code=$3 AND c.state='CONNECTED' AND p.status='CERTIFIED' AND cap.status='CERTIFIED' AND c.validated_at IS NOT NULL AND (c.credential_expires_at IS NULL OR c.credential_expires_at>$4))`, t.Organisation, p.PropertyID, channel, h.Auth.Service.Now().UTC()).Scan(&enabled)
	if e != nil {
		return e
	}
	if !enabled || h.ChannelAdapters[channel] == nil {
		return apperrors.Unavailable("Channel configuration, credentials and certified ARI capability are required.")
	}
	return nil
}
func (h *Handler) holdCreate(w http.ResponseWriter, r *http.Request) {
	var in inventory.HoldCommand
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	p, e := inventory.GetProduct(r.Context(), h.Auth.Service.Pool, t.Organisation, in.ProductID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "commercial:hold:create", in, "holds:write", "PROPERTY", p.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "holds:write", "PROPERTY", p.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if e = h.commercialGate(ctx, tx, t, p.PropertyID); e != nil {
			return 0, nil, e
		}
		if e = h.validateHoldQuote(ctx, tx, t, in); e != nil {
			return 0, nil, e
		}
		hold, e := (inventory.Repository{Pool: h.Auth.Service.Pool, Outbox: h.Auth.Service.Outbox, Now: h.Auth.Service.Now}).PlaceHold(ctx, tx, t.Organisation, t.Account, in)
		return 201, hold, e
	})
}
func (h *Handler) holdList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "inventory_holds", "holds:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		v, e := inventory.GetHold(ctx, h.Auth.Service.Pool, org, id)
		if v.State == "ACTIVE" && !h.Auth.Service.Now().Before(v.ExpiresAt) {
			v.State = "EXPIRED"
		}
		return v, e
	})
}
func (h *Handler) holdTarget(r *http.Request, cap string, mfa bool) (tenant, inventory.Hold, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, inventory.Hold{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, inventory.Hold{}, e
	}
	hold, e := inventory.GetHold(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		t, e = h.commercialScope(r, cap, hold.PropertyID, mfa)
	}
	return t, hold, e
}
func (h *Handler) holdGet(w http.ResponseWriter, r *http.Request) {
	_, hold, e := h.holdTarget(r, "holds:read", false)
	if hold.State == "ACTIVE" && !h.Auth.Service.Now().Before(hold.ExpiresAt) {
		hold.State = "EXPIRED"
	}
	reply(w, r, 200, hold, e)
}

type CommercialHoldChange struct {
	Version         int64  `json:"version"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
	Reason          string `json:"reason"`
}

func (h *Handler) holdCancel(w http.ResponseWriter, r *http.Request) { h.changeHold(w, r, false) }
func (h *Handler) holdExtend(w http.ResponseWriter, r *http.Request) { h.changeHold(w, r, true) }
func (h *Handler) changeHold(w http.ResponseWriter, r *http.Request, extend bool) {
	var in CommercialHoldChange
	if !decode(w, r, &in) {
		return
	}
	_, hold, e := h.holdTarget(r, "holds:write", extend)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || extend && (in.DurationSeconds < 30 || in.DurationSeconds > 900) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current hold version, bounded extension and reason required."))
		return
	}
	h.execute(w, r, "commercial:hold:change:"+hold.ID.String(), in, "holds:write", "PROPERTY", hold.PropertyID, extend, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "holds:write", "PROPERTY", hold.PropertyID, extend)
		if e != nil {
			return 0, nil, e
		}
		current, e := inventory.GetHold(ctx, tx, t.Organisation, hold.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version || current.State != "ACTIVE" || !h.Auth.Service.Now().Before(current.ExpiresAt) {
			return 0, nil, apperrors.Conflict("HOLD_STATE_OR_VERSION", "Only a current unexpired hold can be changed.")
		}
		state := "CANCELLED"
		expiry := current.ExpiresAt
		if extend {
			if e = h.commercialGate(ctx, tx, t, hold.PropertyID); e != nil {
				return 0, nil, e
			}
			if e = h.validateHoldQuote(ctx, tx, t, inventory.HoldCommand{ProductID: current.ProductID, QuoteID: current.QuoteID, DateFrom: current.DateFrom, DateTo: current.DateTo, Quantity: current.Quantity, Channel: current.Channel}); e != nil {
				return 0, nil, e
			}
			state = "ACTIVE"
			expiry = h.Auth.Service.Now().UTC().Add(time.Duration(in.DurationSeconds) * time.Second)
			if !expiry.After(current.ExpiresAt) || expiry.After(current.CreatedAt.Add(30*time.Minute)) {
				return 0, nil, apperrors.InvalidRequest("Extension must increase expiry within the absolute 30-minute hold lifetime.")
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_holds SET state=$3,expires_at=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, hold.ID, state, expiry); e != nil {
			return 0, nil, e
		}
		updated, e := inventory.GetHold(ctx, tx, t.Organisation, hold.ID)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_calendar SET version=version+1,updated_by=$5,updated_at=now() WHERE organisation_id=$1 AND pool_id=$2 AND day>=$3::date AND day<$4::date`, t.Organisation, hold.PoolID, hold.DateFrom, hold.DateTo, t.Account); e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, hold.PropertyID, hold.ID, "inventory_hold", "HOLD_"+state, businessEvents.InventoryChanged, updated.Version, map[string]any{"hold": updated, "reason": in.Reason})
		return 200, updated, e
	})
}
func (h *Handler) holdHandoff(w http.ResponseWriter, r *http.Request) {
	_, _, e := h.holdTarget(r, "holds:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	reply(w, r, 0, nil, apperrors.Unavailable("Canonical Section 9 reservation acceptance is unavailable; the hold is not a confirmed reservation and has not been consumed."))
}

type CommercialAllotment struct {
	ID             ids.ID    `json:"id"`
	OrganisationID ids.ID    `json:"organisation_id"`
	PropertyID     ids.ID    `json:"property_id"`
	PoolID         ids.ID    `json:"pool_id"`
	Channel        string    `json:"channel"`
	DateFrom       string    `json:"date_from"`
	DateTo         string    `json:"date_to"`
	Quantity       int       `json:"quantity"`
	ReleaseAt      time.Time `json:"release_at"`
	State          string    `json:"state"`
	Version        int64     `json:"version"`
	Reason         string    `json:"reason"`
}
type CommercialAllotmentCreate struct {
	PoolID      ids.ID    `json:"pool_id"`
	PoolVersion int64     `json:"pool_version"`
	Channel     string    `json:"channel"`
	DateFrom    string    `json:"date_from"`
	DateTo      string    `json:"date_to"`
	Quantity    int       `json:"quantity"`
	ReleaseAt   time.Time `json:"release_at"`
	Reason      string    `json:"reason"`
}

func (h *Handler) allotmentList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "inventory_allotments", "allotments:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		v, e := typedJSON[CommercialAllotment](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(a) FROM inventory_allotments a WHERE organisation_id=$1 AND id=$2`, org, id)
		if v.State == "ACTIVE" && !h.Auth.Service.Now().Before(v.ReleaseAt) {
			v.State = "EXPIRED"
		}
		return v, e
	})
}
func (h *Handler) allotmentCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialAllotmentCreate
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pool, e := inventory.GetPool(r.Context(), h.Auth.Service.Pool, t.Organisation, in.PoolID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Channel == "" || !inventory.ValidChannel(in.Channel) || in.Quantity < 1 || in.Quantity > pool.Capacity || in.PoolVersion < 1 || !validStaffReason(in.Reason) || !h.Auth.Service.Now().Before(in.ReleaseAt) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current stock, channel, quantity, future release and reason required."))
		return
	}
	h.execute(w, r, "commercial:allotment:create", in, "allotments:write", "PROPERTY", pool.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "allotments:write", "PROPERTY", pool.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if e = h.commercialGate(ctx, tx, t, pool.PropertyID); e != nil {
			return 0, nil, e
		}
		current, e := inventory.GetPool(ctx, tx, t.Organisation, pool.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.PoolVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Pool version changed.")
		}
		if e = inventory.CheckStockAuthority(ctx, tx, current); e != nil {
			return 0, nil, e
		}
		if e = h.commercialChannel(ctx, tx, t, inventory.Product{PropertyID: pool.PropertyID}, in.Channel); e != nil {
			return 0, nil, e
		}

		var timezone string
		if e = tx.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, pool.PropertyID).Scan(&timezone); e != nil {
			return 0, nil, e
		}
		dates, e := (inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}).Days(timezone)
		if e != nil {
			return 0, nil, apperrors.InvalidRequest("Invalid allotment date range.")
		}
		arrival, _ := time.ParseInLocation(time.DateOnly, in.DateFrom, mustCommercialLocation(timezone))
		if in.ReleaseAt.After(arrival) {
			return 0, nil, apperrors.InvalidRequest("Allotments must release no later than property-local arrival midnight.")
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_allotment_days d SET released_at=$3 FROM inventory_allotments a WHERE a.id=d.allotment_id AND a.organisation_id=d.organisation_id AND d.organisation_id=$1 AND d.pool_id=$2 AND d.released_at IS NULL AND (a.state<>'ACTIVE' OR a.release_at<=$3)`, t.Organisation, pool.ID, h.Auth.Service.Now().UTC()); e != nil {
			return 0, nil, e
		}
		days, e := inventory.Calendar(ctx, tx, t.Organisation, pool.ID, inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}, timezone, "", h.Auth.Service.Now().UTC(), true)
		if e != nil {
			return 0, nil, e
		}
		for _, d := range days {
			if d.Available < in.Quantity {
				return 0, nil, apperrors.Conflict("INSUFFICIENT_INVENTORY", "Allotments reserve existing free stock; they cannot increase capacity.")
			}
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO inventory_allotments(id,organisation_id,property_id,pool_id,channel,date_from,date_to,quantity,release_at,reason,created_by) VALUES($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9,$10,$11)`, id, t.Organisation, pool.PropertyID, pool.ID, in.Channel, in.DateFrom, in.DateTo, in.Quantity, in.ReleaseAt, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		for _, day := range dates {
			if _, e = tx.Exec(ctx, `INSERT INTO inventory_allotment_days(organisation_id,allotment_id,pool_id,day,channel,quantity) VALUES($1,$2,$3,$4::date,$5,$6)`, t.Organisation, id, pool.ID, day, in.Channel, in.Quantity); e != nil {
				return 0, nil, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_calendar SET version=version+1,updated_by=$5,updated_at=now() WHERE organisation_id=$1 AND pool_id=$2 AND day>=$3::date AND day<$4::date`, t.Organisation, pool.ID, in.DateFrom, in.DateTo, t.Account); e != nil {
			return 0, nil, e
		}
		value, e := typedJSON[CommercialAllotment](ctx, tx, `SELECT to_jsonb(a) FROM inventory_allotments a WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, pool.PropertyID, id, "inventory_allotment", "ALLOTMENT_CREATED", businessEvents.InventoryChanged, 1, value)
		return 201, value, e
	})
}
func mustCommercialLocation(zone string) *time.Location {
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return time.UTC
	}
	return loc
}
func (h *Handler) allotmentRelease(w http.ResponseWriter, r *http.Request) {
	var in CommercialStateChange
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	a, e := typedJSON[CommercialAllotment](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(a) FROM inventory_allotments a WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if errors.Is(e, pgx.ErrNoRows) {
		e = apperrors.NotFound("ALLOTMENT_NOT_FOUND", "Allotment not found.")
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current allotment version and reason required."))
		return
	}
	h.execute(w, r, "commercial:allotment:release:"+id.String(), in, "allotments:write", "PROPERTY", a.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "allotments:write", "PROPERTY", a.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := typedJSON[CommercialAllotment](ctx, tx, `SELECT to_jsonb(a) FROM inventory_allotments a WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version || current.State != "ACTIVE" {
			return 0, nil, apperrors.Conflict("ALLOTMENT_STATE_CONFLICT", "Allotment state or version changed.")
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_allotments SET state='RELEASED',version=version+1 WHERE organisation_id=$1 AND id=$2`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_allotment_days SET released_at=now() WHERE organisation_id=$1 AND allotment_id=$2 AND released_at IS NULL`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_calendar SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND pool_id=$2 AND day>=$3::date AND day<$4::date`, t.Organisation, current.PoolID, current.DateFrom, current.DateTo); e != nil {
			return 0, nil, e
		}
		current.State = "RELEASED"
		current.Version++
		e = h.commercialCommit(ctx, tx, t, a.PropertyID, id, "inventory_allotment", "ALLOTMENT_RELEASED", businessEvents.InventoryChanged, current.Version, current)
		return 200, current, e
	})
}
