package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

const widgetsSQL = `CREATE TABLE widgets (id serial PRIMARY KEY, name text NOT NULL);`

func newStore(t *testing.T, lease time.Duration) (*Store, *testkit.Database) {
	db := testkit.NewDatabase(t, nil, SchemaSQL, widgetsSQL)
	return NewStore(db.Pool, lease, time.Hour), db
}

func key(k string) Key {
	return Key{Scope: "widget.create", CallerType: CallerService, CallerID: "individual-owner-service", Key: k}
}

func countWidgets(t *testing.T, db *testkit.Database) int {
	var n int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM widgets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func create(name string) func(context.Context, pgx.Tx) (Result, error) {
	return func(ctx context.Context, tx pgx.Tx) (Result, error) {
		var id int
		if err := tx.QueryRow(ctx, `INSERT INTO widgets (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
			return Result{}, err
		}
		return Result{Status: 201, Body: map[string]any{"id": id, "name": name}}, nil
	}
}

func TestFingerprintCanonical(t *testing.T) {
	a, _ := Fingerprint("POST", "/x", []byte(`{"b":1,"a":[1,2]}`))
	b, _ := Fingerprint("POST", "/x", []byte("{ \"a\": [1,2],\n \"b\": 1 }"))
	c, _ := Fingerprint("POST", "/x", []byte(`{"b":2,"a":[1,2]}`))
	d, _ := Fingerprint("POST", "/y", []byte(`{"b":1,"a":[1,2]}`))
	big1, _ := Fingerprint("POST", "/x", []byte(`{"n":9007199254740993}`))
	big2, _ := Fingerprint("POST", "/x", []byte(`{"n":9007199254740992}`))
	if a != b || a == c || a == d || big1 == big2 {
		t.Fatal("fingerprint not canonical/precise")
	}
	if _, err := Fingerprint("POST", "/x", []byte(`{bad`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestReplaySameRequest(t *testing.T) {
	s, db := newStore(t, 5*time.Second)
	ctx := context.Background()
	r1, err := s.Execute(ctx, key("k1"), "h1", create("a"))
	if err != nil || r1.Status != 201 || r1.Replayed {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, err := s.Execute(ctx, key("k1"), "h1", create("a"))
	if err != nil || !r2.Replayed || r2.Status != 201 {
		t.Fatalf("replay %+v %v", r2, err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(r1.Body, &a)
	_ = json.Unmarshal(r2.Body, &b)
	if a["id"] != b["id"] {
		t.Fatal("replayed response differs")
	}
	if countWidgets(t, db) != 1 {
		t.Fatal("command executed twice")
	}
}

func TestDifferentPayloadConflict(t *testing.T) {
	s, db := newStore(t, 5*time.Second)
	ctx := context.Background()
	if _, err := s.Execute(ctx, key("k1"), "h1", create("a")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Execute(ctx, key("k1"), "h2", create("b"))
	if apperrors.CodeOf(err) != apperrors.CodeIdempotencyConflict {
		t.Fatalf("expected IDEMPOTENCY_CONFLICT, got %v", err)
	}
	if countWidgets(t, db) != 1 {
		t.Fatal("conflicting request executed")
	}
	// Same key, different caller or scope is independent.
	k := key("k1")
	k.CallerID = "artisan-service"
	if _, err := s.Execute(ctx, k, "h2", create("c")); err != nil {
		t.Fatalf("other caller should not conflict: %v", err)
	}
}

func TestConcurrentSameKeyExecutesOnce(t *testing.T) {
	s, db := newStore(t, 5*time.Second)
	ctx := context.Background()
	var wg sync.WaitGroup
	var ok, inProgress, replayed atomic.Int32
	start := make(chan struct{})
	slow := func(ctx context.Context, tx pgx.Tx) (Result, error) {
		time.Sleep(100 * time.Millisecond)
		return create("x")(ctx, tx)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := s.Execute(ctx, key("same"), "h", slow)
			switch {
			case err == nil && r.Replayed:
				replayed.Add(1)
			case err == nil:
				ok.Add(1)
			case apperrors.CodeOf(err) == apperrors.CodeIdempotencyInProgress:
				inProgress.Add(1)
			default:
				t.Errorf("unexpected error %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("expected exactly one execution, got %d (inprogress=%d replayed=%d)", ok.Load(), inProgress.Load(), replayed.Load())
	}
	if countWidgets(t, db) != 1 {
		t.Fatalf("widgets=%d", countWidgets(t, db))
	}
}

func TestCrashRecoveryAfterLeaseExpiry(t *testing.T) {
	s, db := newStore(t, 200*time.Millisecond)
	ctx := context.Background()
	// Simulate a crashed executor: claim acquired, never completed.
	claim, rec, err := s.Begin(ctx, key("crash"), "h")
	if err != nil || claim == nil || rec != nil {
		t.Fatalf("begin %v", err)
	}
	if _, err := s.Execute(ctx, key("crash"), "h", create("a")); apperrors.CodeOf(err) != apperrors.CodeIdempotencyInProgress {
		t.Fatalf("expected in-progress while lease valid, got %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	r, err := s.Execute(ctx, key("crash"), "h", create("a"))
	if err != nil || r.Status != 201 {
		t.Fatalf("recovery: %+v %v", r, err)
	}
	// The stale executor wakes up and tries to complete: fencing rejects it.
	err = s.Complete(ctx, db.Pool, claim, 201, []byte(`{}`), "", "")
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale executor must lose: %v", err)
	}
	if countWidgets(t, db) != 1 {
		t.Fatal("duplicate execution")
	}
}

func TestCrashBetweenCommitAndResponseReplays(t *testing.T) {
	s, db := newStore(t, 5*time.Second)
	ctx := context.Background()
	// Execute commits; the "response is lost" (client retries).
	if _, err := s.Execute(ctx, key("lost"), "h", create("a")); err != nil {
		t.Fatal(err)
	}
	r, err := s.Execute(ctx, key("lost"), "h", create("a"))
	if err != nil || !r.Replayed {
		t.Fatalf("retry after lost response must replay: %+v %v", r, err)
	}
	if countWidgets(t, db) != 1 {
		t.Fatal("duplicate execution")
	}
}

func TestFailureClassification(t *testing.T) {
	s, db := newStore(t, 5*time.Second)
	ctx := context.Background()
	var calls atomic.Int32
	definite := func(context.Context, pgx.Tx) (Result, error) {
		calls.Add(1)
		return Result{}, apperrors.Unprocessable("WIDGET_REJECTED", "Widget rejected.")
	}
	_, err := s.Execute(ctx, key("final"), "h", definite)
	if apperrors.CodeOf(err) != "WIDGET_REJECTED" {
		t.Fatal(err)
	}
	r, err := s.Execute(ctx, key("final"), "h", definite)
	if err != nil || !r.Replayed || r.Status != 422 || calls.Load() != 1 {
		t.Fatalf("definite failure must replay: %+v %v calls=%d", r, err, calls.Load())
	}
	var env apperrors.Envelope
	_ = json.Unmarshal(r.Body, &env)
	if env.Error.Code != "WIDGET_REJECTED" {
		t.Fatalf("replayed body %s", r.Body)
	}

	transient := func(context.Context, pgx.Tx) (Result, error) {
		calls.Add(1)
		return Result{}, errors.New("db hiccup")
	}
	calls.Store(0)
	if _, err := s.Execute(ctx, key("retry"), "h", transient); err == nil {
		t.Fatal("expected error")
	}
	r, err = s.Execute(ctx, key("retry"), "h", create("ok"))
	if err != nil || r.Replayed || r.Status != 201 {
		t.Fatalf("retryable failure must allow retry: %+v %v", r, err)
	}
	if countWidgets(t, db) != 1 {
		t.Fatal("rolled back work leaked")
	}
}

func TestInvalidKeyAndPurge(t *testing.T) {
	s, db := newStore(t, time.Second)
	ctx := context.Background()
	if _, _, err := s.Begin(ctx, key("bad key with spaces"), "h"); apperrors.CodeOf(err) != "INVALID_IDEMPOTENCY_KEY" {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Execute(ctx, key("old"), "h", create("a")); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Pool.Exec(ctx, `UPDATE idempotency_records SET expires_at = now() - interval '1 minute'`)
	// Expired key can be reused even with a different payload.
	if _, err := s.Execute(ctx, key("old"), "different", create("b")); err != nil {
		t.Fatalf("expired key reuse: %v", err)
	}
	_, _ = db.Pool.Exec(ctx, `UPDATE idempotency_records SET expires_at = now() - interval '1 minute'`)
	n, err := s.Purge(ctx)
	if err != nil || n != 1 {
		t.Fatalf("purge %d %v", n, err)
	}
}

func TestPrecheckRunsAfterClaimAndNeverPreemptsReplay(t *testing.T) {
	s, db := newStore(t, 5*time.Second)
	ctx := context.Background()
	done := false
	pre := func(context.Context) error {
		if done { // state the first execution changed
			return apperrors.InvalidStateTransition("DONE", "DONE")
		}
		return nil
	}
	fn := func(ctx context.Context, tx pgx.Tx) (Result, error) { done = true; return create("a")(ctx, tx) }
	if _, err := s.Run(ctx, key("pre"), "h", pre, fn); err != nil {
		t.Fatal(err)
	}
	r, err := s.Run(ctx, key("pre"), "h", pre, fn)
	if err != nil || !r.Replayed {
		t.Fatalf("replay must not run the precheck: %+v %v", r, err)
	}
	// A failing precheck on a fresh key is recorded as a definite failure.
	_, err = s.Run(ctx, key("pre2"), "h", pre, fn)
	if apperrors.CodeOf(err) != apperrors.CodeInvalidStateTransition {
		t.Fatal(err)
	}
	r, _ = s.Run(ctx, key("pre2"), "h", nil, fn)
	if !r.Replayed || r.Status != 409 || countWidgets(t, db) != 1 {
		t.Fatalf("failed precheck must replay its error: %+v", r)
	}
}
