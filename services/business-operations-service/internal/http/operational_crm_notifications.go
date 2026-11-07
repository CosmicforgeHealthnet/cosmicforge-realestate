package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Guest notifications are operational alerts derived from authoritative
// Business transitions. Message content stays with the approved Notification
// Service template; CF Meet remains the human conversation authority.
type guestNotificationKind struct {
	Recipient        string
	Consents         []string
	MarketingCapable bool
}

var providerTemplateKey = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,99}$`)

var guestNotificationKinds = map[string]guestNotificationKind{
	"WAITLIST_OFFER":           {Recipient: "GUEST", Consents: []string{"OPERATIONAL_CONTACT"}},
	"WAITLIST_EXPIRED":         {Recipient: "GUEST", Consents: []string{"OPERATIONAL_CONTACT"}},
	"RESERVATION_CHANGE":       {Recipient: "GUEST", Consents: []string{"OPERATIONAL_CONTACT"}},
	"ARRIVAL_NOTICE":           {Recipient: "GUEST", Consents: []string{"OPERATIONAL_CONTACT"}},
	"STAY_NOTICE":              {Recipient: "GUEST", Consents: []string{"OPERATIONAL_CONTACT"}},
	"SERVICE_RECOVERY_UPDATE":  {Recipient: "GUEST", Consents: []string{"OPERATIONAL_CONTACT"}},
	"COMPENSATION_STATUS":      {Recipient: "GUEST", Consents: []string{"OPERATIONAL_CONTACT"}},
	"LOYALTY_ENROLLED":         {Recipient: "GUEST", Consents: []string{"OPERATIONAL_CONTACT", "LOYALTY"}, MarketingCapable: true},
	"LOYALTY_REWARD":           {Recipient: "GUEST", Consents: []string{"OPERATIONAL_CONTACT", "LOYALTY"}, MarketingCapable: true},
	"CORPORATE_BOOKING_UPDATE": {Recipient: "CORPORATE"},
}

type CRMNotificationTemplateCommand struct {
	PropertyID          ids.ID   `json:"property_id"`
	Kind                string   `json:"kind"`
	ProviderTemplateKey string   `json:"provider_template_key"`
	Purpose             string   `json:"purpose"`
	Channels            []string `json:"channels"`
	ApprovalID          ids.ID   `json:"approval_id,omitempty"`
	Reason              string   `json:"reason"`
}
type CRMNotificationTemplate struct {
	ID                  ids.ID     `json:"id"`
	PropertyID          ids.ID     `json:"property_id"`
	Kind                string     `json:"kind"`
	Version             int64      `json:"version"`
	ProviderTemplateKey string     `json:"provider_template_key"`
	Purpose             string     `json:"purpose"`
	Channels            []string   `json:"channels"`
	State               string     `json:"state"`
	ApprovalID          ids.ID     `json:"approval_id"`
	CreatedAt           time.Time  `json:"created_at"`
	RetiredAt           *time.Time `json:"retired_at"`
}
type CRMReasonCommand struct {
	Reason string `json:"reason"`
}
type CRMNotificationSuppressionCommand struct {
	Scope             string `json:"scope"`
	Kind              string `json:"kind,omitempty"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
}
type CRMNotificationSuppression struct {
	ID                ids.ID     `json:"id"`
	PropertyID        ids.ID     `json:"property_id"`
	GuestID           ids.ID     `json:"guest_id"`
	Scope             string     `json:"scope"`
	Kind              *string    `json:"kind"`
	State             string     `json:"state"`
	EvidenceReference string     `json:"evidence_reference"`
	Version           int64      `json:"version"`
	CreatedAt         time.Time  `json:"created_at"`
	LiftedAt          *time.Time `json:"lifted_at"`
}
type CRMWaitlistGuestBinding struct {
	GuestID           ids.ID `json:"guest_id"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
}
type CRMWaitlistGuestBound struct {
	ID         ids.ID `json:"id"`
	EntryID    ids.ID `json:"entry_id"`
	GuestID    ids.ID `json:"guest_id"`
	PropertyID ids.ID `json:"property_id"`
}
type CRMNotificationJob struct {
	ID                     ids.ID          `json:"id"`
	Organisation           ids.ID          `json:"organisation_id"`
	PropertyID             ids.ID          `json:"property_id"`
	RecipientKind          string          `json:"recipient_kind"`
	GuestID                *ids.ID         `json:"guest_id"`
	CorporateID            *ids.ID         `json:"corporate_id"`
	ResolvedGuestID        *ids.ID         `json:"resolved_guest_id"`
	SourceKind             string          `json:"source_kind"`
	SourceID               ids.ID          `json:"source_id"`
	Kind                   string          `json:"kind"`
	Purpose                string          `json:"purpose"`
	ConsentPurposes        []string        `json:"consent_purposes"`
	State                  string          `json:"state"`
	TemplateID             *ids.ID         `json:"template_id"`
	TemplateVersion        *int64          `json:"template_version"`
	Channel                *string         `json:"channel"`
	Variables              json.RawMessage `json:"variables"`
	Attempts               int             `json:"attempts"`
	MaxAttempts            int             `json:"max_attempts"`
	AvailableAt            time.Time       `json:"available_at"`
	ExpiresAt              time.Time       `json:"expires_at"`
	LeaseOwner             *string         `json:"lease_owner"`
	LeaseGeneration        int64           `json:"lease_generation"`
	ProviderNotificationID *ids.ID         `json:"provider_notification_id"`
	ProviderSuppressed     json.RawMessage `json:"provider_suppressed"`
	DeliveryStatus         string          `json:"delivery_status"`
	OutcomeReason          *string         `json:"outcome_reason"`
	LastError              *string         `json:"last_error"`
	AcceptedAt             *time.Time      `json:"accepted_at"`
	Version                int64           `json:"version"`
	CreatedAt              time.Time       `json:"created_at"`
}
type CRMNotificationJobDetail struct {
	Job      CRMNotificationJob `json:"job"`
	Attempts []json.RawMessage  `json:"attempts"`
	// Notification Service exposes acceptance only; delivery and read state
	// remain unknown to Business and are never inferred.
	DeliveryConfirmation string `json:"delivery_confirmation"`
}

func (h *Handler) registerCRMNotifications(r chi.Router) {
	r.Get("/guest-notification-templates", h.crmNotificationTemplates)
	r.Post("/guest-notification-templates", h.crmNotificationTemplateCreate)
	r.Post("/guest-notification-templates/{id}/retire", h.crmNotificationTemplateRetire)
	r.Get("/guest-notifications", h.crmNotificationJobs)
	r.Get("/guest-notifications/{id}", h.crmNotificationJobGet)
	r.Post("/guest-notifications/{id}/cancel", h.crmNotificationJobCancel)
	r.Post("/guest-notifications/{id}/retry", h.crmNotificationJobRetry)
	r.Get("/guests/{id}/notification-suppressions", h.crmNotificationSuppressions)
	r.Post("/guests/{id}/notification-suppressions", h.crmNotificationSuppressionCreate)
	r.Post("/guests/{id}/notification-suppressions/{suppression_id}/lift", h.crmNotificationSuppressionLift)
	r.Post("/waitlist/{id}/guest-binding", h.crmWaitlistGuestBind)
}

func validChannels(channels []string) bool {
	if len(channels) < 1 || len(channels) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range channels {
		if !slices.Contains([]string{"EMAIL", "SMS", "PUSH"}, c) || seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}

func (h *Handler) crmNotificationTemplates(w http.ResponseWriter, r *http.Request) {
	property, e := queryCommercialID(r, "property_id")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "guests:read", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM guest_notification_templates x WHERE organisation_id=$1 AND property_id=$2 ORDER BY kind,version DESC LIMIT 200`, t.Organisation, property)
	reply(w, r, 200, map[string]any{"items": items}, e)
}

