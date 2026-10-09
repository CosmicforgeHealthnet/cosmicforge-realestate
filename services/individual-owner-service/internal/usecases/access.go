package usecases

import (
	"context"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/smartaccess"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Smart access and remote property controls (§13.30). Access grants are
// owner-domain authority: each grant serves one business relationship
// (viewing, tenancy, stay, manager mandate, maintenance job), lives inside
// that relationship's window and ends automatically when it ends. Device
// adapters only provision and revoke credentials; secrets are never stored.
// A compromised credential is revoked immediately with a security
// notification. Physical keys, cards and lockboxes are owner-issued grants.

const AggAccessGrant = "access_grant"

// Owner notification templates.
const (
	TemplateAccessRequested   = "owner.access_requested"   // principal_label
	TemplateAccessCompromised = "owner.access_compromised" // device_label
)

const maxProviderGrant = 14 * 24 * time.Hour

var smartDevices = []string{"SMART_LOCK", "KEYPAD"}

// AccessDeviceInput registers an access device or key set.
type AccessDeviceInput struct {
	PropertyID        ids.ID  `json:"property_id" v:"required"`
	UnitID            *ids.ID `json:"unit_id,omitempty"`
	DeviceType        string  `json:"device_type" v:"required,enum=SMART_LOCK|KEYPAD|LOCKBOX|PHYSICAL_KEY|KEY_CARD"`
	Label             string  `json:"label" v:"required,max=120"`
	ProviderDeviceRef *string `json:"provider_device_ref,omitempty" v:"max=128" doc:"SMART_LOCK/KEYPAD: device id at the smart-access provider"`
}

// AccessDeviceView is an access device.
type AccessDeviceView struct {
	ID                ids.ID  `json:"id"`
	PropertyID        ids.ID  `json:"property_id"`
	UnitID            *ids.ID `json:"unit_id"`
	DeviceType        string  `json:"device_type"`
	Label             string  `json:"label"`
	ProviderDeviceRef *string `json:"provider_device_ref"`
	Status            string  `json:"status"`
}

func accessDeviceView(d db.AccessDevice) AccessDeviceView {
	return AccessDeviceView{ID: d.ID, PropertyID: d.PropertyID, UnitID: d.UnitID, DeviceType: d.DeviceType, Label: d.Label, ProviderDeviceRef: d.ProviderDeviceRef,
		Status: d.Status}
}

// RegisterAccessDevice adds a device; smart devices need a smart-access provider.
func (s *Service) RegisterAccessDevice(ctx context.Context, subj authz.Subject, in AccessDeviceInput) (AccessDeviceView, error) {
	if err := schema.Validate(in); err != nil {
		return AccessDeviceView{}, err
	}
	smart := slices.Contains(smartDevices, in.DeviceType)
	if smart && s.SmartAccess == nil {
		return AccessDeviceView{}, unavailable("Smart locks and keypads", "a smart-access provider")
	}
	if smart != (in.ProviderDeviceRef != nil) {
		return AccessDeviceView{}, apperrors.InvalidRequest("provider_device_ref is required for smart devices and only for them.")
	}
	var out AccessDeviceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapAccessGrant), in.PropertyID); err != nil {
			return err
		}
		if in.UnitID != nil {
			if _, err := s.q.GetUnit(ctx, mustTx(ctx), db.GetUnitParams{ID: *in.UnitID, PropertyID: in.PropertyID}); err != nil {
				return apperrors.InvalidRequest("unit_id does not belong to the property.")
			}
		}
		d, err := s.q.InsertAccessDevice(ctx, mustTx(ctx), db.InsertAccessDeviceParams{ID: ids.New(), PropertyID: in.PropertyID, UnitID: in.UnitID,
			DeviceType: in.DeviceType, Label: in.Label, ProviderDeviceRef: in.ProviderDeviceRef, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = accessDeviceView(d)
		return s.record(ctx, "access.device.register", AggProperty, in.PropertyID, map[string]any{"device_id": d.ID, "device_type": in.DeviceType}, nil)
	})
	return out, err
}

// ListAccessDevices lists a property's devices.
func (s *Service) ListAccessDevices(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]AccessDeviceView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapAccessGrant), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAccessDevices(ctx, s.dbq(ctx), propertyID)
	out := make([]AccessDeviceView, 0, len(rows))
	for _, r := range rows {
		out = append(out, accessDeviceView(r))
	}
	return out, err
}

