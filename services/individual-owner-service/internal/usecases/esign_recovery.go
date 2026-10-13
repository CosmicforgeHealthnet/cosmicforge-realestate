package usecases

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/esign"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func (s *Service) recoverEnvelope(ctx context.Context, id ids.ID, req esign.EnvelopeRequest, key string) (esign.Envelope, error) {
	cctx, cancel := providerContext(ctx, "e-sign create/readback")
	defer cancel()
	op, claimed, err := s.claimExternal(ctx, "ESIGN_ENVELOPE", id, key, req)
	var env esign.Envelope
	if err != nil || !claimed {
		return env, err
	}
	if len(op.result) > 0 {
		err = json.Unmarshal(op.result, &env)
		return env, err
	}
	if op.recovering {
		lookup, ok := s.ESign.(esign.ReadbackAPI)
		if !ok {
			return env, s.finishExternal(ctx, op, nil, true)
		}
		var found bool
		env, found, err = lookup.LookupEnvelope(cctx, req, key)
		providerOutcome(cctx, err)
		if err != nil {
			_ = s.finishExternal(ctx, op, nil, false)
			return env, err
		}
		if found {
			if env.Ref == "" {
				return esign.Envelope{}, s.finishExternal(ctx, op, nil, true)
			}
			return env, s.finishExternal(ctx, op, env, false)
		}
	}
	env, err = s.ESign.CreateEnvelope(cctx, req, key)
	providerOutcome(cctx, err)
	if err != nil {
		_ = s.finishExternal(ctx, op, nil, false)
		return esign.Envelope{}, err
	}
	if env.Ref == "" {
		_ = s.finishExternal(ctx, op, nil, true)
		return env, fmt.Errorf("provider returned no envelope reference")
	}
	return env, s.finishExternal(ctx, op, env, false)
}
