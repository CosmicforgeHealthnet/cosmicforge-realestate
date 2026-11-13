// Package customer is the Marketplace customer side: customer accounts,
// discovery over Marketplace's own listing projection, and checkout journeys
// that are orchestrated through the owning services.
//
// Marketplace is never a competing authority:
//   - prices are canonical Business quotes (channel-quotes);
//   - holds and reservations are canonical Business reservations created
//     through the signed channel-receipt contract;
//   - money moves only in the Payment Service: Marketplace provisions the
//     customer's own wallet there and asks Business to collect; the customer
//     pays on the Payment intent's hosted checkout (read from the Payment
//     API), the provider's signed webhook settles it in Payment, and
//     confirmation is Business's decision after its own Payment readback;
//   - every local checkout state is a reconciled readback of Business.
package customer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/internal/supply"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Role held by customer accounts.
const Role = "CUSTOMER"

// Deps are the collaborators of the customer side.
type Deps struct {
	// Supply is the business-supply.v1 receiving side (mounted as is).
	Supply *supply.Service
	// Payment is a signed client of the Payment Service.
	Payment *httpx.Client
	// LegalEntity is the Payment legal entity customer wallets belong to.
	LegalEntity string
}

type svc struct {
	rt   *domainkit.Runtime
	deps Deps
}

// New composes marketplace-service from configuration.
func New(rt *domainkit.Runtime) domainkit.App {
	d := Deps{Supply: &supply.Service{Pool: rt.Pool, TTL: 24 * time.Hour}, LegalEntity: "cosmicforge-ng"}
	if l := rt.Cfg.Loader; l != nil {
		d.Supply.TTL = l.Duration("SUPPLY_CONNECTION_VALIDATION_TTL", 24*time.Hour)
		d.LegalEntity = l.String("PAYMENT_LEGAL_ENTITY", "cosmicforge-ng")
		if u := l.String("PAYMENT_SERVICE_URL", ""); u != "" && rt.Cfg.Identity != nil && rt.Cfg.Identity.Signer != nil {
			if c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: u, Audience: string(platform.PaymentService), Tokens: rt.Cfg.Identity.Signer, Timeout: 10 * time.Second, MaxAttempts: 3}); err == nil {
				d.Payment = c
			}
		}
	}
	return NewWith(rt, d)
}

// NewWith composes the service with explicit collaborators.
func NewWith(rt *domainkit.Runtime, d Deps) domainkit.App {
	s := &svc{rt: rt, deps: d}
	if rt.Accounts != nil {
		rt.Accounts.DefaultRoles = []string{Role}
	}
	return domainkit.App{
		Raw: func(r chi.Router) {
			if d.Supply != nil && rt.Cfg.Identity != nil {
				supply.Mount(r, d.Supply, rt.Cfg.Identity.Verifier)
			}
		},
		Public: func(r chi.Router) {
			r.Get("/listings", s.search)
			r.Get("/listings/{id}", s.listing)
			r.Post("/listings/{id}/views", s.view)
		},
		Actor: func(r chi.Router) {
			r.Use(domainkit.RequireRole(Role))
			r.Post("/quotes", s.quote)
			r.Get("/checkouts", s.listCheckouts)
			r.Get("/checkouts/{id}", s.getCheckout)
			r.Post("/checkouts/{id}/hold", s.hold)
			r.Post("/checkouts/{id}/pay", s.pay)
			r.Post("/checkouts/{id}/cancel", s.cancel)
			r.Post("/checkouts/{id}/review", s.review)
		},
		Sweep:      s.sweep,
		SweepEvery: 30 * time.Second,
	}
}

func pathID(r *http.Request) (ids.ID, error) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		return id, apperrors.InvalidRequest("A valid identifier is required.")
	}
	return id, nil
}

// ---------------------------------------------------------------- discovery

type ariDay struct {
	Day             string `json:"day"`
	Available       int    `json:"available"`
	AmountMinor     int64  `json:"amount_minor"`
	Currency        string `json:"currency"`
	MinNights       int    `json:"minimum_nights"`
	MaxNights       int    `json:"maximum_nights"`
	StopSell        bool   `json:"stop_sell"`
	ClosedToArrival bool   `json:"closed_to_arrival"`
}

type projection struct {
	ConnectionID   ids.ID
	OrganisationID ids.ID
	PropertyID     ids.ID
	Content        struct {
		Title         string   `json:"title"`
		Description   string   `json:"description"`
		Language      string   `json:"language"`
		Amenities     []string `json:"amenities"`
		HouseRules    []string `json:"house_rules"`
		CheckInLocal  string   `json:"check_in_local"`
		CheckOutLocal string   `json:"check_out_local"`
		MediaLinkIDs  []ids.ID `json:"media_link_ids"`
	}
	ListingID ids.ID
	ProductID ids.ID
	ARI       []ariDay
	ARIAt     time.Time
}

const projectionQuery = `SELECT sc.id,sc.organisation_id,sc.property_id,c.payload,COALESCE(a.payload->'ari',c.payload->'ari'),COALESCE(a.updated_at,c.updated_at)
FROM supply_connections sc
JOIN listing_projections c ON c.connection_id=sc.id AND c.kind='CONTENT' AND c.publication_state='PUBLISHED'
LEFT JOIN listing_projections a ON a.connection_id=sc.id AND a.kind='ARI' AND a.publication_state='PUBLISHED'
WHERE sc.state='ACTIVE'`

