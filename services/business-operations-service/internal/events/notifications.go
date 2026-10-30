package events

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	shared "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

const NotificationRequested = "business.notification.requested.v1"

type NotificationJob struct {
	JobID      ids.ID `json:"job_id"`
	KeyID      string `json:"key_id"`
	Ciphertext []byte `json:"ciphertext"`
}

func notificationAAD(id ids.ID) []byte { return []byte("business-notification:" + id.String()) }

// NotificationRequest protects contact details and one-time invitation secrets
// before either the transactional outbox or JetStream stores the event.
func NotificationRequest(ctx context.Context, sealer *credentials.Sealer, id ids.ID, request notification.SendRequest) (shared.Envelope, error) {
	if sealer == nil || id.ValidateV7() != nil {
		return shared.Envelope{}, errors.New("notification protected storage required")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return shared.Envelope{}, err
	}
	job := NotificationJob{JobID: id, KeyID: sealer.KeyID(), Ciphertext: sealer.Seal(raw, notificationAAD(id))}
	return shared.New(ctx, platform.BusinessOperationsService, NotificationRequested, "notification", id, job)
}
func OpenNotification(sealer *credentials.Sealer, event shared.Envelope) (notification.SendRequest, error) {
	var request notification.SendRequest
	var job NotificationJob
	if sealer == nil || event.Validate() != nil || event.Producer != string(platform.BusinessOperationsService) || event.EventType != NotificationRequested || event.AggregateType != "notification" || event.DecodePayload(&job) != nil || job.JobID.ValidateV7() != nil || job.JobID != event.AggregateID {
		return request, errors.New("invalid business notification event")
	}
	raw, err := sealer.Open(job.KeyID, job.Ciphertext, notificationAAD(job.JobID))
	if err != nil {
		return request, err
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return request, err
	}
	if request.Recipient.Domain != string(platform.DomainBusinessOperations) || request.Recipient.ID.ValidateV7() != nil {
		return request, errors.New("invalid business notification recipient")
	}
	return request, nil
}
