package businesshttp

import (
	"context"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/smartaccess"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

// ProcessPMSAccess uses the existing worker cadence. All external calls happen
// after a local commit, with original-command lookup before a repeated effect.
func (h *Handler) ProcessPMSAccess(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	var failures []error
	if e := h.reconcilePMSAccessRevocations(ctx, limit); e != nil {
		failures = append(failures, e)
	}
	for range limit {
		now := h.Auth.Service.Now().UTC()
		owner := ids.New().String()
		j, e := hospitality.Load[hospitality.AccessJob](ctx, h.Auth.Service.Pool, `UPDATE hospitality_access_jobs j SET delivery_state='RUNNING',attempts=attempts+1,lease_owner=$1,lease_generation=lease_generation+1,lease_expires_at=$2::timestamptz+interval '30 seconds' WHERE id=(SELECT id FROM hospitality_access_jobs WHERE available_at<=$2 AND (delivery_state IN ('READY','RETRY','DEPENDENCY_BLOCKED') OR delivery_state='RUNNING' AND lease_expires_at<=$2) ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING to_jsonb(j)`, owner, now)
		if isHospitalityMissing(e) {
			break
		}
		if e != nil {
			return errors.Join(append(failures, e)...)
		}
		jobCtx := requestcontext.WithRequestID(ctx, "pms-access:"+j.ID.String())
		out := h.runPMSAccess(jobCtx, j)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = h.finishPMSAccess(jobCtx, j, out); e != nil {
			failures = append(failures, e)
		}
		if out.ErrorCode != "" {
			failures = append(failures, apperrors.Unavailable(out.ErrorCode))
		}
	}
	return errors.Join(failures...)
}
func (h *Handler) reconcilePMSAccessRevocations(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT j.organisation_id,j.id FROM hospitality_access_jobs j JOIN access_grant_references a ON a.organisation_id=j.organisation_id AND a.id=j.id WHERE j.request->>'operation'='ISSUE' AND a.operation='ISSUE' AND j.review_at<=$1 AND NOT EXISTS(SELECT 1 FROM access_grant_references rev WHERE rev.organisation_id=a.organisation_id AND rev.assignment_id=a.assignment_id AND rev.operation='REVOKE') ORDER BY j.review_at,j.id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if e = rows.Scan(&r.org, &r.id); e != nil {
			break
		}
		refs = append(refs, r)
	}
	re := rows.Err()
	rows.Close()
	if e == nil {
		e = re
	}
	if e != nil {
		return e
	}
	var failures []error
	for _, r := range refs {
		var authoritySourceFailed bool
		reviewCtx := requestcontext.WithRequestID(ctx, "pms-access-source:"+r.id.String())
		e = database.WithTx(reviewCtx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, r.org); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			j, e := h.loadAccessJob(ctx, tx, r.org, r.id)
			if e != nil {
				return e
			}
			c, e := hospitality.Load[hospitality.AccessConfiguration](ctx, tx, `SELECT to_jsonb(c) FROM hospitality_access_configurations c WHERE organisation_id=$1 AND id=$2`, r.org, j.ConfigurationID)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `SAVEPOINT access_source_authority`); e != nil {
				return e
			}
			allowed, sourceError := h.accessIssueAllowed(ctx, tx, j, c)
			if sourceError != nil {
				authoritySourceFailed = true
				if _, e = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT access_source_authority`); e != nil {
					return e
				}
				allowed = false
			}
			if _, e = tx.Exec(ctx, `RELEASE SAVEPOINT access_source_authority`); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE hospitality_access_jobs SET review_at=$3 WHERE organisation_id=$1 AND id=$2`, r.org, r.id, h.Auth.Service.Now().Add(time.Minute)); e != nil {
				return e
			}
			if !allowed {
				reason := "Current stay, accepted access window or recorded configuration authority ended; preserve UNKNOWN until actual revocation evidence"
				if sourceError != nil {
					reason = "Recorded access authority source could not be read; retain source review and revoke conservatively without assuming source validity"
				}
				_, e = h.queuePMSAccess(ctx, tx, tenant{Organisation: r.org}, j.Request.StayID, j.Request.AssignmentID, "REVOKE", reason)
				return e
			}
			return nil
		})
		if e != nil {
			failures = append(failures, e)
		} else if authoritySourceFailed {
			failures = append(failures, apperrors.Unavailable("ACCESS_AUTHORITY_SOURCE_READ_FAILED"))
		}
	}
	return errors.Join(failures...)
}

type pmsAccessOutcome struct {
	Observation smartaccess.Observation
	State       string
	Delivery    string
	Origin      string
	ErrorCode   string
}