func (h *Handler) crmNotificationTemplateCreate(w http.ResponseWriter, r *http.Request) {
	var in CRMNotificationTemplateCommand
	if !decode(w, r, &in) {
		return
	}
	kind, known := guestNotificationKinds[in.Kind]
	if in.PropertyID.ValidateV7() != nil || !known || !validChannels(in.Channels) || !validStaffReason(in.Reason) || !slices.Contains([]string{"OPERATIONAL", "MARKETING"}, in.Purpose) || in.Purpose == "MARKETING" && !kind.MarketingCapable || !providerTemplateKey.MatchString(in.ProviderTemplateKey) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, supported kind and purpose, approved provider template key, channels and reason required."))
		return
	}
	h.execute(w, r, "crm:notification-template:"+in.PropertyID.String()+":"+in.Kind, in, "guest_privacy:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_GUEST_NOTIFICATION_TEMPLATE", target.Scope, in.ApprovalID, map[string]any{"property_id": in.PropertyID, "command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		var version int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0)+1 FROM guest_notification_templates WHERE organisation_id=$1 AND property_id=$2 AND kind=$3`, t.Organisation, in.PropertyID, in.Kind).Scan(&version); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE guest_notification_templates SET state='RETIRED',retired_by=$4,retired_at=now() WHERE organisation_id=$1 AND property_id=$2 AND kind=$3 AND state='ACTIVE'`, t.Organisation, in.PropertyID, in.Kind, t.Account); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO guest_notification_templates(id,organisation_id,property_id,kind,version,provider_template_key,purpose,channels,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, t.Organisation, in.PropertyID, in.Kind, version, in.ProviderTemplateKey, in.Purpose, in.Channels, in.ApprovalID, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "guest_notification_template", "GUEST_NOTIFICATION_TEMPLATE_APPROVED", "guest.notification_template.updated.v1", version, map[string]any{"kind": in.Kind, "purpose": in.Purpose, "channels": in.Channels, "approval_id": in.ApprovalID}); e != nil {
			return 0, nil, e
		}
		out, e := crmRecord[CRMNotificationTemplate](ctx, tx, `SELECT to_jsonb(x) FROM guest_notification_templates x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 201, out, e
	})
}

func (h *Handler) crmNotificationTemplateRetire(w http.ResponseWriter, r *http.Request) {
	var in CRMReasonCommand
	if !decode(w, r, &in) {
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Retirement reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := crmRecord[CRMNotificationTemplate](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM guest_notification_templates x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	// Retirement only reduces delivery authority, so it needs no approval.
	h.execute(w, r, "crm:notification-template-retire:"+id.String(), in, "guest_privacy:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		tag, e := tx.Exec(ctx, `UPDATE guest_notification_templates SET state='RETIRED',retired_by=$3,retired_at=now() WHERE organisation_id=$1 AND id=$2 AND state='ACTIVE'`, t.Organisation, id, t.Account)
		if e != nil {
			return 0, nil, e
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("TEMPLATE_STATE", "Only an active template can be retired.")
		}
		if e = h.commercialCommit(ctx, tx, t, pre.PropertyID, id, "guest_notification_template", "GUEST_NOTIFICATION_TEMPLATE_RETIRED", "guest.notification_template.updated.v1", pre.Version, map[string]any{"kind": pre.Kind, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := crmRecord[CRMNotificationTemplate](ctx, tx, `SELECT to_jsonb(x) FROM guest_notification_templates x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 200, out, e
	})
}

