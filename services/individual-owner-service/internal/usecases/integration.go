package usecases

import (
	"context"
	"errors"
	"slices"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// checkOwnedMedia verifies that a media-document-service file exists, belongs
// to the owner domain and to one of owners (the uploading account or the
// property), has an allowed class and is not rejected or deleted. It is a
// read-only downstream call and must run outside database transactions.
func (s *Service) checkOwnedMedia(ctx context.Context, mediaID ids.ID, owner ids.ID, classes ...string) error {
	_, err := s.ownedMedia(ctx, mediaID, []ids.ID{owner}, classes...)
	return err
}

func (s *Service) ownedMedia(ctx context.Context, mediaID ids.ID, owners []ids.ID, classes ...string) (media.Media, error) {
	if s.Media == nil {
		return media.Media{}, unavailable("Media linking", "media-document-service")
	}
	m, err := s.Media.Get(ctx, mediaID)
	if err != nil {
		var re *httpx.RemoteError
		if errors.As(err, &re) && (re.Status == 404 || re.Status == 403) {
			return media.Media{}, precondition("MEDIA_NOT_FOUND", "The media file does not exist or is not accessible.")
		}
		return media.Media{}, downstream("media-document-service", err)
	}
	if m.OwnerDomain != string(platform.DomainIndividualOwner) || !slices.Contains(owners, m.OwnerID) {
		return media.Media{}, precondition("MEDIA_NOT_OWNED", "The media file was not uploaded for this owner or property.")
	}
	if len(classes) > 0 && !slices.Contains(classes, m.MediaClass) {
		return media.Media{}, precondition("MEDIA_CLASS_NOT_ALLOWED", "The media file's class is not allowed here.")
	}
	if p := media.Projection(m.Status); p == "REJECTED" || p == "DELETED" {
		return media.Media{}, precondition("MEDIA_UNUSABLE", "The media file was rejected or deleted.")
	}
	return m, nil
}

// downstream maps a dependency failure to a retryable 503/502 without
// leaking provider details.
func downstream(service string, err error) error {
	var re *httpx.RemoteError
	if errors.As(err, &re) && re.Status >= 400 && re.Status < 500 && re.Status != 408 && re.Status != 429 {
		return apperrors.New(502, "DOWNSTREAM_REJECTED", service+" rejected the request.").WithCause(err).
			WithDetails(apperrors.Detail{Field: "dependency", Code: re.Code, Message: service})
	}
	return apperrors.New(503, "DEPENDENCY_UNAVAILABLE", service+" is temporarily unavailable; retry with the same Idempotency-Key.").WithCause(err)
}
