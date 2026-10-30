package businesshttp

import (
	"context"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) queueNotice(ctx context.Context, tx pgx.Tx, account ids.ID, title, message string, securityAlert bool, references ...string) error {
	var email string
	if err := tx.QueryRow(ctx, `SELECT email FROM business_accounts WHERE id=$1`, account).Scan(&email); err != nil {
		return err
	}
	request := notification.SendRequest{Recipient: notification.Recipient{Domain: string(platform.DomainBusinessOperations), ID: account}, TemplateKey: "activity.generic_update", Channels: []string{notification.ChannelEmail}, Variables: map[string]string{"title": title, "message": message}, Destinations: notification.Destinations{Email: email}}
	if securityAlert {
		request.TemplateKey = notification.TemplateSecurityAlert
		request.Variables = map[string]string{"event": message}
	}
	if len(references) > 0 {
		request.CallerReference = references[0]
	}
	event, err := businessEvents.NotificationRequest(ctx, h.Auth.Service.Sealer, ids.New(), request)
	if err != nil {
		return err
	}
	return h.Auth.Service.Outbox.Add(ctx, tx, event)
}
