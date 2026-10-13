package usecases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type externalOperation struct {
	id         ids.ID
	key        string
	token      ids.ID
	recovering bool
	result     json.RawMessage
}

// claimExternal persists uncertainty BEFORE the external call. The existing
// domain workers use a fenced lease; a crash cannot erase the original key.
func (s *Service) claimExternal(ctx context.Context, kind string, id ids.ID, key string, request any) (externalOperation, bool, error) {
	op := externalOperation{id: id, key: key, token: ids.New()}
	raw, err := json.Marshal(request)
	if err != nil {
		return op, false, err
	}
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	claimed := false
	err = s.InTx(ctx, func(ctx context.Context) error {
		tx, now := mustTx(ctx), s.now()
		tag, err := tx.Exec(ctx, `INSERT INTO owner_external_operations(operation_key,kind,aggregate_id,request_hash,state,created_at,updated_at) VALUES($1,$2,$3,$4,'UNKNOWN',$5,$5) ON CONFLICT DO NOTHING`, key, kind, id, fingerprint, now)
		if err != nil {
			return err
		}
		op.recovering = tag.RowsAffected() == 0
		var storedKind, storedHash, state string
		var storedID ids.ID
		var until *time.Time
		if err := tx.QueryRow(ctx, `SELECT kind,aggregate_id,request_hash,state,result,lease_until FROM owner_external_operations WHERE operation_key=$1 FOR UPDATE`, key).Scan(&storedKind, &storedID, &storedHash, &state, &op.result, &until); err != nil {
			return err
		}
		if storedKind != kind || storedID != id || storedHash != fingerprint {
			return conflict("EXTERNAL_OPERATION_CONTEXT_CHANGED", "The original external operation terms must be retained.")
		}
		if state == "CONFIRMED" {
			claimed = true
			return nil
		}
		if state == "MANUAL_REVIEW" || (until != nil && until.After(now)) {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE owner_external_operations SET lease_token=$2,lease_until=$3,updated_at=$4 WHERE operation_key=$1`, key, op.token, now.Add(2*time.Minute), now)
		if err != nil {
			return err
		}
		claimed = true
		return s.record(ctx, "external.operation.claim", kind, id, map[string]any{"operation_key": key, "recovering": op.recovering}, nil)
	})
	return op, claimed, err
}

func (s *Service) finishExternal(ctx context.Context, op externalOperation, result any, review bool) error {
	var raw []byte
	state := "UNKNOWN"
	if review {
		state = "MANUAL_REVIEW"
	}
	if result != nil {
		var err error
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		state = "CONFIRMED"
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		now := s.now()
		tag, err := mustTx(ctx).Exec(ctx, `UPDATE owner_external_operations SET state=$3,result=$4,lease_token=NULL,lease_until=NULL,updated_at=$5 WHERE operation_key=$1 AND lease_token=$2 AND lease_until>$5`, op.key, op.token, state, raw, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return conflict("EXTERNAL_OPERATION_LEASE_LOST", "Read back the original operation after its current worker finishes.")
		}
		return s.record(ctx, "external.operation."+lower(state), "external_operation", op.id, map[string]any{"operation_key": op.key, "state": state}, nil)
	})
}
