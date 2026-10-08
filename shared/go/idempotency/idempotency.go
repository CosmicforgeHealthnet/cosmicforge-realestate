// Package idempotency implements database-backed idempotent command handling
// (IDEMPOTENCY_AND_OUTBOX.md).
//
// Records are unique per (scope, caller_type, caller_id, idempotency_key) and
// live in the owning service's own database (table idempotency_records, see
// SchemaSQL). Semantics:
//
//   - first request: a record is claimed IN_PROGRESS with a lease, the command
//     runs, and the response is stored COMPLETED in the SAME transaction as the
//     business state change (so a crash between commit and response replays);
//   - same key + same request: the stored response is replayed;
//   - same key + different request: IDEMPOTENCY_CONFLICT;
//   - concurrent duplicate while running: IDEMPOTENCY_IN_PROGRESS (retry later);
//     serialization is by the unique constraint/row lock, never memory;
//   - process crash mid-command: the lease expires and the next retry
//     re-acquires; a fencing attempt counter ensures a stale executor cannot
//     also commit;
//   - definite 4xx failures are stored FAILED_FINAL and replayed; transient
//     failures are FAILED_RETRYABLE and may be retried with the same key.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Status of a record.
type Status string

const (
	StatusInProgress      Status = "IN_PROGRESS"
	StatusCompleted       Status = "COMPLETED"
	StatusFailedRetryable Status = "FAILED_RETRYABLE"
	StatusFailedFinal     Status = "FAILED_FINAL"
)

// Caller types.
const (
	CallerService  = "SERVICE"
	CallerUser     = "USER"
	CallerProvider = "PROVIDER"
)

// SchemaSQL is the canonical table definition. Each service includes it in
// its own Goose migrations.
const SchemaSQL = `
CREATE TABLE idempotency_records (
    id               uuid PRIMARY KEY,
    scope            text        NOT NULL,
    caller_type      text        NOT NULL,
    caller_id        text        NOT NULL,
    idempotency_key  text        NOT NULL,
    request_hash     text        NOT NULL,
    status           text        NOT NULL CHECK (status IN ('IN_PROGRESS','COMPLETED','FAILED_RETRYABLE','FAILED_FINAL')),
    response_code    integer,
    response_body    bytea,
    resource_type    text,
    resource_id      text,
    attempts         integer     NOT NULL DEFAULT 1 CHECK (attempts > 0),
    locked_until     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    CONSTRAINT idempotency_records_key_uniq UNIQUE (scope, caller_type, caller_id, idempotency_key),
    CONSTRAINT idempotency_records_key_len CHECK (char_length(idempotency_key) BETWEEN 1 AND 255)
);
CREATE INDEX idempotency_records_expires_idx ON idempotency_records (expires_at);
`

// Key identifies a record.
type Key struct {
	Scope      string // operation, e.g. "verification.submit"
	CallerType string // SERVICE | USER | PROVIDER
	CallerID   string // service name, "<domain>:<uuid>", or provider name
	Key        string // client-supplied Idempotency-Key
}

func (k Key) validate() error {
	if k.Scope == "" || k.CallerType == "" || k.CallerID == "" {
		return errors.New("idempotency: incomplete key")
	}
	if !requestcontext.ValidIdempotencyKey(k.Key) {
		return apperrors.Invalid("INVALID_IDEMPOTENCY_KEY", "Idempotency-Key must be 1-255 visible ASCII characters.")
	}
	return nil
}

