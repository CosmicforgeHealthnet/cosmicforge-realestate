package usecases

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/channels"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Attempt outcomes recorded in delivery_attempts.
const (
	OutcomeAccepted    = "ACCEPTED"
	OutcomeTransient   = "TRANSIENT_ERROR"
	OutcomePermanent   = "PERMANENT_ERROR"
	OutcomeInvalidDest = "INVALID_DESTINATION"
	OutcomeUnknown     = "UNKNOWN"
)

// Error codes recorded on deliveries.
const (
	ErrExpired          = "EXPIRED"
	ErrEndpointInactive = "PUSH_ENDPOINT_INACTIVE"
	ErrLeaseExpired     = "OUTCOME_UNKNOWN_LEASE_EXPIRED"
	ErrNoSender         = "CHANNEL_NOT_CONFIGURED"
)

// DispatchDue claims due deliveries (SKIP LOCKED + lease) and sends them.
// Delivery is at-least-once: a worker that dies after the provider accepted
// a message but before recording it leaves an UNKNOWN attempt that is retried,
// so a recipient may (rarely) receive a duplicate. Exactly-once is never
// claimed.
func (s *Service) DispatchDue(ctx context.Context, owner string, batch int) (int, error) {
	ctx = requestcontext.Job(ctx, "notification-dispatch")
	now := s.now()
	claimed, err := s.q.ClaimDueDeliveries(ctx, s.db, db.ClaimDueDeliveriesParams{LeaseOwner: &owner,
		LeaseExpiresAt: now.Add(s.opts.DispatchLease), Now: now, RowLimit: int32(batch)})
	if err != nil {
		return 0, err
	}
	for _, d := range claimed {
		if err := s.dispatchOne(ctx, owner, d); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "dispatch failed", "delivery_id", d.DeliveryID.String(), "error", err.Error())
		}
	}
	return len(claimed), nil
}

// outcome is the result of one provider attempt.
type outcome struct {
	attemptOutcome string
	result         channels.Result
	err            *channels.Error
	duration       time.Duration
}

type prepared struct {
	delivery db.NotificationDelivery
	request  db.NotificationRequest
	message  channels.Message
	sender   channels.Sender
	send     bool
}

func (s *Service) dispatchOne(ctx context.Context, owner string, d db.NotificationDelivery) error {
	var p prepared
	err := database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		p, err = s.begin(ctx, tx, owner, d.DeliveryID)
		return err
	})
	if err != nil || !p.send {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, s.opts.SendTimeout)
	start := time.Now()
	res, sendErr := p.sender.Send(sendCtx, p.message)
	cancel()
	o := outcome{result: res, duration: time.Since(start), attemptOutcome: OutcomeAccepted}
	if sendErr != nil {
		o.err = channels.Classify(sendErr)
		switch o.err.Kind {
		case channels.Permanent:
			o.attemptOutcome = OutcomePermanent
		case channels.InvalidDestination:
			o.attemptOutcome = OutcomeInvalidDest
		default:
			o.attemptOutcome = OutcomeTransient
		}
	}
	// Record the outcome even if the worker is shutting down.
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer fcancel()
	return database.WithTx(fctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		return s.finish(ctx, tx, owner, p, o)
	})
}

// begin moves a claimed delivery into DISPATCHING (attempt n), resolving
// lease-expired in-flight attempts and expired/unsendable deliveries first.
func (s *Service) begin(ctx context.Context, tx pgx.Tx, owner string, id ids.ID) (prepared, error) {
	d, err := s.q.GetDeliveryForUpdate(ctx, tx, id)
	if err != nil {
		return prepared{}, err
	}
	if d.LeaseOwner == nil || *d.LeaseOwner != owner {
		return prepared{}, nil // claimed by another worker meanwhile
	}
	req, err := s.q.GetRequest(ctx, tx, d.NotificationID)
	if err != nil {
		return prepared{}, err
	}
	now := s.now()
	if d.Status == string(domain.StatusDispatching) {
		// A previous worker's lease expired mid-call: the provider outcome is
		// unknown. Record that honestly, then retry (bounded) or fail.
		if err := s.recordAttempt(ctx, tx, d, outcome{attemptOutcome: OutcomeUnknown}, ErrLeaseExpired); err != nil {
			return prepared{}, err
		}
		if d, err = s.retryOrFail(ctx, tx, req, d, ErrLeaseExpired, 0); err != nil || d.Status == string(domain.StatusFailedFinal) {
			return prepared{}, err
		}
		if d.NextAttemptAt.After(now) {
			return prepared{}, nil // backoff: picked up again when due
		}
	}
	if now.After(d.ExpiresAt) {
		return prepared{}, s.failWithoutAttempt(ctx, tx, req, d, ErrExpired)
	}
	sender, ok := s.senders[domain.Channel(d.Channel)]
	if !ok {
		return prepared{}, s.failWithoutAttempt(ctx, tx, req, d, ErrNoSender)
	}
	msg := channels.Message{DeliveryID: d.DeliveryID, Destination: d.Destination, Subject: deref(d.Subject), Body: d.Body, HTML: deref(d.HtmlBody)}
	if len(d.Data) > 0 {
		_ = json.Unmarshal(d.Data, &msg.Data)
	}
	if msg.Data == nil && d.Channel == string(domain.ChannelPush) {
		msg.Data = map[string]string{}
	}
	if d.Channel == string(domain.ChannelPush) {
		ep, err := s.q.GetPushEndpoint(ctx, tx, *d.PushEndpointID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return prepared{}, err
		}
		if err != nil || ep.Status != "ACTIVE" || ep.RecipientDomain != req.RecipientDomain || ep.RecipientID != req.RecipientID {
			return prepared{}, s.failWithoutAttempt(ctx, tx, req, d, ErrEndpointInactive)
		}
		msg.Destination = ep.Token
		msg.Data["notification_id"] = req.NotificationID.String()
		msg.Data["template_key"] = req.TemplateKey
	}
	lease := now.Add(s.opts.DispatchLease)
	d, err = s.update(ctx, tx, d, domain.StatusDispatching, updateFields{attempts: d.Attempts + 1, next: d.NextAttemptAt,
		leaseOwner: &owner, leaseUntil: &lease, errorCode: d.LastErrorCode})
	if err != nil {
		return prepared{}, err
	}
	return prepared{delivery: d, request: req, message: msg, sender: sender, send: true}, nil
}

