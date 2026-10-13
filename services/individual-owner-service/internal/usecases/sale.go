package usecases

import (
	"context"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Sale transactions (§13.17–§13.19). The sale aggregate is independent of
// the listing. Selling is an owner-only authority (co-owner SALE consent,
// verified authority, identity policy). Accepting an offer creates the
// configured closing checklist; completion requires every required
// milestone. Money milestones are satisfied by payment-service truth
// (PLATFORM) or by owner evidence plus the buyer's confirmation
// (OFF_PLATFORM); perfection/registration only with evidence — the service
// never claims governmental completion on its own.

const AggSale = "sale_transaction"

// MilestoneTemplate is one configured closing-checklist entry.
type MilestoneTemplate struct {
	Code     string
	Title    string
	Kind     string
	Required bool
}

// SaleChecklists are the configured closing checklists per sale kind.
var SaleChecklists = map[string][]MilestoneTemplate{
	"PROPERTY": {
		{"PURCHASE_AGREEMENT", "Purchase agreement signed", "DOCUMENT", true},
		{"BALANCE_PAYMENT", "Purchase price paid", "PAYMENT", true},
		{"POSSESSION_HANDOVER", "Possession and keys handed over", "HANDOVER", true},
		{"TITLE_PERFECTION", "Title registration / perfection", "PERFECTION", false},
	},
	"COMMERCIAL": {
		{"PURCHASE_AGREEMENT", "Purchase agreement signed", "DOCUMENT", true},
		{"BALANCE_PAYMENT", "Purchase price paid", "PAYMENT", true},
		{"TENANCY_SCHEDULE", "Existing tenancies and service-charge position disclosed", "DOCUMENT", true},
		{"POSSESSION_HANDOVER", "Possession handed over", "HANDOVER", true},
		{"TITLE_PERFECTION", "Title registration / perfection", "PERFECTION", false},
	},
	"LAND": {
		{"SURVEY_CHARTING", "Survey plan and charting confirmed", "DOCUMENT", true},
		{"PURCHASE_AGREEMENT", "Deed / purchase agreement signed", "DOCUMENT", true},
		{"BALANCE_PAYMENT", "Purchase price paid", "PAYMENT", true},
		{"STATE_CONSENT", "Statutory consent to the transfer obtained", "CONSENT", true},
		{"POSSESSION_HANDOVER", "Possession of the land handed over", "HANDOVER", true},
		{"TITLE_PERFECTION", "Title registration / perfection", "PERFECTION", false},
	},
}

// SaleView is a sale transaction.
type SaleView struct {
	ID                ids.ID          `json:"id"`
	PropertyID        ids.ID          `json:"property_id"`
	UnitID            *ids.ID         `json:"unit_id"`
	ListingID         *ids.ID         `json:"listing_id"`
	SaleKind          string          `json:"sale_kind"`
	Status            string          `json:"status"`
	Currency          string          `json:"currency"`
	AskingPriceMinor  *int64          `json:"asking_price_minor"`
	AcceptedOfferID   *ids.ID         `json:"accepted_offer_id"`
	BuyerRefID        *ids.ID         `json:"buyer_ref_id"`
	AgreedPriceMinor  *int64          `json:"agreed_price_minor"`
	TargetClosingDate *string         `json:"target_closing_date"`
	EndReason         *string         `json:"end_reason"`
	CompletedAt       *time.Time      `json:"completed_at"`
	Offers            []SaleOfferView `json:"offers,omitempty"`
	Milestones        []MilestoneView `json:"milestones,omitempty"`
	DueDiligence      []DiligenceView `json:"due_diligence,omitempty"`
	Version           int64           `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
}

// SaleOfferView is one offer or counter-offer.
type SaleOfferView struct {
	ID               ids.ID     `json:"id"`
	BuyerRefID       ids.ID     `json:"buyer_ref_id"`
	BuyerDisplayName string     `json:"buyer_display_name"`
	ParentOfferID    *ids.ID    `json:"parent_offer_id"`
	MadeBy           string     `json:"made_by"`
	AmountMinor      int64      `json:"amount_minor"`
	Currency         string     `json:"currency"`
	Financing        string     `json:"financing"`
	Conditions       []string   `json:"conditions"`
	ProofOfFundsRef  *string    `json:"proof_of_funds_ref" doc:"Reference to the buyer's proof of funds held by its domain (never copied)"`
	Status           string     `json:"status"`
	ExpiresAt        time.Time  `json:"expires_at"`
	DecidedAt        *time.Time `json:"decided_at"`
	DeclineReason    *string    `json:"decline_reason"`
	CreatedAt        time.Time  `json:"created_at"`
}

func saleOfferView(o db.SaleOffer) SaleOfferView {
	return SaleOfferView{ID: o.ID, BuyerRefID: o.BuyerRefID, BuyerDisplayName: o.BuyerDisplayName, ParentOfferID: o.ParentOfferID, MadeBy: o.MadeBy,
		AmountMinor: o.AmountMinor, Currency: o.Currency, Financing: o.Financing, Conditions: nonNil(o.Conditions), ProofOfFundsRef: o.ProofOfFundsRef,
		Status: o.Status, ExpiresAt: o.ExpiresAt, DecidedAt: o.DecidedAt, DeclineReason: o.DeclineReason, CreatedAt: o.CreatedAt}
}

// MilestoneView is one closing milestone.
type MilestoneView struct {
	ID                    ids.ID     `json:"id"`
	Code                  string     `json:"code"`
	Title                 string     `json:"title"`
	Kind                  string     `json:"kind"`
	RequiredForCompletion bool       `json:"required_for_completion"`
	Settlement            *string    `json:"settlement"`
	AmountMinor           *int64     `json:"amount_minor"`
	PaidMinor             int64      `json:"paid_minor" doc:"As reported by payment-service"`
	Status                string     `json:"status"`
	EvidenceMediaIDs      []ids.ID   `json:"evidence_media_ids"`
	AttestedBy            *string    `json:"attested_by"`
	BuyerConfirmedAt      *time.Time `json:"buyer_confirmed_at"`
	SatisfiedAt           *time.Time `json:"satisfied_at"`
	WaivedReason          *string    `json:"waived_reason"`
	DueDate               *string    `json:"due_date"`
	Version               int64      `json:"version"`
}

func milestoneView(m db.ClosingMilestone) MilestoneView {
	return MilestoneView{ID: m.ID, Code: m.Code, Title: m.Title, Kind: m.Kind, RequiredForCompletion: m.RequiredForCompletion, Settlement: m.Settlement,
		AmountMinor: m.AmountMinor, PaidMinor: m.PaidMinor, Status: m.Status, EvidenceMediaIDs: nonNilIDs(m.EvidenceMediaIds), AttestedBy: m.AttestedBy,
		BuyerConfirmedAt: m.BuyerConfirmedAt, SatisfiedAt: m.SatisfiedAt, WaivedReason: m.WaivedReason, DueDate: dateStr(m.DueDate), Version: m.Version}
}

// DiligenceView is a due-diligence item.
type DiligenceView struct {
	ID                         ids.ID    `json:"id"`
	Category                   string    `json:"category"`
	Title                      string    `json:"title"`
	RaisedBy                   string    `json:"raised_by"`
	Status                     string    `json:"status"`
	Blocking                   bool      `json:"blocking" doc:"Unresolved blocking items prevent contracting"`
	Finding                    *string   `json:"finding"`
	EvidenceMediaIDs           []ids.ID  `json:"evidence_media_ids"`
	ProfessionalAppointmentRef *ids.ID   `json:"professional_appointment_ref"`
	ResolutionNote             *string   `json:"resolution_note"`
	Version                    int64     `json:"version"`
	CreatedAt                  time.Time `json:"created_at"`
}

func diligenceView(d db.DueDiligenceItem) DiligenceView {
	return DiligenceView{ID: d.ID, Category: d.Category, Title: d.Title, RaisedBy: d.RaisedBy, Status: d.Status, Blocking: d.Blocking, Finding: d.Finding,
		EvidenceMediaIDs: nonNilIDs(d.EvidenceMediaIds), ProfessionalAppointmentRef: d.ProfessionalAppointmentRef, ResolutionNote: d.ResolutionNote,
		Version: d.Version, CreatedAt: d.CreatedAt}
}

func saleBase(t db.SaleTransaction) SaleView {
	return SaleView{ID: t.ID, PropertyID: t.PropertyID, UnitID: t.UnitID, ListingID: t.ListingID, SaleKind: t.SaleKind, Status: t.Status, Currency: t.Currency,
		AskingPriceMinor: t.AskingPriceMinor, AcceptedOfferID: t.AcceptedOfferID, BuyerRefID: t.BuyerRefID, AgreedPriceMinor: t.AgreedPriceMinor,
		TargetClosingDate: dateStr(t.TargetClosingDate), EndReason: t.EndReason, CompletedAt: t.CompletedAt, Version: t.Version, CreatedAt: t.CreatedAt}
}

func (s *Service) saleView(ctx context.Context, t db.SaleTransaction) (SaleView, error) {
	q := s.dbq(ctx)
	v := saleBase(t)
	v.Offers, v.Milestones, v.DueDiligence = []SaleOfferView{}, []MilestoneView{}, []DiligenceView{}
	os, err := s.q.ListSaleOffers(ctx, q, t.ID)
	if err != nil {
		return v, err
	}
	for _, o := range os {
		v.Offers = append(v.Offers, saleOfferView(o))
	}
	ms, err := s.q.ListMilestones(ctx, q, t.ID)
	if err != nil {
		return v, err
	}
	for _, m := range ms {
		v.Milestones = append(v.Milestones, milestoneView(m))
	}
	ds, err := s.q.ListDueDiligenceItems(ctx, q, t.ID)
	for _, d := range ds {
		v.DueDiligence = append(v.DueDiligence, diligenceView(d))
	}
	return v, err
}

// SaleEvent is sale.transaction.updated.v1 (customer-visible sale state).
type SaleEvent struct {
	SchemaVersion    int       `json:"schema_version"`
	SaleID           ids.ID    `json:"sale_id"`
	PropertyID       ids.ID    `json:"property_id"`
	ListingID        *ids.ID   `json:"listing_id"`
	SaleKind         string    `json:"sale_kind"`
	Status           string    `json:"status"`
	PreviousStatus   string    `json:"previous_status"`
	BuyerRefID       *ids.ID   `json:"buyer_ref_id"`
	AgreedPriceMinor *int64    `json:"agreed_price_minor"`
	Currency         string    `json:"currency"`
	SaleVersion      int64     `json:"sale_version"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func (s *Service) saleTo(ctx context.Context, id ids.ID, to, cmd, reason string) error {
	prev, _, err := s.transition(ctx, domain.Sale, id, lifecycle.Change{To: to, Command: cmd, Reason: reason})
	if err != nil {
		return err
	}
	t, err := s.q.GetSale(ctx, mustTx(ctx), id)
	if err != nil {
		return err
	}
	return s.publish(ctx, "sale.transaction.updated.v1", AggSale, id, SaleEvent{SchemaVersion: 1, SaleID: t.ID, PropertyID: t.PropertyID, ListingID: t.ListingID,
		SaleKind: t.SaleKind, Status: t.Status, PreviousStatus: prev.Status, BuyerRefID: t.BuyerRefID, AgreedPriceMinor: t.AgreedPriceMinor, Currency: t.Currency,
		SaleVersion: t.Version, OccurredAt: s.now()})
}

func saleKindOf(category string) string {
	switch category {
	case "COMMERCIAL":
		return "COMMERCIAL"
	case "LAND":
		return "LAND"
	}
	return "PROPERTY"
}

// saleFor authorizes and (for mutations) locks a sale.
func (s *Service) saleFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.SaleTransaction, authz.Decision, error) {
	t, err := s.q.GetSale(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return t, authz.Decision{}, notFound(AggSale)
	}
	if err != nil {
		return t, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, t.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggSale)
	}
	if err == nil && !isReadAction(action) {
		t, err = s.q.GetSaleForUpdate(ctx, mustTx(ctx), id)
	}
	return t, d, err
}

