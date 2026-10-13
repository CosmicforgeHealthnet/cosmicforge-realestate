// Package telemetry exports bounded, reference-free operational measurements.
package telemetry

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"time"
)

type Observation struct {
	Family, Kind, State string
	Value               float64
}

// Snapshot reads authoritative local rows. No external consumer success or money
// balance is inferred. A failed query is UNKNOWN, never an all-zero snapshot.
func Snapshot(ctx context.Context, pool *pgxpool.Pool) ([]Observation, error) {
	rows, err := pool.Query(ctx, `
 SELECT 'events' AS family,'outbox' AS kind,CASE WHEN dead_at IS NULL THEN 'PENDING' ELSE 'DEAD' END AS state,count(*)::float8 AS value FROM outbox_events WHERE published_at IS NULL GROUP BY state
 UNION ALL SELECT 'events','oldest_pending_age_seconds','PENDING',coalesce(extract(epoch FROM now()-min(created_at)),0)::float8 FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL
 UNION ALL SELECT 'events','review',failure_class,count(*)::float8 FROM owner_event_review r WHERE NOT EXISTS(SELECT 1 FROM owner_event_review_decisions d WHERE d.review_id=r.id) GROUP BY failure_class
 UNION ALL SELECT 'workers','notification',status,count(*)::float8 FROM notification_dispatches GROUP BY status
 UNION ALL SELECT 'workers','wallet_registration','MANUAL_REVIEW',count(*)::float8 FROM wallet_registration_delivery WHERE delivered_at IS NULL AND attempts>=20
 UNION ALL SELECT 'workers','notification_attempts','OBSERVED',coalesce(sum(attempts),0)::float8 FROM notification_dispatches
 UNION ALL SELECT 'workers','expired_notification_lease','RECOVERY_DUE',count(*)::float8 FROM notification_dispatches WHERE status='PENDING' AND lease_expires_at<now()
 UNION ALL SELECT 'workers','expired_outbox_lease','RECOVERY_DUE',count(*)::float8 FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL AND lease_expires_at<now()
 UNION ALL SELECT 'workers','expired_outbox_lease_age_seconds','RECOVERY_DUE',coalesce(greatest(extract(epoch FROM now()-min(lease_expires_at)),0),0)::float8 FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL AND lease_expires_at<now()
 UNION ALL SELECT 'workers','external_operation',state,count(*)::float8 FROM owner_external_operations GROUP BY state
 UNION ALL SELECT 'workers','external_operation_age_seconds','UNKNOWN',coalesce(extract(epoch FROM now()-min(updated_at)),0)::float8 FROM owner_external_operations WHERE state='UNKNOWN'
 UNION ALL SELECT 'business','listings',status,count(*)::float8 FROM listings GROUP BY status
 UNION ALL SELECT 'business','stale_paused_listings','PAUSED',count(*)::float8 FROM listings WHERE status='PAUSED' AND pause_source='SYSTEM' AND pause_reason LIKE '%STALE%'
 UNION ALL SELECT 'business','occupancy',status,count(*)::float8 FROM tenancies GROUP BY status
 UNION ALL SELECT 'business','rent_obligations',status,count(*)::float8 FROM rent_obligations GROUP BY status
 UNION ALL SELECT 'business','verification',status,count(*)::float8 FROM verification_links GROUP BY status
 UNION ALL SELECT 'business','ownership',status,count(*)::float8 FROM ownership_records GROUP BY status
 UNION ALL SELECT 'business','core_readback_due','UNKNOWN',count(*)::float8 FROM financial_core_business_bindings WHERE updated_at<now()-interval '15 minutes'
 UNION ALL SELECT 'business','booking_payment_mismatch','MISMATCH',count(*)::float8 FROM shortlet_bookings b JOIN financial_core_business_bindings c ON c.business_type='shortlet_booking' AND c.business_id=b.id WHERE c.observed_state IN ('INITIALIZED','FAILED','CANCELLED') AND (b.paid_minor>0 OR b.status IN ('CONFIRMED','CHECKED_IN'))
 UNION ALL SELECT 'business','calendar_conflict','CONFLICT',coalesce(sum(jsonb_array_length(changes->'conflicts')),0)::float8 FROM audit_events WHERE action='shortlet.calendar.external_import'
 UNION ALL SELECT 'business','application_decision_mean_seconds','OBSERVED',coalesce(avg(extract(epoch FROM d.decided_at-a.created_at)),0)::float8 FROM application_decisions d JOIN rental_applications a ON a.id=d.application_id
 UNION ALL SELECT 'security','security_events',event_type,count(*)::float8 FROM account_security_events WHERE event_type IN ('MFA_FAILED','LOGIN_FAILED','NEW_DEVICE_LOGIN','ACCOUNT_LOCKED','STEP_UP_FAILED') GROUP BY event_type
 UNION ALL SELECT 'security','audit_denials','DENIED',count(*)::float8 FROM audit_events WHERE result='DENIED'
 UNION ALL SELECT 'security','authority_review','REVIEW',count(*)::float8 FROM ownership_records WHERE status IN ('CONFLICT','MANUAL_REVIEW')
 UNION ALL SELECT 'db','lock_waiters','WAITING',count(*)::float8 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'
 `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Observation{}
	for rows.Next() {
		var v Observation
		if err := rows.Scan(&v.Family, &v.Kind, &v.State, &v.Value); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Register reuses the configured OTEL exporter. Unregister before closing pool.
func Register(pool *pgxpool.Pool) (metric.Registration, error) {
	meter := otel.Meter("individual-owner-service")
	return register(meter, pool)
}
func register(meter metric.Meter, pool *pgxpool.Pool) (metric.Registration, error) {
	values, err := meter.Float64ObservableGauge("owner_operational_observation", metric.WithDescription("Local count or duration, identified by family and kind"))
	if err != nil {
		return nil, err
	}
	available, err := meter.Int64ObservableGauge("owner_operational_observation_available", metric.WithDescription("1 only when the local snapshot could be read"))
	if err != nil {
		return nil, err
	}
	connections, err := meter.Int64ObservableGauge("owner_db_connections", metric.WithDescription("Connection pool state"))
	if err != nil {
		return nil, err
	}
	return meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		st := pool.Stat()
		for state, n := range map[string]int64{"acquired": int64(st.AcquiredConns()), "idle": int64(st.IdleConns()), "maximum": int64(st.MaxConns())} {
			o.ObserveInt64(connections, n, metric.WithAttributes(attribute.String("state", state)))
		}
		qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		snapshot, err := Snapshot(qctx, pool)
		if err != nil {
			o.ObserveInt64(available, 0)
			return nil
		}
		o.ObserveInt64(available, 1)
		for _, v := range snapshot {
			o.ObserveFloat64(values, v.Value, metric.WithAttributes(attribute.String("family", v.Family), attribute.String("kind", v.Kind), attribute.String("state", v.State)))
		}
		return nil
	}, values, available, connections)
}