// GrantInput requests time-limited access for a business relationship.
type GrantInput struct {
	DeviceID     ids.ID    `json:"device_id" v:"required"`
	RelationType string    `json:"relation_type" v:"required,enum=viewing_request|tenancy|shortlet_booking|manager_assignment|maintenance_job"`
	RelationID   ids.ID    `json:"relation_id" v:"required"`
	StartsAt     time.Time `json:"starts_at" v:"required"`
	EndsAt       time.Time `json:"ends_at" v:"required" doc:"Must fall within the relationship's lawful period"`
}

// GrantView is an access grant (never a credential secret).
type GrantView struct {
	ID               ids.ID    `json:"id"`
	PropertyID       ids.ID    `json:"property_id"`
	DeviceID         ids.ID    `json:"device_id"`
	Purpose          string    `json:"purpose"`
	RelationType     string    `json:"relation_type"`
	RelationID       ids.ID    `json:"relation_id"`
	PrincipalDomain  string    `json:"principal_domain"`
	PrincipalRef     ids.ID    `json:"principal_ref"`
	PrincipalLabel   string    `json:"principal_label"`
	StartsAt         time.Time `json:"starts_at"`
	EndsAt           time.Time `json:"ends_at"`
	Status           string    `json:"status"`
	RequestedBy      string    `json:"requested_by"`
	CredentialIssued bool      `json:"credential_issued" doc:"A provider credential exists (delivered by the provider to the principal)"`
	EndReason        *string   `json:"end_reason"`
	Version          int64     `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
}

func grantView(g db.AccessGrant) GrantView {
	return GrantView{ID: g.ID, PropertyID: g.PropertyID, DeviceID: g.DeviceID, Purpose: g.Purpose, RelationType: g.RelationType, RelationID: g.RelationID,
		PrincipalDomain: g.PrincipalDomain, PrincipalRef: g.PrincipalRef, PrincipalLabel: g.PrincipalLabel, StartsAt: g.StartsAt, EndsAt: g.EndsAt,
		Status: g.Status, RequestedBy: g.RequestedBy, CredentialIssued: g.CredentialRef != nil, EndReason: g.EndReason, Version: g.Version, CreatedAt: g.CreatedAt}
}

// relation is the business relationship a grant serves.
type relation struct {
	propertyID      ids.ID
	purpose         string
	principalDomain string
	principalRef    ids.ID
	principalLabel  string
	from, until     *time.Time
	open            bool
}

func (s *Service) loadRelation(ctx context.Context, typ string, id ids.ID) (relation, error) {
	q := s.dbq(ctx)
	r := relation{}
	var status string
	var err error
	switch typ {
	case "viewing_request":
		var a, b time.Time
		err = q.QueryRow(ctx, `SELECT property_id, customer_ref_id, status, starts_at, ends_at FROM viewing_requests WHERE id = $1`, id).
			Scan(&r.propertyID, &r.principalRef, &status, &a, &b)
		a, b = a.Add(-30*time.Minute), b.Add(30*time.Minute)
		r.purpose, r.principalDomain, r.principalLabel, r.from, r.until, r.open = "VIEWING", string(platform.DomainMarketplace), "Viewing visitor", &a, &b, status == "CONFIRMED"
	case "tenancy":
		var start time.Time
		var end *time.Time
		var cust *ids.ID
		err = q.QueryRow(ctx, `SELECT property_id, customer_ref_id, tenant_display_name, status, start_date, end_date FROM tenancies WHERE id = $1`, id).
			Scan(&r.propertyID, &cust, &r.principalLabel, &status, &start, &end)
		r.purpose, r.principalDomain, r.principalRef, r.from = "TENANT", string(platform.DomainMarketplace), id, &start
		if cust != nil {
			r.principalRef = *cust
		} else {
			r.principalDomain = "offline-tenant" // tenant without a marketplace account: the tenancy identifies them
		}
		if end != nil {
			e := end.AddDate(0, 0, 1)
			r.until = &e
		}
		r.open = slices.Contains([]string{"PENDING_MOVE_IN", "ACTIVE", "RENEWAL_PENDING", "TERMINATION_PENDING", "MOVE_OUT_PENDING"}, status)
	case "shortlet_booking":
		var in, out time.Time
		err = q.QueryRow(ctx, `SELECT property_id, customer_ref_id, status, check_in, check_out FROM shortlet_bookings WHERE id = $1`, id).
			Scan(&r.propertyID, &r.principalRef, &status, &in, &out)
		e := out.AddDate(0, 0, 1)
		r.purpose, r.principalDomain, r.principalLabel, r.from, r.until, r.open = "GUEST", string(platform.DomainMarketplace), "Guest", &in, &e,
			status == "CONFIRMED" || status == "CHECKED_IN"
	case "manager_assignment":
		var a, b time.Time
		err = q.QueryRow(ctx, `SELECT property_id, manager_ref_id, manager_display_name, status, starts_at, ends_at FROM manager_assignments WHERE id = $1`, id).
			Scan(&r.propertyID, &r.principalRef, &r.principalLabel, &status, &a, &b)
		r.purpose, r.principalDomain, r.from, r.until, r.open = "MANAGER", string(platform.DomainPropertyManager), &a, &b, status == "ACTIVE" || status == "EXPIRING"
	case "maintenance_job":
		var provider *ids.ID
		var label string
		err = q.QueryRow(ctx, `SELECT m.property_id, j.provider_ref, q.provider_display_name, j.status FROM maintenance_jobs j
			JOIN maintenance_requests m ON m.id = j.request_id JOIN maintenance_quotes q ON q.id = j.quote_id WHERE j.id = $1`, id).
			Scan(&r.propertyID, &provider, &label, &status)
		r.purpose, r.principalDomain, r.principalRef, r.principalLabel = "PROVIDER", string(platform.DomainArtisan), id, label
		if provider != nil {
			r.principalRef = *provider
		} else {
			r.principalDomain = "offline-provider" // self-sourced provider: the job identifies them
		}
		r.open = slices.Contains([]string{"ASSIGNED", "IN_PROGRESS", "REWORK_REQUESTED"}, status)
	}
	if isNoRows(err) {
		return r, notFound("access_relation")
	}
	return r, err
}

// AccessGrantEvent is access.grant.revoked.v1 (also emitted on expiry).
type AccessGrantEvent struct {
	SchemaVersion   int       `json:"schema_version"`
	GrantID         ids.ID    `json:"grant_id"`
	PropertyID      ids.ID    `json:"property_id"`
	DeviceID        ids.ID    `json:"device_id"`
	Purpose         string    `json:"purpose"`
	RelationType    string    `json:"relation_type"`
	RelationID      ids.ID    `json:"relation_id"`
	PrincipalDomain string    `json:"principal_domain"`
	PrincipalRef    ids.ID    `json:"principal_ref"`
	Status          string    `json:"status" doc:"EXPIRED, REVOKED or COMPROMISED"`
	Reason          string    `json:"reason"`
	OccurredAt      time.Time `json:"occurred_at"`
}

func (s *Service) accessEvent(ctx context.Context, g db.AccessGrant, typ, actor string, note *string) error {
	return s.q.InsertAccessEvent(ctx, mustTx(ctx), db.InsertAccessEventParams{ID: ids.New(), GrantID: &g.ID, DeviceID: g.DeviceID, PropertyID: g.PropertyID,
		EventType: typ, Actor: actor, Note: note, OccurredAt: s.now()})
}

// grantTo transitions a grant, records history and, when it ends,
// publishes access.grant.revoked.v1.
func (s *Service) grantTo(ctx context.Context, id ids.ID, to, cmd, reason, actor string) (db.AccessGrant, error) {
	if _, _, err := s.transition(ctx, domain.AccessGrant, id, lifecycle.Change{To: to, Command: cmd, Reason: reason}); err != nil {
		return db.AccessGrant{}, err
	}
	tx := mustTx(ctx)
	if reason != "" {
		if err := s.q.SetGrantEndReason(ctx, tx, db.SetGrantEndReasonParams{ID: id, EndReason: &reason, UpdatedAt: s.now()}); err != nil {
			return db.AccessGrant{}, err
		}
	}
	g, err := s.q.GetAccessGrant(ctx, tx, id)
	if err != nil {
		return g, err
	}
	evType := map[string]string{"APPROVED": "APPROVED", "ISSUED": "ISSUED", "ACTIVE": "ACTIVATED", "EXPIRED": "EXPIRED", "REVOKED": "REVOKED",
		"COMPROMISED": "COMPROMISED"}[to]
	if err := s.accessEvent(ctx, g, evType, actor, strp(reason)); err != nil {
		return g, err
	}
	if slices.Contains([]string{"EXPIRED", "REVOKED", "COMPROMISED"}, to) {
		err = s.publish(ctx, "access.grant.revoked.v1", AggAccessGrant, id, AccessGrantEvent{SchemaVersion: 1, GrantID: g.ID, PropertyID: g.PropertyID,
			DeviceID: g.DeviceID, Purpose: g.Purpose, RelationType: g.RelationType, RelationID: g.RelationID, PrincipalDomain: g.PrincipalDomain,
			PrincipalRef: g.PrincipalRef, Status: to, Reason: reason, OccurredAt: s.now()})
	}
	return g, err
}

// CreateGrant creates a grant: owners approve their own grants; delegated
// managers' requests wait for the owner's remote approval.
func (s *Service) CreateGrant(ctx context.Context, subj authz.Subject, in GrantInput) (GrantView, error) {
	if err := schema.Validate(in); err != nil {
		return GrantView{}, err
	}
	if !in.EndsAt.After(in.StartsAt) {
		return GrantView{}, apperrors.InvalidRequest("ends_at must be after starts_at.")
	}
	var out GrantView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx, now := mustTx(ctx), s.now()
		dev, err := s.q.GetAccessDevice(ctx, tx, in.DeviceID)
		if isNoRows(err) || (err == nil && dev.Status != "ACTIVE") {
			return notFound("access_device")
		}
		if err != nil {
			return err
		}
		d, err := s.authorize(ctx, subj, authz.Do(authz.CapAccessGrant), dev.PropertyID)
		if err != nil {
			if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
				return notFound("access_device")
			}
			return err
		}
		rel, err := s.loadRelation(ctx, in.RelationType, in.RelationID)
		if err != nil {
			return err
		}
		manager := d.Via != nil && d.Via.Role == authz.RoleManager
		switch {
		case rel.propertyID != dev.PropertyID:
			return apperrors.InvalidRequest("The relationship belongs to another property.")
		case !rel.open:
			return conflict("RELATION_NOT_ACTIVE", "Access is granted only for a current relationship.")
		case !in.EndsAt.After(now):
			return apperrors.InvalidRequest("ends_at must be in the future.")
		case rel.from != nil && in.StartsAt.Before(*rel.from), rel.until != nil && in.EndsAt.After(*rel.until):
			return precondition("OUTSIDE_RELATION_PERIOD", "Access must stay within the relationship's period.")
		case rel.purpose == "PROVIDER" && in.EndsAt.Sub(in.StartsAt) > maxProviderGrant:
			return precondition("OUTSIDE_RELATION_PERIOD", "Provider access is limited to 14 days per grant.")
		case manager && in.RelationType == "manager_assignment":
			return apperrors.PermissionDenied("A manager cannot grant access to itself.")
		}
		by := "OWNER"
		if manager {
			by = "MANAGER"
		}
		g, err := s.q.InsertAccessGrant(ctx, tx, db.InsertAccessGrantParams{ID: ids.New(), PropertyID: dev.PropertyID, DeviceID: dev.ID, Purpose: rel.purpose,
			RelationType: in.RelationType, RelationID: in.RelationID, PrincipalDomain: rel.principalDomain, PrincipalRef: rel.principalRef,
			PrincipalLabel: rel.principalLabel, StartsAt: in.StartsAt, EndsAt: in.EndsAt, RequestedBy: by, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := domain.AccessGrant.RecordCreation(ctx, tx, g.ID, &g.PropertyID, "REQUESTED", now); err != nil {
			return err
		}
		if err := s.accessEvent(ctx, g, "REQUESTED", by, nil); err != nil {
			return err
		}
		if manager {
			p, err := s.q.GetProperty(ctx, tx, g.PropertyID)
			if err != nil {
				return err
			}
			if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateAccessRequested, map[string]string{"principal_label": rel.principalLabel}); err != nil {
				return err
			}
			if err := s.noteDelegateActivity(ctx, d, g.PropertyID, "ACCESS_REQUESTED", AggAccessGrant, g.ID, rel.principalLabel); err != nil {
				return err
			}
		} else if g, err = s.grantTo(ctx, g.ID, "APPROVED", "approve", "", "OWNER"); err != nil {
			return err
		}
		out = grantView(g)
		return s.record(ctx, "access.grant.create", AggAccessGrant, g.ID, map[string]any{"relation_type": in.RelationType, "device_id": dev.ID,
			"starts_at": in.StartsAt, "ends_at": in.EndsAt, "requested_by": by}, nil)
	})
	return out, err
}

func (s *Service) grantFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.AccessGrant, authz.Decision, error) {
	g, err := s.q.GetAccessGrant(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return g, authz.Decision{}, notFound(AggAccessGrant)
	}
	if err != nil {
		return g, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, g.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggAccessGrant)
	}
	return g, d, err
}

// GrantAction approves (owner, remote approval), issues a physical key or
// card (handover) or revokes a grant.
type GrantAction struct {
	Action      string  `json:"action" v:"required,enum=APPROVE|ISSUE|REVOKE"`
	ReasonCode  *string `json:"reason_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
	Compromised bool    `json:"compromised,omitempty" doc:"REVOKE: the credential or key is compromised (security alert)"`
	Note        *string `json:"note,omitempty" v:"max=500"`
}

