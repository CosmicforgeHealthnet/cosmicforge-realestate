package businesshttp

// Marketplace customer checkout contracts. Marketplace is a demand channel:
// it asks Business for the canonical price, submits the canonical booking
// through its signed connection, and asks Business to collect the accepted
// obligation from the customer's own Payment wallet. Business stays the
// price, inventory and reservation authority; Payment stays the money
// authority. A channel collection never carries a staff identity: it is
// recorded with created_by_service=marketplace-service and is re-validated
// against the live source connection and reservation when the worker runs.

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// MarketplaceQuoteCommand asks for a canonical quote on a published product.
type MarketplaceQuoteCommand struct {
	OrganisationID ids.ID `json:"organisation_id"`
	ConnectionID   ids.ID `json:"connection_id"`
	ProductID      ids.ID `json:"product_id"`
	DateFrom       string `json:"date_from"`
	DateTo         string `json:"date_to"`
	Adults         int    `json:"adults"`
	Children       int    `json:"children"`
	Quantity       int    `json:"quantity"`
}

// MarketplaceCollectionCommand asks Business to collect a Marketplace-sourced
// reservation obligation from the customer's Payment wallet.
type MarketplaceCollectionCommand struct {
	OrganisationID       ids.ID `json:"organisation_id"`
	ConnectionID         ids.ID `json:"connection_id"`
	Version              int64  `json:"version"`
	PaymentPolicyID      ids.ID `json:"payment_policy_id"`
	PaymentPolicyVersion string `json:"payment_policy_version"`
	PaymentPolicyFamily  string `json:"payment_policy_family"`
	PayerWalletID        ids.ID `json:"payer_wallet_id"`
	ConsentReference     string `json:"consent_reference"`
	LegalPartyReference  string `json:"legal_party_reference"`
	Email                string `json:"email"`
	Schedule             string `json:"schedule"`
}

// marketplaceConnection authenticates the signed Marketplace organisation
// delegation and loads its own connection with the required capability.
func (h *Handler) marketplaceConnection(ctx context.Context, org, connection ids.ID, capability string) (CommercialChannelConnection, error) {
	p, ok := serviceauth.PrincipalFrom(ctx)
	if !ok || p.Service != platform.MarketplaceService || org.IsZero() || p.Delegation.OrganizationID != org {
		return CommercialChannelConnection{}, apperrors.PermissionDenied("Signed Marketplace organisation delegation required.")
	}
	c, e := h.loadChannelConnection(ctx, h.Auth.Service.Pool, org, connection)
	if e != nil {
		return c, e
	}
	if c.ProviderCode != distribution.Marketplace {
		return c, apperrors.PermissionDenied("Connection provider does not match Marketplace identity.")
	}
	return c, h.requireChannelCapability(ctx, h.Auth.Service.Pool, c, capability)
}