func scanProjection(row pgx.Row) (projection, error) {
	var p projection
	var content, ari []byte
	if err := row.Scan(&p.ConnectionID, &p.OrganisationID, &p.PropertyID, &content, &ari, &p.ARIAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, apperrors.NotFound("LISTING_NOT_FOUND", "Listing not found.")
		}
		return p, err
	}
	var body struct {
		Content   json.RawMessage `json:"content"`
		ListingID ids.ID          `json:"listing_id"`
		Product   struct {
			ID ids.ID `json:"id"`
		} `json:"product"`
	}
	if err := json.Unmarshal(content, &body); err != nil {
		return p, err
	}
	_ = json.Unmarshal(body.Content, &p.Content)
	p.ListingID, p.ProductID = body.ListingID, body.Product.ID
	if len(ari) > 0 {
		_ = json.Unmarshal(ari, &p.ARI)
	}
	return p, nil
}

// availability evaluates Marketplace's ARI projection for a stay. It is an
// indicative filter: the canonical answer is the Business quote and hold.
func (p projection) availability(from, to string, quantity int) (bool, int64, string) {
	start, e1 := time.Parse(time.DateOnly, from)
	end, e2 := time.Parse(time.DateOnly, to)
	if e1 != nil || e2 != nil || !end.After(start) {
		return false, 0, ""
	}
	days := map[string]ariDay{}
	for _, d := range p.ARI {
		days[d.Day] = d
	}
	nights := int(end.Sub(start).Hours() / 24)
	total, currency := int64(0), ""
	for i := range nights {
		d, ok := days[start.AddDate(0, 0, i).Format(time.DateOnly)]
		if !ok || d.StopSell || d.Available < quantity || i == 0 && (d.ClosedToArrival || d.MinNights > 0 && nights < d.MinNights || d.MaxNights > 0 && nights > d.MaxNights) {
			return false, 0, ""
		}
		total += d.AmountMinor * int64(quantity)
		currency = d.Currency
	}
	return true, total, currency
}

func (p projection) summary() map[string]any {
	from, currency := int64(0), ""
	for _, d := range p.ARI {
		if !d.StopSell && d.Available > 0 && (from == 0 || d.AmountMinor < from) {
			from, currency = d.AmountMinor, d.Currency
		}
	}
	desc := p.Content.Description
	if len(desc) > 280 {
		desc = desc[:280]
	}
	return map[string]any{"listing_id": p.ConnectionID, "title": p.Content.Title, "summary": desc, "amenities": p.Content.Amenities, "property_id": p.PropertyID, "product_id": p.ProductID,
		"from_nightly_minor": from, "currency": currency, "availability_observed_at": p.ARIAt, "price_authority": "business-operations-service"}
}

func (s *svc) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	text := strings.TrimSpace(q.Get("q"))
	if len(text) > 100 {
		domainkit.Fail(w, r, apperrors.InvalidRequest("Search text is limited to 100 characters."))
		return
	}
	quantity := 1
	from, to := q.Get("date_from"), q.Get("date_to")
	rows, err := s.rt.Pool.Query(r.Context(), projectionQuery+` AND ($1='' OR c.payload->'content'->>'title' ILIKE '%'||$1||'%' OR c.payload->'content'->>'description' ILIKE '%'||$1||'%') ORDER BY c.updated_at DESC LIMIT 200`, text)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (projection, error) { return scanProjection(row) })
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	searchID := ids.New()
	items := []map[string]any{}
	for _, p := range list {
		item := p.summary()
		if from != "" || to != "" {
			ok, total, currency := p.availability(from, to, quantity)
			if !ok {
				continue
			}
			item["indicative_total_minor"], item["currency"] = total, currency
		}
		items = append(items, item)
		s.recordFunnel(r.Context(), p, "IMPRESSION", "mkt-impression:"+searchID.String()+":"+p.ConnectionID.String())
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": items, "search_id": searchID})
}

func (s *svc) loadListing(ctx context.Context, q database.Querier, id ids.ID) (projection, error) {
	return scanProjection(q.QueryRow(ctx, projectionQuery+` AND sc.id=$1`, id))
}

func (s *svc) listing(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var p projection
	if err == nil {
		p, err = s.loadListing(r.Context(), s.rt.Pool, id)
	}
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	out := p.summary()
	out["description"], out["house_rules"], out["check_in_local"], out["check_out_local"], out["media_link_ids"], out["language"] = p.Content.Description, p.Content.HouseRules, p.Content.CheckInLocal, p.Content.CheckOutLocal, p.Content.MediaLinkIDs, p.Content.Language
	out["calendar"] = p.ARI
	domainkit.Data(w, http.StatusOK, out)
}

type viewInput struct {
	ViewID ids.ID `json:"view_id"`
}

func (s *svc) view(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var in viewInput
	if err == nil {
		err = httpx.DecodeJSON(w, r, &in, 1024)
	}
	if err == nil && in.ViewID.ValidateV7() != nil {
		err = apperrors.InvalidRequest("A client-generated UUIDv7 view_id is required.")
	}
	var p projection
	if err == nil {
		p, err = s.loadListing(r.Context(), s.rt.Pool, id)
	}
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	s.recordFunnel(r.Context(), p, "LISTING_VIEW", "mkt-view:"+in.ViewID.String())
	domainkit.Data(w, http.StatusAccepted, map[string]any{"recorded": true})
}

