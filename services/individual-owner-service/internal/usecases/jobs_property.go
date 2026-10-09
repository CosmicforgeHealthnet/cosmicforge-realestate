package usecases

import (
	"context"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// ReconcileVerifications reads back verification cases whose projection has
// not changed for a while (lost events, crashes between case creation and
// commit). Errors on one case never block the others.
func (s *Service) ReconcileVerifications(ctx context.Context, batch int32) error {
	if s.Verification == nil {
		return nil
	}
	stale, err := s.q.StaleOpenVerificationLinks(ctx, s.Pool, db.StaleOpenVerificationLinksParams{LastSyncedAt: s.now().Add(-15 * time.Minute), Limit: batch})
	if err != nil {
		return err
	}
	for _, l := range stale {
		if err := s.SyncVerification(ctx, l.VerificationID); err != nil {
			s.Logger.WarnContext(ctx, "verification reconciliation failed", "verification_id", l.VerificationID.String(), "code", apperrors.CodeOf(err))
		}
	}
	return nil
}

// ExpireOwnershipAuthority is the verification-expiry fallback (§11): a
// VERIFIED claim past expires_at requires re-verification even when the
// verification.expired event was not (yet) delivered.
func (s *Service) ExpireOwnershipAuthority(ctx context.Context, batch int32) error {
	due, err := s.q.DueOwnershipExpiries(ctx, s.Pool, db.DueOwnershipExpiriesParams{ExpiresAt: ptr(s.now()), Limit: batch})
	if err != nil {
		return err
	}
	for _, d := range due {
		err := s.InTx(ctx, func(ctx context.Context) error {
			if _, _, err := s.transition(ctx, domain.Ownership, d.ID, lifecycle.Change{To: "REVERIFICATION_REQUIRED", Command: "verification_expired",
				Reason: "VERIFICATION_EXPIRED"}); err != nil {
				return err
			}
			if err := s.authorityLost(ctx, d.PropertyID, "REVERIFICATION_REQUIRED"); err != nil {
				return err
			}
			if err := s.propertyHistory(ctx, d.PropertyID, "REVERIFICATION_REQUIRED", "Ownership verification expired", AggOwnership, d.ID); err != nil {
				return err
			}
			return s.emitVerificationChanged(ctx, d.PropertyID)
		})
		if err != nil && apperrors.CodeOf(err) != "VERSION_CONFLICT" && apperrors.CodeOf(err) != apperrors.CodeInvalidStateTransition {
			return err
		}
	}
	return nil
}

// ExpireConsentsAndMandates expires pending consents and time-limited
// representative mandates (authority ends at valid_until even before this
// runs: the authz engine checks the window on every request).
func (s *Service) ExpireConsentsAndMandates(ctx context.Context) error {
	ctx = requestcontext.Job(ctx, "owner-expiry")
	return s.InTx(ctx, func(ctx context.Context) error {
		tx, now := mustTx(ctx), s.now()
		if _, err := s.q.ExpireOwnershipConsents(ctx, tx, &now); err != nil {
			return err
		}
		ended, err := s.q.ExpireRepresentativeParties(ctx, tx, now)
		if err != nil {
			return err
		}
		for _, p := range ended {
			if err := s.propertyHistory(ctx, p.PropertyID, "MANDATE_EXPIRED", "Representative mandate expired", "ownership_party", p.ID); err != nil {
				return err
			}
		}
		return nil
	})
}
