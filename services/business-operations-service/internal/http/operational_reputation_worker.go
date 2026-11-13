package businesshttp

import (
	"context"
	"errors"
	"fmt"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/messaging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/jackc/pgx/v5"
)

// reviewSourceForProvider maps a connection provider to its review source.
func reviewSourceForProvider(provider string) string {
	switch provider {
	case "BOOKING_COM", "AIRBNB":
		return provider
	case "MARKETPLACE":
		return "MARKETPLACE_VERIFIED_STAY"
	}
	return ""
}

// ProcessReputation runs every Section 13 durable worker once.
func (h *Handler) ProcessReputation(ctx context.Context, limit int) error {
	return errors.Join(h.ProcessReviewImport(ctx, limit), h.ProcessReviewResponses(ctx, limit), h.ProcessQualityMonitoring(ctx, limit))
}

// ProcessReviewImport polls provider-permitted channel reviews with leased,
// fenced cursors. Marketplace verified-stay reviews arrive by signed intake.
func (h *Handler) ProcessReviewImport(ctx context.Context, limit int) error {
	if _, e := h.Auth.Service.Pool.Exec(ctx, `INSERT INTO review_sync_cursors(organisation_id,connection_id) SELECT organisation_id,id FROM messaging_connections WHERE supports_reviews AND provider IN ('BOOKING_COM','AIRBNB') ON CONFLICT DO NOTHING`); e != nil {
		return e
	}
	now := h.Auth.Service.Now().UTC()
	owner := "review-import-" + ids.New().String()
	rows, e := h.Auth.Service.Pool.Query(ctx, `UPDATE review_sync_cursors s SET lease_owner=$1,lease_expires_at=$2,lease_generation=lease_generation+1,updated_at=now() FROM messaging_connections m WHERE m.organisation_id=s.organisation_id AND m.id=s.connection_id AND m.state IN ('CONNECTED','DEGRADED') AND m.supports_reviews AND s.next_poll_at<=$3 AND (s.lease_expires_at IS NULL OR s.lease_expires_at<$3) AND (s.organisation_id,s.connection_id) IN (SELECT organisation_id,connection_id FROM review_sync_cursors WHERE next_poll_at<=$3 ORDER BY next_poll_at LIMIT $4 FOR UPDATE SKIP LOCKED) RETURNING s.organisation_id,s.connection_id,s.cursor,s.lease_generation`, owner, now.Add(time.Minute), now, limit)
	if e != nil {
		return e
	}
	type lease struct {
		org, conn  ids.ID
		cursor     string
		generation int64
	}
	leases := []lease{}
	for rows.Next() {
		var l lease
		if e = rows.Scan(&l.org, &l.conn, &l.cursor, &l.generation); e != nil {
			rows.Close()
			return e
		}
		leases = append(leases, l)
	}
	rows.Close()
	var failures []error
	for _, l := range leases {
		c, e := opsGet[MessagingConnection](ctx, h.Auth.Service.Pool, "messaging_connections", l.org, l.conn)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		source := reviewSourceForProvider(c.Provider)
		page, pollErr := h.reviewAdapter(source).PollReviews(ctx, c.ProviderAccountReference, l.cursor)
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if e := opsService(ctx, tx); e != nil {
				return e
			}
			var generation int64
			var leaseOwner *string
			if e := tx.QueryRow(ctx, `SELECT lease_generation,lease_owner FROM review_sync_cursors WHERE organisation_id=$1 AND connection_id=$2 FOR UPDATE`, l.org, l.conn).Scan(&generation, &leaseOwner); e != nil {
				return e
			}
			if generation != l.generation || leaseOwner == nil || *leaseOwner != owner {
				return nil
			}
			if pollErr != nil {
				_, e := tx.Exec(ctx, `UPDATE review_sync_cursors SET lease_owner=NULL,lease_expires_at=NULL,next_poll_at=$3,last_error=$4,updated_at=now() WHERE organisation_id=$1 AND connection_id=$2`, l.org, l.conn, h.Auth.Service.Now().UTC().Add(5*time.Minute), truncate(pollErr.Error(), 300))
				return e
			}
			t := workerTenant(l.org)
			for _, o := range page.Reviews {
				in := ReviewIntake{PropertyID: c.PropertyID, Source: source, ExternalReviewID: o.ExternalReviewID, SourceVersion: o.SourceVersion, RatingValue: o.RatingValue, RatingScale: o.RatingScale, Text: o.Text, StoreText: o.TextStoragePermitted, ContentReference: o.ContentReference, ReviewedAt: o.ReviewedAt, ModerationStatus: o.ModerationStatus, Deleted: o.Deleted}
				for _, d := range o.Dimensions {
					in.Dimensions = append(in.Dimensions, ReviewDimension{Dimension: d.Dimension, Value: d.Value, Scale: d.Scale})
				}
				if o.ReservationReference != "" {
					var reservation ids.ID
					if e := tx.QueryRow(ctx, `SELECT x.canonical_reservation_id FROM channel_reservation_refs x JOIN reservations r ON r.organisation_id=x.organisation_id AND r.id=x.canonical_reservation_id WHERE x.organisation_id=$1 AND x.external_reservation_id=$2 AND x.state='CANONICALLY_ACCEPTED' AND r.property_id=$3 ORDER BY x.created_at DESC LIMIT 1`, l.org, o.ReservationReference, c.PropertyID).Scan(&reservation); e == nil {
						in.ReservationID = &reservation
					} else if !errors.Is(e, pgx.ErrNoRows) {
						return e
					}
				}
				if !validReviewIntake(in, h.Auth.Service.Now().UTC()) {
					continue // malformed provider data is skipped, never fabricated
				}
				connID := c.ID
				if _, e := h.ingestReview(ctx, tx, t, in, &connID); e != nil {
					return e
				}
			}
			_, e := tx.Exec(ctx, `UPDATE review_sync_cursors SET cursor=$3,lease_owner=NULL,lease_expires_at=NULL,next_poll_at=$4,last_error='',updated_at=now() WHERE organisation_id=$1 AND connection_id=$2`, l.org, l.conn, truncate(page.NextCursor, 1000), h.Auth.Service.Now().UTC().Add(5*time.Minute))
			return e
		})
		if e != nil {
			failures = append(failures, fmt.Errorf("review import %s: %w", l.conn, e))
		}
	}
	return errors.Join(failures...)
}

