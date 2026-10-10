package finance

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"sync"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5"
)

type ProviderConfig struct {
	ID                      ids.ID   `json:"provider_account_id"`
	PoolID                  ids.ID   `json:"pool_id"`
	Name                    string   `json:"provider"`
	LegalEntity             string   `json:"legal_entity"`
	MerchantRef             string   `json:"merchant_ref"`
	Mode                    string   `json:"mode"`
	Countries               []string `json:"countries"`
	Currencies              []string `json:"currencies"`
	Categories              []string `json:"categories"`
	Capabilities            []string `json:"capabilities"`
	Approved                bool     `json:"approved"`
	Operational             bool     `json:"operational"`
	FailoverEligible        bool     `json:"failover_eligible"`
	CustodyApproved         bool     `json:"custody_approved"`
	DVAApproved             bool     `json:"dva_approved"`
	ManualPayoutsApproved   bool     `json:"manual_payouts_approved"`
	PayoutApproved          bool     `json:"payout_approved"`
	PayoutApprovalRef       string   `json:"payout_approval_ref"`
	PayoutFeeApproved       bool     `json:"payout_fee_approved"`
	PayoutFeeMinor          int64    `json:"payout_fee_minor"`
	RefundApproved          bool     `json:"refund_approved"`
	RefundApprovalRef       string   `json:"refund_approval_ref"`
	MinMinor                int64    `json:"min_minor"`
	MaxMinor                int64    `json:"max_minor"`
	Priority                int      `json:"priority"`
	FeeBasisPoints          int      `json:"fee_basis_points"`
	AvailableLiquidityMinor int64    `json:"available_liquidity_minor"`
	Version                 string   `json:"policy_version"`
	CredentialsRef          string   `json:"credentials_ref"`
	ApprovalRef             string   `json:"approval_ref"`
	WebhookRef              string   `json:"webhook_ref"`
	WebhookPreviousRefs     []string `json:"webhook_previous_refs"`
	SettlementAccountRef    string   `json:"settlement_account_ref"`
}
type Adapter struct {
	Collection providers.Collection
	DVA        providers.DedicatedAccounts
	Settlement providers.SettlementProvider
	Payout     providers.Payout
	Refund     providers.RefundProvider
	Reversal   providers.ReversalProvider
}
type Service struct {
	*core.Service
	Environment      string
	mu               sync.RWMutex
	recoveryPolicy   RecoveryPolicy
	adapters         map[ids.ID]Adapter
	Log              *slog.Logger
	transferReadback func(context.Context, ids.ID, string) error
	remedyReadback   func(context.Context, ids.ID, string, string) error
}

