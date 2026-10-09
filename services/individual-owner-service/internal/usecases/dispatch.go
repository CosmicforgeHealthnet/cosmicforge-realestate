package usecases

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// DispatchMaxAttempts bounds security-alert delivery attempts before the
// request is dead-lettered for operator review.
const DispatchMaxAttempts = 8

// DispatchStats are queue figures for metrics/readiness.
type DispatchStats struct{ Pending, Dead int64 }

// DispatchDue sends queued security alerts to notification-service. Rows are
// leased (FOR UPDATE SKIP LOCKED) so parallel workers never double-claim;
// each request carries the dispatch ID as Idempotency-Key, so a crash after
// acceptance but before completion re-sends safely.
func (s *Service) DispatchDue(ctx context.Context, owner string, batch int, lease time.Duration) (sent int, err error) {
	now := s.now()
	rows, err := s.q.ClaimDispatches(ctx, s.Pool, db.ClaimDispatchesParams{Owner: owner, LeaseUntil: now.Add(lease), Now: now, RowLimit: int32(batch)})
	if err != nil {
		return 0, err
	}
	for _, d := range rows {
		jctx := requestcontext.Job(ctx, "owner-dispatch")
		var vars map[string]string
		_ = json.Unmarshal(d.Variables, &vars)
		req := notification.SendRequest{Recipient: notification.Recipient{Domain: string(platform.DomainIndividualOwner), ID: d.AccountID},
			TemplateKey: d.TemplateKey, Channels: d.Channels, Variables: vars, CallerReference: "owner-dispatch:" + d.DispatchID.String()}
		if d.DestinationEmail != nil {
			req.Destinations.Email = *d.DestinationEmail
		}
		if d.DestinationPhone != nil {
			req.Destinations.Phone = *d.DestinationPhone
		}
		cctx, cancel := context.WithTimeout(jctx, 10*time.Second)
		acc, serr := s.Notify.Send(cctx, req, "owner-dispatch-"+d.DispatchID.String())
		cancel()
		if serr == nil {
			if _, err := s.q.CompleteDispatch(ctx, s.Pool, db.CompleteDispatchParams{NotificationID: acc.NotificationID, Now: s.now(), DispatchID: d.DispatchID, Owner: owner}); err != nil {
				return sent, err
			}
			sent++
			continue
		}
		dead := notification.Classify(serr) == notification.OutcomeDefinite || int(d.Attempts) >= DispatchMaxAttempts
		retry := s.now().Add(backoff(int(d.Attempts)))
		if _, err := s.q.FailDispatch(ctx, s.Pool, db.FailDispatchParams{Dead: dead, RetryAt: retry, LastError: truncate(serr.Error(), 500), DispatchID: d.DispatchID, Owner: owner}); err != nil {
			return sent, err
		}
		s.Logger.WarnContext(jctx, "security alert dispatch failed", "dispatch_id", d.DispatchID.String(), "attempt", d.Attempts, "dead", dead, "error", serr.Error())
	}
	return sent, nil
}

// backoff is exponential (2s base, 10m cap) with full jitter on the upper half.
func backoff(attempt int) time.Duration {
	d := 2 * time.Second
	for i := 1; i < attempt && d < 10*time.Minute; i++ {
		d *= 2
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// DispatchQueueStats reports pending and dead-lettered requests.
func (s *Service) DispatchQueueStats(ctx context.Context) (DispatchStats, error) {
	r, err := s.q.DispatchStats(ctx, s.Pool)
	return DispatchStats{Pending: r.Pending, Dead: r.Dead}, err
}

// Housekeep removes expired one-time challenges and long-ended sessions.
// Accounts, audit records and security history are never deleted here.
func (s *Service) Housekeep(ctx context.Context, sessionRetention time.Duration) (challenges, sessionsPurged int64, err error) {
	now := s.now()
	if challenges, err = s.q.PurgeExpiredChallenges(ctx, s.Pool, now.Add(-24*time.Hour)); err != nil {
		return 0, 0, err
	}
	sessionsPurged, err = s.q.PurgeEndedSessions(ctx, s.Pool, ptrTime(now.Add(-sessionRetention)))
	return challenges, sessionsPurged, err
}
