package businesshttp

import (
	"context"
	"errors"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// openOverbookingRecoveryCase links an approved overbooking obligation to the
// Section 11 service-recovery lifecycle in the same transaction. The case is
// opened for the reservation's registered primary guest; an unregistered
// booking source has no CRM guest to own a case and returns nil, leaving the
// retained reconciliation conflict as the operator work item. No remedy,
// compensation or refund is implied by opening the case.
func (h *Handler) openOverbookingRecoveryCase(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, permit ids.ID, reason string) (*ids.ID, error) {
	var guest ids.ID
	e := tx.QueryRow(ctx, `SELECT rg.guest_id FROM reservation_guests rg JOIN guest_property_scopes s ON s.organisation_id=rg.organisation_id AND s.guest_id=rg.guest_id AND s.property_id=$3 AND s.state='ACTIVE' JOIN guest_profiles g ON g.organisation_id=rg.organisation_id AND g.id=rg.guest_id AND g.state IN ('ACTIVE','RESTRICTED') WHERE rg.organisation_id=$1 AND rg.reservation_id=$2 AND rg.guest_id IS NOT NULL ORDER BY rg.is_primary DESC,rg.created_at,rg.id LIMIT 1`, t.Organisation, v.ID, v.PropertyID).Scan(&guest)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	now := h.Auth.Service.Now().UTC()
	id := ids.New()
	evidence := "overbooking-permit:" + permit.String()
	if _, e = tx.Exec(ctx, `INSERT INTO service_recovery_cases(id,organisation_id,property_id,guest_id,reservation_id,issue_type,severity,summary,evidence_reference,sla_due_at,created_by) VALUES($1,$2,$3,$4,$5,'OVERBOOKING','HIGH',$6,$7,$8,$9)`, id, t.Organisation, v.PropertyID, guest, v.ID, "Approved overbooking obligation requires guest recovery before arrival", evidence, now.Add(recoverySLA("HIGH")), t.Account); e != nil {
		return nil, e
	}
	c, e := loadRecoveryCase(ctx, tx, t.Organisation, id, now, false)
	if e != nil {
		return nil, e
	}
	if e = h.recoveryCommit(ctx, tx, t, c, 1, "OPENED", "OPEN", "OPEN", evidence, map[string]any{"issue_type": "OVERBOOKING", "severity": "HIGH", "source": "APPROVED_OVERBOOKING_PERMIT", "permit_id": permit, "reason": reason}); e != nil {
		return nil, e
	}
	return &id, nil
}
