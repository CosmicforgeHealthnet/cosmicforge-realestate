package finance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
)

type DVAInput struct {
	WalletID          ids.ID                    `json:"wallet_id"`
	ProviderAccountID ids.ID                    `json:"provider_account_id"`
	ConsentRef        string                    `json:"consent_ref"`
	ConsentVersion    string                    `json:"consent_version"`
	ConsentAt         time.Time                 `json:"consent_at"`
	ConsentExpiresAt  time.Time                 `json:"consent_expires_at"`
	VerificationRef   string                    `json:"verification_ref"`
	Customer          providers.CustomerRequest `json:"customer"`
	Validation        providers.Validation      `json:"validation"`
}
type DVA struct {
	ID          ids.ID  `json:"virtual_account_id"`
	WalletID    ids.ID  `json:"wallet_id"`
	State       string  `json:"state"`
	AccountMask *string `json:"account_mask,omitempty"`
	BankCode    *string `json:"bank_code,omitempty"`
}

func (s *Service) dvaConfig(ctx context.Context, id ids.ID) (ProviderConfig, error) {
	var raw []byte
	var c ProviderConfig
	e := s.DB.QueryRow(ctx, `SELECT config FROM provider_accounts WHERE provider_account_id=$1`, id).Scan(&raw)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(raw, &c)
	return c, e
}
func (s *Service) dvaEligible(ctx context.Context, w core.Wallet, in DVAInput) error {
	if w.State != "ACTIVE" || w.Currency != "NGN" || in.ConsentRef == "" || in.ConsentVersion == "" || in.ConsentAt.IsZero() || in.ConsentAt.After(time.Now()) || !in.ConsentExpiresAt.After(time.Now()) || in.ConsentExpiresAt.Before(in.ConsentAt) {
		return core.Fail(422, "DVA_CONSENT_REQUIRED")
	}
	c, e := s.dvaConfig(ctx, in.ProviderAccountID)
	if e != nil {
		return e
	}
	if c.LegalEntity != w.LegalEntity || !c.Approved || !c.Operational || !c.DVAApproved || !contains(c.Capabilities, "DVA") || !contains(c.Currencies, w.Currency) || !contains(c.Countries, "NG") || c.Mode != "test" || s.CheckEnvironmentCapability("DVA_ASSIGNMENT") != nil {
		return core.Fail(503, "DVA_NOT_ELIGIBLE")
	}
	var verified bool
	e = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verification_refs WHERE identity_id=$1 AND verification_ref=$2 AND state='VERIFIED' AND expires_at>now())`, w.IdentityID, in.VerificationRef).Scan(&verified)
	if e != nil {
		return e
	}
	if !verified {
		return core.Fail(403, "AUTHORITATIVE_VERIFICATION_REQUIRED")
	}
	return nil
}
func (s *Service) DVA(ctx context.Context, id ids.ID) (x DVA, e error) {
	e = s.DB.QueryRow(ctx, `SELECT virtual_account_id,wallet_id,state,account_mask,bank_code FROM virtual_accounts WHERE virtual_account_id=$1`, id).Scan(&x.ID, &x.WalletID, &x.State, &x.AccountMask, &x.BankCode)
	if e != nil {
		return x, core.Fail(404, "VIRTUAL_ACCOUNT_NOT_FOUND")
	}
	_, e = s.OwnWallet(ctx, x.WalletID)
	return
}
func (s *Service) RequestDVA(ctx context.Context, in DVAInput) (DVA, error) {
	w, e := s.OwnWallet(ctx, in.WalletID)
	if e != nil {
		return DVA{}, e
	}
	if e = core.DomainCaller(ctx, w.Domain); e != nil {
		return DVA{}, e
	}
	if e = s.dvaEligible(ctx, w, in); e != nil {
		return DVA{}, e
	}
	if in.Customer.Email == "" || in.Customer.FirstName == "" || in.Customer.LastName == "" || in.Validation.Country != "NG" || in.Validation.Type != "bank_account" || in.Validation.BVN == "" || in.Validation.BankCode == "" || in.Validation.AccountNumber == "" {
		return DVA{}, core.Fail(422, "DVA_VALIDATION_REQUIRED")
	}
	// No sensitive validation payload is persisted or logged. An interrupted step
	// requires caller resubmission/read-back, never a fabricated validation result.
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return DVA{}, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, in.WalletID.String()+":"+in.ProviderAccountID.String())
	if e != nil {
		return DVA{}, e
	}
	var id ids.ID
	var fp, state string
	var customer *string
	e = tx.QueryRow(ctx, `SELECT virtual_account_id,fingerprint,state,customer_ref FROM virtual_accounts WHERE wallet_id=$1 AND provider_account_id=$2 FOR UPDATE`, in.WalletID, in.ProviderAccountID).Scan(&id, &fp, &state, &customer)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return DVA{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		if state != "CUSTOMER_UNKNOWN" {
			return s.readDVA(ctx, tx, id)
		}
		if e = tx.Commit(ctx); e != nil {
			return DVA{}, e
		}
		a, err := s.Adapter(in.ProviderAccountID)
		if err != nil || a.DVA == nil {
			return s.DVA(ctx, id)
		}
		c, err := a.DVA.FetchCustomer(ctx, in.Customer.Email)
		if err != nil {
			return s.DVA(ctx, id)
		}
		if _, err = s.DB.Exec(ctx, `UPDATE virtual_accounts SET customer_ref=$2,state='VALIDATION_REQUIRED',updated_at=now() WHERE virtual_account_id=$1 AND state='CUSTOMER_UNKNOWN'`, id, c.Code); err != nil {
			return DVA{}, err
		}
		if !c.Identified {
			if err = a.DVA.ValidateCustomer(ctx, c.Code, in.Validation); err != nil {
				return s.DVA(ctx, id)
			}
		}
		return s.SyncDVA(ctx, id, false)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return DVA{}, e
	}
	id = ids.New()
	_, e = tx.Exec(ctx, `INSERT INTO virtual_accounts(virtual_account_id,wallet_id,provider_account_id,consent_ref,consent_version,consent_at,consent_expires_at,verification_ref,fingerprint,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'CUSTOMER_UNKNOWN')`, id, w.WalletID, in.ProviderAccountID, in.ConsentRef, in.ConsentVersion, in.ConsentAt, in.ConsentExpiresAt, in.VerificationRef, core.Fingerprint(in))
	if e != nil {
		return DVA{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return DVA{}, e
	}
	a, e := s.Adapter(in.ProviderAccountID)
	if e != nil || a.DVA == nil {
		return s.DVA(ctx, id)
	}
	c, e := a.DVA.CreateCustomer(ctx, in.Customer)
	if e != nil {
		return s.DVA(ctx, id)
	}
	_, e = s.DB.Exec(ctx, `UPDATE virtual_accounts SET customer_ref=$2,state='VALIDATION_REQUIRED',updated_at=now() WHERE virtual_account_id=$1 AND state='CUSTOMER_UNKNOWN'`, id, c.Code)
	if e != nil {
		return DVA{}, e
	}
	if e = a.DVA.ValidateCustomer(ctx, c.Code, in.Validation); e != nil {
		s.observe(ctx, in.ProviderAccountID, "VALIDATE_CUSTOMER", id.String(), "PENDING")
		return s.DVA(ctx, id)
	}
	return s.SyncDVA(ctx, id, false)
}
func (s *Service) readDVA(ctx context.Context, q querier, id ids.ID) (x DVA, e error) {
	e = q.QueryRow(ctx, `SELECT virtual_account_id,wallet_id,state,account_mask,bank_code FROM virtual_accounts WHERE virtual_account_id=$1`, id).Scan(&x.ID, &x.WalletID, &x.State, &x.AccountMask, &x.BankCode)
	return
}

// SyncDVA reads provider identification before assignment, and fetches the
// original DVA for status synchronization. Unknown assignment is never retried.
func (s *Service) SyncDVA(ctx context.Context, id ids.ID, deactivate bool) (DVA, error) {
	x, e := s.DVA(ctx, id)
	if e != nil {
		return x, e
	}
	w, e := s.OwnWallet(ctx, x.WalletID)
	if e != nil {
		return x, e
	}
	p, _ := core.Caller(ctx)
	if p.Service != platform.PaymentService {
		if e = core.DomainCaller(ctx, w.Domain); e != nil {
			return x, e
		}
	}
	var provider ids.ID
	var consent, version, verification string
	var at, expiry time.Time
	var customer, external *string
	e = s.DB.QueryRow(ctx, `SELECT provider_account_id,consent_ref,consent_version,consent_at,consent_expires_at,verification_ref,customer_ref,external_ref FROM virtual_accounts WHERE virtual_account_id=$1`, id).Scan(&provider, &consent, &version, &at, &expiry, &verification, &customer, &external)
	if e != nil {
		return x, e
	}
	if deactivate && external == nil {
		_, e = s.DB.Exec(ctx, `UPDATE virtual_accounts SET state='INACTIVE',updated_at=now() WHERE virtual_account_id=$1 AND external_ref IS NULL`, id)
		if e != nil {
			return x, e
		}
		return s.DVA(ctx, id)
	}
	if deactivate && x.State == "INACTIVE" {
		return x, nil
	}
	if !deactivate {
		if e = s.dvaEligible(ctx, w, DVAInput{ProviderAccountID: provider, ConsentRef: consent, ConsentVersion: version, ConsentAt: at, ConsentExpiresAt: expiry, VerificationRef: verification}); e != nil {
			_, _ = s.DB.Exec(ctx, `UPDATE virtual_accounts SET state='RESTRICTED' WHERE virtual_account_id=$1`, id)
			return x, e
		}
	}
	config, e := s.dvaConfig(ctx, provider)
	if e != nil {
		return x, e
	}
	a, e := s.Adapter(provider)
	if e != nil {
		return x, e
	}
	if a.DVA == nil {
		return x, core.Fail(503, "PROVIDER_UNAVAILABLE")
	}
	if customer == nil {
		return x, core.Fail(503, "DVA_CUSTOMER_RECONCILIATION_REQUIRED")
	}
	c, e := a.DVA.FetchCustomer(ctx, *customer)
	if e != nil {
		return x, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	if c.Code != *customer || formatMerchant(c.MerchantID) != config.MerchantRef || c.Mode != config.Mode {
		return x, core.Fail(409, "DVA_OWNER_MISMATCH")
	}
	if !deactivate && !c.Identified {
		return x, core.Fail(403, "DVA_PROVIDER_IDENTITY_PENDING")
	}
	var result providers.VirtualAccount
	if external != nil {
		if deactivate {
			if e = a.DVA.DeactivateDVA(ctx, *external); e != nil {
				return x, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
			}
		}
		result, e = a.DVA.FetchDVA(ctx, *external)
		if e != nil {
			return x, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
		}
		if !deactivate {
			if e = a.DVA.RequeryDVA(ctx, result.AccountNumber, result.BankCode); e != nil {
				return x, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
			}
		}
	} else {
		if x.State == "ASSIGNING" || x.State == "ASSIGNMENT_UNKNOWN" {
			result, e = a.DVA.FindDVA(ctx, *customer)
			if e != nil {
				return x, core.Fail(503, "DVA_ASSIGNMENT_RECONCILIATION_REQUIRED")
			}
		} else {
			tag, e := s.DB.Exec(ctx, `UPDATE virtual_accounts SET state='ASSIGNING',updated_at=now() WHERE virtual_account_id=$1 AND state='VALIDATION_REQUIRED'`, id)
			if e != nil {
				return x, e
			}
			if tag.RowsAffected() != 1 {
				return x, core.Fail(503, "DVA_ASSIGNMENT_RECONCILIATION_REQUIRED")
			}
			result, e = a.DVA.AssignDVA(ctx, *customer)
			if e != nil {
				_, _ = s.DB.Exec(ctx, `UPDATE virtual_accounts SET state='ASSIGNMENT_UNKNOWN' WHERE virtual_account_id=$1`, id)
				return x, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
			}
		}
	}
	if result.CustomerRef != *customer || formatMerchant(result.MerchantID) != config.MerchantRef || result.Mode != config.Mode || result.ID == "" || result.BankCode == "" {
		return x, core.Fail(409, "DVA_OWNER_MISMATCH")
	}
	if deactivate && result.Active {
		return x, core.Fail(503, "DVA_DEACTIVATION_PENDING")
	}
	if x.State == "INACTIVE" && result.Active {
		return x, core.Fail(409, "DVA_REASSIGNMENT_REQUIRES_APPROVAL")
	}
	if len(result.AccountNumber) < 4 {
		return x, core.Fail(409, "DVA_INVALID_ACCOUNT")
	}
	state := "ACTIVE"
	if !result.Active {
		state = "INACTIVE"
	}
	mask := strings.Repeat("*", len(result.AccountNumber)-4) + result.AccountNumber[len(result.AccountNumber)-4:]
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return x, e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE virtual_accounts SET state=$2,external_ref=$3,bank_code=$4,account_mask=$5,account_digest=$6,updated_at=now() WHERE virtual_account_id=$1 AND (external_ref IS NULL OR (external_ref=$3 AND bank_code=$4 AND account_digest=$6))`, id, state, result.ID, result.BankCode, mask, core.Fingerprint(result.AccountNumber))
	if e != nil {
		return x, core.Fail(409, "DVA_MAPPING_CONFLICT")
	}
	if tag.RowsAffected() != 1 {
		return x, core.Fail(409, "DVA_REASSIGNMENT_REQUIRES_APPROVAL")
	}
	if external == nil && state == "ACTIVE" {
		if e = s.Emit(ctx, tx, "finance.virtual_account.assigned.v1", "virtual_account", id, map[string]any{"virtual_account_id": id, "wallet_id": w.WalletID, "bank_code": result.BankCode, "account_mask": mask}); e != nil {
			return x, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return x, e
	}
	s.observe(ctx, provider, "SYNC_DVA", id.String(), state)
	return s.DVA(ctx, id)
}
