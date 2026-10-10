package finance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"go.opentelemetry.io/otel/attribute"
)

var webhookRetries = observability.NewCounter("payment-service", "payment.webhook.retries", "Durable callback retries and dead letters")

func (s *Service) IngestWebhook(ctx context.Context, provider ids.ID, body []byte, signature string) error {
	if e := core.Require(ctx, platform.APIGateway); e != nil {
		return e
	}
	a, e := s.Adapter(provider)
	if e != nil {
		return e
	}
	if a.Collection == nil {
		return core.Fail(503, "PROVIDER_UNAVAILABLE")
	}
	event, e := a.Collection.ReceiveWebhook(body, signature)
	if e != nil {
		return core.Fail(401, "INVALID_WEBHOOK_SIGNATURE")
	}
	// Only normalized reference/event and body digest are retained, no card/BVN/PII.
	sum := sha256.Sum256(body)
	_, e = s.DB.Exec(ctx, `INSERT INTO provider_inbox(inbox_id,provider_account_id,digest,event_type,external_ref) VALUES($1,$2,$3,$4,$5) ON CONFLICT(provider_account_id,digest) DO NOTHING`, ids.New(), provider, hex.EncodeToString(sum[:]), event.EventType, event.Reference)
	return e
}
func (s *Service) ProcessInbox(ctx context.Context) (int, error) {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		return 0, e
	}
	token := ids.New()
	rows, e := s.DB.Query(ctx, `WITH due AS (SELECT inbox_id FROM provider_inbox WHERE (status='PENDING' OR (status='PROCESSING' AND lease_until<now())) AND next_attempt_at<=now() ORDER BY next_attempt_at LIMIT 20 FOR UPDATE SKIP LOCKED) UPDATE provider_inbox i SET status='PROCESSING',lease_until=now()+interval '60 seconds',lease_token=$1,attempts=attempts+1 FROM due WHERE i.inbox_id=due.inbox_id RETURNING i.inbox_id,i.provider_account_id,i.event_type,i.external_ref,i.attempts`, token)
	if e != nil {
		return 0, e
	}
	type item struct {
		id, provider ids.ID
		typ, ref     string
		attempts     int
	}
	items := []item{}
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.id, &x.provider, &x.typ, &x.ref, &x.attempts); e != nil {
			rows.Close()
			return 0, e
		}
		items = append(items, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	for _, x := range items {
		var processErr error
		if x.typ == "charge.reversed" || x.typ == "charge.dispute.create" || x.typ == "charge.dispute.resolve" || x.typ == "refund.pending" || x.typ == "refund.processing" || x.typ == "refund.processed" || x.typ == "refund.failed" || x.typ == "refund.reversed" {
			processErr = s.remedyCallback(ctx, x.provider, x.typ, x.ref)
		} else if x.typ == "charge.success" {
			var id ids.ID
			processErr = s.DB.QueryRow(ctx, `SELECT intent_id FROM payment_intents WHERE provider_reference=$1 AND provider_account_id=$2`, x.ref, x.provider).Scan(&id)
			if errors.Is(processErr, pgx.ErrNoRows) {
				processErr = s.MatchDVACharge(ctx, x.provider, x.ref)
			} else if processErr == nil {
				var i Intent
				i, processErr = s.VerifyIntent(ctx, id)
				if processErr == nil && i.State != "VERIFIED_SUCCESS" {
					processErr = core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
				}
			}
			// Unmatched DVA or unknown charge is kept for reconciliation; never attributed
			// solely by a customer-supplied reference.
		} else if x.typ == "transfer.success" || x.typ == "transfer.failed" || x.typ == "transfer.reversed" {
			processErr = s.transferCallback(ctx, x.provider, x.ref)
		} else if x.typ == "customeridentification.success" || x.typ == "customeridentification.failed" || x.typ == "dedicatedaccount.assign.success" || x.typ == "dedicatedaccount.assign.failed" {
			var id ids.ID
			processErr = s.DB.QueryRow(ctx, `SELECT virtual_account_id FROM virtual_accounts WHERE provider_account_id=$1 AND customer_ref=$2`, x.provider, x.ref).Scan(&id)
			if processErr == nil {
				_, processErr = s.SyncDVA(ctx, id, false)
			}
		} else {
			processErr = core.Fail(422, "UNSUPPORTED_PROVIDER_EVENT")
		}
		if processErr == nil {
			_, e = s.DB.Exec(ctx, `UPDATE provider_inbox SET status='PROCESSED',lease_until=NULL,last_error=NULL WHERE inbox_id=$1 AND lease_token=$2`, x.id, token)
		} else {
			status := "PENDING"
			if x.attempts >= 12 {
				status = "DEAD"
			}
			backoff := time.Duration(1<<min(x.attempts, 10)) * time.Second
			reason := "PROCESSING_REQUIRES_REVIEW"
			var rejection *apperrors.Error
			if errors.As(processErr, &rejection) {
				reason = rejection.Code
			}
			_, e = s.DB.Exec(ctx, `UPDATE provider_inbox SET status=$3,next_attempt_at=now()+$4::interval,lease_until=NULL,last_error=$5 WHERE inbox_id=$1 AND lease_token=$2`, x.id, token, status, backoff.String(), reason)
			s.Log.WarnContext(ctx, "financial webhook retry", "inbox_id", x.id, "attempt", x.attempts, "state", status)
			webhookRetries.Add(ctx, 1, attribute.String("state", status))
		}
		if e != nil {
			return len(items), e
		}
	}
	return len(items), nil
}
func (s *Service) ReplayInbox(ctx context.Context, id ids.ID, evidence string) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if evidence == "" {
		return core.Fail(422, "REPLAY_EVIDENCE_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE provider_inbox SET status='PENDING',attempts=0,next_attempt_at=now(),lease_until=NULL,lease_token=NULL WHERE inbox_id=$1 AND (status IN ('DEAD','PROCESSED','PENDING') OR lease_until<now())`, id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return core.Fail(409, "INBOX_LEASE_ACTIVE")
	}
	if e = s.Audit(ctx, tx, "inbox.replayed", id.String(), map[string]string{"evidence_ref": evidence}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