// ProcessReviewResponses submits approved responses and retractions through
// the provider adapter: leased claim, external call outside the transaction,
// ambiguous outcomes read back before any retry.
func (h *Handler) ProcessReviewResponses(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM review_responses WHERE (state IN ('APPROVED','UNKNOWN_PENDING_READBACK','RETRACT_REQUESTED') AND next_attempt_at<=$1) OR (state='SUBMITTING' AND lease_expires_at<$1) ORDER BY next_attempt_at,id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if e = rows.Scan(&r.org, &r.id); e != nil {
			rows.Close()
			return e
		}
		refs = append(refs, r)
	}
	rows.Close()
	owner := "review-response-" + ids.New().String()
	var failures []error
	for _, r := range refs {
		if e := h.dispatchReviewResponse(ctx, r.org, r.id, owner); e != nil {
			failures = append(failures, fmt.Errorf("review response %s: %w", r.id, e))
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) dispatchReviewResponse(ctx context.Context, org, id ids.ID, owner string) error {
	t := workerTenant(org)
	var resp ReviewResponse
	var review ReviewRef
	var account, kind string
	claimed := false
	e := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if e := opsService(ctx, tx); e != nil {
			return e
		}
		var e error
		if resp, e = opsLock[ReviewResponse](ctx, tx, "review_responses", org, id); e != nil {
			return e
		}
		if review, e = opsLock[ReviewRef](ctx, tx, "review_refs", org, resp.ReviewID); e != nil {
			return e
		}
		now := h.Auth.Service.Now().UTC()
		var expired bool
		if e = tx.QueryRow(ctx, `SELECT coalesce(lease_expires_at<$2,false) FROM review_responses WHERE id=$1`, id, now).Scan(&expired); e != nil {
			return e
		}
		if resp.State != "SUBMITTING" && resp.NextAttemptAt.After(now) {
			return nil // backoff honoured under the row lock
		}
		switch {
		case resp.State == "APPROVED":
			kind = "SUBMIT"
			// The author's current authority is rechecked at dispatch.
			ok, e := h.currentRecordedPMSAuthority(ctx, tx, org, resp.PropertyID, resp.CreatedByMember, resp.CreatedByRole, permissions.Permission("reviews:write"))
			if e != nil {
				return e
			}
			if !ok || review.State != "ACTIVE" {
				if _, e = tx.Exec(ctx, `UPDATE review_responses SET state='CANCELLED',outcome_reason='AUTHORITY_OR_REVIEW_NO_LONGER_CURRENT',version=version+1,updated_at=now() WHERE id=$1`, id); e != nil {
					return e
				}
				return h.reviewResponseMirror(ctx, tx, org, review.ID, "CANCELLED")
			}
		case resp.State == "UNKNOWN_PENDING_READBACK" || resp.State == "SUBMITTING" && expired:
			kind = "READBACK"
		case resp.State == "RETRACT_REQUESTED":
			kind = "RETRACT"
		default:
			return nil
		}
		if kind != "RETRACT" {
			if _, e = tx.Exec(ctx, `UPDATE review_responses SET state='SUBMITTING',attempts=attempts+1,lease_owner=$2,lease_expires_at=$3,lease_generation=lease_generation+1,version=version+1,updated_at=now() WHERE id=$1`, id, owner, now.Add(2*time.Minute)); e != nil {
				return e
			}
		} else {
			if _, e = tx.Exec(ctx, `UPDATE review_responses SET attempts=attempts+1,lease_owner=$2,lease_expires_at=$3,lease_generation=lease_generation+1,version=version+1,updated_at=now() WHERE id=$1`, id, owner, now.Add(2*time.Minute)); e != nil {
				return e
			}
		}
		if e = tx.QueryRow(ctx, `SELECT coalesce((SELECT provider_account_reference FROM messaging_connections WHERE organisation_id=$1 AND id=$2),'')`, org, review.ConnectionID).Scan(&account); e != nil {
			return e
		}
		resp, e = opsGet[ReviewResponse](ctx, tx, "review_responses", org, id)
		claimed = e == nil
		return e
	})
	if e != nil || !claimed {
		return e
	}
	adapter := h.reviewAdapter(review.Source)
	var result messaging.MessagingSendResult
	var callErr error
	switch kind {
	case "SUBMIT":
		result, callErr = adapter.Reply(ctx, account, review.ExternalReviewID, resp.ID.String(), resp.Body)
	case "READBACK":
		result, callErr = adapter.ReadbackReply(ctx, account, review.ExternalReviewID, resp.ID.String())
	case "RETRACT":
		callErr = adapter.Retract(ctx, account, review.ExternalReviewID, deref(resp.ProviderResponseID))
	}
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if e := opsService(ctx, tx); e != nil {
			return e
		}
		cur, e := opsLock[ReviewResponse](ctx, tx, "review_responses", org, id)
		if e != nil {
			return e
		}
		if cur.LeaseGeneration != resp.LeaseGeneration {
			return nil
		}
		now := h.Auth.Service.Now().UTC()
		outcome, to, reason := "", "", ""
		switch {
		case kind == "RETRACT" && callErr == nil:
			outcome, to = "ACCEPTED", "RETRACTED"
		case kind == "RETRACT" && messaging.Definite(callErr):
			outcome, to, reason = "REJECTED", "PUBLISHED", "PROVIDER_RETRACT_REJECTED"
		case kind == "RETRACT":
			outcome, to, reason = "AMBIGUOUS", "RETRACT_REQUESTED", "RETRACT_OUTCOME_UNKNOWN"
		case callErr == nil && result.ProviderMessageID != "":
			outcome, to = "ACCEPTED", "PUBLISHED"
		case kind == "READBACK" && errors.Is(callErr, messaging.ErrNotFound):
			outcome, to, reason = "NOT_FOUND", "APPROVED", "READBACK_NOT_FOUND_RETRY"
		case messaging.Definite(callErr):
			outcome, to, reason = "REJECTED", "FAILED", "PROVIDER_REPLY_UNSUPPORTED_OR_REJECTED"
		default:
			outcome, to, reason = "AMBIGUOUS", "UNKNOWN_PENDING_READBACK", "PROVIDER_OUTCOME_UNKNOWN"
		}
		if to == "APPROVED" && cur.Attempts >= 6 {
			to, reason = "FAILED", "ATTEMPTS_EXHAUSTED"
		}
		detail := ""
		if callErr != nil {
			detail = truncate(callErr.Error(), 400)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO review_response_attempts(id,organisation_id,response_id,attempt,lease_generation,kind,outcome,provider_response_id,detail) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9) ON CONFLICT DO NOTHING`, ids.New(), org, id, max(cur.Attempts, 1), cur.LeaseGeneration, kind, outcome, result.ProviderMessageID, detail); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE review_responses SET state=$2,provider_response_id=coalesce(nullif($3,''),provider_response_id),published_at=CASE WHEN $2='PUBLISHED' AND published_at IS NULL THEN $4::timestamptz ELSE published_at END,outcome_reason=$5,next_attempt_at=$6,lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE id=$1`, id, to, result.ProviderMessageID, now, reason, now.Add(time.Duration(min(cur.Attempts, 8))*time.Minute)); e != nil {
			return e
		}
		if e = h.reviewResponseMirror(ctx, tx, org, cur.ReviewID, to); e != nil {
			return e
		}
		return h.opsCommit(ctx, tx, t, cur.PropertyID, id, "review_response", "DISPATCH_"+kind+"_"+outcome, cur.State, to, businessEvents.ReviewResponseUpdated, cur.Version+1, map[string]any{"provider_acknowledged": outcome == "ACCEPTED", "reason": reason})
	})
}

