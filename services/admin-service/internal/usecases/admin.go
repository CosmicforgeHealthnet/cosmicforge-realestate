// Package usecases implements admin-service: the platform governance
// authority. It owns review cases (disputes escalated by Business,
// moderation, financial remedy approvals, high-risk reviews and appeals),
// their maker-checker decisions and account restrictions.
//
// Decisions take effect only through the owning services: a dispute
// decision is recorded on the Business dispute through the signed partner
// contract before it is committed here; a restriction suspends the account
// in the domain service that owns it. A financial remedy approval is an
// authorisation only: Admin moves no money, and the decision event says the
// execution is pending in the Payment Service.
//
// Privileged decisions require a reviewer proposal, a distinct supervisor
// decision and a fresh TOTP step-up code (X-MFA-Code) with replay
// protection, matching the MFA Business demands of staff for the same
// dispute decision.
package usecases

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/totp"
)

// Roles.
const (
	RoleReviewer   = "REVIEWER"
	RoleSupervisor = "SUPERVISOR"
)

// Consumer is the durable Business dispute consumer.
const Consumer = "admin-service-business-disputes"

// HeaderMFA carries the TOTP step-up code (kept out of the idempotent body
// so a retry with a fresh code replays the same command).
const HeaderMFA = "X-MFA-Code"

// outcomes allowed per case kind.
var outcomes = map[string][]string{
	"DISPUTE":          {"UPHELD", "PARTIALLY_UPHELD", "REJECTED"},
	"MODERATION":       {"REMOVE", "KEEP"},
	"FINANCIAL_REMEDY": {"APPROVED", "DECLINED"},
	"HIGH_RISK":        {"CLEARED", "RESTRICT"},
	"APPEAL":           {"UPHELD", "OVERTURNED"},
}

// subjectServices are the domain services whose accounts Admin may restrict.
var subjectServices = []platform.Service{platform.ArtisanService, platform.PropertyManagerService, platform.ProfessionalServicesService}

// Deps are the collaborators of the admin service.
type Deps struct {
	// Subjects are signed clients of the services that own restricted accounts.
	Subjects map[platform.Service]*httpx.Client
	// Sealer protects TOTP seeds at rest; nil disables privileged actions
	// (fail closed).
	Sealer *totp.Sealer
}

type svc struct {
	rt   *domainkit.Runtime
	deps Deps
}

// New composes admin-service from configuration.
func New(rt *domainkit.Runtime) domainkit.App {
	d := Deps{Subjects: map[platform.Service]*httpx.Client{}}
	if l := rt.Cfg.Loader; l != nil && rt.Cfg.Identity != nil && rt.Cfg.Identity.Signer != nil {
		for svc, key := range map[platform.Service]string{platform.ArtisanService: "ARTISAN_SERVICE_URL", platform.PropertyManagerService: "PROPERTY_MANAGER_SERVICE_URL", platform.ProfessionalServicesService: "PROFESSIONAL_SERVICES_SERVICE_URL"} {
			if url := l.String(key, ""); url != "" {
				if c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: url, Audience: string(svc), Tokens: rt.Cfg.Identity.Signer, Timeout: 8 * time.Second, MaxAttempts: 3}); err == nil {
					d.Subjects[svc] = c
				}
			}
		}
		if raw := l.Secret("ADMIN_MFA_ENCRYPTION_KEY", false); !raw.IsEmpty() {
			if key, err := base64.StdEncoding.DecodeString(raw.Reveal()); err == nil {
				if s, err := totp.NewSealer(l.String("ADMIN_MFA_ENCRYPTION_KEY_ID", "mfa-1"), key); err == nil {
					d.Sealer = s
				}
			}
		}
		if d.Sealer == nil && rt.Log != nil {
			rt.Log.Error("ADMIN_MFA_ENCRYPTION_KEY is missing or invalid; privileged admin decisions are disabled")
		}
	}
	return NewWith(rt, d)
}

