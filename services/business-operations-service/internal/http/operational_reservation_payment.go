package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"time"
)

type ReservationCollectionCommand struct {
	Version              int64  `json:"version"`
	FinancialGroupID     ids.ID `json:"financial_group_id"`
	PaymentPolicyID      ids.ID `json:"payment_policy_id"`
	PaymentPolicyVersion string `json:"payment_policy_version"`
	PaymentPolicyFamily  string `json:"payment_policy_family"`
	PayerWalletID        ids.ID `json:"payer_wallet_id"`
	PayerType            string `json:"payer_type"`
	ConsentReference     string `json:"consent_reference"`
	LegalPartyReference  string `json:"legal_party_reference"`
	Email                string `json:"email"`
	Schedule             string `json:"schedule"`
	Reason               string `json:"reason"`
}
type reservationFinancialGroup struct {
	ID                  ids.ID `json:"id"`
	ReservationID       ids.ID `json:"reservation_id"`
	AmountMinor         int64  `json:"amount_minor"`
	RequiredAmountMinor int64  `json:"required_amount_minor"`
	Currency            string `json:"currency"`
	PolicyReference     string `json:"policy_reference"`
	AgreementVersion    int64  `json:"agreement_version"`
	State               string `json:"state"`
	Responsibility      string `json:"responsibility"`
}
type reservationFinancialJob struct {
	ID                ids.ID          `json:"id"`
	OrganisationID    ids.ID          `json:"organisation_id"`
	PropertyID        ids.ID          `json:"property_id"`
	ReservationID     ids.ID          `json:"reservation_id"`
	FinancialGroupID  ids.ID          `json:"financial_group_id"`
	SessionID         *ids.ID         `json:"session_id"`
	CreatedBy         ids.ID          `json:"created_by"`
	Kind              string          `json:"kind"`
	State             string          `json:"state"`
	Request           json.RawMessage `json:"request"`
	RequestHash       string          `json:"request_hash"`
	IdempotencyKey    string          `json:"idempotency_key"`
	ProviderReference string          `json:"provider_reference"`
	LeaseOwner        string          `json:"lease_owner"`
	LeaseGeneration   int64           `json:"lease_generation"`
	Attempts          int             `json:"attempts"`
}
type protectedFinancialRequest struct {
	KeyID      string `json:"key_id"`
	Ciphertext []byte `json:"ciphertext"`
}
type reservationCollectionPayload struct {
	Command ReservationCollectionCommand `json:"command"`
	QuoteID ids.ID                       `json:"quote_id"`
}

