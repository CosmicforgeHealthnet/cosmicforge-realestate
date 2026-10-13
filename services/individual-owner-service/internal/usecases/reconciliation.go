package usecases

import (
	"context"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// ReconciliationIssue exposes local reference/worker inconsistencies without
// claiming external truth. Recovery uses the existing domain workers; material
// conflicts require the existing restricted admin review path.
type ReconciliationIssue struct {
	Code       string    `json:"code"`
	ResourceID ids.ID    `json:"resource_id"`
	State      string    `json:"state"`
	Action     string    `json:"action"`
	ObservedAt time.Time `json:"observed_at"`
}

func (s *Service) ReconciliationIssues(ctx context.Context, subj authz.Subject) ([]ReconciliationIssue, error) {
	if _, err := s.authorize(ctx, subj, authz.ActAdminAccountRead, ids.Nil); err != nil {
		return nil, err
	}
	now := s.now()
	rows, err := s.Pool.Query(ctx, `SELECT code,resource_id,state,action,observed_at FROM (
 SELECT 'VERIFICATION_READBACK_DUE' AS code, verification_id AS resource_id,status AS state,'EXISTING_VERIFICATION_WORKER' AS action,last_synced_at AS observed_at FROM verification_links WHERE status IN ('SUBMITTED','PROCESSING','MANUAL_REVIEW','VERIFIED') AND last_synced_at<$1::timestamptz-interval '15 minutes'
 UNION ALL SELECT 'MEDIA_READBACK_REQUIRED',media_id,media_status,'MEDIA_STATUS_READBACK',updated_at FROM property_media_links WHERE media_status='PENDING' AND updated_at<$1::timestamptz-interval '15 minutes'
 UNION ALL SELECT 'OUTBOX_DELIVERY_FAILED',event_id,CASE WHEN dead_at IS NULL THEN 'PENDING' ELSE 'DEAD' END,CASE WHEN dead_at IS NULL THEN 'EXISTING_OUTBOX_WORKER' ELSE 'MANUAL_REVIEW' END,created_at FROM outbox_events WHERE published_at IS NULL AND (dead_at IS NOT NULL OR created_at<$1::timestamptz-interval '15 minutes')
 UNION ALL SELECT 'NOTIFICATION_DELIVERY_FAILED',dispatch_id,status,CASE WHEN status='DEAD' THEN 'MANUAL_REVIEW' ELSE 'EXISTING_NOTIFICATION_WORKER' END,created_at FROM notification_dispatches WHERE status='DEAD' OR (status='PENDING' AND created_at<$1::timestamptz-interval '15 minutes')
 UNION ALL SELECT 'CORE_BUSINESS_READBACK_DUE',business_id,observed_state,'EXISTING_CORE_READBACK_WORKER',updated_at FROM financial_core_business_bindings WHERE updated_at<$1::timestamptz-interval '15 minutes'
 UNION ALL SELECT 'SOURCE_BUSINESS_MISSING',b.business_id,b.observed_state,'MANUAL_REVIEW',b.updated_at FROM financial_core_business_bindings b WHERE
 (b.business_type='deposit_obligation' AND NOT EXISTS(SELECT 1 FROM deposit_obligations d WHERE d.id=b.business_id)) OR
 (b.business_type='closing_milestone' AND NOT EXISTS(SELECT 1 FROM closing_milestones m WHERE m.id=b.business_id)) OR
 (b.business_type='shortlet_booking' AND NOT EXISTS(SELECT 1 FROM shortlet_bookings k WHERE k.id=b.business_id)) OR
 (b.business_type='shortlet_booking_extension' AND NOT EXISTS(SELECT 1 FROM shortlet_booking_extensions e WHERE e.id=b.business_id)) OR
 (b.business_type='maintenance_job' AND NOT EXISTS(SELECT 1 FROM maintenance_jobs j WHERE j.id=b.business_id)) OR
 (b.business_type='professional_appointment' AND NOT EXISTS(SELECT 1 FROM professional_appointments a WHERE a.id=b.business_id))
 UNION ALL SELECT 'BOOKING_PAYMENT_PROJECTION_CONFLICT',b.id,b.status,'MANUAL_REVIEW',c.updated_at FROM shortlet_bookings b JOIN financial_core_business_bindings c ON c.business_type='shortlet_booking' AND c.business_id=b.id WHERE c.observed_state IN ('INITIALIZED','FAILED','CANCELLED') AND (b.paid_minor>0 OR b.status IN ('CONFIRMED','CHECKED_IN'))
 UNION ALL SELECT 'ORIGINAL_ISSUER_REVIEW_REQUIRED',obligation_id,observed_state,'MANUAL_REVIEW',updated_at FROM financial_core_collections WHERE owner_account_id IS NULL
 UNION ALL SELECT 'EXTERNAL_OUTCOME_UNRESOLVED',aggregate_id,state,CASE WHEN state='MANUAL_REVIEW' THEN 'MANUAL_REVIEW' ELSE 'ORIGINAL_OPERATION_READBACK' END,updated_at FROM owner_external_operations WHERE state<>'CONFIRMED'
 UNION ALL SELECT 'MANAGER_EXPIRY_PROJECTION_DUE',id,status,'EXISTING_MANDATE_WORKER',updated_at FROM manager_assignments WHERE status IN ('ACTIVE','EXPIRING') AND ends_at<=$1
 UNION ALL SELECT 'ACCESS_REVOCATION_DUE',id,status,'EXISTING_ACCESS_WORKER',updated_at FROM access_grants WHERE (status IN ('REQUESTED','APPROVED','ISSUED','ACTIVE') AND ends_at<=$1) OR (status IN ('REVOKED','EXPIRED','COMPROMISED') AND credential_ref IS NOT NULL AND credential_revoked_at IS NULL)
 ) issues ORDER BY observed_at,code,resource_id LIMIT 500`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReconciliationIssue{}
	for rows.Next() {
		var issue ReconciliationIssue
		if err := rows.Scan(&issue.Code, &issue.ResourceID, &issue.State, &issue.Action, &issue.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, issue)
	}
	return out, rows.Err()
}
