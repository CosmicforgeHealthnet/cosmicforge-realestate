package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

type invitationCommand struct {
	Email        string              `json:"email"`
	RoleID       ids.ID              `json:"role_id"`
	ScopeKind    string              `json:"scope_kind"`
	ScopeID      ids.ID              `json:"scope_id,omitempty"`
	DepartmentID *ids.ID             `json:"department_id,omitempty"`
	TeamID       *ids.ID             `json:"team_id,omitempty"`
	Kind         string              `json:"kind"`
	EffectiveAt  time.Time           `json:"effective_at,omitempty"`
	ExpiresAt    *time.Time          `json:"expires_at,omitempty"`
	Grants       []scopeGrantCommand `json:"grants"`
	Reason       string              `json:"reason"`
	ApprovalID   ids.ID              `json:"approval_id,omitempty"`
}

func (h *Handler) inviteStaff(w http.ResponseWriter, r *http.Request) {
	var in invitationCommand
	if !decode(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	parsed, err := mail.ParseAddress(in.Email)
	if err != nil || parsed.Address != in.Email || len(in.Email) > 254 || in.RoleID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid invitation fields."))
		return
	}
	if in.ScopeKind == "" {
		in.ScopeKind = "COMPANY"
	}
	if in.Kind == "" {
		in.Kind = "EMPLOYEE"
	}
	if !validMembershipKind(in.Kind, in.ExpiresAt) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Temporary and contractor memberships require an expiry."))
		return
	}
	acceptURL, err := url.Parse(h.InvitationAcceptURL)
	if err != nil || acceptURL.Scheme != "https" || acceptURL.Host == "" || acceptURL.User != nil || acceptURL.RawQuery != "" || acceptURL.Fragment != "" {
		reply(w, r, 0, nil, apperrors.Unavailable("A secure business invitation frontend URL must be configured."))
		return
	}
	if err := h.Auth.Service.RateLimit(r.Context(), "staff-invite:"+requestcontext.From(r.Context()).Actor.ID.String(), 20); err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	operation := in
	operation.ApprovalID = ids.Nil
	h.execute(w, r, "staff:invite", in, "staff:invite", in.ScopeKind, in.ScopeID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "staff:invite", in.ScopeKind, in.ScopeID, true)
		if err != nil {
			return 0, nil, err
		}
		if err := h.requirePrivilegeChange(ctx, tx, t, approvals.ActionStaffRoleChange, target.Scope, in.ApprovalID, operation); err != nil {
			return 0, nil, err
		}
		now := h.Auth.Service.Now().UTC()
		effective := in.EffectiveAt
		if effective.IsZero() {
			effective = now
		}
		if effective.Before(now.Add(-5*time.Second)) || in.ExpiresAt != nil && !in.ExpiresAt.After(effective) {
			return 0, nil, apperrors.InvalidRequest("Invalid membership dates.")
		}
		grants, err := h.validateStaffGrants(ctx, tx, t, in.RoleID, target, in.Grants, effective, in.ExpiresAt)
		if err != nil {
			return 0, nil, err
		}
		if err := h.validateStaffTeam(ctx, tx, t, target, in.DepartmentID, in.TeamID); err != nil {
			return 0, nil, err
		}
		// A provisional account has no credentials and cannot log in. Its ID is a
		// real domain-owned recipient reference for Notification Service.
		candidate := ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO business_accounts(id,email,display_name,status) VALUES($1,$2,$2,'INVITED') ON CONFLICT(email) DO NOTHING`, candidate, in.Email); err != nil {
			return 0, nil, err
		}
		var account ids.ID
		var accountStatus string
		if err := tx.QueryRow(ctx, `SELECT id,status FROM business_accounts WHERE email=$1 FOR UPDATE`, in.Email).Scan(&account, &accountStatus); err != nil {
			return 0, nil, err
		}
		if account == t.Account || accountStatus != "INVITED" && accountStatus != "ACTIVE" {
			return 0, nil, apperrors.PermissionDenied("This account is not eligible for this invitation.")
		}
		if _, err := tx.Exec(ctx, `UPDATE staff_invitations SET status='EXPIRED',encrypted_delivery=''::bytea WHERE organisation_id=$1 AND email=$2 AND status='PENDING' AND expires_at<=$3`, t.Organisation, in.Email, now); err != nil {
			return 0, nil, err
		}
		var duplicate bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staff_invitations WHERE organisation_id=$1 AND email=$2 AND status='PENDING')`, t.Organisation, in.Email).Scan(&duplicate); err != nil {
			return 0, nil, err
		}
		if duplicate {
			return 0, nil, apperrors.Conflict("INVITATION_EXISTS", "An active invitation already exists.")
		}
		member := ids.New()
		var previous string
		err = tx.QueryRow(ctx, `SELECT id,status FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2 FOR UPDATE`, t.Organisation, account).Scan(&member, &previous)
		if err == nil {
			if previous != "INVITED" {
				return 0, nil, apperrors.Conflict("MEMBERSHIP_EXISTS", "A membership already exists for this account.")
			}
			if _, err := tx.Exec(ctx, `UPDATE staff_memberships SET role_id=$3,department_id=$4,team_id=$5,kind=$6,effective_at=$7,expires_at=$8,scope_kind=$9,scope_id=$10,version=version+1,updated_at=$11 WHERE organisation_id=$1 AND id=$2`, t.Organisation, member, in.RoleID, in.DepartmentID, in.TeamID, in.Kind, effective, in.ExpiresAt, target.Scope.Kind, target.Scope.ID, now); err != nil {
				return 0, nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE staff_scope_grants SET revoked_at=$3 WHERE organisation_id=$1 AND membership_id=$2 AND revoked_at IS NULL`, t.Organisation, member, now); err != nil {
				return 0, nil, err
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			if _, err := tx.Exec(ctx, `INSERT INTO staff_memberships(id,organisation_id,account_id,role_id,department_id,team_id,kind,status,effective_at,expires_at,scope_kind,scope_id) VALUES($1,$2,$3,$4,$5,$6,$7,'INVITED',$8,$9,$10,$11)`, member, t.Organisation, account, in.RoleID, in.DepartmentID, in.TeamID, in.Kind, effective, in.ExpiresAt, target.Scope.Kind, target.Scope.ID); err != nil {
				return 0, nil, err
			}
		} else {
			return 0, nil, err
		}
		if err := insertStaffGrants(ctx, tx, t.Organisation, member, grants); err != nil {
			return 0, nil, err
		}
		invite := ids.New()
		rawToken := "cfbi_" + security.RandomToken(32)
		expires := now.Add(24 * time.Hour)
		if in.ExpiresAt != nil && in.ExpiresAt.Before(expires) {
			expires = *in.ExpiresAt
		}
		var authorizationVersion int64
		if err := tx.QueryRow(ctx, `SELECT authorization_version FROM organisations WHERE id=$1`, t.Organisation).Scan(&authorizationVersion); err != nil {
			return 0, nil, err
		}
		scopes, err := json.Marshal(in.Grants)
		if err != nil {
			return 0, nil, err
		}
		ciphertext := h.Auth.Service.Sealer.Seal([]byte(rawToken), []byte("business-invitation:"+invite.String()))
		_, err = tx.Exec(ctx, `INSERT INTO staff_invitations(id,organisation_id,email,role_id,token_hash,encrypted_delivery,delivery_key_id,invited_by,status,scopes,expires_at,account_id,membership_id,authorization_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'PENDING',$9,$10,$11,$12,$13)`, invite, t.Organisation, in.Email, in.RoleID, security.SHA256Hex([]byte(rawToken)), ciphertext, h.Auth.Service.Sealer.KeyID(), t.Account, scopes, expires, account, member, authorizationVersion)
		if database.IsUniqueViolation(err, "") {
			return 0, nil, apperrors.Conflict("INVITATION_EXISTS", "An active invitation already exists.")
		}
		if err != nil {
			return 0, nil, err
		}
		link := *acceptURL
		link.Fragment = url.Values{"invitation_token": []string{rawToken}}.Encode()
		if err := h.queueNotice(ctx, tx, account, "Business staff invitation", "You have been invited to a business organisation. Open "+link.String()+" to accept before "+expires.Format(time.RFC3339)+".", false, "business-invitation:"+invite.String()); err != nil {
			return 0, nil, err
		}
		if err := audit(ctx, tx, t, "STAFF_INVITED", "staff_membership", member, map[string]any{"invitation_id": invite, "account_id": account, "role_id": in.RoleID, "scope": target.Scope, "reason": in.Reason}); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO staff_membership_history(id,organisation_id,membership_id,actor_id,from_status,to_status,reason) VALUES($1,$2,$3,$4,nullif($5,''),'INVITED',$6)`, ids.New(), t.Organisation, member, t.Account, previous, in.Reason); err != nil {
			return 0, nil, err
		}
		if err := h.staffEvent(ctx, tx, t.Organisation, member, account, "INVITED"); err != nil {
			return 0, nil, err
		}
		return 201, map[string]any{"staff_id": member, "account_id": account, "invitation_id": invite, "status": "INVITED", "expires_at": expires, "notification_status": "QUEUED"}, nil
	})
}

