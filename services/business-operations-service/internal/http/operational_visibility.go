package businesshttp

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Resolve authority before LIMIT/OFFSET. Filtering an already paginated result
// both hides permitted records and leaks the existence of forbidden properties
// through offsets. Reuse the same live grants, denies and emergency audit as
// detail requests; infrastructure failures must not become empty successes.
func (h *Handler) operationalProperties(ctx context.Context, q database.Querier, t tenant, capability, property, branch string) ([]ids.ID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM inventory_nodes WHERE organisation_id=$1 AND kind='PROPERTY' AND ($2='' OR id::text=$2) AND ($3='' OR branch_id::text=$3) ORDER BY id`, t.Organisation, property, branch)
	if err != nil {
		return nil, err
	}
	candidates := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if err = rows.Scan(&id); err != nil {
			break
		}
		candidates = append(candidates, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	permitted := []ids.ID{}
	for _, id := range candidates {
		_, _, err = h.authorize(ctx, q, capability, "PROPERTY", id, false)
		if err == nil {
			permitted = append(permitted, id)
			continue
		}
		var denied *apperrors.Error
		if !errors.As(err, &denied) || denied.Status != 403 {
			return nil, err
		}
	}
	return permitted, nil
}