func (h *Handler) internalMarketplaceQuote(w http.ResponseWriter, r *http.Request) {
	var in MarketplaceQuoteCommand
	if !decode(w, r, &in) {
		return
	}
	c, e := h.marketplaceConnection(r.Context(), in.OrganisationID, in.ConnectionID, "RESERVATION_INGEST")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var out any
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','marketplace-service',true)`); e != nil {
			return e
		}
		t := tenant{Organisation: c.OrganisationID}
		if e := tx.QueryRow(ctx, `SELECT state FROM organisations WHERE id=$1`, c.OrganisationID).Scan(&t.State); e != nil {
			return e
		}
		p, e := inventory.GetProduct(ctx, tx, t.Organisation, in.ProductID)
		if e != nil {
			return e
		}
		if p.PropertyID != c.PropertyID {
			return apperrors.NotFound("PRODUCT_NOT_FOUND", "Product not found for this connection.")
		}
		if e = h.requireConfirmedMappings(ctx, tx, c, p); e != nil {
			return e
		}
		var published bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM listing_publication p JOIN listings l ON l.id=p.listing_id AND l.organisation_id=p.organisation_id WHERE p.organisation_id=$1 AND p.connection_id=$2 AND p.desired_state='PUBLISHED' AND l.product_id=$3)`, c.OrganisationID, c.ID, p.ID).Scan(&published); e != nil {
			return e
		}
		if !published {
			return apperrors.Conflict("LISTING_NOT_PUBLISHED", "The product is not published on this Marketplace connection.")
		}
		if e = h.commercialGate(ctx, tx, t, p.PropertyID); e != nil {
			return e
		}
		quote, e := h.calculateQuote(ctx, tx, t, CommercialQuoteCommand{ProductID: in.ProductID, DateFrom: in.DateFrom, DateTo: in.DateTo, Adults: in.Adults, Children: in.Children, Quantity: in.Quantity, Channel: distribution.Marketplace}, true)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO price_quotes(id,organisation_id,property_id,product_id,rate_version_id,date_from,date_to,amount_minor,currency,expires_at,snapshot,created_by) VALUES($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9,$10,$11,NULL)`, quote.QuoteID, t.Organisation, quote.PropertyID, quote.ProductID, quote.Nights[0].RateVersionID, quote.DateFrom, quote.DateTo, quote.AmountMinor, quote.Currency, quote.ExpiresAt, marshalCommercial(quote)); e != nil {
			return e
		}
		if e = audit(ctx, tx, t, "MARKETPLACE_PRICE_QUOTED", "price_quote", quote.QuoteID, map[string]any{"connection_id": c.ID, "product_id": quote.ProductID, "amount_minor": quote.AmountMinor, "currency": quote.Currency, "expires_at": quote.ExpiresAt}); e != nil {
			return e
		}
		out = quote
		return nil
	})
	reply(w, r, 201, out, e)
}