// recordFunnel stores an observation for delivery to Business (best effort:
// discovery never fails because analytics could not be stored).
func (s *svc) recordFunnel(ctx context.Context, p projection, stage, ref string) {
	var listing *ids.ID
	if !p.ListingID.IsZero() {
		listing = &p.ListingID
	}
	if _, err := s.rt.Pool.Exec(ctx, `INSERT INTO funnel_events(id,connection_id,organisation_id,property_id,listing_id,stage,source_reference,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,now()) ON CONFLICT (source_reference) DO NOTHING`, ids.New(), p.ConnectionID, p.OrganisationID, p.PropertyID, listing, stage, ref); err != nil && s.rt.Log != nil {
		s.rt.Log.Warn("funnel observation not stored", "error", err)
	}
}

// ---------------------------------------------------------------- checkout

// Checkout is a customer's checkout journey.
type Checkout struct {
	ID                    ids.ID     `json:"id"`
	ListingID             ids.ID     `json:"listing_id"`
	OrganisationID        ids.ID     `json:"-"`
	PropertyID            ids.ID     `json:"property_id"`
	ProductID             ids.ID     `json:"product_id"`
	DateFrom              string     `json:"date_from"`
	DateTo                string     `json:"date_to"`
	Adults                int        `json:"adults"`
	Children              int        `json:"children"`
	Quantity              int        `json:"quantity"`
	BusinessQuoteID       ids.ID     `json:"business_quote_id"`
	ProductVersion        int64      `json:"-"`
	PoolVersion           int64      `json:"-"`
	AmountMinor           int64      `json:"amount_minor"`
	Currency              string     `json:"currency"`
	QuoteExpiresAt        time.Time  `json:"quote_expires_at"`
	State                 string     `json:"state"`
	BusinessReservationID *ids.ID    `json:"business_reservation_id"`
	BusinessVersion       *int64     `json:"business_version"`
	BusinessState         *string    `json:"business_state"`
	FinancialState        *string    `json:"financial_state"`
	HoldExpiresAt         *time.Time `json:"hold_expires_at"`
	WalletID              *ids.ID    `json:"wallet_id"`
	CollectionRequestID   *ids.ID    `json:"collection_request_id"`
	// IntentID is the Payment Service intent; the customer opens its hosted
	// checkout from the Payment API (GET /api/v1/payments/payment-intents/{id}).
	IntentID     *ids.ID    `json:"intent_id"`
	CheckoutURL  *string    `json:"-"`
	LastSyncedAt *time.Time `json:"last_synced_at"`
	Version      int64      `json:"version"`
	AccountID    ids.ID     `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
}

const checkoutColumns = `id,connection_id,organisation_id,property_id,product_id,date_from::text,date_to::text,adults,children,quantity,business_quote_id,product_version,pool_version,amount_minor,currency,quote_expires_at,state,business_reservation_id,business_version,business_state,financial_state,hold_expires_at,wallet_id,collection_request_id,intent_id,checkout_url,last_synced_at,version,account_id,created_at`

func scanCheckout(row pgx.Row) (Checkout, error) {
	var c Checkout
	err := row.Scan(&c.ID, &c.ListingID, &c.OrganisationID, &c.PropertyID, &c.ProductID, &c.DateFrom, &c.DateTo, &c.Adults, &c.Children, &c.Quantity, &c.BusinessQuoteID, &c.ProductVersion, &c.PoolVersion, &c.AmountMinor, &c.Currency, &c.QuoteExpiresAt, &c.State, &c.BusinessReservationID, &c.BusinessVersion, &c.BusinessState, &c.FinancialState, &c.HoldExpiresAt, &c.WalletID, &c.CollectionRequestID, &c.IntentID, &c.CheckoutURL, &c.LastSyncedAt, &c.Version, &c.AccountID, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, apperrors.NotFound("CHECKOUT_NOT_FOUND", "Checkout not found.")
	}
	return c, err
}

func (s *svc) owned(ctx context.Context, q database.Querier, id ids.ID, lock bool) (Checkout, error) {
	sql := `SELECT ` + checkoutColumns + ` FROM checkouts WHERE id=$1 AND account_id=$2`
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanCheckout(q.QueryRow(ctx, sql, id, domainkit.ActorID(ctx)))
}

type quoteInput struct {
	ListingID ids.ID `json:"listing_id"`
	DateFrom  string `json:"date_from"`
	DateTo    string `json:"date_to"`
	Adults    int    `json:"adults"`
	Children  int    `json:"children"`
	Quantity  int    `json:"quantity"`
}

type businessQuote struct {
	QuoteID        ids.ID    `json:"quote_id"`
	ProductID      ids.ID    `json:"product_id"`
	ProductVersion int64     `json:"product_version"`
	PoolVersion    int64     `json:"pool_version"`
	DateFrom       string    `json:"date_from"`
	DateTo         string    `json:"date_to"`
	AmountMinor    int64     `json:"amount_minor"`
	Currency       string    `json:"currency"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// quote asks Business for the canonical price of a stay on a listing.
func (s *svc) quote(w http.ResponseWriter, r *http.Request) {
	var in quoteInput
	var p projection
	var q businessQuote
	pre := func(ctx context.Context) error {
		if in.Quantity == 0 {
			in.Quantity = 1
		}
		if in.ListingID.ValidateV7() != nil || in.Adults < 1 || in.Adults > 50 || in.Children < 0 || in.Children > 50 || in.Quantity < 1 || in.Quantity > 20 {
			return apperrors.InvalidRequest("A listing, guests (1-50 adults) and quantity (1-20) are required.")
		}
		var err error
		if p, err = s.loadListing(ctx, s.rt.Pool, in.ListingID); err != nil {
			return err
		}
		return s.rt.CallBusiness(ctx, p.OrganisationID, http.MethodPost, "/channel-quotes", "", map[string]any{"organisation_id": p.OrganisationID, "connection_id": p.ConnectionID, "product_id": p.ProductID, "date_from": in.DateFrom, "date_to": in.DateTo, "adults": in.Adults, "children": in.Children, "quantity": in.Quantity}, &q)
	}
	s.rt.CommandWithPre(w, r, "checkout.quote", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if q.QuoteID.IsZero() || q.ProductID != p.ProductID {
			return domainkit.Change{}, apperrors.Unavailable("Business returned no canonical quote for this listing.")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO checkouts(id,account_id,connection_id,organisation_id,property_id,product_id,date_from,date_to,adults,children,quantity,business_quote_id,product_version,pool_version,amount_minor,currency,quote_expires_at,state) VALUES($1,$2,$3,$4,$5,$6,$7::date,$8::date,$9,$10,$11,$12,$13,$14,$15,$16,$17,'QUOTED')`,
			id, domainkit.ActorID(ctx), p.ConnectionID, p.OrganisationID, p.PropertyID, p.ProductID, q.DateFrom, q.DateTo, in.Adults, in.Children, in.Quantity, q.QuoteID, q.ProductVersion, q.PoolVersion, q.AmountMinor, q.Currency, q.ExpiresAt); err != nil {
			if database.IsUniqueViolation(err, "checkouts_business_quote_id_key") {
				return domainkit.Change{}, apperrors.Conflict("QUOTE_ALREADY_USED", "Business returned a quote that already belongs to a checkout.")
			}
			return domainkit.Change{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO funnel_events(id,connection_id,organisation_id,property_id,listing_id,stage,source_reference,observed_at) VALUES($1,$2,$3,$4,$5,'INQUIRY',$6,now()) ON CONFLICT DO NOTHING`, ids.New(), p.ConnectionID, p.OrganisationID, p.PropertyID, nullable(p.ListingID), "mkt-inquiry:"+id.String()); err != nil {
			return domainkit.Change{}, err
		}
		ch, err := s.change(ctx, tx, id, "checkout.quoted")
		ch.Status = http.StatusCreated
		return ch, err
	})
}

