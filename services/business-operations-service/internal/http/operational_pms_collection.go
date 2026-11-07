package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// PMSCollectionAgreementCommand assigns part of a folio's accepted ancillary
// obligations to one payer and queues its Payment Service collection. It never
// re-prices, never collects accommodation again and never asserts payment.
type PMSCollectionAgreementCommand struct {
	FolioVersion         int64                              `json:"folio_version"`
	PayerType            string                             `json:"payer_type"`
	CorporateCustomerID  *ids.ID                            `json:"corporate_customer_id,omitempty"`
	CorporateContractID  *ids.ID                            `json:"corporate_contract_id,omitempty"`
	Allocations          []hospitality.FolioCollectionClaim `json:"allocations"`
	PaymentPolicyID      ids.ID                             `json:"payment_policy_id"`
	PaymentPolicyVersion string                             `json:"payment_policy_version"`
	PaymentPolicyFamily  string                             `json:"payment_policy_family"`
	PayerWalletID        ids.ID                             `json:"payer_wallet_id"`
	ConsentReference     string                             `json:"consent_reference"`
	LegalPartyReference  string                             `json:"legal_party_reference"`
	Email                string                             `json:"email"`
	Schedule             string                             `json:"schedule"`
	ReplacesAgreementID  *ids.ID                            `json:"replaces_agreement_id,omitempty"`
	ApprovalID           ids.ID                             `json:"approval_id,omitempty"`
	Reason               string                             `json:"reason"`
}

// PMSCollectionReleaseCommand releases an allocation only when no money can be
// in flight: never dispatched, or authoritatively failed at Payment Service.
type PMSCollectionReleaseCommand struct {
	FolioVersion int64  `json:"folio_version"`
	Disposition  string `json:"disposition"`
	Reason       string `json:"reason"`
}

// PMSCollectionReadbackCommand queues a read of the original Payment receipt.
type PMSCollectionReadbackCommand struct {
	FolioVersion int64  `json:"folio_version"`
	Reason       string `json:"reason"`
}

type PMSCollectionAgreement struct {
	ID                  ids.ID          `json:"id"`
	FolioID             ids.ID          `json:"folio_id"`
	ReservationID       ids.ID          `json:"reservation_id"`
	FinancialGroupID    ids.ID          `json:"financial_group_id"`
	PayerType           string          `json:"payer_type"`
	CorporateCustomerID *ids.ID         `json:"corporate_customer_id"`
	CorporateContractID *ids.ID         `json:"corporate_contract_id"`
	AmountMinor         int64           `json:"amount_minor"`
	Currency            string          `json:"currency"`
	FolioVersion        int64           `json:"folio_version"`
	ReplacesAgreementID *ids.ID         `json:"replaces_agreement_id"`
	ApprovalID          *ids.ID         `json:"approval_id"`
	Allocations         json.RawMessage `json:"allocations"`
	Released            bool            `json:"released"`
	Disposition         *string         `json:"disposition"`
	FinancialState      string          `json:"financial_state"`
	ProviderState       *string         `json:"provider_state"`
	SettlementState     *string         `json:"settlement_state"`
	ReversedMinor       int64           `json:"reversed_minor"`
	AppliedMinor        int64           `json:"applied_minor"`
	OutstandingMinor    int64           `json:"outstanding_minor"`
	RequestState        *string         `json:"request_state"`
	CollectionState     string          `json:"collection_state"`
}

// PMSFolioResponsibility states the conservation identity over the folio:
// total = accommodation + guest + corporate + unallocated ancillary.
type PMSFolioResponsibility struct {
	Currency                 string `json:"currency"`
	FolioTotalMinor          int64  `json:"folio_total_minor"`
	AccommodationMinor       int64  `json:"accommodation_minor"`
	AncillaryNetMinor        int64  `json:"ancillary_net_minor"`
	AllocatedGuestMinor      int64  `json:"allocated_guest_minor"`
	AllocatedCorporateMinor  int64  `json:"allocated_corporate_minor"`
	UnallocatedMinor         int64  `json:"unallocated_minor"`
	OverAllocatedMinor       int64  `json:"over_allocated_minor"`
	AppliedReceiptsMinor     int64  `json:"applied_receipts_minor"`
	UnappliedReceiptsMinor   int64  `json:"unapplied_receipts_minor"`
	BalanceMinor             int64  `json:"balance_minor"`
	Conserved                bool   `json:"conserved"`
	MandatoryPayerUnverified bool   `json:"mandatory_payer_unverified"`
}

