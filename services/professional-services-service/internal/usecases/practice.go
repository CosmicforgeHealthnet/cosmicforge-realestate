// Package usecases implements professional-services-service: the owning
// service for valuers, surveyors, lawyers, inspectors and other appointed
// professionals. The Verification Service is the authority for the
// professional's credential; this service never self-certifies one. Business
// Operations owns appointments, data rooms and development milestones; the
// professional owns acceptance, findings, reports and certification
// decisions, which are reported to Business through its signed partner
// contract before they are committed here.
package usecases

import (
	"context"
	"encoding/json"
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
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Role held by professional accounts.
const Role = "PROFESSIONAL"

// Consumer is the durable Business event consumer name.
const Consumer = "professional-services-service-business-practice"

type svc struct {
	rt     *domainkit.Runtime
	verify *httpx.Client
}

// New composes the professional-services service.
func New(rt *domainkit.Runtime) domainkit.App {
	s := &svc{rt: rt}
	if rt.Accounts != nil {
		rt.Accounts.DefaultRoles = []string{Role}
	}
	if url := rt.Cfg.Loader.String("VERIFICATION_SERVICE_URL", ""); url != "" && rt.Cfg.Identity.Signer != nil {
		c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: url, Audience: string(platform.VerificationService), Tokens: rt.Cfg.Identity.Signer, Timeout: 8 * time.Second, MaxAttempts: 3})
		if err == nil {
			s.verify = c
		}
	}
	return s.app()
}

// NewWithVerification composes the service with an explicit Verification
// client (tests point it at a Verification double or a real process).
func NewWithVerification(rt *domainkit.Runtime, verify *httpx.Client) domainkit.App {
	s := &svc{rt: rt, verify: verify}
	if rt.Accounts != nil {
		rt.Accounts.DefaultRoles = []string{Role}
	}
	return s.app()
}