func nullable(id ids.ID) *ids.ID {
	if id.IsZero() {
		return nil
	}
	return &id
}

// change reloads a checkout and builds its audit and event.
func (s *svc) change(ctx context.Context, tx pgx.Tx, id ids.ID, action string) (domainkit.Change, error) {
	c, err := scanCheckout(tx.QueryRow(ctx, `SELECT `+checkoutColumns+` FROM checkouts WHERE id=$1`, id))
	if err != nil {
		return domainkit.Change{}, err
	}
	ev, err := s.event(ctx, c)
	if err != nil {
		return domainkit.Change{}, err
	}
	return domainkit.Change{Body: c, Audit: &audit.Event{Action: action, TargetType: "checkout", TargetID: id, Changes: map[string]any{"state": c.State}}, Events: []events.Envelope{ev}}, nil
}

func (s *svc) event(ctx context.Context, c Checkout) (events.Envelope, error) {
	return s.rt.Event(ctx, "marketplace.checkout.updated.v1", "checkout", c.ID, map[string]any{"checkout_id": c.ID, "listing_id": c.ListingID, "organisation_id": c.OrganisationID, "property_id": c.PropertyID, "state": c.State, "business_reservation_id": c.BusinessReservationID, "business_state": c.BusinessState, "financial_state": c.FinancialState, "version": c.Version})
}

func (s *svc) listCheckouts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rt.Pool.Query(r.Context(), `SELECT `+checkoutColumns+` FROM checkouts WHERE account_id=$1 ORDER BY created_at DESC LIMIT 100`, domainkit.ActorID(r.Context()))
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Checkout, error) { return scanCheckout(row) })
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusOK, map[string]any{"items": items})
}

// getCheckout reconciles the checkout with Business (and Payment) first, so
// the customer always sees the owning services' current answer.
func (s *svc) getCheckout(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var c Checkout
	if err == nil {
		c, err = s.owned(r.Context(), s.rt.Pool, id, false)
	}
	if err == nil {
		c, err = s.reconcile(r.Context(), c)
	}
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	domainkit.Data(w, http.StatusOK, c)
}