// NewWith composes admin-service with explicit collaborators.
func NewWith(rt *domainkit.Runtime, d Deps) domainkit.App {
	s := &svc{rt: rt, deps: d}
	if rt.Accounts != nil {
		// Admin roles are never self-granted: bootstrap grants or a
		// supervisor grant with MFA only.
		rt.Accounts.DefaultRoles = nil
	}
	return domainkit.App{
		Actor: func(r chi.Router) {
			r.Post("/mfa/enrol", s.mfaEnrol)
			r.Post("/mfa/confirm", s.mfaConfirm)
			r.Group(func(r chi.Router) {
				r.Use(domainkit.RequireRole(RoleReviewer, RoleSupervisor))
				r.Get("/cases", s.listCases)
				r.Get("/cases/{id}", s.getCase)
				r.Post("/cases/{id}/notes", s.addNote)
				r.Get("/restrictions", s.listRestrictions)
			})
			r.Group(func(r chi.Router) {
				r.Use(domainkit.RequireRole(RoleReviewer))
				r.Post("/cases/{id}/claim", s.claim)
				r.Post("/cases/{id}/propose", s.propose)
				r.Post("/restrictions", s.proposeRestriction)
			})
			r.Group(func(r chi.Router) {
				r.Use(domainkit.RequireRole(RoleSupervisor))
				r.Post("/cases/{id}/return", s.returnCase)
				r.Post("/cases/{id}/decide", s.decide)
				r.Post("/restrictions/{id}/approve", s.approveRestriction)
				r.Post("/restrictions/{id}/reject", s.rejectRestriction)
				r.Post("/restrictions/{id}/lift", s.liftRestriction)
				r.Post("/accounts/{id}/roles", s.grantRoles)
			})
		},
		InternalCallers: []platform.Service{platform.BusinessOperationsService, platform.MarketplaceService, platform.PaymentService, platform.ArtisanService, platform.PropertyManagerService, platform.ProfessionalServicesService},
		Internal: func(r chi.Router) {
			r.Post("/cases", s.intake)
			r.Get("/cases/by-source", s.bySource)
			r.Post("/cases/{id}/appeals", s.appeal)
		},
		Consumers: func(ctx context.Context, conn *jetstream.Conn) error {
			rt.ConsumeBusiness(ctx, conn, Consumer, []string{"dispute.updated.v1"}, s.onDispute)
			return nil
		},
	}
}

// Case is a review case.
type Case struct {
	ID                ids.ID     `json:"id"`
	Kind              string     `json:"kind"`
	SourceService     string     `json:"source_service"`
	SourceReference   string     `json:"source_reference"`
	OrganisationID    *ids.ID    `json:"organisation_id"`
	BusinessRecordID  *ids.ID    `json:"business_record_id"`
	BusinessVersion   *int64     `json:"business_version"`
	ParentCaseID      *ids.ID    `json:"parent_case_id"`
	SubjectService    *string    `json:"subject_service"`
	SubjectAccountID  *ids.ID    `json:"subject_account_id"`
	Title             string     `json:"title"`
	Summary           string     `json:"summary"`
	AmountMinor       *int64     `json:"amount_minor"`
	Currency          *string    `json:"currency"`
	State             string     `json:"state"`
	AssigneeID        *ids.ID    `json:"assignee_id"`
	ProposedBy        *ids.ID    `json:"proposed_by"`
	ProposedOutcome   *string    `json:"proposed_outcome"`
	ProposedRationale *string    `json:"proposed_rationale"`
	DecidedBy         *ids.ID    `json:"decided_by"`
	Outcome           *string    `json:"outcome"`
	Rationale         *string    `json:"rationale"`
	DecidedAt         *time.Time `json:"decided_at"`
	Version           int64      `json:"version"`
	CreatedAt         time.Time  `json:"created_at"`
}

const caseColumns = `id,kind,source_service,source_reference,organisation_id,business_record_id,business_version,parent_case_id,subject_service,subject_account_id,title,summary,amount_minor,currency,state,assignee_id,proposed_by,proposed_outcome,proposed_rationale,decided_by,outcome,rationale,decided_at,version,created_at`

