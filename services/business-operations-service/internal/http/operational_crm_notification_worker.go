package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

// Notifiable reservation transitions. Later in-house amendments and quantity
// changes use these action families.
const notifiableReservationAction = `(v.action IN ('CONFIRMED','AMENDED','CANCELLED','PARTIAL_CANCELLED','SOURCE_CANCELLED','SPLIT','MERGED','ROOM_MOVED','NO_SHOW','REJECTED','EXPIRED') OR v.action LIKE '%EXTEN%' OR v.action LIKE '%EARLY_DEPART%' OR v.action LIKE 'QUANTITY_%')`

const primaryGuest = `LEFT JOIN LATERAL (SELECT g.guest_id FROM reservation_guests g WHERE g.organisation_id=r.organisation_id AND g.reservation_id=r.id AND g.guest_id IS NOT NULL ORDER BY g.is_primary DESC,g.created_at,g.id LIMIT 1) pg ON true`

func consentArray(kind string) string {
	base := "ARRAY['OPERATIONAL_CONTACT']::text[]"
	if kind == "LOYALTY_ENROLLED" || kind == "LOYALTY_REWARD" {
		base = "ARRAY['OPERATIONAL_CONTACT','LOYALTY']::text[]"
	}
	if kind == "CORPORATE_BOOKING_UPDATE" {
		base = "ARRAY[]::text[]"
	}
	return "CASE WHEN t.purpose='MARKETING' THEN " + base + "||ARRAY['MARKETING']::text[] ELSE " + base + " END"
}