func (s *Service) SetRemedyReadback(fn func(context.Context, ids.ID, string, string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remedyReadback = fn
}
func (s *Service) remedyCallback(ctx context.Context, id ids.ID, event, ref string) error {
	s.mu.RLock()
	fn := s.remedyReadback
	s.mu.RUnlock()
	if fn == nil {
		return core.Fail(503, "REMEDY_READBACK_UNAVAILABLE")
	}
	return fn(ctx, id, event, ref)
}

func (s *Service) SetTransferReadback(fn func(context.Context, ids.ID, string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transferReadback = fn
}
func (s *Service) transferCallback(ctx context.Context, provider ids.ID, ref string) error {
	s.mu.RLock()
	fn := s.transferReadback
	s.mu.RUnlock()
	if fn == nil {
		return core.Fail(503, "PAYOUT_READBACK_UNAVAILABLE")
	}
	return fn(ctx, provider, ref)
}

func New(c *core.Service, environment string) *Service {
	return &Service{Service: c, Environment: environment, adapters: map[ids.ID]Adapter{}, recoveryPolicy: RecoveryPolicy{MaxAttempts: 12, BaseSeconds: 5, MaxBackoffSeconds: 3600, LeaseSeconds: 30}, Log: slog.Default()}
}
func (s *Service) Adapter(id ids.ID) (Adapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.adapters[id]
	if !ok {
		return a, core.Fail(503, "PROVIDER_UNAVAILABLE")
	}
	return a, nil
}
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Register is deployment configuration, not a user-controlled capability switch.
// Production activation is deliberately unavailable in this foundation.
func (s *Service) Register(ctx context.Context, c ProviderConfig, a Adapter) error {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		return e
	}
	if c.Mode != "test" || s.Environment == "production" || c.ID.ValidateV7() != nil || c.PoolID.ValidateV7() != nil || c.LegalEntity == "" || c.MerchantRef == "" || c.Version == "" || len(c.Currencies) != 1 || c.MaxMinor <= 0 || c.MinMinor < 0 || c.CustodyApproved || contains(c.Capabilities, "CUSTODY") || (contains(c.Capabilities, "TRANSFER") && (!c.PayoutApproved || c.PayoutApprovalRef == "" || a.Payout == nil)) || contains(c.Capabilities, "MANDATE") {
		return core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	if c.Approved && c.ApprovalRef == "" {
		return core.Fail(422, "PROVIDER_APPROVAL_REQUIRED")
	}
	if !core.ValidCurrency(c.Currencies[0]) || c.MinMinor > c.MaxMinor || c.AvailableLiquidityMinor < 0 || c.FeeBasisPoints < 0 || c.FeeBasisPoints > 10000 || c.PayoutFeeMinor < 0 || contains(c.Capabilities, "TRANSFER") && !c.PayoutFeeApproved {
		return core.Fail(422, "INVALID_PROVIDER_POLICY")
	}
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO provider_accounts(provider_account_id,provider,legal_entity,merchant_ref,mode,config,config_version) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(provider_account_id) DO UPDATE SET config=EXCLUDED.config,config_version=EXCLUDED.config_version WHERE provider_accounts.provider=EXCLUDED.provider AND provider_accounts.legal_entity=EXCLUDED.legal_entity AND provider_accounts.merchant_ref=EXCLUDED.merchant_ref AND provider_accounts.mode=EXCLUDED.mode`, c.ID, c.Name, c.LegalEntity, c.MerchantRef, c.Mode, string(b), c.Version)
	if e != nil {
		return e
	}
	var saved ProviderConfig
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT config FROM provider_accounts WHERE provider_account_id=$1`, c.ID).Scan(&raw)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(raw, &saved); e != nil {
		return e
	}
	if saved.MerchantRef != c.MerchantRef || saved.LegalEntity != c.LegalEntity || saved.Name != c.Name {
		return core.Fail(409, "PROVIDER_CONTEXT_IMMUTABLE")
	}
	_, e = tx.Exec(ctx, `INSERT INTO custody_pools(pool_id,provider_account_id,legal_entity,currency,label) VALUES($1,$2,$3,$4,'merchant-settlement') ON CONFLICT(pool_id) DO NOTHING`, c.PoolID, c.ID, c.LegalEntity, c.Currencies[0])
	if e != nil {
		return e
	}
	var poolOK bool
	e = tx.QueryRow(ctx, `SELECT provider_account_id=$2 AND legal_entity=$3 AND currency=$4 FROM custody_pools WHERE pool_id=$1`, c.PoolID, c.ID, c.LegalEntity, c.Currencies[0]).Scan(&poolOK)
	if e != nil {
		return e
	}
	if !poolOK {
		return core.Fail(409, "POOL_CONTEXT_IMMUTABLE")
	}
	if e = s.Audit(ctx, tx, "provider.configured", c.ID.String(), map[string]string{"version": c.Version, "mode": c.Mode}); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	s.mu.Lock()
	s.adapters[c.ID] = a
	s.mu.Unlock()
	return nil
}

type IntentInput struct {
	LockedQuoteID          *ids.ID `json:"locked_quote_id,omitempty"`
	WalletID               ids.ID  `json:"wallet_id"`
	AmountMinor            int64   `json:"amount_minor"`
	Currency               string  `json:"currency"`
	Country                string  `json:"country"`
	Category               string  `json:"category"`
	EvidenceRef            string  `json:"evidence_ref"`
	PolicyVersion          string  `json:"policy_version"`
	Email                  string  `json:"email"`
	RequiresCustody        bool    `json:"requires_custody"`
	RequiredLiquidityMinor int64   `json:"required_liquidity_minor"`
	ProviderConstraint     *ids.ID `json:"provider_constraint,omitempty"`
}
type Intent struct {
	ID                ids.ID  `json:"intent_id"`
	WalletID          ids.ID  `json:"wallet_id"`
	AmountMinor       int64   `json:"amount_minor"`
	Currency          string  `json:"currency"`
	LegalEntity       string  `json:"legal_entity"`
	ProviderAccountID ids.ID  `json:"provider_account_id"`
	PoolID            ids.ID  `json:"pool_id"`
	Reference         string  `json:"reference"`
	State             string  `json:"state"`
	SettlementState   string  `json:"settlement_state"`
	CheckoutURL       *string `json:"checkout_url,omitempty"`
}

func (s *Service) route(ctx context.Context, in IntentInput, w core.Wallet) (ProviderConfig, error) {
	rows, e := s.DB.Query(ctx, `SELECT config FROM provider_accounts WHERE legal_entity=$1`, w.LegalEntity)
	if e != nil {
		return ProviderConfig{}, e
	}
	defer rows.Close()
	cs := []ProviderConfig{}
	for rows.Next() {
		var b []byte
		var c ProviderConfig
		if e = rows.Scan(&b); e != nil {
			return c, e
		}
		if e = json.Unmarshal(b, &c); e != nil {
			return c, e
		}
		adapter, ae := s.Adapter(c.ID)
		if c.Approved && c.Operational && c.Mode == "test" && ae == nil && adapter.Collection != nil && (c.Priority == 0 || c.FailoverEligible) && c.AvailableLiquidityMinor >= in.RequiredLiquidityMinor && contains(c.Capabilities, "COLLECTION") && contains(c.Countries, in.Country) && contains(c.Currencies, in.Currency) && contains(c.Categories, in.Category) && in.AmountMinor >= c.MinMinor && in.AmountMinor <= c.MaxMinor && (!in.RequiresCustody || c.CustodyApproved) && (in.ProviderConstraint == nil || *in.ProviderConstraint == c.ID) {
			cs = append(cs, c)
		}
	}
	if e = rows.Err(); e != nil {
		return ProviderConfig{}, e
	}
	if len(cs) == 0 {
		return ProviderConfig{}, core.Fail(503, "NO_ELIGIBLE_PROVIDER")
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Priority != cs[j].Priority {
			return cs[i].Priority < cs[j].Priority
		}
		if cs[i].FeeBasisPoints != cs[j].FeeBasisPoints {
			return cs[i].FeeBasisPoints < cs[j].FeeBasisPoints
		}
		return cs[i].ID.String() < cs[j].ID.String()
	})
	return cs[0], nil
}
func (s *Service) CreateIntent(ctx context.Context, in IntentInput, key string) (Intent, error) {
	walletCtx := ctx
	domain := ""
	if in.LockedQuoteID != nil {
		p, err := core.Caller(ctx)
		if err != nil {
			return Intent{}, err
		}
		var valid bool
		err = s.DB.QueryRow(ctx, `SELECT q.source_domain,(q.source_service=$2 AND q.payer_wallet_id=$3 AND q.amount_minor=$4 AND q.currency=$5 AND q.snapshot->>'quote_version'=$6 AND q.snapshot->>'jurisdiction'=$7 AND p.version=$8 AND p.policy_key=$9 AND COALESCE((p.snapshot->>'requires_custody')::boolean,false)=$10) FROM locked_quotes q JOIN financial_policies p USING(policy_id) WHERE quote_id=$1`, in.LockedQuoteID, p.Service, in.WalletID, in.AmountMinor, in.Currency, in.EvidenceRef, in.Country, in.PolicyVersion, in.Category, in.RequiresCustody).Scan(&domain, &valid)
		if err != nil || !valid {
			return Intent{}, core.Fail(403, "LOCKED_QUOTE_CONTEXT_MISMATCH")
		}
		if err = core.DomainCaller(ctx, domain); err != nil {
			return Intent{}, err
		}
		walletCtx = internal(ctx)
	}
	w, e := s.OwnWallet(walletCtx, in.WalletID)
	if e != nil {
		return Intent{}, e
	}
	if domain == "" {
		domain = w.Domain
	}
	if e = core.DomainCaller(ctx, domain); e != nil {
		return Intent{}, e
	}
	if s.Environment == "production" {
		return Intent{}, core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	if w.State != "ACTIVE" || in.AmountMinor <= 0 || in.RequiredLiquidityMinor < 0 || in.Currency != w.Currency || in.EvidenceRef == "" || in.PolicyVersion == "" || key == "" || len(key) > 128 || in.Email == "" {
		return Intent{}, core.Fail(422, "BUSINESS_EVIDENCE_REQUIRED")
	}
	p, _ := core.Caller(ctx)
	fp := core.Fingerprint(in)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Intent{}, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(p.Service)+":"+key)
	if e != nil {
		return Intent{}, e
	}
	var existing ids.ID
	var prev string
	e = tx.QueryRow(ctx, `SELECT intent_id,fingerprint FROM payment_intents WHERE source_service=$1 AND operation_key=$2`, p.Service, key).Scan(&existing, &prev)
	if e == nil {
		if prev != fp {
			return Intent{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return s.readIntent(ctx, tx, existing)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Intent{}, e
	}
	c, e := s.route(ctx, in, w)
	if e != nil {
		return Intent{}, e
	}
	i := Intent{ID: ids.New(), WalletID: w.WalletID, AmountMinor: in.AmountMinor, Currency: in.Currency, LegalEntity: w.LegalEntity, ProviderAccountID: c.ID, PoolID: c.PoolID, State: "REQUESTED", SettlementState: "PENDING"}
	i.Reference = "cf-" + i.ID.String()
	raw, _ := json.Marshal(c)
	_, e = tx.Exec(ctx, `INSERT INTO payment_intents(intent_id,source_service,wallet_id,operation_key,fingerprint,amount_minor,currency,legal_entity,category,country,evidence_ref,policy_version,provider_account_id,pool_id,route_snapshot,provider_reference,state,locked_quote_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, i.ID, p.Service, w.WalletID, key, fp, in.AmountMinor, in.Currency, w.LegalEntity, in.Category, in.Country, in.EvidenceRef, in.PolicyVersion, c.ID, c.PoolID, string(raw), i.Reference, i.State, in.LockedQuoteID)
	if e != nil {
		return Intent{}, e
	}
	if e = s.Emit(ctx, tx, "finance.payment.intent_created.v1", "payment_intent", i.ID, i); e != nil {
		return Intent{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Intent{}, e
	}
	// Persist SUBMITTING before an external side effect. A crash leaves an
	// unknown attempt; no retry path performs another initialization.
	tag, e := s.ChangeProviderState(internal(ctx), "COLLECTION", i.ID, "REQUESTED", "SUBMITTING", "")
	if e != nil {
		return i, e
	}
	if tag.RowsAffected() != 1 {
		return s.GetIntent(ctx, i.ID)
	}
	a, e := s.Adapter(c.ID)
	if e != nil {
		return i, e
	}
	if a.Collection == nil {
		return i, core.Fail(503, "PROVIDER_UNAVAILABLE")
	}
	initialized, callErr := a.Collection.InitializeCharge(ctx, providers.ChargeRequest{Reference: i.Reference, Email: in.Email, Currency: in.Currency, IntentID: i.ID.String(), AmountMinor: in.AmountMinor, Channels: []string{"card", "bank_transfer"}})
	state := "INITIALIZED"
	checkout := initialized.CheckoutURL
	if callErr != nil || initialized.Reference != i.Reference {
		state = "UNKNOWN_OUTCOME"
		checkout = ""
	}
	_, e = s.ChangeProviderState(internal(ctx), "COLLECTION", i.ID, "SUBMITTING", state, checkout)
	if e != nil {
		return i, e
	}
	s.observe(ctx, c.ID, "INITIALIZE", i.Reference, state)
	return s.GetIntent(ctx, i.ID)
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Service) readIntent(ctx context.Context, q querier, id ids.ID) (i Intent, e error) {
	e = q.QueryRow(ctx, `SELECT intent_id,wallet_id,amount_minor,currency,legal_entity,provider_account_id,pool_id,provider_reference,state,settlement_state,checkout_url FROM payment_intents WHERE intent_id=$1`, id).Scan(&i.ID, &i.WalletID, &i.AmountMinor, &i.Currency, &i.LegalEntity, &i.ProviderAccountID, &i.PoolID, &i.Reference, &i.State, &i.SettlementState, &i.CheckoutURL)
	return
}
func (s *Service) GetIntent(ctx context.Context, id ids.ID) (Intent, error) {
	i, e := s.readIntent(ctx, s.DB, id)
	if e != nil {
		return Intent{}, core.Fail(404, "INTENT_NOT_FOUND")
	}
	p, e := core.Caller(ctx)
	if e != nil {
		return Intent{}, e
	}
	if p.Service != platform.PaymentService {
		var source string
		e = s.DB.QueryRow(ctx, `SELECT source_service FROM payment_intents WHERE intent_id=$1`, id).Scan(&source)
		if e != nil {
			return Intent{}, e
		}
		if p.Service != platform.APIGateway && source != string(p.Service) {
			return Intent{}, core.Fail(403, "SOURCE_NOT_AUTHORIZED")
		}
		var canonical bool
		e = s.DB.QueryRow(ctx, `SELECT locked_quote_id IS NOT NULL FROM payment_intents WHERE intent_id=$1`, id).Scan(&canonical)
		if e != nil {
			return Intent{}, e
		}
		if canonical && p.Service != platform.APIGateway && source == string(p.Service) {
			return i, nil
		}
		if _, e = s.OwnWallet(ctx, i.WalletID); e != nil {
			return Intent{}, e
		}
	}
	return i, nil
}
func internal(ctx context.Context) context.Context {
	return serviceauth.WithPrincipal(ctx, serviceauth.Principal{Service: platform.PaymentService})
}
func (s *Service) VerifyIntent(ctx context.Context, id ids.ID) (Intent, error) {
	i, e := s.GetIntent(ctx, id)
	if e != nil {
		return i, e
	}
	a, e := s.Adapter(i.ProviderAccountID)
	if e != nil {
		return i, e
	}
	if a.Collection == nil {
		return i, core.Fail(503, "PROVIDER_UNAVAILABLE")
	}
	c, e := a.Collection.VerifyCharge(ctx, i.Reference)
	if e != nil {
		s.observe(ctx, i.ProviderAccountID, "VERIFY", i.Reference, "ERROR")
		return i, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	if c.Reference != i.Reference || c.AmountMinor != i.AmountMinor || c.Currency != i.Currency || c.IntentID != i.ID.String() {
		return i, core.Fail(409, "CHARGE_CONTEXT_MISMATCH")
	}
	var route ProviderConfig
	var raw []byte
	e = s.DB.QueryRow(ctx, `SELECT route_snapshot FROM payment_intents WHERE intent_id=$1`, id).Scan(&raw)
	if e != nil {
		return i, e
	}
	if e = json.Unmarshal(raw, &route); e != nil {
		return i, e
	}
	if c.Mode != route.Mode || route.MerchantRef != formatMerchant(c.MerchantID) {
		return i, core.Fail(409, "CHARGE_CONTEXT_MISMATCH")
	}
	ctx = internal(ctx)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return i, e
	}
	defer tx.Rollback(ctx)
	var state string
	e = tx.QueryRow(ctx, `SELECT state FROM payment_intents WHERE intent_id=$1 FOR UPDATE`, id).Scan(&state)
	if e != nil {
		return i, e
	}
	if state == "CANCELLED" {
		return i, core.Fail(409, "CANCELLED_INTENT_READBACK_REVIEW_REQUIRED")
	}
	if state == "VERIFIED_FAILURE" && c.State == "VERIFIED_FAILURE" {
		return s.readIntent(ctx, tx, id)
	}
	if state == "VERIFIED_SUCCESS" || state == "VERIFIED_FAILURE" {
		if state == "VERIFIED_SUCCESS" && c.State == "VERIFIED_FAILURE" || state == "VERIFIED_FAILURE" && c.State == "VERIFIED_SUCCESS" {
			_, e = tx.Exec(ctx, `UPDATE custody_pools SET restricted=true WHERE pool_id=$1`, i.PoolID)
			if e != nil {
				return i, e
			}
			_, e = tx.Exec(ctx, `UPDATE payment_intents SET state='RECONCILIATION_EXCEPTION',settlement_state='EXCEPTION',updated_at=now() WHERE intent_id=$1`, id)
			if e != nil {
				return i, e
			}
			if e = s.Emit(ctx, tx, "finance.reconciliation.exception_detected.v1", "payment_intent", id, map[string]any{"intent_id": id, "reason": "COLLECTED_CHARGE_REVERSED"}); e != nil {
				return i, e
			}
			if e = tx.Commit(ctx); e != nil {
				return i, e
			}
			return s.GetIntent(ctx, id)
		}
		return s.readIntent(ctx, tx, id)
	}
	if state == "RECONCILIATION_EXCEPTION" {
		return s.readIntent(ctx, tx, id)
	}
	if c.State == "VERIFIED_SUCCESS" {
		var active bool
		e = tx.QueryRow(ctx, `SELECT i.state='ACTIVE' FROM wallets w JOIN financial_identities i USING(identity_id) WHERE wallet_id=$1`, i.WalletID).Scan(&active)
		if e != nil {
			return i, e
		}
		liabilityKind := "PAYMENT_PENDING"
		if !active {
			liabilityKind = "FROZEN"
		}
		liability, e := s.Account(ctx, tx, &i.WalletID, i.PoolID, liabilityKind, i.LegalEntity, i.Currency, "")
		if e != nil {
			return i, e
		}
		receivable, e := s.Account(ctx, tx, nil, i.PoolID, "PROVIDER_RECEIVABLE", i.LegalEntity, i.Currency, "")
		if e != nil {
			return i, e
		}
		for _, kind := range core.Views {
			if _, e = s.Account(ctx, tx, &i.WalletID, i.PoolID, kind, i.LegalEntity, i.Currency, ""); e != nil {
				return i, e
			}
		}
		journal, e := s.PostTx(ctx, tx, core.Posting{OperationRef: "charge:" + i.ProviderAccountID.String() + ":" + i.Reference, LegalEntity: i.LegalEntity, Currency: i.Currency, PoolID: i.PoolID, PolicyVersion: "collection-confirmation-v1", Authorization: map[string]string{"evidence_ref": i.Reference, "approved_by": "verified-provider-readback"}, Entries: []core.Entry{{AccountID: receivable, Side: "DEBIT", AmountMinor: i.AmountMinor}, {AccountID: liability, Side: "CREDIT", AmountMinor: i.AmountMinor}}})
		if e != nil {
			return i, e
		}
		_, e = tx.Exec(ctx, `UPDATE payment_intents SET state='VERIFIED_SUCCESS',credited_journal_id=$2,updated_at=now() WHERE intent_id=$1`, id, journal)
		if e != nil {
			return i, e
		}
		if e = s.Emit(ctx, tx, "finance.payment.collected.v1", "payment_intent", id, map[string]any{"intent_id": id, "amount_minor": i.AmountMinor, "currency": i.Currency, "settlement_state": "PENDING"}); e != nil {
			return i, e
		}
		var obligation ids.ID
		err := tx.QueryRow(ctx, `SELECT o.obligation_id FROM rent_obligations o JOIN payment_intents p ON p.locked_quote_id=o.quote_id WHERE p.intent_id=$1 AND o.stream='RENT'`, id).Scan(&obligation)
		if err == nil {
			if e = s.Emit(ctx, tx, "finance.rent_payment_verified.v1", "rent_obligation", obligation, map[string]any{"obligation_id": obligation, "intent_id": id, "amount_minor": i.AmountMinor, "currency": i.Currency, "settlement_state": "PENDING"}); e != nil {
				return i, e
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return i, err
		}
	} else if c.State == "VERIFIED_FAILURE" || c.State == "PENDING" || c.State == "PROCESSING" || c.State == "UNKNOWN_OUTCOME" {
		_, e = tx.Exec(ctx, `UPDATE payment_intents SET state=$2,updated_at=now() WHERE intent_id=$1 AND state<>'VERIFIED_SUCCESS'`, id, c.State)
		if e != nil {
			return i, e
		}
		if state != c.State {
			if e = s.Emit(ctx, tx, "finance.provider_state_changed.v1", "provider_attempt", id, StateChange{id, "COLLECTION", state, c.State}); e != nil {
				return i, e
			}
		}
	} else {
		return i, core.Fail(409, "UNKNOWN_PROVIDER_STATUS")
	}
	if e = tx.Commit(ctx); e != nil {
		return i, e
	}
	s.observe(ctx, i.ProviderAccountID, "VERIFY", i.Reference, c.State, map[string]any{"amount_minor": c.AmountMinor, "fee_minor": c.FeeMinor, "currency": c.Currency})
	return s.GetIntent(ctx, id)
}
func (s *Service) observe(ctx context.Context, provider ids.ID, op, ref, outcome string, metadata ...any) {
	raw := []byte(`{}`)
	if len(metadata) > 0 {
		raw, _ = json.Marshal(metadata[0])
	}
	_, e := s.DB.Exec(ctx, `INSERT INTO provider_observations(observation_id,provider_account_id,operation,external_ref,outcome,metadata) VALUES($1,$2,$3,$4,$5,$6)`, ids.New(), provider, op, ref, outcome, string(raw))
	s.Log.InfoContext(ctx, "financial provider operation", "provider_account_id", provider, "operation", op, "reference", ref, "outcome", outcome, "audit_persisted", e == nil)
}

// Migration is unavailable until Section 4+ supplies approved funding movement,
// reconciliation and corresponding migration journals. Never mutate the route.
func (s *Service) MigrateProvider(context.Context, ids.ID, ids.ID) error {
	return core.Fail(403, "PROVIDER_MIGRATION_EVIDENCE_REQUIRED")
}
func (s *Service) RecoverUnknown(ctx context.Context) error {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		return e
	}
	rows, e := s.DB.Query(ctx, `SELECT intent_id FROM payment_intents WHERE state IN ('REQUESTED','SUBMITTING','UNKNOWN_OUTCOME','PENDING','PROCESSING') AND NOT EXISTS(SELECT 1 FROM original_readback_jobs j WHERE j.kind='COLLECTION' AND j.aggregate_id=intent_id AND (j.state IN ('DEAD','COMPLETED') OR j.next_attempt_at>now() OR j.lease_until>now())) AND updated_at<now()-interval '5 seconds' ORDER BY updated_at LIMIT 50`)
	if e != nil {
		return e
	}
	defer rows.Close()
	idsToRead := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			return e
		}
		idsToRead = append(idsToRead, id)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	rows.Close()
	for _, id := range idsToRead {
		token, claimed, e := s.ClaimReadback(ctx, "COLLECTION", id)
		if e != nil {
			return e
		}
		if !claimed {
			continue
		}
		out, readErr := s.VerifyIntent(ctx, id)
		if e = s.FinishReadback(ctx, "COLLECTION", id, token, readErr == nil && (out.State == "VERIFIED_SUCCESS" || out.State == "VERIFIED_FAILURE" || out.State == "RECONCILIATION_EXCEPTION")); e != nil {
			return e
		}
	}
	return nil
}
func formatMerchant(x int64) string { return strconv.FormatInt(x, 10) }
func intToString(x int64) string    { return strconv.FormatInt(x, 10) }
