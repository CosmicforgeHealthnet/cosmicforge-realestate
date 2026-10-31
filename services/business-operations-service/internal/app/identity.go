package app

import (
	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"time"
)

// EnableIdentity is mandatory for the public API process. Workers/migrations do
// not load account signing keys or expose these public routes.
func (a *App) EnableIdentity(loader *config.Loader) error {
	service, identity, err := businessauth.LoadRuntime(loader, a.Pool, a.Outbox)
	if err != nil {
		return err
	}
	handler, err := businessauth.NewHandler(service, identity.Verifier)
	if err != nil {
		return err
	}
	verificationURL := loader.Required("VERIFICATION_SERVICE_URL")
	notificationURL := loader.Required("NOTIFICATION_SERVICE_URL")
	if err := loader.Err(); err != nil {
		return err
	}
	verify, err := verification.New(verificationURL, identity.Signer, 5*time.Second)
	if err != nil {
		return err
	}
	notify, err := notification.New(notificationURL, identity.Signer, 5*time.Second)
	if err != nil {
		return err
	}
	domain := businesshttp.Register(handler, verify, notify)
	if mediaURL := loader.String("MEDIA_SERVICE_URL", ""); mediaURL != "" {
		client, err := media.New(mediaURL, identity.Signer, 5*time.Second)
		if err != nil {
			return err
		}
		domain.Media = client
	}
	domain.InvitationAcceptURL = loader.String("BUSINESS_INVITATION_ACCEPT_URL", "")
	a.Router = NewRouter(a.Health, handler.Router)
	return nil
}
