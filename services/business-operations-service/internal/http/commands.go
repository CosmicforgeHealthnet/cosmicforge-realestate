package businesshttp

import (
	"context"
	"encoding/json"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

// execute uses the established shared idempotency store. No external call or
// secret-bearing response may be placed in fn. Authorization is checked before
// replay and must be rechecked by fn in its owning transaction.
func (h *Handler) execute(w http.ResponseWriter, r *http.Request, operation string, payload any, capability, kind string, id ids.ID, stepUp bool, fn func(context.Context, pgx.Tx) (int, any, error)) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, capability, kind, id, stepUp)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	hash, err := idempotency.Fingerprint(r.Method, operation, body)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	store := idempotency.NewStore(h.Auth.Service.Pool, 30*time.Second, 7*24*time.Hour)
	response, err := store.Execute(r.Context(), idempotency.Key{Scope: "business:" + t.Organisation.String() + ":" + operation, CallerType: idempotency.CallerUser, CallerID: "business-operations:" + t.Account.String(), Key: r.Header.Get("Idempotency-Key")}, hash, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		// Read expected versions only after the common tenant lock. Triggers
		// alone lock too late if a concurrent hold handoff changed ownership.
		if _, err := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, t.Organisation); err != nil {
			return idempotency.Result{}, err
		}
		status, out, err := fn(ctx, tx)
		if err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{Status: status, Body: map[string]any{"data": out}}, nil
	})
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if response.Replayed {
		w.Header().Set(idempotency.HeaderReplayed, "true")
	}
	httpx.WriteRaw(w, response.Status, response.Body)
}