// marketplaceReservation loads a reservation only if it was accepted from
// this Marketplace connection.
func (h *Handler) marketplaceReservation(ctx context.Context, q database.Querier, c CommercialChannelConnection, id ids.ID) (reservations.Reservation, error) {
	var bound bool
	if e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_source_refs s JOIN channel_event_receipts receipt ON receipt.id=s.receipt_id AND receipt.organisation_id=s.organisation_id WHERE s.organisation_id=$1 AND s.reservation_id=$2 AND receipt.connection_id=$3)`, c.OrganisationID, id, c.ID).Scan(&bound); e != nil {
		return reservations.Reservation{}, e
	}
	if !bound {
		return reservations.Reservation{}, apperrors.NotFound("SOURCE_RESERVATION_NOT_FOUND", "Reservation does not belong to this provider connection.")
	}
	return reservations.Get(ctx, q, c.OrganisationID, id)
}

func (h *Handler) internalMarketplaceCollection(w http.ResponseWriter, r *http.Request) {
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var in MarketplaceCollectionCommand
	if !decode(w, r, &in) {
		return
	}
	address, ae := mail.ParseAddress(in.Email)
	if in.Version < 1 || in.PaymentPolicyID.ValidateV7() != nil || in.PayerWalletID.ValidateV7() != nil || !slices.Contains([]string{"HOTEL", "SHORT_LET"}, in.PaymentPolicyFamily) || len(in.PaymentPolicyVersion) < 1 || len(in.PaymentPolicyVersion) > 100 || len(in.Schedule) < 1 || len(in.Schedule) > 80 || len(in.ConsentReference) < 3 || len(in.ConsentReference) > 300 || !strings.HasPrefix(in.LegalPartyReference, "marketplace:") || len(in.LegalPartyReference) > 300 || ae != nil || address.Address != in.Email || len(in.Email) > 254 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current reservation version, Payment policy, the Marketplace customer's own wallet and explicit consent are required."))
		return
	}
	c, e := h.marketplaceConnection(r.Context(), in.OrganisationID, in.ConnectionID, "RESERVATION_INGEST")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if h.Auth.Service.Sealer == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Protected financial request storage unavailable."))
		return
	}
	bound, e := idempotency.Fingerprint(r.Method, "reservations:marketplace-collection", marshalCommercial(map[string]any{"id": id, "command": in}))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	store := idempotency.NewStore(h.Auth.Service.Pool, 30*time.Second, 7*24*time.Hour)
	result, e := store.Execute(r.Context(), idempotency.Key{Scope: "business:" + in.OrganisationID.String() + ":reservations:marketplace-collection", CallerType: idempotency.CallerService, CallerID: string(platform.MarketplaceService), Key: r.Header.Get("Idempotency-Key")}, bound, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, c.OrganisationID); e != nil {
			return idempotency.Result{}, e
		}
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','marketplace-service',true)`); e != nil {
			return idempotency.Result{}, e
		}
		t := tenant{Organisation: c.OrganisationID}
		if e := tx.QueryRow(ctx, `SELECT state FROM organisations WHERE id=$1`, c.OrganisationID).Scan(&t.State); e != nil {
			return idempotency.Result{}, e
		}
		v, e := h.marketplaceReservation(ctx, tx, c, id)
		if e != nil {
			return idempotency.Result{}, e
		}
		if !slices.Contains(v.PolicySnapshot.AllowedPaymentPolicyIDs, in.PaymentPolicyID) {
			return idempotency.Result{}, apperrors.PermissionDenied("This Payment policy is not authorised by the accepted property contract.")
		}
		if v.Version != in.Version || !slices.Contains([]string{"HOLD", "PENDING_PAYMENT"}, v.State) || v.AllocationState != "ACTIVE" || v.HoldExpiresAt != nil && !h.Auth.Service.Now().Before(*v.HoldExpiresAt) {
			return idempotency.Result{}, apperrors.Conflict("RESERVATION_COLLECTION_STATE", "Only the current active held reservation can request collection.")
		}
		if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
			return idempotency.Result{}, e
		}
		var groupID ids.ID
		e = tx.QueryRow(ctx, `SELECT id FROM reservation_financial_groups WHERE organisation_id=$1 AND reservation_id=$2 AND responsibility='PLATFORM_COLLECT' AND required_amount_minor>0 ORDER BY created_at,id LIMIT 1`, c.OrganisationID, v.ID).Scan(&groupID)
		if errors.Is(e, pgx.ErrNoRows) {
			return idempotency.Result{}, apperrors.Conflict("RESERVATION_COLLECTION_NOT_REQUIRED", "This reservation has no platform-collected obligation.")
		}
		if e != nil {
			return idempotency.Result{}, e
		}
		group, e := h.reservationFinancialGroup(ctx, tx, c.OrganisationID, v.ID, groupID)
		if e != nil {
			return idempotency.Result{}, e
		}
		var existing ids.ID
		if e = tx.QueryRow(ctx, `SELECT id FROM reservation_financial_requests WHERE organisation_id=$1 AND financial_group_id=$2 AND kind='COLLECTION'`, c.OrganisationID, group.ID).Scan(&existing); e == nil {
			return idempotency.Result{}, apperrors.Conflict("RESERVATION_COLLECTION_EXISTS", "An existing collection must be read back; never create another charge to resolve uncertainty.")
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return idempotency.Result{}, e
		}
		if group.State == "VERIFIED" {
			return idempotency.Result{}, apperrors.Conflict("RESERVATION_COLLECTION_NOT_REQUIRED", "This obligation is already collected.")
		}
		requestID := ids.New()
		command := ReservationCollectionCommand{Version: in.Version, FinancialGroupID: group.ID, PaymentPolicyID: in.PaymentPolicyID, PaymentPolicyVersion: in.PaymentPolicyVersion, PaymentPolicyFamily: in.PaymentPolicyFamily, PayerWalletID: in.PayerWalletID, PayerType: "GUEST", ConsentReference: in.ConsentReference, LegalPartyReference: in.LegalPartyReference, Email: in.Email, Schedule: in.Schedule, Reason: "Marketplace customer checkout collection"}
		plain := marshalCommercial(reservationCollectionPayload{Command: command, QuoteID: ids.New()})
		aad := []byte("business:reservation-finance:" + c.OrganisationID.String() + ":" + requestID.String())
		secret := protectedFinancialRequest{KeyID: h.Auth.Service.Sealer.KeyID(), Ciphertext: h.Auth.Service.Sealer.Seal(plain, aad)}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,session_id,kind,request,request_hash,idempotency_key,created_by) VALUES($1,$2,$3,$4,$5,NULL,'COLLECTION',$6,$7,$8,NULL)`, requestID, c.OrganisationID, v.PropertyID, v.ID, group.ID, marshalCommercial(secret), hashCommercialPayload(plain), "reservation-collection:"+requestID.String()); e != nil {
			return idempotency.Result{}, e
		}
		if e = audit(ctx, tx, t, "RESERVATION_COLLECTION_REQUESTED", "reservation", v.ID, map[string]any{"financial_group_id": group.ID, "request_id": requestID, "payment_policy_id": in.PaymentPolicyID, "amount_minor": group.RequiredAmountMinor, "currency": group.Currency, "source_service": "marketplace-service", "connection_id": c.ID}); e != nil {
			return idempotency.Result{}, e
		}
		return idempotency.Result{Status: 202, Body: map[string]any{"data": map[string]any{"id": requestID, "state": "QUEUED", "financial_group_id": group.ID, "amount_minor": group.RequiredAmountMinor, "currency": group.Currency, "payment_authority": "payment-service", "collection_confirmed": false}}}, nil
	})
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if result.Replayed {
		w.Header().Set(idempotency.HeaderReplayed, "true")
	}
	httpx.WriteRaw(w, result.Status, result.Body)
}

// marketplaceCollectionAuthority re-validates a channel collection at run
// time: the source connection is still a capable Marketplace connection and
// the reservation is still the active held obligation it was requested for.
func (h *Handler) marketplaceCollectionAuthority(ctx context.Context, tx pgx.Tx, job reservationFinancialJob, in ReservationCollectionCommand) error {
	var connection ids.ID
	if e := tx.QueryRow(ctx, `SELECT receipt.connection_id FROM reservation_source_refs s JOIN channel_event_receipts receipt ON receipt.id=s.receipt_id AND receipt.organisation_id=s.organisation_id WHERE s.organisation_id=$1 AND s.reservation_id=$2 AND s.source_service='marketplace-service' ORDER BY s.created_at,s.id LIMIT 1`, job.OrganisationID, job.ReservationID).Scan(&connection); e != nil {
		return apperrors.PermissionDenied("The reservation has no Marketplace source connection.")
	}
	c, e := h.loadChannelConnection(ctx, tx, job.OrganisationID, connection)
	if e != nil {
		return e
	}
	if c.ProviderCode != distribution.Marketplace {
		return apperrors.PermissionDenied("The source connection is not Marketplace.")
	}
	if e = h.requireChannelCapability(ctx, tx, c, "RESERVATION_INGEST"); e != nil {
		return e
	}
	if !strings.HasPrefix(in.LegalPartyReference, "marketplace:") || in.PayerType != "GUEST" {
		return apperrors.PermissionDenied("A channel collection is payable only by the Marketplace customer.")
	}
	t := tenant{Organisation: job.OrganisationID}
	if e = tx.QueryRow(ctx, `SELECT state FROM organisations WHERE id=$1`, job.OrganisationID).Scan(&t.State); e != nil {
		return e
	}
	v, e := reservations.Get(ctx, tx, job.OrganisationID, job.ReservationID)
	if e != nil {
		return e
	}
	if !slices.Contains(v.PolicySnapshot.AllowedPaymentPolicyIDs, in.PaymentPolicyID) {
		return apperrors.PermissionDenied("Original accepted property contract does not authorise this financial policy.")
	}
	if v.Version != in.Version || v.AllocationState != "ACTIVE" || !slices.Contains([]string{"HOLD", "PENDING_PAYMENT"}, v.State) || v.HoldExpiresAt != nil && !h.Auth.Service.Now().Before(*v.HoldExpiresAt) {
		return apperrors.Conflict("COLLECTION_AUTHORITY_STALE", "Reservation or hold changed before collection; readback remains available.")
	}
	return h.commercialGate(ctx, tx, t, v.PropertyID)
}

// autoConfirmMarketplaceReservation confirms a Marketplace-sourced held
// reservation once its collection is verified, through exactly the checks of
// the staff confirm command. Policies that require approval, failed gates or
// expired holds leave the reservation for staff (savepoint: no partial
// effects; the payment readback itself is always kept).
func (h *Handler) autoConfirmMarketplaceReservation(ctx context.Context, tx pgx.Tx, job reservationFinancialJob) error {
	v, e := reservations.Get(ctx, tx, job.OrganisationID, job.ReservationID)
	if e != nil {
		return e
	}
	if v.CreatedByService != string(platform.MarketplaceService) || v.PolicySnapshot.RequireApproval || v.AllocationState != "ACTIVE" || !reservations.Transition(v.State, "CONFIRMED") || v.State == "CONFIRMED" {
		return nil
	}
	sp, e := tx.Begin(ctx)
	if e != nil {
		return e
	}
	t := tenant{Organisation: job.OrganisationID}
	if e = sp.QueryRow(ctx, `SELECT state FROM organisations WHERE id=$1`, job.OrganisationID).Scan(&t.State); e != nil {
		_ = sp.Rollback(ctx)
		return e
	}
	in := reservations.Command{Version: v.Version, Reason: "Marketplace instant confirmation after verified Payment collection"}
	event, e := h.confirmReservationTx(ctx, sp, t, v, in)
	if e == nil {
		var next reservations.Reservation
		if next, e = reservations.Get(ctx, sp, t.Organisation, v.ID); e == nil {
			e = h.reservationCommit(ctx, sp, t, next, "CONFIRMED", event, in.Reason, in)
		}
	}
	if e != nil {
		var app *apperrors.Error
		if errors.As(e, &app) && app.Status >= 400 && app.Status < 500 {
			return sp.Rollback(ctx)
		}
		_ = sp.Rollback(ctx)
		return e
	}
	return sp.Commit(ctx)
}

// MarketplaceReservationPayment is the payment view of a Marketplace-sourced
// reservation for the channel's readback.
type MarketplaceReservationPayment struct {
	FinancialGroupID       ids.ID   `json:"financial_group_id"`
	RequiredAmountMinor    int64    `json:"required_amount_minor"`
	Currency               string   `json:"currency"`
	FinancialState         string   `json:"financial_state"`
	AllowedPaymentPolicies []ids.ID `json:"allowed_payment_policy_ids"`
	CollectionRequestID    *ids.ID  `json:"collection_request_id"`
	CollectionState        *string  `json:"collection_state"`
	IntentID               *ids.ID  `json:"intent_id"`
}

func (h *Handler) marketplaceReservationPayment(ctx context.Context, q database.Querier, org ids.ID, v reservations.Reservation) (*MarketplaceReservationPayment, error) {
	out := MarketplaceReservationPayment{AllowedPaymentPolicies: v.PolicySnapshot.AllowedPaymentPolicyIDs}
	e := q.QueryRow(ctx, `SELECT f.id,f.required_amount_minor,f.currency,f.state,j.id,j.state,b.intent_id FROM reservation_financial_groups f LEFT JOIN reservation_financial_requests j ON j.organisation_id=f.organisation_id AND j.financial_group_id=f.id AND j.kind='COLLECTION' LEFT JOIN reservation_payment_bindings b ON b.organisation_id=f.organisation_id AND b.financial_group_id=f.id WHERE f.organisation_id=$1 AND f.reservation_id=$2 AND f.responsibility='PLATFORM_COLLECT' ORDER BY f.created_at,f.id LIMIT 1`, org, v.ID).Scan(&out.FinancialGroupID, &out.RequiredAmountMinor, &out.Currency, &out.FinancialState, &out.CollectionRequestID, &out.CollectionState, &out.IntentID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if out.AllowedPaymentPolicies == nil {
		out.AllowedPaymentPolicies = []ids.ID{}
	}
	return &out, e
}

// MarketplaceGuestRegistration binds an individual reservation's source-typed
// guest (e.g. the Marketplace customer reference) to a canonical guest at
// arrival. It requires the same approved identity link as group rooming: an
// ACTIVE guest_identity_links row backed by the owning source's current
// authenticated assertion.
type MarketplaceGuestRegistration struct {
	Version               int64  `json:"version"`
	SourceKind            string `json:"source_kind"`
	SourceReference       string `json:"source_reference"`
	GuestID               ids.ID `json:"guest_id"`
	RegistrationReference string `json:"registration_reference"`
	Reason                string `json:"reason"`
}

func (h *Handler) registerMarketplaceArrivals(r chi.Router) {
	r.Get("/guest-source-projections", h.guestSourceProjection)
	r.Post("/reservations/{id}/guest-registrations", h.reservationGuestRegistration)
}

// guestSourceProjection returns the current authenticated source assertion
// for a source guest reference (front desk identity linking).
func (h *Handler) guestSourceProjection(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	property, e := ids.ParseV7(q.Get("property_id"))
	kind, reference := q.Get("source_kind"), q.Get("source_reference")
	if e != nil || kind != "MARKETPLACE" && kind != "OTA" || !crm.OpaqueReference(reference) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, external source kind and opaque source reference required."))
		return
	}
	t, e := h.commercialScope(r, "guests:read", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM (SELECT id,connection_id,source_kind,source_service,source_version,state,expires_at,evidence_reference FROM customer_accounts_projection WHERE organisation_id=$1 AND property_id=$2 AND source_kind=$3 AND source_reference_hash=$4 ORDER BY source_version DESC LIMIT 1) x`, t.Organisation, property, kind, h.crmSourceHash(t.Organisation, kind, reference))
	if e == nil && len(out) == 0 {
		e = apperrors.NotFound("SOURCE_PROJECTION_NOT_FOUND", "No authenticated source assertion exists for this reference.")
	}
	var first any
	if e == nil {
		first = out[0]
	}
	reply(w, r, 200, first, e)
}