func scanCase(row pgx.Row) (Case, error) {
	var c Case
	err := row.Scan(&c.ID, &c.Kind, &c.SourceService, &c.SourceReference, &c.OrganisationID, &c.BusinessRecordID, &c.BusinessVersion, &c.ParentCaseID, &c.SubjectService, &c.SubjectAccountID, &c.Title, &c.Summary, &c.AmountMinor, &c.Currency, &c.State, &c.AssigneeID, &c.ProposedBy, &c.ProposedOutcome, &c.ProposedRationale, &c.DecidedBy, &c.Outcome, &c.Rationale, &c.DecidedAt, &c.Version, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, apperrors.NotFound("CASE_NOT_FOUND", "Review case not found.")
	}
	return c, err
}

func (s *svc) loadCase(ctx context.Context, q database.Querier, id ids.ID, lock bool) (Case, error) {
	sql := `SELECT ` + caseColumns + ` FROM review_cases WHERE id=$1`
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanCase(q.QueryRow(ctx, sql, id))
}

func pathID(r *http.Request, name string) (ids.ID, error) {
	id, err := ids.ParseV7(chi.URLParam(r, name))
	if err != nil {
		return id, apperrors.InvalidRequest("A valid identifier is required.")
	}
	return id, nil
}

func (s *svc) listCases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT `+caseColumns+` FROM review_cases WHERE ($1='' OR state=$1) AND ($2='' OR kind=$2) ORDER BY created_at LIMIT 200`, q.Get("state"), q.Get("kind"))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Case, error) { return scanCase(row) })
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": items})
}

func (s *svc) getCase(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	var c Case
	if err == nil {
		c, err = s.loadCase(r.Context(), s.rt.Pool, id, false)
	}
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT id,note,evidence_refs,created_by,created_at FROM case_notes WHERE case_id=$1 ORDER BY created_at`, id)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	type note struct {
		ID           ids.ID    `json:"id"`
		Note         string    `json:"note"`
		EvidenceRefs []ids.ID  `json:"evidence_refs"`
		CreatedBy    ids.ID    `json:"created_by"`
		CreatedAt    time.Time `json:"created_at"`
	}
	notes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (note, error) {
		var n note
		return n, row.Scan(&n.ID, &n.Note, &n.EvidenceRefs, &n.CreatedBy, &n.CreatedAt)
	})
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"case": c, "notes": notes})
}

type noteInput struct {
	Note         string   `json:"note"`
	EvidenceRefs []ids.ID `json:"evidence_refs"`
}

