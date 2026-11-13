// Package usecases implements property-manager-service: the owning service
// for external property managers and management companies. Business
// Operations is the authority for a management delegation (scope,
// capabilities, expiry, revocation). This service owns the manager's
// account, mandate acceptance, the record of delegated actions and the
// manager-facing authorization readback; every delegated action is
// re-validated live by Business, so a revocation or expiry denies the next
// action immediately even before the revocation event arrives here.
package usecases

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Role held by manager accounts.
const Role = "PROPERTY_MANAGER"

// Consumer is the durable Business event consumer name.
const Consumer = "property-manager-service-business-mandates"

type svc struct{ rt *domainkit.Runtime }

// New composes the property-manager service.
func New(rt *domainkit.Runtime) domainkit.App {
	s := &svc{rt: rt}
	if rt.Accounts != nil {
		rt.Accounts.DefaultRoles = []string{Role}
	}
	return domainkit.App{
		Actor: func(r chi.Router) {
			r.Use(domainkit.RequireRole(Role))
			r.Get("/mandates", s.list)
			r.Get("/mandates/{id}", s.get)
			r.Post("/mandates/{id}/accept", s.accept)
			r.Post("/mandates/{id}/decline", s.decline)
			r.Get("/mandates/{id}/authorization", s.authorization)
			r.Post("/mandates/{id}/maintenance-requests", s.maintenanceRequest)
		},
		InternalCallers: []platform.Service{platform.BusinessOperationsService, platform.AdminService},
		Internal: func(r chi.Router) {
			r.Get("/mandates/by-assignment/{id}", s.internalMandate)
			r.Post("/accounts/{id}/status", s.internalAccountStatus)
		},
		Consumers: func(ctx context.Context, conn *jetstream.Conn) error {
			rt.ConsumeBusiness(ctx, conn, Consumer, []string{"manager.assignment.updated.v1"}, s.onBusiness)
			return nil
		},
		Sweep:      s.sweep,
		SweepEvery: time.Minute,
	}
}

// BusinessHandler exposes the Business event projection (worker and tests).
func BusinessHandler(rt *domainkit.Runtime) domainkit.BusinessHandler {
	return (&svc{rt: rt}).onBusiness
}

// Sweep exposes the expiry sweep (worker and tests).
func Sweep(rt *domainkit.Runtime) func(context.Context) error { return (&svc{rt: rt}).sweep }

// Mandate is the manager's view of a Business management assignment.
type Mandate struct {
	ID                   ids.ID     `json:"id"`
	BusinessAssignmentID ids.ID     `json:"business_assignment_id"`
	OrganisationID       ids.ID     `json:"organisation_id"`
	PropertyID           ids.ID     `json:"property_id"`
	Kind                 string     `json:"kind"`
	Title                string     `json:"title"`
	Capabilities         []string   `json:"capabilities"`
	State                string     `json:"state"`
	BusinessState        string     `json:"business_state"`
	EndsAt               *time.Time `json:"ends_at"`
	AcceptedAt           *time.Time `json:"accepted_at"`
	Version              int64      `json:"version"`
}

const mandateColumns = `id,business_assignment_id,organisation_id,property_id,kind,title,capabilities,state,business_state,ends_at,accepted_at,version`

func scanMandate(row pgx.Row) (Mandate, error) {
	var m Mandate
	err := row.Scan(&m.ID, &m.BusinessAssignmentID, &m.OrganisationID, &m.PropertyID, &m.Kind, &m.Title, &m.Capabilities, &m.State, &m.BusinessState, &m.EndsAt, &m.AcceptedAt, &m.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, apperrors.NotFound("MANDATE_NOT_FOUND", "Mandate not found.")
	}
	return m, err
}

func pathID(r *http.Request) (ids.ID, error) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		return ids.Nil, apperrors.InvalidRequest("A canonical identifier is required.")
	}
	return id, nil
}

func (s *svc) owned(ctx context.Context, id ids.ID) (Mandate, error) {
	return scanMandate(s.rt.Pool.QueryRow(ctx, `SELECT `+mandateColumns+` FROM mandates WHERE id=$1 AND manager_account_id=$2`, id, domainkit.ActorID(ctx)))
}

