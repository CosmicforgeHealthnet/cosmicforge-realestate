package lifecycle

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"time"
)

type WalletTransferPolicy struct {
	ID            ids.ID    `json:"policy_id"`
	PoolID        ids.ID    `json:"pool_id"`
	LegalEntity   string    `json:"legal_entity"`
	Currency      string    `json:"currency"`
	Jurisdiction  string    `json:"jurisdiction"`
	Version       string    `json:"version"`
	SourceDomains []string  `json:"source_domains"`
	MaxMinor      int64     `json:"max_minor"`
	DailyMinor    int64     `json:"daily_minor"`
	LocalTestOnly bool      `json:"local_test_only"`
	EvidenceRef   string    `json:"evidence_ref"`
	Approvers     []string  `json:"approvers"`
	EffectiveAt   time.Time `json:"effective_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (s *Service) PutWalletTransferPolicy(ctx context.Context, in WalletTransferPolicy) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if e := s.CheckEnvironmentCapability("INTERNAL_TRANSFER"); e != nil {
		return e
	}
	if in.ID.ValidateV7() != nil || in.PoolID.ValidateV7() != nil || in.LegalEntity == "" || !core.ValidCurrency(in.Currency) || in.Jurisdiction == "" || in.Version == "" || in.MaxMinor <= 0 || in.DailyMinor < in.MaxMinor || !in.LocalTestOnly || in.EvidenceRef == "" || !dual(in.Approvers) || len(in.SourceDomains) == 0 || !in.ExpiresAt.After(in.EffectiveAt) {
		return fail("LOCAL_TRANSFER_POLICY_REQUIRED")
	}
	for _, d := range in.SourceDomains {
		if _, ok := platform.ServiceForDomain(platform.Domain(d)); !ok || d == "admin" {
			return fail("INVALID_TRANSFER_SOURCE")
		}
	}
	var matches bool
	if e := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM custody_pools p JOIN provider_accounts a USING(provider_account_id) WHERE p.pool_id=$1 AND p.legal_entity=$2 AND p.currency=$3 AND a.mode='test')`, in.PoolID, in.LegalEntity, in.Currency).Scan(&matches); e != nil {
		return e
	}
	if !matches {
		return core.Fail(403, "TEST_TRANSFER_POOL_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "wallet-transfer-policy:"+in.ID.String()); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM wallet_transfer_policies WHERE policy_id=$1`, in.ID).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO wallet_transfer_policies(policy_id,pool_id,version,snapshot,fingerprint) VALUES($1,$2,$3,$4,$5)`, in.ID, in.PoolID, in.Version, raw(in), core.Fingerprint(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "wallet.transfer_policy_recorded", in.ID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type WalletTransferInput struct {
	ID                  ids.ID `json:"transfer_id"`
	SourceWalletID      ids.ID `json:"source_wallet_id"`
	DestinationWalletID ids.ID `json:"destination_wallet_id"`
	PoolID              ids.ID `json:"pool_id"`
	PolicyID            ids.ID `json:"policy_id"`
	Currency            string `json:"currency"`
	AmountMinor         int64  `json:"amount_minor"`
	Purpose             string `json:"purpose"`
	ConsentRef          string `json:"consent_ref"`
}
type WalletTransfer struct {
	WalletTransferInput
	ActorID               ids.ID     `json:"actor_id"`
	ActorDomain           string     `json:"actor_domain"`
	SourceService         string     `json:"source_service"`
	SourceIdentityID      ids.ID     `json:"source_identity_id"`
	DestinationIdentityID ids.ID     `json:"destination_identity_id"`
	LegalEntity           string     `json:"legal_entity"`
	PolicyVersion         string     `json:"policy_version"`
	AuthorizationDecision string     `json:"authorization_decision"`
	RiskDecision          string     `json:"risk_decision"`
	State                 string     `json:"state"`
	JournalID             *ids.ID    `json:"journal_id,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	PostedAt              *time.Time `json:"posted_at,omitempty"`
}

func (s *Service) transferPolicy(ctx context.Context, db query, id ids.ID) (WalletTransferPolicy, error) {
	var p WalletTransferPolicy
	e := load(ctx, db, `SELECT snapshot FROM wallet_transfer_policies WHERE policy_id=$1`, id, &p)
	return p, e
}
func (s *Service) transferContext(ctx context.Context, db query, in WalletTransferInput) (core.Wallet, ids.ID, WalletTransferPolicy, error) {
	ctx = beneficiaryContext(ctx)
	principal, e := core.Caller(ctx)
	if e != nil {
		return core.Wallet{}, ids.Nil, WalletTransferPolicy{}, e
	}
	if !principal.Delegation.HasActor() {
		return core.Wallet{}, ids.Nil, WalletTransferPolicy{}, core.Fail(403, "ACTOR_REQUIRED")
	}
	if e = s.CheckEnvironmentCapability("INTERNAL_TRANSFER"); e != nil {
		return core.Wallet{}, ids.Nil, WalletTransferPolicy{}, e
	}
	w, e := s.OwnWalletWith(ctx, db, in.SourceWalletID)
	if e != nil {
		return w, ids.Nil, WalletTransferPolicy{}, e
	}
	if e = core.DomainCaller(ctx, w.Domain); e != nil {
		return w, ids.Nil, WalletTransferPolicy{}, e
	}
	p, e := s.transferPolicy(ctx, db, in.PolicyID)
	if e != nil {
		return w, ids.Nil, p, e
	}
	if !p.LocalTestOnly || p.EffectiveAt.After(time.Now()) || !p.ExpiresAt.After(time.Now()) || p.PoolID != in.PoolID || p.Currency != in.Currency || p.Currency != w.Currency || p.LegalEntity != w.LegalEntity || !has(p.SourceDomains, w.Domain) || in.AmountMinor > p.MaxMinor || in.Purpose != "CUSTOMER_REMITTANCE" {
		return w, ids.Nil, p, core.Fail(403, "TRANSFER_POLICY_NOT_PERMITTED")
	}
	var providerAllowed bool
	if e = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM custody_pools p JOIN provider_accounts a USING(provider_account_id) WHERE p.pool_id=$1 AND a.mode='test' AND a.config->>'approved'='true' AND a.config->>'operational'='true' AND (a.config->'countries') ? $2 AND (a.config->'currencies') ? $3)`, in.PoolID, p.Jurisdiction, in.Currency).Scan(&providerAllowed); e != nil {
		return w, ids.Nil, p, e
	}
	if !providerAllowed {
		return w, ids.Nil, p, core.Fail(403, "APPROVED_TEST_PROVIDER_REQUIRED")
	}
	var destIdentity ids.ID
	var allowed bool
	e = db.QueryRow(ctx, `SELECT d.identity_id,(d.legal_entity=$2 AND d.currency=$3 AND i.state='ACTIVE' AND COALESCE(c.state,'ACTIVE')='ACTIVE') FROM wallets d JOIN financial_identities i USING(identity_id) LEFT JOIN wallet_controls c USING(wallet_id) WHERE d.wallet_id=$1`, in.DestinationWalletID, w.LegalEntity, w.Currency).Scan(&destIdentity, &allowed)
	if e != nil || !allowed || w.State != "ACTIVE" || destIdentity == w.IdentityID {
		return w, destIdentity, p, core.Fail(403, "TRANSFER_DESTINATION_NOT_ELIGIBLE")
	}
	var verified int
	e = db.QueryRow(ctx, `SELECT count(*) FROM verification_refs v WHERE identity_id=ANY($1::uuid[]) AND state='VERIFIED' AND expires_at>now()`, []ids.ID{w.IdentityID, destIdentity}).Scan(&verified)
	if e != nil {
		return w, destIdentity, p, e
	}
	if verified != 2 {
		return w, destIdentity, p, core.Fail(403, "VERIFIED_TRANSFER_PARTIES_REQUIRED")
	}
	return w, destIdentity, p, nil
}
func (s *Service) RequestWalletTransfer(ctx context.Context, in WalletTransferInput, key string) (WalletTransfer, error) {
	ctx = beneficiaryContext(ctx)
	if key == "" || len(key) > 128 || in.ID.ValidateV7() != nil || in.SourceWalletID.ValidateV7() != nil || in.DestinationWalletID.ValidateV7() != nil || in.SourceWalletID == in.DestinationWalletID || in.PoolID.ValidateV7() != nil || in.PolicyID.ValidateV7() != nil || in.AmountMinor <= 0 || in.ConsentRef == "" {
		return WalletTransfer{}, fail("INVALID_WALLET_TRANSFER")
	}
	w, dest, p, e := s.transferContext(ctx, s.DB, in)
	if e != nil {
		return WalletTransfer{}, e
	}
	principal, _ := core.Caller(ctx)
	op := "wallet-transfer:" + string(principal.Delegation.ActorDomain) + ":" + principal.Delegation.ActorID.String() + ":" + key
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return WalletTransfer{}, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, op); e != nil {
		return WalletTransfer{}, e
	}
	var id ids.ID
	var fp string
	e = tx.QueryRow(ctx, `SELECT transfer_id,fingerprint FROM wallet_transfers WHERE operation_key=$1`, op).Scan(&id, &fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return WalletTransfer{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return s.walletTransfer(ctx, tx, id)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return WalletTransfer{}, e
	}
	out := WalletTransfer{WalletTransferInput: in, ActorID: principal.Delegation.ActorID, ActorDomain: string(principal.Delegation.ActorDomain), SourceService: string(principal.Service), SourceIdentityID: w.IdentityID, DestinationIdentityID: dest, LegalEntity: w.LegalEntity, PolicyVersion: p.Version, AuthorizationDecision: "AUTHENTICATED_SOURCE_OWNER", RiskDecision: "LOCAL_VERIFIED_PARTIES_WITHIN_POLICY", State: "REQUESTED", CreatedAt: time.Now().UTC()}
	_, e = tx.Exec(ctx, `INSERT INTO wallet_transfers(transfer_id,source_wallet_id,destination_wallet_id,pool_id,policy_id,actor_id,actor_domain,source_service,legal_entity,currency,amount_minor,purpose,operation_key,fingerprint,snapshot,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, in.ID, in.SourceWalletID, in.DestinationWalletID, in.PoolID, in.PolicyID, out.ActorID, out.ActorDomain, out.SourceService, out.LegalEntity, in.Currency, in.AmountMinor, in.Purpose, op, core.Fingerprint(in), raw(out), out.CreatedAt)
	if e != nil {
		return out, e
	}
	if e = s.transferFact(ctx, tx, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) walletTransfer(ctx context.Context, db query, id ids.ID) (WalletTransfer, error) {
	var out WalletTransfer
	e := load(ctx, db, `SELECT snapshot||jsonb_build_object('state',state)||CASE WHEN journal_id IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('journal_id',journal_id,'posted_at',posted_at) END FROM wallet_transfers WHERE transfer_id=$1`, id, &out)
	return out, e
}
func (s *Service) WalletTransfer(ctx context.Context, id ids.ID) (WalletTransfer, error) {
	out, e := s.walletTransfer(ctx, s.DB, id)
	if e != nil {
		return out, e
	}
	if _, e = s.OwnWallet(ctx, out.SourceWalletID); e != nil {
		if _, e = s.OwnWallet(ctx, out.DestinationWalletID); e != nil {
			return WalletTransfer{}, core.Fail(404, "WALLET_TRANSFER_NOT_FOUND")
		}
	}
	return out, nil
}
func (s *Service) WalletTransfers(ctx context.Context, wallet, after ids.ID, limit int) ([]WalletTransfer, error) {
	if _, e := s.OwnWallet(ctx, wallet); e != nil {
		return nil, e
	}
	if limit < 1 || limit > 100 {
		return nil, fail("INVALID_PAGE_LIMIT")
	}
	rows, e := s.DB.Query(ctx, `SELECT transfer_id FROM wallet_transfers WHERE (source_wallet_id=$1 OR destination_wallet_id=$1) AND ($2::uuid IS NULL OR transfer_id>$2) ORDER BY transfer_id LIMIT $3`, wallet, after, limit)
	if e != nil {
		return nil, e
	}
	idsToRead := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		idsToRead = append(idsToRead, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []WalletTransfer{}
	for _, id := range idsToRead {
		v, e := s.walletTransfer(ctx, s.DB, id)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) transferFact(ctx context.Context, tx pgx.Tx, out WalletTransfer) error {
	if _, e := tx.Exec(ctx, `INSERT INTO wallet_transfer_history(history_id,transfer_id,state,evidence) VALUES($1,$2,$3,$4)`, ids.New(), out.ID, out.State, raw(out)); e != nil {
		return e
	}
	if e := s.Audit(ctx, tx, "wallet.transfer_"+out.State, out.ID.String(), out); e != nil {
		return e
	}
	event := "finance.wallet.transfer_requested.v1"
	if out.State == "POSTED" {
		event = "finance.wallet.transfer_posted.v1"
	}
	return s.Emit(ctx, tx, event, "wallet_transfer", out.ID, out)
}
func (s *Service) ConfirmWalletTransfer(ctx context.Context, id ids.ID) (WalletTransfer, error) {
	ctx = beneficiaryContext(ctx)
	if e := s.CheckEnvironmentCapability("INTERNAL_TRANSFER"); e != nil {
		return WalletTransfer{}, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return WalletTransfer{}, e
	}
	defer tx.Rollback(ctx)
	var op string
	if e = tx.QueryRow(ctx, `SELECT operation_key FROM wallet_transfers WHERE transfer_id=$1 FOR UPDATE`, id).Scan(&op); e != nil {
		return WalletTransfer{}, e
	}
	out, e := s.walletTransfer(ctx, tx, id)
	if e != nil {
		return out, e
	}
	principal, e := core.Caller(ctx)
	if e != nil {
		return out, e
	}
	if principal.Delegation.ActorID != out.ActorID || string(principal.Delegation.ActorDomain) != out.ActorDomain || string(principal.Service) != out.SourceService {
		return WalletTransfer{}, core.Fail(403, "TRANSFER_SOURCE_ACTOR_REQUIRED")
	}
	if _, e = s.OwnWalletWith(ctx, tx, out.SourceWalletID); e != nil {
		return WalletTransfer{}, e
	}
	if out.State == "POSTED" {
		return out, nil
	}
	// Pool lock serializes coverage, concurrent transfers and daily risk limits.
	var valid bool
	if e = tx.QueryRow(ctx, `SELECT NOT p.restricted AND a.mode='test' FROM custody_pools p JOIN provider_accounts a USING(provider_account_id) WHERE pool_id=$1 FOR UPDATE OF p`, out.PoolID).Scan(&valid); e != nil {
		return out, e
	}
	if !valid {
		return out, core.Fail(409, "FUNDED_TEST_TRANSFER_POOL_REQUIRED")
	}
	// Prevent verification revocation from committing between validation and posting.
	rows, e := tx.Query(ctx, `SELECT identity_id FROM verification_refs WHERE identity_id=ANY($1::uuid[]) ORDER BY identity_id FOR SHARE`, []ids.ID{out.SourceIdentityID, out.DestinationIdentityID})
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var locked ids.ID
		if e = rows.Scan(&locked); e != nil {
			rows.Close()
			return out, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	w, dest, policy, e := s.transferContext(ctx, tx, out.WalletTransferInput)
	if e != nil {
		return out, e
	}
	if w.IdentityID != out.SourceIdentityID || dest != out.DestinationIdentityID || policy.Version != out.PolicyVersion {
		return out, core.Fail(409, "TRANSFER_TERMS_CHANGED")
	}
	var withinLimit bool
	e = tx.QueryRow(ctx, `SELECT COALESCE(sum(t.amount_minor::numeric),0)+$3::numeric <= $4::numeric FROM wallet_transfers t JOIN wallets w ON w.wallet_id=t.source_wallet_id WHERE w.identity_id=$1 AND t.pool_id=$2 AND t.state='POSTED' AND t.posted_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, out.SourceIdentityID, out.PoolID, out.AmountMinor, policy.DailyMinor).Scan(&withinLimit)
	// Actual source-wallet daily totals are serialized by the pool lock.
	if e != nil {
		return out, e
	}
	if !withinLimit {
		return out, core.Fail(403, "TRANSFER_DAILY_LIMIT")
	}
	from, e := s.Account(trusted(ctx), tx, &out.SourceWalletID, out.PoolID, "AVAILABLE", out.LegalEntity, out.Currency, "")
	if e != nil {
		return out, e
	}
	to, e := s.Account(trusted(ctx), tx, &out.DestinationWalletID, out.PoolID, "AVAILABLE", out.LegalEntity, out.Currency, "")
	if e != nil {
		return out, e
	}
	journal, e := s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: op, LegalEntity: out.LegalEntity, Currency: out.Currency, PoolID: out.PoolID, PolicyVersion: out.PolicyVersion, Authorization: map[string]string{"evidence_ref": out.ConsentRef, "approved_by": out.ActorID.String(), "risk_policy_ref": policy.EvidenceRef}, Entries: []core.Entry{{AccountID: from, Side: "DEBIT", AmountMinor: out.AmountMinor}, {AccountID: to, Side: "CREDIT", AmountMinor: out.AmountMinor}}})
	if e != nil {
		return out, e
	}
	now := time.Now().UTC()
	out.State = "POSTED"
	out.JournalID = &journal
	out.PostedAt = &now
	_, e = tx.Exec(ctx, `UPDATE wallet_transfers SET state='POSTED',journal_id=$2,posted_at=$3 WHERE transfer_id=$1`, id, journal, now)
	if e != nil {
		return out, e
	}
	if e = s.transferFact(ctx, tx, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