// ProcessQualityMonitoring closes issues whose monitoring window passed with
// no new negative signal.
func (h *Handler) ProcessQualityMonitoring(ctx context.Context, limit int) error {
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if e := opsService(ctx, tx); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT organisation_id,property_id,id,version FROM quality_issues WHERE state='MONITORING' AND monitoring_until<$1 ORDER BY monitoring_until LIMIT $2 FOR UPDATE SKIP LOCKED`, h.Auth.Service.Now().UTC(), limit)
		if e != nil {
			return e
		}
		type ref struct {
			org, property, id ids.ID
			version           int64
		}
		refs := []ref{}
		for rows.Next() {
			var r ref
			if e = rows.Scan(&r.org, &r.property, &r.id, &r.version); e != nil {
				rows.Close()
				return e
			}
			refs = append(refs, r)
		}
		rows.Close()
		for _, r := range refs {
			if _, e = tx.Exec(ctx, `UPDATE quality_issues SET state='CLOSED',version=version+1,updated_at=now() WHERE id=$1`, r.id); e != nil {
				return e
			}
			if e = h.opsCommit(ctx, tx, workerTenant(r.org), r.property, r.id, "quality_issue", "CLOSED_AFTER_MONITORING", "MONITORING", "CLOSED", "", r.version+1, map[string]any{}); e != nil {
				return e
			}
		}
		return nil
	})
}