// businessReservation is Business's channel readback.
type businessReservation struct {
	ReservationID  ids.ID     `json:"reservation_id"`
	Version        int64      `json:"version"`
	State          string     `json:"state"`
	FinancialState string     `json:"financial_state"`
	AmountMinor    int64      `json:"amount_minor"`
	Currency       string     `json:"currency"`
	HoldExpiresAt  *time.Time `json:"hold_expires_at"`
	Confirmed      bool       `json:"confirmed"`
	StayCompleted  bool       `json:"stay_completed"`
	Payment        *struct {
		FinancialGroupID       ids.ID   `json:"financial_group_id"`
		RequiredAmountMinor    int64    `json:"required_amount_minor"`
		Currency               string   `json:"currency"`
		FinancialState         string   `json:"financial_state"`
		AllowedPaymentPolicies []ids.ID `json:"allowed_payment_policy_ids"`
		CollectionRequestID    *ids.ID  `json:"collection_request_id"`
		CollectionState        *string  `json:"collection_state"`
		IntentID               *ids.ID  `json:"intent_id"`
	} `json:"payment"`
}

func (s *svc) readBusiness(ctx context.Context, c Checkout) (businessReservation, error) {
	var b businessReservation
	err := s.rt.CallBusiness(ctx, c.OrganisationID, http.MethodGet, "/reservations/"+c.BusinessReservationID.String()+"?connection_id="+url.QueryEscape(c.ListingID.String()), "", nil, &b)
	return b, err
}

// payment performs a signed Payment Service call and unwraps its "data"
// envelope into out.
func (s *svc) payment(ctx context.Context, req httpx.Request, out any) error {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := s.deps.Payment.Do(ctx, req, &env); err != nil {
		return err
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// reconcile maps Business's authoritative reservation state onto the local
// journey. It never moves a journey forward on its own evidence.
func (s *svc) reconcile(ctx context.Context, c Checkout) (Checkout, error) {
	if c.BusinessReservationID == nil || slices.Contains([]string{"EXPIRED", "CANCELLED", "COMPLETED", "PAYMENT_FAILED"}, c.State) {
		return c, nil
	}
	b, err := s.readBusiness(ctx, c)
	if err != nil {
		return c, err
	}
	var out Checkout
	err = database.WithTx(ctx, s.rt.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := scanCheckout(tx.QueryRow(ctx, `SELECT `+checkoutColumns+` FROM checkouts WHERE id=$1 FOR UPDATE`, c.ID))
		if err != nil {
			return err
		}
		targets := []string{}
		switch {
		case b.State == "CANCELLED":
			targets = []string{"CANCELLED"}
		case slices.Contains([]string{"EXPIRED", "REJECTED"}, b.State):
			targets = []string{"EXPIRED"}
		case b.StayCompleted:
			targets = []string{"CONFIRMED", "COMPLETED"}
		case b.Confirmed:
			targets = []string{"CONFIRMED"}
		case b.FinancialState == "FAILED" && cur.State == "PAYMENT_PENDING":
			targets = []string{"PAYMENT_FAILED"}
		}
		state := cur.State
		for _, t := range targets {
			if t != state && allowed[state+">"+t] {
				state = t
			}
		}
		var request, intentID *ids.ID
		if b.Payment != nil {
			request, intentID = b.Payment.CollectionRequestID, b.Payment.IntentID
		}
		if _, err := tx.Exec(ctx, `UPDATE checkouts SET state=$2,business_version=$3,business_state=$4,financial_state=$5,hold_expires_at=$6,collection_request_id=COALESCE($7,collection_request_id),intent_id=COALESCE($8,intent_id),last_synced_at=now(),version=version+1,updated_at=now() WHERE id=$1`,
			cur.ID, state, b.Version, b.State, b.FinancialState, b.HoldExpiresAt, request, intentID); err != nil {
			return err
		}
		if out, err = scanCheckout(tx.QueryRow(ctx, `SELECT `+checkoutColumns+` FROM checkouts WHERE id=$1`, c.ID)); err != nil {
			return err
		}
		if state != cur.State {
			ev, err := s.event(ctx, out)
			if err != nil {
				return err
			}
			if err = s.rt.Outbox.Add(ctx, tx, ev); err != nil {
				return err
			}
			_, err = s.rt.Audit.Record(ctx, tx, audit.Event{Action: "checkout.reconciled", TargetType: "checkout", TargetID: c.ID, Changes: map[string]any{"state": state, "business_state": b.State}})
			return err
		}
		return nil
	})
	return out, err
}

var allowed = map[string]bool{}

func init() {
	for _, e := range strings.Split("QUOTED>HELD,QUOTED>EXPIRED,QUOTED>CANCELLED,HELD>PAYMENT_PENDING,HELD>CONFIRMED,HELD>EXPIRED,HELD>CANCELLED,PAYMENT_PENDING>CONFIRMED,PAYMENT_PENDING>PAYMENT_FAILED,PAYMENT_PENDING>EXPIRED,PAYMENT_PENDING>CANCELLED,CONFIRMED>CANCELLED,CONFIRMED>COMPLETED", ",") {
		allowed[e] = true
	}
}

type receiptResult struct {
	Accepted      bool    `json:"canonical_reservation_accepted"`
	ReservationID *ids.ID `json:"canonical_reservation_id"`
	State         string  `json:"canonical_reservation_state"`
	Reason        string  `json:"reason"`
	Duplicate     bool    `json:"duplicate"`
}

func (s *svc) receipt(ctx context.Context, c Checkout, kind, version string, booking map[string]any, expected int64, reference string) (receiptResult, error) {
	event := map[string]any{"provider_event_id": "mkt-checkout:" + c.ID.String() + ":" + strings.ToLower(kind) + ":" + version, "external_object_id": "mkt-checkout:" + c.ID.String(), "external_version": version, "kind": kind,
		"occurred_at": time.Now().UTC(), "product_id": c.ProductID, "date_from": c.DateFrom, "date_to": c.DateTo, "quantity": c.Quantity, "reference": reference}
	if booking != nil {
		event["booking"], event["quote_id"] = booking, c.BusinessQuoteID
	}
	if expected > 0 {
		event["expected_reservation_version"] = expected
	}
	var out receiptResult
	err := s.rt.CallBusiness(ctx, c.OrganisationID, http.MethodPost, "/channel-receipts", "", map[string]any{"organisation_id": c.OrganisationID, "connection_id": c.ListingID, "receipt": event}, &out)
	if err == nil && !out.Accepted {
		err = apperrors.Conflict("BUSINESS_NOT_ACCEPTED", "Business did not accept the request: "+out.Reason)
	}
	return out, err
}

type actionInput struct {
	Version int64 `json:"version"`
}

// hold creates the canonical HOLD reservation from the accepted quote.
func (s *svc) hold(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in actionInput
	var c Checkout
	var b businessReservation
	pre := func(ctx context.Context) error {
		var err error
		if c, err = s.owned(ctx, s.rt.Pool, id, false); err != nil {
			return err
		}
		if c.State != "QUOTED" || in.Version > 0 && in.Version != c.Version {
			return apperrors.Conflict("INVALID_TRANSITION", "Only the current quoted checkout can be held.")
		}
		if !time.Now().Before(c.QuoteExpiresAt) {
			return apperrors.Conflict("QUOTE_EXPIRED", "The price quote expired; request a new quote.")
		}
		booking := map[string]any{"property_id": c.PropertyID, "source": "MARKETPLACE", "channel": "COSMICFORGE_MARKETPLACE", "source_reference": "mkt-checkout:" + c.ID.String(), "state": "HOLD", "payment_responsibility": "PLATFORM_COLLECT",
			"rooms":  []map[string]any{{"product_id": c.ProductID, "quote_id": c.BusinessQuoteID, "product_version": c.ProductVersion, "pool_version": c.PoolVersion, "date_from": c.DateFrom, "date_to": c.DateTo, "quantity": c.Quantity}},
			"guests": []map[string]any{{"source_kind": "MARKETPLACE", "source_reference": "marketplace-customer:" + c.AccountID.String(), "is_primary": true}}, "reason": "Marketplace customer checkout hold"}
		res, err := s.receipt(ctx, c, "RESERVATION_CREATED", "1", booking, 0, "")
		if err != nil {
			return err
		}
		if res.ReservationID == nil {
			return apperrors.Unavailable("Business accepted the hold without a reservation reference.")
		}
		c.BusinessReservationID = res.ReservationID
		b, err = s.readBusiness(ctx, c)
		return err
	}
	s.rt.CommandWithPre(w, r, "checkout.hold", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if _, err := tx.Exec(ctx, `UPDATE checkouts SET state='HELD',business_reservation_id=$2,business_version=$3,business_state=$4,financial_state=$5,hold_expires_at=$6,last_synced_at=now(),version=version+1,updated_at=now() WHERE id=$1 AND state='QUOTED'`, c.ID, b.ReservationID, b.Version, b.State, b.FinancialState, b.HoldExpiresAt); err != nil {
			return domainkit.Change{}, err
		}
		return s.change(ctx, tx, c.ID, "checkout.held")
	})
}