func (h *Handler) crmNotificationJobs(w http.ResponseWriter, r *http.Request) {
	property, e := queryCommercialID(r, "property_id")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "guests:read", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	q := r.URL.Query()
	var family any
	if raw := q.Get("guest_id"); raw != "" {
		guest, e := ids.ParseV7(raw)
		if e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid guest filter."))
			return
		}
		members, e := guestFamily(r.Context(), h.Auth.Service.Pool, t.Organisation, guest)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		family = members
	}
	var source any
	if raw := q.Get("source_id"); raw != "" {
		id, e := ids.ParseV7(raw)
		if e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid source filter."))
			return
		}
		source = id
	}
	state, sourceKind := q.Get("state"), q.Get("source_kind")
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(j) FROM guest_notification_jobs j WHERE organisation_id=$1 AND property_id=$2 AND ($3::uuid[] IS NULL OR guest_id=ANY($3::uuid[])) AND ($4::uuid IS NULL OR source_id=$4) AND ($5='' OR state=$5) AND ($6='' OR source_kind=$6) ORDER BY created_at DESC,id DESC LIMIT $7 OFFSET $8`, t.Organisation, property, family, source, state, sourceKind, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}

func (h *Handler) notificationJobTarget(r *http.Request, capability string) (tenant, CRMNotificationJob, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, CRMNotificationJob{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, CRMNotificationJob{}, e
	}
	j, e := crmRecord[CRMNotificationJob](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(j) FROM guest_notification_jobs j WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		return t, j, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", j.PropertyID, false)
	return t, j, e
}

func (h *Handler) crmNotificationJobGet(w http.ResponseWriter, r *http.Request) {
	t, j, e := h.notificationJobTarget(r, "guests:read")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	attempts, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(a) FROM guest_notification_attempts a WHERE organisation_id=$1 AND job_id=$2 ORDER BY created_at,id LIMIT 100`, t.Organisation, j.ID)
	reply(w, r, 200, CRMNotificationJobDetail{Job: j, Attempts: attempts, DeliveryConfirmation: "UNKNOWN"}, e)
}