func (h *Handler) reservationGuestRegistration(w http.ResponseWriter, r *http.Request) {
	var in MarketplaceGuestRegistration
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.GuestID.ValidateV7() != nil || in.SourceKind != "MARKETPLACE" && in.SourceKind != "OTA" || !crm.OpaqueReference(in.SourceReference) || !crm.Text(in.RegistrationReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current reservation version, the booking's source guest, the canonical guest and actual registration evidence are required."))
		return
	}
	_, pre, e := h.reservationTarget(r, "pms:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:guest-registration:"+pre.ID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.AllocationState != "ACTIVE" || v.GroupID != nil || !slices.Contains([]string{"HOLD", "PENDING_PAYMENT", "PENDING_APPROVAL", "CONFIRMED"}, v.State) {
			return 0, nil, apperrors.Conflict("GUEST_REGISTRATION_STATE", "A current active individual reservation before check-in is required (groups use rooming).")
		}
		if e = crm.RequireIdentityLink(ctx, tx, t.Organisation, v.PropertyID, in.GuestID, in.SourceKind, h.crmSourceHash(t.Organisation, in.SourceKind, in.SourceReference), h.Auth.Service.Now()); e != nil {
			return 0, nil, e
		}
		var row ids.ID
		var bound *ids.ID
		e = tx.QueryRow(ctx, `SELECT id,guest_id FROM reservation_guests WHERE organisation_id=$1 AND reservation_id=$2 AND source_kind=$3 AND source_reference=$4 FOR UPDATE`, t.Organisation, v.ID, in.SourceKind, in.SourceReference).Scan(&row, &bound)
		if errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, apperrors.NotFound("RESERVATION_GUEST_NOT_FOUND", "The reservation has no such source guest.")
		}
		if e != nil {
			return 0, nil, e
		}
		if bound != nil && *bound != in.GuestID {
			return 0, nil, apperrors.Conflict("RESERVATION_GUEST_BOUND", "The source guest is already registered to another canonical guest.")
		}
		if bound == nil {
			if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET guest_id=$3,registration_reference=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, row, in.GuestID, in.RegistrationReference); e != nil {
				return 0, nil, e
			}
		}
		if e = audit(ctx, tx, t, "RESERVATION_GUEST_REGISTERED", "reservation", v.ID, map[string]any{"reservation_guest_id": row, "guest_id": in.GuestID, "source_kind": in.SourceKind, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"reservation_id": v.ID, "reservation_guest_id": row, "guest_id": in.GuestID}, nil
	})
}