func (s *svc) addNote(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in noteInput
	s.rt.Command(w, r, "case.note", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if l := len(strings.TrimSpace(in.Note)); l < 3 || l > 4000 || len(in.EvidenceRefs) > 20 {
			return domainkit.Change{}, apperrors.InvalidRequest("A note of 3-4000 characters and at most 20 evidence references are required.")
		}
		if _, err := s.loadCase(ctx, tx, id, false); err != nil {
			return domainkit.Change{}, err
		}
		if in.EvidenceRefs == nil {
			in.EvidenceRefs = []ids.ID{}
		}
		nid := ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO case_notes(id,case_id,note,evidence_refs,created_by) VALUES($1,$2,$3,$4,$5)`, nid, id, strings.TrimSpace(in.Note), in.EvidenceRefs, domainkit.ActorID(ctx)); err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Status: http.StatusCreated, Body: map[string]any{"note_id": nid, "case_id": id}, Audit: &audit.Event{Action: "case.note_added", TargetType: "review_case", TargetID: id}}, nil
	})
}

type versionInput struct {
	Version   int64  `json:"version"`
	Outcome   string `json:"outcome"`
	Rationale string `json:"rationale"`
	Reason    string `json:"reason"`
}

func (s *svc) claim(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in versionInput
	s.rt.Command(w, r, "case.claim", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		c, err := s.loadCase(ctx, tx, id, true)
		if err != nil {
			return domainkit.Change{}, err
		}
		if c.State != "OPEN" {
			return domainkit.Change{}, apperrors.Conflict("INVALID_TRANSITION", "Only an open case can be claimed.")
		}
		if in.Version > 0 && in.Version != c.Version {
			return domainkit.Change{}, apperrors.Conflict("VERSION_CONFLICT", "The case version has changed.")
		}
		me := domainkit.ActorID(ctx)
		if _, err := tx.Exec(ctx, `UPDATE review_cases SET state='IN_REVIEW',assignee_id=$2,version=version+1,updated_at=now() WHERE id=$1`, id, me); err != nil {
			return domainkit.Change{}, err
		}
		return s.caseChange(ctx, tx, id, "case.claimed", "CLAIM")
	})
}

func (s *svc) propose(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in versionInput
	s.rt.Command(w, r, "case.propose", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		c, err := s.loadCase(ctx, tx, id, true)
		if err != nil {
			return domainkit.Change{}, err
		}
		me := domainkit.ActorID(ctx)
		switch {
		case c.State != "IN_REVIEW":
			return domainkit.Change{}, apperrors.Conflict("INVALID_TRANSITION", "Only a case in review can receive a proposal.")
		case c.AssigneeID == nil || *c.AssigneeID != me:
			return domainkit.Change{}, apperrors.PermissionDenied("Only the assigned reviewer can propose a decision.")
		case in.Version > 0 && in.Version != c.Version:
			return domainkit.Change{}, apperrors.Conflict("VERSION_CONFLICT", "The case version has changed.")
		case !slices.Contains(outcomes[c.Kind], in.Outcome):
			return domainkit.Change{}, apperrors.InvalidRequest("The outcome is not valid for a " + c.Kind + " case.")
		case len(strings.TrimSpace(in.Rationale)) < 10 || len(in.Rationale) > 4000:
			return domainkit.Change{}, apperrors.InvalidRequest("A rationale of 10-4000 characters is required.")
		}
		if _, err := tx.Exec(ctx, `UPDATE review_cases SET state='PROPOSED',proposed_by=$2,proposed_outcome=$3,proposed_rationale=$4,version=version+1,updated_at=now() WHERE id=$1`, id, me, in.Outcome, strings.TrimSpace(in.Rationale)); err != nil {
			return domainkit.Change{}, err
		}
		return s.caseChange(ctx, tx, id, "case.proposed", "PROPOSE")
	})
}

func (s *svc) returnCase(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in versionInput
	s.rt.Command(w, r, "case.return", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		c, err := s.loadCase(ctx, tx, id, true)
		if err != nil {
			return domainkit.Change{}, err
		}
		if c.State != "PROPOSED" || len(strings.TrimSpace(in.Reason)) < 5 {
			return domainkit.Change{}, apperrors.Conflict("INVALID_TRANSITION", "Only a proposed case can be returned, with a reason.")
		}
		if _, err := tx.Exec(ctx, `UPDATE review_cases SET state='IN_REVIEW',proposed_by=NULL,proposed_outcome=NULL,proposed_rationale=NULL,version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
			return domainkit.Change{}, err
		}
		return s.caseChange(ctx, tx, id, "case.returned", "RETURN")
	})
}

