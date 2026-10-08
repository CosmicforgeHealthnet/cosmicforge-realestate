package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

func TestNewStampsContextAndValidates(t *testing.T) {
	actor := ids.New()
	ctx := requestcontext.With(context.Background(), requestcontext.Values{
		RequestID: "req-1", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", CallingService: "api-gateway",
		Actor: requestcontext.Actor{Domain: "individual-owner", ID: actor},
	})
	agg := ids.New()
	e, err := New(ctx, platform.VerificationService, "verification.verified.v1", "verification", agg, map[string]string{"status": "VERIFIED"})
	if err != nil {
		t.Fatal(err)
	}
	if e.EventID.ValidateV7() != nil || e.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || e.Actor.ID != actor ||
		e.Metadata["request_id"] != "req-1" || e.OccurredAt.Location().String() != "UTC" {
		t.Fatalf("envelope %+v", e)
	}
	b, err := e.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	_ = json.Unmarshal(b, &generic)
	for _, k := range []string{"event_id", "event_type", "producer", "aggregate_type", "aggregate_id", "occurred_at", "trace_id", "actor", "payload"} {
		if _, ok := generic[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	back, err := Unmarshal(b)
	if err != nil || back.EventID != e.EventID || !back.OccurredAt.Equal(e.OccurredAt) {
		t.Fatalf("round trip %v", err)
	}
	var p struct{ Status string }
	if err := back.DecodePayload(&p); err != nil || p.Status != "VERIFIED" {
		t.Fatal("payload decode")
	}
}

func TestValidationFailures(t *testing.T) {
	ctx := context.Background()
	agg := ids.New()
	for _, et := range []string{"verification.verified", "Verification.verified.v1", "verified.v0", "x.v1", "a..b.v1"} {
		if _, err := New(ctx, platform.VerificationService, et, "verification", agg, map[string]any{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("event type %q accepted", et)
		}
	}
	if _, err := New(ctx, "identity-service", "a.b.v1", "x", agg, map[string]any{}); !errors.Is(err, ErrInvalid) {
		t.Error("unknown producer accepted")
	}
	if _, err := New(ctx, platform.VerificationService, "a.b.v1", "x", ids.Nil, map[string]any{}); !errors.Is(err, ErrInvalid) {
		t.Error("nil aggregate accepted")
	}
	if _, err := New(ctx, platform.VerificationService, "a.b.v1", "x", agg, []int{1}); !errors.Is(err, ErrInvalid) {
		t.Error("non-object payload accepted")
	}
	if _, err := Unmarshal([]byte(`{"event_id":"bad"}`)); !errors.Is(err, ErrInvalid) {
		t.Error("garbage accepted")
	}
}

func TestForwardCompatibleDecoding(t *testing.T) {
	e, _ := New(context.Background(), platform.MediaDocumentService, "media.ready.v1", "media", ids.New(), map[string]any{"media_id": "x", "new_field": 1})
	b, _ := e.Marshal()
	withExtra := strings.Replace(string(b), `"event_id"`, `"future_envelope_field":true,"event_id"`, 1)
	back, err := Unmarshal([]byte(withExtra))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		MediaID string `json:"media_id"`
	}
	if err := back.DecodePayload(&p); err != nil || p.MediaID != "x" {
		t.Fatal("unknown fields must be ignored")
	}
}

func TestHelpers(t *testing.T) {
	if Subject("realestate", "media.ready.v1") != "realestate.media.ready.v1" || Version("media.ready.v12") != 12 {
		t.Fatal("helpers")
	}
	if !IsPermanent(Permanent(errors.New("x"))) || IsPermanent(errors.New("x")) {
		t.Fatal("permanent")
	}
}
