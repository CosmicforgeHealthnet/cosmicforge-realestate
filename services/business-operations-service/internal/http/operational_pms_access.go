package businesshttp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/smartaccess"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type PMSAccessConfigurationCommand struct {
	PropertyID      ids.ID    `json:"property_id"`
	RoomID          ids.ID    `json:"room_id"`
	ExpectedVersion int64     `json:"expected_version"`
	Method          string    `json:"method"`
	Provider        string    `json:"provider,omitempty"`
	DeviceReference string    `json:"device_reference,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	ApprovalID      ids.ID    `json:"approval_id,omitempty"`
	Reason          string    `json:"reason"`
}
type PMSAccessAcknowledgement struct {
	Version             int64  `json:"version"`
	RequestDigest       string `json:"request_digest"`
	CredentialReference string `json:"credential_reference"`
	EvidenceReference   string `json:"evidence_reference"`
	Reason              string `json:"reason"`
}
type PMSAccessCommandView struct {
	ConfigurationID ids.ID    `json:"configuration_id"`
	Method          string    `json:"method"`
	RequestDigest   string    `json:"request_digest"`
	DeliveryState   string    `json:"delivery_state"`
	StartsAt        time.Time `json:"starts_at"`
	EndsAt          time.Time `json:"ends_at"`
	LastErrorCode   string    `json:"last_error_code"`
}
type PMSAccessView struct {
	Reference           hospitality.AccessReference `json:"reference"`
	Command             *PMSAccessCommandView       `json:"command,omitempty"`
	EffectiveState      string                      `json:"effective_state"`
	DeliveryConfirmed   bool                        `json:"delivery_confirmed"`
	RevocationConfirmed bool                        `json:"revocation_confirmed"`
}

func (h *Handler) registerPMSAccess(r chi.Router) {
	r.Get("/hospitality-access-configurations", h.pmsAccessConfigurations)
	r.Post("/hospitality-access-configurations", h.pmsAccessConfiguration)
	r.Get("/access-grants", h.pmsAccessList)
	r.Get("/access-grants/{id}", h.pmsAccessGet)
	r.Post("/access-grants/{id}/request", h.pmsAccessRequest)
	r.Post("/access-grants/{id}/acknowledge", h.pmsAccessAcknowledge)
	r.Post("/access-grants/{id}/retry", h.pmsAccessRetry)
}
func (h *Handler) pmsAccessConfigurations(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "hospitality_access_configurations", "pms:read")
}
func (h *Handler) pmsAccessConfiguration(w http.ResponseWriter, r *http.Request) {
	var in PMSAccessConfigurationCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.RoomID.ValidateV7() != nil || in.ExpectedVersion < 0 || !slices.Contains([]string{"DISABLED", "MANUAL_KEY", "MANUAL_CARD", "SMART_DEVICE"}, in.Method) || !validStaffReason(in.Reason) || in.Method != "DISABLED" && !crm.OpaqueReference(in.DeviceReference) || in.Method != "SMART_DEVICE" && in.Provider != "" || in.Method == "DISABLED" && in.DeviceReference != "" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact canonical room, bounded access method/window and opaque device reference required; never submit a credential code."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "pms:write", "PROPERTY", in.PropertyID, true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "room_status:inspect", "PROPERTY", in.PropertyID, true); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var canonical bool
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM room_status_current WHERE organisation_id=$1 AND property_id=$2 AND room_id=$3)`, t.Organisation, in.PropertyID, in.RoomID).Scan(&canonical); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !canonical {
		reply(w, r, 0, nil, apperrors.NotFound("ACCESS_ROOM_NOT_FOUND", "Canonical operational room not found."))
		return
	}
	proof := smartaccess.DeviceProof{}
	if in.Method == "SMART_DEVICE" {
		adapter := h.AccessAdapters[in.Provider]
		if adapter == nil {
			reply(w, r, 0, nil, apperrors.Unavailable("No authenticated smart-access adapter is configured for this provider."))
			return
		}
		device := smartaccess.Device{OrganisationID: t.Organisation, PropertyID: in.PropertyID, RoomID: in.RoomID, Provider: in.Provider, DeviceReference: in.DeviceReference}
		call, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		proof, e = adapter.Inspect(call, device)
		cancel()
		now := h.Auth.Service.Now()
		if e != nil {
			reply(w, r, 0, nil, apperrors.Unavailable("Authenticated device ownership readback unavailable."))
			return
		}
		if proof.Device != device || !crm.OpaqueReference(proof.AccountReference) || !crm.OpaqueReference(proof.EvidenceReference) || proof.ObservedAt.Before(now.Add(-5*time.Minute)) || proof.ObservedAt.After(now.Add(time.Minute)) || proof.ExpiresAt.Before(in.ExpiresAt) {
			reply(w, r, 0, nil, apperrors.PermissionDenied("Provider proof does not bind this exact tenant, room, device and access policy window."))
			return
		}
	}
	h.execute(w, r, "pms:access-configuration:"+in.RoomID.String(), in, "pms:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "room_status:inspect", "PROPERTY", in.PropertyID, true); e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(7*24*time.Hour)) {
			return 0, nil, apperrors.InvalidRequest("Access configuration must expire within seven days.")
		}
		var current int64
		var scoped bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM room_status_current WHERE organisation_id=$1 AND property_id=$2 AND room_id=$3),(SELECT coalesce(max(version),0) FROM hospitality_access_configurations WHERE organisation_id=$1 AND room_id=$3)`, t.Organisation, in.PropertyID, in.RoomID).Scan(&scoped, &current); e != nil {
			return 0, nil, e
		}
		if !scoped {
			return 0, nil, apperrors.NotFound("ACCESS_ROOM_NOT_FOUND", "Canonical operational room not found.")
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("ACCESS_CONFIGURATION_VERSION", "Access configuration changed.")
		}
		var approval any
		if in.Method != "DISABLED" {
			if t.Direct.ExpiresAt != nil && in.ExpiresAt.After(*t.Direct.ExpiresAt) {
				return 0, nil, apperrors.PermissionDenied("Access configuration cannot outlive its direct membership.")
			}
			allowed, e := h.currentRecordedPMSAuthority(ctx, tx, t.Organisation, in.PropertyID, t.Member, t.Role, "pms:write")
			if e != nil {
				return 0, nil, e
			}
			if !allowed {
				return 0, nil, apperrors.PermissionDenied("Direct current authority is required for bounded access configuration.")
			}
			allowed, e = h.currentRecordedPMSAuthority(ctx, tx, t.Organisation, in.PropertyID, t.Member, t.Role, "room_status:inspect")
			if e != nil {
				return 0, nil, e
			}
			if !allowed {
				return 0, nil, apperrors.PermissionDenied("Direct inspection authority is required; temporary or per-action step-up grants cannot authorize an unattended access policy.")
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_ACCESS_CONFIGURATION", target.Scope, in.ApprovalID, map[string]any{"command": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("Disabling access does not consume an issuance approval.")
		}
		id := ids.New()
		var proofRecord any = map[string]any{}
		if in.Method == "SMART_DEVICE" {
			proofRecord = proof
		}
		if _, e = tx.Exec(ctx, `INSERT INTO hospitality_access_configurations(id,organisation_id,property_id,room_id,version,method,provider,device_reference,account_reference,device_proof,expires_at,approval_id,authorizing_membership_id,authorizing_role_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, id, t.Organisation, in.PropertyID, in.RoomID, current+1, in.Method, in.Provider, in.DeviceReference, proof.AccountReference, marshalCommercial(proofRecord), in.ExpiresAt, approval, t.Member, t.Role, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, id, "access_configuration", "ACCESS_CONFIGURATION_RECORDED", "hospitality.access.updated.v1", 1, map[string]any{"room_id": in.RoomID, "version": current + 1, "method": in.Method, "expires_at": in.ExpiresAt, "approval_id": approval, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := hospitality.LatestAccessConfiguration(ctx, tx, t.Organisation, in.RoomID)
		return 201, out, e
	})
}