// decide is the checker step: a supervisor distinct from the proposer, with
// TOTP step-up, records the decision. Dispute decisions are recorded on the
// Business dispute first (authoritative outcome), appeals that overturn a
// restriction lift it in the owning service first.
func (s *svc) decide(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in versionInput
	var c Case
	var lift *Restriction
	pre := func(ctx context.Context) error {
		var err error
		if c, err = s.loadCase(ctx, s.rt.Pool, id, false); err != nil {
			return err
		}
		me := domainkit.ActorID(ctx)
		switch {
		case c.State != "PROPOSED":
			return apperrors.Conflict("INVALID_TRANSITION", "Only a proposed case can be decided.")
		case in.Version > 0 && in.Version != c.Version:
			return apperrors.Conflict("VERSION_CONFLICT", "The case version has changed.")
		case c.ProposedBy != nil && *c.ProposedBy == me:
			return apperrors.PermissionDenied("The proposer cannot decide the same case (maker-checker).")
		case in.Outcome != "" && c.ProposedOutcome != nil && in.Outcome != *c.ProposedOutcome:
			return apperrors.Conflict("OUTCOME_MISMATCH", "Return the case to change the proposed outcome.")
		case len(strings.TrimSpace(in.Rationale)) < 10:
			return apperrors.InvalidRequest("A decision rationale of at least 10 characters is required.")
		}
		if err = s.stepUp(ctx, r); err != nil {
			return err
		}
		outcome := *c.ProposedOutcome
		switch c.Kind {
		case "DISPUTE":
			body := map[string]any{"action": "RECORD_DECISION", "external_reference": "admin-service:case:" + c.ID.String(), "reason": "Admin review decision " + outcome,
				"attributes": map[string]any{"outcome": outcome, "admin_case_id": c.ID.String(), "decided_by_admin": s.rt.PartnerReference(me), "proposed_by_admin": s.rt.PartnerReference(*c.ProposedBy)}}
			if c.BusinessVersion != nil {
				body["version"] = *c.BusinessVersion
			}
			_, err = s.rt.PartnerTransition(ctx, *c.OrganisationID, "dispute", *c.BusinessRecordID, me, "admin-dispute-decision:"+c.ID.String(), body)
			return err
		case "APPEAL":
			if outcome != "OVERTURNED" {
				return nil
			}
			var rs Restriction
			rs, err = scanRestriction(s.rt.Pool.QueryRow(ctx, `SELECT `+restrictionColumns+` FROM restrictions WHERE case_id=$1 AND state='ACTIVE'`, *c.ParentCaseID))
			if apperrors.CodeOf(err) == "RESTRICTION_NOT_FOUND" {
				return nil
			}
			if err != nil {
				return err
			}
			lift = &rs
			return s.setSubjectStatus(ctx, rs, "ACTIVE", "Restriction overturned on appeal "+c.ID.String())
		}
		return nil
	}
	s.rt.CommandWithPre(w, r, "case.decide", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		me := domainkit.ActorID(ctx)
		tag, err := tx.Exec(ctx, `UPDATE review_cases SET state='DECIDED',decided_by=$2,outcome=proposed_outcome,rationale=$3,decided_at=now(),version=version+1,updated_at=now() WHERE id=$1 AND state='PROPOSED' AND version=$4`, id, me, strings.TrimSpace(in.Rationale), c.Version)
		if err != nil {
			return domainkit.Change{}, err
		}
		if tag.RowsAffected() == 0 {
			return domainkit.Change{}, apperrors.Conflict("VERSION_CONFLICT", "The case changed while it was being decided.")
		}
		var extra []events.Envelope
		if lift != nil {
			if _, err := tx.Exec(ctx, `UPDATE restrictions SET state='LIFTED',lifted_by=$2,lift_reason=$3,version=version+1,updated_at=now() WHERE id=$1 AND state='ACTIVE'`, lift.ID, me, "Overturned on appeal"); err != nil {
				return domainkit.Change{}, err
			}
			ev, err := s.rt.Event(ctx, "admin.restriction.updated.v1", "restriction", lift.ID, map[string]any{"restriction_id": lift.ID, "subject_service": lift.SubjectService, "subject_account_id": lift.SubjectAccountID, "state": "LIFTED", "case_id": lift.CaseID})
			if err != nil {
				return domainkit.Change{}, err
			}
			extra = append(extra, ev)
		}
		ch, err := s.caseChange(ctx, tx, id, "case.decided", "DECIDE")
		if err != nil {
			return ch, err
		}
		dc, _ := ch.Body.(Case)
		payload := map[string]any{"case_id": dc.ID, "kind": dc.Kind, "outcome": dc.Outcome, "source_service": dc.SourceService, "source_reference": dc.SourceReference, "organisation_id": dc.OrganisationID, "parent_case_id": dc.ParentCaseID}
		if dc.Kind == "FINANCIAL_REMEDY" {
			// An approval authorises a remedy; execution belongs to the
			// Payment Service and is not performed or claimed here.
			payload["amount_minor"], payload["currency"], payload["execution"] = dc.AmountMinor, dc.Currency, "PENDING_PAYMENT_SERVICE"
		}
		ev, err := s.rt.Event(ctx, "admin.case.decided.v1", "review_case", id, payload)
		if err != nil {
			return ch, err
		}
		ch.Events = append(append(ch.Events, ev), extra...)
		return ch, nil
	})
}

