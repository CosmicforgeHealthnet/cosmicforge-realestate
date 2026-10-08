// Package database provides pgx pool construction, short transactions with
// bounded retry of serialization/deadlock failures, and PostgreSQL error
// classification. Each service connects only to its own database.
package database

import (
	"context"
	"errors"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// Querier is satisfied by *pgxpool.Pool, *pgxpool.Conn and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Beginner starts transactions.
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// DB is a pool-like handle.
type DB interface {
	Querier
	Beginner
}

// Config configures a pool.
type Config struct {
	URL             security.Secret
	ApplicationName string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	// StatementTimeout bounds every statement server-side (default 15s).
	StatementTimeout time.Duration
	// LockTimeout bounds lock waits server-side (default 5s).
	LockTimeout time.Duration
}

// Open creates and pings a pool.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL.Reveal())
	if err != nil {
		// Never include the DSN (it contains a password).
		return nil, errors.New("database: invalid DATABASE_URL")
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pc.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pc.MaxConnLifetime = cfg.MaxConnLifetime
	}
	st, lt := cfg.StatementTimeout, cfg.LockTimeout
	if st <= 0 {
		st = 15 * time.Second
	}
	if lt <= 0 {
		lt = 5 * time.Second
	}
	rp := pc.ConnConfig.RuntimeParams
	if cfg.ApplicationName != "" {
		rp["application_name"] = cfg.ApplicationName
	}
	rp["statement_timeout"] = durationMS(st)
	rp["lock_timeout"] = durationMS(lt)
	rp["idle_in_transaction_session_timeout"] = durationMS(60 * time.Second)
	rp["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func durationMS(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

// SQLState codes used by the platform.
const (
	CodeUniqueViolation      = "23505"
	CodeForeignKeyViolation  = "23503"
	CodeCheckViolation       = "23514"
	CodeSerializationFailure = "40001"
	CodeDeadlockDetected     = "40P01"
	CodeLockNotAvailable     = "55P03"
)

// SQLState returns the PostgreSQL error code, or "".
func SQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// IsUniqueViolation reports a unique violation, optionally of a named constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == CodeUniqueViolation {
		return constraint == "" || pgErr.ConstraintName == constraint
	}
	return false
}

// IsRetryable reports transient transaction conflicts that are safe to retry
// from the start of the transaction.
func IsRetryable(err error) bool {
	switch SQLState(err) {
	case CodeSerializationFailure, CodeDeadlockDetected:
		return true
	}
	return false
}

// TxOptions controls WithTx.
type TxOptions struct {
	pgx.TxOptions
	// MaxAttempts bounds retries of retryable failures (default 3).
	MaxAttempts int
}

// WithTx runs fn in a transaction, committing on success. Serialization and
// deadlock failures re-run fn from scratch up to MaxAttempts. fn must not
// perform external network calls (providers, other services, object storage)
// — keep transactions short and local.
func WithTx(ctx context.Context, db Beginner, opts TxOptions, fn func(ctx context.Context, tx pgx.Tx) error) error {
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	var err error
	for i := 1; i <= attempts; i++ {
		err = runTx(ctx, db, opts.TxOptions, fn)
		if err == nil || !IsRetryable(err) || i == attempts {
			return err
		}
		backoff := time.Duration(10*i)*time.Millisecond + time.Duration(rand.IntN(10))*time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return err
}

func runTx(ctx context.Context, db Beginner, opts pgx.TxOptions, fn func(context.Context, pgx.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(ctx, tx); err != nil {
		return err
	}
	// Commit even if the client went away after the work was done: the work
	// is complete and idempotency replay depends on the commit.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return tx.Commit(cctx)
}

// Ping is a readiness check.
func Ping(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}
