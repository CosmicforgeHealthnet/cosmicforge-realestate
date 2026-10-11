// Package core owns financial identities, the only ledger and domain authority.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	DB     *pgxpool.Pool
	Outbox *outbox.Store
}

func New(db *pgxpool.Pool) *Service { return &Service{db, outbox.NewStore(db, "")} }
func Fail(status int, code string) error {
	return apperrors.New(status, code, "Financial operation rejected.")
}
func Fingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Caller(ctx context.Context) (serviceauth.Principal, error) {
	p, ok := serviceauth.PrincipalFrom(ctx)
	if !ok {
		return p, apperrors.AuthenticationRequired()
	}
	return p, nil
}
func DomainCaller(ctx context.Context, domain string) error {
	p, e := Caller(ctx)
	if e != nil {
		return e
	}
	owner, ok := platform.ServiceForDomain(platform.Domain(domain))
	if !ok || owner != p.Service || domain == "admin" {
		return Fail(403, "SOURCE_NOT_AUTHORIZED")
	}
	return nil
}
func Require(ctx context.Context, service platform.Service) error {
	p, e := Caller(ctx)
	if e != nil {
		return e
	}
	if p.Service != service {
		return Fail(403, "SOURCE_NOT_AUTHORIZED")
	}
	return nil
}

func validCurrency(code string) bool { _, e := money.ParseCurrency(code); return e == nil }
func ValidCurrency(code string) bool { return validCurrency(code) }

var roles = map[string]map[string]bool{
	"marketplace": {"CUSTOMER": true, "TENANT": true, "AGENT": true, "BROKER": true}, "individual-owner": {"OWNER": true, "CO_OWNER": true},
	"business-operations": {"CORPORATE_OWNER": true, "DEVELOPER": true, "HOSPITALITY": true},
	"property-manager":    {"MANAGER": true, "MANAGEMENT_COMPANY": true}, "artisan": {"ARTISAN": true, "SERVICE_COMPANY": true},
	"professional-services": {"PROFESSIONAL": true, "PROFESSIONAL_FIRM": true},
}

type Provision struct {
	Domain          string `json:"domain"`
	AccountID       ids.ID `json:"account_id"`
	Role            string `json:"role"`
	PartyKind       string `json:"party_kind"`
	LegalEntity     string `json:"legal_entity"`
	Currency        string `json:"currency"`
	RegistrationRef string `json:"registration_ref"`
	Purpose         string `json:"purpose,omitempty"`
}
type Wallet struct {
	WalletID    ids.ID `json:"wallet_id"`
	IdentityID  ids.ID `json:"identity_id"`
	Domain      string `json:"domain"`
	Role        string `json:"role"`
	LegalEntity string `json:"legal_entity"`
	Currency    string `json:"currency"`
	State       string `json:"state"`
}