type payInput struct {
	Consent bool `json:"consent"`
}

type wallet struct {
	WalletID ids.ID `json:"wallet_id"`
	Currency string `json:"currency"`
}

type policy struct {
	ID        ids.ID     `json:"policy_id"`
	Key       string     `json:"policy_key"`
	Version   string     `json:"version"`
	Currency  string     `json:"currency"`
	Active    bool       `json:"active"`
	From      time.Time  `json:"effective_from"`
	Until     *time.Time `json:"effective_until"`
	Schedules []string   `json:"schedules"`
}

// pay provisions the customer's own Payment wallet and asks Business to
// collect the held obligation from it. No money is moved by Marketplace.
func (s *svc) pay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in payInput
	var c Checkout
	var wl wallet
	var request struct {
		ID ids.ID `json:"id"`
	}
	pre := func(ctx context.Context) error {
		if !in.Consent {
			return apperrors.InvalidRequest("Explicit payment consent is required.")
		}
		if s.deps.Payment == nil {
			return apperrors.Unavailable("The Payment Service is not configured.")
		}
		var err error
		if c, err = s.owned(ctx, s.rt.Pool, id, false); err != nil {
			return err
		}
		if c.State != "HELD" {
			return apperrors.Conflict("INVALID_TRANSITION", "Only a held checkout can be paid.")
		}
		b, err := s.readBusiness(ctx, c)
		if err != nil {
			return err
		}
		if b.State != "HOLD" && b.State != "PENDING_PAYMENT" || b.Payment == nil {
			return apperrors.Conflict("RESERVATION_NOT_PAYABLE", "The reservation is no longer awaiting payment.")
		}
		acct, _ := domainkit.AccountFrom(ctx)
		if err = s.payment(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/wallets/ensure", Body: map[string]any{"domain": string(platform.DomainMarketplace), "account_id": acct.ID, "role": "CUSTOMER", "party_kind": "PERSON", "legal_entity": s.deps.LegalEntity, "currency": b.Payment.Currency, "registration_ref": "marketplace-account:" + acct.ID.String()}}, &wl); err != nil {
			return httpx.ToAppError(err)
		}
		var chosen *policy
		for _, pid := range b.Payment.AllowedPaymentPolicies {
			var p policy
			if err := s.payment(ctx, httpx.Request{Method: http.MethodGet, Path: "/internal/v1/financial-policies/" + pid.String()}, &p); err != nil {
				continue
			}
			now := time.Now()
			if p.Active && (p.Key == "HOTEL" || p.Key == "SHORT_LET") && p.Currency == b.Payment.Currency && !now.Before(p.From) && (p.Until == nil || now.Before(*p.Until)) && len(p.Schedules) > 0 {
				chosen = &p
				break
			}
		}
		if chosen == nil {
			return apperrors.Conflict("NO_PAYMENT_POLICY", "No current Payment policy authorised by the property applies to this booking.")
		}
		schedule := chosen.Schedules[0]
		if slices.Contains(chosen.Schedules, "ONE_TIME") {
			schedule = "ONE_TIME"
		}
		return s.rt.CallBusiness(ctx, c.OrganisationID, http.MethodPost, "/reservations/"+c.BusinessReservationID.String()+"/collections", "mkt-collect:"+c.ID.String(), map[string]any{
			"organisation_id": c.OrganisationID, "connection_id": c.ListingID, "version": b.Version, "payment_policy_id": chosen.ID, "payment_policy_version": chosen.Version, "payment_policy_family": chosen.Key,
			"payer_wallet_id": wl.WalletID, "consent_reference": "marketplace-checkout-consent:" + c.ID.String(), "legal_party_reference": "marketplace:" + acct.ID.String(), "email": acct.Email, "schedule": schedule}, &request)
	}
	s.rt.CommandWithPre(w, r, "checkout.pay", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if _, err := tx.Exec(ctx, `UPDATE checkouts SET state='PAYMENT_PENDING',wallet_id=$2,collection_request_id=$3,version=version+1,updated_at=now() WHERE id=$1 AND state='HELD'`, c.ID, wl.WalletID, request.ID); err != nil {
			return domainkit.Change{}, err
		}
		ch, err := s.change(ctx, tx, c.ID, "checkout.payment_requested")
		ch.Status = http.StatusAccepted
		return ch, err
	})
}