func (h *Handler) registerPMSCollection(r chi.Router) {
	r.Get("/folios/{id}/collection-agreements", h.pmsCollectionAgreements)
	r.Post("/folios/{id}/collection-agreements", h.pmsCollectionAgreementCreate)
	r.Post("/folios/{id}/collection-agreements/{agreement_id}/release", h.pmsCollectionAgreementRelease)
	r.Post("/folios/{id}/collection-agreements/{agreement_id}/readback", h.pmsCollectionAgreementReadback)
}

func validPMSCollectionAgreement(in PMSCollectionAgreementCommand) bool {
	address, e := mail.ParseAddress(in.Email)
	corporate := in.PayerType == "CORPORATE_BOOKER"
	return in.FolioVersion >= 1 && slices.Contains([]string{"GUEST", "CORPORATE_BOOKER"}, in.PayerType) &&
		corporate == (in.CorporateCustomerID != nil && in.CorporateContractID != nil) &&
		(in.CorporateCustomerID == nil || in.CorporateCustomerID.ValidateV7() == nil) && (in.CorporateContractID == nil || in.CorporateContractID.ValidateV7() == nil) &&
		(in.ReplacesAgreementID == nil || in.ReplacesAgreementID.ValidateV7() == nil) &&
		len(in.Allocations) >= 1 && len(in.Allocations) <= 50 &&
		in.PaymentPolicyID.ValidateV7() == nil && in.PayerWalletID.ValidateV7() == nil && slices.Contains([]string{"HOTEL", "SHORT_LET"}, in.PaymentPolicyFamily) &&
		len(in.PaymentPolicyVersion) >= 1 && len(in.PaymentPolicyVersion) <= 100 && len(in.Schedule) >= 1 && len(in.Schedule) <= 80 &&
		crm.Text(in.ConsentReference, 1, 300) && crm.Text(in.LegalPartyReference, 1, 300) &&
		e == nil && address.Address == in.Email && len(in.Email) <= 254 && validStaffReason(in.Reason) &&
		(corporate || in.ApprovalID.IsZero())
}

