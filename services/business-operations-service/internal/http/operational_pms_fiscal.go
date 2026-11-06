package businesshttp

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"reflect"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

type PMSFiscalNetAllocation struct {
	Kind          string  `json:"kind"`
	AmountMinor   int64   `json:"amount_minor"`
	AddOnID       *ids.ID `json:"add_on_id,omitempty"`
	SourceVersion int64   `json:"source_version,omitempty"`
	Quantity      int     `json:"quantity,omitempty"`
}
type PMSFiscalReviewCommand struct {
	FolioVersion          int64                    `json:"folio_version"`
	ItemID                ids.ID                   `json:"item_id"`
	ExpectedReviewVersion int64                    `json:"expected_review_version"`
	NetAllocations        []PMSFiscalNetAllocation `json:"net_allocations,omitempty"`
	ApprovalID            ids.ID                   `json:"approval_id,omitempty"`
	EvidenceReference     string                   `json:"evidence_reference,omitempty"`
	Reason                string                   `json:"reason"`
}
type acceptedFiscalSource struct {
	ReservationRoomID ids.ID                `json:"reservation_room_id"`
	Quote             revenue.QuoteSnapshot `json:"accepted_quote"`
	Slot              int                   `json:"slot"`
	Quantity          int                   `json:"quantity"`
}

func (h *Handler) acceptedFolioFiscal(ctx context.Context, q database.Querier, f hospitality.Folio, item hospitality.Item) (hospitality.FiscalBreakdown, revenue.QuoteSnapshot, error) {
	var source acceptedFiscalSource
	if item.Kind != "ACCOMMODATION" || json.Unmarshal(item.PolicySnapshot, &source) != nil || source.Quote.QuoteID.ValidateV7() != nil || source.Quote.PropertyID != f.PropertyID || source.Quote.Currency != f.Currency || source.Quantity != source.Quote.Quantity {
		return hospitality.FiscalBreakdown{}, source.Quote, apperrors.Conflict("FOLIO_FISCAL_SOURCE_REQUIRED", "Original same-property accepted gross quote and quantity source required.")
	}
	canonical, e := loadCommercialQuote(ctx, q, f.OrganisationID, source.Quote.QuoteID)
	if e != nil {
		return hospitality.FiscalBreakdown{}, source.Quote, e
	}
	frozen := source.Quote
	if canonical.PropertyID != frozen.PropertyID || canonical.ProductID != frozen.ProductID || canonical.PoolID != frozen.PoolID || canonical.Quantity != frozen.Quantity || canonical.AmountMinor != frozen.AmountMinor || canonical.Currency != frozen.Currency || canonical.BaseAmountMinor != frozen.BaseAmountMinor || canonical.DiscountMinor != frozen.DiscountMinor || canonical.AddOnsMinor != frozen.AddOnsMinor || !reflect.DeepEqual(canonical.Components, frozen.Components) || !reflect.DeepEqual(canonical.Nights, frozen.Nights) {
		return hospitality.FiscalBreakdown{}, frozen, apperrors.Conflict("FISCAL_ACCEPTED_SNAPSHOT_MISMATCH", "Accepted gross source must match its retained immutable quote.")
	}
	// A retained original quote may contain source metadata omitted by an older
	// folio serializer. Reuse that actual record, never infer lost quantities.
	source.Quote = canonical
	profiles := map[ids.ID]hospitality.FiscalPolicySource{}
	for _, night := range source.Quote.Nights {
		if _, known := profiles[night.TaxProfileID]; known {
			continue
		}
		profile, e := typedJSON[CommercialTaxProfile](ctx, q, `SELECT to_jsonb(p) FROM tax_profiles p WHERE organisation_id=$1 AND property_id=$2 AND id=$3`, f.OrganisationID, f.PropertyID, night.TaxProfileID)
		if e != nil {
			return hospitality.FiscalBreakdown{}, source.Quote, e
		}
		profiles[profile.ID] = hospitality.FiscalPolicySource{Version: profile.Version, Policy: profile.Definition}
	}
	detail, e := hospitality.AcceptedFiscalBreakdown(source.Quote, profiles, source.Slot)
	if e != nil || detail.GrossMinor != item.AmountMinor {
		return detail, source.Quote, apperrors.Conflict("FOLIO_FISCAL_SOURCE_MISMATCH", "Original accepted fiscal components must reconcile without repricing.")
	}
	return detail, source.Quote, nil
}