func (h *Handler) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token       string `json:"invitation_token"`
		Password    string `json:"password,omitempty"`
		DisplayName string `json:"display_name,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !strings.HasPrefix(in.Token, "cfbi_") || len(in.Token) > 128 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid invitation."))
		return
	}
	if err := h.Auth.Service.RateLimit(r.Context(), "invite-accept-client:"+r.Header.Get("X-Forwarded-For"), 20); err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	execute := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var passwordHash string
		var err error
		if in.Password != "" {
			if passwords.DefaultPolicy.Validate(in.Password) != nil || len(strings.TrimSpace(in.DisplayName)) < 1 || len(in.DisplayName) > 200 {
				reply(w, r, 0, nil, apperrors.InvalidRequest("A valid password and display name are required."))
				return
			}
			passwordHash, err = h.Auth.Service.Passwords.Hash(r.Context(), in.Password)
			if err != nil {
				reply(w, r, 0, nil, err)
				return
			}
		}
		var result map[string]any
		err = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			var invite, org, account, member, inviter ids.ID
			var invitationState, accountState, memberState, orgState string
			var expiry time.Time
			var authorizationVersion, currentVersion int64
			var roleActive bool
			err := tx.QueryRow(ctx, `SELECT i.id,i.organisation_id,i.account_id,i.membership_id,i.invited_by,i.status,i.expires_at,i.authorization_version,o.authorization_version,a.status,m.status,o.state,rt.active FROM staff_invitations i JOIN business_accounts a ON a.id=i.account_id JOIN staff_memberships m ON m.id=i.membership_id AND m.organisation_id=i.organisation_id JOIN role_templates rt ON rt.id=i.role_id AND rt.organisation_id=i.organisation_id JOIN organisations o ON o.id=i.organisation_id WHERE i.token_hash=$1 FOR UPDATE OF i,a,m FOR SHARE OF o,rt`, security.SHA256Hex([]byte(in.Token))).Scan(&invite, &org, &account, &member, &inviter, &invitationState, &expiry, &authorizationVersion, &currentVersion, &accountState, &memberState, &orgState, &roleActive)
			if errors.Is(err, pgx.ErrNoRows) {
				return apperrors.InvalidRequest("Invalid invitation.")
			}
			if err != nil {
				return err
			}
			now := h.Auth.Service.Now().UTC()
			if invitationState != "PENDING" || !now.Before(expiry) || memberState != "INVITED" || !roleActive || authorizationVersion != currentVersion || orgState == "RESTRICTED" || orgState == "SUSPENDED" || orgState == "CLOSURE_PENDING" || orgState == "CLOSED" {
				return apperrors.Conflict("INVITATION_UNAVAILABLE", "Invitation is expired, used, revoked or requires reissue after permission changes.")
			}
			var inviterActive bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staff_memberships m JOIN role_templates r ON r.id=m.role_id AND r.organisation_id=m.organisation_id WHERE m.organisation_id=$1 AND m.account_id=$2 AND m.status='ACTIVE' AND r.active AND m.effective_at<=$3 AND (m.expires_at IS NULL OR m.expires_at>$3) AND EXISTS(SELECT 1 FROM role_permissions p WHERE p.role_id=m.role_id AND p.permission='staff:invite'))`, org, inviter, now).Scan(&inviterActive); err != nil {
				return err
			}
			if !inviterActive {
				return apperrors.PermissionDenied("The invitation issuer is no longer authorized.")
			}
			if accountState == "INVITED" {
				if passwordHash == "" {
					return apperrors.InvalidRequest("Set credentials to activate the invited account.")
				}
				if _, err := tx.Exec(ctx, `INSERT INTO business_credentials(account_id,password_hash) VALUES($1,$2)`, account, passwordHash); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE business_accounts SET status='ACTIVE',display_name=$2,version=version+1,updated_at=$3 WHERE id=$1`, account, in.DisplayName, now); err != nil {
					return err
				}
			} else if accountState == "ACTIVE" {
				principal, ok := businessauth.PrincipalFrom(ctx)
				if !ok || principal.ActorID != account {
					return apperrors.AuthenticationRequired()
				}
				if in.Password != "" {
					return apperrors.InvalidRequest("Existing accounts accept invitations using their authenticated session.")
				}
			} else {
				return apperrors.PermissionDenied("The invited account is unavailable.")
			}
			ctx = requestcontext.WithActor(ctx, requestcontext.Actor{Domain: string(platform.DomainBusinessOperations), ID: account})
			ctx = requestcontext.WithOrganization(ctx, org)
			if _, err := tx.Exec(ctx, `UPDATE staff_invitations SET status='ACCEPTED',accepted_at=$2,encrypted_delivery=''::bytea WHERE id=$1`, invite, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE staff_memberships SET status='ACTIVE',version=version+1,updated_at=$2 WHERE id=$1`, member, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO staff_membership_history(id,organisation_id,membership_id,actor_id,from_status,to_status,reason) VALUES($1,$2,$3,$4,'INVITED','ACTIVE','Invitation accepted')`, ids.New(), org, member, account); err != nil {
				return err
			}
			t := tenant{Account: account, Organisation: org, Member: member}
			if err := audit(ctx, tx, t, "STAFF_INVITATION_ACCEPTED", "staff_membership", member, map[string]any{"invitation_id": invite}); err != nil {
				return err
			}
			if err := h.Auth.Service.RecordSecurity(ctx, tx, account, "STAFF_INVITATION_ACCEPTED", member); err != nil {
				return err
			}
			if err := h.staffEvent(ctx, tx, org, member, account, "ACTIVE"); err != nil {
				return err
			}
			result = map[string]any{"staff_id": member, "account_id": account, "organisation_id": org, "status": "ACTIVE", "login_required": true}
			return nil
		})
		reply(w, r, 200, result, err)
	})
	if r.Header.Get("Authorization") != "" {
		h.Auth.Authenticate(execute).ServeHTTP(w, r)
	} else {
		execute.ServeHTTP(w, r)
	}
}

func (h *Handler) staffEvent(ctx context.Context, tx pgx.Tx, org, member, account ids.ID, status string) error {
	var version int64
	if err := tx.QueryRow(ctx, `SELECT version FROM staff_memberships WHERE organisation_id=$1 AND id=$2`, org, member).Scan(&version); err != nil {
		return err
	}
	event, err := events.New(ctx, platform.BusinessOperationsService, "business.staff.updated.v1", "staff_membership", member, map[string]any{"organisation_id": org, "staff_id": member, "account_id": account, "status": status, "version": version})
	if err != nil {
		return err
	}
	return h.Auth.Service.Outbox.Add(ctx, tx, event)
}
