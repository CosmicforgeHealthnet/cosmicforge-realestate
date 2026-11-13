// Package usecases implements artisan-service: the owning service for
// artisan provider work. Business Operations owns work orders, approvals and
// verification; the artisan owns acceptance, execution, evidence and the
// workmanship warranty. Every outcome is reported to Business through its
// signed partner contract before it is committed locally, so the two never
// disagree about an accepted action (a failed Business call commits nothing;
// a retried command replays the stored result).
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

// Role held by artisan accounts.
const Role = "ARTISAN"

// Consumer is the durable Business event consumer name.
const Consumer = "artisan-service-business-work"

type svc struct{ rt *domainkit.Runtime }

// New composes the artisan service.
func New(rt *domainkit.Runtime) domainkit.App {
	s := &svc{rt: rt}
	if rt.Accounts != nil {
		rt.Accounts.DefaultRoles = []string{Role}
	}
	return domainkit.App{
		Actor: func(r chi.Router) {
			r.Use(domainkit.RequireRole(Role))
			r.Get("/work-requests", s.listWork)
			r.Get("/work-requests/{id}", s.getWork)
			r.Post("/work-requests/{id}/accept", s.action("ACCEPT"))
			r.Post("/work-requests/{id}/decline", s.action("DECLINE"))
			r.Post("/work-requests/{id}/arrive", s.action("ARRIVE"))
			r.Post("/work-requests/{id}/complete", s.action("COMPLETE"))
			r.Post("/work-requests/{id}/evidence", s.addEvidence)
			r.Get("/quote-requests", s.listQuotes)
			r.Post("/quote-requests/{id}/submit", s.submitQuote)
			r.Get("/warranties", s.listWarranties)
		},
		InternalCallers: []platform.Service{platform.BusinessOperationsService, platform.AdminService},
		Internal: func(r chi.Router) {
			r.Get("/work-requests/by-assignment/{id}", s.internalWork)
			r.Post("/accounts/{id}/status", s.internalAccountStatus)
		},
		Consumers: func(ctx context.Context, conn *jetstream.Conn) error {
			rt.ConsumeBusiness(ctx, conn, Consumer, []string{"maintenance.assignment.updated.v1", "maintenance.quote.updated.v1"}, s.onBusiness)
			return nil
		},
		Sweep:      s.sweep,
		SweepEvery: time.Minute,
	}
}

// WorkRequest is the artisan's projection of a Business provider assignment.
type WorkRequest struct {
	ID                   ids.ID    `json:"id"`
	BusinessAssignmentID ids.ID    `json:"business_assignment_id"`
	OrganisationID       ids.ID    `json:"organisation_id"`
	PropertyID           ids.ID    `json:"property_id"`
	WorkOrderID          ids.ID    `json:"work_order_id"`
	Kind                 string    `json:"kind"`
	ReworkOf             *ids.ID   `json:"rework_of"`
	Title                string    `json:"title"`
	ScopeOfWork          string    `json:"scope_of_work"`
	State                string    `json:"state"`
	BusinessState        string    `json:"business_state"`
	WarrantyDays         *int      `json:"warranty_days"`
	Version              int64     `json:"version"`
	CreatedAt            time.Time `json:"created_at"`
}

const workColumns = `id,business_assignment_id,organisation_id,property_id,work_order_id,kind,rework_of,title,scope_of_work,state,business_state,warranty_days,version,created_at`

func scanWork(row pgx.Row) (WorkRequest, error) {
	var w WorkRequest
	err := row.Scan(&w.ID, &w.BusinessAssignmentID, &w.OrganisationID, &w.PropertyID, &w.WorkOrderID, &w.Kind, &w.ReworkOf, &w.Title, &w.ScopeOfWork, &w.State, &w.BusinessState, &w.WarrantyDays, &w.Version, &w.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, apperrors.NotFound("WORK_REQUEST_NOT_FOUND", "Work request not found.")
	}
	return w, err
}

func pathID(r *http.Request) (ids.ID, error) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		return ids.Nil, apperrors.InvalidRequest("A canonical identifier is required.")
	}
	return id, nil
}