// SaleInput opens a sale transaction.
type SaleInput struct {
	PropertyID       ids.ID  `json:"property_id" v:"required"`
	UnitID           *ids.ID `json:"unit_id,omitempty"`
	ListingID        *ids.ID `json:"listing_id,omitempty" doc:"Published SALE listing (optional; the sale survives its withdrawal)"`
	Currency         string  `json:"currency" v:"required,format=currency"`
	AskingPriceMinor int64   `json:"asking_price_minor,omitempty" v:"min=1"`
}

// CreateSale opens a sale transaction (INQUIRY).
func (s *Service) CreateSale(ctx context.Context, subj authz.Subject, in SaleInput) (SaleView, error) {
	if err := schema.Validate(in); err != nil {
		return SaleView{}, err
	}
	var out SaleView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapSaleManage), in.PropertyID); err != nil {
			return err
		}
		p, err := s.requireMutable(ctx, in.PropertyID)
		if err != nil {
			return err
		}
		if err := s.checkSpace(ctx, in.PropertyID, in.UnitID, in.ListingID); err != nil {
			return err
		}
		if in.ListingID != nil {
			l, err := s.q.GetListing(ctx, mustTx(ctx), *in.ListingID)
			if err != nil {
				return err
			}
			if l.TransactionType != "SALE" {
				return precondition("TRANSACTION_TYPE_MISMATCH", "Only SALE listings can be attached to a sale.")
			}
		}
		t, err := s.openSale(ctx, p, in.UnitID, in.ListingID, in.Currency, optInt(in.AskingPriceMinor))
		if err != nil {
			return err
		}
		out = saleBase(t)
		return nil
	})
	return out, err
}