func (s *Service) EnsureWallet(ctx context.Context, in Provision) (w Wallet, err error) {
	defer func() { recordCommand(ctx, "wallet.ensure", err) }()
	if err = DomainCaller(ctx, in.Domain); err != nil {
		return
	}
	if in.AccountID.ValidateV7() != nil || !roles[in.Domain][in.Role] || (in.PartyKind != "PERSON" && in.PartyKind != "ORGANIZATION") || in.LegalEntity == "" || !validCurrency(in.Currency) || in.RegistrationRef == "" {
		err = Fail(422, "INVALID_WALLET_REGISTRATION")
		return
	}
	organizationRole := in.Role == "CORPORATE_OWNER" || in.Role == "DEVELOPER" || in.Role == "HOSPITALITY" || in.Role == "MANAGEMENT_COMPANY" || in.Role == "SERVICE_COMPANY" || in.Role == "PROFESSIONAL_FIRM"
	if (organizationRole && in.PartyKind != "ORGANIZATION") || (!organizationRole && in.Role != "CUSTOMER" && in.PartyKind != "PERSON") {
		err = Fail(422, "ROLE_LEGAL_PARTY_MISMATCH")
		return
	}
	purpose := "EARNINGS"
	if in.Role == "CUSTOMER" || in.Role == "TENANT" {
		purpose = "PERSONAL"
	}
	if in.Purpose != "" && in.Purpose != purpose {
		return w, Fail(422, "WALLET_PURPOSE_NOT_PERMITTED")
	}
	principal, _ := Caller(ctx)
	if principal.Delegation.HasActor() && (principal.Delegation.ActorID != in.AccountID || string(principal.Delegation.ActorDomain) != in.Domain) {
		return w, Fail(403, "WALLET_REGISTRATION_ACTOR_MISMATCH")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	// Stable domain/account lock; duplicate calls cannot generate divergent parties.
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, in.Domain+":"+in.AccountID.String())
	if err != nil {
		return
	}
	var identity ids.ID
	var kind string
	err = tx.QueryRow(ctx, `SELECT i.identity_id,i.party_kind FROM identity_bindings b JOIN financial_identities i USING(identity_id) WHERE b.domain=$1 AND b.account_id=$2`, in.Domain, in.AccountID).Scan(&identity, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		identity = ids.New()
		kind = in.PartyKind
		_, err = tx.Exec(ctx, `INSERT INTO financial_identities(identity_id,legal_party_ref,party_kind) VALUES($1,$2,$3)`, identity, in.Domain+":"+in.AccountID.String(), kind)
		if err != nil {
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO identity_bindings(domain,account_id,identity_id,registration_ref) VALUES($1,$2,$3,$4)`, in.Domain, in.AccountID, identity, in.RegistrationRef)
	}
	if err != nil {
		return
	}
	if kind != in.PartyKind {
		err = Fail(409, "IDENTITY_KIND_CONFLICT")
		return
	}
	// Serialize purpose creation and financial identity activation without merging old wallets.
	var state string
	if err = tx.QueryRow(ctx, `SELECT state FROM financial_identities WHERE identity_id=$1 FOR UPDATE`, identity).Scan(&state); err != nil {
		return
	}
	if state != "ACTIVE" {
		return w, Fail(403, "FINANCIAL_IDENTITY_RESTRICTED")
	}
	w = Wallet{WalletID: ids.New(), IdentityID: identity, Domain: in.Domain, Role: in.Role, LegalEntity: in.LegalEntity, Currency: in.Currency, State: state}
	err = tx.QueryRow(ctx, `SELECT wallet_id FROM wallet_purposes WHERE identity_id=$1 AND domain=$2 AND purpose=$3 AND legal_entity=$4 AND currency=$5`, identity, in.Domain, purpose, in.LegalEntity, in.Currency).Scan(&w.WalletID)
	created := errors.Is(err, pgx.ErrNoRows)
	if created {
		_, err = tx.Exec(ctx, `INSERT INTO wallets(wallet_id,identity_id,domain,role,legal_entity,currency) VALUES($1,$2,$3,$4,$5,$6)`, w.WalletID, identity, in.Domain, in.Role, in.LegalEntity, in.Currency)
		if err != nil {
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO wallet_purposes(wallet_id,identity_id,domain,purpose,legal_entity,currency) VALUES($1,$2,$3,$4,$5,$6)`, w.WalletID, identity, in.Domain, purpose, in.LegalEntity, in.Currency)
	}
	if err != nil {
		return
	}
	var walletState string
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT state FROM wallet_controls WHERE wallet_id=$1),'ACTIVE')`, w.WalletID).Scan(&walletState)
	if err != nil {
		return
	}
	if walletState != "ACTIVE" {
		return w, Fail(403, "WALLET_RESTRICTED")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO wallet_roles(wallet_id,role,registration_ref) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, w.WalletID, in.Role, in.RegistrationRef)
	if err != nil {
		return
	}
	if tag.RowsAffected() > 0 {
		err = s.Audit(ctx, tx, "wallet.role_attached", w.WalletID.String(), map[string]string{"role": in.Role, "purpose": purpose, "registration_ref": in.RegistrationRef})
		if err != nil {
			return
		}
	}
	if created {
		err = s.Emit(ctx, tx, "finance.wallet.opened.v1", "wallet", w.WalletID, w)
		if err != nil {
			return
		}
	}
	err = tx.Commit(ctx)
	return
}

// LinkAccount accepts verified cross-domain legal-party linkage only from the
// verification authority. It never merges existing parties or ledger history.
func (s *Service) LinkAccount(ctx context.Context, identity ids.ID, domain string, account ids.ID, evidence string) error {
	if e := Require(ctx, platform.VerificationService); e != nil {
		return e
	}
	if identity.ValidateV7() != nil || account.ValidateV7() != nil || roles[domain] == nil || evidence == "" {
		return Fail(422, "LINK_EVIDENCE_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, domain+":"+account.String())
	if e != nil {
		return e
	}
	var existing ids.ID
	var priorEvidence *string
	e = tx.QueryRow(ctx, `SELECT identity_id,link_evidence_ref FROM identity_bindings WHERE domain=$1 AND account_id=$2`, domain, account).Scan(&existing, &priorEvidence)
	if e == nil {
		if existing == identity && priorEvidence != nil && *priorEvidence == evidence {
			return nil
		}
		return Fail(409, "IDENTITY_LINK_CONFLICT")
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO identity_bindings(domain,account_id,identity_id,registration_ref,link_evidence_ref) VALUES($1,$2,$3,$4,$4)`, domain, account, identity, evidence)
	if e != nil {
		return Fail(409, "IDENTITY_LINK_CONFLICT")
	}
	if e = s.Audit(ctx, tx, "identity.linked", identity.String(), map[string]string{"evidence_ref": evidence}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type WalletReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Service) OwnWallet(ctx context.Context, id ids.ID) (Wallet, error) {
	return s.OwnWalletWith(ctx, s.DB, id)
}

