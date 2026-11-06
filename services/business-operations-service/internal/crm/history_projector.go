package crm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const HistoryConsumer = "business-operations-guest-history-v1"

type HistoryProjector struct {
	Pool   *pgxpool.Pool
	Outbox *outbox.Store
	Now    func() time.Time
}
type HistoryRecord struct {
	ID           ids.ID      `json:"id"`
	Version      int64       `json:"version"`
	SourceDigest string      `json:"source_digest"`
	Snapshot     StayHistory `json:"snapshot"`
}

func (p *HistoryProjector) clock() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *HistoryProjector) Handle(ctx context.Context, d events.Delivery) error {
	event := d.Envelope
	if event.Validate() != nil || event.Producer != string(platform.BusinessOperationsService) {
		return events.Permanent(errors.New("invalid guest history producer"))
	}
	var payload struct {
		OrganisationID ids.ID `json:"organisation_id"`
		Version        int64  `json:"version"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil || payload.OrganisationID.ValidateV7() != nil || payload.Version < 1 {
		return events.Permanent(errors.New("invalid history source reference"))
	}
	var org, reservation ids.ID
	var version int64
	var query string
	switch {
	case event.AggregateType == "reservation" && strings.HasPrefix(event.EventType, "reservation."):
		query = `SELECT organisation_id,id,version FROM reservations WHERE id=$1`
	case event.AggregateType == "stay" && strings.HasPrefix(event.EventType, "hospitality.stay."):
		query = `SELECT organisation_id,reservation_id,version FROM stay_records WHERE id=$1`
	case event.AggregateType == "folio" && event.EventType == "hospitality.folio.updated.v1":
		query = `SELECT organisation_id,reservation_id,version FROM folios WHERE id=$1`
	default:
		return events.Permanent(errors.New("unsupported guest history source"))
	}
	e := p.Pool.QueryRow(ctx, query, event.AggregateID).Scan(&org, &reservation, &version)
	if errors.Is(e, pgx.ErrNoRows) {
		return events.Permanent(errors.New("unknown owned history aggregate"))
	}
	if e != nil {
		return e
	}
	if payload.OrganisationID != org || payload.Version > version {
		return events.Permanent(errors.New("history source organisation or version mismatch"))
	}
	return database.WithTx(ctx, p.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); e != nil {
			return e
		}
		fresh, e := events.MarkProcessed(ctx, tx, HistoryConsumer, event)
		if e != nil || !fresh {
			return e
		}
		return p.RebuildTx(ctx, tx, org, reservation, &event)
	})
}

// RebuildTx requires the organisation fence. Every snapshot is computed from
// owned canonical rows; replaying old events can only refresh current facts.
func (p *HistoryProjector) RebuildTx(ctx context.Context, tx pgx.Tx, org, reservation ids.ID, event *events.Envelope) error {
	ctx = requestcontext.WithActor(ctx, requestcontext.Actor{})
	if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT guest_id FROM reservation_guests WHERE organisation_id=$1 AND reservation_id=$2 AND guest_id IS NOT NULL UNION SELECT guest_id FROM stay_records WHERE organisation_id=$1 AND reservation_id=$2 ORDER BY guest_id`, org, reservation)
	if e != nil {
		return e
	}
	var guests []ids.ID
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			break
		}
		guests = append(guests, id)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	for _, guest := range guests {
		fact, e := CurrentStayHistory(ctx, tx, org, reservation, guest, p.clock())
		if e != nil {
			return e
		}
		id := ids.New()
		version := int64(1)
		var oldDigest string
		e = tx.QueryRow(ctx, `SELECT id,version,source_digest FROM stay_history_projection WHERE organisation_id=$1 AND guest_id=$2 AND reservation_id=$3`, org, guest, reservation).Scan(&id, &version, &oldDigest)
		exists := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if exists && oldDigest == fact.SourceDigest {
			continue
		}
		raw, e := json.Marshal(fact)
		if e != nil {
			return e
		}
		if exists {
			version++
			_, e = tx.Exec(ctx, `UPDATE stay_history_projection SET source_digest=$3,snapshot=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, id, fact.SourceDigest, raw)
		} else {
			_, e = tx.Exec(ctx, `INSERT INTO stay_history_projection(id,organisation_id,property_id,guest_id,reservation_id,source_digest,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, org, fact.PropertyID, guest, reservation, fact.SourceDigest, raw)
		}
		if e != nil {
			return e
		}
		var eventID, sourceVersion, eventType, aggregateType, aggregateID any
		if event != nil {
			eventID = event.EventID
			eventType = event.EventType
			aggregateType = event.AggregateType
			aggregateID = event.AggregateID
			var payload struct {
				Version int64 `json:"version"`
			}
			if e = json.Unmarshal(event.Payload, &payload); e != nil {
				return e
			}
			sourceVersion = payload.Version
		}
		_, e = tx.Exec(ctx, `INSERT INTO guest_stay_history(id,organisation_id,property_id,projection_id,projection_version,source_digest,snapshot,source_event_id,source_event_version,source_event_type,source_aggregate_type,source_aggregate_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, ids.New(), org, fact.PropertyID, id, version, fact.SourceDigest, raw, eventID, sourceVersion, eventType, aggregateType, aggregateID)
		if e != nil {
			return e
		}
		// Event payloads omit monetary amounts and guest profile/contact data.
		if e = businessEvents.CommercialCommit(ctx, tx, p.Outbox, org, fact.PropertyID, id, ids.Nil, "guest_stay_projection", "GUEST_STAY_PROJECTED", "guest.stay_history.updated.v1", version, map[string]any{"guest_id": guest, "reservation_id": reservation, "reservation_version": fact.ReservationVersion, "state": fact.ReservationState, "completed": fact.Completed, "source_digest": fact.SourceDigest, "actor_service": "business-operations-service"}); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO crm_history_cursors(organisation_id,reservation_id,next_check_at) VALUES($1,$2,$3) ON CONFLICT(organisation_id,reservation_id) DO UPDATE SET next_check_at=excluded.next_check_at,failure_count=0,last_error_code=NULL,last_failed_at=NULL,updated_at=now()`, org, reservation, p.clock().Add(time.Minute))
	return e
}

// Tick also reconciles time-sensitive evidence and recovers after broker
// retention or restart. Due cursors prevent old completed roots starving new
// guests. Transactional digest/version guards make overlapping workers safe.
func (p *HistoryProjector) Tick(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, e := p.Pool.Query(ctx, `SELECT r.organisation_id,r.id FROM reservations r LEFT JOIN crm_history_cursors c ON c.organisation_id=r.organisation_id AND c.reservation_id=r.id WHERE c.last_error_code IS DISTINCT FROM 'CRM_HISTORY_SOURCE_QUARANTINED' AND (c.next_check_at IS NULL OR c.next_check_at<=$1) AND (EXISTS(SELECT 1 FROM reservation_guests g WHERE g.organisation_id=r.organisation_id AND g.reservation_id=r.id AND g.guest_id IS NOT NULL) OR EXISTS(SELECT 1 FROM stay_records s WHERE s.organisation_id=r.organisation_id AND s.reservation_id=r.id)) ORDER BY coalesce(c.next_check_at,r.created_at),r.id LIMIT $2`, p.clock(), limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	var refs []ref
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.id); e != nil {
			break
		}
		refs = append(refs, v)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	var firstError error
	for _, v := range refs {
		var sourceError error
		e = database.WithTx(ctx, p.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, v.org); e != nil {
				return e
			}
			var deferred bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM crm_history_cursors WHERE organisation_id=$1 AND reservation_id=$2 AND (next_check_at>$3 OR last_error_code='CRM_HISTORY_SOURCE_QUARANTINED'))`, v.org, v.id, p.clock()).Scan(&deferred); e != nil {
				return e
			}
			if deferred {
				return nil
			}
			if _, e := tx.Exec(ctx, `SAVEPOINT crm_history_source`); e != nil {
				return e
			}
			sourceError = p.RebuildTx(ctx, tx, v.org, v.id, nil)
			if sourceError == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Keep the organisation fence while discarding all partial source writes.
			// A failed worker cannot overwrite another worker's later successful cursor.
			if _, e := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT crm_history_source`); e != nil {
				return errors.Join(sourceError, e)
			}
			_, e := tx.Exec(ctx, `INSERT INTO crm_history_cursors(organisation_id,reservation_id,next_check_at,failure_count,last_error_code,last_failed_at) VALUES($1,$2,$3,1,'CRM_HISTORY_SOURCE_REVIEW_REQUIRED',$4) ON CONFLICT(organisation_id,reservation_id) DO UPDATE SET failure_count=crm_history_cursors.failure_count+1,last_failed_at=excluded.last_failed_at,last_error_code=CASE WHEN crm_history_cursors.failure_count>=11 THEN 'CRM_HISTORY_SOURCE_QUARANTINED' ELSE 'CRM_HISTORY_SOURCE_REVIEW_REQUIRED' END,next_check_at=CASE WHEN crm_history_cursors.failure_count>=11 THEN $4::timestamptz+interval '1 hour' ELSE excluded.next_check_at END,updated_at=now()`, v.org, v.id, p.clock().Add(5*time.Minute), p.clock())
			return e
		})
		if e != nil {
			return e
		}
		if sourceError != nil && firstError == nil {
			firstError = sourceError
		}
	}
	return firstError
}
