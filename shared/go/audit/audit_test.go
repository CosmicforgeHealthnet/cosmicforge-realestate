package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestRecordAndAppendOnly(t *testing.T) {
	db := testkit.NewDatabase(t, nil, SchemaSQL)
	ctx := requestcontext.With(context.Background(), requestcontext.Values{
		RequestID: "req-1", TraceID: "t1", CallingService: "admin-service",
		Actor: requestcontext.Actor{Domain: "admin", ID: ids.New()},
	})
	r := NewRecorder("verification-service")
	target := ids.New()
	id, err := r.Record(ctx, db.Pool, Event{
		Action: "verification.transition", TargetType: "verification", TargetID: target,
		Changes:  map[string]any{"from": "MANUAL_REVIEW", "to": "VERIFIED", "provider_secret": "sk_123"},
		Metadata: map[string]any{"decision_source": "MANUAL_REVIEW", "nested": map[string]any{"api_key": "x", "ok": 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var actorType, callSvc, reqID, traceID, changes, metadata string
	err = db.Pool.QueryRow(context.Background(), `SELECT actor_type, calling_service, request_id, trace_id, changes::text, metadata::text FROM audit_events WHERE audit_id=$1`, id).
		Scan(&actorType, &callSvc, &reqID, &traceID, &changes, &metadata)
	if err != nil {
		t.Fatal(err)
	}
	if actorType != ActorUser || callSvc != "admin-service" || reqID != "req-1" || traceID != "t1" {
		t.Fatalf("context not stamped: %s %s %s %s", actorType, callSvc, reqID, traceID)
	}
	if strings.Contains(changes, "sk_123") || strings.Contains(metadata, "api_key") || !strings.Contains(changes, "VERIFIED") {
		t.Fatalf("sanitization failed: %s %s", changes, metadata)
	}
	if _, err := db.Pool.Exec(context.Background(), `UPDATE audit_events SET result='DENIED'`); err == nil {
		t.Fatal("audit rows must be immutable")
	}
	if _, err := db.Pool.Exec(context.Background(), `DELETE FROM audit_events`); err == nil {
		t.Fatal("audit rows must not be deletable")
	}
	if _, err := r.Record(context.Background(), db.Pool, Event{Action: "Bad Action", TargetType: "x", TargetID: target}); err == nil {
		t.Fatal("invalid action accepted")
	}
	// System actor when no context.
	sid, err := r.Record(context.Background(), db.Pool, Event{Action: "verification.expired", TargetType: "verification", TargetID: target})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Pool.QueryRow(context.Background(), `SELECT actor_type FROM audit_events WHERE audit_id=$1`, sid).Scan(&actorType)
	if actorType != ActorSystem {
		t.Fatalf("expected SYSTEM, got %s", actorType)
	}
}
