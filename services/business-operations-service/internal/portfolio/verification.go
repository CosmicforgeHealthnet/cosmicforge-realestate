package portfolio

import (
	"context"
	"errors"
	"time"

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
)

type VerificationProjector struct {
	Pool      *pgxpool.Pool
	Outbox    *outbox.Store
	Now       func() time.Time
	Authorize func(context.Context, pgx.Tx) error
}

func (p VerificationProjector) Apply(ctx context.Context, org, node, actor ids.ID, version int64, c verification.Case, event *events.Envelope) error {
	if c.VerificationID.ValidateV7() != nil || c.Type != verification.TypePropertyAuthority || c.Subject.Type != verification.SubjectProperty || c.Subject.Domain != string(platform.DomainBusinessOperations) || c.Subject.ID != node || c.UpdatedAt.IsZero() {
		return apperrors.PermissionDenied("Authoritative case does not verify this canonical property.")
	}
	switch c.Status {
	case "DRAFT", "CREATED", "SUBMITTED", "PROCESSING", "MANUAL_REVIEW_REQUIRED", "VERIFIED", "REJECTED", "EXPIRED", "RESTRICTED":
	default:
		return apperrors.Unavailable("Unsupported property verification outcome.")
	}
	return database.WithTx(ctx, p.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if !actor.IsZero() {
			if p.Authorize == nil {
				return apperrors.PermissionDenied("Current projection authorization required.")
			}
			if err := p.Authorize(ctx, tx); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); err != nil {
			return err
		}
		n, err := Get(ctx, tx, org, node)
		if err != nil {
			return err
		}
		if n.Kind != Property && n.Kind != Parcel {
			return apperrors.InvalidRequest("Authority cases attach to canonical property or parcel identities.")
		}
		if n.State == "ARCHIVED" || n.State == "RETIRED" {
			return apperrors.Conflict("INVENTORY_RETIRED", "Retired property cannot gain verification authority.")
		}
		var oldCase ids.ID
		var oldVersion int64
		var oldStatus string
		var updated time.Time
		var expiry *time.Time
		err = tx.QueryRow(ctx, `SELECT case_id,node_version,provider_status,provider_updated_at,expires_at FROM inventory_verification_links WHERE organisation_id=$1 AND node_id=$2 FOR UPDATE`, org, node).Scan(&oldCase, &oldVersion, &oldStatus, &updated, &expiry)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if event != nil {
			fresh, e := events.MarkProcessed(ctx, tx, "business-operations-verification-v1", *event)
			if e != nil || !fresh {
				return e
			}
		}
		if !oldCase.IsZero() && oldCase != c.VerificationID && actor.IsZero() {
			return nil
		}
		if oldCase == c.VerificationID && c.UpdatedAt.Before(updated) {
			return nil
		}
		if version > 0 && (n.Version != version || c.UpdatedAt.Before(n.UpdatedAt)) {
			return apperrors.Conflict("STALE_VERIFICATION", "The provider must review the current inventory version.")
		}
		boundVersion := version
		if boundVersion == 0 {
			boundVersion = oldVersion
		}
		if boundVersion < 1 {
			return apperrors.Conflict("VERIFICATION_LINK_REQUIRED", "Link a case before applying a projection.")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO inventory_verification_links(id,organisation_id,node_id,case_id,provider_status,node_version,provider_updated_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(organisation_id,node_id) DO UPDATE SET case_id=excluded.case_id,provider_status=excluded.provider_status,node_version=excluded.node_version,provider_updated_at=excluded.provider_updated_at,expires_at=excluded.expires_at,checked_at=now(),version=inventory_verification_links.version+1`, ids.New(), org, node, c.VerificationID, c.Status, boundVersion, c.UpdatedAt, c.ExpiresAt); err != nil {
			return err
		}
		if oldCase == c.VerificationID && oldStatus == c.Status && sameTime(expiry, c.ExpiresAt) {
			return nil
		}
		var actorRef any
		if !actor.IsZero() {
			actorRef = actor
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,organisation_id,action,target_type,target_id,correlation_id,change_context) VALUES($1,$2,$3,'PROPERTY_VERIFICATION_PROJECTED','inventory_node',$4,$5,jsonb_build_object('case_id',$6::uuid,'status',$7::text,'node_version',$8::bigint))`, ids.New(), actorRef, org, node, requestcontext.From(ctx).RequestID, c.VerificationID, c.Status, boundVersion); err != nil {
			return err
		}
		e, err := events.New(ctx, platform.BusinessOperationsService, "property.authority.changed.v1", "inventory_node", node, map[string]any{"organisation_id": org, "branch_id": n.BranchID, "target_id": node, "node_id": node, "verification_id": c.VerificationID, "status": c.Status, "node_version": boundVersion, "expires_at": c.ExpiresAt})
		if err != nil {
			return err
		}
		if err = p.Outbox.Add(ctx, tx, e); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE outbox_events SET subject=left(subject,length(subject)-length(event_type))||'business.'||event_type WHERE event_id=$1`, e.EventID)
		return err
	})
}
func sameTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}
