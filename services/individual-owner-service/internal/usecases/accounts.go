package usecases

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/validation"
)

var (
	localePattern = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	e164Pattern   = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
)

// NormalizeEmail lowercases and trims an email address.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func validatePassword(p passwords.Policy, field, pw string) error {
	switch p.Validate(pw) {
	case nil:
		return nil
	case passwords.ErrTooShort:
		return apperrors.InvalidRequest("Password is too short.", apperrors.Detail{Field: field, Code: "TOO_SHORT", Message: "password is too short"})
	case passwords.ErrTooLong:
		return apperrors.InvalidRequest("Password is too long.", apperrors.Detail{Field: field, Code: "TOO_LONG", Message: "password is too long"})
	}
	return apperrors.InvalidRequest("Password is invalid.", apperrors.Detail{Field: field, Code: "INVALID", Message: "password is invalid"})
}

// RegisterInput is an owner registration.
type RegisterInput struct {
	Email       string
	Password    string
	Phone       string
	DisplayName string
	Locale      string
}

// Register creates a PENDING_VERIFICATION account, starts a session and
// sends an email verification code. Token-issuing commands are never stored
// for idempotent replay (bearer secrets must not rest in idempotency rows);
// a retried registration returns EMAIL_ALREADY_REGISTERED.
func (s *Service) Register(ctx context.Context, in RegisterInput, ci ClientInfo) (AuthResult, *ChallengeView, error) {
	in.Email, in.DisplayName = NormalizeEmail(in.Email), strings.TrimSpace(in.DisplayName)
	if in.Locale == "" {
		in.Locale = "en"
	}
	v := &validation.Validator{}
	v.Email("email", in.Email)
	v.MaxLen("email", in.Email, 254)
	v.Required("display_name", in.DisplayName)
	v.MaxLen("display_name", in.DisplayName, 120)
	v.Check(localePattern.MatchString(in.Locale), "locale", "INVALID", "locale must look like en or en-NG")
	if in.Phone != "" {
		v.PhoneE164("phone", in.Phone)
	}
	if err := v.Err(); err != nil {
		return AuthResult{}, nil, err
	}
	if err := validatePassword(s.Policy.PasswordPolicy, "password", in.Password); err != nil {
		return AuthResult{}, nil, err
	}
	hash, err := s.Passwords.Hash(ctx, in.Password)
	if err != nil {
		return AuthResult{}, nil, err
	}
	var res AuthResult
	var acc db.OwnerAccount
	var chv ChallengeView
	var code string
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		acc, err = s.q.CreateAccount(ctx, tx, db.CreateAccountParams{AccountID: ids.New(), Email: in.Email, PhoneE164: strp(in.Phone),
			DisplayName: in.DisplayName, Locale: in.Locale, PasswordHash: hash, PasswordChangedAt: s.now()})
		if database.IsUniqueViolation(err, "owner_accounts_email_uniq") {
			return domain.ErrEmailTaken()
		}
		if err != nil {
			return err
		}
		ctx = withAccountActor(ctx, acc.AccountID)
		if _, err := tx.Exec(ctx, `INSERT INTO wallet_registration_delivery(account_id,registration_ref) VALUES($1,$2)`, acc.AccountID, "owner-registration:"+acc.AccountID.String()); err != nil {
			return err
		}
		if err := s.q.InsertStatusHistory(ctx, tx, db.InsertStatusHistoryParams{HistoryID: ids.New(), AccountID: acc.AccountID, ToStatus: acc.Status,
			ReasonCode: "REGISTERED", ActorType: audit.ActorUser, ActorDomain: strp("individual-owner"), ActorID: &acc.AccountID,
			AccountVersion: acc.Version, OccurredAt: acc.CreatedAt}); err != nil {
			return err
		}
		if err := s.auditTx(ctx, tx, "owner.account.registered", acc.AccountID, audit.ResultSuccess, map[string]any{"status": acc.Status}, nil); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, "ACCOUNT_REGISTERED", ids.Nil, ids.Nil, ci, nil); err != nil {
			return err
		}
		if res, err = s.startSessionTx(ctx, tx, acc, loginMethod{auth: AuthPassword}, ci); err != nil {
			return err
		}
		chv, code, err = s.issueChallenge(ctx, tx, newChallenge{accountID: acc.AccountID, purpose: PurposeEmailVerification,
			channel: notification.ChannelEmail, destination: acc.Email, ttl: s.Policy.CodeTTL, rateLimited: true})
		return err
	})
	if err != nil {
		return AuthResult{}, nil, err
	}
	// Registration succeeded even if the code cannot be sent now; the owner
	// can request another from an authenticated session.
	if derr := s.deliverCode(ctx, acc.AccountID, &chv, acc.Email, code); derr != nil {
		chv.DeliveryStatus = deliveryFailed
	}
	return res, &chv, nil
}