func (s *svc) app() domainkit.App {
	return domainkit.App{
		Actor: func(r chi.Router) {
			r.Use(domainkit.RequireRole(Role))
			r.Get("/profile", s.getProfile)
			r.Put("/profile", s.putProfile)
			r.Post("/profile/verification", s.startVerification)
			r.Post("/profile/verification/refresh", s.refreshVerification)
			r.Get("/appointments", s.listAppointments)
			r.Post("/appointments/{id}/accept", s.appointmentAction("ACCEPT"))
			r.Post("/appointments/{id}/start", s.appointmentAction("START"))
			r.Post("/appointments/{id}/report", s.appointmentAction("SUBMIT_REPORT"))
			r.Post("/appointments/{id}/findings", s.addFinding)
			r.Get("/appointments/{id}/documents/{grant}/authorization", s.documentAuthorization)
			r.Get("/certifications", s.listCertifications)
			r.Post("/certifications/{id}/review", s.certificationAction("START_REVIEW"))
			r.Post("/certifications/{id}/certify", s.certificationAction("CERTIFY"))
			r.Post("/certifications/{id}/reject", s.certificationAction("REJECT"))
		},
		InternalCallers: []platform.Service{platform.BusinessOperationsService, platform.AdminService},
		Internal: func(r chi.Router) {
			r.Get("/professionals/{id}/credential", s.internalCredential)
			r.Post("/accounts/{id}/status", s.internalAccountStatus)
		},
		Consumers: func(ctx context.Context, conn *jetstream.Conn) error {
			s.rt.ConsumeBusiness(ctx, conn, Consumer, []string{"professional.appointment.updated.v1", "development.progress.updated.v1"}, s.onBusiness)
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

func pathID(r *http.Request, name string) (ids.ID, error) {
	id, err := ids.ParseV7(chi.URLParam(r, name))
	if err != nil {
		return ids.Nil, apperrors.InvalidRequest("A canonical identifier is required.")
	}
	return id, nil
}

// Profile is the professional's credential state.
type Profile struct {
	AccountID      ids.ID     `json:"account_id"`
	Profession     string     `json:"profession"`
	LicenseNumber  string     `json:"license_number"`
	State          string     `json:"state"`
	VerificationID *ids.ID    `json:"verification_id"`
	VerifiedAt     *time.Time `json:"verified_at"`
	ExpiresAt      *time.Time `json:"expires_at"`
	Version        int64      `json:"version"`
}

func (s *svc) profile(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, account ids.ID) (Profile, error) {
	var p Profile
	err := q.QueryRow(ctx, `SELECT account_id,profession,license_number,state,verification_id,verified_at,expires_at,version FROM professional_profiles WHERE account_id=$1`, account).
		Scan(&p.AccountID, &p.Profession, &p.LicenseNumber, &p.State, &p.VerificationID, &p.VerifiedAt, &p.ExpiresAt, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, apperrors.NotFound("PROFILE_NOT_FOUND", "Create the professional profile first.")
	}
	return p, err
}

// verified returns the verification reference of a currently verified
// professional, or a permission error.
func (s *svc) verified(ctx context.Context, account ids.ID) (string, error) {
	p, err := s.profile(ctx, s.rt.Pool, account)
	if err != nil {
		return "", err
	}
	if p.State != "VERIFIED" || p.ExpiresAt != nil && !s.rt.Now().Before(*p.ExpiresAt) {
		return "", apperrors.PermissionDenied("A current Verification Service professional credential is required.")
	}
	return "verification-service:" + p.VerificationID.String(), nil
}

func (s *svc) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.profile(r.Context(), s.rt.Pool, domainkit.ActorID(r.Context()))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusOK, p)
}

type profileInput struct {
	Profession    string `json:"profession"`
	LicenseNumber string `json:"license_number"`
}

var professions = []string{"VALUER", "SURVEYOR", "LAWYER", "INSPECTOR", "ARCHITECT", "ENGINEER", "AUDITOR", "QUANTITY_SURVEYOR"}

func (s *svc) putProfile(w http.ResponseWriter, r *http.Request) {
	var in profileInput
	s.rt.Command(w, r, "profile.put", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if !slices.Contains(professions, in.Profession) || len(strings.TrimSpace(in.LicenseNumber)) < 3 || len(in.LicenseNumber) > 100 {
			return domainkit.Change{}, apperrors.InvalidRequest("A supported profession and licence number are required.")
		}
		account := domainkit.ActorID(ctx)
		// Changing licence details invalidates any earlier verification.
		tag, err := tx.Exec(ctx, `INSERT INTO professional_profiles(account_id,id,profession,license_number,state) VALUES($1,$2,$3,$4,'UNVERIFIED') ON CONFLICT (account_id) DO NOTHING`, account, ids.New(), in.Profession, in.LicenseNumber)
		if err != nil {
			return domainkit.Change{}, err
		}
		if tag.RowsAffected() == 0 {
			p, err := s.profile(ctx, tx, account)
			if err != nil {
				return domainkit.Change{}, err
			}
			if p.Profession != in.Profession || p.LicenseNumber != in.LicenseNumber {
				return domainkit.Change{}, apperrors.Conflict("PROFILE_LOCKED", "Credential details change only through a new verification.")
			}
		}
		p, err := s.profile(ctx, tx, account)
		if err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Body: p, Audit: &audit.Event{Action: "professional.profile_saved", TargetType: "account", TargetID: account, Changes: map[string]any{"profession": in.Profession}}}, nil
	})
}

type verificationInput struct {
	MediaID ids.ID `json:"media_id"`
}

