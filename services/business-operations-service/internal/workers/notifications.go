// Package workers composes business lifecycle and authenticated event handlers.
package workers

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
)

const NotificationConsumer = "business-operations-notifications-v1"

type Notifications struct {
	Pool   *pgxpool.Pool
	Sealer *credentials.Sealer
	Sender notification.Sender
}

func (w *Notifications) Handle(ctx context.Context, delivery events.Delivery) error {
	if w.Pool == nil || w.Sender == nil || w.Sealer == nil {
		return errors.New("notification worker dependencies unavailable")
	}
	event := delivery.Envelope
	request, err := businessEvents.OpenNotification(w.Sealer, event)
	if err != nil {
		return events.Permanent(errors.New("invalid encrypted notification job"))
	}
	var processed bool
	if err := w.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM processed_events WHERE consumer=$1 AND event_id=$2)`, NotificationConsumer, event.EventID).Scan(&processed); err != nil {
		return err
	}
	if processed {
		return nil
	}
	if strings.HasPrefix(request.CallerReference, "business-mfa:") {
		id, err := ids.ParseV7(strings.TrimPrefix(request.CallerReference, "business-mfa:"))
		if err != nil {
			return events.Permanent(errors.New("invalid MFA notification reference"))
		}
		var active bool
		err = w.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_mfa_one_time_codes c JOIN business_mfa_methods m ON m.id=c.method_id AND m.account_id=c.account_id JOIN business_accounts a ON a.id=c.account_id WHERE c.id=$1 AND c.account_id=$2 AND a.status='ACTIVE' AND c.consumed_at IS NULL AND c.expires_at>now() AND c.attempts<5 AND ((c.purpose='ENROL' AND m.status='PENDING' AND m.expires_at>now()) OR (c.purpose='LOGIN' AND m.status='ACTIVE' AND EXISTS(SELECT 1 FROM business_mfa_challenges l WHERE l.id=c.binding_id AND l.account_id=c.account_id AND l.consumed_at IS NULL AND l.attempts<5 AND l.expires_at>now())) OR (c.purpose='STEP_UP' AND m.status='ACTIVE' AND EXISTS(SELECT 1 FROM business_sessions s WHERE s.id=c.binding_id AND s.account_id=c.account_id AND s.revoked_at IS NULL AND s.expires_at>now()))))`, id, request.Recipient.ID).Scan(&active)
		if err != nil {
			return err
		}
		if !active {
			return database.WithTx(ctx, w.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				fresh, err := events.MarkProcessed(ctx, tx, NotificationConsumer, event)
				if err != nil || !fresh {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO security_events(id,account_id,event_type,target_id,context) VALUES($1,$2,'MFA_NOTIFICATION_SUPPRESSED',$3,'{"reason":"CHALLENGE_NO_LONGER_ACTIVE"}')`, ids.New(), request.Recipient.ID, id)
				return err
			})
		}
	}
	if strings.HasPrefix(request.CallerReference, "business-invitation:") {
		id, err := ids.ParseV7(strings.TrimPrefix(request.CallerReference, "business-invitation:"))
		if err != nil {
			return events.Permanent(errors.New("invalid invitation notification reference"))
		}
		var pending bool
		if err := w.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staff_invitations i JOIN organisations o ON o.id=i.organisation_id JOIN role_templates r ON r.id=i.role_id AND r.organisation_id=i.organisation_id JOIN staff_memberships m ON m.id=i.membership_id AND m.organisation_id=i.organisation_id JOIN business_accounts a ON a.id=i.account_id WHERE i.id=$1 AND i.account_id=$2 AND i.status='PENDING' AND i.expires_at>now() AND i.authorization_version=o.authorization_version AND o.state IN ('DRAFT','KYB_PENDING','ACTIVE') AND r.active AND m.status='INVITED' AND a.status IN ('INVITED','ACTIVE') AND EXISTS(SELECT 1 FROM staff_memberships issuer JOIN role_templates ir ON ir.id=issuer.role_id AND ir.organisation_id=issuer.organisation_id JOIN business_accounts ia ON ia.id=issuer.account_id WHERE issuer.organisation_id=i.organisation_id AND issuer.account_id=i.invited_by AND issuer.status='ACTIVE' AND ia.status='ACTIVE' AND ir.active AND issuer.effective_at<=now() AND (issuer.expires_at IS NULL OR issuer.expires_at>now()) AND EXISTS(SELECT 1 FROM role_permissions p WHERE p.role_id=issuer.role_id AND p.permission='staff:invite')))`, id, request.Recipient.ID).Scan(&pending); err != nil {
			return err
		}
		if !pending {
			return database.WithTx(ctx, w.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				fresh, err := events.MarkProcessed(ctx, tx, NotificationConsumer, event)
				if err != nil || !fresh {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO security_events(id,account_id,event_type,target_id,context) VALUES($1,$2,'INVITATION_NOTIFICATION_SUPPRESSED',$3,'{"reason":"INVITATION_NO_LONGER_PENDING"}')`, ids.New(), request.Recipient.ID, id)
				return err
			})
		}
	}
	// A broker envelope is not a signed user delegation. Delivery is performed
	// as the authenticated Business Operations service, without asserting actor.
	ctx = requestcontext.WithActor(ctx, requestcontext.Actor{})
	accepted, err := w.Sender.Send(ctx, request, "business-notification:"+event.AggregateID.String())
	if err != nil {
		if notification.Classify(err) == notification.OutcomeDefinite {
			return events.Permanent(errors.New("notification request rejected; operator review required"))
		}
		return errors.New("notification acceptance unavailable")
	}
	if accepted.NotificationID.ValidateV7() != nil {
		return errors.New("invalid notification acceptance reference")
	}
	return database.WithTx(ctx, w.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		fresh, err := events.MarkProcessed(ctx, tx, NotificationConsumer, event)
		if err != nil {
			return err
		}
		if !fresh {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO notification_acceptances(job_id,event_id,notification_id) VALUES($1,$2,$3) ON CONFLICT(job_id) DO NOTHING`, event.AggregateID, event.EventID, accepted.NotificationID)
		return err
	})
}
func (w *Notifications) Quarantine(ctx context.Context, f jetstream.Failure) error {
	_, err := w.Pool.Exec(ctx, `INSERT INTO event_failures(id,consumer,stream,sequence,event_id,digest,failure_class,attempt) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(consumer,stream,sequence) DO NOTHING`, ids.New(), f.Consumer, f.Stream, f.Sequence, f.EventID, f.Digest, f.Class, f.Attempt)
	return err
}

// RequeueUnaccepted retains the canonical outbox as the recovery source even
// after broker retention. Already acknowledged provider calls are idempotent.
// Quarantined jobs require an explicit operator review instead of hot retries.
func (w *Notifications) RequeueUnaccepted(ctx context.Context) error {
	_, err := w.Pool.Exec(ctx, `UPDATE outbox_events o SET published_at=NULL,next_attempt_at=now(),lease_owner=NULL,lease_expires_at=NULL WHERE o.event_type=$1 AND o.dead_at IS NULL AND o.published_at<now()-interval '30 minutes' AND NOT EXISTS(SELECT 1 FROM processed_events p WHERE p.consumer=$2 AND p.event_id=o.event_id) AND NOT EXISTS(SELECT 1 FROM event_failures f WHERE f.event_id=o.event_id::text)`, businessEvents.NotificationRequested, NotificationConsumer)
	return err
}
