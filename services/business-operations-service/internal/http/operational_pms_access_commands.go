package businesshttp

import (
	"context"
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) pmsAccessRequest(w http.ResponseWriter, r *http.Request) {
	h.pmsAccessSchedule(w, r, true)
}
func (h *Handler) pmsAccessRetry(w http.ResponseWriter, r *http.Request) {
	h.pmsAccessSchedule(w, r, false)
}
func (h *Handler) pmsAccessSchedule(w http.ResponseWriter, r *http.Request, initial bool) {
	var in PMSVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current access reference version and qualified reason required."))
		return
	}
	_, pre, e := h.accessTarget(r, "pms:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "room_status:inspect", "PROPERTY", pre.PropertyID, true); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	action := "RETRY"
	if initial {
		action = "REQUEST"
	}
	h.execute(w, r, "pms:access:"+action+":"+pre.ID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "room_status:inspect", "PROPERTY", pre.PropertyID, true); e != nil {
			return 0, nil, e
		}
		a, e := hospitality.GetAccessReference(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if a.Version != in.Version {
			return 0, nil, apperrors.Conflict("ACCESS_REFERENCE_VERSION", "Access reference changed.")
		}
		j, e := h.loadAccessJob(ctx, tx, t.Organisation, a.ID)
		if initial {
			if !isHospitalityMissing(e) {
				if e != nil {
					return 0, nil, e
				}
				return 0, nil, apperrors.Conflict("ACCESS_REQUEST_EXISTS", "Read back or retry the original command; never create a second issuance key.")
			}
			prepared, e := h.preparePMSAccess(ctx, tx, t, a)
			if e != nil {
				return 0, nil, e
			}
			if !prepared {
				return 0, nil, apperrors.Conflict("ACCESS_CONFIGURATION_REQUIRED", "Current approved access configuration and accepted stay window required.")
			}
			j, e = h.loadAccessJob(ctx, tx, t.Organisation, a.ID)
			if e != nil {
				return 0, nil, e
			}
		} else {
			if e != nil {
				return 0, nil, e
			}
			if j.DeliveryState == "RUNNING" {
				return 0, nil, apperrors.Conflict("ACCESS_COMMAND_RUNNING", "The original dispatch/readback still owns its lease.")
			}
		}
		c, e := hospitality.Load[hospitality.AccessConfiguration](ctx, tx, `SELECT to_jsonb(c) FROM hospitality_access_configurations c WHERE organisation_id=$1 AND id=$2`, t.Organisation, j.ConfigurationID)
		if e != nil {
			return 0, nil, e
		}
		if a.Operation == "ISSUE" && j.DispatchedAt == nil {
			allowed, e := h.accessIssueAllowed(ctx, tx, j, c)
			if e != nil {
				return 0, nil, e
			}
			if !allowed {
				return 0, nil, apperrors.Conflict("ACCESS_ISSUE_AUTHORITY_CHANGED", "Current owning stay, accepted window and configuration authority required.")
			}
		}
		state := "READY"
		if c.Method != "SMART_DEVICE" {
			state = "WAITING_MANUAL"
			if a.State == "CONFIRMED" {
				return 0, nil, apperrors.Conflict("ACCESS_MANUAL_OBSERVATION_IMMUTABLE", "A confirmed physical handover cannot be replaced by another issuance.")
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE hospitality_access_jobs SET delivery_state=$3,available_at=$4,last_error_code='',lease_owner='',lease_expires_at=NULL WHERE organisation_id=$1 AND id=$2`, t.Organisation, a.ID, state, h.Auth.Service.Now().UTC()); e != nil {
			return 0, nil, e
		}
		if e = h.recordPMSAccessObservation(ctx, tx, t, a, j.RequestDigest, "UNKNOWN", "SERVICE_SOURCE", a.ProviderReference, "EXACT_ORIGINAL_COMMAND_"+action, in.Reason); e != nil {
			return 0, nil, e
		}
		a, e = hospitality.GetAccessReference(ctx, tx, t.Organisation, a.ID)
		if e != nil {
			return 0, nil, e
		}
		out, e := h.pmsAccessView(ctx, tx, a)
		return 202, out, e
	})
}
func (h *Handler) pmsAccessAcknowledge(w http.ResponseWriter, r *http.Request) {
	var in PMSAccessAcknowledgement
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || len(in.RequestDigest) != 64 || !crm.OpaqueReference(in.CredentialReference) || !crm.OpaqueReference(in.EvidenceReference) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current command digest/version, opaque credential inventory reference and actual physical handover/return evidence required; no credential secrets."))
		return
	}
	_, pre, e := h.accessTarget(r, "pms:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "room_status:inspect", "PROPERTY", pre.PropertyID, true); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:access:manual:"+pre.ID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "room_status:inspect", "PROPERTY", pre.PropertyID, true); e != nil {
			return 0, nil, e
		}
		a, e := hospitality.GetAccessReference(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		j, e := h.loadAccessJob(ctx, tx, t.Organisation, a.ID)
		if e != nil {
			return 0, nil, e
		}
		c, e := hospitality.Load[hospitality.AccessConfiguration](ctx, tx, `SELECT to_jsonb(c) FROM hospitality_access_configurations c WHERE organisation_id=$1 AND id=$2`, t.Organisation, j.ConfigurationID)
		if e != nil {
			return 0, nil, e
		}
		if c.Method == "SMART_DEVICE" {
			return 0, nil, apperrors.PermissionDenied("A browser cannot assert provider device truth; authenticated original-command readback required.")
		}
		if a.Version != in.Version || j.RequestDigest != in.RequestDigest || a.State == "CONFIRMED" || j.DeliveryState != "WAITING_MANUAL" {
			return 0, nil, apperrors.Conflict("ACCESS_MANUAL_COMMAND_CHANGED", "Current original pending physical command required.")
		}
		if a.Operation == "ISSUE" {
			allowed, e := h.accessIssueAllowed(ctx, tx, j, c)
			if e != nil {
				return 0, nil, e
			}
			if !allowed {
				return 0, nil, apperrors.Conflict("ACCESS_ISSUE_AUTHORITY_CHANGED", "A physical key cannot be issued after stay, window or current configuration authority ended.")
			}
			var revoked bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access_grant_references WHERE organisation_id=$1 AND assignment_id=$2 AND operation='REVOKE')`, t.Organisation, a.AssignmentID).Scan(&revoked); e != nil {
				return 0, nil, e
			}
			if revoked {
				return 0, nil, apperrors.Conflict("ACCESS_REVOCATION_STARTED", "The original grant is already pending revocation.")
			}
		} else {
			var original string
			if e = tx.QueryRow(ctx, `SELECT provider_reference FROM access_grant_references WHERE organisation_id=$1 AND assignment_id=$2 AND operation='ISSUE'`, t.Organisation, a.AssignmentID).Scan(&original); e != nil {
				return 0, nil, e
			}
			if original != "" && original != in.CredentialReference {
				return 0, nil, apperrors.Conflict("ACCESS_CREDENTIAL_REFERENCE_MISMATCH", "Return evidence must name the original issued inventory reference.")
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE hospitality_access_jobs SET delivery_state='COMPLETE',last_error_code='' WHERE organisation_id=$1 AND id=$2`, t.Organisation, a.ID); e != nil {
			return 0, nil, e
		}
		if e = h.recordPMSAccessObservation(ctx, tx, t, a, j.RequestDigest, "CONFIRMED", "MANUAL_OBSERVATION", in.CredentialReference, in.EvidenceReference, in.Reason); e != nil {
			return 0, nil, e
		}
		a, e = hospitality.GetAccessReference(ctx, tx, t.Organisation, a.ID)
		if e != nil {
			return 0, nil, e
		}
		out, e := h.pmsAccessView(ctx, tx, a)
		return 200, out, e
	})
}
func (h *Handler) recordPMSAccessObservation(ctx context.Context, tx pgx.Tx, t tenant, a hospitality.AccessReference, digest, state, origin, credential, evidence, reason string) error {
	tag, e := tx.Exec(ctx, `UPDATE access_grant_references SET state=$3,provider_reference=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND version=$5`, a.OrganisationID, a.ID, state, credential, a.Version)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return apperrors.Conflict("ACCESS_REFERENCE_VERSION", "Access outcome changed concurrently.")
	}
	if _, e := tx.Exec(ctx, `INSERT INTO hospitality_access_observations(id,organisation_id,property_id,access_reference_id,reference_version,request_digest,state,origin,credential_reference,evidence_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, ids.New(), a.OrganisationID, a.PropertyID, a.ID, a.Version+1, digest, state, origin, credential, evidence, reason, t.Account); e != nil {
		return e
	}
	return h.pmsCommit(ctx, tx, t, a.PropertyID, a.ID, "access_reference", "ACCESS_OUTCOME_RECORDED", "hospitality.access.updated.v1", a.Version+1, map[string]any{"reference_version": a.Version + 1, "stay_id": a.StayID, "assignment_id": a.AssignmentID, "operation": a.Operation, "state": state, "origin": origin, "request_digest": digest, "evidence_reference": evidence, "reason": reason, "actor_service": serviceActor(t)})
}