func optInt(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func (s *Service) openSale(ctx context.Context, p db.Property, unitID, listingID *ids.ID, currency string, asking *int64) (db.SaleTransaction, error) {
	tx, now := mustTx(ctx), s.now()
	t, err := s.q.InsertSale(ctx, tx, db.InsertSaleParams{ID: ids.New(), PropertyID: p.ID, UnitID: unitID, ListingID: listingID, SaleKind: saleKindOf(p.Category),
		Currency: currency, AskingPriceMinor: asking, CreatedAt: now})
	if isUnique(err, "sale_transactions_live_uniq") {
		return t, conflict("SALE_ALREADY_OPEN", "A sale is already in progress for this property.")
	}
	if err != nil {
		return t, err
	}
	if err := domain.Sale.RecordCreation(ctx, tx, t.ID, &t.PropertyID, "INQUIRY", now); err != nil {
		return t, err
	}
	if err := s.propertyHistory(ctx, p.ID, "SALE_OPENED", "Sale transaction opened", AggSale, t.ID); err != nil {
		return t, err
	}
	return t, s.record(ctx, "sale.create", AggSale, t.ID, map[string]any{"sale_kind": t.SaleKind}, nil)
}

// GetSale returns a sale with offers, milestones and due diligence.
func (s *Service) GetSale(ctx context.Context, subj authz.Subject, id ids.ID) (SaleView, error) {
	t, _, err := s.saleFor(ctx, subj, id, authz.Read(authz.CapSaleManage))
	if err != nil {
		return SaleView{}, err
	}
	return s.saleView(ctx, t)
}

// ListSales pages the caller's sales.
func (s *Service) ListSales(ctx context.Context, subj authz.Subject, propertyID ids.ID, status, cursor string, limit int) (pagination.Response[SaleView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[SaleView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[SaleView]{}, err
	}
	rows, err := s.q.ListSalesForAccount(ctx, s.dbq(ctx), db.ListSalesForAccountParams{AccountID: accountOf(subj), Now: s.now(), PropertyID: idp(propertyID),
		Status: strp(status), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[SaleView]{}, err
	}
	out := make([]SaleView, 0, len(rows))
	for _, r := range rows {
		out = append(out, saleBase(r))
	}
	return pagination.Build(out, limit, func(v SaleView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

// ---- offers ----

// CustomerSaleOfferInput is a buyer's offer (marketplace).
type CustomerSaleOfferInput struct {
	ListingID        ids.ID   `json:"listing_id" v:"required"`
	BuyerDisplayName string   `json:"buyer_display_name" v:"required,max=120"`
	AmountMinor      int64    `json:"amount_minor" v:"required,min=1"`
	Currency         string   `json:"currency" v:"required,format=currency"`
	Financing        string   `json:"financing" v:"required,enum=CASH|MORTGAGE|INSTALLMENT|OTHER"`
	Conditions       []string `json:"conditions,omitempty" v:"max=10"`
	ProofOfFundsRef  *string  `json:"proof_of_funds_ref,omitempty" v:"max=128" doc:"Reference only; documents stay with the buyer's domain"`
	ExpiresInDays    int32    `json:"expires_in_days,omitempty" v:"min=1,max=30"`
}

// CustomerMakeSaleOffer records an offer on a published SALE listing,
// opening the sale transaction when needed (INQUIRY → OFFERING).
func (s *Service) CustomerMakeSaleOffer(ctx context.Context, subj authz.Subject, in CustomerSaleOfferInput) (SaleOfferView, error) {
	if err := schema.Validate(in); err != nil {
		return SaleOfferView{}, err
	}
	var out SaleOfferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		buyer, err := s.requireCustomer(ctx, subj)
		if err != nil {
			return err
		}
		l, err := s.bookableListing(ctx, in.ListingID, "SALE")
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		t, err := s.q.LiveSaleForSpace(ctx, tx, db.LiveSaleForSpaceParams{PropertyID: l.PropertyID, UnitID: l.UnitID})
		if isNoRows(err) {
			p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
			if err != nil {
				return err
			}
			if t, err = s.openSale(ctx, p, l.UnitID, &l.ID, in.Currency, nil); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if t.Status != "INQUIRY" && t.Status != "OFFERING" {
			return conflict("SALE_NOT_ACCEPTING_OFFERS", "The property is no longer accepting offers.")
		}
		if t.Currency != in.Currency {
			return precondition("CURRENCY_MISMATCH", "Offers must be made in "+t.Currency+".")
		}
		if n, err := s.q.OpenBuyerOffer(ctx, tx, db.OpenBuyerOfferParams{SaleID: t.ID, BuyerRefID: buyer}); err != nil {
			return err
		} else if n > 0 {
			return conflict("OFFER_ALREADY_OPEN", "You already have an open offer; wait for the owner's answer.")
		}
		days := in.ExpiresInDays
		if days == 0 {
			days = int32(s.Policy.Domain.OfferTTL / (24 * time.Hour))
		}
		o, err := s.q.InsertSaleOffer(ctx, tx, db.InsertSaleOfferParams{ID: ids.New(), SaleID: t.ID, BuyerRefID: buyer, BuyerDisplayName: in.BuyerDisplayName,
			MadeBy: "BUYER", AmountMinor: in.AmountMinor, Currency: in.Currency, Financing: in.Financing, Conditions: nonNil(in.Conditions),
			ProofOfFundsRef: in.ProofOfFundsRef, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour), CreatedAt: now})
		if err != nil {
			return err
		}
		if t.Status == "INQUIRY" {
			if err := s.saleTo(ctx, t.ID, "OFFERING", "first_offer", ""); err != nil {
				return err
			}
		}
		p, err := s.q.GetProperty(ctx, tx, t.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateSaleOffer, map[string]string{"listing_title": l.Title}); err != nil {
			return err
		}
		out = saleOfferView(o)
		return s.record(ctx, "sale.offer", AggSale, t.ID, map[string]any{"offer_id": o.ID, "amount_minor": in.AmountMinor}, nil)
	})
	return out, err
}

// OfferDecisionInput is the owner's decision on an offer.
type OfferDecisionInput struct {
	Decision          string   `json:"decision" v:"required,enum=ACCEPT|COUNTER|DECLINE"`
	CounterAmount     int64    `json:"counter_amount_minor,omitempty" v:"min=1"`
	CounterConditions []string `json:"counter_conditions,omitempty" v:"max=10"`
	ReasonCode        *string  `json:"reason_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
	Settlement        string   `json:"settlement,omitempty" v:"enum=PLATFORM|OFF_PLATFORM" doc:"How the purchase money is paid (PLATFORM needs payment-service)"`
	ReservationMinor  int64    `json:"reservation_minor,omitempty" v:"min=1" doc:"Optional reservation deposit milestone"`
	TargetClosing     *string  `json:"target_closing_date,omitempty" v:"format=date"`
}

// DecideSaleOffer accepts, counters or declines a buyer offer. Accepting or
// countering binds the owner: co-owner SALE consent and the identity policy
// apply.
func (s *Service) DecideSaleOffer(ctx context.Context, subj authz.Subject, saleID, offerID ids.ID, in OfferDecisionInput) (SaleView, error) {
	if err := schema.Validate(in); err != nil {
		return SaleView{}, err
	}
	settlement := in.Settlement
	if settlement == "" {
		settlement = "OFF_PLATFORM"
	}
	if in.Decision == "ACCEPT" && settlement == "PLATFORM" && s.Payment == nil && !s.hasCoreFinancial() {
		return SaleView{}, unavailable("Platform settlement of the purchase price", "payment-service")
	}
	var out SaleView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, d, err := s.saleFor(ctx, subj, saleID, authz.Do(authz.CapSaleManage))
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		o, err := s.q.GetSaleOfferForUpdate(ctx, tx, offerID)
		if isNoRows(err) || (err == nil && o.SaleID != saleID) {
			return notFound("sale_offer")
		}
		if err != nil {
			return err
		}
		if o.Status != "SUBMITTED" || o.MadeBy != "BUYER" {
			return conflict("OFFER_NOT_AWAITING_OWNER", "The offer is not awaiting your decision.")
		}
		if !o.ExpiresAt.After(now) {
			return conflict("OFFER_EXPIRED", "The offer has expired.")
		}
		switch in.Decision {
		case "DECLINE":
			_, err = s.q.SetSaleOfferStatus(ctx, tx, db.SetSaleOfferStatusParams{ID: o.ID, Status: "SUBMITTED", Status_2: "DECLINED", DecidedAt: &now,
				DeclineReason: in.ReasonCode})
		case "COUNTER":
			if in.CounterAmount == 0 {
				return apperrors.InvalidRequest("counter_amount_minor is required.")
			}
			if err := s.bindingSaleChecks(ctx, d, t); err != nil {
				return err
			}
			if _, err = s.q.SetSaleOfferStatus(ctx, tx, db.SetSaleOfferStatusParams{ID: o.ID, Status: "SUBMITTED", Status_2: "COUNTERED", DecidedAt: &now}); err != nil {
				return err
			}
			_, err = s.q.InsertSaleOffer(ctx, tx, db.InsertSaleOfferParams{ID: ids.New(), SaleID: t.ID, BuyerRefID: o.BuyerRefID, BuyerDisplayName: o.BuyerDisplayName,
				ParentOfferID: &o.ID, MadeBy: "OWNER", AmountMinor: in.CounterAmount, Currency: o.Currency, Financing: o.Financing,
				Conditions: nonNil(in.CounterConditions), ExpiresAt: now.Add(s.Policy.Domain.OfferTTL), CreatedAt: now})
		default:
			if err := s.bindingSaleChecks(ctx, d, t); err != nil {
				return err
			}
			err = s.acceptSaleOffer(ctx, t, o, settlement, in.ReservationMinor, schema.ParseDate(in.TargetClosing))
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "sale.offer_decision", AggSale, saleID, map[string]any{"offer_id": offerID, "decision": in.Decision}, nil); err != nil {
			return err
		}
		n, err := s.q.GetSale(ctx, tx, saleID)
		if err != nil {
			return err
		}
		out, err = s.saleView(ctx, n)
		return err
	})
	return out, err
}

func (s *Service) bindingSaleChecks(ctx context.Context, d authz.Decision, t db.SaleTransaction) error {
	p, err := s.q.GetProperty(ctx, mustTx(ctx), t.PropertyID)
	if err != nil {
		return err
	}
	if err := s.requireIdentity(ctx, p.OwnerAccountID, GateSale); err != nil {
		return err
	}
	return s.requireConsent(ctx, d, t.PropertyID, ConsentSale, t.ID, p.OwnerAccountID)
}

// acceptSaleOffer binds the sale to an offer and creates the closing checklist.
func (s *Service) acceptSaleOffer(ctx context.Context, t db.SaleTransaction, o db.SaleOffer, settlement string, reservation int64, target *time.Time) error {
	tx, now := mustTx(ctx), s.now()
	if t.Status != "OFFERING" {
		return apperrors.InvalidStateTransition(t.Status, "OFFER_ACCEPTED")
	}
	if reservation >= o.AmountMinor {
		return apperrors.InvalidRequest("The reservation must be less than the agreed price.")
	}
	if _, err := s.q.SetSaleOfferStatus(ctx, tx, db.SetSaleOfferStatusParams{ID: o.ID, Status: "SUBMITTED", Status_2: "ACCEPTED", DecidedAt: &now}); err != nil {
		if isNoRows(err) {
			return conflict("OFFER_NOT_AWAITING_OWNER", "The offer was already answered.")
		}
		return err
	}
	if err := s.q.SupersedeOpenOffers(ctx, tx, db.SupersedeOpenOffersParams{SaleID: t.ID, ID: o.ID, DecidedAt: &now}); err != nil {
		return err
	}
	if err := s.q.SetSaleAccepted(ctx, tx, db.SetSaleAcceptedParams{ID: t.ID, AcceptedOfferID: &o.ID, BuyerRefID: &o.BuyerRefID, AgreedPriceMinor: &o.AmountMinor,
		TargetClosingDate: target, UpdatedAt: now}); err != nil {
		return err
	}
	if err := s.saleTo(ctx, t.ID, "OFFER_ACCEPTED", "offer_accepted", ""); err != nil {
		return err
	}
	seq := int32(0)
	add := func(code, title, kind string, required bool, settle *string, amount *int64) error {
		seq++
		_, err := s.q.InsertMilestone(ctx, tx, db.InsertMilestoneParams{ID: ids.New(), SaleID: t.ID, Code: code, Title: title, Kind: kind,
			RequiredForCompletion: required, Sequence: seq * 10, Settlement: settle, AmountMinor: amount, DueDate: target, CreatedAt: now})
		return err
	}
	balance := o.AmountMinor
	if reservation > 0 {
		balance -= reservation
		if err := add("RESERVATION_DEPOSIT", "Reservation deposit paid", "PAYMENT", true, &settlement, &reservation); err != nil {
			return err
		}
	}
	for _, m := range SaleChecklists[t.SaleKind] {
		var settle *string
		var amount *int64
		if m.Kind == "PAYMENT" {
			settle, amount = &settlement, &balance
		}
		if err := add(m.Code, m.Title, m.Kind, m.Required, settle, amount); err != nil {
			return err
		}
	}
	if t.ListingID != nil {
		if av, err := s.q.GetAvailability(ctx, tx, *t.ListingID); err == nil && domain.Availability.Can(av.Status, "OFFER_ACCEPTED") {
			if _, _, err := s.transition(ctx, domain.Availability, *t.ListingID, lifecycle.Change{To: "OFFER_ACCEPTED", Command: "sale_offer_accepted",
				ExpectedVersion: av.Version, Metadata: map[string]any{"sale_id": t.ID}}); err != nil {
				return err
			}
			if l, err := s.q.GetListingForUpdate(ctx, tx, *t.ListingID); err != nil {
				return err
			} else if l.Status == "PUBLISHED" {
				if err := s.pauseTx(ctx, l, "SYSTEM", "OFFER_ACCEPTED"); err != nil {
					return err
				}
			}
		} else if err != nil && !isNoRows(err) {
			return err
		}
	}
	return s.propertyHistory(ctx, t.PropertyID, "SALE_OFFER_ACCEPTED", "Sale offer accepted", AggSale, t.ID)
}

// CustomerSaleOfferResponse is the buyer's answer to an owner counter.
type CustomerSaleOfferResponse struct {
	Decision string `json:"decision" v:"required,enum=ACCEPT|DECLINE|WITHDRAW"`
}

// CustomerRespondSaleOffer answers an owner counter or withdraws an offer.
func (s *Service) CustomerRespondSaleOffer(ctx context.Context, subj authz.Subject, offerID ids.ID, in CustomerSaleOfferResponse) (SaleOfferView, error) {
	if err := schema.Validate(in); err != nil {
		return SaleOfferView{}, err
	}
	var out SaleOfferView
	err := s.InTx(ctx, func(ctx context.Context) error {
		buyer, err := s.requireCustomer(ctx, subj)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		o, err := s.q.GetSaleOfferForUpdate(ctx, tx, offerID)
		if isNoRows(err) || (err == nil && o.BuyerRefID != buyer) {
			return notFound("sale_offer")
		}
		if err != nil {
			return err
		}
		t, err := s.q.GetSaleForUpdate(ctx, tx, o.SaleID)
		if err != nil {
			return err
		}
		if o.Status != "SUBMITTED" {
			return conflict("OFFER_NOT_OPEN", "The offer is "+o.Status+".")
		}
		switch {
		case in.Decision == "WITHDRAW" && o.MadeBy == "BUYER":
			_, err = s.q.SetSaleOfferStatus(ctx, tx, db.SetSaleOfferStatusParams{ID: o.ID, Status: "SUBMITTED", Status_2: "WITHDRAWN", DecidedAt: &now})
		case in.Decision != "WITHDRAW" && o.MadeBy == "OWNER":
			if !o.ExpiresAt.After(now) {
				return conflict("OFFER_EXPIRED", "The counter-offer has expired.")
			}
			if in.Decision == "DECLINE" {
				_, err = s.q.SetSaleOfferStatus(ctx, tx, db.SetSaleOfferStatusParams{ID: o.ID, Status: "SUBMITTED", Status_2: "DECLINED", DecidedAt: &now})
			} else {
				// The owner bound themselves (consent + identity) when countering.
				err = s.acceptSaleOffer(ctx, t, o, "OFF_PLATFORM", 0, nil)
			}
		default:
			return conflict("OFFER_ACTION_NOT_ALLOWED", "Buyers withdraw their own offers and answer the owner's counter-offers.")
		}
		if err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, t.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateTenantResponse, map[string]string{"property_title": p.Title, "subject": "sale offer " + lower(in.Decision)}); err != nil {
			return err
		}
		if err := s.record(ctx, "sale.buyer_response", AggSale, t.ID, map[string]any{"offer_id": offerID, "decision": in.Decision}, nil); err != nil {
			return err
		}
		n, err := s.q.GetSaleOffer(ctx, tx, offerID)
		out = saleOfferView(n)
		return err
	})
	return out, err
}

// ---- progression ----

var saleForward = []string{"OFFER_ACCEPTED", "DUE_DILIGENCE", "CONTRACTING", "CONDITIONS_PENDING", "CLOSING", "COMPLETED"}

// AdvanceSaleInput moves a sale to its next stage.
type AdvanceSaleInput struct {
	To string `json:"to" v:"required,enum=DUE_DILIGENCE|CONTRACTING|CONDITIONS_PENDING|CLOSING|COMPLETED"`
}

// AdvanceSale progresses the sale one stage with stage guards: contracting
// needs no unresolved blocking diligence; closing needs the documentary and
// consent milestones; completion needs every required milestone.
func (s *Service) AdvanceSale(ctx context.Context, subj authz.Subject, id ids.ID, in AdvanceSaleInput) (SaleView, error) {
	if err := schema.Validate(in); err != nil {
		return SaleView{}, err
	}
	var out SaleView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.saleFor(ctx, subj, id, authz.Do(authz.CapSaleManage))
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		if i := slices.Index(saleForward, t.Status); i < 0 || i+1 >= len(saleForward) || saleForward[i+1] != in.To {
			return apperrors.InvalidStateTransition(t.Status, in.To)
		}
		switch in.To {
		case "CONTRACTING":
			n, err := s.q.OpenBlockingDiligence(ctx, tx, id)
			if err != nil {
				return err
			}
			if n > 0 {
				return precondition("BLOCKING_DILIGENCE_OPEN", "Resolve or waive the blocking due-diligence items first.")
			}
		case "CLOSING", "COMPLETED":
			unmet, err := s.q.UnmetRequiredMilestones(ctx, tx, id)
			if err != nil {
				return err
			}
			if in.To == "CLOSING" {
				ms, err := s.q.ListMilestones(ctx, tx, id)
				if err != nil {
					return err
				}
				unmet = nil
				for _, m := range ms {
					if m.RequiredForCompletion && (m.Kind == "DOCUMENT" || m.Kind == "CONSENT") && m.Status != "SATISFIED" && m.Status != "WAIVED" {
						unmet = append(unmet, m.Code)
					}
				}
			}
			if len(unmet) > 0 {
				return apperrors.Unprocessable("MILESTONES_INCOMPLETE", "Required milestones are not satisfied.").
					WithDetails(apperrors.Detail{Field: "milestones", Code: "UNMET", Message: joinCodes(unmet)})
			}
		}
		if err := s.saleTo(ctx, id, in.To, "advance", ""); err != nil {
			return err
		}
		if in.To == "COMPLETED" {
			if err := s.completeSale(ctx, t); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "sale.advance", AggSale, id, map[string]any{"to": in.To}, nil); err != nil {
			return err
		}
		n, err := s.q.GetSale(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.saleView(ctx, n)
		return err
	})
	return out, err
}

func joinCodes(cs []string) string {
	out := ""
	for i, c := range cs {
		if i > 0 {
			out += ","
		}
		out += c
	}
	return out
}

// completeSale applies completion effects: availability SOLD, listing
// withdrawn, data room closed. Transfer of ownership authority is a separate
// guarded ownership-transfer workflow (the buyer must hold an owner account).
func (s *Service) completeSale(ctx context.Context, t db.SaleTransaction) error {
	tx, now := mustTx(ctx), s.now()
	if err := s.q.SetSaleEnded(ctx, tx, db.SetSaleEndedParams{ID: t.ID, EndReason: ptr("COMPLETED"), CompletedAt: &now, UpdatedAt: now}); err != nil {
		return err
	}
	if t.ListingID != nil {
		if av, err := s.q.GetAvailability(ctx, tx, *t.ListingID); err == nil && domain.Availability.Can(av.Status, "SOLD") {
			if _, _, err := s.transition(ctx, domain.Availability, *t.ListingID, lifecycle.Change{To: "SOLD", Command: "sale_completed", ExpectedVersion: av.Version}); err != nil {
				return err
			}
		} else if err != nil && !isNoRows(err) {
			return err
		}
		l, err := s.q.GetListingForUpdate(ctx, tx, *t.ListingID)
		if err != nil {
			return err
		}
		if l.Status == "PUBLISHED" || l.Status == "PAUSED" || l.Status == "READY_FOR_REVIEW" {
			if err := s.closeListingTx(ctx, l, "WITHDRAWN", "SYSTEM", "SOLD"); err != nil {
				return err
			}
		}
	}
	if err := s.closeDataRoom(ctx, t.ID); err != nil {
		return err
	}
	return s.propertyHistory(ctx, t.PropertyID, "SALE_COMPLETED", "Sale completed; transfer ownership authority through the ownership-transfer workflow", AggSale, t.ID)
}

func (s *Service) closeDataRoom(ctx context.Context, saleID ids.ID) error {
	tx, now := mustTx(ctx), s.now()
	dr, err := s.q.GetDataRoomBySale(ctx, tx, saleID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.q.RevokeAllDataRoomGrants(ctx, tx, db.RevokeAllDataRoomGrantsParams{DataRoomID: dr.ID, RevokedAt: &now}); err != nil {
		return err
	}
	return s.q.CloseDataRoom(ctx, tx, db.CloseDataRoomParams{ID: dr.ID, ClosedAt: &now})
}

// EndSaleInput cancels, fails, disputes or resumes a sale.
type EndSaleInput struct {
	Action     string `json:"action" v:"required,enum=CANCEL|FAIL|DISPUTE|RESUME"`
	ReasonCode string `json:"reason_code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
}

// EndSale ends a sale before completion or opens/closes a dispute.
func (s *Service) EndSale(ctx context.Context, subj authz.Subject, id ids.ID, in EndSaleInput) (SaleView, error) {
	if err := schema.Validate(in); err != nil {
		return SaleView{}, err
	}
	var out SaleView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.saleFor(ctx, subj, id, authz.Do(authz.CapSaleManage))
		if err != nil {
			return err
		}
		if err := s.endSale(ctx, t, in.Action, in.ReasonCode); err != nil {
			return err
		}
		if err := s.record(ctx, "sale."+lower(in.Action), AggSale, id, map[string]any{"reason_code": in.ReasonCode}, nil); err != nil {
			return err
		}
		n, err := s.q.GetSale(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.saleView(ctx, n)
		return err
	})
	return out, err
}

func (s *Service) endSale(ctx context.Context, t db.SaleTransaction, action, reason string) error {
	tx, now := mustTx(ctx), s.now()
	switch action {
	case "DISPUTE":
		if err := s.q.SetSaleDisputeOrigin(ctx, tx, db.SetSaleDisputeOriginParams{ID: t.ID, StatusBeforeDispute: &t.Status, UpdatedAt: now}); err != nil {
			return err
		}
		return s.saleTo(ctx, t.ID, "DISPUTED", "dispute", reason)
	case "RESUME":
		if t.Status != "DISPUTED" || t.StatusBeforeDispute == nil {
			return apperrors.InvalidStateTransition(t.Status, "RESUME")
		}
		return s.saleTo(ctx, t.ID, *t.StatusBeforeDispute, "resume", reason)
	}
	to := map[string]string{"CANCEL": "CANCELLED", "FAIL": "FAILED"}[action]
	wasAccepted := t.AcceptedOfferID != nil
	if err := s.saleTo(ctx, t.ID, to, lower(action), reason); err != nil {
		return err
	}
	if err := s.q.SetSaleEnded(ctx, tx, db.SetSaleEndedParams{ID: t.ID, EndReason: &reason, UpdatedAt: now}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE sale_offers SET status = 'WITHDRAWN', decided_at = $2, version = version + 1, updated_at = $2
		WHERE sale_id = $1 AND status IN ('SUBMITTED','COUNTERED')`, t.ID, now); err != nil {
		return err
	}
	if err := s.q.FailOpenMilestones(ctx, tx, db.FailOpenMilestonesParams{SaleID: t.ID, UpdatedAt: now}); err != nil {
		return err
	}
	if err := s.closeDataRoom(ctx, t.ID); err != nil {
		return err
	}
	if wasAccepted && t.ListingID != nil {
		if av, err := s.q.GetAvailability(ctx, tx, *t.ListingID); err == nil && av.Status == "OFFER_ACCEPTED" {
			if _, _, err := s.transition(ctx, domain.Availability, *t.ListingID, lifecycle.Change{To: "AVAILABLE", Command: "sale_ended",
				ExpectedVersion: av.Version, Reason: reason}); err != nil {
				return err
			}
		} else if err != nil && !isNoRows(err) {
			return err
		}
	}
	return s.propertyHistory(ctx, t.PropertyID, "SALE_ENDED", "Sale "+lower(to)+" ("+reason+")", AggSale, t.ID)
}

// ---- milestones ----

// MilestoneInput records progress on a closing milestone (or adds one).
type MilestoneInput struct {
	Code             string   `json:"code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
	Action           string   `json:"action" v:"required,enum=EVIDENCE|WAIVE|ADD"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=20"`
	ReasonCode       *string  `json:"reason_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
	Title            *string  `json:"title,omitempty" v:"max=200" doc:"ADD only"`
	Kind             *string  `json:"kind,omitempty" v:"enum=DOCUMENT|CONSENT|HANDOVER|PERFECTION|OTHER" doc:"ADD only (payment milestones come from the accepted offer)"`
	Required         *bool    `json:"required_for_completion,omitempty" doc:"ADD only"`
	Attestation      *string  `json:"attestation,omitempty" v:"enum=OWNER|PROFESSIONAL|PROVIDER" doc:"Who produced the evidence (default OWNER)"`
}

// MediaRefs lists referenced media.
func (in MilestoneInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// RecordMilestone records evidence, waives a non-payment milestone or adds a
// custom milestone. Off-platform payments need the buyer's confirmation;
// platform payments are only ever satisfied by payment-service.
func (s *Service) RecordMilestone(ctx context.Context, subj authz.Subject, saleID ids.ID, in MilestoneInput, ev Evidence) (SaleView, error) {
	if err := schema.Validate(in); err != nil {
		return SaleView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return SaleView{}, err
	}
	var out SaleView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.saleFor(ctx, subj, saleID, authz.Do(authz.CapSaleManage))
		if err != nil {
			return err
		}
		if t.AcceptedOfferID == nil || slices.Contains([]string{"COMPLETED", "CANCELLED", "FAILED"}, t.Status) {
			return conflict("SALE_NOT_IN_CLOSING", "Milestones apply to an accepted, running sale.")
		}
		tx, now := mustTx(ctx), s.now()
		if in.Action == "ADD" {
			if in.Title == nil || in.Kind == nil {
				return apperrors.InvalidRequest("title and kind are required to add a milestone.")
			}
			ms, err := s.q.ListMilestones(ctx, tx, saleID)
			if err != nil {
				return err
			}
			_, err = s.q.InsertMilestone(ctx, tx, db.InsertMilestoneParams{ID: ids.New(), SaleID: saleID, Code: in.Code, Title: *in.Title, Kind: *in.Kind,
				RequiredForCompletion: in.Required != nil && *in.Required, Sequence: int32(len(ms)+1) * 10, DueDate: t.TargetClosingDate, CreatedAt: now})
			if isUnique(err, "closing_milestones_uniq") {
				return conflict("MILESTONE_EXISTS", "A milestone with this code exists.")
			}
			if err != nil {
				return err
			}
		} else {
			m, err := s.q.GetMilestoneForUpdate(ctx, tx, db.GetMilestoneForUpdateParams{SaleID: saleID, Code: in.Code})
			if isNoRows(err) {
				return notFound("milestone")
			}
			if err != nil {
				return err
			}
			if m.Status != "PENDING" && m.Status != "AWAITING_CONFIRMATION" {
				return conflict("MILESTONE_FINAL", "The milestone is "+m.Status+".")
			}
			p := db.UpdateMilestoneParams{ID: m.ID, Status: m.Status, EvidenceMediaIds: nonNilIDs(m.EvidenceMediaIds), AttestedBy: m.AttestedBy,
				BuyerConfirmedAt: m.BuyerConfirmedAt, SatisfiedAt: m.SatisfiedAt, WaivedReason: m.WaivedReason, PaidMinor: m.PaidMinor, UpdatedAt: now}
			if in.Action == "WAIVE" {
				if m.Kind == "PAYMENT" {
					return precondition("PAYMENT_MILESTONE_NOT_WAIVABLE", "Payment milestones are settled, never waived; cancel or renegotiate instead.")
				}
				if in.ReasonCode == nil {
					return apperrors.InvalidRequest("reason_code is required to waive.")
				}
				p.Status, p.WaivedReason = "WAIVED", in.ReasonCode
			} else {
				if len(in.EvidenceMediaIDs) == 0 {
					return precondition("EVIDENCE_REQUIRED", "Attach the evidence for this milestone.")
				}
				if m.Kind == "PAYMENT" && deref(m.Settlement) == "PLATFORM" {
					return precondition("PAYMENT_SERVICE_AUTHORITATIVE", "Platform payments are confirmed by payment-service only.")
				}
				p.EvidenceMediaIds = append(p.EvidenceMediaIds, in.EvidenceMediaIDs...)
				if m.Kind == "PAYMENT" {
					p.Status, p.AttestedBy = "AWAITING_CONFIRMATION", ptr("OWNER")
				} else {
					att := "OWNER"
					if in.Attestation != nil {
						att = *in.Attestation
					}
					p.Status, p.AttestedBy, p.SatisfiedAt = "SATISFIED", &att, &now
				}
			}
			if _, err := s.q.UpdateMilestone(ctx, tx, p); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "sale.milestone."+lower(in.Action), AggSale, saleID, map[string]any{"code": in.Code}, nil); err != nil {
			return err
		}
		n, err := s.q.GetSale(ctx, tx, saleID)
		if err != nil {
			return err
		}
		out, err = s.saleView(ctx, n)
		return err
	})
	return out, err
}

// ---- due diligence ----

// DiligenceInput raises a due-diligence item.
type DiligenceInput struct {
	Category                   string   `json:"category" v:"required,enum=LEGAL|SURVEY|VALUATION|INSPECTION|TITLE_SEARCH|CHARTING|ENCUMBRANCE|PLANNING|OTHER"`
	Title                      string   `json:"title" v:"required,max=200"`
	Blocking                   bool     `json:"blocking"`
	Finding                    *string  `json:"finding,omitempty" v:"max=4000"`
	EvidenceMediaIDs           []ids.ID `json:"evidence_media_ids,omitempty" v:"max=20"`
	ProfessionalAppointmentRef *ids.ID  `json:"professional_appointment_ref,omitempty"`
}

// MediaRefs lists referenced media.
func (in DiligenceInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// CustomerMediaRefs lists referenced customer media.
func (in DiligenceInput) CustomerMediaRefs() []ids.ID { return in.EvidenceMediaIDs }

func (s *Service) insertDiligence(ctx context.Context, t db.SaleTransaction, by string, in DiligenceInput) (db.DueDiligenceItem, error) {
	if slices.Contains([]string{"COMPLETED", "CANCELLED", "FAILED"}, t.Status) {
		return db.DueDiligenceItem{}, conflict("SALE_ENDED", "The sale has ended.")
	}
	return s.q.InsertDueDiligenceItem(ctx, mustTx(ctx), db.InsertDueDiligenceItemParams{ID: ids.New(), SaleID: t.ID, Category: in.Category, Title: in.Title,
		RaisedBy: by, Blocking: in.Blocking, Finding: in.Finding, EvidenceMediaIds: nonNilIDs(in.EvidenceMediaIDs),
		ProfessionalAppointmentRef: in.ProfessionalAppointmentRef, CreatedAt: s.now()})
}

// AddDiligenceItem raises an owner-side due-diligence item.
func (s *Service) AddDiligenceItem(ctx context.Context, subj authz.Subject, saleID ids.ID, in DiligenceInput, ev Evidence) (DiligenceView, error) {
	if err := schema.Validate(in); err != nil {
		return DiligenceView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return DiligenceView{}, err
	}
	var out DiligenceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.saleFor(ctx, subj, saleID, authz.Do(authz.CapSaleManage))
		if err != nil {
			return err
		}
		d, err := s.insertDiligence(ctx, t, "OWNER", in)
		if err != nil {
			return err
		}
		out = diligenceView(d)
		return s.record(ctx, "sale.diligence.add", AggSale, saleID, map[string]any{"item_id": d.ID, "category": in.Category}, nil)
	})
	return out, err
}

// DiligenceUpdate progresses an item (If-Match).
type DiligenceUpdate struct {
	Status           string   `json:"status" v:"required,enum=OPEN|IN_PROGRESS|ISSUE_FOUND|RESOLVED|WAIVED"`
	Blocking         bool     `json:"blocking"`
	Finding          *string  `json:"finding,omitempty" v:"max=4000"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=20" doc:"Added to the item's evidence"`
	ResolutionNote   *string  `json:"resolution_note,omitempty" v:"max=2000"`
}

// MediaRefs lists referenced media.
func (in DiligenceUpdate) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// UpdateDiligenceItem updates an item; resolution needs a note.
func (s *Service) UpdateDiligenceItem(ctx context.Context, subj authz.Subject, saleID, itemID ids.ID, expected int64, in DiligenceUpdate, ev Evidence) (DiligenceView, error) {
	if err := schema.Validate(in); err != nil {
		return DiligenceView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return DiligenceView{}, err
	}
	if (in.Status == "RESOLVED" || in.Status == "WAIVED") && in.ResolutionNote == nil {
		return DiligenceView{}, apperrors.InvalidRequest("A resolution note is required to resolve or waive an item.")
	}
	var out DiligenceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, _, err := s.saleFor(ctx, subj, saleID, authz.Do(authz.CapSaleManage)); err != nil {
			return err
		}
		tx := mustTx(ctx)
		d, err := s.q.GetDueDiligenceItem(ctx, tx, db.GetDueDiligenceItemParams{ID: itemID, SaleID: saleID})
		if isNoRows(err) {
			return notFound("due_diligence_item")
		}
		if err != nil {
			return err
		}
		finding := d.Finding
		if in.Finding != nil {
			finding = in.Finding
		}
		n, err := s.q.UpdateDueDiligenceItem(ctx, tx, db.UpdateDueDiligenceItemParams{ID: itemID, Version: expected, Status: in.Status, Blocking: in.Blocking,
			Finding: finding, EvidenceMediaIds: append(nonNilIDs(d.EvidenceMediaIds), in.EvidenceMediaIDs...), ResolutionNote: in.ResolutionNote, UpdatedAt: s.now()})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		out = diligenceView(n)
		return s.record(ctx, "sale.diligence.update", AggSale, saleID, map[string]any{"item_id": itemID, "status": in.Status}, nil)
	})
	return out, err
}

// ---- data room ----

// DataRoomView is the sale's data room.
type DataRoomView struct {
	ID        ids.ID              `json:"id"`
	SaleID    ids.ID              `json:"sale_id"`
	Status    string              `json:"status"`
	Documents []DataRoomDocument  `json:"documents"`
	Grants    []DataRoomGrantView `json:"grants,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
	ClosedAt  *time.Time          `json:"closed_at"`
}

// DataRoomDocument is a document in the data room.
type DataRoomDocument struct {
	ID        ids.ID    `json:"id"`
	MediaID   ids.ID    `json:"media_id"`
	Category  string    `json:"category"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// DataRoomGrantView is an access grant.
type DataRoomGrantView struct {
	ID             ids.ID     `json:"id"`
	GranteeDomain  string     `json:"grantee_domain"`
	GranteeRefID   ids.ID     `json:"grantee_ref_id"`
	GranteeLabel   string     `json:"grantee_label"`
	Purpose        string     `json:"purpose"`
	Categories     []string   `json:"categories" doc:"Empty = every category"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at"`
	AccessCount    int32      `json:"access_count"`
	LastAccessedAt *time.Time `json:"last_accessed_at"`
}

func (s *Service) dataRoomView(ctx context.Context, dr db.DataRoom, withGrants bool, categories []string) (DataRoomView, error) {
	q := s.dbq(ctx)
	v := DataRoomView{ID: dr.ID, SaleID: dr.SaleID, Status: dr.Status, CreatedAt: dr.CreatedAt, ClosedAt: dr.ClosedAt, Documents: []DataRoomDocument{}}
	docs, err := s.q.ListDataRoomDocuments(ctx, q, dr.ID)
	if err != nil {
		return v, err
	}
	for _, d := range docs {
		if len(categories) > 0 && !slices.Contains(categories, d.Category) {
			continue
		}
		v.Documents = append(v.Documents, DataRoomDocument{ID: d.ID, MediaID: d.MediaID, Category: d.Category, Title: d.Title, CreatedAt: d.CreatedAt})
	}
	if withGrants {
		v.Grants = []DataRoomGrantView{}
		gs, err := s.q.ListDataRoomGrants(ctx, q, dr.ID)
		if err != nil {
			return v, err
		}
		for _, g := range gs {
			v.Grants = append(v.Grants, DataRoomGrantView{ID: g.ID, GranteeDomain: g.GranteeDomain, GranteeRefID: g.GranteeRefID, GranteeLabel: g.GranteeLabel,
				Purpose: g.Purpose, Categories: nonNil(g.Categories), ExpiresAt: g.ExpiresAt, RevokedAt: g.RevokedAt, AccessCount: g.AccessCount,
				LastAccessedAt: g.LastAccessedAt})
		}
	}
	return v, nil
}

// OpenDataRoom opens (idempotently) the sale's data room.
func (s *Service) OpenDataRoom(ctx context.Context, subj authz.Subject, saleID ids.ID) (DataRoomView, error) {
	var out DataRoomView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.saleFor(ctx, subj, saleID, authz.Do(authz.CapSaleManage))
		if err != nil {
			return err
		}
		if slices.Contains([]string{"COMPLETED", "CANCELLED", "FAILED"}, t.Status) {
			return conflict("SALE_ENDED", "The sale has ended.")
		}
		tx := mustTx(ctx)
		if _, err := s.q.InsertDataRoom(ctx, tx, db.InsertDataRoomParams{ID: ids.New(), SaleID: t.ID, PropertyID: t.PropertyID, CreatedAt: s.now()}); err != nil && !isNoRows(err) {
			return err
		}
		dr, err := s.q.GetDataRoomBySale(ctx, tx, saleID)
		if err != nil {
			return err
		}
		out, err = s.dataRoomView(ctx, dr, true, nil)
		return err
	})
	return out, err
}

// GetDataRoom returns the data room with grants (owner).
func (s *Service) GetDataRoom(ctx context.Context, subj authz.Subject, saleID ids.ID) (DataRoomView, error) {
	if _, _, err := s.saleFor(ctx, subj, saleID, authz.Read(authz.CapSaleManage)); err != nil {
		return DataRoomView{}, err
	}
	dr, err := s.q.GetDataRoomBySale(ctx, s.dbq(ctx), saleID)
	if isNoRows(err) {
		return DataRoomView{}, notFound("data_room")
	}
	if err != nil {
		return DataRoomView{}, err
	}
	return s.dataRoomView(ctx, dr, true, nil)
}

func (s *Service) openRoom(ctx context.Context, subj authz.Subject, saleID ids.ID) (db.DataRoom, error) {
	if _, _, err := s.saleFor(ctx, subj, saleID, authz.Do(authz.CapSaleManage)); err != nil {
		return db.DataRoom{}, err
	}
	dr, err := s.q.GetDataRoomBySale(ctx, mustTx(ctx), saleID)
	if isNoRows(err) {
		return dr, conflict("DATA_ROOM_NOT_OPEN", "Open the data room first.")
	}
	if err == nil && dr.Status != "OPEN" {
		return dr, conflict("DATA_ROOM_CLOSED", "The data room is closed.")
	}
	return dr, err
}

// DataRoomDocumentInput adds a document.
type DataRoomDocumentInput struct {
	MediaID  ids.ID `json:"media_id" v:"required"`
	Category string `json:"category" v:"required,enum=TITLE|SURVEY|PLANNING|VALUATION|INSPECTION|TAX|LEGAL|FINANCIAL|OTHER"`
	Title    string `json:"title" v:"required,max=200"`
}

// MediaRefs lists referenced media.
func (in DataRoomDocumentInput) MediaRefs() []ids.ID { return []ids.ID{in.MediaID} }

// AddDataRoomDocument adds an owner document to the data room.
func (s *Service) AddDataRoomDocument(ctx context.Context, subj authz.Subject, saleID ids.ID, in DataRoomDocumentInput, ev Evidence) (DataRoomView, error) {
	if err := schema.Validate(in); err != nil {
		return DataRoomView{}, err
	}
	if err := ev.require(in.MediaID); err != nil {
		return DataRoomView{}, err
	}
	var out DataRoomView
	err := s.InTx(ctx, func(ctx context.Context) error {
		dr, err := s.openRoom(ctx, subj, saleID)
		if err != nil {
			return err
		}
		if _, err := s.q.InsertDataRoomDocument(ctx, mustTx(ctx), db.InsertDataRoomDocumentParams{ID: ids.New(), DataRoomID: dr.ID, MediaID: in.MediaID,
			Category: in.Category, Title: in.Title, CreatedAt: s.now()}); isNoRows(err) {
			return conflict("DOCUMENT_ALREADY_ADDED", "The document is already in the data room.")
		} else if err != nil {
			return err
		}
		if err := s.record(ctx, "sale.data_room.document", AggSale, saleID, map[string]any{"media_id": in.MediaID, "category": in.Category}, nil); err != nil {
			return err
		}
		out, err = s.dataRoomView(ctx, dr, true, nil)
		return err
	})
	return out, err
}

// RemoveDataRoomDocument removes a document from the room (the file stays in media service).
func (s *Service) RemoveDataRoomDocument(ctx context.Context, subj authz.Subject, saleID, docID ids.ID) (DataRoomView, error) {
	var out DataRoomView
	err := s.InTx(ctx, func(ctx context.Context) error {
		dr, err := s.openRoom(ctx, subj, saleID)
		if err != nil {
			return err
		}
		n, err := s.q.RemoveDataRoomDocument(ctx, mustTx(ctx), db.RemoveDataRoomDocumentParams{ID: docID, DataRoomID: dr.ID, RemovedAt: ptr(s.now())})
		if err != nil {
			return err
		}
		if n == 0 {
			return notFound("data_room_document")
		}
		if err := s.record(ctx, "sale.data_room.document_removed", AggSale, saleID, map[string]any{"document_id": docID}, nil); err != nil {
			return err
		}
		out, err = s.dataRoomView(ctx, dr, true, nil)
		return err
	})
	return out, err
}

// DataRoomGrantInput grants time/purpose-limited access.
type DataRoomGrantInput struct {
	GranteeDomain string   `json:"grantee_domain" v:"required,enum=marketplace|professional-services"`
	GranteeRefID  ids.ID   `json:"grantee_ref_id" v:"required"`
	GranteeLabel  string   `json:"grantee_label" v:"required,max=120"`
	Purpose       string   `json:"purpose" v:"required,pattern=[a-z][a-z0-9_.:-]{2,99}"`
	Categories    []string `json:"categories,omitempty" v:"max=9,enum=TITLE|SURVEY|PLANNING|VALUATION|INSPECTION|TAX|LEGAL|FINANCIAL|OTHER"`
	ExpiresInDays int32    `json:"expires_in_days" v:"required,min=1,max=30"`
}

// GrantDataRoomAccess grants least-privilege access to a buyer or professional.
func (s *Service) GrantDataRoomAccess(ctx context.Context, subj authz.Subject, saleID ids.ID, in DataRoomGrantInput) (DataRoomView, error) {
	if _, err := s.authorize(ctx, subj, "owner.sensitive_grant", ids.Nil); err != nil {
		return DataRoomView{}, err
	}
	if err := schema.Validate(in); err != nil {
		return DataRoomView{}, err
	}
	var out DataRoomView
	err := s.InTx(ctx, func(ctx context.Context) error {
		dr, err := s.openRoom(ctx, subj, saleID)
		if err != nil {
			return err
		}
		now := s.now()
		if _, err := s.q.InsertDataRoomGrant(ctx, mustTx(ctx), db.InsertDataRoomGrantParams{ID: ids.New(), DataRoomID: dr.ID, GranteeDomain: in.GranteeDomain,
			GranteeRefID: in.GranteeRefID, GranteeLabel: in.GranteeLabel, Purpose: in.Purpose, Categories: nonNil(in.Categories),
			ExpiresAt: now.Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour), CreatedAt: now}); err != nil {
			return err
		}
		if err := s.record(ctx, "sale.data_room.grant", AggSale, saleID, map[string]any{"grantee_domain": in.GranteeDomain, "purpose": in.Purpose,
			"expires_in_days": in.ExpiresInDays}, nil); err != nil {
			return err
		}
		out, err = s.dataRoomView(ctx, dr, true, nil)
		return err
	})
	return out, err
}