// ActOnGrant applies an owner action. Credential revocation at the provider
// happens right after commit (and is retried by the worker).
func (s *Service) ActOnGrant(ctx context.Context, subj authz.Subject, id ids.ID, in GrantAction) (GrantView, error) {
	if err := schema.Validate(in); err != nil {
		return GrantView{}, err
	}
	var out GrantView
	err := s.InTx(ctx, func(ctx context.Context) error {
		g, d, err := s.grantFor(ctx, subj, id, authz.Do(authz.CapAccessGrant))
		if err != nil {
			return err
		}
		if d.Via != nil && d.Via.Role == authz.RoleManager && in.Action == "APPROVE" {
			return apperrors.PermissionDenied("Only the owner approves access requests.")
		}
		tx := mustTx(ctx)
		dev, err := s.q.GetAccessDevice(ctx, tx, g.DeviceID)
		if err != nil {
			return err
		}
		actor, _ := reporterOf(d, subj)
		switch in.Action {
		case "APPROVE":
			if g.Status != "REQUESTED" {
				return apperrors.InvalidStateTransition(g.Status, "APPROVED")
			}
			g, err = s.grantTo(ctx, id, "APPROVED", "approve", "", actor)
		case "ISSUE":
			if slices.Contains(smartDevices, dev.DeviceType) {
				return conflict("PROVIDER_ISSUES_CREDENTIAL", "Smart-device credentials are issued by the provider.")
			}
			if g.Status != "APPROVED" {
				return apperrors.InvalidStateTransition(g.Status, "ISSUED")
			}
			if err = s.q.InsertAccessEvent(ctx, tx, db.InsertAccessEventParams{ID: ids.New(), GrantID: &g.ID, DeviceID: g.DeviceID, PropertyID: g.PropertyID,
				EventType: "KEY_HANDED_OVER", Actor: actor, Note: in.Note, OccurredAt: s.now()}); err != nil {
				return err
			}
			if g, err = s.grantTo(ctx, id, "ISSUED", "issue", "", actor); err == nil && !g.StartsAt.After(s.now()) {
				g, err = s.grantTo(ctx, id, "ACTIVE", "activate", "", actor)
			}
		case "REVOKE":
			if in.ReasonCode == nil {
				return apperrors.InvalidRequest("reason_code is required.")
			}
			to := "REVOKED"
			if in.Compromised {
				if g.Status != "ISSUED" && g.Status != "ACTIVE" {
					return conflict("NOT_ISSUED", "Only issued access can be compromised.")
				}
				to = "COMPROMISED"
			}
			if g, err = s.grantTo(ctx, id, to, lower(to), *in.ReasonCode, actor); err == nil && in.Compromised {
				p, perr := s.q.GetProperty(ctx, tx, g.PropertyID)
				if perr != nil {
					return perr
				}
				err = s.notifyOwner(ctx, p.OwnerAccountID, TemplateAccessCompromised, map[string]string{"device_label": dev.Label})
				if err == nil {
					err = s.record(ctx, "access.grant.compromised", AggAccessGrant, id, map[string]any{"device_id": dev.ID},
						map[string]any{"security": true, "action_required": map[bool]string{true: "credential revoked at provider", false: "rekey or replace the physical access item"}[dev.ProviderDeviceRef != nil]})
				}
			}
		}
		if err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, g.PropertyID, "ACCESS_"+in.Action, AggAccessGrant, id, g.PrincipalLabel); err != nil {
			return err
		}
		if err := s.record(ctx, "access.grant."+lower(in.Action), AggAccessGrant, id, map[string]any{"reason_code": in.ReasonCode, "compromised": in.Compromised}, nil); err != nil {
			return err
		}
		out = grantView(g)
		return nil
	})
	if err == nil && in.Action == "REVOKE" {
		s.revokeCredential(ctx, id)
	}
	return out, err
}

