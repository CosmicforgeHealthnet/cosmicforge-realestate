package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"time"
)

type Authority struct {
	ID            ids.ID    `json:"authority_id"`
	Domain        string    `json:"domain"`
	BusinessRef   string    `json:"business_ref"`
	WalletID      *ids.ID   `json:"wallet_id,omitempty"`
	LegalPartyRef string    `json:"legal_party_ref"`
	Role          string    `json:"role"`
	Currency      string    `json:"currency"`
	LegalEntity   string    `json:"legal_entity"`
	Components    []string  `json:"components"`
	EvidenceRef   string    `json:"evidence_ref"`
	MandateRef    string    `json:"mandate_ref"`
	OwnershipBPS  int       `json:"ownership_bps"`
	MaxMinor      int64     `json:"max_minor"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (s *Service) PutAuthority(ctx context.Context, a Authority) (Authority, error) {
	p, e := core.Caller(ctx)
	if e != nil {
		return a, e
	}
	if a.Role == "PLATFORM" || a.Role == "REGULATED_PARTNER" {
		if e = core.Require(ctx, platform.AdminService); e != nil {
			return a, e
		}
	} else if e = core.DomainCaller(ctx, a.Domain); e != nil {
		return a, e
	}
	if a.ID.ValidateV7() != nil || a.BusinessRef == "" || a.EvidenceRef == "" || a.LegalPartyRef == "" || !core.ValidCurrency(a.Currency) || a.LegalEntity == "" || len(a.Components) == 0 || !a.ExpiresAt.After(time.Now()) || a.MaxMinor <= 0 || a.OwnershipBPS < 0 || a.OwnershipBPS > 10000 {
		return a, fail("BENEFICIARY_AUTHORITY_REQUIRED")
	}
	if has([]string{"OWNER", "CO_OWNER", "CORPORATE_OWNER"}, a.Role) && (a.OwnershipBPS == 0 || !has([]string{"individual-owner", "business-operations"}, a.Domain)) {
		return a, fail("VERIFIED_OWNERSHIP_REQUIRED")
	}
	if has([]string{"MANAGER", "MANAGEMENT_COMPANY"}, a.Role) {
		if a.MandateRef == "" || !has([]string{"individual-owner", "business-operations"}, a.Domain) {
			return a, fail("MANAGER_MANDATE_REQUIRED")
		}
		for _, c := range a.Components {
			if c != "MANAGER_FEE" && c != "INSPECTION" && c != "SERVICE" && c != "BOOKING" {
				return a, fail("MANAGER_FEE_ONLY")
			}
		}
	}
	if has([]string{"PROFESSIONAL", "PROFESSIONAL_FIRM"}, a.Role) && a.MandateRef == "" {
		return a, fail("PROFESSIONAL_MANDATE_REQUIRED")
	}
	if a.WalletID != nil {
		var ok bool
		e = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallets w JOIN financial_identities i USING(identity_id) JOIN verification_refs v USING(identity_id) WHERE w.wallet_id=$1 AND w.role=$2 AND w.currency=$3 AND w.legal_entity=$4 AND i.legal_party_ref=$5 AND i.state='ACTIVE' AND v.state='VERIFIED' AND v.expires_at>now())`, a.WalletID, a.Role, a.Currency, a.LegalEntity, a.LegalPartyRef).Scan(&ok)
		if e != nil {
			return a, e
		}
		if !ok {
			return a, core.Fail(403, "VERIFIED_BENEFICIARY_REQUIRED")
		}
	} else if a.Role != "PLATFORM" {
		return a, fail("BENEFICIARY_WALLET_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return a, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "authority:"+a.ID.String()); e != nil {
		return a, e
	}
	var prior Authority
	e = load(ctx, tx, `SELECT snapshot FROM beneficiary_authorities WHERE authority_id=$1`, a.ID, &prior)
	if e == nil {
		if core.Fingerprint(prior) != core.Fingerprint(a) {
			return a, core.Fail(409, "AUTHORITY_IMMUTABLE")
		}
		return prior, nil
	}
	_, e = tx.Exec(ctx, `INSERT INTO beneficiary_authorities(authority_id,source_service,business_ref,wallet_id,fingerprint,snapshot,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, a.ID, p.Service, a.BusinessRef, a.WalletID, core.Fingerprint(a), raw(a), a.ExpiresAt)
	if e != nil {
		return a, e
	}
	if e = s.Audit(ctx, tx, "beneficiary.authorized", a.ID.String(), a); e != nil {
		return a, e
	}
	return a, tx.Commit(ctx)
}
func (s *Service) RevokeAuthority(ctx context.Context, id ids.ID, evidence string) error {
	p, e := core.Caller(ctx)
	if e != nil {
		return e
	}
	if evidence == "" {
		return fail("REVOCATION_EVIDENCE_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var source string
	if e = tx.QueryRow(ctx, `SELECT source_service FROM beneficiary_authorities WHERE authority_id=$1 FOR UPDATE`, id).Scan(&source); e != nil {
		return e
	}
	if source != string(p.Service) && p.Service != platform.AdminService {
		return core.Fail(403, "SOURCE_NOT_AUTHORIZED")
	}
	_, e = tx.Exec(ctx, `UPDATE beneficiary_authorities SET revoked=true WHERE authority_id=$1`, id)
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "beneficiary.revoked", id.String(), map[string]string{"evidence_ref": evidence}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type Component struct {
	Ref                string `json:"component_ref"`
	Kind               string `json:"kind"`
	AmountMinor        int64  `json:"amount_minor"`
	PayerRef           string `json:"payer_ref"`
	Recurrence         string `json:"recurrence"`
	ServiceEvidenceRef string `json:"service_evidence_ref,omitempty"`
}
type Quote struct {
	ID               ids.ID      `json:"quote_id"`
	Domain           string      `json:"domain"`
	BusinessRef      string      `json:"business_ref"`
	PayerWalletID    ids.ID      `json:"payer_wallet_id"`
	PayerType        string      `json:"payer_type"`
	PayerConsentRef  string      `json:"payer_consent_ref"`
	LegalPartyRef    string      `json:"legal_party_ref"`
	QuoteVersion     string      `json:"quote_version"`
	AgreementRef     string      `json:"agreement_ref"`
	AgreementVersion string      `json:"agreement_version"`
	PolicyID         ids.ID      `json:"policy_id"`
	Currency         string      `json:"currency"`
	Jurisdiction     string      `json:"jurisdiction"`
	Schedule         string      `json:"schedule"`
	AmountMinor      int64       `json:"amount_minor"`
	Components       []Component `json:"components"`
}

func (s *Service) validateQuote(ctx context.Context, q Quote) error {
	return s.validateQuoteWith(ctx, s.DB, q)
}
func (s *Service) validateQuoteWith(ctx context.Context, db query, q Quote) error {
	if e := core.DomainCaller(ctx, q.Domain); e != nil {
		return e
	}
	var p Policy
	e := load(ctx, db, `SELECT snapshot FROM financial_policies WHERE policy_id=$1`, q.PolicyID, &p)
	if e != nil {
		return e
	}
	if e = policyAt(p, time.Now()); e != nil {
		return e
	}
	var disabled bool
	e = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM policy_deactivations WHERE policy_id=$1 AND effective_at<=now())`, p.ID).Scan(&disabled)
	if e != nil {
		return e
	}
	if disabled {
		return core.Fail(403, "POLICY_DISABLED_OR_NOT_EFFECTIVE")
	}
	f, _ := family(p.Key)
	if q.ID.ValidateV7() != nil || q.BusinessRef == "" || q.QuoteVersion == "" || q.AgreementRef == "" || q.AgreementVersion == "" || q.PayerConsentRef == "" || q.LegalPartyRef == "" || q.Currency != p.Currency || q.Jurisdiction != p.Jurisdiction || !has(p.Schedules, q.Schedule) || !has(f.Payers, q.PayerType) || len(q.Components) == 0 {
		return fail("LOCKED_QUOTE_REQUIRED")
	}
	var party, currency, state string
	e = db.QueryRow(ctx, `SELECT i.legal_party_ref,w.currency,i.state FROM wallets w JOIN financial_identities i USING(identity_id) WHERE wallet_id=$1`, q.PayerWalletID).Scan(&party, &currency, &state)
	if e != nil {
		return e
	}
	if party != q.LegalPartyRef || currency != q.Currency || state != "ACTIVE" {
		return core.Fail(403, "PAYER_CONTEXT_MISMATCH")
	}
	seen := map[string]bool{}
	amounts := []int64{}
	for _, c := range q.Components {
		if c.Ref == "" || seen[c.Ref] || c.PayerRef != q.LegalPartyRef || !has(f.Components, c.Kind) || !has([]string{"ONE_TIME", "RECURRING", "EVENT_DRIVEN", "ACTUAL_SERVICE_ONLY", "REFUNDABLE_DEPOSIT", "ADJUSTMENT", "TAX", "PROVIDER_PROCESSING"}, c.Recurrence) {
			return fail("CATEGORY_COMPONENT_MISMATCH")
		}
		seen[c.Ref] = true
		if c.Kind == "DEPOSIT" && c.Recurrence != "REFUNDABLE_DEPOSIT" {
			return fail("DEPOSIT_MUST_BE_SEPARATE")
		}
		if (c.Kind == "LEGAL" && (c.Recurrence != "ACTUAL_SERVICE_ONLY" || c.ServiceEvidenceRef == "")) || (has([]string{"ONBOARDING", "PLATFORM"}, c.Kind) && c.Recurrence != "ONE_TIME") {
			return fail("INITIAL_CHARGE_RECURRENCE_FORBIDDEN")
		}
		amounts = append(amounts, c.AmountMinor)
	}
	total, e := sum(amounts)
	if e != nil {
		return e
	}
	if total != q.AmountMinor {
		return fail("QUOTE_PAYABLE_MISMATCH")
	}
	return nil
}
func (s *Service) LockQuote(ctx context.Context, q Quote, key string) (Quote, error) {
	if key == "" || len(key) > 128 {
		return q, fail("IDEMPOTENCY_KEY_REQUIRED")
	}
	if e := s.validateQuote(ctx, q); e != nil {
		return q, e
	}
	p, _ := core.Caller(ctx)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return q, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "quote:"+string(p.Service)+key); e != nil {
		return q, e
	}
	var id ids.ID
	var fp string
	e = tx.QueryRow(ctx, `SELECT quote_id,fingerprint FROM locked_quotes WHERE source_service=$1 AND operation_key=$2`, p.Service, key).Scan(&id, &fp)
	if e == nil {
		if fp != core.Fingerprint(q) {
			return q, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return s.quote(ctx, tx, id)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return q, e
	}
	if e = insertQuote(ctx, tx, q, key, string(p.Service)); e != nil {
		return q, e
	}
	if e = s.Audit(ctx, tx, "quote.locked", q.ID.String(), q); e != nil {
		return q, e
	}
	return q, tx.Commit(ctx)
}
func insertQuote(ctx context.Context, tx pgx.Tx, q Quote, key, source string) error {
	_, e := tx.Exec(ctx, `INSERT INTO locked_quotes(quote_id,source_service,source_domain,business_ref,operation_key,fingerprint,payer_wallet_id,policy_id,amount_minor,currency,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, q.ID, source, q.Domain, q.BusinessRef, key, core.Fingerprint(q), q.PayerWalletID, q.PolicyID, q.AmountMinor, q.Currency, raw(q))
	return e
}
func (s *Service) quote(ctx context.Context, q query, id ids.ID) (x Quote, e error) {
	e = load(ctx, q, `SELECT snapshot FROM locked_quotes WHERE quote_id=$1`, id, &x)
	return
}
func (s *Service) Quote(ctx context.Context, id ids.ID) (Quote, error) {
	q, e := s.quote(ctx, s.DB, id)
	if e != nil {
		return q, e
	}
	p, e := core.Caller(ctx)
	if e != nil {
		return q, e
	}
	var source string
	e = s.DB.QueryRow(ctx, `SELECT source_service FROM locked_quotes WHERE quote_id=$1`, id).Scan(&source)
	if e != nil {
		return q, e
	}
	if string(p.Service) != source && p.Service != platform.PaymentService {
		if _, e = s.OwnWallet(ctx, q.PayerWalletID); e != nil {
			return Quote{}, e
		}
	}
	return q, nil
}

type AllocationInput struct {
	IntentID      ids.ID      `json:"intent_id"`
	Version       int         `json:"version"`
	SupersedesID  *ids.ID     `json:"supersedes_id,omitempty"`
	CorrectionRef string      `json:"correction_ref,omitempty"`
	Lines         []LineInput `json:"lines"`
}
type LineInput struct {
	AuthorityID  ids.ID `json:"authority_id"`
	ComponentRef string `json:"component_ref"`
	Kind         string `json:"kind"`
	AmountMinor  int64  `json:"amount_minor"`
	Currency     string `json:"currency"`
}
type AllocationLine struct {
	ID ids.ID `json:"line_id"`
	LineInput
	WalletID      *ids.ID `json:"wallet_id,omitempty"`
	State         string  `json:"state"`
	ReleasedMinor int64   `json:"released_minor"`
	ReservedMinor int64   `json:"reserved_minor"`
	PaidMinor     int64   `json:"paid_minor"`
}
type Allocation struct {
	ID          ids.ID           `json:"allocation_id"`
	IntentID    ids.ID           `json:"intent_id"`
	Version     int              `json:"version"`
	QuoteID     ids.ID           `json:"quote_id"`
	PolicyID    ids.ID           `json:"policy_id"`
	PoolID      ids.ID           `json:"pool_id"`
	Currency    string           `json:"currency"`
	LegalEntity string           `json:"legal_entity"`
	Lines       []AllocationLine `json:"lines"`
}

func (s *Service) allocation(ctx context.Context, q query, id ids.ID) (a Allocation, e error) {
	e = load(ctx, q, `SELECT snapshot FROM allocation_snapshots WHERE allocation_id=$1`, id, &a)
	return
}
func (s *Service) Allocation(ctx context.Context, id ids.ID) (Allocation, error) {
	a, e := s.allocation(ctx, s.DB, id)
	if e != nil {
		return a, e
	}
	p, e := core.Caller(ctx)
	if e != nil {
		return a, e
	}
	if p.Service != platform.PaymentService {
		var source string
		e = s.DB.QueryRow(ctx, `SELECT p.source_service FROM allocation_snapshots a JOIN payment_intents p USING(intent_id) WHERE allocation_id=$1`, id).Scan(&source)
		if e != nil {
			return a, e
		}
		if string(p.Service) != source {
			allowed := false
			for _, l := range a.Lines {
				if l.WalletID != nil {
					if _, err := s.OwnWallet(ctx, *l.WalletID); err == nil {
						allowed = true
					}
				}
			}
			if !allowed {
				return Allocation{}, core.Fail(403, "SOURCE_NOT_AUTHORIZED")
			}
			filtered := []AllocationLine{}
			for _, l := range a.Lines {
				if l.WalletID != nil {
					if _, err := s.OwnWallet(ctx, *l.WalletID); err == nil {
						filtered = append(filtered, l)
					}
				}
			}
			a.Lines = filtered
		}
	}
	for n := range a.Lines {
		l := &a.Lines[n]
		e = s.DB.QueryRow(ctx, `SELECT state,released_minor,reserved_minor,paid_minor FROM allocation_lines WHERE line_id=$1`, l.ID).Scan(&l.State, &l.ReleasedMinor, &l.ReservedMinor, &l.PaidMinor)
		if e != nil {
			return a, e
		}
	}
	return a, nil
}
func (s *Service) Allocate(ctx context.Context, in AllocationInput) (Allocation, error) {
	i, e := s.GetIntent(ctx, in.IntentID)
	if e != nil {
		return Allocation{}, e
	}
	p, _ := core.Caller(ctx)
	var source string
	var quoteID *ids.ID
	e = s.DB.QueryRow(ctx, `SELECT source_service,locked_quote_id FROM payment_intents WHERE intent_id=$1`, i.ID).Scan(&source, &quoteID)
	if e != nil {
		return Allocation{}, e
	}
	if source != string(p.Service) || quoteID == nil {
		return Allocation{}, core.Fail(403, "CANONICAL_QUOTE_SOURCE_REQUIRED")
	}
	q, e := s.quote(ctx, s.DB, *quoteID)
	if e != nil {
		return Allocation{}, e
	}
	policy, e := s.Policy(ctx, q.PolicyID)
	if e != nil {
		return Allocation{}, e
	}
	if in.Version < 1 || len(in.Lines) == 0 || len(in.Lines) > 100 {
		return Allocation{}, fail("INVALID_ALLOCATION")
	}
	ctx = trusted(ctx)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Allocation{}, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "allocate:"+i.ID.String()); e != nil {
		return Allocation{}, e
	}
	var existing ids.ID
	var fp string
	e = tx.QueryRow(ctx, `SELECT allocation_id,fingerprint FROM allocation_snapshots WHERE intent_id=$1 AND version=$2`, i.ID, in.Version).Scan(&existing, &fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return Allocation{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return s.allocation(ctx, tx, existing)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Allocation{}, e
	}
	var state string
	e = tx.QueryRow(ctx, `SELECT state FROM payment_intents WHERE intent_id=$1 FOR UPDATE`, i.ID).Scan(&state)
	if e != nil {
		return Allocation{}, e
	}
	if state != "VERIFIED_SUCCESS" {
		return Allocation{}, core.Fail(409, "PAYMENT_NOT_VERIFIED")
	}
	var versions int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM allocation_snapshots WHERE intent_id=$1`, i.ID).Scan(&versions)
	if e != nil {
		return Allocation{}, e
	}
	if in.Version != versions+1 || (versions == 0) != (in.SupersedesID == nil) {
		return Allocation{}, core.Fail(409, "ALLOCATION_VERSION_CONFLICT")
	}
	if in.SupersedesID != nil {
		if in.CorrectionRef == "" || policy.AdjustmentRef == "" {
			return Allocation{}, fail("CORRECTION_AUTHORITY_REQUIRED")
		}
		old, e := s.allocation(ctx, tx, *in.SupersedesID)
		if e != nil {
			return old, e
		}
		if old.IntentID != i.ID || old.Version != in.Version-1 {
			return old, fail("CORRECTION_VERSION_MISMATCH")
		}
		var busy bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM allocation_lines WHERE allocation_id=$1 AND (released_minor>0 OR state IN ('DISPUTED','HELD','REVERSED','CORRECTED')))`, old.ID).Scan(&busy)
		if e != nil {
			return old, e
		}
		if busy {
			return old, core.Fail(409, "RELEASED_OR_HELD_CORRECTION_REQUIRES_REVIEW")
		}
		if e = s.reverseAllocation(ctx, tx, old, in.CorrectionRef, policy.Version); e != nil {
			return old, e
		}
	}
	a := Allocation{ID: ids.New(), IntentID: i.ID, Version: in.Version, QuoteID: q.ID, PolicyID: q.PolicyID, PoolID: i.PoolID, Currency: i.Currency, LegalEntity: i.LegalEntity, Lines: []AllocationLine{}}
	comps := map[string]Component{}
	for _, c := range q.Components {
		comps[c.Ref] = c
	}
	totals := map[string]int64{}
	authorityTotals := map[ids.ID]int64{}
	owners := map[string][]struct {
		amount int64
		bps    int
	}{}
	entries := []core.Entry{}
	f, _ := family(policy.Key)
	pending, e := s.Account(ctx, tx, &i.WalletID, i.PoolID, "PAYMENT_PENDING", i.LegalEntity, i.Currency, "")
	if e != nil {
		return a, e
	}
	for _, l := range in.Lines {
		c, ok := comps[l.ComponentRef]
		if !ok || l.AmountMinor <= 0 || l.Currency != i.Currency || !has([]string{"BENEFICIARY", "PLATFORM", "DEPOSIT", "RESERVE", "TAX", "PROCESSING"}, l.Kind) {
			return a, fail("ALLOCATION_COMPONENT_MISMATCH")
		}
		if l.AmountMinor > c.AmountMinor-totals[c.Ref] {
			return a, fail("OVER_ALLOCATION")
		}
		totals[c.Ref] += l.AmountMinor
		var auth Authority
		var b []byte
		var issuer string
		e = tx.QueryRow(ctx, `SELECT snapshot,source_service FROM beneficiary_authorities WHERE authority_id=$1 AND NOT revoked AND expires_at>now() FOR SHARE`, l.AuthorityID).Scan(&b, &issuer)
		if e != nil {
			return a, core.Fail(403, "CURRENT_BENEFICIARY_AUTHORITY_REQUIRED")
		}
		if e = decode(b, &auth); e != nil {
			return a, e
		}
		if issuer == "" || auth.BusinessRef != q.BusinessRef || auth.Currency != i.Currency || auth.LegalEntity != i.LegalEntity || l.AmountMinor > auth.MaxMinor || (!has(auth.Components, c.Kind) && !(c.Kind == "RENT" && has(auth.Components, "MANAGER_FEE"))) {
			return a, core.Fail(403, "BENEFICIARY_ENTITLEMENT_MISMATCH")
		}
		if l.AmountMinor > auth.MaxMinor-authorityTotals[auth.ID] {
			return a, core.Fail(403, "BENEFICIARY_ENTITLEMENT_LIMIT")
		}
		authorityTotals[auth.ID] += l.AmountMinor
		if l.Kind == "BENEFICIARY" && !has(f.Roles, auth.Role) {
			return a, fail("CATEGORY_BENEFICIARY_MISMATCH")
		}
		if has([]string{"MANAGER", "MANAGEMENT_COMPANY"}, auth.Role) && l.Kind != "BENEFICIARY" {
			return a, fail("MANAGER_FEE_ONLY")
		}
		if auth.WalletID != nil {
			if e = s.currentWallet(ctx, tx, *auth.WalletID, auth.LegalPartyRef); e != nil {
				return a, e
			}
		}
		if has([]string{"OWNER", "CO_OWNER", "CORPORATE_OWNER"}, auth.Role) && l.Kind == "BENEFICIARY" {
			owners[c.Ref] = append(owners[c.Ref], struct {
				amount int64
				bps    int
			}{l.AmountMinor, auth.OwnershipBPS})
		}
		wallet := auth.WalletID
		kind := "PROTECTED"
		target := "AVAILABLE"
		switch l.Kind {
		case "PLATFORM":
			if auth.Role != "PLATFORM" {
				return a, fail("PLATFORM_FEE_AUTHORITY_REQUIRED")
			}
			wallet = nil
			kind = "SETTLEMENT_CONTROL"
			target = "REVENUE"
		case "DEPOSIT":
			if c.Recurrence != "REFUNDABLE_DEPOSIT" {
				return a, fail("DEPOSIT_MUST_BE_SEPARATE")
			}
			wallet = &i.WalletID
			kind = "DEPOSIT"
			target = ""
		case "RESERVE", "TAX":
			wallet = nil
			kind = "RESERVE"
			target = ""
		case "PROCESSING":
			if c.Kind != "PROCESSING" {
				return a, fail("PROCESSING_TREATMENT_MISMATCH")
			}
			wallet = nil
			kind = "PROCESSING_EXPENSE"
			target = ""
		}
		if c.Recurrence == "REFUNDABLE_DEPOSIT" && l.Kind != "DEPOSIT" {
			return a, fail("DEPOSIT_MUST_BE_SEPARATE")
		}
		if kind == "PROTECTED" && wallet == nil {
			return a, fail("BENEFICIARY_WALLET_REQUIRED")
		}
		line := AllocationLine{ID: ids.New(), LineInput: l, WalletID: wallet, State: "PROTECTED"}
		dimension := "allocation:" + line.ID.String()
		protected, e := s.Account(ctx, tx, wallet, i.PoolID, kind, i.LegalEntity, i.Currency, dimension)
		if e != nil {
			return a, e
		}
		var available *ids.ID
		if target != "" {
			id, e := s.Account(ctx, tx, wallet, i.PoolID, target, i.LegalEntity, i.Currency, dimension)
			if e != nil {
				return a, e
			}
			available = &id
		}
		_, e = tx.Exec(ctx, `INSERT INTO allocation_lines(line_id,allocation_id,authority_id,wallet_id,amount_minor,snapshot,protected_account_id,available_account_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'PROTECTED')`, line.ID, a.ID, l.AuthorityID, wallet, l.AmountMinor, raw(line), protected, available)
		// Header is inserted after posting; the FK is deferred within this transaction.
		if e != nil {
			return a, e
		}
		entries = append(entries, core.Entry{AccountID: protected, Side: "CREDIT", AmountMinor: l.AmountMinor})
		a.Lines = append(a.Lines, line)
	}
	for ref, c := range comps {
		if totals[ref] != c.AmountMinor {
			return a, fail("UNEXPLAINED_ALLOCATION_RESIDUAL")
		}
	}
	for _, xs := range owners {
		var total int64
		var bps int
		for _, x := range xs {
			total += x.amount
			bps += x.bps
		}
		if bps != 10000 {
			return a, fail("COOWNER_PERCENTAGES_NOT_RECONCILED")
		}
		for _, x := range xs {
			expected, e := mul(total, x.bps)
			if e != nil || expected != x.amount {
				return a, fail("COOWNER_SHARE_MISMATCH")
			}
		}
	}
	entries = append(entries, core.Entry{AccountID: pending, Side: "DEBIT", AmountMinor: i.AmountMinor})
	journal, e := s.PostTx(ctx, tx, core.Posting{OperationRef: "allocation:" + a.ID.String(), PoolID: i.PoolID, LegalEntity: i.LegalEntity, Currency: i.Currency, PolicyVersion: policy.Version, Authorization: map[string]string{"evidence_ref": q.BusinessRef + ":" + q.QuoteVersion, "approved_by": source}, Entries: entries})
	if e != nil {
		return a, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO allocation_snapshots(allocation_id,intent_id,version,supersedes_id,fingerprint,snapshot,journal_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, a.ID, i.ID, in.Version, in.SupersedesID, core.Fingerprint(in), raw(a), journal)
	if e != nil {
		return a, e
	}
	if e = s.Emit(ctx, tx, "finance.allocation_created.v1", "allocation", a.ID, a); e != nil {
		return a, e
	}
	var obligation ids.ID
	if err := tx.QueryRow(ctx, `SELECT obligation_id FROM rent_obligations WHERE quote_id=$1 AND stream='RENT'`, q.ID).Scan(&obligation); err == nil {
		if e = s.Emit(ctx, tx, "finance.rent_split_pending.v1", "rent_obligation", obligation, map[string]any{"obligation_id": obligation, "intent_id": i.ID, "allocation_id": a.ID, "amount_minor": i.AmountMinor, "currency": i.Currency}); e != nil {
			return a, e
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return a, err
	}
	if e = s.Audit(ctx, tx, "allocation.accepted", a.ID.String(), in); e != nil {
		return a, e
	}
	return a, tx.Commit(ctx)
}
func decode(b []byte, v any) error { return json.Unmarshal(b, v) }
func (s *Service) currentWallet(ctx context.Context, tx pgx.Tx, id ids.ID, party string) error {
	var ok bool
	e := tx.QueryRow(ctx, `SELECT true FROM wallets w JOIN financial_identities i USING(identity_id) JOIN verification_refs v USING(identity_id) WHERE wallet_id=$1 AND i.legal_party_ref=$2 AND i.state='ACTIVE' AND v.state='VERIFIED' AND v.expires_at>now() FOR SHARE OF v`, id, party).Scan(&ok)
	if errors.Is(e, pgx.ErrNoRows) {
		return core.Fail(403, "VERIFIED_BENEFICIARY_REQUIRED")
	}
	if e != nil {
		return e
	}
	if !ok {
		return core.Fail(403, "VERIFIED_BENEFICIARY_REQUIRED")
	}
	return nil
}
func (s *Service) reverseAllocation(ctx context.Context, tx pgx.Tx, a Allocation, evidence, policy string) error {
	var journal ids.ID
	if e := tx.QueryRow(ctx, `SELECT journal_id FROM allocation_snapshots WHERE allocation_id=$1`, a.ID).Scan(&journal); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT account_id,CASE side WHEN 'DEBIT' THEN 'CREDIT' ELSE 'DEBIT' END,amount_minor FROM journal_entries WHERE journal_id=$1 ORDER BY entry_id`, journal)
	if e != nil {
		return e
	}
	entries := []core.Entry{}
	for rows.Next() {
		var x core.Entry
		if e = rows.Scan(&x.AccountID, &x.Side, &x.AmountMinor); e != nil {
			rows.Close()
			return e
		}
		entries = append(entries, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	_, e = s.PostTx(ctx, tx, core.Posting{OperationRef: "allocation-correction:" + a.ID.String(), PoolID: a.PoolID, LegalEntity: a.LegalEntity, Currency: a.Currency, PolicyVersion: policy, Authorization: map[string]string{"evidence_ref": evidence, "approved_by": "source-correction"}, Entries: entries, ReversesID: &journal})
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE allocation_lines SET state='CORRECTED' WHERE allocation_id=$1`, a.ID)
	return e
}