// RevokeDataRoomGrant revokes a grant immediately.
func (s *Service) RevokeDataRoomGrant(ctx context.Context, subj authz.Subject, saleID, grantID ids.ID) (DataRoomView, error) {
	var out DataRoomView
	err := s.InTx(ctx, func(ctx context.Context) error {
		dr, err := s.openRoom(ctx, subj, saleID)
		if err != nil {
			return err
		}
		n, err := s.q.RevokeDataRoomGrant(ctx, mustTx(ctx), db.RevokeDataRoomGrantParams{ID: grantID, DataRoomID: dr.ID, RevokedAt: ptr(s.now())})
		if err != nil {
			return err
		}
		if n == 0 {
			return notFound("data_room_grant")
		}
		if err := s.record(ctx, "sale.data_room.revoke", AggSale, saleID, map[string]any{"grant_id": grantID}, nil); err != nil {
			return err
		}
		out, err = s.dataRoomView(ctx, dr, true, nil)
		return err
	})
	return out, err
}

// granteeGrant authorizes a grantee (marketplace buyer or professional) on a room.
func (s *Service) granteeGrant(ctx context.Context, subj authz.Subject, roomID ids.ID) (db.DataRoom, db.DataRoomGrant, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalDataRoomAccess, authz.Resource{}); err != nil {
		return db.DataRoom{}, db.DataRoomGrant{}, err
	}
	dr, err := s.q.GetDataRoom(ctx, s.dbq(ctx), roomID)
	if isNoRows(err) {
		return dr, db.DataRoomGrant{}, notFound("data_room")
	}
	if err != nil {
		return dr, db.DataRoomGrant{}, err
	}
	g, err := s.q.ActiveDataRoomGrant(ctx, s.dbq(ctx), db.ActiveDataRoomGrantParams{DataRoomID: roomID, GranteeDomain: string(subj.ActorDomain),
		GranteeRefID: subj.ActorID, Now: s.now()})
	if isNoRows(err) || (err == nil && dr.Status != "OPEN") {
		return dr, g, notFound("data_room")
	}
	return dr, g, err
}