func (h *Handler) pmsCollectionFolio(r *http.Request) (tenant, hospitality.Folio, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, hospitality.Folio{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, hospitality.Folio{}, e
	}
	f, e := hospitality.GetFolio(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	return t, f, e
}

func (h *Handler) pmsCollectionAgreementCreate(w http.ResponseWriter, r *http.Request) {
	var in PMSCollectionAgreementCommand
	if !decode(w, r, &in) {
		return
	}
	if !validPMSCollectionAgreement(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current folio version, payer, one to fifty ancillary allocations, Payment policy, explicit consent and payer references required; only corporate payers consume an approval."))
		return
	}
	_, pre, e := h.pmsCollectionFolio(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	corporate := in.PayerType == "CORPORATE_BOOKER"
	h.execute(w, r, "pms:folio:collection:"+pre.ID.String(), in, "folios:write", "PROPERTY", pre.PropertyID, corporate, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		return h.executePMSCollectionAgreement(ctx, tx, pre, in)
	})
}

func (h *Handler) executePMSCollectionAgreement(ctx context.Context, tx pgx.Tx, pre hospitality.Folio, in PMSCollectionAgreementCommand) (int, any, error) {
	t, target, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", pre.PropertyID, in.PayerType == "CORPORATE_BOOKER")
	if e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM folios WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, pre.ID); e != nil {
		return 0, nil, e
	}
	f, e := hospitality.GetFolio(ctx, tx, t.Organisation, pre.ID)
	if e != nil {
		return 0, nil, e
	}
	if f.Version != in.FolioVersion || f.State != "OPEN" && f.State != "SETTLEMENT_PENDING" {
		return 0, nil, apperrors.Conflict("FOLIO_COLLECTION_STATE", "Current open or settlement-pending folio version required.")
	}
	v, e := reservations.Get(ctx, tx, t.Organisation, f.ReservationID)
	if e != nil {
		return 0, nil, e
	}
	if !slices.Contains([]string{"CHECKED_IN", "CHECKED_OUT", "COMPLETED"}, v.State) {
		return 0, nil, apperrors.Conflict("FOLIO_COLLECTION_RESERVATION_STATE", "Ancillary collection follows an actual stay.")
	}
	if !slices.Contains(v.PolicySnapshot.AllowedPaymentPolicyIDs, in.PaymentPolicyID) {
		return 0, nil, apperrors.PermissionDenied("This Payment policy is not authorised by the accepted independently approved property contract.")
	}
	if e = h.commercialGate(ctx, tx, t, f.PropertyID); e != nil {
		return 0, nil, e
	}
	sources, e := pmsCollectionSources(ctx, tx, t.Organisation, f.ID)
	if e != nil {
		return 0, nil, e
	}
	plan, e := hospitality.PlanFolioCollection(sources, in.Allocations, f.Currency, in.PaymentPolicyFamily)
	if e != nil {
		return 0, nil, e
	}
	var corporateSnapshot map[string]any
	if in.PayerType == "CORPORATE_BOOKER" {
		if corporateSnapshot, e = pmsCorporatePayer(ctx, tx, t.Organisation, f, *in.CorporateCustomerID, *in.CorporateContractID, h.Auth.Service.Now()); e != nil {
			return 0, nil, e
		}
		op := in
		op.ApprovalID = ids.Nil
		amount := plan.AmountMinor
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_FOLIO_COLLECTION", target.Scope, in.ApprovalID, map[string]any{"folio_id": f.ID, "command": op}, "", &amount, f.Currency); e != nil {
			return 0, nil, e
		}
	}
	if in.ReplacesAgreementID != nil {
		var released bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folio_collection_agreements a JOIN folio_collection_cancellations c ON c.organisation_id=a.organisation_id AND c.agreement_id=a.id WHERE a.organisation_id=$1 AND a.folio_id=$2 AND a.id=$3) AND NOT EXISTS(SELECT 1 FROM folio_collection_agreements WHERE organisation_id=$1 AND replaces_agreement_id=$3)`, t.Organisation, f.ID, *in.ReplacesAgreementID).Scan(&released); e != nil {
			return 0, nil, e
		}
		if !released {
			return 0, nil, apperrors.Conflict("FOLIO_COLLECTION_REPLACEMENT_INVALID", "A replacement names one released agreement on this folio, once.")
		}
	}
	if h.Auth.Service.Sealer == nil {
		return 0, nil, apperrors.Unavailable("Protected financial request storage unavailable.")
	}
	now := h.Auth.Service.Now().UTC()
	agreement, group, request := ids.New(), ids.New(), ids.New()
	sourceDigest := hashCommercialPayload(marshalCommercial(plan))
	op := in
	op.ApprovalID = ids.Nil
	commandDigest := hashCommercialPayload(marshalCommercial(op))
	if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_groups(id,organisation_id,property_id,reservation_id,amount_minor,required_amount_minor,currency,policy_reference,agreement_version,responsibility,created_by) VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,'PLATFORM_COLLECT',$9)`, group, t.Organisation, f.PropertyID, f.ReservationID, plan.AmountMinor, f.Currency, "folio-collection-agreement:"+agreement.String(), f.Version, t.Account); e != nil {
		return 0, nil, e
	}
	var approval any
	if !in.ApprovalID.IsZero() {
		approval = in.ApprovalID
	}
	snapshot := map[string]any{"plan": plan, "corporate": corporateSnapshot, "reservation_version": v.Version}
	policy := map[string]any{"payment_policy_id": in.PaymentPolicyID, "payment_policy_version": in.PaymentPolicyVersion, "payment_policy_family": in.PaymentPolicyFamily, "schedule": in.Schedule}
	if _, e = tx.Exec(ctx, `INSERT INTO folio_collection_agreements(id,organisation_id,property_id,folio_id,reservation_id,financial_group_id,request_reference,folio_version,amount_minor,currency,payer_type,corporate_customer_id,corporate_contract_id,consent_reference,effective_at,replaces_agreement_id,source_digest,command_digest,snapshot,accepted_policy,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		agreement, t.Organisation, f.PropertyID, f.ID, f.ReservationID, group, request, f.Version, plan.AmountMinor, f.Currency, in.PayerType, in.CorporateCustomerID, in.CorporateContractID, in.ConsentReference, now, in.ReplacesAgreementID, sourceDigest, commandDigest, marshalCommercial(snapshot), marshalCommercial(policy), approval, in.Reason, t.Account); e != nil {
		return 0, nil, e
	}
	for _, a := range plan.Allocations {
		if _, e = tx.Exec(ctx, `INSERT INTO folio_collection_allocations(id,organisation_id,property_id,folio_id,agreement_id,folio_item_id,amount_minor,currency,source_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), t.Organisation, f.PropertyID, f.ID, agreement, a.Source.ItemID, a.AmountMinor, f.Currency, marshalCommercial(a)); e != nil {
			return 0, nil, e
		}
	}
	collection := ReservationCollectionCommand{Version: f.Version, FinancialGroupID: group, PaymentPolicyID: in.PaymentPolicyID, PaymentPolicyVersion: in.PaymentPolicyVersion, PaymentPolicyFamily: in.PaymentPolicyFamily, PayerWalletID: in.PayerWalletID, PayerType: in.PayerType, ConsentReference: in.ConsentReference, LegalPartyReference: in.LegalPartyReference, Email: in.Email, Schedule: in.Schedule, Reason: in.Reason}
	plain := marshalCommercial(reservationCollectionPayload{Command: collection, QuoteID: ids.New()})
	aad := []byte("business:reservation-finance:" + t.Organisation.String() + ":" + request.String())
	secret := protectedFinancialRequest{KeyID: h.Auth.Service.Sealer.KeyID(), Ciphertext: h.Auth.Service.Sealer.Seal(plain, aad)}
	p, _ := businessauth.PrincipalFrom(ctx)
	if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,session_id,kind,request,request_hash,idempotency_key,created_by,folio_agreement_id) VALUES($1,$2,$3,$4,$5,$6,'COLLECTION',$7,$8,$9,$10,$11)`, request, t.Organisation, f.PropertyID, f.ReservationID, group, p.SessionID, marshalCommercial(secret), hashCommercialPayload(plain), "folio-collection:"+request.String(), t.Account, agreement); e != nil {
		return 0, nil, e
	}
	change := map[string]any{"agreement_id": agreement, "financial_group_id": group, "request_id": request, "payer_type": in.PayerType, "amount_minor": plan.AmountMinor, "currency": f.Currency, "source_digest": sourceDigest, "replaces_agreement_id": in.ReplacesAgreementID, "reason": in.Reason}
	if e = h.pmsCommit(ctx, tx, t, f.PropertyID, agreement, "folio_collection_agreement", "FOLIO_COLLECTION_REQUESTED", "hospitality.folio_collection.updated.v1", 1, change); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE folios SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, f.ID); e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, f.PropertyID, f.ID, "folio", "FOLIO_COLLECTION_AGREED", "hospitality.folio.updated.v1", f.Version+1, change); e != nil {
		return 0, nil, e
	}
	return 202, map[string]any{"id": agreement, "financial_group_id": group, "request_id": request, "amount_minor": plan.AmountMinor, "currency": f.Currency, "folio_version": f.Version + 1, "state": "QUEUED", "payment_authority": "payment-service", "collection_confirmed": false}, nil
}

// pmsCollectionSources lists actual ancillary items with their remaining net
// obligation and existing non-released payer allocations.
func pmsCollectionSources(ctx context.Context, tx pgx.Tx, org, folio ids.ID) ([]hospitality.FolioCollectionSource, error) {
	rows, e := commercialJSONRows(ctx, tx, `SELECT jsonb_build_object('item_id',i.id,'kind',i.kind,'currency',i.currency,'source_reference',i.source_reference,'policy_reference',coalesce(i.policy_snapshot->>'policy_reference',''),'original_minor',i.amount_minor,'net_minor',i.amount_minor+coalesce((SELECT sum(c.amount_minor) FROM folio_items c WHERE c.organisation_id=i.organisation_id AND c.adjusts_item_id=i.id),0),'allocated_minor',coalesce((SELECT sum(a.amount_minor) FROM folio_collection_allocations a WHERE a.organisation_id=i.organisation_id AND a.folio_item_id=i.id AND NOT EXISTS(SELECT 1 FROM folio_collection_cancellations x WHERE x.organisation_id=a.organisation_id AND x.agreement_id=a.agreement_id)),0)) FROM folio_items i WHERE i.organisation_id=$1 AND i.folio_id=$2 AND i.kind NOT IN ('ACCOMMODATION','ADJUSTMENT') ORDER BY i.created_at,i.id LIMIT 501`, org, folio)
	if e != nil {
		return nil, e
	}
	if len(rows) > 500 {
		return nil, apperrors.Conflict("FOLIO_COLLECTION_REVIEW_REQUIRED", "Folio exceeds the bounded collection source batch.")
	}
	out := make([]hospitality.FolioCollectionSource, 0, len(rows))
	for _, raw := range rows {
		var s hospitality.FolioCollectionSource
		if e = json.Unmarshal(raw, &s); e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, nil
}

// pmsCorporatePayer requires a live corporate profile, a current contract in
// this property and an active traveler association for the stay's guest.
func pmsCorporatePayer(ctx context.Context, tx pgx.Tx, org ids.ID, f hospitality.Folio, corporate, contract ids.ID, now time.Time) (map[string]any, error) {
	var ok bool
	var definition json.RawMessage
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM corporate_customer_profiles p JOIN corporate_customer_contracts k ON k.organisation_id=p.organisation_id AND k.corporate_id=p.id JOIN stay_records s ON s.organisation_id=k.organisation_id AND s.id=$5 JOIN guest_company_associations g ON g.organisation_id=s.organisation_id AND g.guest_id=s.guest_id AND g.corporate_id=p.id AND g.contract_id=k.id AND g.property_id=k.property_id WHERE p.organisation_id=$1 AND p.id=$2 AND k.id=$3 AND k.property_id=$4 AND p.state='ACTIVE' AND k.state='ACTIVE' AND k.effective_at<=$6 AND k.expires_at>$6 AND k.version=(SELECT max(version) FROM corporate_customer_contracts latest WHERE latest.contract_key=k.contract_key) AND g.state='ACTIVE' AND g.effective_at<=$6 AND g.expires_at>$6),coalesce((SELECT definition FROM corporate_customer_contracts WHERE organisation_id=$1 AND id=$3),'{}'::jsonb)`, org, corporate, contract, f.PropertyID, f.StayID, now).Scan(&ok, &definition)
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, apperrors.PermissionDenied("Corporate billing requires an active profile, current property contract and current traveler association for this stay's guest.")
	}
	return map[string]any{"corporate_customer_id": corporate, "corporate_contract_id": contract, "contract_digest": hashCommercialPayload(definition)}, nil
}

