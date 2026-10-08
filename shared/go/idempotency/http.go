package idempotency

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// HeaderReplayed marks responses served from a stored idempotent result.
const HeaderReplayed = "Idempotent-Replayed"

// CallerKey derives the caller part of the idempotency scope from the
// authenticated request context: the delegated user when present, otherwise
// the calling service.
func CallerKey(ctx context.Context) (callerType, callerID string, ok bool) {
	rc := requestcontext.From(ctx)
	if !rc.Actor.IsZero() {
		return CallerUser, rc.Actor.Domain + ":" + rc.Actor.ID.String(), true
	}
	if rc.CallingService != "" {
		return CallerService, rc.CallingService, true
	}
	return "", "", false
}

// ServeCommand executes fn exactly once per (scope, caller, Idempotency-Key,
// request fingerprint) and writes the (possibly replayed) response.
//
// pre (optional) runs after the key is claimed and before the transaction;
// use it for read-only checks against other services.
func (s *Store) ServeCommand(w http.ResponseWriter, r *http.Request, scope string, body []byte,
	pre func(ctx context.Context) error, fn func(ctx context.Context, tx pgx.Tx) (Result, error)) {
	ctx := r.Context()
	key := requestcontext.From(ctx).IdempotencyKey
	if key == "" {
		if r.Header.Get(requestcontext.HeaderIdempotencyKey) != "" {
			httpx.WriteError(w, r, apperrors.Invalid("INVALID_IDEMPOTENCY_KEY", "Idempotency-Key must be 1-255 visible ASCII characters."))
			return
		}
		httpx.WriteError(w, r, apperrors.IdempotencyKeyRequired())
		return
	}
	ct, cid, ok := CallerKey(ctx)
	if !ok {
		httpx.WriteError(w, r, apperrors.AuthenticationRequired())
		return
	}
	hash, err := Fingerprint(r.Method, r.URL.Path, body)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := s.Run(ctx, Key{Scope: scope, CallerType: ct, CallerID: cid, Key: key}, hash, pre, fn)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if resp.Replayed {
		w.Header().Set(HeaderReplayed, "true")
	}
	httpx.WriteRaw(w, resp.Status, resp.Body)
}
