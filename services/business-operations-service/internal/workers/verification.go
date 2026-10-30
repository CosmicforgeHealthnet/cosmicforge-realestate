package workers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/organisation"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
	"time"
)

const VerificationConsumer = "business-operations-verification-v1"

type Verification struct {
	Projection organisation.Projection
	API        verification.API
	after      ids.ID
}

func (w *Verification) Refresh(ctx context.Context, org, caseID ids.ID) error {
	if w.API == nil {
		return errors.New("verification provider unavailable")
	}
	ctx = requestcontext.WithActor(ctx, requestcontext.Actor{})
	authoritative, err := w.API.Get(ctx, caseID)
	if err != nil {
		return errors.New("authoritative verification lookup unavailable")
	}
	if authoritative.VerificationID != caseID {
		return errors.New("verification case identity mismatch")
	}
	return w.Projection.Apply(ctx, org, nil, authoritative)
}

// Broker messages are hints, never verification truth. Even a forged envelope
// cannot activate a business: authenticated provider HTTP is always consulted.
func (w *Verification) Handle(ctx context.Context, d events.Delivery) error {
	e := d.Envelope
	if e.Validate() != nil || e.Producer != string(platform.VerificationService) || e.AggregateType != "verification" {
		return events.Permanent(errors.New("invalid verification producer or aggregate"))
	}
	switch e.EventType {
	case "verification.verified.v1", "verification.rejected.v1", "verification.expired.v1", "verification.submitted.v1", "verification.processing.v1", "verification.manual_review_required.v1":
	default:
		return events.Permanent(errors.New("unsupported verification event"))
	}
	var payload struct {
		Case ids.ID `json:"verification_id"`
	}
	if json.Unmarshal(e.Payload, &payload) != nil || payload.Case != e.AggregateID {
		return events.Permanent(errors.New("verification aggregate mismatch"))
	}
	var org ids.ID
	var exists bool
	if err := w.Projection.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM processed_events WHERE consumer=$1 AND event_id=$2)`, VerificationConsumer, e.EventID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	err := w.Projection.Pool.QueryRow(ctx, `SELECT v.organisation_id FROM verification_links v JOIN organisations o ON o.id=v.organisation_id WHERE v.case_id=$1 AND v.purpose='KYB' AND o.state NOT IN ('CLOSED','CLOSURE_PENDING')`, payload.Case).Scan(&org)
	if err != nil { // Events for other domains are irrelevant; reconciliation finds newly linked cases.
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	} else {
		authoritative, err := w.API.Get(requestcontext.WithActor(ctx, requestcontext.Actor{}), payload.Case)
		if err != nil {
			return errors.New("authoritative verification lookup unavailable")
		}
		if authoritative.VerificationID != payload.Case {
			return events.Permanent(errors.New("verification case identity mismatch"))
		}
		return w.Projection.ApplyEvent(ctx, org, authoritative, e)
	}
	_, err = w.Projection.Pool.Exec(ctx, `INSERT INTO processed_events(consumer,event_id,event_type) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, VerificationConsumer, e.EventID, e.EventType)
	return err
}

// Cursor pagination prevents a permanently unavailable case from starving
// other organisations. Expiry is independently enforced on every request.
func (w *Verification) Tick(ctx context.Context) error {
	rows, err := w.Projection.Pool.Query(ctx, `SELECT v.organisation_id,v.case_id FROM verification_links v JOIN organisations o ON o.id=v.organisation_id WHERE v.purpose='KYB' AND o.state NOT IN ('CLOSED','CLOSURE_PENDING') AND ($1::uuid IS NULL OR v.organisation_id>$1) ORDER BY v.organisation_id LIMIT 100`, w.after)
	if err != nil {
		return err
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.org, &r.id); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		w.after = ids.Nil
		return nil
	}
	var failures []error
	for _, r := range refs {
		w.after = r.org
		call, cancel := context.WithTimeout(ctx, 6*time.Second)
		err := w.Refresh(call, r.org, r.id)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.Join(failures...)
}
