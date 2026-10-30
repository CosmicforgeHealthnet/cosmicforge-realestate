package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (h *Handler) renewKYB(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Previous ids.ID `json:"previous_verification_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Previous.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A previous case reference is required."))
		return
	}
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "organisation:verify", "COMPANY", ids.Nil, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	provider, ok := h.Verification.(verification.RenewalAPI)
	if !ok {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification renewal contract is unavailable."))
		return
	}
	var current ids.ID
	if err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT case_id FROM verification_links WHERE organisation_id=$1 AND purpose='KYB'`, t.Organisation).Scan(&current); err != nil {
		reply(w, r, 0, nil, apperrors.NotFound("VERIFICATION_NOT_FOUND", "No KYB case is linked."))
		return
	}
	if current != in.Previous {
		var completed ids.ID
		if err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT replacement_case_id FROM verification_link_history WHERE organisation_id=$1 AND case_id=$2`, t.Organisation, in.Previous).Scan(&completed); err != nil || completed != current {
			reply(w, r, 0, nil, apperrors.Conflict("VERIFICATION_VERSION_CONFLICT", "The linked verification changed."))
			return
		}
		c, err := h.Verification.Get(r.Context(), current)
		if err != nil {
			reply(w, r, 0, nil, apperrors.Unavailable("Authoritative verification status is unavailable."))
			return
		}
		err = h.applyVerification(r.Context(), t, c)
		reply(w, r, 202, c, err)
		return
	}
	old, err := h.Verification.Get(r.Context(), in.Previous)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Authoritative verification status is unavailable."))
		return
	}
	if old.VerificationID != in.Previous || old.Subject.ID != t.Organisation {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Verification case identity mismatch."))
		return
	}
	switch old.Status {
	case "VERIFIED", "REJECTED", "EXPIRED", "RESTRICTED":
	default:
		reply(w, r, 0, nil, apperrors.Conflict("VERIFICATION_NOT_DECIDED", "Only a decided or expired case can be renewed."))
		return
	}
	reference := "business-kyb:" + t.Organisation.String() + ":renew:" + in.Previous.String()
	created, err := provider.Reverify(r.Context(), in.Previous, reference, reference)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Verification renewal could not be completed."))
		return
	}
	c, err := h.Verification.Get(r.Context(), created.VerificationID)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Renewal was requested; authoritative read-back is unavailable. Retry the same previous case reference."))
		return
	}
	if c.VerificationID.ValidateV7() != nil || c.SupersedesID == nil || *c.SupersedesID != in.Previous || c.Type != verification.TypeBusiness || c.Subject.Type != verification.SubjectBusiness || c.Subject.Domain != "business-operations" || c.Subject.ID != t.Organisation {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Renewal does not preserve verified subject/supersedes identity."))
		return
	}
	// The external call is outside the owning transaction. The stable provider
	// key makes retries recover the same case if the local commit fails.
	err = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		actor, _, err := h.authorize(ctx, tx, "organisation:verify", "COMPANY", ids.Nil, true)
		if err != nil {
			return err
		}
		var linked ids.ID
		if err = tx.QueryRow(ctx, `SELECT case_id FROM verification_links WHERE organisation_id=$1 AND purpose='KYB' FOR UPDATE`, t.Organisation).Scan(&linked); err != nil {
			return err
		}
		if linked == c.VerificationID {
			return nil
		}
		if linked != in.Previous {
			return apperrors.Conflict("VERIFICATION_VERSION_CONFLICT", "The linked case changed during renewal.")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO verification_link_history(id,organisation_id,case_id,replacement_case_id,provider_status,expires_at,provider_updated_at,actor_id) SELECT $1,organisation_id,case_id,$3,provider_status,expires_at,provider_updated_at,$4 FROM verification_links WHERE organisation_id=$2 AND purpose='KYB'`, ids.New(), t.Organisation, c.VerificationID, t.Account); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE verification_links SET case_id=$2,provider_status=$3,expires_at=$4,provider_updated_at=$5,updated_at=now() WHERE organisation_id=$1 AND purpose='KYB'`, t.Organisation, c.VerificationID, c.Status, c.ExpiresAt, c.UpdatedAt); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE organisations SET state='RESTRICTED',restriction_source='KYB',version=version+1,updated_at=now() WHERE id=$1 AND state IN ('ACTIVE','KYB_PENDING') AND restriction_source<>'ADMIN'`, t.Organisation); err != nil {
			return err
		}
		return h.accessEvent(ctx, tx, actor, "KYB_RENEWAL_LINKED", "business.verification.renewed.v1", "verification", c.VerificationID, map[string]any{"previous_case_id": in.Previous, "case_id": c.VerificationID})
	})
	if err == nil {
		err = h.applyVerification(r.Context(), t, c)
	}
	reply(w, r, 202, c, err)
}