func (s *svc) list(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT `+mandateColumns+` FROM mandates WHERE manager_account_id=$1 ORDER BY created_at DESC LIMIT 200`, domainkit.ActorID(r.Context()))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []Mandate{}
	for rows.Next() {
		m, err := scanMandate(rows)
		if err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		out = append(out, m)
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": out})
}

// Action is a delegated action performed under a mandate.
type Action struct {
	ID               ids.ID    `json:"id"`
	Action           string    `json:"action"`
	BusinessRecordID ids.ID    `json:"business_record_id"`
	Capability       string    `json:"capability"`
	CreatedAt        time.Time `json:"created_at"`
}

func (s *svc) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var m Mandate
	if err == nil {
		m, err = s.owned(r.Context(), id)
	}
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT id,action,business_record_id,capability,created_at FROM mandate_actions WHERE mandate_id=$1 ORDER BY created_at,id`, m.ID)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	defer rows.Close()
	actions := []Action{}
	for rows.Next() {
		var a Action
		if err = rows.Scan(&a.ID, &a.Action, &a.BusinessRecordID, &a.Capability, &a.CreatedAt); err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		actions = append(actions, a)
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"mandate": m, "actions": actions})
}

type reasonInput struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

// accept requires Business to confirm, live, that the delegation naming this
// manager is active and unexpired.
func (s *svc) accept(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in reasonInput
	var m Mandate
	var live domainkit.BusinessRecord
	pre := func(ctx context.Context) error {
		var err error
		if m, err = s.owned(ctx, id); err != nil {
			return err
		}
		if m.State != "OFFERED" {
			return apperrors.Conflict("INVALID_TRANSITION", "Only an offered mandate can be accepted.")
		}
		if err = s.rt.CallBusiness(ctx, m.OrganisationID, http.MethodGet, "/partner-records/manager_assignment/"+m.BusinessAssignmentID.String()+"?account_id="+domainkit.ActorID(ctx).String(), "", nil, &live); err != nil {
			return err
		}
		if live.Record.State != "ACTIVE" && live.Record.State != "EXPIRING" || live.Record.EndsAt == nil || !s.rt.Now().Before(*live.Record.EndsAt) {
			return apperrors.Conflict("DELEGATION_NOT_ACTIVE", "Business has not activated this delegation (or it has ended).")
		}
		return nil
	}
	s.rt.CommandWithPre(w, r, "mandate.accept", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		st, err := domainkit.Move(ctx, tx, "mandates", m.ID, 0, []string{"OFFERED"}, "ACTIVE")
		if err != nil {
			return domainkit.Change{}, err
		}
		caps := capabilities(live.Attributes)
		if _, err = tx.Exec(ctx, `UPDATE mandates SET accepted_at=now(),capabilities=$2,business_state=$3,ends_at=$4,version=version+1 WHERE id=$1`, m.ID, caps, live.Record.State, live.Record.EndsAt); err != nil {
			return domainkit.Change{}, err
		}
		m.State, m.Version, m.Capabilities, m.BusinessState, m.EndsAt = st.State, st.Version+1, caps, live.Record.State, live.Record.EndsAt
		return s.change(ctx, m, "ACCEPTED")
	})
}

func (s *svc) decline(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in reasonInput
	s.rt.Command(w, r, "mandate.decline", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		m, err := s.owned(ctx, id)
		if err != nil {
			return domainkit.Change{}, err
		}
		st, err := domainkit.Move(ctx, tx, "mandates", m.ID, 0, []string{"OFFERED"}, "DECLINED")
		if err != nil {
			return domainkit.Change{}, err
		}
		m.State, m.Version = st.State, st.Version
		return s.change(ctx, m, "DECLINED")
	})
}

func (s *svc) change(ctx context.Context, m Mandate, action string) (domainkit.Change, error) {
	ev, err := s.rt.Event(ctx, "property_manager.mandate.updated.v1", "mandate", m.ID, map[string]any{"mandate_id": m.ID, "business_assignment_id": m.BusinessAssignmentID, "organisation_id": m.OrganisationID, "state": m.State, "action": action, "version": m.Version})
	if err != nil {
		return domainkit.Change{}, err
	}
	return domainkit.Change{Body: m, Audit: &audit.Event{Action: "mandate." + strings.ToLower(action), TargetType: "mandate", TargetID: m.ID, Changes: map[string]any{"state": m.State}, Metadata: map[string]any{"business_assignment_id": m.BusinessAssignmentID}}, Events: []events.Envelope{ev}}, nil
}