func (h *Handler) accessJobFence(ctx context.Context, tx pgx.Tx, j hospitality.AccessJob) error {
	var current bool
	if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hospitality_access_jobs WHERE organisation_id=$1 AND id=$2 AND delivery_state='RUNNING' AND lease_owner=$3 AND lease_generation=$4 AND lease_expires_at>$5)`, j.OrganisationID, j.ID, j.LeaseOwner, j.LeaseGeneration, h.Auth.Service.Now()).Scan(&current); e != nil {
		return e
	}
	if !current {
		return apperrors.Conflict("ACCESS_LEASE_CHANGED", "Original access command lease changed.")
	}
	return nil
}
func (h *Handler) accessDispatchAuthority(ctx context.Context, j hospitality.AccessJob, c hospitality.AccessConfiguration, dispatch bool) (bool, error) {
	allowed := j.Request.Operation == "REVOKE"
	e := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, j.OrganisationID); e != nil {
			return e
		}
		if e := h.accessJobFence(ctx, tx, j); e != nil {
			return e
		}
		if j.Request.Operation == "ISSUE" {
			var e error
			allowed, e = h.accessIssueAllowed(ctx, tx, j, c)
			if e != nil {
				return e
			}
			var revoking bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access_grant_references WHERE organisation_id=$1 AND assignment_id=$2 AND operation='REVOKE')`, j.OrganisationID, j.Request.AssignmentID).Scan(&revoking); e != nil {
				return e
			}
			allowed = allowed && !revoking
		}
		if allowed && dispatch {
			_, e := tx.Exec(ctx, `UPDATE hospitality_access_jobs SET dispatched_at=coalesce(dispatched_at,$3) WHERE organisation_id=$1 AND id=$2`, j.OrganisationID, j.ID, h.Auth.Service.Now().UTC())
			return e
		}
		return nil
	})
	return allowed, e
}
func (h *Handler) runPMSAccess(ctx context.Context, j hospitality.AccessJob) pmsAccessOutcome {
	fail := func(code, delivery string) pmsAccessOutcome {
		return pmsAccessOutcome{State: "UNKNOWN", Delivery: delivery, Origin: "SERVICE_SOURCE", ErrorCode: code}
	}
	if security.SHA256Hex(marshalCommercial(j.Request)) != j.RequestDigest {
		return fail("ACCESS_REQUEST_INTEGRITY_FAILED", "QUARANTINED")
	}
	c, e := hospitality.Load[hospitality.AccessConfiguration](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(c) FROM hospitality_access_configurations c WHERE organisation_id=$1 AND id=$2`, j.OrganisationID, j.ConfigurationID)
	if e != nil {
		return fail("ACCESS_CONFIGURATION_READ_FAILED", "RETRY")
	}
	if c.Method != "SMART_DEVICE" {
		return fail("ACCESS_REQUIRES_PHYSICAL_OBSERVATION", "WAITING_MANUAL")
	}
	allowed, e := h.accessDispatchAuthority(ctx, j, c, false)
	if e != nil {
		return fail("ACCESS_SOURCE_OR_LEASE_CHANGED", "RETRY")
	}
	if !allowed && j.DispatchedAt == nil {
		return pmsAccessOutcome{State: "FAILED", Delivery: "CANCELLED", Origin: "SERVICE_SOURCE", Observation: smartaccess.Observation{RequestDigest: j.RequestDigest, EvidenceReference: "NOT_DISPATCHED_SOURCE_INELIGIBLE", ObservedAt: h.Auth.Service.Now()}}
	}
	if j.Request.Operation == "REVOKE" {
		issue, e := h.loadAccessJob(ctx, h.Auth.Service.Pool, j.OrganisationID, j.Request.IssueID)
		if e != nil {
			return fail("ACCESS_ISSUE_RECONCILIATION_REQUIRED", "RETRY")
		}
		if issue.DeliveryState == "CANCELLED" {
			return pmsAccessOutcome{State: "CONFIRMED", Delivery: "COMPLETE", Origin: "SERVICE_SOURCE", Observation: smartaccess.Observation{RequestDigest: j.RequestDigest, EvidenceReference: "ORIGINAL_ISSUE_DEFINITIVELY_ABSENT", ObservedAt: h.Auth.Service.Now()}}
		}
		if issue.DeliveryState != "COMPLETE" {
			return fail("ACCESS_ISSUE_RECONCILIATION_REQUIRED", "RETRY")
		}
	}
	adapter := h.AccessAdapters[c.Provider]
	if adapter == nil {
		return fail("ACCESS_AUTHENTICATED_ADAPTER_UNAVAILABLE", "DEPENDENCY_BLOCKED")
	}
	call, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var observation smartaccess.Observation
	boundObservation := func(o smartaccess.Observation) bool {
		if !hospitality.ValidAccessObservation(o, j.RequestDigest, j.Request.Operation, h.Auth.Service.Now()) {
			return false
		}
		if o.State == "ABSENT" {
			return true
		}
		id := j.ID
		if j.Request.Operation == "REVOKE" {
			id = j.Request.IssueID
		}
		a, e := hospitality.GetAccessReference(ctx, h.Auth.Service.Pool, j.OrganisationID, id)
		return e == nil && (a.ProviderReference == "" || a.ProviderReference == o.CredentialReference)
	}
	if j.DispatchedAt != nil {
		observation, e = adapter.Lookup(call, j.Request, j.IdempotencyKey)
		if e != nil {
			return fail("ACCESS_PROVIDER_READBACK_UNAVAILABLE", "RETRY")
		}
		if !boundObservation(observation) {
			return fail("ACCESS_PROVIDER_OBSERVATION_MISMATCH", "QUARANTINED")
		}
		if j.Request.Operation == "ISSUE" && observation.State == "ACTIVE" || j.Request.Operation == "REVOKE" && (observation.State == "REVOKED" || observation.State == "ABSENT") {
			return pmsAccessOutcome{State: "CONFIRMED", Delivery: "COMPLETE", Origin: "AUTHENTICATED_ADAPTER", Observation: observation}
		}
		if !allowed {
			return pmsAccessOutcome{State: "FAILED", Delivery: "CANCELLED", Origin: "AUTHENTICATED_ADAPTER", Observation: observation}
		}
	}
	allowed, e = h.accessDispatchAuthority(ctx, j, c, true)
	if e != nil {
		return fail("ACCESS_SOURCE_OR_LEASE_CHANGED", "RETRY")
	}
	if !allowed {
		return fail("ACCESS_SOURCE_CHANGED_BEFORE_DISPATCH", "RETRY")
	}
	observation, e = adapter.Execute(call, j.Request, j.IdempotencyKey)
	if e != nil {
		return fail("ACCESS_PROVIDER_OUTCOME_UNKNOWN", "RETRY")
	}
	if !boundObservation(observation) {
		return fail("ACCESS_PROVIDER_OBSERVATION_MISMATCH", "QUARANTINED")
	}
	confirmed := j.Request.Operation == "ISSUE" && observation.State == "ACTIVE" || j.Request.Operation == "REVOKE" && (observation.State == "REVOKED" || observation.State == "ABSENT")
	if !confirmed {
		return fail("ACCESS_PROVIDER_OUTCOME_PENDING", "RETRY")
	}
	return pmsAccessOutcome{State: "CONFIRMED", Delivery: "COMPLETE", Origin: "AUTHENTICATED_ADAPTER", Observation: observation}
}
func (h *Handler) finishPMSAccess(ctx context.Context, j hospitality.AccessJob, out pmsAccessOutcome) error {
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, j.OrganisationID); e != nil {
			return e
		}
		if e := h.accessJobFence(ctx, tx, j); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
			return e
		}
		a, e := hospitality.GetAccessReference(ctx, tx, j.OrganisationID, j.ID)
		if e != nil {
			return e
		}
		if out.ErrorCode != "" {
			out.Observation.EvidenceReference = out.ErrorCode
			out.Observation.CredentialReference = a.ProviderReference
			if j.Attempts >= 12 && out.Delivery == "RETRY" {
				out.Delivery = "QUARANTINED"
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE hospitality_access_jobs SET delivery_state=$3,available_at=$4,lease_owner='',lease_expires_at=NULL,last_error_code=$5 WHERE organisation_id=$1 AND id=$2`, j.OrganisationID, j.ID, out.Delivery, h.Auth.Service.Now().Add(5*time.Minute), out.ErrorCode); e != nil {
			return e
		}
		t := tenant{Organisation: j.OrganisationID}
		if e = h.recordPMSAccessObservation(ctx, tx, t, a, j.RequestDigest, out.State, out.Origin, out.Observation.CredentialReference, out.Observation.EvidenceReference, "Original bounded access command/readback; no credential secret or unverified device outcome accepted"); e != nil {
			return e
		}
		if out.ErrorCode != "" && (out.ErrorCode != j.LastErrorCode || out.Delivery == "QUARANTINED" && j.Attempts == 12) {
			var account ids.ID
			if e = tx.QueryRow(ctx, `SELECT created_by FROM hospitality_access_configurations WHERE organisation_id=$1 AND id=$2`, j.OrganisationID, j.ConfigurationID).Scan(&account); e != nil {
				return e
			}
			if e = h.queueNotice(ctx, tx, account, "Access provider outcome requires review", "The original bounded access request remains unconfirmed or quarantined. Review the retained command and authenticate its original provider readback; no replacement issuance was created.", true, "access-reference:"+j.ID.String()); e != nil {
				return e
			}
		}
		if a.Operation == "ISSUE" && out.State == "CONFIRMED" {
			c, e := hospitality.Load[hospitality.AccessConfiguration](ctx, tx, `SELECT to_jsonb(c) FROM hospitality_access_configurations c WHERE organisation_id=$1 AND id=$2`, j.OrganisationID, j.ConfigurationID)
			if e != nil {
				return e
			}
			allowed, e := h.accessIssueAllowed(ctx, tx, j, c)
			if e != nil {
				return e
			}
			if !allowed {
				_, e = h.queuePMSAccess(ctx, tx, t, a.StayID, a.AssignmentID, "REVOKE", "Provider issuance completed after its local source authority ended; revoke the original credential without presenting it as current active access")
				return e
			}
		}
		return nil
	})
}