// Fingerprint hashes the semantically relevant request parts. JSON bodies
// are canonicalised (key order and whitespace do not matter).
func Fingerprint(method, path string, body []byte) (string, error) {
	canon := body
	if len(bytes.TrimSpace(body)) > 0 {
		var v any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return "", apperrors.New(400, apperrors.CodeInvalidJSON, "Request body is not valid JSON.")
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		canon = b
	}
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(canon)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Record is a stored idempotency record.
type Record struct {
	ID           ids.ID
	Key          Key
	RequestHash  string
	Status       Status
	ResponseCode int
	ResponseBody []byte
	ResourceType string
	ResourceID   string
	Attempts     int
	LockedUntil  *time.Time
	ExpiresAt    time.Time
}

// Claim is an acquired, in-progress record.
type Claim struct {
	ID      ids.ID
	Attempt int
}

var (
	// ErrConflict: same key, different request.
	ErrConflict = apperrors.IdempotencyConflict()
	// ErrInProgress: same key currently executing.
	ErrInProgress = apperrors.IdempotencyInProgress()
	// ErrLeaseLost: this executor's lease was taken over; its work must roll back.
	ErrLeaseLost = errors.New("idempotency: lease lost")
)

// Store is the PostgreSQL implementation.
type Store struct {
	db    database.DB
	lease time.Duration
	ttl   time.Duration
}

// NewStore returns a store. lease bounds how long a crashed executor blocks
// retries (default 30s); ttl is how long keys are remembered (default 24h).
func NewStore(db database.DB, lease, ttl time.Duration) *Store {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Store{db: db, lease: lease, ttl: ttl}
}

const selectCols = `id, scope, caller_type, caller_id, idempotency_key, request_hash, status,
	coalesce(response_code, 0), response_body, coalesce(resource_type, ''), coalesce(resource_id, ''),
	attempts, locked_until, expires_at`

func scan(row pgx.Row) (Record, error) {
	var r Record
	var status string
	err := row.Scan(&r.ID, &r.Key.Scope, &r.Key.CallerType, &r.Key.CallerID, &r.Key.Key, &r.RequestHash, &status,
		&r.ResponseCode, &r.ResponseBody, &r.ResourceType, &r.ResourceID, &r.Attempts, &r.LockedUntil, &r.ExpiresAt)
	r.Status = Status(status)
	return r, err
}

// Begin claims the key or returns the stored outcome. Exactly one of
// (*Claim, *Record) is non-nil on success; a *Record means "replay".
func (s *Store) Begin(ctx context.Context, k Key, requestHash string) (*Claim, *Record, error) {
	if err := k.validate(); err != nil {
		return nil, nil, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		id := ids.New()
		var claimed ids.ID
		err := s.db.QueryRow(ctx, `
			INSERT INTO idempotency_records
			  (id, scope, caller_type, caller_id, idempotency_key, request_hash, status, attempts, locked_until, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,'IN_PROGRESS',1, now() + $7::interval, now() + $8::interval)
			ON CONFLICT ON CONSTRAINT idempotency_records_key_uniq DO NOTHING
			RETURNING id`,
			id, k.Scope, k.CallerType, k.CallerID, k.Key, requestHash, interval(s.lease), interval(s.ttl)).Scan(&claimed)
		if err == nil {
			return &Claim{ID: claimed, Attempt: 1}, nil, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
		rec, err := scan(s.db.QueryRow(ctx, `SELECT `+selectCols+` FROM idempotency_records
			WHERE scope=$1 AND caller_type=$2 AND caller_id=$3 AND idempotency_key=$4`,
			k.Scope, k.CallerType, k.CallerID, k.Key))
		if errors.Is(err, pgx.ErrNoRows) {
			continue // deleted concurrently (expiry purge); retry insert
		}
		if err != nil {
			return nil, nil, err
		}
		if rec.ExpiresAt.Before(time.Now()) {
			// Expired keys may be reused: remove and retry the insert.
			if _, err := s.db.Exec(ctx, `DELETE FROM idempotency_records WHERE id=$1 AND expires_at < now()`, rec.ID); err != nil {
				return nil, nil, err
			}
			continue
		}
		if rec.RequestHash != requestHash {
			return nil, nil, ErrConflict
		}
		switch rec.Status {
		case StatusCompleted, StatusFailedFinal:
			return nil, &rec, nil
		}
		// IN_PROGRESS with an expired lease (crashed executor) or
		// FAILED_RETRYABLE: try to take over atomically.
		var att int
		err = s.db.QueryRow(ctx, `
			UPDATE idempotency_records
			   SET status='IN_PROGRESS', attempts = attempts + 1, locked_until = now() + $2::interval, updated_at = now()
			 WHERE id = $1 AND request_hash = $3
			   AND (status = 'FAILED_RETRYABLE' OR (status = 'IN_PROGRESS' AND locked_until < now()))
			RETURNING attempts`, rec.ID, interval(s.lease), requestHash).Scan(&att)
		if err == nil {
			return &Claim{ID: rec.ID, Attempt: att}, nil, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
		if rec.Status == StatusInProgress {
			return nil, nil, ErrInProgress
		}
		// State changed concurrently; re-evaluate.
	}
	return nil, nil, ErrInProgress
}

func interval(d time.Duration) string { return fmt.Sprintf("%d milliseconds", d.Milliseconds()) }

// Complete marks the claim COMPLETED with the response, inside tx (the same
// transaction as the business change). ErrLeaseLost means another executor
// took over; the caller must roll back.
func (s *Store) Complete(ctx context.Context, tx database.Querier, c *Claim, code int, body []byte, resourceType, resourceID string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE idempotency_records
		   SET status='COMPLETED', response_code=$3, response_body=$4, resource_type=nullif($5,''),
		       resource_id=nullif($6,''), locked_until=NULL, updated_at=now()
		 WHERE id=$1 AND attempts=$2 AND status='IN_PROGRESS'`,
		c.ID, c.Attempt, code, jsonOrNull(body), resourceType, resourceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Fail records a failure outside the (rolled back) business transaction.
// final=true stores the response for replay (definite failure).
func (s *Store) Fail(ctx context.Context, c *Claim, final bool, code int, body []byte) error {
	status := StatusFailedRetryable
	if final {
		status = StatusFailedFinal
	} else {
		body, code = nil, 0
	}
	_, err := s.db.Exec(ctx, `
		UPDATE idempotency_records
		   SET status=$3, response_code=nullif($4,0), response_body=$5, locked_until=NULL, updated_at=now()
		 WHERE id=$1 AND attempts=$2 AND status='IN_PROGRESS'`,
		c.ID, c.Attempt, string(status), code, jsonOrNull(body))
	return err
}

func jsonOrNull(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// Purge deletes expired records; run periodically from a worker.
func (s *Store) Purge(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM idempotency_records WHERE expires_at < now()`)
	return tag.RowsAffected(), err
}

// Result is what a command returns for storage and response.
type Result struct {
	Status       int
	Body         any // marshalled to JSON as-is (use httpx.DataEnvelope for {"data":...})
	ResourceType string
	ResourceID   string
}

// Response is the HTTP response to write.
type Response struct {
	Status   int
	Body     []byte
	Replayed bool
}

// Execute runs fn exactly once per (key, request) and returns the response
// to write. fn runs inside a transaction together with the completion
// record; it must not perform external network calls.
func (s *Store) Execute(ctx context.Context, k Key, requestHash string, fn func(ctx context.Context, tx pgx.Tx) (Result, error)) (Response, error) {
	return s.Run(ctx, k, requestHash, nil, fn)
}

// Run is Execute with an optional precheck. pre runs only after the key is
// claimed (so replays are never pre-empted by state that the original
// request itself changed) and outside the transaction, which makes it the
// place for read-only calls to other services. A pre failure is recorded
// like a command failure (definite 4xx replayed; transient retryable).
func (s *Store) Run(ctx context.Context, k Key, requestHash string, pre func(ctx context.Context) error,
	fn func(ctx context.Context, tx pgx.Tx) (Result, error)) (Response, error) {
	claim, rec, err := s.Begin(ctx, k, requestHash)
	if err != nil {
		return Response{}, err
	}
	if rec != nil {
		return Response{Status: rec.ResponseCode, Body: rec.ResponseBody, Replayed: true}, nil
	}
	if pre != nil {
		if err := pre(ctx); err != nil {
			return Response{}, s.recordFailure(ctx, claim, err)
		}
	}
	var resp Response
	err = database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		res, err := fn(ctx, tx)
		if err != nil {
			return err
		}
		body, err := json.Marshal(res.Body)
		if err != nil {
			return err
		}
		if err := s.Complete(ctx, tx, claim, res.Status, body, res.ResourceType, res.ResourceID); err != nil {
			return err
		}
		resp = Response{Status: res.Status, Body: body}
		return nil
	})
	if err == nil {
		return resp, nil
	}
	if errors.Is(err, ErrLeaseLost) {
		return Response{}, ErrInProgress
	}
	return Response{}, s.recordFailure(ctx, claim, err)
}

// recordFailure stores a failed outcome and returns err (joined with any
// bookkeeping error).
func (s *Store) recordFailure(ctx context.Context, claim *Claim, err error) error {
	fctx := context.WithoutCancel(ctx)
	ae := apperrors.From(err)
	// Definite client-side failures are replayed. Errors explicitly marked
	// retryable (e.g. "upload not received yet") let the client retry with
	// the same key.
	if ae.Status >= 400 && ae.Status < 500 && ae.Status != 408 && ae.Status != 429 &&
		ae.Code != apperrors.CodeIdempotencyInProgress && ae.Outcome != apperrors.OutcomeRetryable {
		body, _ := json.Marshal(apperrors.ToEnvelope(ae, requestcontext.RequestID(ctx)))
		if ferr := s.Fail(fctx, claim, true, ae.Status, body); ferr != nil {
			return errors.Join(err, ferr)
		}
		return err
	}
	if ferr := s.Fail(fctx, claim, false, 0, nil); ferr != nil {
		return errors.Join(err, ferr)
	}
	return err
}