// guestNotificationPlans derive jobs only from authoritative owned rows. The
// unique (organisation, source, kind, recipient) key makes planning idempotent.
func guestNotificationPlans() []struct{ kind, sql string } {
	tmpl := func(kind, alias string) string {
		return ` JOIN guest_notification_templates t ON t.organisation_id=` + alias + `.organisation_id AND t.property_id=` + alias + `.property_id AND t.kind='` + kind + `' AND t.state='ACTIVE' `
	}
	ins := `INSERT INTO guest_notification_jobs(id,organisation_id,property_id,recipient_kind,guest_id,corporate_id,source_kind,source_id,kind,purpose,consent_purposes,variables,expires_at) `
	notPlanned := func(sourceKind, idExpr, kind string) string {
		return ` AND NOT EXISTS(SELECT 1 FROM guest_notification_jobs x WHERE x.organisation_id=t.organisation_id AND x.source_kind='` + sourceKind + `' AND x.source_id=` + idExpr + ` AND x.kind='` + kind + `') `
	}
	return []struct{ kind, sql string }{
		{"WAITLIST_OFFER", ins + `SELECT business_crm_uuidv7(),o.organisation_id,o.property_id,'GUEST',b.guest_id,NULL,'WAITLIST_OFFER',o.id,'WAITLIST_OFFER',t.purpose,` + consentArray("WAITLIST_OFFER") + `,jsonb_build_object('offer_id',o.id,'entry_id',w.id,'date_from',w.date_from,'date_to',w.date_to,'quantity',w.quantity,'offer_expires_at',o.expires_at),o.expires_at FROM waitlist_offers o JOIN waitlist_entries w ON w.organisation_id=o.organisation_id AND w.id=o.entry_id JOIN waitlist_guest_bindings b ON b.organisation_id=o.organisation_id AND b.entry_id=o.entry_id` + tmpl("WAITLIST_OFFER", "o") + `WHERE o.state='OFFERED' AND o.expires_at>$2` + notPlanned("WAITLIST_OFFER", "o.id", "WAITLIST_OFFER") + `ORDER BY o.created_at,o.id LIMIT $1 ON CONFLICT DO NOTHING`},
		{"WAITLIST_EXPIRED", ins + `SELECT business_crm_uuidv7(),o.organisation_id,o.property_id,'GUEST',b.guest_id,NULL,'WAITLIST_OFFER',o.id,'WAITLIST_EXPIRED',t.purpose,` + consentArray("WAITLIST_EXPIRED") + `,jsonb_build_object('offer_id',o.id,'entry_id',w.id,'date_from',w.date_from,'date_to',w.date_to,'entry_state',w.state),o.updated_at+interval '48 hours' FROM waitlist_offers o JOIN waitlist_entries w ON w.organisation_id=o.organisation_id AND w.id=o.entry_id JOIN waitlist_guest_bindings b ON b.organisation_id=o.organisation_id AND b.entry_id=o.entry_id` + tmpl("WAITLIST_EXPIRED", "o") + `WHERE o.state='EXPIRED' AND o.updated_at+interval '48 hours'>$2` + notPlanned("WAITLIST_OFFER", "o.id", "WAITLIST_EXPIRED") + `ORDER BY o.updated_at,o.id LIMIT $1 ON CONFLICT DO NOTHING`},
		{"RESERVATION_CHANGE", ins + `SELECT business_crm_uuidv7(),v.organisation_id,v.property_id,'GUEST',pg.guest_id,NULL,'RESERVATION_VERSION',v.id,'RESERVATION_CHANGE',t.purpose,` + consentArray("RESERVATION_CHANGE") + `,jsonb_build_object('reservation_id',r.id,'reservation_version',v.reservation_version,'action',v.action,'state',v.snapshot->'reservation'->>'state','date_from',v.snapshot->'reservation'->>'date_from','date_to',v.snapshot->'reservation'->>'date_to'),v.created_at+interval '48 hours' FROM reservation_versions v JOIN reservations r ON r.organisation_id=v.organisation_id AND r.id=v.reservation_id ` + primaryGuest + tmpl("RESERVATION_CHANGE", "v") + `WHERE pg.guest_id IS NOT NULL AND v.created_at+interval '48 hours'>$2 AND ` + notifiableReservationAction + notPlanned("RESERVATION_VERSION", "v.id", "RESERVATION_CHANGE") + `ORDER BY v.created_at,v.id LIMIT $1 ON CONFLICT DO NOTHING`},
		{"CORPORATE_BOOKING_UPDATE", ins + `SELECT business_crm_uuidv7(),v.organisation_id,v.property_id,'CORPORATE',NULL,r.corporate_id,'RESERVATION_VERSION',v.id,'CORPORATE_BOOKING_UPDATE',t.purpose,` + consentArray("CORPORATE_BOOKING_UPDATE") + `,jsonb_build_object('reservation_id',r.id,'reservation_version',v.reservation_version,'action',v.action,'state',v.snapshot->'reservation'->>'state','date_from',v.snapshot->'reservation'->>'date_from','date_to',v.snapshot->'reservation'->>'date_to'),v.created_at+interval '48 hours' FROM reservation_versions v JOIN reservations r ON r.organisation_id=v.organisation_id AND r.id=v.reservation_id` + tmpl("CORPORATE_BOOKING_UPDATE", "v") + `WHERE r.corporate_id IS NOT NULL AND v.created_at+interval '48 hours'>$2 AND (v.action='RESERVATION_CREATED' OR ` + notifiableReservationAction + `)` + notPlanned("RESERVATION_VERSION", "v.id", "CORPORATE_BOOKING_UPDATE") + `ORDER BY v.created_at,v.id LIMIT $1 ON CONFLICT DO NOTHING`},
		{"ARRIVAL_NOTICE", ins + `SELECT business_crm_uuidv7(),r.organisation_id,r.property_id,'GUEST',pg.guest_id,NULL,'RESERVATION',r.id,'ARRIVAL_NOTICE',t.purpose,` + consentArray("ARRIVAL_NOTICE") + `,jsonb_build_object('reservation_id',r.id,'date_from',r.date_from,'date_to',r.date_to),((r.date_from+1)::timestamp AT TIME ZONE n.timezone) FROM reservations r JOIN inventory_nodes n ON n.organisation_id=r.organisation_id AND n.id=r.property_id ` + primaryGuest + tmpl("ARRIVAL_NOTICE", "r") + `WHERE r.state='CONFIRMED' AND pg.guest_id IS NOT NULL AND r.date_from BETWEEN ($2 AT TIME ZONE n.timezone)::date AND ($2 AT TIME ZONE n.timezone)::date+2 AND ((r.date_from+1)::timestamp AT TIME ZONE n.timezone)>$2` + notPlanned("RESERVATION", "r.id", "ARRIVAL_NOTICE") + `ORDER BY r.date_from,r.id LIMIT $1 ON CONFLICT DO NOTHING`},
		{"STAY_NOTICE", ins + `SELECT business_crm_uuidv7(),s.organisation_id,s.property_id,'GUEST',s.guest_id,NULL,'STAY',s.id,'STAY_NOTICE',t.purpose,` + consentArray("STAY_NOTICE") + `,jsonb_build_object('stay_id',s.id,'reservation_id',s.reservation_id),s.checked_in_at+interval '24 hours' FROM stay_records s` + tmpl("STAY_NOTICE", "s") + `WHERE s.state='ACTIVE' AND s.checked_in_at+interval '24 hours'>$2` + notPlanned("STAY", "s.id", "STAY_NOTICE") + `ORDER BY s.checked_in_at,s.id LIMIT $1 ON CONFLICT DO NOTHING`},
		{"SERVICE_RECOVERY_UPDATE", ins + `SELECT business_crm_uuidv7(),a.organisation_id,a.property_id,'GUEST',c.guest_id,NULL,'SERVICE_RECOVERY_ACTION',a.id,'SERVICE_RECOVERY_UPDATE',t.purpose,` + consentArray("SERVICE_RECOVERY_UPDATE") + `,jsonb_build_object('case_id',c.id,'state',a.to_state,'issue_type',c.issue_type),a.created_at+interval '72 hours' FROM service_recovery_actions a JOIN service_recovery_cases c ON c.organisation_id=a.organisation_id AND c.id=a.case_id` + tmpl("SERVICE_RECOVERY_UPDATE", "a") + `WHERE a.to_state IN ('INVESTIGATING','APPROVED','EXECUTION_PENDING','RESOLVED','CLOSED') AND a.created_at+interval '72 hours'>$2` + notPlanned("SERVICE_RECOVERY_ACTION", "a.id", "SERVICE_RECOVERY_UPDATE") + `ORDER BY a.created_at,a.id LIMIT $1 ON CONFLICT DO NOTHING`},
		{"COMPENSATION_STATUS", ins + `SELECT business_crm_uuidv7(),x.organisation_id,x.property_id,'GUEST',c.guest_id,NULL,'RECOVERY_COMPENSATION',x.id,'COMPENSATION_STATUS',t.purpose,` + consentArray("COMPENSATION_STATUS") + `,jsonb_build_object('case_id',c.id,'compensation_id',x.id,'kind',x.kind,'state',x.state),x.updated_at+interval '72 hours' FROM service_recovery_compensation_refs x JOIN service_recovery_cases c ON c.organisation_id=x.organisation_id AND c.id=x.case_id` + tmpl("COMPENSATION_STATUS", "x") + `WHERE x.state IN ('EXECUTED','DECLINED') AND x.updated_at+interval '72 hours'>$2` + notPlanned("RECOVERY_COMPENSATION", "x.id", "COMPENSATION_STATUS") + `ORDER BY x.updated_at,x.id LIMIT $1 ON CONFLICT DO NOTHING`},
		{"LOYALTY_ENROLLED", ins + `SELECT business_crm_uuidv7(),a.organisation_id,a.property_id,'GUEST',a.guest_id,NULL,'LOYALTY_ACCOUNT',a.id,'LOYALTY_ENROLLED',t.purpose,` + consentArray("LOYALTY_ENROLLED") + `,jsonb_build_object('account_id',a.id,'tier',a.tier),a.created_at+interval '72 hours' FROM loyalty_accounts a` + tmpl("LOYALTY_ENROLLED", "a") + `WHERE a.state='ACTIVE' AND a.created_at+interval '72 hours'>$2` + notPlanned("LOYALTY_ACCOUNT", "a.id", "LOYALTY_ENROLLED") + `ORDER BY a.created_at,a.id LIMIT $1 ON CONFLICT DO NOTHING`},
		{"LOYALTY_REWARD", ins + `SELECT business_crm_uuidv7(),x.organisation_id,x.property_id,'GUEST',a.guest_id,NULL,'LOYALTY_TRANSACTION',x.id,'LOYALTY_REWARD',t.purpose,` + consentArray("LOYALTY_REWARD") + `,jsonb_build_object('account_id',a.id,'transaction_id',x.id,'transaction_kind',x.kind,'points',x.points),x.created_at+interval '72 hours' FROM loyalty_transactions x JOIN loyalty_accounts a ON a.organisation_id=x.organisation_id AND a.id=x.account_id` + tmpl("LOYALTY_REWARD", "x") + `WHERE x.kind IN ('ACCRUAL','REDEMPTION','RECOVERY_BENEFIT','REVERSAL') AND x.created_at+interval '72 hours'>$2` + notPlanned("LOYALTY_TRANSACTION", "x.id", "LOYALTY_REWARD") + `ORDER BY x.created_at,x.id LIMIT $1 ON CONFLICT DO NOTHING`},
	}
}