func (h *Handler) crmNotificationJobOperate(w http.ResponseWriter, r *http.Request, retry bool) {
	var in CRMVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current job version and reason required."))
		return
	}
	_, pre, e := h.notificationJobTarget(r, "guests:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	op := "cancel"
	if retry {
		op = "retry"
	}
	h.execute(w, r, "crm:notification-job:"+op+":"+pre.ID.String(), in, "guests:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		j, e := crmRecord[CRMNotificationJob](ctx, tx, `SELECT to_jsonb(j) FROM guest_notification_jobs j WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if j.Version != in.Version {
			return 0, nil, apperrors.Conflict("NOTIFICATION_JOB_VERSION", "The notification job changed; reload it.")
		}
		var outcome, operation string
		if retry {
			if j.State != "FAILED" && j.State != "BLOCKED" || !now.Before(j.ExpiresAt) {
				return 0, nil, apperrors.Conflict("NOTIFICATION_JOB_STATE", "Only an unexpired failed or blocked job can be retried.")
			}
			if _, e = tx.Exec(ctx, `UPDATE guest_notification_jobs SET state='RETRY',available_at=$3,max_attempts=least(20,attempts+4),outcome_reason=NULL,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, j.ID, now); e != nil {
				return 0, nil, e
			}
			outcome, operation = "OPERATOR_RETRY", "GUEST_NOTIFICATION_RETRY_REQUESTED"
		} else {
			if !slices.Contains([]string{"PENDING", "RETRY", "FAILED", "BLOCKED"}, j.State) {
				return 0, nil, apperrors.Conflict("NOTIFICATION_JOB_STATE", "In-flight or completed notifications cannot be cancelled.")
			}
			if _, e = tx.Exec(ctx, `UPDATE guest_notification_jobs SET state='CANCELLED',outcome_reason='OPERATOR_CANCELLED',lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, j.ID); e != nil {
				return 0, nil, e
			}
			outcome, operation = "OPERATOR_CANCEL", "GUEST_NOTIFICATION_CANCELLED"
		}
		if _, e = tx.Exec(ctx, `INSERT INTO guest_notification_attempts(id,organisation_id,job_id,attempt,outcome,detail,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, ids.New(), t.Organisation, j.ID, j.Attempts, outcome, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, j.PropertyID, j.ID, "guest_notification_job", operation, "guest.notification.updated.v1", j.Version+1, map[string]any{"kind": j.Kind, "source_kind": j.SourceKind, "source_id": j.SourceID, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := crmRecord[CRMNotificationJob](ctx, tx, `SELECT to_jsonb(j) FROM guest_notification_jobs j WHERE organisation_id=$1 AND id=$2`, t.Organisation, j.ID)
		return 200, out, e
	})
}
func (h *Handler) crmNotificationJobCancel(w http.ResponseWriter, r *http.Request) {
	h.crmNotificationJobOperate(w, r, false)
}
func (h *Handler) crmNotificationJobRetry(w http.ResponseWriter, r *http.Request) {
	h.crmNotificationJobOperate(w, r, true)
}

func (h *Handler) crmNotificationSuppressions(w http.ResponseWriter, r *http.Request) {
	t, g, property, e := h.crmGuestTarget(r, "guests:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(s) FROM guest_notification_suppressions s WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY created_at DESC,id DESC LIMIT 100`, t.Organisation, g.ID, property)
	reply(w, r, 200, map[string]any{"items": items}, e)
}