// cancel cancels a quote locally, or asks Business to cancel the canonical
// reservation under its source-cancellation policy.
func (s *svc) cancel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in actionInput
	var c Checkout
	var b businessReservation
	pre := func(ctx context.Context) error {
		var err error
		if c, err = s.owned(ctx, s.rt.Pool, id, false); err != nil {
			return err
		}
		if !slices.Contains([]string{"QUOTED", "HELD", "PAYMENT_PENDING", "CONFIRMED"}, c.State) {
			return apperrors.Conflict("INVALID_TRANSITION", "This checkout can no longer be cancelled.")
		}
		if c.BusinessReservationID == nil {
			return nil
		}
		if b, err = s.readBusiness(ctx, c); err != nil {
			return err
		}
		_, err = s.receipt(ctx, c, "RESERVATION_CANCELLED", "cancel-"+itoa(b.Version), nil, b.Version, "Customer cancelled through Marketplace")
		if err != nil {
			return err
		}
		b, err = s.readBusiness(ctx, c)
		return err
	}
	s.rt.CommandWithPre(w, r, "checkout.cancel", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		if c.BusinessReservationID != nil && b.State != "CANCELLED" {
			return domainkit.Change{}, apperrors.Conflict("CANCELLATION_PENDING", "Business has not cancelled the reservation.")
		}
		if _, err := tx.Exec(ctx, `UPDATE checkouts SET state='CANCELLED',business_state=COALESCE($2,business_state),business_version=COALESCE($3,business_version),last_synced_at=now(),version=version+1,updated_at=now() WHERE id=$1`, c.ID, nilIfEmpty(b.State), nilIfZero(b.Version)); err != nil {
			return domainkit.Change{}, err
		}
		return s.change(ctx, tx, c.ID, "checkout.cancelled")
	})
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func nilIfEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func nilIfZero(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

type reviewInput struct {
	Rating int    `json:"rating"`
	Body   string `json:"body"`
}

// review records a verified-stay review: only for a stay Business reports
// as completed, accepted by Business reputation before it is stored here.
func (s *svc) review(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		domainkit.Fail(w, r, err)
		return
	}
	var in reviewInput
	var c Checkout
	var accepted json.RawMessage
	pre := func(ctx context.Context) error {
		if in.Rating < 1 || in.Rating > 5 || len(in.Body) > 5000 {
			return apperrors.InvalidRequest("A rating of 1-5 and at most 5000 characters are required.")
		}
		var err error
		if c, err = s.owned(ctx, s.rt.Pool, id, false); err != nil {
			return err
		}
		if c.BusinessReservationID == nil {
			return apperrors.Conflict("STAY_NOT_COMPLETED", "Only a completed stay can be reviewed.")
		}
		b, err := s.readBusiness(ctx, c)
		if err != nil {
			return err
		}
		if !b.StayCompleted {
			return apperrors.Conflict("STAY_NOT_COMPLETED", "Only a completed stay can be reviewed.")
		}
		review := map[string]any{"property_id": c.PropertyID, "source": "MARKETPLACE_VERIFIED_STAY", "external_review_id": "mkt-review:" + c.ID.String(), "source_version": 1, "reservation_id": c.BusinessReservationID,
			"rating_value": in.Rating, "rating_scale": 5, "text": in.Body, "store_text": true, "content_reference": "marketplace-review:" + c.ID.String(), "reviewed_at": time.Now().UTC(), "moderation_status": "PUBLISHED"}
		return s.rt.CallBusiness(ctx, c.OrganisationID, http.MethodPost, "/reviews", "mkt-review:"+c.ID.String(), map[string]any{"organisation_id": c.OrganisationID, "connection_id": c.ListingID, "review": review}, &accepted)
	}
	s.rt.CommandWithPre(w, r, "checkout.review", &in, pre, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
		rid := ids.New()
		if len(accepted) == 0 {
			accepted = json.RawMessage(`{}`)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stay_reviews(id,checkout_id,account_id,rating,body,business_review) VALUES($1,$2,$3,$4,$5,$6)`, rid, c.ID, domainkit.ActorID(ctx), in.Rating, in.Body, accepted); err != nil {
			if database.IsUniqueViolation(err, "stay_reviews_checkout_id_key") {
				return domainkit.Change{}, apperrors.Conflict("REVIEW_EXISTS", "This stay has already been reviewed.")
			}
			return domainkit.Change{}, err
		}
		ev, err := s.rt.Event(ctx, "marketplace.review.submitted.v1", "stay_review", rid, map[string]any{"review_id": rid, "checkout_id": c.ID, "organisation_id": c.OrganisationID, "property_id": c.PropertyID, "rating": in.Rating})
		if err != nil {
			return domainkit.Change{}, err
		}
		return domainkit.Change{Status: http.StatusCreated, Body: map[string]any{"review_id": rid, "checkout_id": c.ID, "rating": in.Rating, "business": accepted},
			Audit: &audit.Event{Action: "review.submitted", TargetType: "stay_review", TargetID: rid}, Events: []events.Envelope{ev}}, nil
	})
}

// ---------------------------------------------------------------- worker

// sweep expires lapsed quotes, reconciles live journeys with Business and
// delivers funnel observations.
func (s *svc) sweep(ctx context.Context) error {
	var errs []error
	if _, err := s.rt.Pool.Exec(ctx, `UPDATE checkouts SET state='EXPIRED',version=version+1,updated_at=now() WHERE state='QUOTED' AND quote_expires_at<=now()`); err != nil {
		errs = append(errs, err)
	}
	rows, err := s.rt.Pool.Query(ctx, `SELECT `+checkoutColumns+` FROM checkouts WHERE state IN ('HELD','PAYMENT_PENDING','CONFIRMED') ORDER BY last_synced_at NULLS FIRST LIMIT 100`)
	if err == nil {
		var list []Checkout
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Checkout, error) { return scanCheckout(row) })
		for _, c := range list {
			if _, e := s.reconcile(requestcontext.WithRequestID(ctx, ids.New().String()), c); e != nil {
				errs = append(errs, e)
			}
		}
	}
	if err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, s.deliverFunnel(ctx))
	return errors.Join(errs...)
}

func (s *svc) deliverFunnel(ctx context.Context) error {
	rows, err := s.rt.Pool.Query(ctx, `SELECT id,organisation_id,property_id,listing_id,stage,source_reference,quantity,observed_at FROM funnel_events WHERE delivered_at IS NULL AND attempts<20 ORDER BY created_at LIMIT 200`)
	if err != nil {
		return err
	}
	type pending struct {
		id, org, property ids.ID
		listing           *ids.ID
		stage, ref        string
		quantity          int
		observed          time.Time
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pending, error) {
		var p pending
		return p, row.Scan(&p.id, &p.org, &p.property, &p.listing, &p.stage, &p.ref, &p.quantity, &p.observed)
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range list {
		err := s.rt.CallBusiness(ctx, p.org, http.MethodPost, "/funnel-observations", "", map[string]any{"organisation_id": p.org, "property_id": p.property, "listing_id": p.listing, "stage": p.stage, "source_reference": p.ref, "channel": "MARKETPLACE", "quantity": p.quantity, "observed_at": p.observed}, nil)
		if err != nil {
			_, _ = s.rt.Pool.Exec(ctx, `UPDATE funnel_events SET attempts=attempts+1,last_error=$2 WHERE id=$1`, p.id, truncate(err.Error(), 300))
			errs = append(errs, err)
			continue
		}
		if _, err = s.rt.Pool.Exec(ctx, `UPDATE funnel_events SET delivered_at=now(),attempts=attempts+1,last_error=NULL WHERE id=$1`, p.id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

// Sweep exposes the worker sweep (tests drive it directly).
func Sweep(app domainkit.App) func(context.Context) error { return app.Sweep }
