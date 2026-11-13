package businesshttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// opsCommit records history, audit and the outbox event for a governed
// Sections 12-24 entity in its owning transaction.
func (h *Handler) opsCommit(ctx context.Context, tx pgx.Tx, t tenant, property, target ids.ID, kind, action, from, to, event string, version int64, details any) error {
	return businessEvents.OperationCommit(ctx, tx, h.Auth.Service.Outbox, t.Organisation, property, target, t.Account, kind, action, from, to, event, version, details)
}

// opsService marks the transaction as acting under the explicit service
// identity. Workers never borrow a human session or MFA.
func opsService(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`)
	return e
}

// opsList returns a paginated, tenant- and property-scoped JSON projection.
// query must use $1 organisation, $2 property, then extra args, and end with
// LIMIT/OFFSET placeholders supplied last.
func (h *Handler) opsList(w http.ResponseWriter, r *http.Request, capability, query string, extra ...any) {
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A property_id scope is required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	args := append([]any{t.Organisation, property}, extra...)
	args = append(args, limit, offset)
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, query, args...)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}

// opsRecordProperty reads the property scope of a tenant-owned record using a
// constant table identifier, then authorizes the caller on that property.
func (h *Handler) opsRecordProperty(r *http.Request, table, capability string, mfa bool) (tenant, ids.ID, ids.ID, error) {
	id, e := pathCommercialID(r)
	if e != nil {
		return tenant{}, ids.Nil, ids.Nil, e
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, ids.Nil, id, e
	}
	if !opsTables[table] {
		return t, ids.Nil, id, apperrors.Unavailable("Unknown operational collection.")
	}
	var property ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM `+table+` WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&property); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return t, ids.Nil, id, apperrors.NotFound("RESOURCE_NOT_FOUND", "Resource not found.")
		}
		return t, ids.Nil, id, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", property, mfa)
	return t, property, id, e
}

var opsTables = map[string]bool{}

func registerOpsTable(names ...string) {
	for _, n := range names {
		opsTables[n] = true
	}
}

func opsText(v string, min, max int) bool {
	n := len(strings.TrimSpace(v))
	return n >= min && len(v) <= max && !strings.ContainsRune(v, 0)
}

// opsLock locks one tenant record for update in the owning transaction.
func opsLock[T any](ctx context.Context, q database.Querier, table string, org, id ids.ID) (T, error) {
	var zero T
	if !opsTables[table] {
		return zero, apperrors.Unavailable("Unknown operational collection.")
	}
	v, e := typedJSON[T](ctx, q, `SELECT to_jsonb(x) FROM `+table+` x WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, org, id)
	return v, opsNotFound(e)
}

// opsGet reads one tenant record without locking.
func opsGet[T any](ctx context.Context, q database.Querier, table string, org, id ids.ID) (T, error) {
	var zero T
	if !opsTables[table] {
		return zero, apperrors.Unavailable("Unknown operational collection.")
	}
	v, e := typedJSON[T](ctx, q, `SELECT to_jsonb(x) FROM `+table+` x WHERE organisation_id=$1 AND id=$2`, org, id)
	return v, opsNotFound(e)
}

func opsNotFound(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return apperrors.NotFound("RESOURCE_NOT_FOUND", "Resource not found.")
	}
	return e
}