func (h *Handler) captureFolioFiscal(ctx context.Context, tx pgx.Tx, t tenant, f hospitality.Folio, item hospitality.Item, detail hospitality.FiscalBreakdown, kind string, approval ids.ID, evidence, reason string, bumpFolio bool) (hospitality.FiscalReview, error) {
	var prior int64
	if e := tx.QueryRow(ctx, `SELECT coalesce(max(review_version),0) FROM folio_fiscal_reviews WHERE organisation_id=$1 AND folio_item_id=$2`, t.Organisation, item.ID).Scan(&prior); e != nil {
		return hospitality.FiscalReview{}, e
	}
	var source acceptedFiscalSource
	if json.Unmarshal(item.PolicySnapshot, &source) != nil || source.ReservationRoomID.ValidateV7() != nil {
		return hospitality.FiscalReview{}, apperrors.Conflict("FISCAL_SOURCE_IDENTITY", "Original accepted room source required.")
	}
	id := ids.New()
	raw := marshalCommercial(detail)
	digest := security.SHA256Hex(raw)
	_, e := tx.Exec(ctx, `INSERT INTO folio_fiscal_reviews(id,organisation_id,property_id,folio_id,folio_item_id,reservation_room_id,source_quote_id,review_version,expected_folio_version,state,source_kind,breakdown,digest,approval_id,evidence_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, id, t.Organisation, f.PropertyID, f.ID, item.ID, source.ReservationRoomID, detail.QuoteID, prior+1, f.Version, detail.State, kind, raw, digest, approval, evidence, reason, t.Account)
	if e != nil {
		return hospitality.FiscalReview{}, e
	}
	if e = h.pmsCommit(ctx, tx, t, f.PropertyID, id, "folio_fiscal_review", "FOLIO_FISCAL_DETAIL_RECORDED", "hospitality.folio.fiscal.updated.v1", 1, map[string]any{"folio_id": f.ID, "item_id": item.ID, "review_version": prior + 1, "digest": digest, "state": detail.State, "source_kind": kind, "approval_id": approval, "actor_service": serviceActor(t), "reason": reason}); e != nil {
		return hospitality.FiscalReview{}, e
	}
	if bumpFolio {
		if _, e = tx.Exec(ctx, `UPDATE folios SET state=CASE WHEN state='CLOSED' AND $3='LEGACY_AGGREGATE_REVIEW_REQUIRED' THEN 'SETTLEMENT_PENDING' ELSE state END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, f.ID, detail.State); e != nil {
			return hospitality.FiscalReview{}, e
		}
		if e = h.pmsCommit(ctx, tx, t, f.PropertyID, f.ID, "folio", "FOLIO_FISCAL_DETAIL_CHANGED", "hospitality.folio.updated.v1", f.Version+1, map[string]any{"fiscal_review_id": id, "item_id": item.ID, "digest": digest, "state": detail.State, "actor_service": serviceActor(t), "reason": reason}); e != nil {
			return hospitality.FiscalReview{}, e
		}
	}
	if detail.State == "ITEMIZED" || detail.State == "OPERATOR_REVIEWED" {
		ready, e := hospitality.FiscalReady(ctx, tx, t.Organisation, f.ID)
		if e != nil {
			return hospitality.FiscalReview{}, e
		}
		if ready {
			if e = h.resolvePMSFolioExceptionsByCode(ctx, tx, t, f, "FISCAL_SOURCE_REVIEW_REQUIRED"); e != nil {
				return hospitality.FiscalReview{}, e
			}
		}
	} else {
		if e = h.pmsOperationException(ctx, tx, t, f, security.SHA256Hex(item.PolicySnapshot), "FISCAL_SOURCE_REVIEW_REQUIRED", map[string]any{"item_id": item.ID, "state": detail.State, "reason": "Original source lacks individual add-on attribution; reviewed net allocation required"}); e != nil {
			return hospitality.FiscalReview{}, e
		}
	}
	return hospitality.GetFiscalReview(ctx, tx, t.Organisation, id)
}