func pmsCollectionAgreementID(r *http.Request) (ids.ID, error) {
	id, e := ids.ParseV7(chi.URLParam(r, "agreement_id"))
	if e != nil {
		return ids.Nil, apperrors.InvalidRequest("A UUIDv7 agreement identifier is required.")
	}
	return id, nil
}

func (h *Handler) pmsCollectionAgreementRelease(w http.ResponseWriter, r *http.Request) {
	var in PMSCollectionReleaseCommand
	if !decode(w, r, &in) {
		return
	}
	agreement, e := pmsCollectionAgreementID(r)
	if e != nil || in.FolioVersion < 1 || !slices.Contains([]string{"NEVER_DISPATCHED", "VERIFIED_FAILURE"}, in.Disposition) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current folio version, release disposition and reason required."))
		return
	}
	_, pre, e := h.pmsCollectionFolio(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:folio:collection-release:"+agreement.String(), in, "folios:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `SELECT id FROM folios WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, pre.ID); e != nil {
			return 0, nil, e
		}
		f, e := hospitality.GetFolio(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if f.Version != in.FolioVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Folio changed.")
		}
		var request ids.ID
		e = tx.QueryRow(ctx, `SELECT r.id FROM folio_collection_agreements a JOIN reservation_financial_requests r ON r.organisation_id=a.organisation_id AND r.folio_agreement_id=a.id AND r.kind='COLLECTION' WHERE a.organisation_id=$1 AND a.folio_id=$2 AND a.id=$3 FOR UPDATE OF r`, t.Organisation, f.ID, agreement).Scan(&request)
		if errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, apperrors.NotFound("FOLIO_COLLECTION_NOT_FOUND", "Agreement does not belong to this folio.")
		}
		if e != nil {
			return 0, nil, e
		}
		cancellation := ids.New()
		if _, e = tx.Exec(ctx, `SAVEPOINT folio_collection_release`); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO folio_collection_cancellations(id,organisation_id,property_id,agreement_id,disposition,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, cancellation, t.Organisation, f.PropertyID, agreement, in.Disposition, in.Reason, t.Account); e != nil {
			if _, re := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT folio_collection_release`); re != nil {
				return 0, nil, re
			}
			return 0, nil, apperrors.Conflict("FOLIO_COLLECTION_RELEASE_BLOCKED", "Only a never-dispatched or authoritatively failed collection, released once, can return its allocation; reconcile uncertain money first.")
		}
		if in.Disposition == "NEVER_DISPATCHED" {
			if _, e = tx.Exec(ctx, `UPDATE reservation_financial_requests SET state='DEPENDENCY_BLOCKED',last_error='FOLIO_AGREEMENT_RELEASED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, request); e != nil {
				return 0, nil, e
			}
		}
		change := map[string]any{"agreement_id": agreement, "disposition": in.Disposition, "reason": in.Reason}
		if e = h.pmsCommit(ctx, tx, t, f.PropertyID, cancellation, "folio_collection_cancellation", "FOLIO_COLLECTION_RELEASED", "hospitality.folio_collection.updated.v1", 1, change); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE folios SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, f.ID); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, f.PropertyID, f.ID, "folio", "FOLIO_COLLECTION_RELEASED", "hospitality.folio.updated.v1", f.Version+1, change); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"id": agreement, "released": true, "disposition": in.Disposition, "folio_version": f.Version + 1}, nil
	})
}

func (h *Handler) pmsCollectionAgreementReadback(w http.ResponseWriter, r *http.Request) {
	var in PMSCollectionReadbackCommand
	if !decode(w, r, &in) {
		return
	}
	agreement, e := pmsCollectionAgreementID(r)
	if e != nil || in.FolioVersion < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current folio version and reason required."))
		return
	}
	_, pre, e := h.pmsCollectionFolio(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:folio:collection-readback:"+agreement.String(), in, "folios:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		f, e := hospitality.GetFolio(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if f.Version != in.FolioVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Folio changed.")
		}
		var group, intent ids.ID
		e = tx.QueryRow(ctx, `SELECT a.financial_group_id,b.intent_id FROM folio_collection_agreements a JOIN reservation_payment_bindings b ON b.organisation_id=a.organisation_id AND b.financial_group_id=a.financial_group_id WHERE a.organisation_id=$1 AND a.folio_id=$2 AND a.id=$3`, t.Organisation, f.ID, agreement).Scan(&group, &intent)
		if errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, apperrors.Conflict("PAYMENT_BINDING_REQUIRED", "The original collection request must establish its Payment-owned binding first.")
		}
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		p, _ := businessauth.PrincipalFrom(ctx)
		raw := marshalCommercial(map[string]any{"intent_id": intent})
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,session_id,kind,request,request_hash,idempotency_key,provider_reference,created_by,folio_agreement_id) VALUES($1,$2,$3,$4,$5,$6,'READBACK',$7,$8,$9,$10,$11,$12)`, id, t.Organisation, f.PropertyID, f.ReservationID, group, p.SessionID, raw, hashCommercialPayload(raw), "folio-readback:"+id.String(), intent.String(), t.Account, agreement); e != nil {
			return 0, nil, e
		}
		return 202, map[string]any{"id": id, "state": "QUEUED", "payment_authority": "payment-service"}, nil
	})
}

