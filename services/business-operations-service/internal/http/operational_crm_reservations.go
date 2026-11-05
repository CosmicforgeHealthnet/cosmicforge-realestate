package businesshttp

import (
	"context"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func (h *Handler) validateReservationCRM(ctx context.Context, q database.Querier, t tenant, in reservations.Create, quotes []revenue.QuoteSnapshot, amount int64, currency string) error {
	guests := []ids.ID{}
	for _, g := range in.Guests {
		if g.GuestID == nil {
			continue
		}
		if !t.Account.IsZero() {
			if _, _, e := h.authorize(ctx, q, "guests:read", "PROPERTY", in.PropertyID, false); e != nil {
				return e
			}
		}
		if e := crm.RequireIdentityLink(ctx, q, t.Organisation, in.PropertyID, *g.GuestID, g.SourceKind, h.crmSourceHash(t.Organisation, g.SourceKind, g.SourceReference), h.Auth.Service.Now()); e != nil {
			return e
		}
		guests = append(guests, *g.GuestID)
	}
	if in.CorporateID == nil && in.CorporateContractID == nil {
		return nil
	}
	if in.CorporateID == nil || in.CorporateContractID == nil {
		return apperrors.InvalidRequest("Corporate customer and exact negotiated contract must be supplied together.")
	}
	if !t.Account.IsZero() {
		if _, _, e := h.authorize(ctx, q, "corporate:read", "PROPERTY", in.PropertyID, false); e != nil {
			return e
		}
	}
	lines := []crm.BookingLine{}
	for _, quote := range quotes {
		lines = append(lines, crm.BookingLine{PlanID: quote.RatePlanID, DateFrom: quote.DateFrom, DateTo: quote.DateTo})
	}
	return crm.RequireCorporateBooking(ctx, q, t.Organisation, in.PropertyID, crm.CorporateBooking{CorporateID: *in.CorporateID, ContractID: *in.CorporateContractID, Source: in.Source, Responsibility: in.PaymentResponsibility, Currency: currency, AmountMinor: amount, Lines: lines, GuestIDs: guests}, h.Auth.Service.Now())
}
func (h *Handler) reservationCRMEligibility(ctx context.Context, q database.Querier, t tenant, v reservations.Reservation, rooms []reservations.Room) error {
	detail, e := h.reservationDetail(ctx, q, t.Organisation, v.ID)
	if e != nil {
		return e
	}
	quotes := []revenue.QuoteSnapshot{}
	for _, room := range rooms {
		quotes = append(quotes, room.Snapshot)
	}
	return h.validateReservationCRM(ctx, q, t, reservations.Create{PropertyID: v.PropertyID, Source: v.Source, PaymentResponsibility: v.PaymentResponsibility, CorporateID: v.CorporateID, CorporateContractID: v.CorporateContractID, Guests: detail.Guests}, quotes, v.AmountMinor, v.Currency)
}