// revokeCredential revokes a provider credential (best effort; the worker retries).
func (s *Service) revokeCredential(ctx context.Context, id ids.ID) {
	if s.SmartAccess == nil {
		return
	}
	g, err := s.q.GetAccessGrant(ctx, s.Pool, id)
	if err != nil || g.CredentialRef == nil || g.CredentialRevokedAt != nil {
		return
	}
	dev, err := s.q.GetAccessDevice(ctx, s.Pool, g.DeviceID)
	if err != nil || dev.ProviderDeviceRef == nil {
		return
	}
	if err := s.SmartAccess.Revoke(ctx, *dev.ProviderDeviceRef, *g.CredentialRef, "access_grant:"+id.String()+":revoke"); err != nil {
		s.Logger.WarnContext(ctx, "credential revocation deferred", "grant_id", id, "error", err)
		return
	}
	if err := s.InTx(ctx, func(ctx context.Context) error {
		if err := s.q.MarkCredentialRevoked(ctx, mustTx(ctx), db.MarkCredentialRevokedParams{ID: id, CredentialRevokedAt: ptr(s.now())}); err != nil {
			return err
		}
		return s.accessEvent(ctx, g, "CREDENTIAL_REVOKED", "SYSTEM", nil)
	}); err != nil {
		s.Logger.WarnContext(ctx, "credential revocation not recorded", "grant_id", id, "error", err)
	}
}

