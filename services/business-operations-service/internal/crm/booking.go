package crm

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func decodeTerms(raw []byte) (CorporateTerms, error) {
	var t CorporateTerms
	e := json.Unmarshal(raw, &t)
	return t, e
}

func RequireGuestScope(ctx context.Context, q database.Querier, org, property, guest ids.ID) error {
	var valid bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_property_scopes WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 AND state='ACTIVE')`, org, guest, property).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return apperrors.PermissionDenied("Current guest property association required.")
	}
	return nil
}
func RequireIdentityLink(ctx context.Context, q database.Querier, org, property, guest ids.ID, kind, hash string, now time.Time) error {
	if e := RequireGuest(ctx, q, org, property, guest); e != nil {
		return e
	}
	var valid bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_identity_links l LEFT JOIN LATERAL (SELECT p.* FROM customer_accounts_projection p WHERE p.organisation_id=l.organisation_id AND p.connection_id=l.connection_id AND p.source_kind=l.source_kind AND p.source_reference_hash=l.source_reference_hash AND p.source_service=l.source_authority ORDER BY p.source_version DESC LIMIT 1) current_source ON true WHERE l.organisation_id=$1 AND l.guest_id=$2 AND l.source_kind=$3 AND l.source_reference_hash=$4 AND l.state='ACTIVE' AND (l.property_id=$5 OR l.source_authority='business-operations-service') AND (l.projection_id IS NULL OR current_source.state='ACTIVE' AND current_source.expires_at>$6))`, org, guest, kind, hash, property, now).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return apperrors.PermissionDenied("Exact current typed identity proof is required for this canonical guest reference.")
	}
	return nil
}
func RequireCorporateScope(ctx context.Context, q database.Querier, org, property, corporate ids.ID) error {
	var valid bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM corporate_customer_profiles c JOIN corporate_property_scopes s ON s.organisation_id=c.organisation_id AND s.corporate_id=c.id WHERE c.organisation_id=$1 AND c.id=$2 AND c.state='ACTIVE' AND s.property_id=$3 AND s.state='ACTIVE')`, org, corporate, property).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return apperrors.PermissionDenied("Current corporate customer and property association required.")
	}
	return nil
}
func RequireCurrentContract(ctx context.Context, q database.Querier, org, property, corporate, contract ids.ID, now time.Time) error {
	if e := RequireCorporateScope(ctx, q, org, property, corporate); e != nil {
		return e
	}
	var valid bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM corporate_customer_contracts c WHERE c.organisation_id=$1 AND c.property_id=$2 AND c.corporate_id=$3 AND c.id=$4 AND c.state='ACTIVE' AND c.effective_at<=$5 AND c.expires_at>$5 AND NOT EXISTS(SELECT 1 FROM corporate_customer_contracts newer WHERE newer.organisation_id=c.organisation_id AND newer.contract_key=c.contract_key AND newer.version>c.version))`, org, property, corporate, contract, now).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return apperrors.PermissionDenied("Exact current effective corporate contract required.")
	}
	return nil
}

type BookingLine struct {
	PlanID           ids.ID
	DateFrom, DateTo string
}
type CorporateBooking struct {
	CorporateID, ContractID          ids.ID
	Source, Responsibility, Currency string
	AmountMinor                      int64
	Lines                            []BookingLine
	GuestIDs                         []ids.ID
}

func RequireCorporateBooking(ctx context.Context, q database.Querier, org, property ids.ID, b CorporateBooking, now time.Time) error {
	if e := RequireCurrentContract(ctx, q, org, property, b.CorporateID, b.ContractID, now); e != nil {
		return e
	}
	c, e := GetContract(ctx, q, org, b.ContractID)
	if e != nil {
		return e
	}
	if e = termsCover(c.Definition, b); e != nil {
		return e
	}
	if c.Definition.RequireNamedTravelers && len(b.GuestIDs) == 0 {
		return apperrors.PermissionDenied("Corporate policy requires registered authorized travelers.")
	}
	for _, guest := range b.GuestIDs {
		if e = RequireGuest(ctx, q, org, property, guest); e != nil {
			return e
		}
		var raw []byte
		e = q.QueryRow(ctx, `SELECT policy FROM guest_company_associations WHERE organisation_id=$1 AND property_id=$2 AND corporate_id=$3 AND contract_id=$4 AND guest_id=$5 AND state='ACTIVE' AND effective_at<=$6 AND expires_at>$6`, org, property, b.CorporateID, c.ID, guest, now).Scan(&raw)
		if e != nil {
			return apperrors.PermissionDenied("Every named corporate guest requires a current approved traveler association.")
		}
		policy, err := decodeTerms(raw)
		if err != nil {
			return err
		}
		if e = termsCover(policy, b); e != nil {
			return e
		}
	}
	return nil
}
func termsCover(t CorporateTerms, b CorporateBooking) error {
	if t.Currency != b.Currency || b.AmountMinor < 0 || b.AmountMinor > t.MaxBookingMinor || t.BillingResponsibility != b.Responsibility || !slices.Contains(t.AllowedSources, b.Source) {
		return apperrors.PermissionDenied("Booking exceeds the approved corporate/traveler financial or source policy.")
	}
	for _, line := range b.Lines {
		from, e := time.Parse(time.DateOnly, line.DateFrom)
		if e != nil {
			return e
		}
		to, e := time.Parse(time.DateOnly, line.DateTo)
		if e != nil {
			return e
		}
		if !from.Before(to) || int(to.Sub(from)/(24*time.Hour)) > t.MaxNights || !slices.Contains(t.RatePlanIDs, line.PlanID) {
			return apperrors.PermissionDenied("Booking exceeds the approved negotiated rate/length-of-stay policy.")
		}
	}
	return nil
}
