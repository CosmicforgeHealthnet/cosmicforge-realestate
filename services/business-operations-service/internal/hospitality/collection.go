package hospitality

import (
	"slices"
	"strings"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

type FolioCollectionClaim struct {
	ItemID      ids.ID `json:"item_id"`
	AmountMinor int64  `json:"amount_minor"`
}
type FolioCollectionSource struct {
	ItemID          ids.ID `json:"item_id"`
	Kind            string `json:"kind"`
	Currency        string `json:"currency"`
	SourceReference string `json:"source_reference"`
	PolicyReference string `json:"policy_reference"`
	OriginalMinor   int64  `json:"original_minor"`
	NetMinor        int64  `json:"net_minor"`
	AllocatedMinor  int64  `json:"allocated_minor"`
}
type FolioCollectionAllocation struct {
	Source        FolioCollectionSource `json:"source"`
	AmountMinor   int64                 `json:"amount_minor"`
	ComponentKind string                `json:"component_kind"`
}
type FolioCollectionPlan struct {
	Currency    string                      `json:"currency"`
	AmountMinor int64                       `json:"amount_minor"`
	Allocations []FolioCollectionAllocation `json:"allocations"`
}

// PlanFolioCollection allocates accepted ancillary obligations; it neither
// changes prices nor asserts payment. Original accommodation is already owned
// by its reservation financial group and must never be collected again here.
func PlanFolioCollection(sources []FolioCollectionSource, claims []FolioCollectionClaim, currency, family string) (FolioCollectionPlan, error) {
	out := FolioCollectionPlan{Currency: currency, Allocations: []FolioCollectionAllocation{}}
	total, e := money.New(0, currency)
	if e != nil {
		return out, apperrors.InvalidRequest("Valid accepted folio currency required.")
	}
	if len(claims) < 1 || len(claims) > 50 {
		return out, apperrors.InvalidRequest("One to fifty distinct accepted source items required.")
	}
	byID := map[ids.ID]FolioCollectionSource{}
	for _, s := range sources {
		byID[s.ItemID] = s
	}
	seen := map[ids.ID]bool{}
	for _, c := range claims {
		if c.ItemID.ValidateV7() != nil || c.AmountMinor < 1 || c.AmountMinor > 1000000000000 || seen[c.ItemID] {
			return out, apperrors.InvalidRequest("Distinct UUIDv7 source items and bounded positive integer-minor amounts required.")
		}
		seen[c.ItemID] = true
		s, ok := byID[c.ItemID]
		if !ok {
			return out, apperrors.PermissionDenied("Every collection item must belong to the authenticated folio.")
		}
		component, ok := FolioPaymentComponent(family, s.Kind)
		if !ok {
			return out, apperrors.Conflict("FOLIO_PAYMENT_COMPONENT_UNSUPPORTED", "The selected Payment policy family does not support this ancillary source; original accommodation and compensating adjustments are not new collections.")
		}
		if s.Currency != currency || s.OriginalMinor < 0 || s.NetMinor < 0 || s.AllocatedMinor < 0 || s.NetMinor > s.OriginalMinor || strings.TrimSpace(s.SourceReference) == "" || len(strings.TrimSpace(s.PolicyReference)) < 5 {
			return out, apperrors.Conflict("FOLIO_COLLECTION_SOURCE_REVIEW_REQUIRED", "Current accepted source policy, currency and compensating-adjustment budget require review.")
		}
		if s.AllocatedMinor > s.NetMinor || c.AmountMinor > s.NetMinor-s.AllocatedMinor {
			return out, apperrors.Conflict("FOLIO_COLLECTION_SOURCE_EXHAUSTED", "Existing accepted payer agreements and this allocation must fit the remaining original source obligation.")
		}
		part, _ := money.New(c.AmountMinor, currency)
		total, e = total.Add(part)
		if e != nil || total.AmountMinor > 1000000000000 {
			return out, apperrors.InvalidRequest("Collection exceeds the bounded integer-minor contract.")
		}
		out.Allocations = append(out.Allocations, FolioCollectionAllocation{Source: s, AmountMinor: c.AmountMinor, ComponentKind: component})
	}
	slices.SortFunc(out.Allocations, func(a, b FolioCollectionAllocation) int {
		return strings.Compare(a.Source.ItemID.String(), b.Source.ItemID.String())
	})
	out.AmountMinor = total.AmountMinor
	return out, nil
}

// These identifiers are the existing Payment policy-library components. Never
// disguise a missing tax/deposit-remedy operation as an unrelated component.
func FolioPaymentComponent(family, kind string) (string, bool) {
	if family == "HOTEL" {
		switch kind {
		case "MEALS":
			return "BOARD", true
		case "MINIBAR", "OTHER":
			return "EXTRA", true
		case "LAUNDRY", "SPA", "TRANSFER", "EVENT", "FEE":
			return "SERVICE", true
		case "TAX":
			return "TAX", true
		case "DAMAGE":
			return "PENALTY", true
		}
	}
	if family == "SHORT_LET" && slices.Contains([]string{"MEALS", "MINIBAR", "LAUNDRY", "SPA", "TRANSFER", "EVENT", "FEE", "OTHER"}, kind) {
		return "ADD_ON", true
	}
	return "", false
}
