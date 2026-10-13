package usecases

import (
	"context"
	"time"
	_ "time/tzdata" // Supply IANA rules in minimal deployment images.

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func validateTimezone(zone string) error {
	if _, err := time.LoadLocation(zone); err != nil {
		return apperrors.InvalidRequest("timezone must be a supported IANA timezone.")
	}
	return nil
}

// businessDate compares SQL dates using the property calendar, not the server.
func (s *Service) businessDate(ctx context.Context, propertyID ids.ID, instant time.Time) (time.Time, error) {
	p, err := s.q.GetProperty(ctx, s.dbq(ctx), propertyID)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.Time{}, precondition("PROPERTY_TIMEZONE_INVALID", "Correct the property's timezone before using its business clock.")
	}
	t := instant.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
}
