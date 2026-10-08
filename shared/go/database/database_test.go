package database

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestOpenAndInvalidURLNotLeaked(t *testing.T) {
	_, err := Open(context.Background(), Config{URL: security.NewSecret("postgres://u:supersecret@[bad")})
	if err == nil || contains(err.Error(), "supersecret") {
		t.Fatalf("dsn leaked or accepted: %v", err)
	}
	db := testkit.NewDatabase(t, nil)
	pool, err := Open(context.Background(), Config{URL: security.NewSecret(db.URL), ApplicationName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var tz, st string
	_ = pool.QueryRow(context.Background(), `SELECT current_setting('TimeZone'), current_setting('statement_timeout')`).Scan(&tz, &st)
	if tz != "UTC" || st != "15s" {
		t.Fatalf("session settings tz=%s statement_timeout=%s", tz, st)
	}
	if err := Ping(pool)(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Two serializable transactions doing read-then-write on the same row
// conflict; WithTx retries the loser so both increments land.
func TestWithTxRetriesSerializationFailure(t *testing.T) {
	db := testkit.NewDatabase(t, nil, `CREATE TABLE counters (id int PRIMARY KEY, n int NOT NULL); INSERT INTO counters VALUES (1, 0);`)
	ctx := context.Background()
	var attempts atomic.Int32
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	var once sync.Once
	var ready atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithTx(ctx, db.Pool, TxOptions{TxOptions: pgx.TxOptions{IsoLevel: pgx.Serializable}, MaxAttempts: 5},
				func(ctx context.Context, tx pgx.Tx) error {
					attempts.Add(1)
					var n int
					if err := tx.QueryRow(ctx, `SELECT n FROM counters WHERE id=1`).Scan(&n); err != nil {
						return err
					}
					if ready.Add(1) == 2 {
						once.Do(func() { close(barrier) })
					}
					<-barrier
					_, err := tx.Exec(ctx, `UPDATE counters SET n=$1 WHERE id=1`, n+1)
					return err
				})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var n int
	_ = db.Pool.QueryRow(ctx, `SELECT n FROM counters WHERE id=1`).Scan(&n)
	if n != 2 {
		t.Fatalf("counter=%d, lost update", n)
	}
	if attempts.Load() < 3 {
		t.Fatalf("expected a retry, attempts=%d", attempts.Load())
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	db := testkit.NewDatabase(t, nil, `CREATE TABLE t (id int PRIMARY KEY)`)
	ctx := context.Background()
	sentinel := errors.New("boom")
	err := WithTx(ctx, db.Pool, TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, _ = tx.Exec(ctx, `INSERT INTO t VALUES (1)`)
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	err = WithTx(ctx, db.Pool, TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO t VALUES (2)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO t VALUES (2)`)
		return err
	})
	if !IsUniqueViolation(err, "t_pkey") || IsRetryable(err) {
		t.Fatalf("classification: %v", err)
	}
	var n int
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n)
	if n != 0 {
		t.Fatal("rollback failed")
	}
}
