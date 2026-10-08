package validation

import (
	"errors"
	"testing"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func TestValidator(t *testing.T) {
	var v Validator
	v.Required("name", " ")
	v.MaxLen("bio", "abcdef", 3)
	v.OneOf("channel", "FAX", "PUSH", "EMAIL", "SMS")
	v.Range("limit", 500, 1, 100)
	v.UUIDv7("subject_id", "f47ac10b-58cc-4372-a567-0e02b2c3d479")
	v.Email("email", "not-an-email")
	v.PhoneE164("phone", "08012345678")
	v.Slug("template_key", "Bad Key", 64)
	v.ID("media_id", ids.Nil)
	err := v.Err()
	var e *apperrors.Error
	if !errors.As(err, &e) || e.Status != 400 || e.Code != apperrors.CodeInvalidRequest {
		t.Fatalf("got %v", err)
	}
	if len(e.Details) != 9 {
		t.Fatalf("expected 9 details, got %d: %+v", len(e.Details), e.Details)
	}

	var ok Validator
	id := ok.UUIDv7("id", ids.New().String())
	ok.Email("email", "user@example.com")
	ok.PhoneE164("phone", "+2348012345678")
	ok.Slug("k", "verification.verified", 64)
	ok.OneOf("c", "PUSH", "PUSH")
	if ok.Err() != nil || id.IsZero() {
		t.Fatalf("unexpected %v", ok.Details())
	}
}