func (h *Handler) pmsCollectionAgreements(w http.ResponseWriter, r *http.Request) {
	t, f, e := h.pmsCollectionFolio(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "folios:read", "PROPERTY", f.PropertyID, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	now := h.Auth.Service.Now()
	items, e := pmsCollectionAgreementViews(r.Context(), h.Auth.Service.Pool, t.Organisation, f.ID, now)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	resp, e := pmsFolioResponsibility(r.Context(), h.Auth.Service.Pool, t.Organisation, f, items, now)
	reply(w, r, 200, map[string]any{"folio_id": f.ID, "items": items, "responsibility": resp, "payment_truth": "PAYMENT_SERVICE"}, e)
}

func pmsCollectionAgreementViews(ctx context.Context, q database.Querier, org, folio ids.ID, now time.Time) ([]PMSCollectionAgreement, error) {
	rows, e := commercialJSONRows(ctx, q, `SELECT jsonb_build_object('id',a.id,'folio_id',a.folio_id,'reservation_id',a.reservation_id,'financial_group_id',a.financial_group_id,'payer_type',a.payer_type,'corporate_customer_id',a.corporate_customer_id,'corporate_contract_id',a.corporate_contract_id,'amount_minor',a.amount_minor,'currency',a.currency,'folio_version',a.folio_version,'replaces_agreement_id',a.replaces_agreement_id,'approval_id',a.approval_id,
		'allocations',coalesce((SELECT jsonb_agg(jsonb_build_object('item_id',l.folio_item_id,'amount_minor',l.amount_minor) ORDER BY l.folio_item_id) FROM folio_collection_allocations l WHERE l.organisation_id=a.organisation_id AND l.agreement_id=a.id),'[]'::jsonb),
		'released',c.id IS NOT NULL,'disposition',c.disposition,'financial_state',g.state,'provider_state',p.provider_state,'settlement_state',p.settlement_state,'reversed_minor',coalesce(p.reversed_minor,0),
		'applied_minor',coalesce((SELECT sum(x.amount_minor) FROM folio_payment_references x WHERE x.organisation_id=a.organisation_id AND x.folio_id=a.folio_id AND x.financial_group_id=a.financial_group_id AND p.provider_state='VERIFIED_SUCCESS' AND p.settlement_state='SETTLED' AND p.funding_confirmed AND p.reversed_minor=0 AND NOT p.remedy_review_required AND p.expires_at>$3),0),
		'request_state',(SELECT r.state FROM reservation_financial_requests r WHERE r.organisation_id=a.organisation_id AND r.folio_agreement_id=a.id AND r.kind='COLLECTION' LIMIT 1))
		FROM folio_collection_agreements a JOIN reservation_financial_groups g ON g.organisation_id=a.organisation_id AND g.id=a.financial_group_id LEFT JOIN folio_collection_cancellations c ON c.organisation_id=a.organisation_id AND c.agreement_id=a.id LEFT JOIN LATERAL(SELECT * FROM reservation_payment_projections p WHERE p.organisation_id=a.organisation_id AND p.financial_group_id=a.financial_group_id ORDER BY p.observed_at DESC,p.id DESC LIMIT 1) p ON true
		WHERE a.organisation_id=$1 AND a.folio_id=$2 ORDER BY a.created_at,a.id LIMIT 201`, org, folio, now)
	if e != nil {
		return nil, e
	}
	if len(rows) > 200 {
		return nil, apperrors.Conflict("FOLIO_COLLECTION_REVIEW_REQUIRED", "Folio exceeds the bounded agreement batch.")
	}
	out := make([]PMSCollectionAgreement, 0, len(rows))
	for _, raw := range rows {
		var a PMSCollectionAgreement
		if e = json.Unmarshal(raw, &a); e != nil {
			return nil, e
		}
		a.CollectionState = pmsCollectionState(a)
		if !a.Released {
			a.OutstandingMinor = a.AmountMinor - a.AppliedMinor
		}
		out = append(out, a)
	}
	return out, nil
}

// pmsCollectionState never reports more than the latest Payment readback.
func pmsCollectionState(a PMSCollectionAgreement) string {
	switch {
	case a.Released:
		return "RELEASED"
	case a.FinancialState == "DISPUTED" || a.ReversedMinor > 0:
		return "REVERSAL_REVIEW_REQUIRED"
	case a.FinancialState == "FAILED":
		return "FAILED"
	case a.FinancialState == "UNKNOWN":
		return "OUTCOME_UNKNOWN"
	case a.AppliedMinor >= a.AmountMinor:
		return "APPLIED"
	case a.AppliedMinor > 0:
		return "PARTIALLY_APPLIED"
	case a.FinancialState == "VERIFIED" && (a.SettlementState == nil || *a.SettlementState != "SETTLED"):
		return "COLLECTED_SETTLEMENT_PENDING"
	case a.FinancialState == "VERIFIED":
		return "SETTLED_UNAPPLIED"
	case a.RequestState != nil && *a.RequestState == "QUEUED":
		return "QUEUED"
	default:
		return "PENDING"
	}
}

func pmsFolioResponsibility(ctx context.Context, q database.Querier, org ids.ID, f hospitality.Folio, agreements []PMSCollectionAgreement, now time.Time) (PMSFolioResponsibility, error) {
	out := PMSFolioResponsibility{Currency: f.Currency}
	e := q.QueryRow(ctx, `SELECT coalesce(sum(i.amount_minor),0)::bigint,coalesce(sum(i.amount_minor) FILTER (WHERE i.kind='ACCOMMODATION' OR i.kind='ADJUSTMENT' AND src.kind='ACCOMMODATION'),0)::bigint,coalesce((SELECT sum(x.amount_minor) FROM folio_payment_references x WHERE x.organisation_id=$1 AND x.folio_id=$2),0)::bigint FROM folio_items i LEFT JOIN folio_items src ON src.organisation_id=i.organisation_id AND src.id=i.adjusts_item_id WHERE i.organisation_id=$1 AND i.folio_id=$2`, org, f.ID).Scan(&out.FolioTotalMinor, &out.AccommodationMinor, &out.AppliedReceiptsMinor)
	if e != nil {
		return out, e
	}
	out.AncillaryNetMinor = out.FolioTotalMinor - out.AccommodationMinor
	for _, a := range agreements {
		if a.Released {
			continue
		}
		if a.PayerType == "CORPORATE_BOOKER" {
			out.AllocatedCorporateMinor += a.AmountMinor
		} else {
			out.AllocatedGuestMinor += a.AmountMinor
		}
		if a.CollectionState != "APPLIED" {
			out.MandatoryPayerUnverified = true
		}
		if a.FinancialState == "VERIFIED" && a.SettlementState != nil && *a.SettlementState == "SETTLED" && a.ReversedMinor == 0 {
			out.UnappliedReceiptsMinor += a.AmountMinor - a.AppliedMinor
		}
	}
	allocated := out.AllocatedGuestMinor + out.AllocatedCorporateMinor
	out.UnallocatedMinor = max(out.AncillaryNetMinor-allocated, 0)
	out.OverAllocatedMinor = max(allocated-out.AncillaryNetMinor, 0)
	if out.BalanceMinor, e = hospitality.Balance(ctx, q, org, f.ID, now); e != nil {
		return out, e
	}
	// Conservation: every folio minor unit has exactly one responsibility bucket.
	out.Conserved = out.FolioTotalMinor == out.AccommodationMinor+allocated+out.UnallocatedMinor-out.OverAllocatedMinor
	return out, nil
}

// folioCollectionAuthority replaces the pre-arrival reservation checks for an
// in-house folio agreement: the agreement must be unreleased, its folio still
// collectable and the reservation an actual stay.
func (h *Handler) folioCollectionAuthority(ctx context.Context, tx pgx.Tx, t tenant, job reservationFinancialJob) error {
	var ok bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folio_collection_agreements a JOIN folios f ON f.organisation_id=a.organisation_id AND f.id=a.folio_id JOIN reservations r ON r.organisation_id=a.organisation_id AND r.id=a.reservation_id WHERE a.organisation_id=$1 AND a.id=$2 AND a.financial_group_id=$3 AND f.state IN ('OPEN','SETTLEMENT_PENDING') AND r.state IN ('CHECKED_IN','CHECKED_OUT','COMPLETED') AND NOT EXISTS(SELECT 1 FROM folio_collection_cancellations c WHERE c.organisation_id=a.organisation_id AND c.agreement_id=a.id))`, t.Organisation, *job.FolioAgreementID, job.FinancialGroupID).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return apperrors.Conflict("FOLIO_COLLECTION_AUTHORITY_STALE", "The folio agreement was released or its folio is no longer collectable; readback remains available.")
	}
	return nil
}

// folioCollectionComponents maps each accepted allocation to the existing
// Payment policy-library component for that ancillary kind.
func (h *Handler) folioCollectionComponents(ctx context.Context, job reservationFinancialJob, family, payer string) ([]payment.Component, error) {
	rows, e := commercialJSONRows(ctx, h.Auth.Service.Pool, `SELECT jsonb_build_object('item_id',l.folio_item_id,'kind',i.kind,'amount_minor',l.amount_minor,'folio_id',l.folio_id) FROM folio_collection_allocations l JOIN folio_items i ON i.organisation_id=l.organisation_id AND i.id=l.folio_item_id WHERE l.organisation_id=$1 AND l.agreement_id=$2 ORDER BY l.folio_item_id`, job.OrganisationID, *job.FolioAgreementID)
	if e != nil {
		return nil, e
	}
	out := make([]payment.Component, 0, len(rows))
	for _, raw := range rows {
		var a struct {
			ItemID      ids.ID `json:"item_id"`
			Kind        string `json:"kind"`
			AmountMinor int64  `json:"amount_minor"`
			FolioID     ids.ID `json:"folio_id"`
		}
		if e = json.Unmarshal(raw, &a); e != nil {
			return nil, e
		}
		kind, ok := hospitality.FolioPaymentComponent(family, a.Kind)
		if !ok {
			return nil, apperrors.Conflict("FOLIO_PAYMENT_COMPONENT_UNSUPPORTED", "The current Payment policy family does not support this ancillary source.")
		}
		out = append(out, payment.Component{Ref: a.ItemID.String(), Kind: kind, AmountMinor: a.AmountMinor, PayerRef: payer, Recurrence: "ONE_TIME", ServiceEvidenceRef: "business-folio:" + a.FolioID.String() + ":" + a.ItemID.String()})
	}
	if len(out) == 0 {
		return nil, apperrors.Conflict("FOLIO_COLLECTION_REVIEW_REQUIRED", "Folio agreement has no accepted allocations.")
	}
	return out, nil
}