func (h *Handler) pmsFiscalReviews(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	f, e := hospitality.GetFolio(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "folios:read", "PROPERTY", f.PropertyID, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := hospitality.ListFiscalReviews(r.Context(), h.Auth.Service.Pool, t.Organisation, id, int32(limit), int32(offset))
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) pmsFiscalReview(w http.ResponseWriter, r *http.Request) {
	var in PMSFiscalReviewCommand
	if !decode(w, r, &in) {
		return
	}
	if in.FolioVersion < 1 || in.ExpectedReviewVersion < 0 || in.ItemID.ValidateV7() != nil || !validStaffReason(in.Reason) || len(in.NetAllocations) > 101 || len(in.NetAllocations) > 0 && (!crm.OpaqueReference(in.EvidenceReference) || len(in.EvidenceReference) < 5) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current folio/item/review versions and reason required; legacy net attribution needs reviewed evidence."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := hospitality.GetFolio(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:folio:fiscal:"+id.String()+":"+in.ItemID.String(), in, "folios:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		f, e := hospitality.GetFolio(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if f.Version != in.FolioVersion || f.State == "DISPUTED" {
			return 0, nil, apperrors.Conflict("FOLIO_FISCAL_VERSION", "Current undisputed folio required.")
		}
		item, e := hospitality.Load[hospitality.Item](ctx, tx, `SELECT to_jsonb(i) FROM folio_items i WHERE organisation_id=$1 AND folio_id=$2 AND id=$3`, t.Organisation, id, in.ItemID)
		if e != nil {
			return 0, nil, e
		}
		var prior int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(review_version),0) FROM folio_fiscal_reviews WHERE organisation_id=$1 AND folio_item_id=$2`, t.Organisation, item.ID).Scan(&prior); e != nil {
			return 0, nil, e
		}
		if prior != in.ExpectedReviewVersion {
			return 0, nil, apperrors.Conflict("FISCAL_REVIEW_VERSION", "Fiscal review changed.")
		}
		detail, quote, e := h.acceptedFolioFiscal(ctx, tx, f, item)
		if e != nil {
			return 0, nil, e
		}

		if len(in.NetAllocations) == 0 && prior > 0 {
			if !in.ApprovalID.IsZero() || in.EvidenceReference != "" {
				return 0, nil, apperrors.InvalidRequest("Source-derived detail does not consume a manual attribution approval.")
			}
			current, e := hospitality.Load[hospitality.FiscalReview](ctx, tx, `SELECT to_jsonb(v) FROM folio_fiscal_reviews v WHERE organisation_id=$1 AND folio_item_id=$2 ORDER BY review_version DESC LIMIT 1`, t.Organisation, item.ID)
			if e != nil {
				return 0, nil, e
			}
			// Rebuilding original source detail must not overwrite an independently
			// reviewed legacy allocation or create a duplicate unchanged version.
			return 200, map[string]any{"review": current, "folio_version": f.Version, "financial_effect": "DETAIL_ONLY", "changed": false}, nil
		}
		kind := "ACCEPTED_QUOTE"
		if len(in.NetAllocations) > 0 {
			if detail.State != "LEGACY_AGGREGATE_REVIEW_REQUIRED" {
				return 0, nil, apperrors.Conflict("FISCAL_SOURCE_ALREADY_ITEMIZED", "Recorded source quantities already determine fiscal detail.")
			}
			detail, e = reviewLegacyFiscalNet(detail, quote, in.NetAllocations, in.EvidenceReference)
			if e != nil {
				return 0, nil, e
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_FISCAL_REVIEW", target.Scope, in.ApprovalID, map[string]any{"folio_id": id, "command": op}, "", &item.AmountMinor, item.Currency); e != nil {
				return 0, nil, e
			}
			kind = "OPERATOR_ATTESTED"
		} else if !in.ApprovalID.IsZero() || in.EvidenceReference != "" {
			return 0, nil, apperrors.InvalidRequest("Source-derived detail does not consume a manual attribution approval.")
		}
		review, e := h.captureFolioFiscal(ctx, tx, t, f, item, detail, kind, in.ApprovalID, in.EvidenceReference, in.Reason, true)
		if e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"review": review, "folio_version": f.Version + 1, "financial_effect": "DETAIL_ONLY", "changed": true}, nil
	})
}

func reviewLegacyFiscalNet(detail hospitality.FiscalBreakdown, quote revenue.QuoteSnapshot, in []PMSFiscalNetAllocation, evidence string) (hospitality.FiscalBreakdown, error) {
	var remaining int64
	taxes := []hospitality.FiscalComponent{}
	for _, part := range detail.Components {
		if part.Kind == "ACCEPTED_SERVICES_NET" {
			remaining += part.AmountMinor
		} else {
			taxes = append(taxes, part)
		}
	}
	parts := []hospitality.FiscalComponent{}
	seen := map[string]bool{}
	var total int64
	accommodation := false
	for _, part := range in {
		if part.AmountMinor < 0 || total > math.MaxInt64-part.AmountMinor {
			return detail, apperrors.InvalidRequest("Bounded nonnegative integer-minor source allocation required.")
		}
		total += part.AmountMinor
		c := hospitality.FiscalComponent{Kind: part.Kind, AmountMinor: part.AmountMinor, SourceReference: evidence}
		switch part.Kind {
		case "ACCOMMODATION_NET":
			if accommodation || part.AddOnID != nil || part.SourceVersion != 0 || part.Quantity != 0 {
				return detail, apperrors.InvalidRequest("One accommodation net component without add-on metadata required.")
			}
			accommodation = true
			c.Code = "ACCOMMODATION_NET"
		case "ADD_ON_NET":
			if part.AddOnID == nil || part.AddOnID.ValidateV7() != nil || part.SourceVersion < 1 || quote.AddOnVersions[part.AddOnID.String()] != part.SourceVersion || part.Quantity < 1 || part.Quantity > 100 {
				return detail, apperrors.InvalidRequest("Original accepted add-on identity/version and attested folio source quantity required.")
			}
			c.AddOnID = part.AddOnID
			c.SourceVersion = part.SourceVersion
			c.AcceptedQuantity = part.Quantity
			c.Code = "ADD_ON:" + part.AddOnID.String()
		default:
			return detail, apperrors.InvalidRequest("Only legacy net attribution is reviewable; frozen taxes and fees cannot change.")
		}
		if seen[c.Code] {
			return detail, apperrors.InvalidRequest("Duplicate fiscal source component.")
		}
		seen[c.Code] = true
		parts = append(parts, c)
	}
	if !accommodation || total != remaining {
		return detail, apperrors.Conflict("FISCAL_NET_CONSERVATION", "Reviewed net attribution must exactly conserve the known source net amount.")
	}
	detail.Components = append(parts, taxes...)
	detail.State = "OPERATOR_REVIEWED"
	detail.AllocationMethod = "OPERATOR_ATTESTED_FOLIO_NET_V1/CYCLIC_RESIDUAL_V1"
	return detail, nil
}