// GetAccount returns the owner's own account.
func (s *Service) GetAccount(ctx context.Context, subj authz.Subject) (AccountView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActAccountRead, authz.Resource{}); err != nil {
		return AccountView{}, err
	}
	acc, err := s.q.GetAccount(ctx, s.Pool, subj.Account.AccountID)
	if err != nil {
		return AccountView{}, err
	}
	return accountView(acc), nil
}

// UpdateProfileInput is a partial update; nil fields are unchanged.
type UpdateProfileInput struct {
	DisplayName     *string
	Locale          *string
	ExpectedVersion int64
}

// UpdateProfile applies an optimistic-concurrency profile update.
func (s *Service) UpdateProfile(ctx context.Context, subj authz.Subject, in UpdateProfileInput) (AccountView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActAccountUpdate, authz.Resource{}); err != nil {
		return AccountView{}, err
	}
	if in.ExpectedVersion <= 0 {
		return AccountView{}, domain.ErrPreconditionRequired()
	}
	var out db.OwnerAccount
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		acc, err := s.lockAccount(ctx, tx, subj.Account.AccountID)
		if err != nil {
			return err
		}
		name, loc := acc.DisplayName, acc.Locale
		if in.DisplayName != nil {
			name = strings.TrimSpace(*in.DisplayName)
		}
		if in.Locale != nil {
			loc = *in.Locale
		}
		v := &validation.Validator{}
		v.Required("display_name", name)
		v.MaxLen("display_name", name, 120)
		v.Check(localePattern.MatchString(loc), "locale", "INVALID", "locale must look like en or en-NG")
		if err := v.Err(); err != nil {
			return err
		}
		out, err = s.q.UpdateProfile(ctx, tx, db.UpdateProfileParams{DisplayName: name, Locale: loc, Now: s.now(), AccountID: acc.AccountID, ExpectedVersion: in.ExpectedVersion})
		if isNoRows(err) {
			return domain.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "owner.account.profile_updated", acc.AccountID, audit.ResultSuccess,
			map[string]any{"display_name_changed": name != acc.DisplayName, "locale": loc, "version": out.Version}, nil)
	})
	return accountView(out), err
}

// StartEmailVerification sends a code to the account's current email.
func (s *Service) StartEmailVerification(ctx context.Context, subj authz.Subject) (ChallengeView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActContactVerify, authz.Resource{}); err != nil {
		return ChallengeView{}, err
	}
	return s.startContactChallenge(ctx, subj, PurposeEmailVerification, "", func(acc db.OwnerAccount) (string, error) {
		if acc.EmailVerifiedAt != nil {
			return "", apperrors.Conflict("EMAIL_ALREADY_VERIFIED", "The email address is already verified.")
		}
		return acc.Email, nil
	})
}

// StartEmailChange sends a code to a new email address (step-up required).
func (s *Service) StartEmailChange(ctx context.Context, subj authz.Subject, email string) (ChallengeView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActContactChange, authz.Resource{}); err != nil {
		return ChallengeView{}, err
	}
	email = NormalizeEmail(email)
	v := &validation.Validator{}
	v.Email("email", email)
	v.MaxLen("email", email, 254)
	if err := v.Err(); err != nil {
		return ChallengeView{}, err
	}
	return s.startContactChallenge(ctx, subj, PurposeEmailChange, email, func(acc db.OwnerAccount) (string, error) {
		if email == acc.Email {
			return "", apperrors.Conflict("EMAIL_UNCHANGED", "This is already the account's email address.")
		}
		return email, nil
	})
}

// StartPhoneVerification sends an SMS code to phone. Adding a first phone
// needs no step-up; replacing a verified phone does.
func (s *Service) StartPhoneVerification(ctx context.Context, subj authz.Subject, phone string) (ChallengeView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActContactVerify, authz.Resource{}); err != nil {
		return ChallengeView{}, err
	}
	if !e164Pattern.MatchString(phone) {
		return ChallengeView{}, apperrors.InvalidRequest("phone must be E.164.", apperrors.Detail{Field: "phone", Code: "INVALID_PHONE", Message: "must be E.164"})
	}
	purpose := PurposePhoneVerification
	return s.startContactChallenge(ctx, subj, purpose, phone, func(acc db.OwnerAccount) (string, error) {
		if acc.PhoneVerifiedAt != nil {
			if acc.PhoneE164 != nil && *acc.PhoneE164 == phone {
				return "", apperrors.Conflict("PHONE_ALREADY_VERIFIED", "This phone number is already verified.")
			}
			if _, err := s.Authz.Require(ctx, subj, authz.ActContactChange, authz.Resource{}); err != nil {
				return "", err
			}
		}
		return phone, nil
	})
}