func (s *svc) ownedWork(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id ids.ID, lock bool) (WorkRequest, error) {
	query := `SELECT ` + workColumns + ` FROM work_requests WHERE id=$1 AND artisan_account_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanWork(q.QueryRow(ctx, query, id, domainkit.ActorID(ctx)))
}

func (s *svc) listWork(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT `+workColumns+` FROM work_requests WHERE artisan_account_id=$1 AND ($2='' OR state=$2) ORDER BY created_at DESC LIMIT 200`, domainkit.ActorID(r.Context()), r.URL.Query().Get("state"))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []WorkRequest{}
	for rows.Next() {
		wr, err := scanWork(rows)
		if err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		out = append(out, wr)
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": out})
}

func (s *svc) getWork(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		var wr WorkRequest
		if wr, err = s.ownedWork(r.Context(), s.rt.Pool, id, false); err == nil {
			ev, _ := s.evidence(r.Context(), wr.ID)
			domainkit.Data(w, http.StatusOK, map[string]any{"work_request": wr, "evidence": ev})
			return
		}
	}
	domainkit.Fail(w, r, err)
}

// Evidence is an artisan Media evidence reference.
type Evidence struct {
	ID             ids.ID    `json:"id"`
	Stage          string    `json:"stage"`
	MediaReference ids.ID    `json:"media_reference"`
	Note           string    `json:"note"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s *svc) evidence(ctx context.Context, id ids.ID) ([]Evidence, error) {
	rows, err := s.rt.Pool.Query(ctx, `SELECT id,stage,media_reference,note,created_at FROM work_evidence WHERE work_request_id=$1 ORDER BY created_at,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Evidence{}
	for rows.Next() {
		var e Evidence
		if err = rows.Scan(&e.ID, &e.Stage, &e.MediaReference, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *svc) evidenceRefs(ctx context.Context, id ids.ID, stage string) ([]ids.ID, error) {
	rows, err := s.rt.Pool.Query(ctx, `SELECT media_reference FROM work_evidence WHERE work_request_id=$1 AND stage=$2 ORDER BY created_at,id`, id, stage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.ID
	for rows.Next() {
		var m ids.ID
		if err = rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type evidenceInput struct {
	Stage          string `json:"stage"`
	MediaReference ids.ID `json:"media_reference"`
	Note           string `json:"note"`
}

func (s *svc) addEvidence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in evidenceInput
	s.rt.Command(w, r, "work.evidence", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if !slices.Contains([]string{"BEFORE", "DURING", "AFTER"}, in.Stage) || in.MediaReference.ValidateV7() != nil || len(in.Note) > 500 {
			return domainkit.Change{}, apperrors.InvalidRequest("A stage and a Media evidence reference are required.")
		}
		wr, err := s.ownedWork(ctx, tx, id, true)
		if err != nil {
			return domainkit.Change{}, err
		}
		if wr.State != "ACCEPTED" && wr.State != "ON_SITE" {
			return domainkit.Change{}, apperrors.Conflict("WORK_NOT_ACTIVE", "Evidence is added to accepted or on-site work.")
		}
		eid := ids.New()
		if _, err = tx.Exec(ctx, `INSERT INTO work_evidence(id,work_request_id,stage,media_reference,note,created_by) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (work_request_id,media_reference) DO NOTHING`, eid, wr.ID, in.Stage, in.MediaReference, in.Note, domainkit.ActorID(ctx)); err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Status: http.StatusCreated, Body: Evidence{ID: eid, Stage: in.Stage, MediaReference: in.MediaReference, Note: in.Note},
			Audit: &audit.Event{Action: "work_request.evidence_added", TargetType: "work_request", TargetID: wr.ID, Metadata: map[string]any{"stage": in.Stage, "media_reference": in.MediaReference}}}, nil
	})
}

type actionInput struct {
	Version      int64  `json:"version"`
	WarrantyDays int    `json:"warranty_days"`
	Reason       string `json:"reason"`
}

var actions = map[string]struct {
	from     []string
	to       string
	business string
}{
	"ACCEPT":   {[]string{"OFFERED"}, "ACCEPTED", "RECORD_ACCEPTANCE"},
	"DECLINE":  {[]string{"OFFERED"}, "DECLINED", "RECORD_DECLINE"},
	"ARRIVE":   {[]string{"ACCEPTED"}, "ON_SITE", "RECORD_ARRIVAL"},
	"COMPLETE": {[]string{"ON_SITE"}, "COMPLETED", "RECORD_COMPLETION"},
}

// action reports the outcome to Business first (authoritative work order
// record), then commits the local state, audit and event atomically.
func (s *svc) action(name string) http.HandlerFunc {
	a := actions[name]
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		var in actionInput
		var wr WorkRequest
		var stageRefs []ids.ID
		pre := func(ctx context.Context) error {
			var err error
			if wr, err = s.ownedWork(ctx, s.rt.Pool, id, false); err != nil {
				return err
			}
			if in.Version > 0 && in.Version != wr.Version {
				return apperrors.Conflict("VERSION_CONFLICT", "The work request version has changed.")
			}
			if !slices.Contains(a.from, wr.State) {
				return apperrors.Conflict("INVALID_TRANSITION", name+" is not allowed from "+wr.State+".")
			}
			reason := strings.TrimSpace(in.Reason)
			if len(reason) < 5 {
				reason = "Artisan " + strings.ToLower(name) + " via artisan-service"
			}
			body := map[string]any{"action": a.business, "reason": reason}
			switch name {
			case "ACCEPT":
				body["external_reference"] = "artisan-work-request:" + wr.ID.String()
			case "ARRIVE", "COMPLETE":
				stage := map[string]string{"ARRIVE": "BEFORE", "COMPLETE": "AFTER"}[name]
				if stageRefs, err = s.evidenceRefs(ctx, wr.ID, stage); err != nil {
					return err
				}
				if len(stageRefs) == 0 {
					return apperrors.InvalidRequest(name + " requires " + stage + " evidence.")
				}
				body["evidence_refs"] = stageRefs
				if name == "COMPLETE" {
					if in.WarrantyDays < 0 || in.WarrantyDays > 3650 {
						return apperrors.InvalidRequest("warranty_days must be 0-3650.")
					}
					attrs := map[string]any{"warranty_days": in.WarrantyDays}
					if in.WarrantyDays > 0 {
						attrs["warranty_reference"] = "artisan-warranty:" + wr.ID.String()
					}
					body["attributes"] = attrs
				}
			}
			_, err = s.rt.PartnerTransition(ctx, wr.OrganisationID, "provider_assignment", wr.BusinessAssignmentID, domainkit.ActorID(ctx), "artisan:"+wr.ID.String()+":"+a.business, body)
			return err
		}
		s.rt.CommandWithPre(w, r, "work."+strings.ToLower(name), &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
			// The Business call above emits an event this service also consumes, so
			// the row may have advanced; move under the row lock by state.
			st, err := domainkit.Move(ctx, tx, "work_requests", wr.ID, 0, a.from, a.to)
			if err != nil {
				return domainkit.Change{}, err
			}
			if name == "COMPLETE" {
				if _, err = tx.Exec(ctx, `UPDATE work_requests SET warranty_days=$2,business_state='COMPLETED',version=version+1,updated_at=now() WHERE id=$1`, wr.ID, in.WarrantyDays); err != nil {
					return domainkit.Change{}, err
				}
				st.Version++
				if in.WarrantyDays > 0 {
					now := s.rt.Now()
					if _, err = tx.Exec(ctx, `INSERT INTO warranties(id,work_request_id,artisan_account_id,reference,days,starts_at,expires_at,state) VALUES($1,$2,$3,$4,$5,$6,$7,'ACTIVE')`,
						ids.New(), wr.ID, domainkit.ActorID(ctx), "artisan-warranty:"+wr.ID.String(), in.WarrantyDays, now, now.AddDate(0, 0, in.WarrantyDays)); err != nil {
						return domainkit.Change{}, err
					}
				}
			}
			wr.State, wr.Version = st.State, st.Version
			ev, err := s.rt.Event(ctx, "artisan.work_request.updated.v1", "work_request", wr.ID, map[string]any{"work_request_id": wr.ID, "business_assignment_id": wr.BusinessAssignmentID, "organisation_id": wr.OrganisationID, "state": st.State, "action": name, "version": st.Version})
			if err != nil {
				return domainkit.Change{}, err
			}
			return domainkit.Change{Body: wr, Audit: &audit.Event{Action: "work_request." + strings.ToLower(name), TargetType: "work_request", TargetID: wr.ID, Changes: map[string]any{"state": st.State}, Metadata: map[string]any{"business_assignment_id": wr.BusinessAssignmentID, "business_action": a.business}}, Events: []events.Envelope{ev}}, nil
		})
	}
}