func (h *Handler) loadAccessJob(ctx context.Context, q database.Querier, org, id ids.ID) (hospitality.AccessJob, error) {
	return hospitality.Load[hospitality.AccessJob](ctx, q, `SELECT to_jsonb(j) FROM hospitality_access_jobs j WHERE organisation_id=$1 AND id=$2`, org, id)
}
func (h *Handler) accessIssueAllowed(ctx context.Context, tx pgx.Tx, j hospitality.AccessJob, c hospitality.AccessConfiguration) (bool, error) {
	var valid bool
	if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stay_records s JOIN room_assignments a ON a.organisation_id=s.organisation_id AND a.id=s.assignment_id JOIN room_status_current status ON status.organisation_id=a.organisation_id AND status.room_id=a.room_id JOIN reservations r ON r.organisation_id=s.organisation_id AND r.id=s.reservation_id WHERE s.organisation_id=$1 AND s.id=$2 AND s.state='ACTIVE' AND a.id=$3 AND a.state='OCCUPIED' AND status.state='OCCUPIED' AND r.state='CHECKED_IN' AND r.allocation_state='ACTIVE') AND $4=(SELECT id FROM hospitality_access_configurations WHERE organisation_id=$1 AND room_id=$5 ORDER BY version DESC LIMIT 1)`, j.OrganisationID, j.Request.StayID, j.Request.AssignmentID, c.ID, c.RoomID).Scan(&valid); e != nil {
		return false, e
	}
	if !valid || c.Method == "DISABLED" || !h.Auth.Service.Now().Before(c.ExpiresAt) || h.Auth.Service.Now().Before(j.Request.StartsAt) || !h.Auth.Service.Now().Before(j.Request.EndsAt) {
		return false, nil
	}
	allowed, e := h.currentRecordedPMSAuthority(ctx, tx, c.OrganisationID, c.PropertyID, c.AuthorizingMembershipID, c.AuthorizingRoleID, "pms:write")
	if e != nil || !allowed {
		return false, e
	}
	allowed, e = h.currentRecordedPMSAuthority(ctx, tx, c.OrganisationID, c.PropertyID, c.AuthorizingMembershipID, c.AuthorizingRoleID, "room_status:inspect")
	if e != nil || !allowed {
		return false, e
	}
	if e = h.commercialGate(ctx, tx, tenant{Organisation: c.OrganisationID, State: "ACTIVE"}, c.PropertyID); e != nil {
		var app *apperrors.Error
		if errors.As(e, &app) && (app.Status == 403 || app.Status == 409 || app.Status == 503) {
			return false, nil
		}
		return false, e
	}
	return true, nil
}
func (h *Handler) queuePMSAccess(ctx context.Context, tx pgx.Tx, t tenant, stay, assignment ids.ID, operation, reason string) (ids.ID, error) {
	var existing ids.ID
	e := tx.QueryRow(ctx, `SELECT id FROM access_grant_references WHERE organisation_id=$1 AND assignment_id=$2 AND operation=$3`, t.Organisation, assignment, operation).Scan(&existing)
	if e == nil {
		return existing, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return ids.Nil, e
	}
	id := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO access_grant_references(id,organisation_id,property_id,stay_id,assignment_id,operation,state) SELECT $1,$2,property_id,$3,$4,$5,'UNKNOWN' FROM stay_records WHERE organisation_id=$2 AND id=$3`, id, t.Organisation, stay, assignment, operation); e != nil {
		return ids.Nil, e
	}
	a, e := hospitality.GetAccessReference(ctx, tx, t.Organisation, id)
	if e != nil {
		return ids.Nil, e
	}
	prepared, e := h.preparePMSAccess(ctx, tx, t, a)
	if e != nil {
		return ids.Nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, a.PropertyID, id, "access_reference", "ACCESS_REQUEST_RECORDED", "hospitality.access.updated.v1", a.Version, map[string]any{"stay_id": stay, "assignment_id": assignment, "operation": operation, "configured": prepared, "state": "UNKNOWN", "reason": reason, "actor_service": serviceActor(t)}); e != nil {
		return ids.Nil, e
	}
	if prepared && operation == "REVOKE" {
		var account ids.ID
		if e = tx.QueryRow(ctx, `SELECT c.created_by FROM hospitality_access_jobs j JOIN hospitality_access_configurations c ON c.organisation_id=j.organisation_id AND c.id=j.configuration_id WHERE j.organisation_id=$1 AND j.id=$2`, t.Organisation, id).Scan(&account); e != nil {
			return ids.Nil, e
		}
		if e = h.queueNotice(ctx, tx, account, "Stay access revocation requires confirmation", "The original physical key or device credential requires actual return/revocation evidence. An unknown result remains unknown.", true, "access-reference:"+id.String(), "stay:"+stay.String(), "assignment:"+assignment.String()); e != nil {
			return ids.Nil, e
		}
	}
	return id, nil
}
func (h *Handler) preparePMSAccess(ctx context.Context, tx pgx.Tx, t tenant, a hospitality.AccessReference) (bool, error) {
	physical, e := hospitality.GetAssignment(ctx, tx, t.Organisation, a.AssignmentID)
	if e != nil {
		return false, e
	}
	var config hospitality.AccessConfiguration
	var request smartaccess.Request
	if a.Operation == "REVOKE" {
		issue, e := hospitality.Load[hospitality.AccessJob](ctx, tx, `SELECT to_jsonb(j) FROM hospitality_access_jobs j JOIN access_grant_references r ON r.organisation_id=j.organisation_id AND r.id=j.id WHERE r.organisation_id=$1 AND r.assignment_id=$2 AND r.operation='ISSUE'`, t.Organisation, a.AssignmentID)
		if isHospitalityMissing(e) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		config, e = hospitality.Load[hospitality.AccessConfiguration](ctx, tx, `SELECT to_jsonb(c) FROM hospitality_access_configurations c WHERE organisation_id=$1 AND id=$2`, t.Organisation, issue.ConfigurationID)
		if e != nil {
			return false, e
		}
		request = issue.Request
		request.IssueID = issue.ID
		request.ID = a.ID
		request.Operation = "REVOKE"
	} else {
		config, e = hospitality.LatestAccessConfiguration(ctx, tx, t.Organisation, physical.RoomID)
		if isHospitalityMissing(e) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if config.Method == "DISABLED" || !h.Auth.Service.Now().Before(config.ExpiresAt) {
			return false, nil
		}
		var checkout, zone string
		var checkedIn time.Time
		if e = tx.QueryRow(ctx, `SELECT r.policy_snapshot->>'check_out_local',r.policy_snapshot->>'acceptance_timezone',s.checked_in_at FROM stay_records s JOIN reservations r ON r.organisation_id=s.organisation_id AND r.id=s.reservation_id WHERE s.organisation_id=$1 AND s.id=$2`, t.Organisation, a.StayID).Scan(&checkout, &zone, &checkedIn); e != nil {
			return false, e
		}
		end, e := hospitality.LocalInstant(physical.DateTo, checkout, zone)
		if e != nil {
			return false, e
		}
		if config.ExpiresAt.Before(end) {
			end = config.ExpiresAt
		}
		start := h.Auth.Service.Now().UTC()
		if checkedIn.After(start) {
			start = checkedIn
		}
		if !end.After(start) {
			return false, nil
		}
		request = smartaccess.Request{ID: a.ID, ConfigurationID: config.ID, Device: smartaccess.Device{OrganisationID: t.Organisation, PropertyID: a.PropertyID, RoomID: physical.RoomID, Provider: config.Provider, DeviceReference: config.DeviceReference}, AccountReference: config.AccountReference, StayID: a.StayID, AssignmentID: a.AssignmentID, Operation: a.Operation, StartsAt: start, EndsAt: end}
	}
	state := "READY"
	if config.Method != "SMART_DEVICE" {
		state = "WAITING_MANUAL"
	}
	_, e = tx.Exec(ctx, `INSERT INTO hospitality_access_jobs(id,organisation_id,property_id,configuration_id,request,request_digest,idempotency_key,delivery_state,available_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, a.ID, t.Organisation, a.PropertyID, config.ID, marshalCommercial(request), security.SHA256Hex(marshalCommercial(request)), "pms-access:"+a.ID.String()+":v1", state, h.Auth.Service.Now().UTC())
	return e == nil, e
}
func isHospitalityMissing(e error) bool {
	var a *apperrors.Error
	return errors.As(e, &a) && a.Status == 404
}

func (h *Handler) pmsAccessView(ctx context.Context, q database.Querier, a hospitality.AccessReference) (PMSAccessView, error) {
	if _, ok := q.(pgx.Tx); !ok {
		var out PMSAccessView
		e := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, a.OrganisationID); e != nil {
				return e
			}
			current, e := hospitality.GetAccessReference(ctx, tx, a.OrganisationID, a.ID)
			if e != nil {
				return e
			}
			out, e = h.pmsAccessView(ctx, tx, current)
			return e
		})
		return out, e
	}
	out := PMSAccessView{Reference: a, EffectiveState: "UNKNOWN"}
	j, e := h.loadAccessJob(ctx, q, a.OrganisationID, a.ID)
	if isHospitalityMissing(e) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	c, e := hospitality.Load[hospitality.AccessConfiguration](ctx, q, `SELECT to_jsonb(c) FROM hospitality_access_configurations c WHERE organisation_id=$1 AND id=$2`, a.OrganisationID, j.ConfigurationID)
	if e != nil {
		return out, e
	}
	out.Command = &PMSAccessCommandView{ConfigurationID: c.ID, Method: c.Method, RequestDigest: j.RequestDigest, DeliveryState: j.DeliveryState, StartsAt: j.Request.StartsAt, EndsAt: j.Request.EndsAt, LastErrorCode: j.LastErrorCode}
	if a.Operation == "REVOKE" {
		out.RevocationConfirmed = a.State == "CONFIRMED"
		if out.RevocationConfirmed {
			out.EffectiveState = "REVOKED"
		}
		return out, nil
	}
	out.DeliveryConfirmed = a.State == "CONFIRMED"
	var revoke, revoked bool
	e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access_grant_references WHERE organisation_id=$1 AND assignment_id=$2 AND operation='REVOKE'),EXISTS(SELECT 1 FROM access_grant_references WHERE organisation_id=$1 AND assignment_id=$2 AND operation='REVOKE' AND state='CONFIRMED')`, a.OrganisationID, a.AssignmentID).Scan(&revoke, &revoked)
	if e != nil {
		return out, e
	}
	if revoked {
		out.EffectiveState = "REVOKED"
		out.RevocationConfirmed = true
		return out, nil
	}
	if revoke {
		out.EffectiveState = "REVOCATION_REQUIRED"
		return out, nil
	}
	if !out.DeliveryConfirmed {
		return out, nil
	}
	valid, e := h.accessIssueAllowed(ctx, q.(pgx.Tx), j, c)
	if e != nil {
		return out, e
	}
	if !valid {
		out.EffectiveState = "REVOCATION_REQUIRED"
	} else {
		out.EffectiveState = "RECORDED_ACTIVE"
	}
	return out, nil
}
func (h *Handler) pmsAccessList(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "pms:read", r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := h.Auth.Service.Pool.Query(r.Context(), `SELECT id FROM access_grant_references WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	refs := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			break
		}
		refs = append(refs, id)
	}
	re := rows.Err()
	rows.Close()
	if e == nil {
		e = re
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out := []PMSAccessView{}
	for _, id := range refs {
		a, err := hospitality.GetAccessReference(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
		if err != nil {
			e = err
			break
		}
		v, err := h.pmsAccessView(r.Context(), h.Auth.Service.Pool, a)
		if err != nil {
			e = err
			break
		}
		out = append(out, v)
	}
	if e == nil {
		e = audit(r.Context(), h.Auth.Service.Pool, t, "ACCESS_REFERENCES_READ", "access_references", t.Organisation, map[string]any{"property_ids": properties, "limit": limit, "offset": offset, "count": len(out)})
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, 200, map[string]any{"items": out, "limit": limit, "offset": offset}, e)
}
func (h *Handler) accessTarget(r *http.Request, cap string, step bool) (tenant, hospitality.AccessReference, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, hospitality.AccessReference{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, hospitality.AccessReference{}, e
	}
	a, e := hospitality.GetAccessReference(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		return t, a, e
	}
	_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, cap, "PROPERTY", a.PropertyID, step)
	return t, a, e
}
func (h *Handler) pmsAccessGet(w http.ResponseWriter, r *http.Request) {
	t, a, e := h.accessTarget(r, "pms:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := h.pmsAccessView(r.Context(), h.Auth.Service.Pool, a)
	if e == nil {
		e = audit(r.Context(), h.Auth.Service.Pool, t, "ACCESS_REFERENCE_READ", "access_reference", a.ID, map[string]any{"property_id": a.PropertyID, "operation": a.Operation})
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, 200, out, e)
}