// endRelationGrants revokes every open grant of a relationship (in the
// caller's transaction), e.g. when a manager mandate is revoked.
func (s *Service) endRelationGrants(ctx context.Context, relType string, relID ids.ID, reason string) error {
	open, err := s.q.GrantsForRelation(ctx, mustTx(ctx), db.GrantsForRelationParams{RelationType: relType, RelationID: relID})
	if err != nil {
		return err
	}
	for _, id := range open {
		if _, err := s.grantTo(ctx, id, "REVOKED", "relation_ended", reason, "SYSTEM"); err != nil {
			return err
		}
	}
	return nil
}

// ListGrants lists grants across the caller's properties.
func (s *Service) ListGrants(ctx context.Context, subj authz.Subject, propertyID ids.ID, status string) ([]GrantView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAccessGrants(ctx, s.dbq(ctx), db.ListAccessGrantsParams{AccountID: accountOf(subj), Now: s.now(), PropertyID: idp(propertyID), Status: strp(status)})
	out := make([]GrantView, 0, len(rows))
	for _, r := range rows {
		out = append(out, grantView(r))
	}
	return out, err
}

// AccessEventView is one access history record.
type AccessEventView struct {
	ID         ids.ID    `json:"id"`
	GrantID    *ids.ID   `json:"grant_id"`
	DeviceID   ids.ID    `json:"device_id"`
	PropertyID ids.ID    `json:"property_id"`
	EventType  string    `json:"event_type"`
	Actor      string    `json:"actor"`
	Note       *string   `json:"note"`
	OccurredAt time.Time `json:"occurred_at"`
}

