package usecases

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/totp"
)

func serviceCaller(r *http.Request) (string, bool) {
	p, ok := serviceauth.PrincipalFrom(r.Context())
	return string(p.Service), ok
}

func orgFrom(ctx context.Context) *ids.ID {
	org := requestcontext.From(ctx).OrganizationID
	if org.IsZero() {
		return nil
	}
	return &org
}

// --- TOTP step-up -----------------------------------------------------------

func (s *svc) mfaAAD(account ids.ID) []byte { return []byte("admin-mfa:" + account.String()) }

// mfaEnrol issues a new (unconfirmed) seed; a confirmed factor cannot be
// replaced through this route.
func (s *svc) mfaEnrol(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sealer == nil {
		domainkit.Fail(w, r, apperrors.Unavailable("MFA is not configured for this deployment."))
		return
	}
	me := domainkit.ActorID(r.Context())
	acct, _ := domainkit.AccountFrom(r.Context())
	secret := totp.NewSecret()
	err := database.WithTx(r.Context(), s.rt.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO admin_mfa(account_id,key_id,sealed_secret) VALUES($1,$2,$3)
			ON CONFLICT (account_id) DO UPDATE SET key_id=EXCLUDED.key_id,sealed_secret=EXCLUDED.sealed_secret,last_step=0,updated_at=now() WHERE admin_mfa.confirmed_at IS NULL`,
			me, s.deps.Sealer.KeyID(), s.deps.Sealer.Seal(secret, s.mfaAAD(me)))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperrors.Conflict("MFA_ALREADY_ENROLLED", "A confirmed MFA factor already exists.")
		}
		_, err = s.rt.Audit.Record(ctx, tx, audit.Event{Action: "mfa.enrolment_started", TargetType: "account", TargetID: me})
		return err
	})
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusCreated, map[string]any{"secret": totp.Encode(secret), "otpauth_uri": totp.URI("Cosmicforge Admin", acct.Email, secret)})
}

type codeInput struct {
	Code string `json:"code"`
}

func (s *svc) mfaConfirm(w http.ResponseWriter, r *http.Request) {
	var in codeInput
	if err := httpx.DecodeJSON(w, r, &in, 4096); err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	me := domainkit.ActorID(r.Context())
	err := database.WithTx(r.Context(), s.rt.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.verifyCode(ctx, tx, me, in.Code, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE admin_mfa SET confirmed_at=now(),updated_at=now() WHERE account_id=$1`, me); err != nil {
			return err
		}
		_, err := s.rt.Audit.Record(ctx, tx, audit.Event{Action: "mfa.confirmed", TargetType: "account", TargetID: me})
		return err
	})
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"mfa": "CONFIRMED"})
}

// verifyCode checks a TOTP code for account and consumes its time step
// (a code can never be used twice).
func (s *svc) verifyCode(ctx context.Context, tx pgx.Tx, account ids.ID, code string, confirmed bool) (int64, error) {
	if s.deps.Sealer == nil {
		return 0, apperrors.Unavailable("MFA is not configured for this deployment.")
	}
	var keyID string
	var sealed []byte
	var confirmedAt *time.Time
	var last int64
	err := tx.QueryRow(ctx, `SELECT key_id,sealed_secret,confirmed_at,last_step FROM admin_mfa WHERE account_id=$1 FOR UPDATE`, account).Scan(&keyID, &sealed, &confirmedAt, &last)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && confirmed && confirmedAt == nil {
		return 0, apperrors.New(http.StatusForbidden, "MFA_REQUIRED", "A confirmed MFA factor is required for this action.")
	}
	if err != nil {
		return 0, err
	}
	secret, err := s.deps.Sealer.Open(keyID, sealed, s.mfaAAD(account))
	if err != nil {
		return 0, apperrors.Unavailable("The MFA factor cannot be read with the configured key.")
	}
	step, err := totp.Verify(secret, code, s.rt.Now())
	if err != nil || step <= last {
		return 0, apperrors.New(http.StatusForbidden, "MFA_INVALID", "The MFA code is invalid or already used.")
	}
	_, err = tx.Exec(ctx, `UPDATE admin_mfa SET last_step=$2,updated_at=now() WHERE account_id=$1`, account, step)
	return step, err
}

// stepUp verifies the X-MFA-Code of the acting supervisor in its own
// transaction, before any authoritative call to another service.
func (s *svc) stepUp(ctx context.Context, r *http.Request) error {
	return database.WithTx(ctx, s.rt.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := s.verifyCode(ctx, tx, domainkit.ActorID(ctx), r.Header.Get(HeaderMFA), true)
		return err
	})
}

// --- Role grants ------------------------------------------------------------

type rolesInput struct {
	Roles  []string `json:"roles"`
	Reason string   `json:"reason"`
}

