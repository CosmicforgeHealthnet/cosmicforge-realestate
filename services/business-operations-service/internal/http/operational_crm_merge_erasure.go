package businesshttp

import (
	"context"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// eraseMergedMembers removes the protected data of records merged into an
// erased identity. They describe the same data subject; MERGED lineage and
// retained operational/financial evidence stay intact.
func (h *Handler) eraseMergedMembers(ctx context.Context, tx pgx.Tx, org, guest ids.ID, erased crm.Profile) (int, error) {
	members, e := guestFamily(ctx, tx, org, guest)
	if e != nil {
		return 0, e
	}
	sealer := h.Auth.Service.Sealer
	count := 0
	for _, member := range members {
		if member == guest {
			continue
		}
		tag, e := tx.Exec(ctx, `UPDATE guest_profiles SET encrypted_profile=$3,profile_key_id=$4,registration_reference='anonymised:'||id::text,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state='MERGED' AND registration_reference NOT LIKE 'anonymised:%'`, org, member, sealer.Seal(marshalCommercial(erased), crm.ProfileAAD(org, member)), sealer.KeyID())
		if e != nil {
			return count, e
		}
		count += int(tag.RowsAffected())
		preferences, e := queryMergeRows(ctx, tx, `SELECT id,property_id FROM guest_preferences WHERE organisation_id=$1 AND guest_id=$2`, org, member)
		if e != nil {
			return count, e
		}
		for _, p := range preferences {
			if _, e = tx.Exec(ctx, `UPDATE guest_preferences SET encrypted_preferences=$3,preference_key_id=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, p.id, sealer.Seal([]byte(`{}`), crm.PreferencesAAD(org, member, p.property)), sealer.KeyID()); e != nil {
				return count, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE guest_identity_links SET state='REVOKED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND guest_id=$2 AND state='ACTIVE'`, org, member); e != nil {
			return count, e
		}
		if _, e = tx.Exec(ctx, `UPDATE loyalty_accounts SET state='CLOSED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND guest_id=$2 AND state<>'CLOSED'`, org, member); e != nil {
			return count, e
		}
	}
	return count, nil
}