// OwnWalletWith preserves authorization while using the caller's transaction.
// Financial commands must not reacquire the pool while holding a transaction.
func (s *Service) OwnWalletWith(ctx context.Context, db WalletReader, id ids.ID) (w Wallet, err error) {
	p, err := Caller(ctx)
	if err != nil {
		return
	}
	if p.Service == platform.PaymentService {
		err = db.QueryRow(ctx, `SELECT w.wallet_id,w.identity_id,w.domain,w.role,w.legal_entity,w.currency,CASE WHEN i.state<>'ACTIVE' THEN i.state ELSE COALESCE((SELECT state FROM wallet_controls c WHERE c.wallet_id=w.wallet_id),'ACTIVE') END FROM wallets w JOIN financial_identities i USING(identity_id) WHERE wallet_id=$1`, id).Scan(&w.WalletID, &w.IdentityID, &w.Domain, &w.Role, &w.LegalEntity, &w.Currency, &w.State)
		return
	}
	if !p.Delegation.HasActor() {
		return w, Fail(403, "ACTOR_REQUIRED")
	}
	if p.Service != platform.APIGateway {
		if err = DomainCaller(ctx, string(p.Delegation.ActorDomain)); err != nil {
			return
		}
	}
	err = db.QueryRow(ctx, `SELECT w.wallet_id,w.identity_id,w.domain,w.role,w.legal_entity,w.currency,CASE WHEN i.state<>'ACTIVE' THEN i.state ELSE COALESCE((SELECT state FROM wallet_controls c WHERE c.wallet_id=w.wallet_id),'ACTIVE') END FROM wallets w JOIN financial_identities i USING(identity_id) JOIN identity_bindings b ON b.identity_id=w.identity_id AND b.domain=w.domain WHERE w.wallet_id=$1 AND b.domain=$2 AND b.account_id=$3`, id, p.Delegation.ActorDomain, p.Delegation.ActorID).Scan(&w.WalletID, &w.IdentityID, &w.Domain, &w.Role, &w.LegalEntity, &w.Currency, &w.State)
	if errors.Is(err, pgx.ErrNoRows) {
		err = Fail(404, "WALLET_NOT_FOUND")
	}
	return
}
func (s *Service) MyWallets(ctx context.Context) ([]Wallet, error) {
	p, e := Caller(ctx)
	if e != nil {
		return nil, e
	}
	if !p.Delegation.HasActor() {
		return nil, Fail(403, "ACTOR_REQUIRED")
	}
	if p.Service != platform.APIGateway {
		if e = DomainCaller(ctx, string(p.Delegation.ActorDomain)); e != nil {
			return nil, e
		}
	}
	rows, e := s.DB.Query(ctx, `SELECT w.wallet_id,w.identity_id,w.domain,w.role,w.legal_entity,w.currency,CASE WHEN i.state<>'ACTIVE' THEN i.state ELSE COALESCE((SELECT state FROM wallet_controls c WHERE c.wallet_id=w.wallet_id),'ACTIVE') END FROM wallets w JOIN financial_identities i USING(identity_id) JOIN identity_bindings b ON b.identity_id=w.identity_id AND b.domain=w.domain WHERE b.domain=$1 AND b.account_id=$2 ORDER BY w.wallet_id`, p.Delegation.ActorDomain, p.Delegation.ActorID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []Wallet{}
	for rows.Next() {
		var w Wallet
		if e = rows.Scan(&w.WalletID, &w.IdentityID, &w.Domain, &w.Role, &w.LegalEntity, &w.Currency, &w.State); e != nil {
			return nil, e
		}
		result = append(result, w)
	}
	return result, rows.Err()
}

type Balance struct {
	Kind         string `json:"kind"`
	PoolID       ids.ID `json:"pool_id"`
	AmountMinor  string `json:"amount_minor"`
	Withdrawable bool   `json:"withdrawable"`
}

func (s *Service) Balances(ctx context.Context, id ids.ID) ([]Balance, error) {
	if _, e := s.OwnWallet(ctx, id); e != nil {
		return nil, e
	}
	rows, e := s.DB.Query(ctx, `SELECT CASE WHEN kind='DEPOSIT' THEN 'PROTECTED' ELSE kind END AS view_kind,pool_id,sum(balance_minor)::text FROM account_balances WHERE wallet_id=$1 AND kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT') GROUP BY view_kind,pool_id ORDER BY pool_id,view_kind`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	bs := []Balance{}
	for rows.Next() {
		var b Balance
		if e = rows.Scan(&b.Kind, &b.PoolID, &b.AmountMinor); e != nil {
			return nil, e
		}
		bs = append(bs, b)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	groups := map[ids.ID]map[string]Balance{}
	for _, b := range bs {
		if groups[b.PoolID] == nil {
			groups[b.PoolID] = map[string]Balance{}
		}
		groups[b.PoolID][b.Kind] = b
	}
	if len(groups) == 0 {
		groups[ids.Nil] = map[string]Balance{}
	}
	pools := make([]ids.ID, 0, len(groups))
	for pool := range groups {
		pools = append(pools, pool)
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].String() < pools[j].String() })
	bs = []Balance{}
	for _, pool := range pools {
		for _, kind := range Views {
			b, ok := groups[pool][kind]
			if !ok {
				b = Balance{Kind: kind, PoolID: pool, AmountMinor: "0"}
			}
			bs = append(bs, b)
		}
	}
	return bs, nil
}

type HistoryEntry struct {
	EntryID      ids.ID    `json:"entry_id"`
	JournalID    ids.ID    `json:"journal_id"`
	OperationRef string    `json:"operation_ref"`
	Kind         string    `json:"kind"`
	Side         string    `json:"side"`
	AmountMinor  int64     `json:"amount_minor"`
	Currency     string    `json:"currency"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Service) History(ctx context.Context, wallet ids.ID, after ids.ID, limit int) ([]HistoryEntry, error) {
	if _, e := s.OwnWallet(ctx, wallet); e != nil {
		return nil, e
	}
	if limit < 1 || limit > 100 {
		return nil, Fail(422, "INVALID_PAGE_LIMIT")
	}
	rows, e := s.DB.Query(ctx, `SELECT e.entry_id,j.journal_id,j.operation_ref,a.kind,e.side,e.amount_minor,j.currency,j.created_at FROM journals j JOIN journal_entries e USING(journal_id) JOIN ledger_accounts a USING(account_id) WHERE a.wallet_id=$1 AND ($2::uuid IS NULL OR e.entry_id>$2) ORDER BY e.entry_id LIMIT $3`, wallet, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []HistoryEntry{}
	for rows.Next() {
		var x HistoryEntry
		if e = rows.Scan(&x.EntryID, &x.JournalID, &x.OperationRef, &x.Kind, &x.Side, &x.AmountMinor, &x.Currency, &x.CreatedAt); e != nil {
			return nil, e
		}
		result = append(result, x)
	}
	return result, rows.Err()
}
func (s *Service) Emit(ctx context.Context, tx pgx.Tx, typ, aggregate string, id ids.ID, payload any) error {
	e, err := events.New(ctx, platform.PaymentService, typ, aggregate, id, payload)
	if err != nil {
		return err
	}
	return s.Outbox.Add(ctx, tx, e)
}
func (s *Service) Audit(ctx context.Context, tx pgx.Tx, action, ref string, evidence any) error {
	p, e := Caller(ctx)
	if e != nil {
		return e
	}
	b, _ := json.Marshal(evidence)
	_, e = tx.Exec(ctx, `INSERT INTO financial_audit(audit_id,origin_service,action,aggregate_ref,evidence) VALUES($1,$2,$3,$4,$5)`, ids.New(), p.Service, action, ref, string(b))
	return e
}
func (s *Service) Verification(ctx context.Context, identity ids.ID, ref, state string, expiry time.Time) error {
	if e := Require(ctx, platform.VerificationService); e != nil {
		return e
	}
	if ref == "" || (state != "VERIFIED" && state != "REVOKED" && state != "EXPIRED") {
		return Fail(422, "INVALID_VERIFICATION_REFERENCE")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO verification_refs(identity_id,verification_ref,state,expires_at) VALUES($1,$2,$3,$4) ON CONFLICT(identity_id) DO UPDATE SET verification_ref=EXCLUDED.verification_ref,state=EXCLUDED.state,expires_at=EXCLUDED.expires_at,updated_at=now()`, identity, ref, state, expiry)
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "verification.recorded", identity.String(), map[string]string{"verification_ref": ref, "state": state}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