func (s *svc) grantRoles(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in rolesInput
	pre := func(ctx context.Context) error {
		for _, role := range in.Roles {
			if role != RoleReviewer && role != RoleSupervisor {
				return apperrors.InvalidRequest("Only REVIEWER and SUPERVISOR can be granted.")
			}
		}
		if len(strings.TrimSpace(in.Reason)) < 5 {
			return apperrors.InvalidRequest("A reason is required.")
		}
		if id == domainkit.ActorID(ctx) {
			return apperrors.PermissionDenied("Supervisors cannot change their own roles.")
		}
		return s.stepUp(ctx, r)
	}
	s.rt.CommandWithPre(w, r, "account.roles", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		roles := slices.Compact(slices.Sorted(slices.Values(in.Roles)))
		if roles == nil {
			roles = []string{}
		}
		if err := s.rt.Accounts.GrantRoles(ctx, tx, id, roles); err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Body: map[string]any{"account_id": id, "roles": roles}, Audit: &audit.Event{Action: "account.roles_granted", TargetType: "account", TargetID: id, Changes: map[string]any{"roles": roles}, Metadata: map[string]any{"reason": in.Reason}}}, nil
	})
}

// --- Restrictions -----------------------------------------------------------

// Restriction suspends an account in the domain service that owns it.
type Restriction struct {
	ID               ids.ID    `json:"id"`
	CaseID           ids.ID    `json:"case_id"`
	SubjectService   string    `json:"subject_service"`
	SubjectAccountID ids.ID    `json:"subject_account_id"`
	Reason           string    `json:"reason"`
	State            string    `json:"state"`
	ProposedBy       ids.ID    `json:"proposed_by"`
	ApprovedBy       *ids.ID   `json:"approved_by"`
	LiftedBy         *ids.ID   `json:"lifted_by"`
	Version          int64     `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
}

const restrictionColumns = `id,case_id,subject_service,subject_account_id,reason,state,proposed_by,approved_by,lifted_by,version,created_at`

func scanRestriction(row pgx.Row) (Restriction, error) {
	var x Restriction
	err := row.Scan(&x.ID, &x.CaseID, &x.SubjectService, &x.SubjectAccountID, &x.Reason, &x.State, &x.ProposedBy, &x.ApprovedBy, &x.LiftedBy, &x.Version, &x.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, apperrors.NotFound("RESTRICTION_NOT_FOUND", "Restriction not found.")
	}
	return x, err
}

func (s *svc) listRestrictions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT `+restrictionColumns+` FROM restrictions WHERE ($1='' OR state=$1) ORDER BY created_at DESC LIMIT 200`, r.URL.Query().Get("state"))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Restriction, error) { return scanRestriction(row) })
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": items})
}

type restrictionInput struct {
	CaseID           ids.ID `json:"case_id"`
	SubjectService   string `json:"subject_service"`
	SubjectAccountID ids.ID `json:"subject_account_id"`
	Reason           string `json:"reason"`
	Version          int64  `json:"version"`
}