// startVerification opens a PROFESSIONAL_CREDENTIAL case at the Verification
// Service with the professional's scanned Media evidence and submits it.
func (s *svc) startVerification(w http.ResponseWriter, r *http.Request) {
	var in verificationInput
	var caseID ids.ID
	pre := func(ctx context.Context) error {
		if s.verify == nil {
			return apperrors.Unavailable("The Verification Service is not configured.")
		}
		if in.MediaID.ValidateV7() != nil {
			return apperrors.InvalidRequest("The credential document Media id is required.")
		}
		account := domainkit.ActorID(ctx)
		p, err := s.profile(ctx, s.rt.Pool, account)
		if err != nil {
			return err
		}
		if p.State == "PENDING" || p.State == "VERIFIED" {
			return apperrors.Conflict("VERIFICATION_IN_PROGRESS", "A verification is already pending or current.")
		}
		var created struct {
			Data struct {
				VerificationID ids.ID `json:"verification_id"`
			} `json:"data"`
		}
		key := "prof-verify:" + account.String() + ":" + in.MediaID.String()
		if err = s.verify.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/verifications", IdempotencyKey: key, Body: map[string]any{
			"verification_type": "PROFESSIONAL_CREDENTIAL", "caller_reference": "professional:" + account.String(),
			"subject":           map[string]any{"subject_type": "PROFESSIONAL", "subject_domain": string(platform.DomainProfessionalServices), "subject_id": account},
			"provider_metadata": map[string]string{"profession": strings.ToLower(p.Profession)}}}, &created); err != nil {
			return httpx.ToAppError(err)
		}
		caseID = created.Data.VerificationID
		if err = s.verify.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/verifications/" + caseID.String() + "/evidence", IdempotencyKey: key + ":evidence", Body: map[string]any{"media_id": in.MediaID, "evidence_type": "PROFESSIONAL_LICENSE"}}, nil); err != nil {
			return httpx.ToAppError(err)
		}
		if err = s.verify.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/verifications/" + caseID.String() + "/submit", IdempotencyKey: key + ":submit"}, nil); err != nil {
			return httpx.ToAppError(err)
		}
		// Verification dispatches a submitted case to its provider only on
		// an explicit processing request.
		if err = s.verify.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/verifications/" + caseID.String() + "/process", IdempotencyKey: key + ":process"}, nil); err != nil {
			return httpx.ToAppError(err)
		}
		return nil
	}
	s.rt.CommandWithPre(w, r, "profile.verification", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		account := domainkit.ActorID(ctx)
		p, err := s.profile(ctx, tx, account)
		if err != nil {
			return domainkit.Change{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE professional_profiles SET state='PENDING',verification_id=$2,verified_at=NULL,expires_at=NULL,version=version+1,updated_at=now() WHERE account_id=$1 AND version=$3`, account, caseID, p.Version); err != nil {
			return domainkit.Change{}, err
		}
		p.State, p.VerificationID, p.Version = "PENDING", &caseID, p.Version+1
		return domainkit.Change{Status: http.StatusAccepted, Body: p, Audit: &audit.Event{Action: "professional.verification_submitted", TargetType: "account", TargetID: account, Metadata: map[string]any{"verification_id": caseID}}}, nil
	})
}

// refreshVerification reads the authoritative case outcome.
func (s *svc) refreshVerification(w http.ResponseWriter, r *http.Request) {
	var outcome struct {
		Status    string     `json:"status"`
		ExpiresAt *time.Time `json:"expires_at"`
		Subject   struct {
			Domain string `json:"subject_domain"`
			ID     ids.ID `json:"subject_id"`
		} `json:"subject"`
	}
	var p Profile
	pre := func(ctx context.Context) error {
		if s.verify == nil {
			return apperrors.Unavailable("The Verification Service is not configured.")
		}
		var err error
		if p, err = s.profile(ctx, s.rt.Pool, domainkit.ActorID(ctx)); err != nil {
			return err
		}
		if p.VerificationID == nil {
			return apperrors.Conflict("NO_VERIFICATION", "Start a verification first.")
		}
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err = s.verify.Do(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/verifications/" + p.VerificationID.String()}, &env); err != nil {
			return httpx.ToAppError(err)
		}
		if err = json.Unmarshal(env.Data, &outcome); err != nil {
			return err
		}
		if outcome.Subject.Domain != string(platform.DomainProfessionalServices) || outcome.Subject.ID != p.AccountID {
			return apperrors.PermissionDenied("The verification case does not belong to this professional.")
		}
		return nil
	}
	s.rt.CommandWithPre(w, r, "profile.verification_refresh", nil, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		to := map[string]string{"VERIFIED": "VERIFIED", "REJECTED": "REJECTED", "EXPIRED": "EXPIRED"}[outcome.Status]
		if to == "" || to == p.State {
			return domainkit.Change{Body: map[string]any{"profile": p, "verification_status": outcome.Status}}, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE professional_profiles SET state=$2,verified_at=CASE WHEN $2='VERIFIED' THEN now() ELSE verified_at END,expires_at=$3,version=version+1,updated_at=now() WHERE account_id=$1 AND version=$4`, p.AccountID, to, outcome.ExpiresAt, p.Version); err != nil {
			return domainkit.Change{}, err
		}
		p, err := s.profile(ctx, tx, p.AccountID)
		if err != nil {
			return domainkit.Change{}, err
		}
		ev, err := s.rt.Event(ctx, "professional.credential.updated.v1", "account", p.AccountID, map[string]any{"account_id": p.AccountID, "state": p.State, "verification_id": p.VerificationID, "expires_at": p.ExpiresAt})
		if err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Body: map[string]any{"profile": p, "verification_status": outcome.Status}, Audit: &audit.Event{Action: "professional.credential_updated", TargetType: "account", TargetID: p.AccountID, Changes: map[string]any{"state": p.State}}, Events: []events.Envelope{ev}}, nil
	})
}

