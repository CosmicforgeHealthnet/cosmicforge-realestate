package auth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
	"net/http"
	"regexp"
	"time"
)

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

type DeliveryChallenge struct {
	Kind           string    `json:"kind"`
	DeliveryQueued bool      `json:"delivery_queued"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func (s *Service) EnrolDeliveryMFA(ctx context.Context, account, session ids.ID, password, kind, phone string) (Enrolment, error) {
	if kind != "EMAIL" && kind != "SMS" || kind == "SMS" && !e164.MatchString(phone) {
		return Enrolment{}, apperrors.InvalidRequest("Choose EMAIL or a valid E.164 SMS destination.")
	}
	if err := s.mfaReady(); err != nil {
		return Enrolment{}, err
	}
	if err := s.limit(ctx, "mfa-enrol:"+account.String(), 5); err != nil {
		return Enrolment{}, err
	}
	out := Enrolment{MethodID: ids.New(), Kind: kind, DeliveryQueued: true, ExpiresAt: s.Now().UTC().Add(5 * time.Minute)}
	err := database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.liveSession(ctx, tx, account, session); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT id FROM business_accounts WHERE id=$1 FOR UPDATE`, account); err != nil {
			return err
		}
		if err := s.checkPassword(ctx, tx, account, password); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE business_mfa_methods SET status='REMOVED',removed_at=$2,encrypted_secret=''::bytea WHERE account_id=$1 AND status='PENDING' AND expires_at<=$2`, account, s.Now().UTC()); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_mfa_methods WHERE account_id=$1 AND status IN ('ACTIVE','PENDING'))`, account).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return apperrors.Conflict("MFA_METHOD_EXISTS", "Remove the current method with step-up verification before replacing it.")
		}
		contact := phone
		if kind == "EMAIL" {
			if err := tx.QueryRow(ctx, `SELECT email FROM business_accounts WHERE id=$1`, account).Scan(&contact); err != nil {
				return err
			}
		}
		cipher := s.Sealer.Seal([]byte(contact), aad(account, out.MethodID))
		if _, err := tx.Exec(ctx, `INSERT INTO business_mfa_methods(id,account_id,kind,status,key_id,encrypted_secret,expires_at) VALUES($1,$2,$3,'PENDING',$4,$5,$6)`, out.MethodID, account, kind, s.Sealer.KeyID(), cipher, out.ExpiresAt); err != nil {
			return err
		}
		method := mfaMethod{id: out.MethodID, account: account, kind: kind, key: s.Sealer.KeyID(), cipher: cipher}
		if _, err := s.queueMFAOTP(ctx, tx, method, out.MethodID, "ENROL"); err != nil {
			return err
		}
		return s.record(ctx, tx, &account, "MFA_ENROLMENT_STARTED", out.MethodID)
	})
	return out, err
}
func (s *Service) queueMFAOTP(ctx context.Context, tx pgx.Tx, method mfaMethod, binding ids.ID, purpose string) (DeliveryChallenge, error) {
	if method.kind != "EMAIL" && method.kind != "SMS" {
		return DeliveryChallenge{}, apperrors.InvalidRequest("This method uses an authenticator code, not a delivered challenge.")
	}
	contact, err := s.Sealer.Open(method.key, method.cipher, aad(method.account, method.id))
	if err != nil {
		return DeliveryChallenge{}, err
	}
	now := s.Now().UTC()
	expires := now.Add(5 * time.Minute)
	id := ids.New()
	code := credentials.NumericCode(8)
	if _, err = tx.Exec(ctx, `UPDATE business_mfa_one_time_codes SET consumed_at=$4 WHERE method_id=$1 AND binding_id=$2 AND purpose=$3 AND consumed_at IS NULL`, method.id, binding, purpose, now); err != nil {
		return DeliveryChallenge{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO business_mfa_one_time_codes(id,account_id,method_id,binding_id,purpose,code_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, method.account, method.id, binding, purpose, s.hash("mfa-otp:"+method.id.String()+":"+binding.String()+":"+purpose, code), expires); err != nil {
		return DeliveryChallenge{}, err
	}
	request := notification.SendRequest{Recipient: notification.Recipient{Domain: string(platform.DomainBusinessOperations), ID: method.account}, TemplateKey: notification.TemplateVerificationCode, Variables: map[string]string{"code": code}, CallerReference: "business-mfa:" + id.String()}
	if method.kind == "EMAIL" {
		request.Channels = []string{notification.ChannelEmail}
		request.Destinations.Email = string(contact)
	} else {
		request.Channels = []string{notification.ChannelSMS}
		request.Destinations.Phone = string(contact)
	}
	job, err := businessEvents.NotificationRequest(ctx, s.Sealer, ids.New(), request)
	if err != nil {
		return DeliveryChallenge{}, err
	}
	if err = s.Outbox.Add(ctx, tx, job); err != nil {
		return DeliveryChallenge{}, err
	}
	return DeliveryChallenge{Kind: method.kind, DeliveryQueued: true, ExpiresAt: expires}, nil
}
func (s *Service) consumeMFAOTP(ctx context.Context, tx pgx.Tx, method mfaMethod, binding ids.ID, purpose, code string) error {
	var id ids.ID
	var hash string
	var attempts int
	var expiry time.Time
	err := tx.QueryRow(ctx, `SELECT id,code_hash,attempts,expires_at FROM business_mfa_one_time_codes WHERE account_id=$1 AND method_id=$2 AND binding_id=$3 AND purpose=$4 AND consumed_at IS NULL FOR UPDATE`, method.account, method.id, binding, purpose).Scan(&id, &hash, &attempts, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalidMFA()
	}
	if err != nil {
		return err
	}
	if attempts >= 5 || !s.Now().Before(expiry) {
		return invalidMFA()
	}
	if _, err = tx.Exec(ctx, `UPDATE business_mfa_one_time_codes SET attempts=attempts+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if len(code) != 8 || !security.ConstantTimeEqual(hash, s.hash("mfa-otp:"+method.id.String()+":"+binding.String()+":"+purpose, code)) {
		return invalidMFA()
	}
	_, err = tx.Exec(ctx, `UPDATE business_mfa_one_time_codes SET consumed_at=$2 WHERE id=$1`, id, s.Now().UTC())
	return err
}
func (s *Service) SendMFAChallenge(ctx context.Context, account, session ids.ID, key string) (DeliveryChallenge, bool, error) {
	if err := s.mfaReady(); err != nil {
		return DeliveryChallenge{}, false, err
	}
	if err := s.limit(ctx, "mfa-delivery:"+account.String(), 5); err != nil {
		return DeliveryChallenge{}, false, err
	}
	store := idempotency.NewStore(s.Pool, 30*time.Second, time.Hour)
	hash, _ := idempotency.Fingerprint("POST", "mfa-step-up", []byte(session.String()))
	response, err := store.Execute(ctx, idempotency.Key{Scope: "business:mfa-step-up:" + session.String(), CallerType: idempotency.CallerUser, CallerID: account.String(), Key: key}, hash, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		if _, err := s.liveSession(ctx, tx, account, session); err != nil {
			return idempotency.Result{}, err
		}
		method, err := loadMethod(ctx, tx, account, "ACTIVE")
		if err != nil {
			return idempotency.Result{}, invalidMFA()
		}
		out, err := s.queueMFAOTP(ctx, tx, method, session, "STEP_UP")
		if err != nil {
			return idempotency.Result{}, err
		}
		if err = s.record(ctx, tx, &account, "MFA_STEP_UP_CHALLENGE", method.id); err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{Status: 202, Body: map[string]any{"data": out}}, nil
	})
	if err != nil {
		return DeliveryChallenge{}, false, err
	}
	var out struct {
		Data DeliveryChallenge `json:"data"`
	}
	err = json.Unmarshal(response.Body, &out)
	return out.Data, response.Replayed, err
}
func (h *Handler) challengeMFA(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	out, replayed, err := h.Service.SendMFAChallenge(r.Context(), p.ActorID, p.SessionID, r.Header.Get("Idempotency-Key"))
	if replayed {
		w.Header().Set(idempotency.HeaderReplayed, "true")
	}
	reply(w, r, 202, out, err)
}
