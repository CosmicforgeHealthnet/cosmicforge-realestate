package finance

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"time"
)

func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }

type RecoveryPolicy struct {
	MaxAttempts       int
	BaseSeconds       int64
	MaxBackoffSeconds int64
	LeaseSeconds      int64
}

func (s *Service) ConfigureRecovery(ctx context.Context, p RecoveryPolicy) error {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		return e
	}
	if p.MaxAttempts <= 0 || p.MaxAttempts > 100 || p.BaseSeconds <= 0 || p.MaxBackoffSeconds < p.BaseSeconds || p.MaxBackoffSeconds > 86400 || p.LeaseSeconds <= 0 || p.LeaseSeconds > 3600 {
		return core.Fail(422, "INVALID_READBACK_POLICY")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoveryPolicy = p
	return nil
}
func (s *Service) recoveryConfig() RecoveryPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.recoveryPolicy
}
func (s *Service) ClaimReadback(ctx context.Context, kind string, id ids.ID) (ids.ID, bool, error) {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		return ids.Nil, false, e
	}
	p := s.recoveryConfig()
	var provider ids.ID
	var ref string
	var sql string
	switch kind {
	case "COLLECTION":
		sql = `SELECT provider_account_id,provider_reference FROM payment_intents WHERE intent_id=$1`
	case "PAYOUT":
		sql = `SELECT provider_account_id,provider_reference FROM payout_attempts WHERE payout_id=$1`
	case "REFUND":
		sql = `SELECT provider_account_id,attempt_reference FROM refund_attempts WHERE refund_id=$1`
	default:
		return ids.Nil, false, core.Fail(422, "INVALID_READBACK_KIND")
	}
	if e := s.DB.QueryRow(ctx, sql, id).Scan(&provider, &ref); e != nil {
		return ids.Nil, false, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return ids.Nil, false, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO original_readback_jobs(aggregate_id,kind,provider_account_id,original_reference,state) VALUES($1,$2,$3,$4,'PENDING') ON CONFLICT DO NOTHING`, id, kind, provider, ref)
	if e != nil {
		return ids.Nil, false, e
	}
	tag, e := tx.Exec(ctx, `UPDATE original_readback_jobs SET state='DEAD',lease_until=NULL,last_error_code='READBACK_LEASE_EXHAUSTED',updated_at=now() WHERE aggregate_id=$1 AND kind=$2 AND state='PROCESSING' AND lease_until<now() AND attempts>=$3`, id, kind, p.MaxAttempts)
	if e != nil {
		return ids.Nil, false, e
	}
	if tag.RowsAffected() > 0 {
		_, e = tx.Exec(ctx, `INSERT INTO original_readback_history(history_id,aggregate_id,kind,snapshot) VALUES($1,$2,$3,$4)`, ids.New(), id, kind, jsonString(map[string]string{"state": "DEAD", "error_code": "READBACK_LEASE_EXHAUSTED"}))
		if e != nil {
			return ids.Nil, false, e
		}
		return ids.Nil, false, tx.Commit(ctx)
	}
	token := ids.New()
	var attempts int
	e = tx.QueryRow(ctx, `UPDATE original_readback_jobs SET state='PROCESSING',attempts=attempts+1,lease_token=$3,lease_until=now()+$4::interval,updated_at=now() WHERE aggregate_id=$1 AND kind=$2 AND state IN ('PENDING','PROCESSING') AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) AND attempts<$5 RETURNING attempts`, id, kind, token, (time.Duration(p.LeaseSeconds) * time.Second).String(), p.MaxAttempts).Scan(&attempts)
	if errors.Is(e, pgx.ErrNoRows) {
		return ids.Nil, false, tx.Commit(ctx)
	}
	if e != nil {
		return ids.Nil, false, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO original_readback_history(history_id,aggregate_id,kind,snapshot) VALUES($1,$2,$3,$4)`, ids.New(), id, kind, jsonString(map[string]any{"state": "PROCESSING", "attempt": attempts, "original_reference": ref}))
	if e != nil {
		return ids.Nil, false, e
	}
	return token, true, tx.Commit(ctx)
}
func (s *Service) FinishReadback(ctx context.Context, kind string, id, token ids.ID, terminal bool) error {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		return e
	}
	p := s.recoveryConfig()
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var attempts int
	e = tx.QueryRow(ctx, `SELECT attempts FROM original_readback_jobs WHERE aggregate_id=$1 AND kind=$2 AND lease_token=$3 FOR UPDATE`, id, kind, token).Scan(&attempts)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	state := "PENDING"
	if terminal {
		state = "COMPLETED"
	} else if attempts >= p.MaxAttempts {
		state = "DEAD"
	}
	seconds := p.BaseSeconds
	for n := 1; n < attempts && seconds < p.MaxBackoffSeconds; n++ {
		seconds = min(seconds*2, p.MaxBackoffSeconds)
	}
	_, e = tx.Exec(ctx, `UPDATE original_readback_jobs SET state=$4,next_attempt_at=now()+$5::interval,lease_until=NULL,last_error_code=CASE WHEN $4='COMPLETED' THEN NULL ELSE 'ORIGINAL_READBACK_REQUIRED' END,updated_at=now() WHERE aggregate_id=$1 AND kind=$2 AND lease_token=$3`, id, kind, token, state, (time.Duration(seconds) * time.Second).String())
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO original_readback_history(history_id,aggregate_id,kind,snapshot) VALUES($1,$2,$3,$4)`, ids.New(), id, kind, jsonString(map[string]any{"state": state, "attempt": attempts, "next_delay_seconds": seconds, "error_code": "ORIGINAL_READBACK_REQUIRED"}))
	if e != nil {
		return e
	}
	if state == "DEAD" {
		if e = s.Audit(ctx, tx, "readback.dead_letter", id.String(), map[string]string{"kind": kind}); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (s *Service) ReplayReadback(ctx context.Context, kind string, id ids.ID, ref string) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if ref == "" {
		return core.Fail(422, "REPLAY_EVIDENCE_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE original_readback_jobs SET state='PENDING',attempts=0,next_attempt_at=now(),lease_until=NULL,lease_token=NULL,updated_at=now() WHERE aggregate_id=$1 AND kind=$2 AND state='DEAD'`, id, kind)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return core.Fail(409, "READBACK_NOT_DEAD_LETTERED")
	}
	_, e = tx.Exec(ctx, `INSERT INTO original_readback_history(history_id,aggregate_id,kind,snapshot) VALUES($1,$2,$3,$4)`, ids.New(), id, kind, jsonString(map[string]string{"state": "PENDING", "authority_ref": ref}))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "readback.replayed", id.String(), map[string]string{"kind": kind, "authority_ref": ref}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