// proposeRestriction is the maker step; it is grounded in a decided case
// whose outcome warrants it.
func (s *svc) proposeRestriction(w http.ResponseWriter, r *http.Request) {
	var in restrictionInput
	s.rt.Command(w, r, "restriction.propose", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if !slices.Contains(subjectServices, platform.Service(in.SubjectService)) || in.SubjectAccountID.ValidateV7() != nil || len(strings.TrimSpace(in.Reason)) < 5 || len(in.Reason) > 1000 {
			return domainkit.Change{}, apperrors.InvalidRequest("A supported subject service, account and reason are required.")
		}
		c, err := s.loadCase(ctx, tx, in.CaseID, false)
		if err != nil {
			return domainkit.Change{}, err
		}
		if c.State != "DECIDED" || c.Outcome == nil || !slices.Contains([]string{"RESTRICT", "UPHELD", "PARTIALLY_UPHELD", "REMOVE"}, *c.Outcome) || c.Kind == "APPEAL" {
			return domainkit.Change{}, apperrors.Conflict("CASE_NOT_GROUNDS", "A restriction needs a decided case whose outcome warrants it.")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO restrictions(id,case_id,subject_service,subject_account_id,reason,state,proposed_by) VALUES($1,$2,$3,$4,$5,'PROPOSED',$6)`, id, c.ID, in.SubjectService, in.SubjectAccountID, strings.TrimSpace(in.Reason), domainkit.ActorID(ctx)); err != nil {
			if database.IsUniqueViolation(err, "restrictions_one_live") {
				return domainkit.Change{}, apperrors.Conflict("RESTRICTION_EXISTS", "The account already has a live restriction.")
			}
			return domainkit.Change{}, err
		}
		ch, err := s.restrictionChange(ctx, tx, id, "restriction.proposed")
		ch.Status = http.StatusCreated
		return ch, err
	})
}

// setSubjectStatus asks the owning domain service to change the account
// status (it owns the account, its sessions and its enforcement).
func (s *svc) setSubjectStatus(ctx context.Context, x Restriction, status, reason string) error {
	c := s.deps.Subjects[platform.Service(x.SubjectService)]
	if c == nil {
		return apperrors.Unavailable("The owning service of the account is not configured.")
	}
	err := c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/accounts/" + x.SubjectAccountID.String() + "/status", IdempotencyKey: "admin-restriction:" + x.ID.String() + ":" + status, Body: map[string]any{"status": status, "reason": truncate(reason, 500)}}, nil)
	if err != nil {
		return httpx.ToAppError(err)
	}
	return nil
}

func (s *svc) restrictionTransition(name, from, to, status string, mfa bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "id")
		if err != nil {
			domainkit.Fail(w, r, err)
			return
		}
		var in restrictionInput
		var x Restriction
		pre := func(ctx context.Context) error {
			var err error
			if x, err = scanRestriction(s.rt.Pool.QueryRow(ctx, `SELECT `+restrictionColumns+` FROM restrictions WHERE id=$1`, id)); err != nil {
				return err
			}
			switch {
			case x.State != from:
				return apperrors.Conflict("INVALID_TRANSITION", "The restriction is "+x.State+".")
			case in.Version > 0 && in.Version != x.Version:
				return apperrors.Conflict("VERSION_CONFLICT", "The restriction version has changed.")
			case len(strings.TrimSpace(in.Reason)) < 5:
				return apperrors.InvalidRequest("A reason is required.")
			case to == "ACTIVE" && x.ProposedBy == domainkit.ActorID(ctx):
				return apperrors.PermissionDenied("The proposer cannot approve the same restriction (maker-checker).")
			}
			if mfa {
				if err = s.stepUp(ctx, r); err != nil {
					return err
				}
			}
			if status != "" {
				return s.setSubjectStatus(ctx, x, status, in.Reason)
			}
			return nil
		}
		s.rt.CommandWithPre(w, r, "restriction."+name, &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
			me := domainkit.ActorID(ctx)
			sql := map[string]string{
				"ACTIVE":   `UPDATE restrictions SET state='ACTIVE',approved_by=$2,version=version+1,updated_at=now() WHERE id=$1 AND version=$3`,
				"REJECTED": `UPDATE restrictions SET state='REJECTED',approved_by=NULL,version=version+1,updated_at=now() WHERE id=$1 AND version=$3 AND $2::uuid IS NOT NULL`,
				"LIFTED":   `UPDATE restrictions SET state='LIFTED',lifted_by=$2,lift_reason=$4,version=version+1,updated_at=now() WHERE id=$1 AND version=$3`,
			}[to]
			args := []any{id, me, x.Version}
			if to == "LIFTED" {
				args = append(args, strings.TrimSpace(in.Reason))
			}
			tag, err := tx.Exec(ctx, sql, args...)
			if err != nil {
				return domainkit.Change{}, err
			}
			if tag.RowsAffected() == 0 {
				return domainkit.Change{}, apperrors.Conflict("VERSION_CONFLICT", "The restriction changed concurrently.")
			}
			return s.restrictionChange(ctx, tx, id, "restriction."+name)
		})
	}
}

func (s *svc) approveRestriction(w http.ResponseWriter, r *http.Request) {
	s.restrictionTransition("approved", "PROPOSED", "ACTIVE", "SUSPENDED", true)(w, r)
}

func (s *svc) rejectRestriction(w http.ResponseWriter, r *http.Request) {
	s.restrictionTransition("rejected", "PROPOSED", "REJECTED", "", false)(w, r)
}

func (s *svc) liftRestriction(w http.ResponseWriter, r *http.Request) {
	s.restrictionTransition("lifted", "ACTIVE", "LIFTED", "ACTIVE", true)(w, r)
}

func (s *svc) restrictionChange(ctx context.Context, tx pgx.Tx, id ids.ID, action string) (domainkit.Change, error) {
	x, err := scanRestriction(tx.QueryRow(ctx, `SELECT `+restrictionColumns+` FROM restrictions WHERE id=$1`, id))
	if err != nil {
		return domainkit.Change{}, err
	}
	ev, err := s.rt.Event(ctx, "admin.restriction.updated.v1", "restriction", id, map[string]any{"restriction_id": x.ID, "case_id": x.CaseID, "subject_service": x.SubjectService, "subject_account_id": x.SubjectAccountID, "state": x.State, "version": x.Version})
	if err != nil {
		return domainkit.Change{}, err
	}
	return domainkit.Change{Body: x, Audit: &audit.Event{Action: action, TargetType: "restriction", TargetID: id, Changes: map[string]any{"state": x.State}}, Events: []events.Envelope{ev}}, nil
}
