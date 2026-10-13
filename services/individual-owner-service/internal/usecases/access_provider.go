package usecases

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/smartaccess"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func (s *Service) provisionCredential(ctx context.Context, id ids.ID, req smartaccess.ProvisionRequest, allowCreate bool) (string, error) {
	key := "access_grant:" + id.String() + ":provision"
	op, claimed, err := s.claimExternal(ctx, "SMART_ACCESS_PROVISION", id, key, req)
	if err != nil || !claimed {
		return "", err
	}
	var ref string
	if len(op.result) > 0 {
		err = json.Unmarshal(op.result, &ref)
		return ref, err
	}
	if op.recovering {
		lookup, ok := s.SmartAccess.(smartaccess.ReadbackAPI)
		if !ok {
			return "", s.finishExternal(ctx, op, nil, true)
		}
		var found bool
		ref, found, err = lookup.LookupProvision(ctx, req, key)
		if err != nil {
			_ = s.finishExternal(ctx, op, nil, false)
			return "", err
		}
		if found {
			if ref == "" {
				return "", s.finishExternal(ctx, op, nil, true)
			}
			return ref, s.finishExternal(ctx, op, ref, false)
		}
	}
	if !allowCreate {
		return "", s.finishExternal(ctx, op, nil, true)
	}
	ref, err = s.SmartAccess.Provision(ctx, req, key)
	if err != nil {
		_ = s.finishExternal(ctx, op, nil, false)
		return "", err
	}
	if ref == "" {
		_ = s.finishExternal(ctx, op, nil, true)
		return "", fmt.Errorf("provider returned no credential reference")
	}
	return ref, s.finishExternal(ctx, op, ref, false)
}