// QuoteRequest is the artisan's projection of a Business vendor quote.
type QuoteRequest struct {
	ID              ids.ID  `json:"id"`
	BusinessQuoteID ids.ID  `json:"business_quote_id"`
	OrganisationID  ids.ID  `json:"organisation_id"`
	WorkOrderID     ids.ID  `json:"work_order_id"`
	Title           string  `json:"title"`
	ScopeOfWork     string  `json:"scope_of_work"`
	State           string  `json:"state"`
	AmountMinor     *int64  `json:"amount_minor"`
	Currency        *string `json:"currency"`
	Version         int64   `json:"version"`
}

func (s *svc) listQuotes(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT id,business_quote_id,organisation_id,work_order_id,title,scope_of_work,state,amount_minor,currency,version FROM quote_requests WHERE artisan_account_id=$1 ORDER BY created_at DESC LIMIT 200`, domainkit.ActorID(r.Context()))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []QuoteRequest{}
	for rows.Next() {
		var q QuoteRequest
		if err = rows.Scan(&q.ID, &q.BusinessQuoteID, &q.OrganisationID, &q.WorkOrderID, &q.Title, &q.ScopeOfWork, &q.State, &q.AmountMinor, &q.Currency, &q.Version); err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		out = append(out, q)
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": out})
}

type quoteInput struct {
	AmountMinor  int64    `json:"amount_minor"`
	Currency     string   `json:"currency"`
	EvidenceRefs []ids.ID `json:"evidence_refs"`
	Reason       string   `json:"reason"`
}

func (s *svc) submitQuote(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in quoteInput
	var q QuoteRequest
	pre := func(ctx context.Context) error {
		if in.AmountMinor < 1 || len(in.Currency) != 3 || strings.ToUpper(in.Currency) != in.Currency || len(in.EvidenceRefs) == 0 {
			return apperrors.InvalidRequest("A positive amount, currency and the quote document evidence are required.")
		}
		err := s.rt.Pool.QueryRow(ctx, `SELECT id,business_quote_id,organisation_id,work_order_id,title,state,version FROM quote_requests WHERE id=$1 AND artisan_account_id=$2`, id, domainkit.ActorID(ctx)).Scan(&q.ID, &q.BusinessQuoteID, &q.OrganisationID, &q.WorkOrderID, &q.Title, &q.State, &q.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.NotFound("QUOTE_REQUEST_NOT_FOUND", "Quote request not found.")
		}
		if err != nil {
			return err
		}
		if q.State != "REQUESTED" {
			return apperrors.Conflict("INVALID_TRANSITION", "The quote request is not open.")
		}
		_, err = s.rt.PartnerTransition(ctx, q.OrganisationID, "vendor_quote", q.BusinessQuoteID, domainkit.ActorID(ctx), "artisan-quote:"+q.ID.String(), map[string]any{"action": "RECORD_QUOTE", "amount_minor": in.AmountMinor, "currency": in.Currency, "external_reference": "artisan-quote:" + q.ID.String(), "evidence_refs": in.EvidenceRefs, "reason": "Artisan quote submitted via artisan-service"})
		return err
	}
	s.rt.CommandWithPre(w, r, "quote.submit", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		tag, err := tx.Exec(ctx, `UPDATE quote_requests SET state='SUBMITTED',amount_minor=$2,currency=$3,version=version+1,updated_at=now() WHERE id=$1 AND state='REQUESTED'`, q.ID, in.AmountMinor, in.Currency)
		if err != nil {
			return domainkit.Change{}, err
		}
		if tag.RowsAffected() == 0 {
			return domainkit.Change{}, apperrors.Conflict("VERSION_CONFLICT", "The quote request changed concurrently.")
		}
		q.State, q.Version, q.AmountMinor, q.Currency = "SUBMITTED", q.Version+1, &in.AmountMinor, &in.Currency
		ev, err := s.rt.Event(ctx, "artisan.quote.updated.v1", "quote_request", q.ID, map[string]any{"quote_request_id": q.ID, "business_quote_id": q.BusinessQuoteID, "organisation_id": q.OrganisationID, "state": q.State, "amount_minor": in.AmountMinor, "currency": in.Currency})
		if err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Body: q, Audit: &audit.Event{Action: "quote_request.submitted", TargetType: "quote_request", TargetID: q.ID, Changes: map[string]any{"amount_minor": in.AmountMinor, "currency": in.Currency}}, Events: []events.Envelope{ev}}, nil
	})
}

// Warranty is the artisan's workmanship warranty reference.
type Warranty struct {
	ID            ids.ID    `json:"id"`
	WorkRequestID ids.ID    `json:"work_request_id"`
	Reference     string    `json:"reference"`
	Days          int       `json:"days"`
	StartsAt      time.Time `json:"starts_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	State         string    `json:"state"`
}