// authorization is the manager-facing readback: Business decides, live.
func (s *svc) authorization(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	capability := r.URL.Query().Get("capability")
	var m Mandate
	if err == nil {
		m, err = s.owned(r.Context(), id)
	}
	if err == nil && capability == "" {
		err = apperrors.InvalidRequest("capability is required.")
	}
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var out map[string]any
	if err = s.rt.CallBusiness(r.Context(), m.OrganisationID, http.MethodGet, "/manager-authorizations/"+m.BusinessAssignmentID.String()+"?property_id="+m.PropertyID.String()+"&capability="+capability, "", nil, &out); err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	allowed := out["allowed"] == true && m.State == "ACTIVE"
	domainkit.Data(w, http.StatusOK, map[string]any{"mandate_id": m.ID, "capability": capability, "allowed": allowed, "business_state": out["state"], "expires_at": out["expires_at"], "local_state": m.State})
}

type maintenanceInput struct {
	Kind      string  `json:"kind"`
	Title     string  `json:"title"`
	SubjectID *ids.ID `json:"subject_id,omitempty"`
	Reason    string  `json:"reason"`
}

// maintenanceRequest performs a delegated action. Business re-validates the
// live delegation (state, expiry, scope, capability) in its own transaction.
func (s *svc) maintenanceRequest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in maintenanceInput
	var m Mandate
	var record domainkit.BusinessOps
	key := r.Header.Get("Idempotency-Key")
	pre := func(ctx context.Context) error {
		var err error
		if m, err = s.owned(ctx, id); err != nil {
			return err
		}
		if m.State != "ACTIVE" {
			return apperrors.PermissionDenied("The mandate is not active.")
		}
		if !slices.Contains(m.Capabilities, "maintenance:write") {
			return apperrors.PermissionDenied("The mandate does not include maintenance:write.")
		}
		return s.rt.CallBusiness(ctx, m.OrganisationID, http.MethodPost, "/manager-assignments/"+m.BusinessAssignmentID.String()+"/maintenance-requests", "pm:"+m.ID.String()+":"+key,
			map[string]any{"account_id": domainkit.ActorID(ctx), "request_key": key, "kind": in.Kind, "title": in.Title, "subject_id": in.SubjectID, "reason": in.Reason}, &record)
	}
	s.rt.CommandWithPre(w, r, "mandate.maintenance_request", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		aid := ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO mandate_actions(id,mandate_id,action,request_key,business_record_id,capability,created_by) VALUES($1,$2,'MAINTENANCE_REQUEST',$3,$4,'maintenance:write',$5) ON CONFLICT (mandate_id,request_key) DO NOTHING`, aid, m.ID, key, record.ID, domainkit.ActorID(ctx)); err != nil {
			return domainkit.Change{}, err
		}
		ev, err := s.rt.Event(ctx, "property_manager.action.recorded.v1", "mandate", m.ID, map[string]any{"mandate_id": m.ID, "business_assignment_id": m.BusinessAssignmentID, "organisation_id": m.OrganisationID, "action": "MAINTENANCE_REQUEST", "business_record_id": record.ID})
		if err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Status: http.StatusCreated, Body: map[string]any{"action_id": aid, "mandate_id": m.ID, "business_record": record},
			Audit: &audit.Event{Action: "mandate.action_performed", TargetType: "mandate", TargetID: m.ID, Metadata: map[string]any{"action": "MAINTENANCE_REQUEST", "business_record_id": record.ID}}, Events: []events.Envelope{ev}}, nil
	})
}

func capabilities(attrs map[string]any) []string {
	raw, _ := attrs["capabilities"].([]any)
	out := []string{}
	for _, v := range raw {
		if c, ok := v.(string); ok {
			out = append(out, c)
		}
	}
	return out
}

var businessToLocal = map[string]string{"SUSPENDED": "SUSPENDED", "REVOKED": "REVOKED", "TERMINATED": "REVOKED", "EXPIRED": "EXPIRED"}

// onBusiness projects management assignments naming a manager account here.
func (s *svc) onBusiness(ctx context.Context, tx pgx.Tx, e events.Envelope, b domainkit.BusinessEvent) error {
	account, ok := s.rt.AccountFromReference(b.Snapshot.CounterpartyReference)
	if !ok || b.Snapshot.Domain != "manager_assignment" || b.PropertyID == nil {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_accounts WHERE id=$1)`, account).Scan(&exists); err != nil || !exists {
		return err
	}
	caps := capabilities(b.Snapshot.Attributes)
	var m Mandate
	m, err := scanMandate(tx.QueryRow(ctx, `SELECT `+mandateColumns+` FROM mandates WHERE business_assignment_id=$1 FOR UPDATE`, b.TargetID))
	if err != nil {
		var ae *apperrors.Error
		if !errors.As(err, &ae) {
			return err
		}
		id := ids.New()
		if _, err = tx.Exec(ctx, `INSERT INTO mandates(id,business_assignment_id,organisation_id,property_id,manager_account_id,kind,title,capabilities,state,business_state,business_version,ends_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'OFFERED',$9,$10,$11)`,
			id, b.TargetID, b.OrganisationID, *b.PropertyID, account, b.Snapshot.Kind, truncate(b.Snapshot.Title, 200), caps, b.State, b.Version, b.Snapshot.EndsAt); err != nil {
			return err
		}
		return s.emit(ctx, tx, id, b, "OFFERED")
	}
	if b.Version <= 0 || m.BusinessState == b.State && len(caps) == 0 {
		return nil
	}
	to := businessToLocal[b.State]
	switch {
	case to != "" && m.State != to && !(to == "SUSPENDED" && m.State != "ACTIVE"):
		if _, err = domainkit.Move(ctx, tx, "mandates", m.ID, m.Version, []string{m.State}, to); err != nil {
			var ae *apperrors.Error
			if !errors.As(err, &ae) {
				return err
			}
		} else {
			m.Version++
		}
	case (b.State == "ACTIVE" || b.State == "EXPIRING") && m.State == "SUSPENDED":
		if _, err = domainkit.Move(ctx, tx, "mandates", m.ID, m.Version, []string{"SUSPENDED"}, "ACTIVE"); err != nil {
			return err
		}
		m.Version++
	}
	if len(caps) == 0 {
		caps = m.Capabilities
	}
	if _, err = tx.Exec(ctx, `UPDATE mandates SET business_state=$2,business_version=greatest(business_version,$3),capabilities=$4,ends_at=coalesce($5,ends_at),version=version+1,updated_at=now() WHERE id=$1`, m.ID, b.State, b.Version, caps, b.Snapshot.EndsAt); err != nil {
		return err
	}
	return s.emit(ctx, tx, m.ID, b, "BUSINESS_"+b.Operation)
}

