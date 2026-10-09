package usecases

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Challenge purposes.
const (
	PurposeEmailVerification = "EMAIL_VERIFICATION"
	PurposePhoneVerification = "PHONE_VERIFICATION"
	PurposeEmailChange       = "EMAIL_CHANGE"
	PurposePhoneChange       = "PHONE_CHANGE"
	PurposePasswordReset     = "PASSWORD_RESET"
	PurposeLoginOTP          = "LOGIN_OTP"
	PurposeLoginMFA          = "LOGIN_MFA"
	PurposeMFACode           = "MFA_CODE"
	PurposeStepUpCode        = "STEP_UP_CODE"
)

// Delivery states of a challenge.
const (
	deliveryNotRequired = "NOT_REQUIRED"
	deliveryPending     = "PENDING"
	deliveryAccepted    = "ACCEPTED"
	deliveryFailed      = "FAILED"
	deliveryUnknown     = "UNKNOWN"
)

type newChallenge struct {
	accountID   ids.ID
	sessionID   ids.ID
	parentID    ids.ID
	purpose     string
	channel     string // EMAIL | SMS | NONE
	destination string
	ttl         time.Duration
	maxAttempts int
	context     map[string]any
	rateLimited bool
}

// issueChallenge stores a challenge and returns its view plus the raw secret
// (a 6-digit code, or an opaque token for channel NONE). The raw secret is
// never persisted.
func (s *Service) issueChallenge(ctx context.Context, tx pgx.Tx, c newChallenge) (ChallengeView, string, error) {
	now := s.now()
	if c.rateLimited {
		n, err := s.q.CountRecentChallenges(ctx, tx, db.CountRecentChallengesParams{AccountID: c.accountID, Purpose: c.purpose, CreatedAt: now.Add(-s.Policy.CodeSendWindow)})
		if err != nil {
			return ChallengeView{}, "", err
		}
		if n >= int64(s.Policy.CodeSendLimit) {
			return ChallengeView{}, "", domain.ErrTooManyCodes(int(s.Policy.CodeSendWindow.Seconds()))
		}
		// A new code supersedes earlier open ones for the same purpose.
		if err := s.q.InvalidateOpenChallenges(ctx, tx, db.InvalidateOpenChallengesParams{Now: now, AccountID: c.accountID, Purpose: c.purpose, ParentID: idp(c.parentID)}); err != nil {
			return ChallengeView{}, "", err
		}
	}
	id := ids.New()
	var secret, delivery string
	if c.channel == "NONE" {
		secret, delivery = credentials.OpaqueToken(""), deliveryNotRequired
	} else {
		secret, delivery = credentials.NumericCode(6), deliveryPending
	}
	if c.maxAttempts <= 0 {
		c.maxAttempts = s.Policy.CodeMaxAttempts
	}
	exp := now.Add(c.ttl)
	err := s.q.CreateChallenge(ctx, tx, db.CreateChallengeParams{
		ChallengeID: id, AccountID: c.accountID, SessionID: idp(c.sessionID), ParentID: idp(c.parentID), Purpose: c.purpose,
		Channel: c.channel, Destination: strp(c.destination), SecretHash: s.Hasher.Hash(c.purpose, id.String(), secret),
		MaxAttempts: int32(c.maxAttempts), DeliveryStatus: delivery, Context: jsonb(c.context), CreatedAt: now, ExpiresAt: exp,
	})
	if err != nil {
		return ChallengeView{}, "", err
	}
	v := ChallengeView{ChallengeID: id, Channel: c.channel, ExpiresAt: exp, DeliveryStatus: delivery}
	switch c.channel {
	case notification.ChannelEmail:
		v.DestinationHint = maskEmail(c.destination)
	case notification.ChannelSMS:
		v.DestinationHint = maskPhone(c.destination)
	}
	return v, secret, nil
}

