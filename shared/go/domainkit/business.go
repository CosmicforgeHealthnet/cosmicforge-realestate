package domainkit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// BusinessPrefix is the Business internal partner API prefix.
const BusinessPrefix = "/internal/v1/business-supply"

// PartnerReference is how Business names an account of this service as a
// record counterparty.
func (rt *Runtime) PartnerReference(account ids.ID) string {
	return string(rt.Cfg.Service) + ":account:" + account.String()
}

// AccountFromReference returns the local account named by a Business
// counterparty reference, if it belongs to this service.
func (rt *Runtime) AccountFromReference(ref string) (ids.ID, bool) {
	raw, ok := strings.CutPrefix(ref, string(rt.Cfg.Service)+":account:")
	if !ok {
		return ids.Nil, false
	}
	id, err := ids.ParseV7(raw)
	return id, err == nil
}

// CallBusiness performs a signed call to Business with the organisation
// delegation; out receives the "data" member.
func (rt *Runtime) CallBusiness(ctx context.Context, org ids.ID, method, path, key string, body, out any) error {
	if rt.Business == nil {
		return apperrors.Unavailable("Business Operations is not configured.")
	}
	// Partner contracts name the acting account in the body; the end-user
	// actor of this domain is not delegated to Business.
	v := requestcontext.From(ctx)
	v.Actor, v.OrganizationID = requestcontext.Actor{}, org
	ctx = requestcontext.With(ctx, v)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := rt.Business.Do(ctx, httpx.Request{Method: method, Path: BusinessPrefix + path, Body: body, IdempotencyKey: key}, &env); err != nil {
		// Business 4xx decisions propagate; outages and unknown outcomes are
		// 503 (retry with the same Idempotency-Key replays safely).
		return httpx.ToAppError(err)
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// BusinessRecord is the Business partner readback projection.
type BusinessRecord struct {
	Domain     string         `json:"domain"`
	Record     BusinessOps    `json:"record"`
	Attributes map[string]any `json:"attributes"`
}

// BusinessOps is the subset of a Business governed record partners use.
type BusinessOps struct {
	ID                    ids.ID     `json:"id"`
	ScopeID               ids.ID     `json:"scope_id"`
	PropertyID            *ids.ID    `json:"property_id"`
	ParentID              *ids.ID    `json:"parent_id"`
	Kind                  string     `json:"kind"`
	State                 string     `json:"state"`
	Title                 string     `json:"title"`
	CounterpartyReference string     `json:"counterparty_reference"`
	ExternalReference     string     `json:"external_reference"`
	AmountMinor           *int64     `json:"amount_minor"`
	Currency              *string    `json:"currency"`
	StartsAt              *time.Time `json:"starts_at"`
	EndsAt                *time.Time `json:"ends_at"`
	Version               int64      `json:"version"`
}

// PartnerTransition reports an allow-listed outcome on a Business record.
func (rt *Runtime) PartnerTransition(ctx context.Context, org ids.ID, domain string, record, account ids.ID, key string, body map[string]any) (BusinessRecord, error) {
	body["account_id"] = account
	var out BusinessRecord
	err := rt.CallBusiness(ctx, org, http.MethodPost, "/partner-records/"+domain+"/"+record.String()+"/transitions", key, body, &out)
	return out, err
}

// BusinessEvent is a Business operational event payload.
type BusinessEvent struct {
	OrganisationID ids.ID         `json:"organisation_id"`
	TargetID       ids.ID         `json:"target_id"`
	PropertyID     *ids.ID        `json:"property_id"`
	Operation      string         `json:"operation"`
	State          string         `json:"state"`
	Version        int64          `json:"version"`
	Snapshot       BusinessDetail `json:"snapshot"`
}

// BusinessDetail is the operational snapshot published by Business.
type BusinessDetail struct {
	Domain                string         `json:"domain"`
	Kind                  string         `json:"kind"`
	ScopeID               ids.ID         `json:"scope_id"`
	ParentID              *ids.ID        `json:"parent_id"`
	SubjectID             *ids.ID        `json:"subject_id"`
	Title                 string         `json:"title"`
	CounterpartyReference string         `json:"counterparty_reference"`
	ExternalReference     string         `json:"external_reference"`
	AmountMinor           *int64         `json:"amount_minor"`
	Currency              *string        `json:"currency"`
	StartsAt              *time.Time     `json:"starts_at"`
	EndsAt                *time.Time     `json:"ends_at"`
	Attributes            map[string]any `json:"attributes"`
}

// BusinessHandler handles one decoded Business event.
type BusinessHandler func(ctx context.Context, tx pgx.Tx, e events.Envelope, b BusinessEvent) error

// ConsumeBusiness subscribes to Business event types with exactly-once
// effects. Business owns its stream; this retries until it exists and never
// creates or alters it.
func (rt *Runtime) ConsumeBusiness(ctx context.Context, conn *jetstream.Conn, durable string, eventTypes []string, h BusinessHandler) {
	subjects := make([]string, 0, len(eventTypes))
	for _, et := range eventTypes {
		subjects = append(subjects, rt.Cfg.SubjectPrefix+".business."+et)
	}
	stream := jetstream.StreamName(rt.Cfg.SubjectPrefix, "business_operations")
	go func() {
		for {
			_, err := rt.Consume(ctx, conn, stream, durable, subjects, func(ctx context.Context, tx pgx.Tx, e events.Envelope) error {
				if e.Producer != string(platform.BusinessOperationsService) {
					return events.Permanent(errors.New("unexpected producer"))
				}
				var b BusinessEvent
				if err := e.DecodePayload(&b); err != nil {
					return events.Permanent(err)
				}
				return h(ctx, tx, e, b)
			})
			if err == nil {
				return
			}
			rt.Log.Warn("business consumer not ready; retrying", "durable", durable, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
}