func (s *svc) emit(ctx context.Context, tx pgx.Tx, id ids.ID, b domainkit.BusinessEvent, action string) error {
	ev, err := s.rt.Event(ctx, "property_manager.mandate.updated.v1", "mandate", id, map[string]any{"mandate_id": id, "business_assignment_id": b.TargetID, "organisation_id": b.OrganisationID, "business_state": b.State, "action": action})
	if err != nil {
		return err
	}
	return s.rt.Outbox.Add(ctx, tx, ev)
}

func (s *svc) internalMandate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		var m Mandate
		if m, err = scanMandate(s.rt.Pool.QueryRow(r.Context(), `SELECT `+mandateColumns+` FROM mandates WHERE business_assignment_id=$1`, id)); err == nil {
			domainkit.Data(w, http.StatusOK, m)
			return
		}
	}
	domainkit.Fail(w, r, err)
}

type statusInput struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (s *svc) internalAccountStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in statusInput
	s.rt.Command(w, r, "accounts.status", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if len(strings.TrimSpace(in.Reason)) < 5 {
			return domainkit.Change{}, apperrors.InvalidRequest("A reason is required.")
		}
		if err := s.rt.Accounts.SetStatus(ctx, tx, id, in.Status); err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Body: map[string]any{"account_id": id, "status": in.Status}, Audit: &audit.Event{Action: "account.status_changed", TargetType: "account", TargetID: id, Changes: map[string]any{"status": in.Status}, Metadata: map[string]any{"reason": in.Reason}}}, nil
	})
}

// sweep expires mandates whose delegation window has ended.
func (s *svc) sweep(ctx context.Context) error {
	_, err := s.rt.Pool.Exec(ctx, `UPDATE mandates SET state='EXPIRED',version=version+1,updated_at=now() WHERE state IN ('OFFERED','ACTIVE','SUSPENDED') AND ends_at IS NOT NULL AND ends_at<=now()`)
	return err
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}