func (h *Handler) registerReservationPayments(r chi.Router) {
	r.Post("/reservations/{id}/payment-requests", h.reservationCollectionRequest)
	r.Get("/reservations/{id}/payment-requests", h.reservationPaymentRequests)
	r.Post("/reservations/{id}/payment-readback", h.reservationPaymentReadback)
	r.Post("/reservations/{id}/payment-requests/{request_id}/retry", h.reservationFinancialRetry)
}
func (h *Handler) reservationCollectionRequest(w http.ResponseWriter, r *http.Request) {
	var in ReservationCollectionCommand
	if !decode(w, r, &in) {
		return
	}
	address, e := mail.ParseAddress(in.Email)
	if in.Version < 1 || in.FinancialGroupID.ValidateV7() != nil || in.PaymentPolicyID.ValidateV7() != nil || in.PayerWalletID.ValidateV7() != nil || !slices.Contains([]string{"HOTEL", "SHORT_LET"}, in.PaymentPolicyFamily) || !slices.Contains([]string{"GUEST", "CORPORATE_BOOKER"}, in.PayerType) || len(in.PaymentPolicyVersion) < 1 || len(in.PaymentPolicyVersion) > 100 || len(in.Schedule) < 1 || len(in.Schedule) > 80 || len(in.ConsentReference) < 1 || len(in.ConsentReference) > 300 || len(in.LegalPartyReference) < 1 || len(in.LegalPartyReference) > 300 || e != nil || address.Address != in.Email || len(in.Email) > 254 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current financial group, Payment policy, payer and explicit consent references required."))
		return
	}
	_, pre, e := h.reservationTarget(r, "reservations:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:collection:"+pre.ID.String(), in, "reservations:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if !slices.Contains(v.PolicySnapshot.AllowedPaymentPolicyIDs, in.PaymentPolicyID) {
			return 0, nil, apperrors.PermissionDenied("This Payment policy is not authorised by the accepted independently approved property contract.")
		}
		if v.Version != in.Version || !slices.Contains([]string{"HOLD", "PENDING_PAYMENT", "PENDING_APPROVAL", "CONFIRMED"}, v.State) || v.AllocationState != "ACTIVE" {
			return 0, nil, apperrors.Conflict("RESERVATION_COLLECTION_STATE", "Only the current active accepted obligation can request collection.")
		}
		if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
			return 0, nil, e
		}
		group, e := h.reservationFinancialGroup(ctx, tx, t.Organisation, v.ID, in.FinancialGroupID)
		if e != nil {
			return 0, nil, e
		}
		if group.Responsibility != "PLATFORM_COLLECT" || group.State == "VERIFIED" || group.RequiredAmountMinor <= 0 {
			return 0, nil, apperrors.Conflict("RESERVATION_COLLECTION_NOT_REQUIRED", "This obligation does not require a new platform collection.")
		}
		var existing bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_financial_requests WHERE organisation_id=$1 AND financial_group_id=$2 AND kind='COLLECTION')`, t.Organisation, group.ID).Scan(&existing); e != nil {
			return 0, nil, e
		}
		if existing {
			return 0, nil, apperrors.Conflict("RESERVATION_COLLECTION_EXISTS", "An existing collection must be read back; never create another charge to resolve uncertainty.")
		}
		if h.Auth.Service.Sealer == nil {
			return 0, nil, apperrors.Unavailable("Protected financial request storage unavailable.")
		}
		id := ids.New()
		plain := marshalCommercial(reservationCollectionPayload{Command: in, QuoteID: ids.New()})
		aad := []byte("business:reservation-finance:" + t.Organisation.String() + ":" + id.String())
		secret := protectedFinancialRequest{KeyID: h.Auth.Service.Sealer.KeyID(), Ciphertext: h.Auth.Service.Sealer.Seal(plain, aad)}
		p, _ := businessauth.PrincipalFrom(ctx)
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,session_id,kind,request,request_hash,idempotency_key,created_by) VALUES($1,$2,$3,$4,$5,$6,'COLLECTION',$7,$8,$9,$10)`, id, t.Organisation, v.PropertyID, v.ID, group.ID, p.SessionID, marshalCommercial(secret), hashCommercialPayload(plain), "reservation-collection:"+id.String(), t.Account); e != nil {
			return 0, nil, e
		}
		if e = audit(ctx, tx, t, "RESERVATION_COLLECTION_REQUESTED", "reservation", v.ID, map[string]any{"financial_group_id": group.ID, "request_id": id, "payment_policy_id": in.PaymentPolicyID, "amount_minor": group.RequiredAmountMinor, "currency": group.Currency}); e != nil {
			return 0, nil, e
		}
		return 202, map[string]any{"id": id, "state": "QUEUED", "financial_group_id": group.ID, "payment_authority": "payment-service", "collection_confirmed": false}, nil
	})
}
func (h *Handler) reservationFinancialGroup(ctx context.Context, q database.Querier, org, reservation, id ids.ID) (reservationFinancialGroup, error) {
	return reservations.Load[reservationFinancialGroup](ctx, q, `WITH RECURSIVE accessible(id) AS (SELECT financial_group_id FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$3 AND state='ACTIVE' UNION SELECT c.source_group_id FROM accessible a JOIN reservation_financial_credits c ON c.organisation_id=$1 AND c.target_group_id=a.id) SELECT to_jsonb(f) FROM reservation_financial_groups f WHERE f.organisation_id=$1 AND f.id=$2 AND f.id IN (SELECT id FROM accessible)`, org, id, reservation)
}
func (h *Handler) reservationPaymentRequests(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.reservationTarget(r, "reservations:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(j)-'request'-'request_hash'-'session_id'-'lease_owner'-'lease_expires_at' FROM reservation_financial_requests j WHERE organisation_id=$1 AND reservation_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, v.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": out, "limit": limit, "offset": offset}, e)
}
func (h *Handler) reservationPaymentReadback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version          int64  `json:"version"`
		FinancialGroupID ids.ID `json:"financial_group_id"`
		Reason           string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.FinancialGroupID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current reservation/group and reason required."))
		return
	}
	_, pre, e := h.reservationTarget(r, "reservations:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:payment-readback:"+pre.ID.String(), in, "reservations:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Reservation changed.")
		}
		group, e := h.reservationFinancialGroup(ctx, tx, t.Organisation, v.ID, in.FinancialGroupID)
		if e != nil {
			return 0, nil, e
		}
		var intent ids.ID
		if e = tx.QueryRow(ctx, `SELECT intent_id FROM reservation_payment_bindings WHERE organisation_id=$1 AND financial_group_id=$2`, t.Organisation, group.ID).Scan(&intent); errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, apperrors.Conflict("PAYMENT_BINDING_REQUIRED", "The original collection request must establish its Payment-owned binding first.")
		}
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		p, _ := businessauth.PrincipalFrom(ctx)
		raw := marshalCommercial(map[string]any{"intent_id": intent})
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,session_id,kind,request,request_hash,idempotency_key,provider_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,'READBACK',$7,$8,$9,$10,$11)`, id, t.Organisation, v.PropertyID, v.ID, group.ID, p.SessionID, raw, hashCommercialPayload(raw), "reservation-readback:"+id.String(), intent.String(), t.Account); e != nil {
			return 0, nil, e
		}
		return 202, map[string]any{"id": id, "state": "QUEUED", "payment_authority": "payment-service"}, nil
	})
}
func (h *Handler) ProcessReservationFinance(ctx context.Context, limit int) error {
	if limit < 1 || limit > 20 {
		limit = 20
	}
	owner := ids.New().String()
	for range limit {
		job, e := typedJSON[reservationFinancialJob](ctx, h.Auth.Service.Pool, `UPDATE reservation_financial_requests j SET state='RUNNING',lease_owner=$1,lease_generation=lease_generation+1,lease_expires_at=now()+interval '45 seconds',attempts=attempts+1,version=version+1,updated_at=now() WHERE id=(SELECT id FROM reservation_financial_requests WHERE kind IN ('COLLECTION','READBACK') AND available_at<=now() AND (state IN ('QUEUED','RETRY','PENDING') OR state='RUNNING' AND lease_expires_at<now()) ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING to_jsonb(j)`, owner)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		observation, callErr := h.runReservationFinancialJob(ctx, job)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = h.finishReservationFinancialJob(ctx, job, observation, callErr); e != nil {
			return e
		}
	}
	return nil
}

type reservationFinancialObservation struct {
	Intent  payment.Intent
	Quote   payment.Quote
	Receipt payment.Receipt
}

func (h *Handler) runReservationFinancialJob(ctx context.Context, job reservationFinancialJob) (reservationFinancialObservation, error) {
	var out reservationFinancialObservation
	if h.Payment == nil {
		return out, apperrors.Unavailable("Payment Service configuration is unavailable.")
	}
	call, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	group, e := typedJSON[reservationFinancialGroup](call, h.Auth.Service.Pool, `SELECT to_jsonb(f) FROM reservation_financial_groups f WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, job.FinancialGroupID)
	if e != nil {
		return out, e
	}
	if job.ProviderReference != "" || job.Kind == "READBACK" {
		var quoteID ids.ID
		if e = h.Auth.Service.Pool.QueryRow(call, `SELECT locked_quote_id FROM reservation_payment_bindings WHERE organisation_id=$1 AND financial_group_id=$2`, job.OrganisationID, group.ID).Scan(&quoteID); e != nil {
			return out, e
		}
		return h.readReservationFinancialObservation(call, quoteID, group)
	}
	if h.Auth.Service.Sealer == nil {
		return out, apperrors.PermissionDenied("Protected financial request unavailable.")
	}
	var secret protectedFinancialRequest
	if e = json.Unmarshal(job.Request, &secret); e != nil {
		return out, e
	}
	plain, e := h.Auth.Service.Sealer.Open(secret.KeyID, secret.Ciphertext, []byte("business:reservation-finance:"+job.OrganisationID.String()+":"+job.ID.String()))
	if e != nil {
		return out, e
	}
	if hashCommercialPayload(plain) != job.RequestHash {
		return out, apperrors.InvalidRequest("Financial request integrity failed.")
	}
	var payload reservationCollectionPayload
	if e = json.Unmarshal(plain, &payload); e != nil {
		return out, e
	}
	in := payload.Command
	// A prior call may have committed before this worker crashed. Recover the
	// exact locked-quote receipt before considering any new collection command,
	// even if the original session/hold has since expired.
	if job.Attempts > 1 {
		receipt, readErr := h.Payment.Receipt(call, payload.QuoteID)
		if readErr == nil && receipt.IntentID != nil {
			return h.readReservationFinancialObservation(call, payload.QuoteID, group)
		}
		var remote *httpx.RemoteError
		if readErr != nil && !(errors.As(readErr, &remote) && remote.Status == 404) {
			return out, readErr
		}
	}
	if job.SessionID == nil {
		return out, apperrors.PermissionDenied("Original collection session unavailable.")
	}
	callCtx := businessauth.StoredJobContext(call, job.CreatedBy, job.OrganisationID, *job.SessionID, job.ID)
	callCtx = requestcontext.WithRequestID(callCtx, "reservation-finance:"+job.ID.String())
	e = database.WithTx(callCtx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, job.OrganisationID); e != nil {
			return e
		}
		if e := h.reservationFinancialFence(ctx, tx, job); e != nil {
			return e
		}
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", job.PropertyID, false)
		if e != nil {
			return e
		}
		var active bool
		if e = tx.QueryRow(ctx, `SELECT status='ACTIVE' FROM business_accounts WHERE id=$1`, job.CreatedBy).Scan(&active); e != nil {
			return e
		}
		if !active {
			return apperrors.PermissionDenied("Collection account no longer active.")
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, job.ReservationID)
		if e != nil {
			return e
		}
		if !slices.Contains(v.PolicySnapshot.AllowedPaymentPolicyIDs, in.PaymentPolicyID) {
			return apperrors.PermissionDenied("Original accepted property contract does not authorise this financial policy.")
		}
		if v.Version != in.Version || v.AllocationState != "ACTIVE" || !slices.Contains([]string{"HOLD", "PENDING_PAYMENT", "PENDING_APPROVAL", "CONFIRMED"}, v.State) || v.HoldExpiresAt != nil && !h.Auth.Service.Now().Before(*v.HoldExpiresAt) {
			return apperrors.Conflict("COLLECTION_AUTHORITY_STALE", "Reservation or hold changed before collection; readback remains available.")
		}
		return h.commercialGate(ctx, tx, t, v.PropertyID)
	})
	if e != nil {
		return out, e
	}
	policy, e := h.Payment.Policy(callCtx, in.PaymentPolicyID)
	if e != nil {
		return out, e
	}
	now := h.Auth.Service.Now().UTC()
	if policy.ID != in.PaymentPolicyID || policy.Version != in.PaymentPolicyVersion || policy.Key != in.PaymentPolicyFamily || !policy.Active || now.Before(policy.EffectiveFrom) || policy.EffectiveUntil != nil && !now.Before(*policy.EffectiveUntil) || policy.Currency != group.Currency || !slices.Contains(policy.Schedules, in.Schedule) {
		return out, apperrors.Conflict("PAYMENT_POLICY_MISMATCH", "Current authoritative Payment policy does not match the accepted obligation.")
	}
	kind := "ROOM"
	if policy.Key == "SHORT_LET" {
		kind = "ACCOMMODATION"
	}
	quote := payment.Quote{ID: payload.QuoteID, Domain: string(platform.DomainBusinessOperations), BusinessRef: group.ID.String(), PayerWalletID: in.PayerWalletID, PayerType: in.PayerType, PayerConsentRef: in.ConsentReference, LegalPartyRef: in.LegalPartyReference, QuoteVersion: group.ID.String() + ":" + strconv.FormatInt(group.AgreementVersion, 10), AgreementRef: group.ReservationID.String(), AgreementVersion: strconv.FormatInt(group.AgreementVersion, 10), PolicyID: policy.ID, Currency: group.Currency, Jurisdiction: policy.Jurisdiction, Schedule: in.Schedule, AmountMinor: group.RequiredAmountMinor, Components: []payment.Component{{Ref: group.ID.String(), Kind: kind, AmountMinor: group.RequiredAmountMinor, PayerRef: in.LegalPartyReference, Recurrence: "ONE_TIME", ServiceEvidenceRef: "business-reservation:" + group.ReservationID.String() + ":" + strconv.FormatInt(group.AgreementVersion, 10)}}}
	locked, e := h.Payment.Lock(callCtx, quote, job.IdempotencyKey+":quote")
	if e != nil {
		return out, e
	}
	if hashCommercialPayload(marshalCommercial(locked)) != hashCommercialPayload(marshalCommercial(quote)) {
		return out, apperrors.Conflict("PAYMENT_LOCKED_QUOTE_MISMATCH", "Payment returned a different canonical financial obligation.")
	}
	// The existing receipt proves whether a collection already exists for this
	// exact quote. An unavailable readback never authorises a duplicate charge.
	receipt, e := h.Payment.Receipt(callCtx, quote.ID)
	if e != nil {
		return out, e
	}
	if receipt.RemedyVersion != 1 {
		return out, apperrors.Conflict("PAYMENT_REMEDY_READBACK_REQUIRED", "Payment must expose the versioned current remedy restriction contract before collecting this obligation.")
	}
	if hashCommercialPayload(marshalCommercial(receipt.Quote)) != hashCommercialPayload(marshalCommercial(quote)) {
		return out, apperrors.Conflict("PAYMENT_RECEIPT_MISMATCH", "Payment receipt has a different accepted contract.")
	}
	if receipt.IntentID == nil {
		_, e = h.Payment.Collect(callCtx, payment.IntentInput{LockedQuoteID: &quote.ID, WalletID: quote.PayerWalletID, AmountMinor: quote.AmountMinor, Currency: quote.Currency, Country: policy.Jurisdiction, Category: policy.Key, EvidenceRef: quote.QuoteVersion, PolicyVersion: policy.Version, Email: in.Email, RequiresCustody: policy.RequiresCustody}, job.IdempotencyKey+":intent")
		// Even a transport error can follow a committed collection. Read the exact
		// quote again; its Payment-owned receipt, not the POST result, binds it.
		if e != nil {
			recovered, readErr := h.readReservationFinancialObservation(callCtx, quote.ID, group)
			if readErr == nil {
				return recovered, nil
			}
			return out, e
		}
	}
	return h.readReservationFinancialObservation(callCtx, quote.ID, group)
}
func (h *Handler) readReservationFinancialObservation(ctx context.Context, quoteID ids.ID, group reservationFinancialGroup) (reservationFinancialObservation, error) {
	var out reservationFinancialObservation
	receipt, e := h.Payment.Receipt(ctx, quoteID)
	if e != nil {
		return out, e
	}
	quote := receipt.Quote
	if quote.ID != quoteID || quote.Domain != string(platform.DomainBusinessOperations) || quote.BusinessRef != group.ID.String() || quote.AgreementRef != group.ReservationID.String() || quote.AgreementVersion != strconv.FormatInt(group.AgreementVersion, 10) || quote.AmountMinor != group.RequiredAmountMinor || quote.Currency != group.Currency || quote.PayerWalletID != receipt.PayerContext.WalletID || quote.LegalPartyRef != receipt.PayerContext.CanonicalPartyRef || receipt.IntentID == nil || receipt.RemedyVersion != 1 || receipt.ReversedMinor < 0 || receipt.ReversedMinor > quote.AmountMinor {
		return out, apperrors.Conflict("PAYMENT_RECEIPT_BINDING_MISMATCH", "Payment receipt does not match this accepted reservation obligation and payer.")
	}
	intent, e := h.Payment.Intent(ctx, *receipt.IntentID)
	if e != nil {
		return out, e
	}
	if intent.ID != *receipt.IntentID || intent.WalletID != quote.PayerWalletID || intent.AmountMinor != quote.AmountMinor || intent.Currency != quote.Currency || intent.State != receipt.CollectionState || intent.SettlementState != receipt.SettlementState {
		return out, apperrors.Conflict("PAYMENT_READBACK_BINDING_MISMATCH", "Payment collection changed during readback; retry the coherent authoritative snapshot.")
	}
	return reservationFinancialObservation{Intent: intent, Quote: quote, Receipt: receipt}, nil
}
func (h *Handler) reservationFinancialFence(ctx context.Context, tx pgx.Tx, job reservationFinancialJob) error {
	var live bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_financial_requests WHERE id=$1 AND state='RUNNING' AND lease_owner=$2 AND lease_generation=$3 AND lease_expires_at>now())`, job.ID, job.LeaseOwner, job.LeaseGeneration).Scan(&live)
	if e != nil {
		return e
	}
	if !live {
		return apperrors.Conflict("FINANCIAL_LEASE_LOST", "Another worker owns financial reconciliation.")
	}
	return nil
}
func (h *Handler) finishReservationFinancialJob(ctx context.Context, job reservationFinancialJob, observation reservationFinancialObservation, cause error) error {
	intent, quote := observation.Intent, observation.Quote
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, job.OrganisationID); e != nil {
			return e
		}
		if e := h.reservationFinancialFence(ctx, tx, job); e != nil {
			return e
		}
		if cause != nil {
			state, code := "RETRY", "PAYMENT_UNAVAILABLE_OR_UNKNOWN"
			var remote *httpx.RemoteError
			var app *apperrors.Error
			if errors.As(cause, &remote) && remote.Status >= 400 && remote.Status < 500 && remote.Status != 429 || errors.As(cause, &app) && app.Status >= 400 && app.Status < 500 {
				state = "DEPENDENCY_BLOCKED"
				code = "PAYMENT_CONTRACT_OR_AUTHORITY_REJECTED"
			}
			if job.Attempts >= 12 {
				state = "QUARANTINED"
			}
			_, e := tx.Exec(ctx, `UPDATE reservation_financial_requests SET state=$2,last_error=$3,lease_owner=NULL,lease_expires_at=NULL,available_at=now()+($4::int*interval '1 second'),version=version+1,updated_at=now() WHERE id=$1`, job.ID, state, code, min(1800, 30*job.Attempts))
			return e
		}
		group, e := typedJSON[reservationFinancialGroup](ctx, tx, `SELECT to_jsonb(f) FROM reservation_financial_groups f WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, job.FinancialGroupID)
		if e != nil {
			return e
		}
		if job.Kind == "COLLECTION" && !quote.ID.IsZero() {
			if _, e = tx.Exec(ctx, `INSERT INTO reservation_payment_bindings(id,organisation_id,property_id,financial_group_id,intent_id,locked_quote_id,payer_wallet_id,source_service,request_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,'payment-service',$8,$9) ON CONFLICT(financial_group_id) DO NOTHING`, ids.New(), job.OrganisationID, job.PropertyID, group.ID, intent.ID, quote.ID, quote.PayerWalletID, job.RequestHash, job.CreatedBy); e != nil {
				return e
			}
		}
		var expectedIntent, wallet ids.ID
		if e = tx.QueryRow(ctx, `SELECT intent_id,payer_wallet_id FROM reservation_payment_bindings WHERE organisation_id=$1 AND financial_group_id=$2`, job.OrganisationID, group.ID).Scan(&expectedIntent, &wallet); e != nil {
			return e
		}
		if intent.ID != expectedIntent || intent.WalletID != wallet || intent.AmountMinor != group.RequiredAmountMinor || intent.Currency != group.Currency {
			return apperrors.Conflict("PAYMENT_READBACK_BINDING_MISMATCH", "Authoritative response does not match the original collection binding.")
		}
		financial := "PENDING"
		switch intent.State {
		case "VERIFIED_SUCCESS":
			financial = "VERIFIED"
		case "VERIFIED_FAILURE":
			financial = "FAILED"
		case "UNKNOWN_OUTCOME", "RECONCILIATION_EXCEPTION":
			financial = "UNKNOWN"
		}
		if observation.Receipt.ReversedMinor > 0 || observation.Receipt.RemedyReviewRequired {
			financial = "DISPUTED"
		}
		now := h.Auth.Service.Now().UTC()
		hash := hashCommercialPayload(marshalCommercial(observation))
		ref := job.ID.String() + ":" + strconv.Itoa(job.Attempts)
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_payment_projections(id,organisation_id,property_id,financial_group_id,intent_id,provider_state,settlement_state,amount_minor,currency,source_reference,payload_hash,observed_at,expires_at,source_service,created_by,reversed_minor,funding_confirmed,remedy_version,remedy_review_required) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'payment-service',$14,$15,$16,$17,$18)`, ids.New(), job.OrganisationID, job.PropertyID, group.ID, intent.ID, intent.State, intent.SettlementState, intent.AmountMinor, intent.Currency, ref, hash, now, now.Add(5*time.Minute), job.CreatedBy, observation.Receipt.ReversedMinor, observation.Receipt.FundingConfirmed, observation.Receipt.RemedyVersion, observation.Receipt.RemedyReviewRequired); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE reservation_financial_groups SET state=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, group.ID, financial, now); e != nil {
			return e
		}
		if e = h.projectReservationFinancialRoots(ctx, tx, job, observation, financial, now); e != nil {
			return e
		}
		if e = h.reconcileReservationRefundReviews(ctx, tx, job); e != nil {
			return e
		}
		state := "SUCCEEDED"
		if financial == "PENDING" || financial == "UNKNOWN" {
			state = "PENDING"
		}
		_, e = tx.Exec(ctx, `UPDATE reservation_financial_requests SET state=$2,provider_reference=$3,last_error=NULL,lease_owner=NULL,lease_expires_at=NULL,available_at=now()+interval '30 seconds',version=version+1,updated_at=now() WHERE id=$1`, job.ID, state, intent.ID.String())
		return e
	})
}

type ReservationFinancialRetry struct {
	Version        int64  `json:"version"`
	RequestVersion int64  `json:"request_version"`
	Reason         string `json:"reason"`
}

func (h *Handler) reservationFinancialRetry(w http.ResponseWriter, r *http.Request) {
	var in ReservationFinancialRetry
	if !decode(w, r, &in) {
		return
	}
	requestID, e := ids.ParseV7(chi.URLParam(r, "request_id"))
	if e != nil || in.Version < 1 || in.RequestVersion < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current reservation/request versions and recovery reason required."))
		return
	}
	_, pre, e := h.reservationTarget(r, "reservations:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:financial-retry:"+requestID.String(), in, "reservations:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Reservation changed before financial recovery.")
		}
		var version int64
		var state, kind string
		var lease *time.Time
		if e = tx.QueryRow(ctx, `SELECT version,state,kind,lease_expires_at FROM reservation_financial_requests WHERE organisation_id=$1 AND reservation_id=$2 AND id=$3 FOR UPDATE`, t.Organisation, v.ID, requestID).Scan(&version, &state, &kind, &lease); errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, apperrors.NotFound("FINANCIAL_REQUEST_NOT_FOUND", "Financial request does not belong to this reservation.")
		}
		if e != nil {
			return 0, nil, e
		}
		if version != in.RequestVersion || kind != "COLLECTION" && kind != "READBACK" || !slices.Contains([]string{"RETRY", "PENDING", "DEPENDENCY_BLOCKED", "QUARANTINED", "RUNNING"}, state) || state == "RUNNING" && (lease == nil || h.Auth.Service.Now().Before(*lease)) {
			return 0, nil, apperrors.Conflict("FINANCIAL_RECOVERY_STATE", "Only a current recoverable collection/readback or expired lease can be retried; refund execution is owned by its authorised financial service.")
		}
		// Keep the original encrypted command, actor/session, provider quote, key,
		// attempt count and generation. The worker first reads the original receipt;
		// current initiating authority is still required if no collection exists.
		if _, e = tx.Exec(ctx, `UPDATE reservation_financial_requests SET state='RETRY',available_at=now(),lease_owner=NULL,lease_expires_at=NULL,last_error=NULL,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, requestID); e != nil {
			return 0, nil, e
		}
		if e = audit(ctx, tx, t, "RESERVATION_FINANCIAL_RECOVERY_REQUESTED", "reservation", v.ID, map[string]any{"request_id": requestID, "prior_state": state, "request_version": version + 1, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 202, map[string]any{"id": requestID, "state": "RETRY", "version": version + 1, "collection_confirmed": false}, nil
	})
}