// finish records the provider outcome for attempt n (if this worker still
// owns it) and moves the delivery to SENT, RETRY_SCHEDULED or FAILED_FINAL.
func (s *Service) finish(ctx context.Context, tx pgx.Tx, owner string, p prepared, o outcome) error {
	d, err := s.q.GetDeliveryForUpdate(ctx, tx, p.delivery.DeliveryID)
	if err != nil {
		return err
	}
	code := ""
	if o.err != nil {
		code = o.err.Code
	}
	if d.Status != string(domain.StatusDispatching) || d.Attempts != p.delivery.Attempts || d.LeaseOwner == nil || *d.LeaseOwner != owner {
		// Lease lost: another worker already recorded attempt n as UNKNOWN.
		// Keep what the provider told us for diagnosis; do not change state.
		s.log.WarnContext(ctx, "dispatch outcome arrived after lease expiry", "delivery_id", d.DeliveryID.String(), "outcome", o.attemptOutcome)
		return s.recordAttempt(ctx, tx, p.delivery, o, code)
	}
	if err := s.recordAttempt(ctx, tx, d, o, code); err != nil {
		return err
	}
	if o.err == nil {
		now := s.now()
		d, err = s.update(ctx, tx, d, domain.StatusSent, updateFields{attempts: d.Attempts, next: d.NextAttemptAt,
			providerMessageID: strPtr(o.result.ProviderMessageID), sentAt: &now})
		if err != nil {
			return err
		}
		return s.terminal(ctx, tx, p.request, d, EventSent)
	}
	switch o.err.Kind {
	case channels.InvalidDestination:
		if d.PushEndpointID != nil {
			if err := s.invalidateEndpoint(ctx, tx, *d.PushEndpointID, o.err.Code); err != nil {
				return err
			}
		}
		return s.fail(ctx, tx, p.request, d, code)
	case channels.Permanent:
		return s.fail(ctx, tx, p.request, d, code)
	default:
		_, err := s.retryOrFail(ctx, tx, p.request, d, code, o.err.RetryAfter)
		return err
	}
}

func (s *Service) backoff(attempt int32, retryAfter time.Duration) time.Duration {
	b := s.opts.RetryBase
	for i := int32(1); i < attempt && b < s.opts.RetryMax; i++ {
		b *= 2
	}
	b = min(b, s.opts.RetryMax)
	if retryAfter > b {
		b = min(retryAfter, s.opts.RetryMax)
	}
	return b + s.opts.Jitter(b)
}

// retryOrFail schedules the next attempt within the attempt budget and the
// notification TTL, else fails finally (DISPATCHING -> RETRY_SCHEDULED|FAILED_FINAL).
func (s *Service) retryOrFail(ctx context.Context, tx pgx.Tx, r db.NotificationRequest, d db.NotificationDelivery, code string, retryAfter time.Duration) (db.NotificationDelivery, error) {
	next := s.now().Add(s.backoff(d.Attempts, retryAfter))
	if d.Attempts >= d.MaxAttempts || next.After(d.ExpiresAt) {
		return d, s.fail(ctx, tx, r, d, code)
	}
	return s.update(ctx, tx, d, domain.StatusRetryScheduled, updateFields{attempts: d.Attempts, next: next, errorCode: strPtr(code)})
}