// PlanGuestNotifications is safe to run concurrently and repeatedly.
func (h *Handler) PlanGuestNotifications(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 500 {
		limit = 500
	}
	now := h.Auth.Service.Now().UTC()
	var total int64
	var failures []error
	for _, plan := range guestNotificationPlans() {
		tag, e := h.Auth.Service.Pool.Exec(ctx, plan.sql, limit, now)
		if e != nil {
			failures = append(failures, fmt.Errorf("plan %s: %w", plan.kind, e))
			continue
		}
		total += tag.RowsAffected()
	}
	return total, errors.Join(failures...)
}

// ProcessGuestNotifications plans and dispatches through the existing signed
// Notification client. All provider calls happen after a committed lease and
// use the job identity as the Notification Service idempotency key.
func (h *Handler) ProcessGuestNotifications(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	_, planErr := h.PlanGuestNotifications(ctx, limit*5)
	failures := []error{planErr}
	for range limit {
		owner := ids.New().String()
		now := h.Auth.Service.Now().UTC()
		j, e := typedJSON[CRMNotificationJob](ctx, h.Auth.Service.Pool, `UPDATE guest_notification_jobs j SET state='RUNNING',attempts=attempts+1,lease_owner=$1,lease_generation=lease_generation+1,lease_expires_at=$2::timestamptz+interval '60 seconds',version=version+1,updated_at=now() WHERE id=(SELECT id FROM guest_notification_jobs WHERE available_at<=$2 AND (state IN ('PENDING','RETRY') OR state='RUNNING' AND lease_expires_at<=$2) ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING to_jsonb(j)`, owner, now)
		if isNotFound(e) {
			break
		}
		if e != nil {
			failures = append(failures, e)
			break
		}
		jobCtx := requestcontext.WithRequestID(ctx, "guest-notification:"+j.ID.String())
		if e = h.dispatchGuestNotification(jobCtx, j, owner); e != nil {
			failures = append(failures, e)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.Join(failures...)
}

func isNotFound(e error) bool {
	if e == nil {
		return false
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return true
	}
	var app *apperrors.Error
	return errors.As(e, &app) && app.Status == 404
}

type notificationDecision struct {
	state, reason, attemptOutcome string
	request                       *notification.SendRequest
	templateID                    ids.ID
	templateVersion               int64
	channel                       string
	resolved                      *ids.ID
}

func (h *Handler) dispatchGuestNotification(ctx context.Context, j CRMNotificationJob, owner string) error {
	var d notificationDecision
	e := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
			return e
		}
		var live bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_notification_jobs WHERE id=$1 AND state='RUNNING' AND lease_owner=$2 AND lease_generation=$3 FOR UPDATE)`, j.ID, owner, j.LeaseGeneration).Scan(&live); e != nil {
			return e
		}
		if !live {
			d = notificationDecision{state: "LEASE_LOST"}
			return nil
		}
		var e error
		d, e = h.decideGuestNotification(ctx, tx, j)
		if e != nil {
			return e
		}
		if d.request == nil {
			return h.finishGuestNotification(ctx, tx, j, owner, d.state, d.reason, d.attemptOutcome, nil, nil, d.resolved)
		}
		_, e = tx.Exec(ctx, `UPDATE guest_notification_jobs SET template_id=$3,template_version=$4,channel=$5,resolved_guest_id=$6,version=version+1,updated_at=now() WHERE id=$1 AND lease_owner=$2`, j.ID, owner, d.templateID, d.templateVersion, d.channel, d.resolved)
		return e
	})
	if e != nil || d.request == nil {
		if e != nil && ctx.Err() == nil {
			// Evaluation failures retry with backoff; nothing was sent.
			_, ue := h.Auth.Service.Pool.Exec(ctx, `UPDATE guest_notification_jobs SET state='RETRY',last_error='EVALUATION_FAILED',available_at=now()+make_interval(secs=>least(3600,30*attempts)),lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE id=$1 AND lease_owner=$2 AND state='RUNNING'`, j.ID, owner)
			return errors.Join(e, ue)
		}
		return e
	}
	// External acceptance call: outside any transaction, after a committed lease.
	var accepted notification.Accepted
	var sendErr error
	if h.Notification == nil {
		sendErr = errors.New("notification client unavailable")
	} else {
		sendCtx := requestcontext.WithActor(ctx, requestcontext.Actor{})
		accepted, sendErr = h.Notification.Send(sendCtx, *d.request, "business-guest-notification:"+j.ID.String())
	}
	now := h.Auth.Service.Now().UTC()
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
			return e
		}
		cur, e := typedJSON[CRMNotificationJob](ctx, tx, `SELECT to_jsonb(j) FROM guest_notification_jobs j WHERE id=$1 FOR UPDATE`, j.ID)
		if e != nil {
			return e
		}
		if cur.State != "RUNNING" || cur.LeaseOwner == nil || *cur.LeaseOwner != owner || cur.LeaseGeneration != j.LeaseGeneration {
			// A newer lease owns the job; its retry reuses the same idempotency key.
			return nil
		}
		if sendErr == nil && accepted.NotificationID.ValidateV7() != nil {
			sendErr = errors.New("invalid notification acceptance reference")
		}
		if sendErr != nil {
			if h.Notification != nil && notification.Classify(sendErr) == notification.OutcomeDefinite {
				return h.finishGuestNotification(ctx, tx, cur, owner, "FAILED", "NOTIFICATION_REQUEST_REJECTED", "DEFINITE_ERROR", nil, nil, d.resolved)
			}
			next := now.Add(time.Duration(min(3600, 30<<min(cur.Attempts-1, 7))) * time.Second)
			switch {
			case cur.Attempts >= cur.MaxAttempts:
				return h.finishGuestNotification(ctx, tx, cur, owner, "FAILED", "RETRY_LIMIT_REACHED", "RETRYABLE_ERROR", nil, nil, d.resolved)
			case !next.Before(cur.ExpiresAt):
				return h.finishGuestNotification(ctx, tx, cur, owner, "EXPIRED", "NOT_ACCEPTED_BEFORE_EXPIRY", "EXPIRED", nil, nil, d.resolved)
			}
			reason := "NOTIFICATION_SERVICE_UNAVAILABLE"
			if h.Notification == nil {
				reason = "NOTIFICATION_CLIENT_UNAVAILABLE"
			}
			if _, e = tx.Exec(ctx, `UPDATE guest_notification_jobs SET state='RETRY',available_at=$3,last_error=$4,lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE id=$1 AND lease_owner=$2`, cur.ID, owner, next, reason); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `INSERT INTO guest_notification_attempts(id,organisation_id,job_id,attempt,outcome,detail) VALUES($1,$2,$3,$4,'RETRYABLE_ERROR',$5)`, ids.New(), cur.Organisation, cur.ID, cur.Attempts, reason)
			return e
		}
		status := "ACCEPTED_BY_NOTIFICATION_SERVICE"
		for _, s := range accepted.Suppressed {
			if s.Channel == d.channel {
				status = "PROVIDER_SUPPRESSED"
			}
		}
		suppressed := marshalCommercial(accepted.Suppressed)
		return h.finishGuestNotification(ctx, tx, cur, owner, "ACCEPTED", status, "ACCEPTED", &accepted.NotificationID, suppressed, d.resolved)
	})
}

func (h *Handler) finishGuestNotification(ctx context.Context, tx pgx.Tx, j CRMNotificationJob, owner, state, reason, outcome string, providerID *ids.ID, suppressed json.RawMessage, resolved *ids.ID) error {
	var version int64
	var e error
	if state == "ACCEPTED" {
		e = tx.QueryRow(ctx, `UPDATE guest_notification_jobs SET state='ACCEPTED',provider_notification_id=$3,provider_suppressed=$4,delivery_status=$5,accepted_at=now(),outcome_reason=$5,last_error=NULL,resolved_guest_id=coalesce($6,resolved_guest_id),lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE id=$1 AND lease_owner=$2 RETURNING version`, j.ID, owner, *providerID, suppressed, reason, resolved).Scan(&version)
	} else {
		e = tx.QueryRow(ctx, `UPDATE guest_notification_jobs SET state=$3,outcome_reason=$4,resolved_guest_id=coalesce($5,resolved_guest_id),lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE id=$1 AND lease_owner=$2 RETURNING version`, j.ID, owner, state, reason, resolved).Scan(&version)
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO guest_notification_attempts(id,organisation_id,job_id,attempt,outcome,detail,provider_notification_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, ids.New(), j.Organisation, j.ID, j.Attempts, outcome, reason, providerID); e != nil {
		return e
	}
	t := tenant{Organisation: j.Organisation}
	return h.commercialCommit(ctx, tx, t, j.PropertyID, j.ID, "guest_notification_job", "GUEST_NOTIFICATION_"+state, "guest.notification.updated.v1", version, map[string]any{"kind": j.Kind, "source_kind": j.SourceKind, "source_id": j.SourceID, "state": state, "reason": reason, "provider_notification_id": providerID, "delivery_confirmed": false})
}

// blocked records a terminal or operator-review outcome; nothing is sent.
func blocked(state, reason string) notificationDecision {
	return notificationDecision{state: state, reason: reason, attemptOutcome: state}
}

// decideGuestNotification rechecks the authoritative source, template,
// recipient, consent, suppression and destination at dispatch time.
func (h *Handler) decideGuestNotification(ctx context.Context, tx pgx.Tx, j CRMNotificationJob) (notificationDecision, error) {
	now := h.Auth.Service.Now().UTC()
	org := j.Organisation
	if !now.Before(j.ExpiresAt) {
		return blocked("EXPIRED", "EXPIRED_BEFORE_ACCEPTANCE"), nil
	}
	var vars map[string]any
	if e := json.Unmarshal(j.Variables, &vars); e != nil {
		return notificationDecision{}, e
	}
	var current bool
	var e error
	switch j.Kind {
	case "WAITLIST_OFFER":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM waitlist_offers WHERE organisation_id=$1 AND id=$2 AND state='OFFERED' AND expires_at>$3)`, org, j.SourceID, now).Scan(&current)
	case "ARRIVAL_NOTICE":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservations WHERE organisation_id=$1 AND id=$2 AND state='CONFIRMED' AND date_from::text=$3)`, org, j.SourceID, fmt.Sprint(vars["date_from"])).Scan(&current)
	case "CAMPAIGN_MESSAGE":
		// The campaign must still be running and the member still eligible.
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM campaign_members m JOIN campaigns c ON c.organisation_id=m.organisation_id AND c.id=m.campaign_id WHERE m.organisation_id=$1 AND m.id=$2 AND m.eligibility='ELIGIBLE' AND c.state='RUNNING')`, org, j.SourceID).Scan(&current)
	case "STAY_NOTICE":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stay_records WHERE organisation_id=$1 AND id=$2 AND state='ACTIVE')`, org, j.SourceID).Scan(&current)
	case "RESERVATION_CHANGE", "CORPORATE_BOOKING_UPDATE":
		var superseded bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_notification_jobs x WHERE x.organisation_id=$1 AND x.kind=$2 AND x.id<>$3 AND x.guest_id IS NOT DISTINCT FROM $4 AND x.corporate_id IS NOT DISTINCT FROM $5 AND x.variables->>'reservation_id'=$6 AND (x.variables->>'reservation_version')::bigint>$7::bigint AND x.state NOT IN ('CANCELLED'))`, org, j.Kind, j.ID, j.GuestID, j.CorporateID, fmt.Sprint(vars["reservation_id"]), fmt.Sprint(vars["reservation_version"])).Scan(&superseded)
		current = !superseded
	default:
		current = true
	}
	if e != nil {
		return notificationDecision{}, e
	}
	if !current {
		reason := "SOURCE_NO_LONGER_CURRENT"
		if j.Kind == "RESERVATION_CHANGE" || j.Kind == "CORPORATE_BOOKING_UPDATE" {
			reason = "SUPERSEDED_BY_NEWER_TRANSITION"
		}
		return blocked("CANCELLED", reason), nil
	}
	tpl, e := typedJSON[CRMNotificationTemplate](ctx, tx, `SELECT to_jsonb(x) FROM guest_notification_templates x WHERE organisation_id=$1 AND property_id=$2 AND kind=$3 AND state='ACTIVE'`, org, j.PropertyID, j.Kind)
	if isNotFound(e) {
		return blocked("BLOCKED", "TEMPLATE_NOT_APPROVED"), nil
	}
	if e != nil {
		return notificationDecision{}, e
	}
	if tpl.Purpose != j.Purpose {
		return blocked("BLOCKED", "TEMPLATE_PURPOSE_CHANGED"), nil
	}
	variables := map[string]string{"kind": j.Kind, "property_id": j.PropertyID.String()}
	for k, v := range vars {
		if v != nil {
			variables[k] = fmt.Sprint(v)
		}
	}
	d := notificationDecision{templateID: tpl.ID, templateVersion: tpl.Version}
	var recipient ids.ID
	var dest notification.Destinations
	if j.RecipientKind == "CORPORATE" {
		var state string
		var cipher []byte
		var key string
		if e = tx.QueryRow(ctx, `SELECT state,encrypted_contacts,contact_key_id FROM corporate_customer_profiles WHERE organisation_id=$1 AND id=$2`, org, *j.CorporateID).Scan(&state, &cipher, &key); e != nil {
			return d, e
		}
		if state != "ACTIVE" {
			return blocked("SUPPRESSED", "CORPORATE_NOT_ACTIVE"), nil
		}
		if h.Auth.Service.Sealer == nil {
			return d, errors.New("protected corporate storage unavailable")
		}
		plain, e := h.Auth.Service.Sealer.Open(key, cipher, corporateAAD(org, *j.CorporateID))
		if e != nil {
			return d, e
		}
		var contacts []crm.CorporateContact
		if e = json.Unmarshal(plain, &contacts); e != nil {
			return d, e
		}
		for _, c := range contacts {
			if dest.Email == "" && c.Email != "" {
				dest.Email = c.Email
			}
			if dest.Phone == "" && c.Phone != "" {
				dest.Phone = c.Phone
			}
		}
		recipient = *j.CorporateID
		d.channel = pickChannel(tpl.Channels, "", dest)
	} else {
		canonical, e := guestCanonical(ctx, tx, org, *j.GuestID)
		if e != nil {
			return d, e
		}
		d.resolved = &canonical
		g, e := crm.GetGuest(ctx, tx, org, canonical)
		if e != nil {
			return d, e
		}
		if g.State != "ACTIVE" {
			out := blocked("SUPPRESSED", "GUEST_NOT_CONTACTABLE_"+g.State)
			out.resolved = &canonical
			return out, nil
		}
		if e = crm.RequireGuestScope(ctx, tx, org, j.PropertyID, canonical); e != nil {
			out := blocked("SUPPRESSED", "GUEST_SCOPE_REVOKED")
			out.resolved = &canonical
			return out, nil
		}
		var suppression *ids.ID
		if e = tx.QueryRow(ctx, `SELECT id FROM guest_notification_suppressions WHERE organisation_id=$1 AND property_id=$2 AND guest_id IN ($3,$4) AND state='ACTIVE' AND (scope='ALL' OR scope='MARKETING' AND $5='MARKETING' OR scope='KIND' AND kind=$6) ORDER BY created_at LIMIT 1`, org, j.PropertyID, canonical, *j.GuestID, j.Purpose, j.Kind).Scan(&suppression); e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return d, e
		}
		if suppression != nil {
			out := blocked("SUPPRESSED", "GUEST_SUPPRESSION_ACTIVE")
			out.resolved = &canonical
			return out, nil
		}
		for _, purpose := range j.ConsentPurposes {
			ok, e := crm.ConsentAllowed(ctx, tx, org, canonical, j.PropertyID, purpose, now)
			if e != nil {
				return d, e
			}
			if !ok {
				out := blocked("SUPPRESSED", "CONSENT_REQUIRED_"+purpose)
				out.resolved = &canonical
				return out, nil
			}
		}
		var cipher []byte
		var key string
		e = tx.QueryRow(ctx, `SELECT encrypted_preferences,preference_key_id FROM guest_preferences WHERE organisation_id=$1 AND property_id=$2 AND guest_id=$3`, org, j.PropertyID, canonical).Scan(&cipher, &key)
		if errors.Is(e, pgx.ErrNoRows) {
			out := blocked("BLOCKED", "NO_AUTHORIZED_DESTINATION")
			out.resolved = &canonical
			return out, nil
		}
		if e != nil {
			return d, e
		}
		if h.Auth.Service.Sealer == nil {
			return d, errors.New("protected guest storage unavailable")
		}
		plain, e := h.Auth.Service.Sealer.Open(key, cipher, crm.PreferencesAAD(org, canonical, j.PropertyID))
		if e != nil {
			return d, e
		}
		var private crm.PrivateProfile
		if e = json.Unmarshal(plain, &private); e != nil {
			return d, e
		}
		if private.ContactPreference == "NONE" {
			out := blocked("SUPPRESSED", "GUEST_CONTACT_PREFERENCE_NONE")
			out.resolved = &canonical
			return out, nil
		}
		dest = notification.Destinations{Email: private.Email, Phone: private.Phone}
		recipient = canonical
		d.channel = pickChannel(tpl.Channels, private.ContactPreference, dest)
	}
	if d.channel == "" {
		out := blocked("BLOCKED", "NO_AUTHORIZED_DESTINATION")
		out.resolved = d.resolved
		return out, nil
	}
	// Only the chosen channel's destination leaves protected storage.
	send := notification.Destinations{}
	if d.channel == notification.ChannelEmail {
		send.Email = dest.Email
	} else {
		send.Phone = dest.Phone
	}
	d.request = &notification.SendRequest{Recipient: notification.Recipient{Domain: string(platform.DomainBusinessOperations), ID: recipient}, TemplateKey: tpl.ProviderTemplateKey, Channels: []string{d.channel}, Variables: variables, Destinations: send, CallerReference: "business-guest-notification:" + j.ID.String()}
	return d, nil
}

// pickChannel honours the guest preference, then the approved template order.
// PUSH needs a device registration this domain does not own, so it is unused.
func pickChannel(channels []string, preference string, dest notification.Destinations) string {
	order := []string{}
	if preference == notification.ChannelEmail || preference == notification.ChannelSMS {
		order = append(order, preference)
	}
	order = append(order, channels...)
	for _, c := range order {
		if !slices.Contains(channels, c) {
			continue
		}
		if c == notification.ChannelEmail && dest.Email != "" || c == notification.ChannelSMS && dest.Phone != "" {
			return c
		}
	}
	return ""
}
