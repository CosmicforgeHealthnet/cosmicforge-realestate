package events

import (
	"context"
	"encoding/json"
	"strings"

	shared "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

const (
	InventoryChanged         = "inventory.changed.v1"
	RatePlanChanged          = "rate_plan.changed.v1"
	ListingPublished         = "listing.published.v1"
	ListingUpdated           = "listing.updated.v1"
	ListingWithdrawn         = "listing.withdrawn.v1"
	ChannelConnectionChanged = "channel.connection.changed.v1"
	ChannelSyncFailed        = "channel.sync.failed.v1"
)

// CommercialCommit belongs to the owning transaction. Snapshots must be typed
// public projections: credentials, secret material and signed URLs are forbidden.
func CommercialCommit(ctx context.Context, tx pgx.Tx, store *outbox.Store, org, property, target, actor ids.ID, kind, operation, eventType string, version int64, snapshot any) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	var actorRef any
	if !actor.IsZero() {
		actorRef = actor
	}
	if _, err = tx.Exec(ctx, `INSERT INTO commercial_history(id,organisation_id,property_id,entity_type,entity_id,operation,version,actor_id,correlation_id,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, ids.New(), org, property, kind, target, operation, version, actorRef, requestcontext.From(ctx).RequestID, raw); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,organisation_id,action,target_type,target_id,correlation_id,change_context) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), actorRef, org, operation, kind, target, requestcontext.From(ctx).RequestID, raw); err != nil {
		return err
	}
	if eventType == "" {
		return nil
	}
	envelope, err := shared.New(ctx, platform.BusinessOperationsService, eventType, kind, target, map[string]any{"organisation_id": org, "property_id": property, "target_id": target, "operation": operation, "version": version, "snapshot": snapshot})
	if err != nil {
		return err
	}
	if err = store.Add(ctx, tx, envelope); err != nil {
		return err
	}
	// Preserve canonical envelope names while using the existing Business stream;
	// Individual Owner already reserves the root listing/inventory subjects.
	if !strings.HasPrefix(eventType, "business.") {
		_, err = tx.Exec(ctx, `UPDATE outbox_events SET subject=left(subject,length(subject)-length(event_type))||'business.'||event_type WHERE event_id=$1`, envelope.EventID)
	}
	return err
}