func (s *Service) fail(ctx context.Context, tx pgx.Tx, r db.NotificationRequest, d db.NotificationDelivery, code string) error {
	now := s.now()
	d, err := s.update(ctx, tx, d, domain.StatusFailedFinal, updateFields{attempts: d.Attempts, next: d.NextAttemptAt, errorCode: strPtr(code), failedAt: &now})
	if err != nil {
		return err
	}
	return s.terminal(ctx, tx, r, d, EventFailed)
}

// failWithoutAttempt finalizes a QUEUED/RETRY_SCHEDULED delivery that must
// not be sent (expired, endpoint gone) via DISPATCHING without a provider call.
func (s *Service) failWithoutAttempt(ctx context.Context, tx pgx.Tx, r db.NotificationRequest, d db.NotificationDelivery, code string) error {
	d, err := s.update(ctx, tx, d, domain.StatusDispatching, updateFields{attempts: d.Attempts, next: d.NextAttemptAt, errorCode: strPtr(code)})
	if err != nil {
		return err
	}
	return s.fail(ctx, tx, r, d, code)
}

func (s *Service) terminal(ctx context.Context, tx pgx.Tx, r db.NotificationRequest, d db.NotificationDelivery, eventType string) error {
	if r.Critical {
		// Security content (codes, alerts) is not kept once handled.
		if err := s.q.ClearDeliveryContent(ctx, tx, d.DeliveryID); err != nil {
			return err
		}
	}
	if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "notification.delivery_" + map[string]string{EventSent: "sent", EventFailed: "failed"}[eventType],
		ActorType: audit.ActorSystem, TargetType: "notification_delivery", TargetID: d.DeliveryID,
		Changes: map[string]any{"to": d.Status}, Metadata: map[string]any{"notification_id": r.NotificationID.String(), "channel": d.Channel,
			"provider": d.Provider, "attempts": d.Attempts, "error_code": deref(d.LastErrorCode)}}); err != nil {
		return err
	}
	return s.emit(ctx, tx, r, d, eventType)
}

type updateFields struct {
	attempts          int32
	next              time.Time
	leaseOwner        *string
	leaseUntil        *time.Time
	providerMessageID *string
	errorCode         *string
	sentAt, failedAt  *time.Time
}

func (s *Service) update(ctx context.Context, tx pgx.Tx, d db.NotificationDelivery, to domain.Status, f updateFields) (db.NotificationDelivery, error) {
	if err := domain.Machine.Transition(domain.Status(d.Status), to); err != nil {
		return d, err
	}
	return s.q.UpdateDelivery(ctx, tx, db.UpdateDeliveryParams{Status: string(to), Attempts: f.attempts, NextAttemptAt: f.next,
		LeaseOwner: f.leaseOwner, LeaseExpiresAt: f.leaseUntil, ProviderMessageID: f.providerMessageID, LastErrorCode: f.errorCode,
		SentAt: f.sentAt, FailedAt: f.failedAt, UpdatedAt: s.now(), DeliveryID: d.DeliveryID, ExpectedStatus: d.Status})
}

func (s *Service) recordAttempt(ctx context.Context, tx pgx.Tx, d db.NotificationDelivery, o outcome, code string) error {
	if d.Attempts <= 0 {
		return nil
	}
	now := s.now()
	a, err := s.q.InsertAttempt(ctx, tx, db.InsertAttemptParams{AttemptID: ids.New(), DeliveryID: d.DeliveryID, AttemptNo: d.Attempts,
		Provider: d.Provider, Outcome: o.attemptOutcome, ErrorCode: strPtr(code), ProviderMessageID: strPtr(o.result.ProviderMessageID),
		DurationMs: int32(o.duration.Milliseconds()), CreatedAt: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // attempt n already recorded (lease recovery won the race)
	}
	if err != nil || o.attemptOutcome == OutcomeUnknown {
		return err
	}
	rp := db.InsertProviderResponseParams{ResponseID: ids.New(), AttemptID: a.AttemptID, Provider: d.Provider, CreatedAt: now}
	if o.err != nil {
		hs, ra := int32(o.err.HTTPStatus), int32(o.err.RetryAfter/time.Second)
		rp.HttpStatus, rp.ProviderCode = &hs, strPtr(o.err.Code)
		if ra > 0 {
			rp.RetryAfterSeconds = &ra
		}
	} else if o.result.HTTPStatus != 0 {
		hs := int32(o.result.HTTPStatus)
		rp.HttpStatus, rp.ProviderCode = &hs, strPtr(o.result.ProviderCode)
	}
	return s.q.InsertProviderResponse(ctx, tx, rp)
}

// PurgeContent clears rendered content of finished deliveries after the
// retention window (delivery metadata and attempts are kept).
func (s *Service) PurgeContent(ctx context.Context) (int64, error) {
	return s.q.PurgeDeliveredContent(ctx, s.db, s.now().Add(-s.opts.ContentRetention))
}

// Stats summarizes recent delivery states (operational metrics).
func (s *Service) Stats(ctx context.Context, since time.Duration) ([]db.DeliveryStatsRow, error) {
	return s.q.DeliveryStats(ctx, s.db, s.now().Add(-since))
}