func (s *Service) startContactChallenge(ctx context.Context, subj authz.Subject, purpose, _ string, dest func(db.OwnerAccount) (string, error)) (ChallengeView, error) {
	var chv ChallengeView
	var code, destination string
	accountID := subj.Account.AccountID
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		acc, err := s.lockAccount(ctx, tx, accountID)
		if err != nil {
			return err
		}
		if destination, err = dest(acc); err != nil {
			return err
		}
		channel := notification.ChannelEmail
		if purpose == PurposePhoneVerification || purpose == PurposePhoneChange {
			channel = notification.ChannelSMS
		}
		chv, code, err = s.issueChallenge(ctx, tx, newChallenge{accountID: accountID, sessionID: subj.Account.SessionID, purpose: purpose,
			channel: channel, destination: destination, ttl: s.Policy.CodeTTL, rateLimited: true})
		if err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "owner.account.contact_verification_started", accountID, audit.ResultSuccess, nil,
			map[string]any{"purpose": purpose, "channel": channel})
	})
	if err != nil {
		return ChallengeView{}, err
	}
	if err := s.deliverCode(ctx, accountID, &chv, destination, code); err != nil {
		return chv, err
	}
	return chv, nil
}

// ConfirmContact verifies an email/phone code and applies it. Verifying the
// required contact activates a PENDING_VERIFICATION account.
func (s *Service) ConfirmContact(ctx context.Context, subj authz.Subject, kind string, challengeID ids.ID, code string, ci ClientInfo) (AccountView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActContactVerify, authz.Resource{}); err != nil {
		return AccountView{}, err
	}
	purposes := []string{PurposeEmailVerification, PurposeEmailChange}
	if kind == "phone" {
		purposes = []string{PurposePhoneVerification, PurposePhoneChange}
	}
	var out db.OwnerAccount
	err := s.txKeepingAttempts(ctx, func(ctx context.Context, tx pgx.Tx) error {
		ch, err := s.checkChallenge(ctx, tx, challengeID, code, purposes...)
		if err != nil {
			return err
		}
		if ch.AccountID != subj.Account.AccountID || ch.Destination == nil {
			return domain.ErrInvalidCode()
		}
		acc, err := s.lockAccount(ctx, tx, ch.AccountID)
		if err != nil {
			return err
		}
		if err := s.consumeChallenge(ctx, tx, ch.ChallengeID); err != nil {
			return err
		}
		now := s.now()
		event, alert, oldEmail := "", "", ""
		switch ch.Purpose {
		case PurposeEmailVerification, PurposeEmailChange:
			if ch.Purpose == PurposeEmailChange {
				oldEmail, event, alert = acc.Email, "EMAIL_CHANGED", "EMAIL_CHANGED"
				// The new address must still be free at confirmation time.
				if other, err := s.q.GetOpenAccountByEmail(ctx, tx, *ch.Destination); err == nil && other.AccountID != acc.AccountID {
					return domain.ErrEmailTaken()
				} else if err != nil && !isNoRows(err) {
					return err
				}
			} else {
				if *ch.Destination != acc.Email {
					return domain.ErrInvalidCode() // email changed since the code was sent
				}
				event = "EMAIL_VERIFIED"
			}
			out, err = s.q.SetEmailVerified(ctx, tx, db.SetEmailVerifiedParams{Email: *ch.Destination, Now: now, AccountID: acc.AccountID})
			if database.IsUniqueViolation(err, "owner_accounts_email_uniq") {
				return domain.ErrEmailTaken()
			}
		default:
			event = "PHONE_VERIFIED"
			if acc.PhoneVerifiedAt != nil {
				event, alert = "PHONE_CHANGED", "PHONE_CHANGED"
			}
			out, err = s.q.SetPhoneVerified(ctx, tx, db.SetPhoneVerifiedParams{PhoneE164: *ch.Destination, Now: now, AccountID: acc.AccountID})
			if database.IsUniqueViolation(err, "owner_accounts_verified_phone_uniq") {
				return domain.ErrPhoneTaken()
			}
		}
		if err != nil {
			return err
		}
		if err := s.auditTx(ctx, tx, "owner.account.contact_verified", acc.AccountID, audit.ResultSuccess,
			map[string]any{"purpose": ch.Purpose, "version": out.Version}, nil); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, event, subj.Account.SessionID, ids.Nil, ci, nil); err != nil {
			return err
		}
		if alert != "" {
			if err := s.enqueueAlert(ctx, tx, out, alert, oldEmail); err != nil {
				return err
			}
		}
		out, err = s.maybeActivate(ctx, tx, out)
		return err
	})
	if err != nil {
		var ae *apperrors.Error
		if errors.As(err, &ae) {
			return AccountView{}, ae
		}
		return AccountView{}, err
	}
	return accountView(out), nil
}