func (s *svc) listWarranties(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT id,work_request_id,reference,days,starts_at,expires_at,state FROM warranties WHERE artisan_account_id=$1 ORDER BY created_at DESC LIMIT 200`, domainkit.ActorID(r.Context()))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []Warranty{}
	for rows.Next() {
		var x Warranty
		if err = rows.Scan(&x.ID, &x.WorkRequestID, &x.Reference, &x.Days, &x.StartsAt, &x.ExpiresAt, &x.State); err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		out = append(out, x)
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": out})
}

// onBusiness projects Business provider assignments and quote requests that
// name an artisan account of this service.
func (s *svc) onBusiness(ctx context.Context, tx pgx.Tx, e events.Envelope, b domainkit.BusinessEvent) error {
	account, ok := s.rt.AccountFromReference(b.Snapshot.CounterpartyReference)
	if !ok || b.PropertyID == nil || b.Snapshot.ParentID == nil {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_accounts WHERE id=$1)`, account).Scan(&exists); err != nil || !exists {
		return err
	}
	scope, _ := b.Snapshot.Attributes["scope_of_work"].(string)
	switch b.Snapshot.Domain {
	case "provider_assignment":
		if b.Operation == "CREATED" {
			kind := "STANDARD"
			var reworkOf *ids.ID
			if raw, _ := b.Snapshot.Attributes["rework_of"].(string); raw != "" {
				var prior ids.ID
				if tx.QueryRow(ctx, `SELECT id FROM work_requests WHERE business_assignment_id=$1`, raw).Scan(&prior) == nil {
					kind, reworkOf = "REWORK", &prior
				}
			}
			id := ids.New()
			tag, err := tx.Exec(ctx, `INSERT INTO work_requests(id,business_assignment_id,organisation_id,property_id,work_order_id,artisan_account_id,kind,rework_of,title,scope_of_work,state,business_state,business_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'OFFERED',$11,$12) ON CONFLICT (business_assignment_id) DO NOTHING`,
				id, b.TargetID, b.OrganisationID, *b.PropertyID, *b.Snapshot.ParentID, account, kind, reworkOf, truncate(b.Snapshot.Title, 200), truncate(scope, 2000), b.State, b.Version)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			return s.emit(ctx, tx, "artisan.work_request.updated.v1", "work_request", id, map[string]any{"work_request_id": id, "business_assignment_id": b.TargetID, "organisation_id": b.OrganisationID, "state": "OFFERED", "action": "OFFERED"})
		}
		// Business cancellation ends local work that has not completed.
		if b.State == "CANCELLED" {
			var id ids.ID
			var state string
			var version int64
			if err := tx.QueryRow(ctx, `SELECT id,state,version FROM work_requests WHERE business_assignment_id=$1 FOR UPDATE`, b.TargetID).Scan(&id, &state, &version); err != nil {
				return nil
			}
			if slices.Contains([]string{"OFFERED", "ACCEPTED", "ON_SITE"}, state) {
				if _, err := domainkit.Move(ctx, tx, "work_requests", id, version, []string{state}, "CANCELLED"); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `UPDATE work_requests SET business_state=$2,business_version=$3,version=version+1,updated_at=now() WHERE business_assignment_id=$1 AND business_version<$3`, b.TargetID, b.State, b.Version)
		return err
	case "vendor_quote":
		if b.Operation == "CREATED" {
			_, err := tx.Exec(ctx, `INSERT INTO quote_requests(id,business_quote_id,organisation_id,property_id,work_order_id,artisan_account_id,title,scope_of_work,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'REQUESTED') ON CONFLICT (business_quote_id) DO NOTHING`,
				ids.New(), b.TargetID, b.OrganisationID, *b.PropertyID, *b.Snapshot.ParentID, account, truncate(b.Snapshot.Title, 200), truncate(scope, 2000))
			return err
		}
		to := map[string]string{"ACCEPTED": "ACCEPTED", "REJECTED": "REJECTED", "WITHDRAWN": "WITHDRAWN"}[b.State]
		if to == "" {
			return nil
		}
		var id ids.ID
		var state string
		var version int64
		if err := tx.QueryRow(ctx, `SELECT id,state,version FROM quote_requests WHERE business_quote_id=$1 FOR UPDATE`, b.TargetID).Scan(&id, &state, &version); err != nil || state == to {
			return nil
		}
		if _, err := domainkit.Move(ctx, tx, "quote_requests", id, version, []string{"REQUESTED", "SUBMITTED"}, to); err != nil {
			var ae *apperrors.Error
			if errors.As(err, &ae) {
				return nil
			}
			return err
		}
		return s.emit(ctx, tx, "artisan.quote.updated.v1", "quote_request", id, map[string]any{"quote_request_id": id, "business_quote_id": b.TargetID, "organisation_id": b.OrganisationID, "state": to})
	}
	return nil
}