// checkChallenge locks and validates a challenge for purposes and secret.
// A wrong secret consumes an attempt (committed by the caller returning
// errAttemptRecorded so the transaction still commits).
func (s *Service) checkChallenge(ctx context.Context, tx pgx.Tx, id ids.ID, secret string, purposes ...string) (db.AuthChallenge, error) {
	ch, err := s.q.GetChallengeForUpdate(ctx, tx, id)
	if isNoRows(err) {
		return db.AuthChallenge{}, domain.ErrInvalidCode()
	}
	if err != nil {
		return db.AuthChallenge{}, err
	}
	ok := false
	for _, p := range purposes {
		if ch.Purpose == p {
			ok = true
		}
	}
	now := s.now()
	if !ok || ch.ConsumedAt != nil || ch.InvalidatedAt != nil || !now.Before(ch.ExpiresAt) {
		return db.AuthChallenge{}, domain.ErrInvalidCode()
	}
	if !credentials.Equal(s.Hasher.Hash(ch.Purpose, ch.ChallengeID.String(), strings.TrimSpace(secret)), ch.SecretHash) {
		if err := s.q.RecordChallengeAttempt(ctx, tx, db.RecordChallengeAttemptParams{Now: now, ChallengeID: ch.ChallengeID}); err != nil {
			return db.AuthChallenge{}, err
		}
		return db.AuthChallenge{}, errAttemptRecorded
	}
	return ch, nil
}

func (s *Service) consumeChallenge(ctx context.Context, tx pgx.Tx, id ids.ID) error {
	n, err := s.q.ConsumeChallenge(ctx, tx, db.ConsumeChallengeParams{ChallengeID: id, ConsumedAt: ptrTime(s.now())})
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrInvalidCode()
	}
	return nil
}

func ptrTime(t time.Time) *time.Time { return &t }

// errAttemptRecorded marks a failed verification whose attempt counter must
// be committed; commitFailure converts it into the client error after commit.
var errAttemptRecorded = errors.New("usecases: verification attempt recorded")

// txKeepingAttempts runs fn; when fn reports errAttemptRecorded (or any error
// produced after a recorded attempt), the transaction commits the attempt and
// INVALID_VERIFICATION_CODE is returned.
func (s *Service) txKeepingAttempts(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	var failed bool
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		failed = false
		err := fn(ctx, tx)
		if errors.Is(err, errAttemptRecorded) {
			failed = true
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	if failed {
		return domain.ErrInvalidCode()
	}
	return nil
}

func challengeContext(ch db.AuthChallenge) map[string]any {
	m := map[string]any{}
	if len(ch.Context) > 0 {
		_ = json.Unmarshal(ch.Context, &m)
	}
	return m
}

// deliverCode sends a one-time code through notification-service and
// records the acceptance outcome. It never claims delivery to the person.
func (s *Service) deliverCode(ctx context.Context, accountID ids.ID, v *ChallengeView, destination, code string) error {
	req := notification.SendRequest{
		Recipient:   notification.Recipient{Domain: string(platform.DomainIndividualOwner), ID: accountID},
		TemplateKey: notification.TemplateVerificationCode, Channels: []string{v.Channel},
		Variables: map[string]string{"code": code}, CallerReference: "owner-challenge:" + v.ChallengeID.String(),
	}
	if v.Channel == notification.ChannelEmail {
		req.Destinations.Email = destination
	} else {
		req.Destinations.Phone = destination
	}
	acc, err := s.Notify.Send(ctx, req, "owner-challenge-"+v.ChallengeID.String())
	status, nid := deliveryAccepted, idp(acc.NotificationID)
	outcome := apperrors.OutcomeNone
	if err == nil {
		for _, sup := range acc.Suppressed {
			if sup.Channel == v.Channel {
				status, err = deliveryFailed, errors.New("suppressed: "+sup.Reason)
				outcome = apperrors.OutcomeDefiniteFailure
			}
		}
	} else if notification.Classify(err) == notification.OutcomeDefinite {
		status, outcome = deliveryFailed, apperrors.OutcomeDefiniteFailure
	} else {
		// Timeout/5xx: notification-service may have accepted it; keep the
		// code valid and let the owner request another if none arrives.
		status, outcome = deliveryUnknown, apperrors.OutcomeUnknown
	}
	v.DeliveryStatus = status
	if uerr := s.q.SetChallengeDelivery(context.WithoutCancel(ctx), s.Pool, db.SetChallengeDeliveryParams{ChallengeID: v.ChallengeID, DeliveryStatus: status, NotificationID: nid}); uerr != nil {
		s.Logger.WarnContext(ctx, "recording code delivery outcome failed", "error", uerr.Error())
	}
	if err != nil {
		s.Logger.WarnContext(ctx, "verification code not accepted by notification-service", "challenge_id", v.ChallengeID.String(), "status", status, "error", err.Error())
		return domain.ErrNotificationUnavailable(outcome, err)
	}
	return nil
}