// Appointment is the professional's projection of a Business appointment.
type Appointment struct {
	ID                    ids.ID  `json:"id"`
	BusinessAppointmentID ids.ID  `json:"business_appointment_id"`
	OrganisationID        ids.ID  `json:"organisation_id"`
	PropertyID            ids.ID  `json:"property_id"`
	Kind                  string  `json:"kind"`
	Title                 string  `json:"title"`
	Purpose               string  `json:"purpose"`
	State                 string  `json:"state"`
	BusinessState         string  `json:"business_state"`
	ReportReference       *ids.ID `json:"report_reference"`
	Version               int64   `json:"version"`
}

const apptColumns = `id,business_appointment_id,organisation_id,property_id,kind,title,purpose,state,business_state,report_reference,version`

func scanAppt(row pgx.Row) (Appointment, error) {
	var a Appointment
	err := row.Scan(&a.ID, &a.BusinessAppointmentID, &a.OrganisationID, &a.PropertyID, &a.Kind, &a.Title, &a.Purpose, &a.State, &a.BusinessState, &a.ReportReference, &a.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, apperrors.NotFound("APPOINTMENT_NOT_FOUND", "Appointment not found.")
	}
	return a, err
}

func (s *svc) ownedAppt(ctx context.Context, id ids.ID) (Appointment, error) {
	return scanAppt(s.rt.Pool.QueryRow(ctx, `SELECT `+apptColumns+` FROM appointments WHERE id=$1 AND professional_account_id=$2`, id, domainkit.ActorID(ctx)))
}

