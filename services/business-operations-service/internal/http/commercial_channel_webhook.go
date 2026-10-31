package businesshttp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type CommercialWebhookKeyCommand struct {
	Version    int64  `json:"version"`
	KeyBase64  string `json:"key_base64"`
	Reason     string `json:"reason"`
	ApprovalID ids.ID `json:"approval_id"`
}
type CommercialWebhookKeyOperation struct {
	Version           int64  `json:"version"`
	SecretFingerprint string `json:"secret_fingerprint"`
	Reason            string `json:"reason"`
	ApprovalID        ids.ID `json:"approval_id"`
}

func webhookKeyAAD(org, conn ids.ID) []byte {
	return []byte("business-supply-webhook.v1:" + org.String() + ":" + conn.String())
}
func (h *Handler) channelWebhookKeySet(w http.ResponseWriter, r *http.Request) {
	var in CommercialWebhookKeyCommand
	if !decode(w, r, &in) {
		return
	}
	_, c, e := h.channelTarget(r, "channels:credentials", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	key, e := base64.StdEncoding.DecodeString(in.KeyBase64)
	if e != nil || len(key) != 32 || in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A base64-encoded random 32-byte callback key, current version and reason required."))
		return
	}
	defer clear(key)
	if c.ProviderCode != distribution.Marketplace || h.Auth.Service.Sealer == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("This versioned callback signing contract or protected key storage is unavailable for the provider."))
		return
	}
	sum := sha256.Sum256(key)
	operation := CommercialWebhookKeyOperation{Version: in.Version, SecretFingerprint: hex.EncodeToString(sum[:]), Reason: in.Reason, ApprovalID: in.ApprovalID}
	ciphertext := h.Auth.Service.Sealer.Seal(key, webhookKeyAAD(c.OrganisationID, c.ID))
	// Neither the idempotency request body, approval operation, event, audit nor
	// response contains the callback key. The key is sealed with tenant/connection AAD.
	h.execute(w, r, "commercial:channel:webhook-key:"+c.ID.String(), operation, "channels:credentials", "PROPERTY", c.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "channels:credentials", "PROPERTY", c.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := h.loadChannelConnection(ctx, tx, t.Organisation, c.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Connection changed before callback-key rotation.")
		}
		command := operation
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CHANNEL_CHANGE", target.Scope, in.ApprovalID, map[string]any{"connection_id": c.ID, "callback_key": command}, c.ProviderCode, nil, ""); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE channel_connections SET encrypted_credentials=$3,credential_key_id=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, c.ID, ciphertext, h.Auth.Service.Sealer.KeyID()); e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, c.ID, "channel_connection", "CHANNEL_WEBHOOK_KEY_ROTATED", businessEvents.ChannelConnectionChanged, current.Version+1, map[string]any{"callback_signing_configured": true, "reason": in.Reason})
		return 200, map[string]any{"connection_id": c.ID, "version": current.Version + 1, "callback_signing_configured": true}, e
	})
}
func (h *Handler) DistributionWebhookRouter(verifier *serviceauth.Verifier) http.Handler {
	r := chi.NewRouter()
	r.Use(serviceauth.Authenticate(verifier, serviceauth.HeaderServiceAuthorization))
	r.Use(serviceauth.RequireCaller(platform.APIGateway))
	r.Post("/{provider}/{connection_id}", h.channelProviderWebhook)
	return r
}
func (h *Handler) channelProviderWebhook(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "provider") != distribution.Marketplace {
		reply(w, r, 0, nil, apperrors.Unavailable("No certified provider-specific callback signature contract is configured."))
		return
	}
	id, e := ids.ParseV7(chi.URLParam(r, "connection_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid connection identifier."))
		return
	}
	var org ids.ID
	var ciphertext []byte
	var keyID *string
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT organisation_id,credential_key_id,encrypted_credentials FROM channel_connections WHERE id=$1`, id).Scan(&org, &keyID, &ciphertext); e != nil || keyID == nil || len(ciphertext) == 0 || h.Auth.Service.Sealer == nil {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Provider callback authentication failed."))
		return
	}
	key, e := h.Auth.Service.Sealer.Open(*keyID, ciphertext, webhookKeyAAD(org, id))
	if e != nil {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Provider callback authentication failed."))
		return
	}
	defer clear(key)
	body, e := io.ReadAll(io.LimitReader(r.Body, 65537))
	if e != nil || !distribution.VerifySupplyWebhook(key, body, r.URL.Path, r.Header.Get("X-CF-Timestamp"), r.Header.Get("X-CF-Signature"), h.Auth.Service.Now().UTC()) {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Provider callback authentication failed."))
		return
	}
	var in CommercialReceiptCommand
	if e = json.Unmarshal(body, &in); e != nil || in.ConnectionID != id || in.OrganisationID != org {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Signed callback context does not match its connection."))
		return
	}
	c, e := h.loadChannelConnection(r.Context(), h.Auth.Service.Pool, org, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if c.ProviderCode != distribution.Marketplace {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Provider callback identity mismatch."))
		return
	}
	if e = h.requireChannelCapability(r.Context(), h.Auth.Service.Pool, c, "WEBHOOK"); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var result CommercialReceiptResult
	var domainErr error
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); e != nil {
			return e
		}
		var failure error
		result, domainErr, failure = h.acceptChannelReceipt(ctx, tx, c, in.Receipt)
		return failure
	})
	if e == nil {
		e = domainErr
	}
	reply(w, r, 202, result, e)
}