// caseChange reloads a case after a transition and builds its audit/event.
func (s *svc) caseChange(ctx context.Context, tx pgx.Tx, id ids.ID, action, op string) (domainkit.Change, error) {
	c, err := s.loadCase(ctx, tx, id, false)
	if err != nil {
		return domainkit.Change{}, err
	}
	ev, err := s.rt.Event(ctx, "admin.case.updated.v1", "review_case", id, map[string]any{"case_id": c.ID, "kind": c.Kind, "state": c.State, "operation": op, "version": c.Version, "source_service": c.SourceService, "source_reference": c.SourceReference})
	if err != nil {
		return domainkit.Change{}, err
	}
	return domainkit.Change{Body: c, Audit: &audit.Event{Action: action, TargetType: "review_case", TargetID: id, Changes: map[string]any{"state": c.State}}, Events: []events.Envelope{ev}}, nil
}

// intakeInput opens a case from an owning service.
type intakeInput struct {
	Kind             string  `json:"kind"`
	SourceReference  string  `json:"source_reference"`
	Title            string  `json:"title"`
	Summary          string  `json:"summary"`
	SubjectService   *string `json:"subject_service"`
	SubjectAccountID *ids.ID `json:"subject_account_id"`
	AmountMinor      *int64  `json:"amount_minor"`
	Currency         *string `json:"currency"`
}

// intake opens a moderation, financial remedy or high-risk case from the
// service that owns the subject (idempotent per source reference).
func (s *svc) intake(w http.ResponseWriter, r *http.Request) {
	caller, _ := serviceCaller(r)
	var in intakeInput
	s.rt.Command(w, r, "case.intake", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if !slices.Contains([]string{"MODERATION", "FINANCIAL_REMEDY", "HIGH_RISK"}, in.Kind) || len(in.SourceReference) < 3 || len(in.SourceReference) > 200 || len(strings.TrimSpace(in.Title)) < 3 || len(in.Title) > 200 || len(in.Summary) > 4000 {
			return domainkit.Change{}, apperrors.InvalidRequest("A moderation, financial remedy or high-risk case with a source reference and title is required.")
		}
		if in.Kind == "FINANCIAL_REMEDY" && (in.AmountMinor == nil || *in.AmountMinor <= 0 || in.Currency == nil || len(*in.Currency) != 3) {
			return domainkit.Change{}, apperrors.InvalidRequest("A financial remedy needs a positive amount and currency.")
		}
		if in.SubjectService != nil && !slices.Contains(subjectServices, platform.Service(*in.SubjectService)) {
			return domainkit.Change{}, apperrors.InvalidRequest("Unsupported subject service.")
		}
		org := orgFrom(ctx)
		id := ids.New()
		tag, err := tx.Exec(ctx, `INSERT INTO review_cases(id,kind,source_service,source_reference,organisation_id,subject_service,subject_account_id,title,summary,amount_minor,currency,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'OPEN') ON CONFLICT (source_service,source_reference,kind) DO NOTHING`,
			id, in.Kind, caller, in.SourceReference, org, in.SubjectService, in.SubjectAccountID, strings.TrimSpace(in.Title), in.Summary, in.AmountMinor, in.Currency)
		if err != nil {
			return domainkit.Change{}, err
		}
		if tag.RowsAffected() == 0 {
			c, err := scanCase(tx.QueryRow(ctx, `SELECT `+caseColumns+` FROM review_cases WHERE source_service=$1 AND source_reference=$2 AND kind=$3`, caller, in.SourceReference, in.Kind))
			return domainkit.Change{Body: c}, err
		}
		ch, err := s.caseChange(ctx, tx, id, "case.opened", "OPEN")
		ch.Status = http.StatusCreated
		return ch, err
	})
}

// bySource is the authoritative readback for the service that raised a case.
func (s *svc) bySource(w http.ResponseWriter, r *http.Request) {
	caller, _ := serviceCaller(r)
	q := r.URL.Query()
	c, err := scanCase(s.rt.Pool.QueryRow(r.Context(), `SELECT `+caseColumns+` FROM review_cases WHERE source_service=$1 AND source_reference=$2 AND kind=$3`, caller, q.Get("reference"), q.Get("kind")))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusOK, c)
}

type appealInput struct {
	AppellantReference string `json:"appellant_reference"`
	Grounds            string `json:"grounds"`
}