func (s *svc) listAppointments(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT `+apptColumns+` FROM appointments WHERE professional_account_id=$1 ORDER BY created_at DESC LIMIT 200`, domainkit.ActorID(r.Context()))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []Appointment{}
	for rows.Next() {
		a, err := scanAppt(rows)
		if err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		out = append(out, a)
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": out})
}

type apptInput struct {
	ReportReference *ids.ID `json:"report_reference,omitempty"`
	Reason          string  `json:"reason"`
}

var apptActions = map[string]struct{ from, to string }{
	"ACCEPT":        {"OFFERED", "ACCEPTED"},
	"START":         {"ACCEPTED", "IN_PROGRESS"},
	"SUBMIT_REPORT": {"IN_PROGRESS", "REPORT_SUBMITTED"},
}

func (s *svc) appointmentAction(action string) http.HandlerFunc {
	spec := apptActions[action]
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "id")
		if err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		var in apptInput
		var a Appointment
		pre := func(ctx context.Context) error {
			var err error
			if a, err = s.ownedAppt(ctx, id); err != nil {
				return err
			}
			if a.State != spec.from {
				return apperrors.Conflict("INVALID_TRANSITION", action+" is not allowed from "+a.State+".")
			}
			body := map[string]any{"action": action, "reason": "Professional " + strings.ToLower(action) + " via professional-services-service"}
			switch action {
			case "ACCEPT":
				body["external_reference"] = "professional-appointment:" + a.ID.String()
			case "SUBMIT_REPORT":
				ref, err := s.verified(ctx, domainkit.ActorID(ctx))
				if err != nil {
					return err
				}
				if in.ReportReference == nil || in.ReportReference.ValidateV7() != nil {
					return apperrors.InvalidRequest("The report's Media reference is required.")
				}
				body["evidence_refs"] = []ids.ID{*in.ReportReference}
				body["attributes"] = map[string]any{"professional_verification_reference": ref}
			}
			_, err = s.rt.PartnerTransition(ctx, a.OrganisationID, "professional_appointment", a.BusinessAppointmentID, domainkit.ActorID(ctx), "professional:"+a.ID.String()+":"+action, body)
			return err
		}
		s.rt.CommandWithPre(w, r, "appointment."+strings.ToLower(action), &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
			st, err := domainkit.Move(ctx, tx, "appointments", a.ID, a.Version, []string{spec.from}, spec.to)
			if err != nil {
				return domainkit.Change{}, err
			}
			a.State, a.Version = st.State, st.Version
			if action == "SUBMIT_REPORT" {
				if _, err = tx.Exec(ctx, `UPDATE appointments SET report_reference=$2,version=version+1 WHERE id=$1`, a.ID, in.ReportReference); err != nil {
					return domainkit.Change{}, err
				}
				a.ReportReference, a.Version = in.ReportReference, a.Version+1
			}
			ev, err := s.rt.Event(ctx, "professional.appointment.updated.v1", "appointment", a.ID, map[string]any{"appointment_id": a.ID, "business_appointment_id": a.BusinessAppointmentID, "organisation_id": a.OrganisationID, "state": a.State, "action": action})
			if err != nil {
				return domainkit.Change{}, err
			}
			return domainkit.Change{Body: a, Audit: &audit.Event{Action: "appointment." + strings.ToLower(action), TargetType: "appointment", TargetID: a.ID, Changes: map[string]any{"state": a.State}}, Events: []events.Envelope{ev}}, nil
		})
	}
}

type findingInput struct {
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	EvidenceRefs []ids.ID `json:"evidence_refs"`
}

func (s *svc) addFinding(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in findingInput
	var a Appointment
	var record domainkit.BusinessOps
	key := r.Header.Get("Idempotency-Key")
	pre := func(ctx context.Context) error {
		var err error
		if a, err = s.ownedAppt(ctx, id); err != nil {
			return err
		}
		if a.State != "IN_PROGRESS" && a.State != "REPORT_SUBMITTED" {
			return apperrors.Conflict("APPOINTMENT_NOT_ACTIVE", "Findings are recorded on an appointment in progress.")
		}
		ref, err := s.verified(ctx, domainkit.ActorID(ctx))
		if err != nil {
			return err
		}
		if len(in.EvidenceRefs) == 0 || len(in.EvidenceRefs) > 20 {
			return apperrors.InvalidRequest("Findings require 1-20 Media evidence references.")
		}
		if err := s.rt.CallBusiness(ctx, a.OrganisationID, http.MethodPost, "/professional-appointments/"+a.BusinessAppointmentID.String()+"/findings", "professional-finding:"+a.ID.String()+":"+key,
			map[string]any{"account_id": domainkit.ActorID(ctx), "finding_key": "pf-" + key, "kind": in.Kind, "title": in.Title, "evidence_refs": in.EvidenceRefs, "professional_verification_reference": ref, "reason": "Professional finding via professional-services-service"}, &record); err != nil {
			return err
		}
		if record.ID.IsZero() {
			return apperrors.Unavailable("Business Operations returned no finding record.")
		}
		return nil
	}
	s.rt.CommandWithPre(w, r, "appointment.finding", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		fid := ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO findings(id,appointment_id,business_finding_id,kind,title,evidence_refs,created_by) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (business_finding_id) DO NOTHING`, fid, a.ID, record.ID, in.Kind, in.Title, in.EvidenceRefs, domainkit.ActorID(ctx)); err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Status: http.StatusCreated, Body: map[string]any{"finding_id": fid, "business_finding_id": record.ID, "state": record.State},
			Audit: &audit.Event{Action: "appointment.finding_recorded", TargetType: "appointment", TargetID: a.ID, Metadata: map[string]any{"business_finding_id": record.ID}}}, nil
	})
}

// documentAuthorization asks Business whether a purpose-limited data-room
// grant on this professional's appointment currently allows access.
func (s *svc) documentAuthorization(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	grant, gerr := pathID(r, "grant")
	var a Appointment
	if err == nil && gerr == nil {
		a, err = s.ownedAppt(r.Context(), id)
	}
	if err == nil {
		err = gerr
	}
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var out map[string]any
	if err = s.rt.CallBusiness(r.Context(), a.OrganisationID, http.MethodGet, "/data-room-grants/"+grant.String()+"/authorization?account_id="+domainkit.ActorID(r.Context()).String(), "", nil, &out); err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"appointment_id": a.ID, "grant_id": grant, "allowed": out["allowed"] == true && a.State != "CANCELLED", "purpose": out["purpose"], "expires_at": out["expires_at"], "download_policy": out["download_policy"]})
}

// Certification is the professional's projection of a Business progress
// certification request.
type Certification struct {
	ID                      ids.ID  `json:"id"`
	BusinessCertificationID ids.ID  `json:"business_certification_id"`
	OrganisationID          ids.ID  `json:"organisation_id"`
	DevelopmentID           ids.ID  `json:"development_id"`
	MilestoneID             string  `json:"milestone_id"`
	Title                   string  `json:"title"`
	State                   string  `json:"state"`
	CertificateReference    *string `json:"certificate_reference"`
	Version                 int64   `json:"version"`
}

