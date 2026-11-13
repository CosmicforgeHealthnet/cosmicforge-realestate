package messaging

import (
	"context"
	"time"
)

// ReviewDimensionObservation is one provider dimension rating.
type ReviewDimensionObservation struct {
	Dimension string `json:"dimension"`
	Value     int    `json:"value"`
	Scale     int    `json:"scale"`
}

// ReviewObservation is a provider-permitted review record. Text is supplied
// only where the provider permits storing it.
type ReviewObservation struct {
	ExternalReviewID     string                       `json:"external_review_id"`
	SourceVersion        int64                        `json:"source_version"`
	ReservationReference string                       `json:"reservation_reference,omitempty"`
	RatingValue          int                          `json:"rating_value"`
	RatingScale          int                          `json:"rating_scale"`
	Dimensions           []ReviewDimensionObservation `json:"dimensions,omitempty"`
	Text                 string                       `json:"text,omitempty"`
	TextStoragePermitted bool                         `json:"text_storage_permitted"`
	ContentReference     string                       `json:"content_reference"`
	ReviewedAt           time.Time                    `json:"reviewed_at"`
	ModerationStatus     string                       `json:"moderation_status"`
	Deleted              bool                         `json:"deleted"`
}

// ReviewPage is a poll page with the next durable cursor.
type ReviewPage struct {
	Reviews    []ReviewObservation `json:"reviews"`
	NextCursor string              `json:"next_cursor"`
}

// ReviewAdapter is the provider review boundary. Errors other than
// ErrRejected/ErrUnsupported/ErrNotFound are ambiguous.
type ReviewAdapter interface {
	PollReviews(ctx context.Context, account, cursor string) (ReviewPage, error)
	Reply(ctx context.Context, account, externalReviewID, clientID, body string) (MessagingSendResult, error)
	ReadbackReply(ctx context.Context, account, externalReviewID, clientID string) (MessagingSendResult, error)
	Retract(ctx context.Context, account, externalReviewID, providerResponseID string) error
}

// DisabledReviews fails closed for providers without an approved contract.
type DisabledReviews struct{}

func (DisabledReviews) PollReviews(context.Context, string, string) (ReviewPage, error) {
	return ReviewPage{}, ErrUnsupported
}
func (DisabledReviews) Reply(context.Context, string, string, string, string) (MessagingSendResult, error) {
	return MessagingSendResult{}, ErrUnsupported
}
func (DisabledReviews) ReadbackReply(context.Context, string, string, string) (MessagingSendResult, error) {
	return MessagingSendResult{}, ErrUnsupported
}
func (DisabledReviews) Retract(context.Context, string, string, string) error { return ErrUnsupported }
