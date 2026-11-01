package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	Pool   *pgxpool.Pool
	Outbox *outbox.Store
	Now    func() time.Time
}

func load[T any](ctx context.Context, q database.Querier, query string, org, id ids.ID) (T, error) {
	var value T
	var raw []byte
	err := q.QueryRow(ctx, query, org, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, apperrors.NotFound("COMMERCIAL_RESOURCE_NOT_FOUND", "Commercial resource not found.")
	}
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(raw, &value)
	return value, err
}
func GetProduct(ctx context.Context, q database.Querier, org, id ids.ID) (Product, error) {
	return load[Product](ctx, q, `SELECT to_jsonb(p) FROM sellable_products p WHERE organisation_id=$1 AND id=$2`, org, id)
}
func GetPool(ctx context.Context, q database.Querier, org, id ids.ID) (Pool, error) {
	return load[Pool](ctx, q, `SELECT to_jsonb(p) FROM inventory_pools p WHERE organisation_id=$1 AND id=$2`, org, id)
}
func GetHold(ctx context.Context, q database.Querier, org, id ids.ID) (Hold, error) {
	return load[Hold](ctx, q, `SELECT to_jsonb(h) FROM inventory_holds h WHERE organisation_id=$1 AND id=$2`, org, id)
}

// CheckStockAuthority rejects stale physical membership, even if maintenance
// has not yet reconciled the graph. A product never manufactures extra stock.
func CheckStockAuthority(ctx context.Context, q database.Querier, pool Pool) error {
	var current bool
	err := q.QueryRow(ctx, `SELECT p.state='ACTIVE' AND EXISTS(SELECT 1 FROM inventory_nodes source WHERE source.organisation_id=p.organisation_id AND source.id=p.node_id AND source.version=p.source_node_version AND source.state='ACTIVE' AND business_graph_property(source.organisation_id,source.id)=p.property_id) AND EXISTS(SELECT 1 FROM inventory_pool_members WHERE organisation_id=p.organisation_id AND pool_id=p.id) AND NOT EXISTS(SELECT 1 FROM inventory_pool_members m JOIN inventory_nodes n ON n.organisation_id=m.organisation_id AND n.id=m.node_id WHERE m.organisation_id=p.organisation_id AND m.pool_id=p.id AND (n.state<>'ACTIVE' OR n.version<>m.node_version OR business_graph_property(n.organisation_id,n.id) IS DISTINCT FROM p.property_id)) FROM inventory_pools p WHERE p.organisation_id=$1 AND p.id=$2`, pool.OrganisationID, pool.ID).Scan(&current)
	if err != nil {
		return err
	}
	if !current {
		return apperrors.Conflict("STOCK_AUTHORITY_STALE", "Active unchanged physical stock is required; revalidate the pool after a graph change.")
	}
	return nil
}