func (s *svc) ownedCert(ctx context.Context, id ids.ID) (Certification, error) {
	var c Certification
	err := s.rt.Pool.QueryRow(ctx, `SELECT id,business_certification_id,organisation_id,development_id,milestone_id,title,state,certificate_reference,version FROM certification_requests WHERE id=$1 AND professional_account_id=$2`, id, domainkit.ActorID(ctx)).
		Scan(&c.ID, &c.BusinessCertificationID, &c.OrganisationID, &c.DevelopmentID, &c.MilestoneID, &c.Title, &c.State, &c.CertificateReference, &c.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, apperrors.NotFound("CERTIFICATION_NOT_FOUND", "Certification request not found.")
	}
	return c, err
}

func (s *svc) listCertifications(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT id,business_certification_id,organisation_id,development_id,milestone_id,title,state,certificate_reference,version FROM certification_requests WHERE professional_account_id=$1 ORDER BY created_at DESC LIMIT 200`, domainkit.ActorID(r.Context()))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []Certification{}
	for rows.Next() {
		var c Certification
		if err = rows.Scan(&c.ID, &c.BusinessCertificationID, &c.OrganisationID, &c.DevelopmentID, &c.MilestoneID, &c.Title, &c.State, &c.CertificateReference, &c.Version); err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		out = append(out, c)
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": out})
}

type certInput struct {
	CertificateReference string   `json:"certificate_reference"`
	EvidenceRefs         []ids.ID `json:"evidence_refs"`
	Reason               string   `json:"reason"`
}

var certActions = map[string]struct {
	from []string
	to   string
}{
	"START_REVIEW": {[]string{"REQUESTED"}, "IN_REVIEW"},
	"CERTIFY":      {[]string{"IN_REVIEW"}, "CERTIFIED"},
	"REJECT":       {[]string{"REQUESTED", "IN_REVIEW"}, "REJECTED"},
}

func (s *svc) certificationAction(action string) http.HandlerFunc {
	spec := certActions[action]
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "id")
		if err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		var in certInput
		var c Certification
		pre := func(ctx context.Context) error {
			var err error
			if c, err = s.ownedCert(ctx, id); err != nil {
				return err
			}
			if !slices.Contains(spec.from, c.State) {
				return apperrors.Conflict("INVALID_TRANSITION", action+" is not allowed from "+c.State+".")
			}
			ref, err := s.verified(ctx, domainkit.ActorID(ctx))
			if err != nil {
				return err
			}
			body := map[string]any{"action": action, "reason": "Professional " + strings.ToLower(action) + " via professional-services-service", "attributes": map[string]any{"professional_verification_reference": ref}}
			switch action {
			case "START_REVIEW":
				body["external_reference"] = "professional-certification:" + c.ID.String()
			case "CERTIFY":
				if len(strings.TrimSpace(in.CertificateReference)) < 3 || len(in.EvidenceRefs) == 0 {
					return apperrors.InvalidRequest("A certificate reference and Media evidence are required to certify.")
				}
				body["external_reference"] = in.CertificateReference
				body["evidence_refs"] = in.EvidenceRefs
			case "REJECT":
				if len(in.EvidenceRefs) == 0 {
					return apperrors.InvalidRequest("A rejection requires Media evidence of the inspection.")
				}
				body["evidence_refs"] = in.EvidenceRefs
			}
			_, err = s.rt.PartnerTransition(ctx, c.OrganisationID, "progress_certification", c.BusinessCertificationID, domainkit.ActorID(ctx), "professional-cert:"+c.ID.String()+":"+action, body)
			return err
		}
		s.rt.CommandWithPre(w, r, "certification."+strings.ToLower(action), &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
			var ref any
			if action == "CERTIFY" {
				ref = in.CertificateReference
			}
			tag, err := tx.Exec(ctx, `UPDATE certification_requests SET state=$3,certificate_reference=coalesce($4,certificate_reference),version=version+1,updated_at=now() WHERE id=$1 AND version=$2`, c.ID, c.Version, spec.to, ref)
			if err != nil {
				return domainkit.Change{}, err
			}
			if tag.RowsAffected() == 0 {
				return domainkit.Change{}, apperrors.Conflict("VERSION_CONFLICT", "The certification request changed concurrently.")
			}
			c.State, c.Version = spec.to, c.Version+1
			if action == "CERTIFY" {
				c.CertificateReference = &in.CertificateReference
			}
			ev, err := s.rt.Event(ctx, "professional.certification.updated.v1", "certification_request", c.ID, map[string]any{"certification_request_id": c.ID, "business_certification_id": c.BusinessCertificationID, "organisation_id": c.OrganisationID, "state": c.State, "action": action})
			if err != nil {
				return domainkit.Change{}, err
			}
			return domainkit.Change{Body: c, Audit: &audit.Event{Action: "certification." + strings.ToLower(action), TargetType: "certification_request", TargetID: c.ID, Changes: map[string]any{"state": c.State}}, Events: []events.Envelope{ev}}, nil
		})
	}
}

// onBusiness projects appointments and certification requests naming a
// professional account of this service.
func (s *svc) onBusiness(ctx context.Context, tx pgx.Tx, e events.Envelope, b domainkit.BusinessEvent) error {
	account, ok := s.rt.AccountFromReference(b.Snapshot.CounterpartyReference)
	if !ok {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_accounts WHERE id=$1)`, account).Scan(&exists); err != nil || !exists {
		return err
	}
	switch b.Snapshot.Domain {
	case "professional_appointment":
		if b.PropertyID == nil {
			return nil
		}
		purpose, _ := b.Snapshot.Attributes["purpose"].(string)
		if b.Operation == "CREATED" {
			_, err := tx.Exec(ctx, `INSERT INTO appointments(id,business_appointment_id,organisation_id,property_id,professional_account_id,kind,title,purpose,state,business_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'OFFERED',$9) ON CONFLICT (business_appointment_id) DO NOTHING`,
				ids.New(), b.TargetID, b.OrganisationID, *b.PropertyID, account, b.Snapshot.Kind, truncate(b.Snapshot.Title, 200), truncate(purpose, 500), b.State)
			return err
		}
		var id ids.ID
		var state string
		var version int64
		if err := tx.QueryRow(ctx, `SELECT id,state,version FROM appointments WHERE business_appointment_id=$1 FOR UPDATE`, b.TargetID).Scan(&id, &state, &version); err != nil {
			return nil
		}
		if b.State == "CANCELLED" && slices.Contains([]string{"OFFERED", "ACCEPTED", "IN_PROGRESS"}, state) {
			if _, err := domainkit.Move(ctx, tx, "appointments", id, version, []string{state}, "CANCELLED"); err != nil {
				return err
			}
		}
		if b.State == "COMPLETED" && state == "REPORT_SUBMITTED" {
			if _, err := domainkit.Move(ctx, tx, "appointments", id, version, []string{state}, "COMPLETED"); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE appointments SET business_state=$2,version=version+1,updated_at=now() WHERE id=$1`, id, b.State)
		return err
	case "progress_certification":
		if b.Operation != "CREATED" {
			return nil
		}
		milestone, _ := b.Snapshot.Attributes["milestone_id"].(string)
		_, err := tx.Exec(ctx, `INSERT INTO certification_requests(id,business_certification_id,organisation_id,development_id,professional_account_id,milestone_id,title,state) VALUES($1,$2,$3,$4,$5,$6,$7,'REQUESTED') ON CONFLICT (business_certification_id) DO NOTHING`,
			ids.New(), b.TargetID, b.OrganisationID, b.Snapshot.ScopeID, account, milestone, truncate(b.Snapshot.Title, 200))
		return err
	}
	return nil
}

// internalCredential lets Business or Admin read a professional's current
// verified credential reference.
func (s *svc) internalCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil {
		var p Profile
		if p, err = s.profile(r.Context(), s.rt.Pool, id); err == nil {
			domainkit.Data(w, http.StatusOK, map[string]any{"account_id": p.AccountID, "profession": p.Profession, "state": p.State, "verification_id": p.VerificationID, "expires_at": p.ExpiresAt, "verified": p.State == "VERIFIED"})
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
	id, err := pathID(r, "id")
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

// sweep expires lapsed credentials.
func (s *svc) sweep(ctx context.Context) error {
	_, err := s.rt.Pool.Exec(ctx, `UPDATE professional_profiles SET state='EXPIRED',version=version+1,updated_at=now() WHERE state='VERIFIED' AND expires_at IS NOT NULL AND expires_at<=now()`)
	return err
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}
