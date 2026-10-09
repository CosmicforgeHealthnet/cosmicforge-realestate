package usecases

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/dbx"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Domain use cases (Sections 6–13) follow one pattern:
//
//	authorize (authz engine, property facts from individual_owner_db)
//	→ load + lock aggregate → check preconditions → lifecycle CAS transition
//	→ write rows + immutable history + audit record + outbox event
//
// all inside one transaction. HTTP commands run inside the idempotency
// transaction (dbx.WithTx); other callers get a fresh transaction from InTx.

// InTx runs fn in the transaction carried by ctx, or in a new one.
func (s *Service) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := dbx.Tx(ctx); ok {
		return fn(ctx)
	}
	return database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		return fn(dbx.WithTx(ctx, tx))
	})
}

// dbq returns the transaction in ctx or the pool (reads).
func (s *Service) dbq(ctx context.Context) db.DBTX { return dbx.Q(ctx, s.Pool) }

// mustTx returns the transaction in ctx; writes outside a transaction are a
// programming error.
func mustTx(ctx context.Context) pgx.Tx {
	tx, ok := dbx.Tx(ctx)
	if !ok {
		panic("usecases: write outside a transaction")
	}
	return tx
}

// authorize evaluates action on a property (or the account when propertyID is zero).
func (s *Service) authorize(ctx context.Context, subj authz.Subject, action authz.Action, propertyID ids.ID) (authz.Decision, error) {
	res := authz.Resource{PropertyID: propertyID}
	if subj.Account != nil && propertyID.IsZero() {
		res.AccountID = subj.Account.AccountID
	}
	return s.Authz.Require(ctx, subj, action, res)
}

// accountOf returns the acting owner account (zero for service subjects).
func accountOf(subj authz.Subject) ids.ID {
	if subj.Account != nil {
		return subj.Account.AccountID
	}
	return ids.Nil
}

// record writes audit + outbox for a domain change inside the transaction.
func (s *Service) record(ctx context.Context, action, targetType string, target ids.ID, changes, md map[string]any) error {
	if md == nil {
		md = map[string]any{}
	}
	md["policy_version"] = s.Policy.Version
	_, err := s.audit.Record(ctx, mustTx(ctx), audit.Event{Action: action, TargetType: targetType, TargetID: target, Result: "SUCCESS", Changes: changes, Metadata: md})
	return err
}

// publish adds an integration event to the outbox inside the transaction.
func (s *Service) publish(ctx context.Context, eventType, aggregate string, id ids.ID, payload any) error {
	e, err := events.New(ctx, platform.IndividualOwnerService, eventType, aggregate, id, payload)
	if err != nil {
		return err
	}
	return s.Outbox.Add(ctx, mustTx(ctx), e)
}

// propertyHistory appends an owner-visible property history entry.
func (s *Service) propertyHistory(ctx context.Context, propertyID ids.ID, eventType, summary, refType string, refID ids.ID) error {
	rc := requestcontext.From(ctx)
	var dom *string
	var actor *ids.ID
	if !rc.Actor.IsZero() {
		dom, actor = &rc.Actor.Domain, &rc.Actor.ID
	}
	_, err := mustTx(ctx).Exec(ctx, `INSERT INTO property_history (id, property_id, event_type, summary, ref_type, ref_id, actor_domain, actor_id, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), propertyID, eventType, truncate(summary, 500), strp(refType), idp(refID), dom, actor, s.now())
	return err
}

// transition applies a lifecycle change to a locked aggregate row.
func (s *Service) transition(ctx context.Context, m *lifecycle.Machine, id ids.ID, ch lifecycle.Change) (lifecycle.Row, lifecycle.Row, error) {
	tx := mustTx(ctx)
	row, err := m.Lock(ctx, tx, id)
	if isNoRows(err) {
		return lifecycle.Row{}, lifecycle.Row{}, notFound(m.Aggregate)
	}
	if err != nil {
		return lifecycle.Row{}, lifecycle.Row{}, err
	}
	if ch.Now.IsZero() {
		ch.Now = s.now()
	}
	next, err := m.Apply(ctx, tx, row, ch)
	return row, next, err
}

func notFound(aggregate string) error {
	return apperrors.NotFound(upper(aggregate)+"_NOT_FOUND", "The requested resource was not found.")
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// unavailable is the honest answer for a feature whose external dependency
// is not configured or not implemented (Appendix D launch rule).
func unavailable(feature, dependency string) error {
	return apperrors.New(503, "FEATURE_UNAVAILABLE", feature+" is unavailable: "+dependency+" is not configured for this environment.").
		WithDetails(apperrors.Detail{Field: "dependency", Code: "DEPENDENCY_UNAVAILABLE", Message: dependency})
}

// precondition is a 422 business-rule failure.
func precondition(code, msg string) error { return apperrors.Unprocessable(code, msg) }

// conflict is a 409 state conflict.
func conflict(code, msg string) error { return apperrors.Conflict(code, msg) }

// checkVersion enforces If-Match on non-lifecycle updates.
func checkVersion(expected, actual int64) error {
	if expected > 0 && expected != actual {
		return lifecycle.ErrVersionConflict()
	}
	return nil
}

func isUnique(err error, constraint string) bool { return database.IsUniqueViolation(err, constraint) }

// actorFromCtx returns the attributable actor of the request, if any.
func actorFromCtx(ctx context.Context) *requestcontext.Actor {
	if a, ok := requestcontext.ActorFrom(ctx); ok && !a.IsZero() {
		return &a
	}
	return nil
}