func (h *Handler) crmNotificationSuppressionCreate(w http.ResponseWriter, r *http.Request) {
	var in CRMNotificationSuppressionCommand
	if !decode(w, r, &in) {
		return
	}
	_, kindKnown := guestNotificationKinds[in.Kind]
	if !slices.Contains([]string{"ALL", "MARKETING", "KIND"}, in.Scope) || (in.Scope == "KIND") != (in.Kind != "") || in.Kind != "" && !kindKnown || !crm.Text(in.EvidenceReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Suppression scope, optional kind, evidence and reason required."))
		return
	}
	_, g, property, e := h.crmGuestTarget(r, "guests:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:notification-suppression:"+g.ID.String()+":"+property.String(), in, "guests:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		if e = crm.RequireGuestScope(ctx, tx, t.Organisation, property, g.ID); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		var kind any
		if in.Kind != "" {
			kind = in.Kind
		}
		if _, e = tx.Exec(ctx, `INSERT INTO guest_notification_suppressions(id,organisation_id,property_id,guest_id,scope,kind,evidence_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, t.Organisation, property, g.ID, in.Scope, kind, in.EvidenceReference, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, property, g.ID, id, "guest_notification_suppression", 1, "GUEST_NOTIFICATION_SUPPRESSED", map[string]any{"scope": in.Scope, "kind": in.Kind, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := crmRecord[CRMNotificationSuppression](ctx, tx, `SELECT to_jsonb(s) FROM guest_notification_suppressions s WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 201, out, e
	})
}

func (h *Handler) crmNotificationSuppressionLift(w http.ResponseWriter, r *http.Request) {
	var in CRMVersionCommand
	if !decode(w, r, &in) {
		return
	}
	suppression, e := ids.ParseV7(chi.URLParam(r, "suppression_id"))
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Suppression, current version and reason required."))
		return
	}
	_, g, property, e := h.crmGuestTarget(r, "guest_privacy:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:notification-suppression-lift:"+suppression.String(), in, "guest_privacy:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		tag, e := tx.Exec(ctx, `UPDATE guest_notification_suppressions SET state='LIFTED',lift_reason=$5,lifted_by=$6,lifted_at=now(),version=version+1 WHERE organisation_id=$1 AND id=$2 AND guest_id=$3 AND version=$4 AND state='ACTIVE'`, t.Organisation, suppression, g.ID, in.Version, in.Reason, t.Account)
		if e != nil {
			return 0, nil, e
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("SUPPRESSION_STATE", "Only the current active suppression for this guest can be lifted.")
		}
		if e = h.crmGuestCommit(ctx, tx, t, property, g.ID, suppression, "guest_notification_suppression", in.Version+1, "GUEST_NOTIFICATION_SUPPRESSION_LIFTED", map[string]any{"reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := crmRecord[CRMNotificationSuppression](ctx, tx, `SELECT to_jsonb(s) FROM guest_notification_suppressions s WHERE organisation_id=$1 AND id=$2`, t.Organisation, suppression)
		return 200, out, e
	})
}

func (h *Handler) crmWaitlistGuestBind(w http.ResponseWriter, r *http.Request) {
	var in CRMWaitlistGuestBinding
	if !decode(w, r, &in) {
		return
	}
	entry, e := pathCommercialID(r)
	if e != nil || in.GuestID.ValidateV7() != nil || !crm.Text(in.EvidenceReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Waitlist entry, registered guest, evidence and reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := crmRecord[ReservationWaitlist](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(w) FROM waitlist_entries w WHERE organisation_id=$1 AND id=$2`, t.Organisation, entry)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:waitlist-guest:"+entry.String(), in, "reservations:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "guests:read", "PROPERTY", pre.PropertyID, false); e != nil {
			return 0, nil, e
		}
		v, e := crmRecord[ReservationWaitlist](ctx, tx, `SELECT to_jsonb(w) FROM waitlist_entries w WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, entry)
		if e != nil {
			return 0, nil, e
		}
		if v.State != "WAITING" && v.State != "OFFERED" {
			return 0, nil, apperrors.Conflict("WAITLIST_STATE", "Only an open waitlist entry can be bound to a guest.")
		}
		if e = crm.RequireGuest(ctx, tx, t.Organisation, v.PropertyID, in.GuestID); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO waitlist_guest_bindings(id,organisation_id,property_id,entry_id,guest_id,evidence_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, v.PropertyID, v.ID, in.GuestID, in.EvidenceReference, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, v.PropertyID, in.GuestID, id, "waitlist_guest_binding", 1, "WAITLIST_GUEST_BOUND", map[string]any{"entry_id": v.ID, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 201, CRMWaitlistGuestBound{ID: id, EntryID: v.ID, GuestID: in.GuestID, PropertyID: v.PropertyID}, nil
	})
}
