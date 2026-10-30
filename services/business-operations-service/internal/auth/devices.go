package auth

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

// Client labels are display metadata. No endpoint accepts a trusted-device
// flag, risk override or a client-selected association identifier.
func (h *Handler) updateDevice(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid device ID."))
		return
	}
	var in struct {
		Label   string `json:"label"`
		Version int64  `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(strings.TrimSpace(in.Label)) < 1 || len(in.Label) > 100 || in.Version < 1 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A label and current version are required."))
		return
	}
	p, _ := PrincipalFrom(r.Context())
	err = database.WithTx(r.Context(), h.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE business_devices SET label=$3,version=version+1 WHERE account_id=$1 AND id=$2 AND version=$4 AND retired_at IS NULL`, p.ActorID, id, in.Label, in.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apperrors.Conflict("VERSION_CONFLICT", "Device version changed or is unavailable.")
		}
		return h.Service.record(ctx, tx, &p.ActorID, "DEVICE_LABEL_CHANGED", id)
	})
	reply(w, r, 200, map[string]any{"device_id": id, "version": in.Version + 1}, err)
}
func (h *Handler) revokeDevice(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid device ID."))
		return
	}
	p, _ := PrincipalFrom(r.Context())
	err = database.WithTx(r.Context(), h.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE business_devices SET retired_at=coalesce(retired_at,now()),version=version+1 WHERE account_id=$1 AND id=$2`, p.ActorID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apperrors.NotFound("DEVICE_NOT_FOUND", "Device not found.")
		}
		if _, err = tx.Exec(ctx, `UPDATE business_sessions SET revoked_at=coalesce(revoked_at,now()),revoke_reason='DEVICE_REVOKED' WHERE account_id=$1 AND device_id=$2`, p.ActorID, id); err != nil {
			return err
		}
		return h.Service.record(ctx, tx, &p.ActorID, "DEVICE_REVOKED", id)
	})
	reply(w, r, 200, map[string]any{"device_id": id, "revoked": true}, err)
}