func (s *svc) emit(ctx context.Context, tx pgx.Tx, eventType, aggregate string, id ids.ID, payload map[string]any) error {
	ev, err := s.rt.Event(ctx, eventType, aggregate, id, payload)
	if err != nil {
		return err
	}
	return s.rt.Outbox.Add(ctx, tx, ev)
}

func (s *svc) internalWork(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		var wr WorkRequest
		if wr, err = scanWork(s.rt.Pool.QueryRow(r.Context(), `SELECT `+workColumns+` FROM work_requests WHERE business_assignment_id=$1`, id)); err == nil {
			domainkit.Data(w, http.StatusOK, wr)
			return
		}
	}
	domainkit.Fail(w, r, err)
}

type statusInput struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// internalAccountStatus lets Admin suspend or reinstate an artisan account
// (sessions are revoked on suspension).
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

// sweep expires lapsed warranties.
func (s *svc) sweep(ctx context.Context) error {
	_, err := s.rt.Pool.Exec(ctx, `UPDATE warranties SET state='EXPIRED',version=version+1,updated_at=now() WHERE state IN ('ACTIVE','CLAIMED') AND expires_at<=now()`)
	return err
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

// BusinessHandler exposes the Business event projection (worker and tests).
func BusinessHandler(rt *domainkit.Runtime) domainkit.BusinessHandler {
	return (&svc{rt: rt}).onBusiness
}

// Sweep exposes the periodic sweep (worker and tests).
func Sweep(rt *domainkit.Runtime) func(context.Context) error { return (&svc{rt: rt}).sweep }