// Calendar locks in civil-date order, the same order used by multi-day holds.
// Expired holds/allotments cease to consume stock independently of worker uptime.
func Calendar(ctx context.Context, q database.Querier, org, pool ids.ID, dateRange DateRange, timezone, channel string, now time.Time, lock bool) ([]Day, error) {
	days, err := dateRange.Days(timezone)
	if err != nil || !ValidChannel(channel) {
		return nil, apperrors.InvalidRequest("Valid checkout-exclusive dates and channel are required.")
	}
	if lock {
		rows, e := q.Query(ctx, `SELECT day FROM inventory_calendar WHERE organisation_id=$1 AND pool_id=$2 AND day>=$3::date AND day<$4::date ORDER BY day FOR UPDATE`, org, pool, dateRange.DateFrom, dateRange.DateTo)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	rows, err := q.Query(ctx, `SELECT c.day::text,c.capacity,c.committed,c.overbooked,c.blocked,c.version,c.updated_at,
 (SELECT coalesce(sum(d.quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND h.state='ACTIVE' AND h.expires_at>$6),
 (SELECT coalesce(sum(d.quantity),0) FROM inventory_allotment_days d JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND d.released_at IS NULL AND a.state='ACTIVE' AND a.release_at>$6),
 (SELECT coalesce(sum(d.allotted_quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND h.state='ACTIVE' AND h.expires_at>$6 AND a.state='ACTIVE' AND a.release_at>$6),
 (SELECT d.allotment_id FROM inventory_allotment_days d JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND d.channel=$5 AND d.released_at IS NULL AND a.state='ACTIVE' AND a.release_at>$6),
 (SELECT coalesce(sum(d.quantity),0) FROM inventory_allotment_days d JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND d.channel=$5 AND d.released_at IS NULL AND a.state='ACTIVE' AND a.release_at>$6),
 (SELECT coalesce(sum(d.allotted_quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND h.channel=$5 AND h.state='ACTIVE' AND h.expires_at>$6 AND a.state='ACTIVE' AND a.release_at>$6),
 (SELECT coalesce(sum(d.allotted_quantity),0) FROM reservation_inventory_days d JOIN inventory_allotments a ON a.organisation_id=d.organisation_id AND a.id=d.allotment_id WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND d.released_at IS NULL AND a.state='ACTIVE' AND a.release_at>$6),
 (SELECT coalesce(sum(d.allotted_quantity),0) FROM reservation_inventory_days d JOIN inventory_allotments a ON a.organisation_id=d.organisation_id AND a.id=d.allotment_id WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND d.released_at IS NULL AND a.channel=$5 AND a.state='ACTIVE' AND a.release_at>$6)
 FROM inventory_calendar c WHERE c.organisation_id=$1 AND c.pool_id=$2 AND c.day>=$3::date AND c.day<$4::date ORDER BY c.day`, org, pool, dateRange.DateFrom, dateRange.DateTo, channel, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Day{}
	for rows.Next() {
		var d Day
		var usedAllotted, ownAllotted, ownUsed, committedAllotted, ownCommitted int
		if err = rows.Scan(&d.Day, &d.Capacity, &d.Committed, &d.Overbooked, &d.Blocked, &d.Version, &d.UpdatedAt, &d.Held, &d.Allotted, &usedAllotted, &d.AllotmentID, &ownAllotted, &ownUsed, &committedAllotted, &ownCommitted); err != nil {
			return nil, err
		}
		d.Allotted = max(0, d.Allotted-committedAllotted)
		d.ChannelAllotted = max(0, ownAllotted-ownUsed-ownCommitted)
		d.Available = max(0, d.Capacity-d.Committed-d.Blocked-d.Held-d.Allotted+usedAllotted) + d.ChannelAllotted
		d.PhysicalAvailable = max(0, d.Capacity-d.Committed+d.Overbooked-d.Blocked-d.Held-d.Allotted+usedAllotted) + d.ChannelAllotted
		result = append(result, d)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(days) {
		return nil, apperrors.Conflict("CALENDAR_INCOMPLETE", "Every requested civil date must be configured before quoting or holding inventory.")
	}
	return result, nil
}

type HoldCommand struct {
	ProductID       ids.ID `json:"product_id"`
	QuoteID         ids.ID `json:"quote_id"`
	ProductVersion  int64  `json:"product_version"`
	PoolVersion     int64  `json:"pool_version"`
	DateFrom        string `json:"date_from"`
	DateTo          string `json:"date_to"`
	Quantity        int    `json:"quantity"`
	Channel         string `json:"channel,omitempty"`
	DurationSeconds int    `json:"duration_seconds"`
}

// PlaceHold runs inside the caller's owning, authorised transaction. The quote
// and current restrictions are revalidated by the caller before this stock fence.
func (r Repository) PlaceHold(ctx context.Context, tx pgx.Tx, org, actor ids.ID, command HoldCommand) (Hold, error) {
	if _, err := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); err != nil {
		return Hold{}, err
	}
	now := r.Now().UTC()
	p, err := GetProduct(ctx, tx, org, command.ProductID)
	if err != nil {
		return Hold{}, err
	}
	pool, err := GetPool(ctx, tx, org, p.PoolID)
	if err != nil {
		return Hold{}, err
	}
	if command.Quantity < 1 || command.Quantity > 100000 || command.DurationSeconds < 30 || command.DurationSeconds > 900 || p.State != "ACTIVE" || p.Version != command.ProductVersion || pool.Version != command.PoolVersion || !ValidChannel(command.Channel) {
		return Hold{}, apperrors.Conflict("HOLD_POLICY_OR_VERSION", "Active product, current versions, bounded duration and valid quantity/channel required.")
	}
	if err = CheckStockAuthority(ctx, tx, pool); err != nil {
		return Hold{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM inventory_pools WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, org, pool.ID); err != nil {
		return Hold{}, err
	}
	var timezone string
	if err = tx.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, org, p.PropertyID).Scan(&timezone); err != nil {
		return Hold{}, err
	}
	days, err := Calendar(ctx, tx, org, pool.ID, DateRange{DateFrom: command.DateFrom, DateTo: command.DateTo}, timezone, command.Channel, now, true)
	if err != nil {
		return Hold{}, err
	}
	for _, day := range days {
		if day.Available < command.Quantity {
			return Hold{}, apperrors.Conflict("INSUFFICIENT_INVENTORY", "The full requested range cannot be held atomically.")
		}
	}
	var quoteCurrent bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM price_quotes WHERE organisation_id=$1 AND id=$2 AND product_id=$3 AND date_from=$4::date AND date_to=$5::date AND expires_at>$6 AND snapshot->>'channel'=$7 AND (snapshot->>'quantity')::integer=$8)`, org, command.QuoteID, p.ID, command.DateFrom, command.DateTo, now, command.Channel, command.Quantity).Scan(&quoteCurrent); err != nil {
		return Hold{}, err
	}
	if !quoteCurrent {
		return Hold{}, apperrors.Conflict("QUOTE_EXPIRED_OR_MISMATCH", "A current matching immutable quote is required.")
	}
	hold := Hold{ID: ids.New(), OrganisationID: org, PropertyID: p.PropertyID, ProductID: p.ID, PoolID: pool.ID, QuoteID: command.QuoteID, DateFrom: command.DateFrom, DateTo: command.DateTo, Quantity: command.Quantity, Channel: command.Channel, State: "ACTIVE", ExpiresAt: now.Add(time.Duration(command.DurationSeconds) * time.Second), Version: 1, CreatedAt: now}
	if _, err = tx.Exec(ctx, `INSERT INTO inventory_holds(id,organisation_id,property_id,product_id,pool_id,quote_id,date_from,date_to,quantity,channel,expires_at,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7::date,$8::date,$9,$10,$11,$12,$13)`, hold.ID, org, p.PropertyID, p.ID, pool.ID, hold.QuoteID, hold.DateFrom, hold.DateTo, hold.Quantity, hold.Channel, hold.ExpiresAt, actor, now); err != nil {
		return Hold{}, err
	}
	for _, day := range days {
		allocated := min(command.Quantity, day.ChannelAllotted)
		var allotment any
		if allocated > 0 {
			allotment = day.AllotmentID
		}
		if _, err = tx.Exec(ctx, `INSERT INTO inventory_hold_days(organisation_id,hold_id,pool_id,day,quantity,allotment_id,allotted_quantity) VALUES($1,$2,$3,$4::date,$5,$6,$7)`, org, hold.ID, pool.ID, day.Day, command.Quantity, allotment, allocated); err != nil {
			return Hold{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE inventory_calendar SET updated_by_service=coalesce(nullif(current_setting('business.actor_service',true),''),''),version=version+1,updated_at=$5,updated_by=$6 WHERE organisation_id=$1 AND pool_id=$2 AND day>=$3::date AND day<$4::date`, org, pool.ID, hold.DateFrom, hold.DateTo, now, actor); err != nil {
		return Hold{}, err
	}
	err = businessEvents.CommercialCommit(ctx, tx, r.Outbox, org, p.PropertyID, hold.ID, actor, "inventory_hold", "INVENTORY_HOLD_CREATED", businessEvents.InventoryChanged, 1, hold)
	return hold, err
}