// AccessHistory pages access history across the caller's properties.
func (s *Service) AccessHistory(ctx context.Context, subj authz.Subject, propertyID, grantID ids.ID, cursor string, limit int) (pagination.Response[AccessEventView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[AccessEventView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[AccessEventView]{}, err
	}
	rows, err := s.q.ListAccessEvents(ctx, s.dbq(ctx), db.ListAccessEventsParams{AccountID: accountOf(subj), Now: s.now(), PropertyID: idp(propertyID),
		GrantID: idp(grantID), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[AccessEventView]{}, err
	}
	out := make([]AccessEventView, 0, len(rows))
	for _, e := range rows {
		out = append(out, AccessEventView{ID: e.ID, GrantID: e.GrantID, DeviceID: e.DeviceID, PropertyID: e.PropertyID, EventType: e.EventType, Actor: e.Actor,
			Note: e.Note, OccurredAt: e.OccurredAt})
	}
	return pagination.Build(out, limit, func(v AccessEventView) any { return timeCursor{At: v.OccurredAt, ID: v.ID} })
}

// RunAccessJobs provisions smart credentials, activates started grants,
// expires grants past their window, revokes grants whose relationship ended
// and revokes leftover provider credentials (§11 smart access expiry).
func (s *Service) RunAccessJobs(ctx context.Context, batch int32) error {
	now := s.now()
	step := func(list []ids.ID, fn func(context.Context, ids.ID) error) error {
		for _, id := range list {
			if err := s.InTx(ctx, func(ctx context.Context) error { return fn(ctx, id) }); err != nil && !benign(err) {
				return err
			}
		}
		return nil
	}
	if s.SmartAccess != nil {
		prov, err := s.q.GrantsToProvision(ctx, s.Pool, db.GrantsToProvisionParams{Now: now, MaxRows: batch})
		if err != nil {
			return err
		}
		for _, id := range prov {
			g, err := s.q.GetAccessGrant(ctx, s.Pool, id)
			if err != nil {
				return err
			}
			dev, err := s.q.GetAccessDevice(ctx, s.Pool, g.DeviceID)
			if err != nil {
				return err
			}
			ref, err := s.SmartAccess.Provision(ctx, smartaccess.ProvisionRequest{DeviceRef: *dev.ProviderDeviceRef, GrantRef: g.ID.String(), PrincipalLabel: g.PrincipalLabel,
				StartsAt: g.StartsAt, EndsAt: g.EndsAt}, "access_grant:"+g.ID.String()+":provision")
			if err != nil {
				s.Logger.WarnContext(ctx, "credential provisioning deferred", "grant_id", id, "error", err)
				continue
			}
			if err := s.InTx(ctx, func(ctx context.Context) error {
				cur, err := s.q.GetAccessGrant(ctx, mustTx(ctx), id)
				if err != nil || cur.Status != "APPROVED" {
					return err // ended meanwhile: the credential is revoked below
				}
				if err := s.q.SetGrantCredential(ctx, mustTx(ctx), db.SetGrantCredentialParams{ID: id, CredentialRef: &ref, UpdatedAt: s.now()}); err != nil {
					return err
				}
				_, err = s.grantTo(ctx, id, "ISSUED", "provision", "", "SYSTEM")
				return err
			}); err != nil && !benign(err) {
				return err
			}
			if cur, err := s.q.GetAccessGrant(ctx, s.Pool, id); err == nil && cur.CredentialRef == nil {
				// Ended before the credential was recorded: revoke it at once.
				_ = s.SmartAccess.Revoke(ctx, *dev.ProviderDeviceRef, ref, "access_grant:"+id.String()+":revoke")
			}
		}
	}
	act, err := s.q.GrantsToActivate(ctx, s.Pool, db.GrantsToActivateParams{Now: now, MaxRows: batch})
	if err != nil {
		return err
	}
	if err := step(act, func(ctx context.Context, id ids.ID) error {
		_, err := s.grantTo(ctx, id, "ACTIVE", "activate", "", "SYSTEM")
		return err
	}); err != nil {
		return err
	}
	ended, err := s.q.GrantsWithEndedRelation(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	if err := step(ended, func(ctx context.Context, id ids.ID) error {
		_, err := s.grantTo(ctx, id, "REVOKED", "relation_ended", "RELATION_ENDED", "SYSTEM")
		return err
	}); err != nil {
		return err
	}
	due, err := s.q.GrantsToEnd(ctx, s.Pool, db.GrantsToEndParams{Now: now, MaxRows: batch})
	if err != nil {
		return err
	}
	if err := step(due, func(ctx context.Context, id ids.ID) error {
		g, err := s.q.GetAccessGrant(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		if g.Status == "REQUESTED" {
			_, err = s.grantTo(ctx, id, "REVOKED", "lapse", "REQUEST_LAPSED", "SYSTEM")
		} else {
			_, err = s.grantTo(ctx, id, "EXPIRED", "expire", "WINDOW_ENDED", "SYSTEM")
		}
		return err
	}); err != nil {
		return err
	}
	if s.SmartAccess == nil {
		return nil
	}
	left, err := s.q.GrantsToRevokeCredential(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range left {
		s.revokeCredential(ctx, id)
	}
	return nil
}

func init() {
	produces("access.grant.revoked.v1", AggAccessGrant, "Access ended: revoked, compromised or expired (credential revocation propagation).", "Smart-access adapter, Notification", AccessGrantEvent{})
}
