// Package finance contains approval references only. Payment Service remains
// the sole executor of charges, refunds, payouts and provider operations.
package finance

import (
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

type AuthorizationReference struct {
	OrganisationID, ApprovalID ids.ID
	Action, OperationDigest    string
	AmountMinor                int64
	Currency                   string
}

func (r AuthorizationReference) Validate() error {
	if r.OrganisationID.ValidateV7() != nil || r.ApprovalID.ValidateV7() != nil || r.AmountMinor < 0 || len(r.OperationDigest) != 64 || (r.Action != "HIGH_VALUE_REFUND" && r.Action != "BUSINESS_PAYOUT_DESTINATION_CHANGE") {
		return errors.New("invalid financial approval reference")
	}
	_, err := money.ParseCurrency(r.Currency)
	return err
}
