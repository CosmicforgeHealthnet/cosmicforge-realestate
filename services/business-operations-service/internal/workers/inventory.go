package workers

import (
	"context"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

type InventoryReferences struct {
	Projection                    portfolio.VerificationProjector
	Verification                  verification.API
	Media                         media.API
	afterVerification, afterMedia ids.ID
}

func (w *InventoryReferences) Tick(ctx context.Context) error {
	rows, err := w.Projection.Pool.Query(ctx, `SELECT v.id,v.organisation_id,v.node_id,v.case_id FROM inventory_verification_links v JOIN inventory_nodes n ON n.id=v.node_id AND n.organisation_id=v.organisation_id WHERE n.state NOT IN ('ARCHIVED','RETIRED') AND ($1::uuid IS NULL OR v.id>$1) ORDER BY v.id LIMIT 20`, w.afterVerification)
	if err != nil {
		return err
	}
	type link struct{ id, org, node, external ids.ID }
	links := []link{}
	for rows.Next() {
		var l link
		if err = rows.Scan(&l.id, &l.org, &l.node, &l.external); err != nil {
			break
		}
		links = append(links, l)
	}
	rowErr := rows.Err()
	rows.Close()
	if err == nil {
		err = rowErr
	}
	if err != nil {
		return err
	}
	if len(links) == 0 {
		w.afterVerification = ids.Nil
	}
	var failures []error
	for _, l := range links {
		w.afterVerification = l.id
		if ctx.Err() != nil {
			return ctx.Err()
		}
		call, cancel := context.WithTimeout(ctx, 6*time.Second)
		if w.Verification == nil {
			cancel()
			failures = append(failures, errors.New("property verification provider unavailable"))
			continue
		}
		c, e := w.Verification.Get(requestcontext.WithActor(call, requestcontext.Actor{}), l.external)
		if e == nil && c.VerificationID == l.external {
			e = w.Projection.Apply(call, l.org, l.node, ids.Nil, 0, c, nil)
		} else if e == nil {
			e = errors.New("property verification identity mismatch")
		}
		cancel()
		if e != nil {
			failures = append(failures, errors.New("property verification reconciliation unavailable"))
		}
	}
	if w.Media == nil {
		return errors.Join(failures...)
	}
	rows, err = w.Projection.Pool.Query(ctx, `SELECT id,organisation_id,node_id,media_id,owner_type,owner_id,provider_status,version FROM inventory_media_links WHERE ($1::uuid IS NULL OR id>$1) ORDER BY id LIMIT 20`, w.afterMedia)
	if err != nil {
		return err
	}
	type mediaLink struct {
		link
		owner             ids.ID
		ownerType, status string
		version           int64
	}
	mediaLinks := []mediaLink{}
	for rows.Next() {
		var l mediaLink
		if err = rows.Scan(&l.id, &l.org, &l.node, &l.external, &l.ownerType, &l.owner, &l.status, &l.version); err != nil {
			break
		}
		mediaLinks = append(mediaLinks, l)
	}
	rowErr = rows.Err()
	rows.Close()
	if err == nil {
		err = rowErr
	}
	if err != nil {
		return err
	}
	if len(mediaLinks) == 0 {
		w.afterMedia = ids.Nil
	}
	for _, l := range mediaLinks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.afterMedia = l.id
		call, cancel := context.WithTimeout(ctx, 6*time.Second)
		m, e := w.Media.Get(requestcontext.WithActor(call, requestcontext.Actor{}), l.external)
		cancel()
		status := m.Status
		if e != nil || m.ID != l.external || m.OwnerDomain != string(platform.DomainBusinessOperations) || m.OwnerType != l.ownerType || m.OwnerID != l.owner {
			status = "UNAVAILABLE"
		}
		if status == "" {
			status = "UNAVAILABLE"
		}
		err = database.WithTx(ctx, w.Projection.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, l.org); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE inventory_media_links SET provider_status=$4,provider_checked_at=now(),version=version+1 WHERE organisation_id=$1 AND id=$2 AND version=$3`, l.org, l.id, l.version, status)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			if status == l.status {
				return nil
			}
			if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,organisation_id,action,target_type,target_id,change_context) VALUES($1,$2,'INVENTORY_MEDIA_STATUS_PROJECTED','inventory_node',$3,jsonb_build_object('media_link_id',$4::uuid,'status',$5::text))`, ids.New(), l.org, l.node, l.id, status); err != nil {
				return err
			}
			event, err := events.New(requestcontext.WithActor(ctx, requestcontext.Actor{}), platform.BusinessOperationsService, "business.inventory.media.changed.v1", "inventory_node", l.node, map[string]any{"organisation_id": l.org, "target_id": l.node, "media_link_id": l.id, "status": status})
			if err != nil {
				return err
			}
			return w.Projection.Outbox.Add(ctx, tx, event)
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