// GranteeListDataRoom lists the documents a grantee may access.
func (s *Service) GranteeListDataRoom(ctx context.Context, subj authz.Subject, roomID ids.ID) (DataRoomView, error) {
	dr, g, err := s.granteeGrant(ctx, subj, roomID)
	if err != nil {
		return DataRoomView{}, err
	}
	return s.dataRoomView(ctx, dr, false, g.Categories)
}

// DataRoomDownload is a short-lived download URL.
type DataRoomDownload struct {
	DocumentID ids.ID    `json:"document_id"`
	URL        string    `json:"url"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// GranteeDownloadURL returns a short-lived URL for one in-scope document;
// every access is logged and counted.
func (s *Service) GranteeDownloadURL(ctx context.Context, subj authz.Subject, roomID, docID ids.ID) (DataRoomDownload, error) {
	if s.Media == nil {
		return DataRoomDownload{}, unavailable("Data-room downloads", "media-document-service")
	}
	var doc db.DataRoomDocument
	var purpose string
	var grantID ids.ID
	var validUntil time.Time
	err := s.InTx(ctx, func(ctx context.Context) error {
		_, g, err := s.granteeGrant(ctx, subj, roomID)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		doc, err = s.q.GetDataRoomDocument(ctx, tx, db.GetDataRoomDocumentParams{ID: docID, DataRoomID: roomID})
		if isNoRows(err) || (err == nil && len(g.Categories) > 0 && !slices.Contains(g.Categories, doc.Category)) {
			return notFound("data_room_document")
		}
		if err != nil {
			return err
		}
		purpose = g.Purpose
		grantID = g.ID
		validUntil = g.ExpiresAt
		if err := s.q.TouchDataRoomGrant(ctx, tx, db.TouchDataRoomGrantParams{ID: g.ID, LastAccessedAt: &now}); err != nil {
			return err
		}
		if err := s.q.InsertDataRoomAccess(ctx, tx, db.InsertDataRoomAccessParams{ID: ids.New(), GrantID: g.ID, DocumentID: docID, AccessedAt: now}); err != nil {
			return err
		}
		return s.record(ctx, "sale.data_room.access", "data_room", roomID, map[string]any{"grant_id": g.ID, "document_id": docID},
			map[string]any{"privileged_read": true})
	})
	if err != nil {
		return DataRoomDownload{}, err
	}
	// This is an authorized Owner-domain proxy read, not a delegation of the
	// buyer's identity to Media. Owner owns this document and enforces the
	// recipient's active purpose grant; both services retain the same trace.
	// Keep the original actor on Owner audit records and clear it only for
	// the downstream domain-service read, which Media explicitly supports.
	proxyCtx := requestcontext.WithActor(ctx, requestcontext.Actor{})
	m, err := s.Media.Get(proxyCtx, doc.MediaID)
	if err != nil {
		return DataRoomDownload{}, downstream("media-document-service", err)
	}
	if m.OwnerDomain != string(platform.DomainIndividualOwner) || m.Status != "READY" {
		return DataRoomDownload{}, precondition("DATA_ROOM_MEDIA_UNUSABLE", "The referenced Owner document is not ready for sharing.")
	}
	dl, err := s.Media.DownloadURL(proxyCtx, doc.MediaID, purpose)
	if err != nil {
		return DataRoomDownload{}, downstream("media-document-service", err)
	}
	if dl.ExpiresAt.After(validUntil) {
		return DataRoomDownload{}, precondition("PURPOSE_WINDOW_TOO_SHORT", "The media URL would outlive this purpose grant. A new authorized grant is required.")
	}
	// Revocation or room closure during the downstream call must prevent a
	// newly returned URL. Previously issued URLs retain Media's bounded TTL.
	if err := s.InTx(ctx, func(ctx context.Context) error {
		_, g, err := s.granteeGrant(ctx, subj, roomID)
		if err != nil {
			return err
		}
		if g.ID != grantID {
			return notFound("data_room_grant")
		}
		cur, err := s.q.GetDataRoomDocument(ctx, mustTx(ctx), db.GetDataRoomDocumentParams{ID: docID, DataRoomID: roomID})
		if err != nil || cur.MediaID != doc.MediaID || (len(g.Categories) > 0 && !slices.Contains(g.Categories, cur.Category)) {
			return notFound("data_room_document")
		}
		return s.record(ctx, "sale.data_room.download_confirmed", "data_room", roomID, map[string]any{"grant_id": grantID, "document_id": docID, "purpose": purpose}, map[string]any{"privileged_read": true})
	}); err != nil {
		return DataRoomDownload{}, err
	}
	return DataRoomDownload{DocumentID: docID, URL: dl.URL, ExpiresAt: dl.ExpiresAt}, nil
}

// ---- buyer (marketplace) ----

func (s *Service) buyerSale(ctx context.Context, subj authz.Subject, id ids.ID, lock bool) (db.SaleTransaction, ids.ID, error) {
	buyer, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return db.SaleTransaction{}, ids.Nil, err
	}
	t, err := s.q.GetSale(ctx, s.dbq(ctx), id)
	if isNoRows(err) || (err == nil && (t.BuyerRefID == nil || *t.BuyerRefID != buyer)) {
		return t, buyer, notFound(AggSale)
	}
	if err == nil && lock {
		t, err = s.q.GetSaleForUpdate(ctx, mustTx(ctx), id)
	}
	return t, buyer, err
}

// CustomerGetSale returns the accepted buyer's view of a sale.
func (s *Service) CustomerGetSale(ctx context.Context, subj authz.Subject, id ids.ID) (SaleView, error) {
	t, _, err := s.buyerSale(ctx, subj, id, false)
	if err != nil {
		return SaleView{}, err
	}
	v, err := s.saleView(ctx, t)
	v.Offers = slices.DeleteFunc(v.Offers, func(o SaleOfferView) bool { return o.BuyerRefID != *t.BuyerRefID })
	return v, err
}

// CustomerSaleAction is a buyer action on an accepted sale.
type CustomerSaleAction struct {
	Action        string  `json:"action" v:"required,enum=CONFIRM_MILESTONE|WITHDRAW|DISPUTE"`
	MilestoneCode *string `json:"milestone_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
	ReasonCode    *string `json:"reason_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
}

// CustomerActOnSale lets the buyer confirm an off-platform payment, withdraw
// from the purchase (the sale FAILS) or raise a dispute.
func (s *Service) CustomerActOnSale(ctx context.Context, subj authz.Subject, id ids.ID, in CustomerSaleAction) (SaleView, error) {
	if err := schema.Validate(in); err != nil {
		return SaleView{}, err
	}
	var out SaleView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.buyerSale(ctx, subj, id, true)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		switch in.Action {
		case "CONFIRM_MILESTONE":
			if in.MilestoneCode == nil {
				return apperrors.InvalidRequest("milestone_code is required.")
			}
			m, err := s.q.GetMilestoneForUpdate(ctx, tx, db.GetMilestoneForUpdateParams{SaleID: id, Code: *in.MilestoneCode})
			if isNoRows(err) {
				return notFound("milestone")
			}
			if err != nil {
				return err
			}
			if m.Status != "AWAITING_CONFIRMATION" {
				return conflict("MILESTONE_NOT_AWAITING_CONFIRMATION", "The milestone is not awaiting your confirmation.")
			}
			if _, err := s.q.UpdateMilestone(ctx, tx, db.UpdateMilestoneParams{ID: m.ID, Status: "SATISFIED", EvidenceMediaIds: nonNilIDs(m.EvidenceMediaIds),
				AttestedBy: ptr("OWNER_AND_BUYER"), BuyerConfirmedAt: &now, SatisfiedAt: &now, PaidMinor: deref(m.AmountMinor), UpdatedAt: now}); err != nil {
				return err
			}
		case "WITHDRAW":
			if err := s.endSale(ctx, t, "FAIL", "BUYER_WITHDREW"); err != nil {
				return err
			}
		case "DISPUTE":
			if in.ReasonCode == nil {
				return apperrors.InvalidRequest("reason_code is required.")
			}
			if err := s.endSale(ctx, t, "DISPUTE", *in.ReasonCode); err != nil {
				return err
			}
		}
		p, err := s.q.GetProperty(ctx, tx, t.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateTenantResponse, map[string]string{"property_title": p.Title, "subject": "buyer " + lower(in.Action)}); err != nil {
			return err
		}
		if err := s.record(ctx, "sale.buyer_action", AggSale, id, map[string]any{"action": in.Action}, nil); err != nil {
			return err
		}
		n, err := s.q.GetSale(ctx, tx, id)
		if err != nil {
			return err
		}
		out = saleBase(n)
		return nil
	})
	return out, err
}

// CustomerRaiseDiligence lets the accepted buyer raise a due-diligence item.
func (s *Service) CustomerRaiseDiligence(ctx context.Context, subj authz.Subject, saleID ids.ID, in DiligenceInput, ev Evidence) (DiligenceView, error) {
	if err := schema.Validate(in); err != nil {
		return DiligenceView{}, err
	}
	if err := ev.require(in.CustomerMediaRefs()...); err != nil {
		return DiligenceView{}, err
	}
	var out DiligenceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.buyerSale(ctx, subj, saleID, true)
		if err != nil {
			return err
		}
		d, err := s.insertDiligence(ctx, t, "BUYER", in)
		if err != nil {
			return err
		}
		out = diligenceView(d)
		return s.record(ctx, "sale.diligence.buyer", AggSale, saleID, map[string]any{"item_id": d.ID}, nil)
	})
	return out, err
}

// ---- jobs (§11) ----

// RunSaleJobs expires stale offers and LOIs, requests payment intents for
// platform milestones, reconciles them by read-back and cancels intents of
// failed milestones.
func (s *Service) RunSaleJobs(ctx context.Context, batch int32) error {
	now := s.now()
	offers, err := s.q.DueSaleOfferExpiries(ctx, s.Pool, db.DueSaleOfferExpiriesParams{ExpiresAt: now, Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range offers {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			_, err := s.q.SetSaleOfferStatus(ctx, mustTx(ctx), db.SetSaleOfferStatusParams{ID: id, Status: "SUBMITTED", Status_2: "EXPIRED", DecidedAt: &now})
			if isNoRows(err) {
				return nil
			}
			return err
		}); err != nil {
			return err
		}
	}
	lois, err := s.q.DueLOIExpiries(ctx, s.Pool, db.DueLOIExpiriesParams{RespondBy: now, Limit: batch})
	if err != nil {
		return err
	}
	for _, id := range lois {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			_, _, err := s.transition(ctx, domain.CommercialLOI, id, lifecycle.Change{To: "EXPIRED", Command: "expire", Reason: "DEADLINE_PASSED"})
			return err
		}); err != nil && !benign(err) {
			return err
		}
	}
	if s.Payment == nil {
		return nil
	}
	need, err := s.q.MilestonesNeedingIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range need {
		if err := s.requestMilestoneIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "sale payment intent deferred", "milestone_id", id, "error", err)
		}
	}
	recon, err := s.q.MilestonesToReconcile(ctx, s.Pool, db.MilestonesToReconcileParams{CheckedBefore: now.Add(-s.Policy.Domain.PaymentReconcileAfter), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range recon {
		if err := s.reconcileMilestone(ctx, id, false); err != nil {
			s.Logger.WarnContext(ctx, "sale payment read-back deferred", "milestone_id", id, "error", err)
		}
	}
	cancel, err := s.q.MilestonesToCancel(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range cancel {
		if err := s.reconcileMilestone(ctx, id, true); err != nil {
			s.Logger.WarnContext(ctx, "sale payment cancellation deferred", "milestone_id", id, "error", err)
		}
	}
	return nil
}

func (s *Service) requestMilestoneIntent(ctx context.Context, id ids.ID) error {
	m, err := s.q.GetMilestoneByID(ctx, s.Pool, id)
	if err != nil || m.PaymentIntentRef != nil {
		return err
	}
	t, err := s.q.GetSale(ctx, s.Pool, m.SaleID)
	if err != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, s.Pool, t.PropertyID)
	if err != nil {
		return err
	}
	due := s.now()
	if m.DueDate != nil {
		due = *m.DueDate
	}
	in, err := s.Payment.CreateIntent(ctx, payment.IntentRequest{Purpose: payment.PurposeSaleMilestone, ReferenceType: "closing_milestone", ReferenceID: m.ID,
		PayeeOwnerAccountID: p.OwnerAccountID, PayerDomain: string(platform.DomainMarketplace), PayerRefID: t.BuyerRefID, AmountMinor: deref(m.AmountMinor),
		Currency: t.Currency, DueDate: due, Escrow: true}, "closing_milestone:"+m.ID.String())
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		now := s.now()
		if err := s.q.SetMilestoneIntent(ctx, mustTx(ctx), db.SetMilestoneIntentParams{ID: id, PaymentIntentRef: &in.Ref, LastPaymentStatus: &in.Status,
			LastCheckedAt: &now}); err != nil {
			return err
		}
		return s.projectMilestoneIntent(ctx, id, in)
	})
}

func (s *Service) reconcileMilestone(ctx context.Context, id ids.ID, cancel bool) error {
	m, err := s.q.GetMilestoneByID(ctx, s.Pool, id)
	if err != nil || m.PaymentIntentRef == nil {
		return err
	}
	var in payment.Intent
	if cancel {
		in, err = s.Payment.CancelIntent(ctx, *m.PaymentIntentRef, "sale ended before payment")
	} else {
		in, err = s.Payment.GetIntent(ctx, *m.PaymentIntentRef)
	}
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error { return s.projectMilestoneIntent(ctx, id, in) })
}

// projectMilestoneIntent applies payment truth to a platform milestone.
func (s *Service) projectMilestoneIntent(ctx context.Context, id ids.ID, in payment.Intent) error {
	if fresh, err := s.intentIsFresh(ctx, in); !fresh || err != nil {
		return err
	}
	tx, now := mustTx(ctx), s.now()
	if err := s.q.SetMilestonePayment(ctx, tx, db.SetMilestonePaymentParams{ID: id, PaidMinor: in.PaidMinor, LastPaymentStatus: &in.Status, LastCheckedAt: &now}); err != nil {
		return err
	}
	m, err := s.q.GetMilestoneByID(ctx, tx, id)
	if err != nil {
		return err
	}
	if m.Status == "FAILED" && in.PaidMinor > 0 {
		return s.record(ctx, "sale.milestone.payment_after_end", AggSale, m.SaleID, map[string]any{"code": m.Code, "paid_minor": in.PaidMinor},
			map[string]any{"attention": "money received after the sale ended; refund through payment-service"})
	}
	if m.Status != "PENDING" || (in.Status != payment.IntentSucceeded && in.Status != payment.IntentHeld) || in.PaidMinor < deref(m.AmountMinor) {
		return nil
	}
	_, err = s.q.UpdateMilestone(ctx, tx, db.UpdateMilestoneParams{ID: id, Status: "SATISFIED", EvidenceMediaIds: nonNilIDs(m.EvidenceMediaIds),
		AttestedBy: ptr("PAYMENT_SERVICE"), SatisfiedAt: &now, PaidMinor: in.PaidMinor, UpdatedAt: now})
	return err
}

// Owner notification templates of the sale contexts.
const TemplateSaleOffer = "owner.sale_offer" // listing_title

func init() {
	produces("sale.transaction.updated.v1", AggSale, "Customer-visible sale state (stage, accepted buyer reference and agreed price).", "Marketplace, Notification, Admin", SaleEvent{})
	archiveBlockerChecks = append(archiveBlockerChecks, struct{ name, sql string }{"open sale",
		`SELECT EXISTS (SELECT 1 FROM sale_transactions WHERE property_id = $1 AND status NOT IN ('COMPLETED','CANCELLED','FAILED'))`})
	closureHoldChecks = append(closureHoldChecks, struct{ code, sql string }{"OPEN_SALE", `SELECT EXISTS (SELECT 1 FROM sale_transactions t
		JOIN properties p ON p.id = t.property_id WHERE p.owner_account_id = $1 AND t.status NOT IN ('COMPLETED','CANCELLED','FAILED','INQUIRY'))`})
}