// appeal opens the single appeal of a decided case.
func (s *svc) appeal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	caller, _ := serviceCaller(r)
	var in appealInput
	s.rt.Command(w, r, "case.appeal", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if len(strings.TrimSpace(in.Grounds)) < 10 || len(in.Grounds) > 4000 || len(in.AppellantReference) < 3 || len(in.AppellantReference) > 200 {
			return domainkit.Change{}, apperrors.InvalidRequest("Appellant reference and grounds (10-4000 characters) are required.")
		}
		parent, err := s.loadCase(ctx, tx, id, true)
		if err != nil {
			return domainkit.Change{}, err
		}
		if parent.State != "DECIDED" || parent.Kind == "APPEAL" {
			return domainkit.Change{}, apperrors.Conflict("NOT_APPEALABLE", "Only a decided, non-appeal case can be appealed.")
		}
		aid := ids.New()
		tag, err := tx.Exec(ctx, `INSERT INTO review_cases(id,kind,source_service,source_reference,organisation_id,parent_case_id,subject_service,subject_account_id,title,summary,state) VALUES($1,'APPEAL',$2,$3,$4,$5,$6,$7,$8,$9,'OPEN') ON CONFLICT DO NOTHING`,
			aid, caller, "appeal:"+parent.ID.String(), parent.OrganisationID, parent.ID, parent.SubjectService, parent.SubjectAccountID, truncate("Appeal: "+parent.Title, 200), "Appellant "+in.AppellantReference+": "+strings.TrimSpace(in.Grounds))
		if err != nil {
			return domainkit.Change{}, err
		}
		if tag.RowsAffected() == 0 {
			return domainkit.Change{}, apperrors.Conflict("APPEAL_EXISTS", "This case already has an appeal.")
		}
		ch, err := s.caseChange(ctx, tx, aid, "case.appealed", "OPEN")
		ch.Status = http.StatusCreated
		return ch, err
	})
}

// onDispute opens a DISPUTE case when Business submits a dispute for
// platform review, and withdraws an undecided case when Business does.
func (s *svc) onDispute(ctx context.Context, tx pgx.Tx, e events.Envelope, b domainkit.BusinessEvent) error {
	if b.Snapshot.Domain != "dispute" {
		return nil
	}
	switch b.State {
	case "SUBMITTED":
		summary, _ := b.Snapshot.Attributes["claim_summary"].(string)
		title := b.Snapshot.Title
		if len(title) < 3 {
			title = "Business dispute"
		}
		// A requested remedy is carried only with its currency (never assumed).
		var amount *int64
		currency := b.Snapshot.Currency
		if v, ok := b.Snapshot.Attributes["requested_remedy_minor"].(float64); ok && v > 0 && currency != nil {
			n := int64(v)
			amount = &n
		} else {
			currency = nil
		}
		id := ids.New()
		tag, err := tx.Exec(ctx, `INSERT INTO review_cases(id,kind,source_service,source_reference,organisation_id,business_record_id,business_version,title,summary,amount_minor,currency,state) VALUES($1,'DISPUTE',$2,$3,$4,$5,$6,$7,$8,$9,$10,'OPEN') ON CONFLICT (source_service,source_reference,kind) DO NOTHING`,
			id, string(platform.BusinessOperationsService), b.TargetID.String(), b.OrganisationID, b.TargetID, b.Version, truncate(title, 200), truncate(summary, 4000), amount, currency)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		ev, err := s.rt.Event(ctx, "admin.case.updated.v1", "review_case", id, map[string]any{"case_id": id, "kind": "DISPUTE", "state": "OPEN", "operation": "OPEN", "version": 1, "source_service": string(platform.BusinessOperationsService), "source_reference": b.TargetID.String()})
		if err != nil {
			return err
		}
		return s.rt.Outbox.Add(ctx, tx, ev)
	case "WITHDRAWN":
		_, err := tx.Exec(ctx, `UPDATE review_cases SET state='WITHDRAWN',version=version+1,updated_at=now() WHERE kind='DISPUTE' AND source_service=$1 AND source_reference=$2 AND state IN ('OPEN','IN_REVIEW','PROPOSED')`, string(platform.BusinessOperationsService), b.TargetID.String())
		return err
	}
	return nil
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

// DisputeHandler exposes the Business dispute handler (tests deliver
// events directly; the worker consumes them from JetStream).
func DisputeHandler(rt *domainkit.Runtime) domainkit.BusinessHandler {
	return (&svc{rt: rt}).onDispute
}
