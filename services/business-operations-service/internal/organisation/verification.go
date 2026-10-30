package organisation

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Projection struct {
	Sealer *credentials.Sealer
	Pool   *pgxpool.Pool
	Outbox *outbox.Store
	Now    func() time.Time
}

func (p *Projection) Apply(ctx context.Context, org ids.ID, actor *ids.ID, c verification.Case) error {
	return p.apply(ctx, org, actor, c, nil)
}
func (p *Projection) ApplyEvent(ctx context.Context, org ids.ID, c verification.Case, event events.Envelope) error {
	return p.apply(ctx, org, nil, c, &event)
}
func (p *Projection) apply(ctx context.Context, org ids.ID, actor *ids.ID, c verification.Case, event *events.Envelope) error {
	if c.VerificationID.ValidateV7() != nil || c.Type != verification.TypeBusiness || c.Subject.Type != verification.SubjectBusiness || c.Subject.Domain != string(platform.DomainBusinessOperations) || c.Subject.ID != org {
		return apperrors.PermissionDenied("Verification case does not belong to this organisation.")
	}
	switch c.Status {
	case "CREATED", "DRAFT", "SUBMITTED", "PROCESSING", "MANUAL_REVIEW_REQUIRED", "VERIFIED", "REJECTED", "EXPIRED", "RESTRICTED":
	default:
		return apperrors.Unavailable("Unsupported authoritative verification outcome.")
	}
	return database.WithTx(ctx, p.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if event != nil {
			fresh, err := events.MarkProcessed(ctx, tx, "business-operations-verification-v1", *event)
			if err != nil || !fresh {
				return err
			}
		}
		var current State
		var source string
		var representative ids.ID
		if err := tx.QueryRow(ctx, `SELECT state,restriction_source,representative_account_id FROM organisations WHERE id=$1 FOR UPDATE`, org).Scan(&current, &source, &representative); err != nil {
			return err
		}
		if current == Closed || current == ClosurePending {
			return apperrors.Conflict("ORGANISATION_CLOSING", "Organisation is closing.")
		}
		var linkedID ids.ID
		var updated *time.Time
		var previousStatus string
		var previousExpiry *time.Time
		err := tx.QueryRow(ctx, `SELECT case_id,provider_updated_at,provider_status,expires_at FROM verification_links WHERE organisation_id=$1 AND purpose='KYB'`, org).Scan(&linkedID, &updated, &previousStatus, &previousExpiry)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && linkedID != c.VerificationID {
			return apperrors.Conflict("VERIFICATION_LINK_CONFLICT", "A different KYB case is already linked.")
		}
		if updated != nil && (c.UpdatedAt.Before(*updated) || c.UpdatedAt.Equal(*updated) && previousStatus == c.Status && sameExpiry(previousExpiry, c.ExpiresAt)) {
			return nil
		}
		linked, err := tx.Exec(ctx, `INSERT INTO verification_links(id,organisation_id,case_id,purpose,provider_status,expires_at,provider_updated_at) VALUES($1,$2,$3,'KYB',$4,$5,$6) ON CONFLICT(organisation_id,purpose) DO UPDATE SET provider_status=excluded.provider_status,expires_at=excluded.expires_at,provider_updated_at=excluded.provider_updated_at,updated_at=now() WHERE verification_links.case_id=excluded.case_id`, ids.New(), org, c.VerificationID, c.Status, c.ExpiresAt, c.UpdatedAt)
		if err != nil {
			return err
		}
		if linked.RowsAffected() != 1 {
			return apperrors.Conflict("VERIFICATION_LINK_CONFLICT", "A different KYB case is already linked.")
		}
		next := current
		now := p.Now().UTC()
		v := Verification{CaseID: c.VerificationID, Status: c.Status, ExpiresAt: c.ExpiresAt}
		if current == Draft {
			if err := Transition(current, KYBPending, v, now, false); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE organisations SET state='KYB_PENDING',version=version+1,updated_at=now() WHERE id=$1`, org); err != nil {
				return err
			}
			if err := p.audit(ctx, tx, org, actor, "ORGANISATION_KYB_PENDING", "organisation", org, map[string]any{"case_id": c.VerificationID}); err != nil {
				return err
			}
			current = KYBPending
			next = current
		}
		if c.Status == "VERIFIED" && v.Current(now) {
			next = Active
		} else if c.Status == "REJECTED" || c.Status == "EXPIRED" || c.Status == "RESTRICTED" || c.ExpiresAt != nil && !now.Before(*c.ExpiresAt) {
			next = Restricted
		}
		if current == Suspended || source == "ADMIN" {
			next = current
		}
		if next != current {
			if err := Transition(current, next, v, now, false); err != nil {
				return apperrors.Conflict("ORGANISATION_STATE_CONFLICT", "Invalid organisation lifecycle transition.")
			}
			if _, err := tx.Exec(ctx, `UPDATE organisations SET state=$2,restriction_source=CASE WHEN $2='RESTRICTED' THEN 'KYB' ELSE '' END,version=version+1,updated_at=now(),activated_at=CASE WHEN $2='ACTIVE' THEN coalesce(activated_at,now()) ELSE activated_at END WHERE id=$1`, org, string(next)); err != nil {
				return err
			}
			eventType := "business.restricted.v1"
			if next == Active {
				eventType = "business.verified.v1"
			} else if next == KYBPending {
				eventType = "business.organisation.verification_pending.v1"
			}
			event, err := events.New(ctx, platform.BusinessOperationsService, eventType, "organisation", org, map[string]any{"organisation_id": org, "verification_id": c.VerificationID, "state": next, "expires_at": c.ExpiresAt})
			if err != nil {
				return err
			}
			if err := p.Outbox.Add(ctx, tx, event); err != nil {
				return err
			}
		}
		if previousStatus != c.Status || !sameExpiry(previousExpiry, c.ExpiresAt) {
			var email string
			if err := tx.QueryRow(ctx, `SELECT email FROM business_accounts WHERE id=$1`, representative).Scan(&email); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO security_events(id,account_id,organisation_id,event_type,target_id,correlation_id,context) VALUES($1,$2,$3,'KYB_STATUS_UPDATED',$4,$5,jsonb_build_object('status',$6::text,'state',$7::text))`, ids.New(), representative, org, c.VerificationID, requestcontext.From(ctx).RequestID, c.Status, string(next)); err != nil {
				return err
			}
			job, err := businessEvents.NotificationRequest(ctx, p.Sealer, ids.New(), notification.SendRequest{Recipient: notification.Recipient{Domain: string(platform.DomainBusinessOperations), ID: representative}, TemplateKey: notification.TemplateSecurityAlert, Channels: []string{notification.ChannelEmail}, Variables: map[string]string{"event": "Business verification status changed to " + c.Status}, Destinations: notification.Destinations{Email: email}})
			if err != nil {
				return err
			}
			if err = p.Outbox.Add(ctx, tx, job); err != nil {
				return err
			}
		}
		return p.audit(ctx, tx, org, actor, "KYB_STATUS_UPDATED", "organisation", org, map[string]any{"case_id": c.VerificationID, "status": c.Status, "state": next})
	})
}

func (p *Projection) audit(ctx context.Context, tx pgx.Tx, org ids.ID, actor *ids.ID, action, kind string, id ids.ID, change any) error {
	raw, err := json.Marshal(change)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,organisation_id,action,target_type,target_id,correlation_id,change_context) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), actor, org, action, kind, id, requestcontext.From(ctx).RequestID, raw)
	return err
}

func sameExpiry(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}
