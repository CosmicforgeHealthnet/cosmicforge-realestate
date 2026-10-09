package usecases

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Banking, payout and tax (§13.3). Beneficiary truth comes only from
// payment-service: the owner domain stores masked metadata (bank, name,
// last 4 digits, keyed fingerprint) and the payment-service reference. New or
// re-verified accounts pass a cooling-off period before they can receive
// payouts; every change needs step-up and alerts the owner.

// PayoutAccountView is a masked payout account.
type PayoutAccountView struct {
	ID              ids.ID     `json:"id"`
	Status          string     `json:"status" doc:"PENDING_VERIFICATION, COOLING_OFF, ACTIVE, REJECTED"`
	BankCode        string     `json:"bank_code"`
	BankName        string     `json:"bank_name"`
	AccountName     string     `json:"account_name"`
	AccountLast4    string     `json:"account_number_last4"`
	Currency        string     `json:"currency"`
	NameMatch       *string    `json:"name_match" doc:"payment-service account-name match result"`
	IsDefault       bool       `json:"is_default"`
	VerifiedAt      *time.Time `json:"verified_at"`
	CoolingOffUntil *time.Time `json:"cooling_off_until"`
	ActivatedAt     *time.Time `json:"activated_at"`
	Version         int64      `json:"version"`
	CreatedAt       time.Time  `json:"created_at"`
}

func payoutView(a db.PayoutAccount, def *ids.ID) PayoutAccountView {
	return PayoutAccountView{ID: a.ID, Status: a.Status, BankCode: a.BankCode, BankName: a.BankName, AccountName: a.AccountName,
		AccountLast4: a.AccountNumberLast4, Currency: a.Currency, NameMatch: a.NameMatch, IsDefault: def != nil && *def == a.ID,
		VerifiedAt: a.VerifiedAt, CoolingOffUntil: a.CoolingOffUntil, ActivatedAt: a.ActivatedAt, Version: a.Version, CreatedAt: a.CreatedAt}
}

func (s *Service) defaultPayout(ctx context.Context, accID ids.ID) (*ids.ID, error) {
	p, err := s.q.GetPayoutProfile(ctx, s.dbq(ctx), accID)
	if isNoRows(err) {
		return nil, nil
	}
	return p.DefaultPayoutAccountID, err
}

// ListPayoutAccounts lists the caller's payout accounts.
func (s *Service) ListPayoutAccounts(ctx context.Context, subj authz.Subject) ([]PayoutAccountView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPayoutRead, ids.Nil); err != nil {
		return nil, err
	}
	def, err := s.defaultPayout(ctx, accountOf(subj))
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListPayoutAccounts(ctx, s.dbq(ctx), accountOf(subj))
	out := make([]PayoutAccountView, 0, len(rows))
	for _, r := range rows {
		out = append(out, payoutView(r, def))
	}
	return out, err
}

// AddPayoutAccountInput registers a payout destination. account_number is
// forwarded to payment-service and never stored or echoed.
type AddPayoutAccountInput struct {
	BankCode      string `json:"bank_code" v:"required,pattern=[A-Z0-9]{2,20}" ex:"058"`
	BankName      string `json:"bank_name" v:"required,max=120"`
	AccountName   string `json:"account_name" v:"required,max=160"`
	AccountNumber string `json:"account_number" v:"required,pattern=[0-9]{6,20}" doc:"Forwarded to payment-service; only the last 4 digits are kept"`
	Currency      string `json:"currency" v:"required,format=currency" ex:"NGN"`
}

// RegisterPayoutBeneficiary is the downstream phase of adding a payout account.
func (s *Service) RegisterPayoutBeneficiary(ctx context.Context, subj authz.Subject, in AddPayoutAccountInput, key string) (payment.Beneficiary, error) {
	if err := schema.Validate(in); err != nil {
		return payment.Beneficiary{}, err
	}
	if _, err := s.authorize(ctx, subj, authz.ActPayoutWrite, ids.Nil); err != nil {
		return payment.Beneficiary{}, err
	}
	if err := s.requireIdentity(ctx, accountOf(subj), GatePayout); err != nil {
		return payment.Beneficiary{}, err
	}
	if s.Payment == nil {
		return payment.Beneficiary{}, unavailable("Payout account verification", "payment-service")
	}
	b, err := s.Payment.RegisterBeneficiary(ctx, payment.BeneficiaryRequest{OwnerAccountID: accountOf(subj), BankCode: in.BankCode,
		AccountNumber: in.AccountNumber, AccountName: in.AccountName, Currency: in.Currency}, key)
	if err != nil {
		return payment.Beneficiary{}, downstream("payment-service", err)
	}
	return b, nil
}

// AddPayoutAccount records the masked account with payment-service's verdict.
func (s *Service) AddPayoutAccount(ctx context.Context, subj authz.Subject, in AddPayoutAccountInput, b payment.Beneficiary) (PayoutAccountView, error) {
	var out PayoutAccountView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActPayoutWrite, ids.Nil); err != nil {
			return err
		}
		tx, now, acc := mustTx(ctx), s.now(), accountOf(subj)
		fp := s.Hasher.Hash("payout-account", acc.String(), in.BankCode, in.AccountNumber)
		a, err := s.q.InsertPayoutAccount(ctx, tx, db.InsertPayoutAccountParams{ID: ids.New(), AccountID: acc, BankCode: in.BankCode, BankName: in.BankName,
			AccountName: strings.TrimSpace(in.AccountName), AccountNumberLast4: in.AccountNumber[len(in.AccountNumber)-4:], AccountFingerprint: fp,
			Currency: in.Currency, PaymentBeneficiaryRef: strp(b.Ref), NameMatch: strp(b.NameMatch), CreatedAt: now})
		if isUnique(err, "payout_accounts_fingerprint_uniq") {
			return conflict("PAYOUT_ACCOUNT_EXISTS", "This payout account is already registered.")
		}
		if err != nil {
			return err
		}
		if a, err = s.applyBeneficiary(ctx, a, b); err != nil {
			return err
		}
		if err := s.payoutChanged(ctx, subj, a, "ADD"); err != nil {
			return err
		}
		def, err := s.defaultPayout(ctx, acc)
		out = payoutView(a, def)
		return err
	})
	return out, err
}

// applyBeneficiary maps payment-service's verdict onto the account.
func (s *Service) applyBeneficiary(ctx context.Context, a db.PayoutAccount, b payment.Beneficiary) (db.PayoutAccount, error) {
	now := s.now()
	p := db.SetPayoutAccountVerificationParams{ID: a.ID, Version: a.Version, Status: a.Status, NameMatch: strp(b.NameMatch), BeneficiaryRef: strp(b.Ref), Now: now,
		VerifiedAt: a.VerifiedAt, CoolingOffUntil: a.CoolingOffUntil}
	switch b.Status {
	case "VERIFIED":
		if b.NameMatch == "MISMATCH" {
			p.Status = "REJECTED"
		} else if a.Status == "PENDING_VERIFICATION" {
			until := now.Add(s.Policy.Domain.PayoutCoolingOff)
			p.Status, p.VerifiedAt, p.CoolingOffUntil = "COOLING_OFF", b.VerifiedAt, &until
			if p.VerifiedAt == nil {
				p.VerifiedAt = &now
			}
		}
	case "REJECTED":
		p.Status = "REJECTED"
	}
	r, err := s.q.SetPayoutAccountVerification(ctx, mustTx(ctx), p)
	if isNoRows(err) {
		return a, lifecycle.ErrVersionConflict()
	}
	if err == nil && p.Status == "COOLING_OFF" {
		_, err = s.q.InsertPayoutChangeRequest(ctx, mustTx(ctx), db.InsertPayoutChangeRequestParams{ID: ids.New(), AccountID: a.AccountID, PayoutAccountID: a.ID,
			ChangeType: "ADD", Status: "PENDING_COOLING_OFF", StepUpMethod: "STEP_UP", CoolingOffUntil: *p.CoolingOffUntil, CreatedAt: now})
	}
	return r, err
}

func (s *Service) payoutChanged(ctx context.Context, subj authz.Subject, a db.PayoutAccount, change string) error {
	if err := s.record(ctx, "owner.payout_account."+lower(change), "payout_account", a.ID, map[string]any{"status": a.Status, "last4": a.AccountNumberLast4}, nil); err != nil {
		return err
	}
	acc, err := s.q.GetAccount(ctx, mustTx(ctx), a.AccountID)
	if err != nil {
		return err
	}
	return s.enqueueAlert(ctx, mustTx(ctx), acc, "PAYOUT_CHANGED", "")
}

// RefreshPayoutBeneficiary reads payment-service's current verdict (downstream phase).
func (s *Service) RefreshPayoutBeneficiary(ctx context.Context, subj authz.Subject, id ids.ID) (payment.Beneficiary, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPayoutWrite, ids.Nil); err != nil {
		return payment.Beneficiary{}, err
	}
	a, err := s.q.GetPayoutAccount(ctx, s.Pool, db.GetPayoutAccountParams{ID: id, AccountID: accountOf(subj)})
	if isNoRows(err) || (err == nil && a.Status == "REMOVED") {
		return payment.Beneficiary{}, notFound("payout_account")
	}
	if err != nil {
		return payment.Beneficiary{}, err
	}
	if s.Payment == nil {
		return payment.Beneficiary{}, unavailable("Payout account verification", "payment-service")
	}
	if a.PaymentBeneficiaryRef == nil {
		return payment.Beneficiary{}, conflict("PAYOUT_ACCOUNT_NOT_REGISTERED", "The account has no payment-service reference.")
	}
	b, err := s.Payment.GetBeneficiary(ctx, *a.PaymentBeneficiaryRef)
	if err != nil {
		return payment.Beneficiary{}, downstream("payment-service", err)
	}
	return b, nil
}

// VerifyPayoutAccount applies the refreshed verdict.
func (s *Service) VerifyPayoutAccount(ctx context.Context, subj authz.Subject, id ids.ID, b payment.Beneficiary) (PayoutAccountView, error) {
	var out PayoutAccountView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, err := s.q.GetPayoutAccountForUpdate(ctx, mustTx(ctx), db.GetPayoutAccountForUpdateParams{ID: id, AccountID: accountOf(subj)})
		if err != nil {
			return err
		}
		if a, err = s.applyBeneficiary(ctx, a, b); err != nil {
			return err
		}
		if err := s.record(ctx, "owner.payout_account.verify", "payout_account", a.ID, map[string]any{"status": a.Status}, nil); err != nil {
			return err
		}
		def, err := s.defaultPayout(ctx, a.AccountID)
		out = payoutView(a, def)
		return err
	})
	return out, err
}

// MakeDefaultPayout sets an ACTIVE account as the default payout destination.
func (s *Service) MakeDefaultPayout(ctx context.Context, subj authz.Subject, id ids.ID) (PayoutAccountView, error) {
	var out PayoutAccountView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActPayoutWrite, ids.Nil); err != nil {
			return err
		}
		tx, acc := mustTx(ctx), accountOf(subj)
		a, err := s.q.GetPayoutAccountForUpdate(ctx, tx, db.GetPayoutAccountForUpdateParams{ID: id, AccountID: acc})
		if isNoRows(err) || (err == nil && a.Status == "REMOVED") {
			return notFound("payout_account")
		}
		if err != nil {
			return err
		}
		if a.Status != "ACTIVE" {
			return conflict("PAYOUT_ACCOUNT_NOT_ACTIVE", "Only verified accounts past their cooling-off period can be the default.")
		}
		prof, err := s.lockPayoutProfile(ctx, acc)
		if err != nil {
			return err
		}
		prof.DefaultPayoutAccountID = &a.ID
		if _, err := s.savePayoutProfile(ctx, prof); err != nil {
			return err
		}
		if _, err := s.q.InsertPayoutChangeRequest(ctx, tx, db.InsertPayoutChangeRequestParams{ID: ids.New(), AccountID: acc, PayoutAccountID: a.ID,
			ChangeType: "MAKE_DEFAULT", Status: "APPLIED", StepUpMethod: "STEP_UP", CoolingOffUntil: s.now(), AppliedAt: ptr(s.now()), CreatedAt: s.now()}); err != nil {
			return err
		}
		if err := s.payoutChanged(ctx, subj, a, "MAKE_DEFAULT"); err != nil {
			return err
		}
		out = payoutView(a, &a.ID)
		return nil
	})
	return out, err
}

func (s *Service) lockPayoutProfile(ctx context.Context, acc ids.ID) (db.PayoutProfile, error) {
	p, err := s.q.GetPayoutProfileForUpdate(ctx, mustTx(ctx), acc)
	if isNoRows(err) {
		return db.PayoutProfile{AccountID: acc}, nil
	}
	return p, err
}

func (s *Service) savePayoutProfile(ctx context.Context, p db.PayoutProfile) (db.PayoutProfile, error) {
	tx, now := mustTx(ctx), s.now()
	if p.Version == 0 {
		r, err := s.q.InsertPayoutProfile(ctx, tx, db.InsertPayoutProfileParams{AccountID: p.AccountID, DefaultPayoutAccountID: p.DefaultPayoutAccountID,
			RentPayoutAccountID: p.RentPayoutAccountID, SalePayoutAccountID: p.SalePayoutAccountID, ShortletPayoutAccountID: p.ShortletPayoutAccountID, UpdatedAt: now})
		if isUnique(err, "") {
			return r, lifecycle.ErrVersionConflict()
		}
		return r, err
	}
	r, err := s.q.UpdatePayoutProfile(ctx, tx, db.UpdatePayoutProfileParams{AccountID: p.AccountID, Version: p.Version, DefaultPayoutAccountID: p.DefaultPayoutAccountID,
		RentPayoutAccountID: p.RentPayoutAccountID, SalePayoutAccountID: p.SalePayoutAccountID, ShortletPayoutAccountID: p.ShortletPayoutAccountID, UpdatedAt: now})
	if isNoRows(err) {
		return r, lifecycle.ErrVersionConflict()
	}
	return r, err
}

// RemovePayoutAccount removes an account (the default only when it is the last one).
func (s *Service) RemovePayoutAccount(ctx context.Context, subj authz.Subject, id ids.ID) (Removed, error) {
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActPayoutWrite, ids.Nil); err != nil {
			return err
		}
		tx, acc := mustTx(ctx), accountOf(subj)
		a, err := s.q.GetPayoutAccountForUpdate(ctx, tx, db.GetPayoutAccountForUpdateParams{ID: id, AccountID: acc})
		if isNoRows(err) || (err == nil && a.Status == "REMOVED") {
			return notFound("payout_account")
		}
		if err != nil {
			return err
		}
		prof, err := s.lockPayoutProfile(ctx, acc)
		if err != nil {
			return err
		}
		mapped := func(p *ids.ID) bool { return p != nil && *p == id }
		if mapped(prof.DefaultPayoutAccountID) {
			all, err := s.q.ListPayoutAccounts(ctx, tx, acc)
			if err != nil {
				return err
			}
			for _, o := range all {
				if o.ID != id && o.Status == "ACTIVE" {
					return conflict("PAYOUT_ACCOUNT_IS_DEFAULT", "Make another account the default before removing this one.")
				}
			}
		}
		if prof.Version > 0 {
			clear := func(p *ids.ID) *ids.ID {
				if mapped(p) {
					return nil
				}
				return p
			}
			prof.DefaultPayoutAccountID, prof.RentPayoutAccountID = clear(prof.DefaultPayoutAccountID), clear(prof.RentPayoutAccountID)
			prof.SalePayoutAccountID, prof.ShortletPayoutAccountID = clear(prof.SalePayoutAccountID), clear(prof.ShortletPayoutAccountID)
			if _, err := s.savePayoutProfile(ctx, prof); err != nil {
				return err
			}
		}
		removed, err := s.q.RemovePayoutAccount(ctx, tx, db.RemovePayoutAccountParams{ID: id, Version: a.Version, RemovedAt: ptr(s.now())})
		if err != nil {
			return err
		}
		return s.payoutChanged(ctx, subj, removed, "REMOVE")
	})
	return Removed{ID: id, Removed: err == nil}, err
}

// PayoutPreferencesView maps business lines to payout accounts.
type PayoutPreferencesView struct {
	DefaultPayoutAccountID  *ids.ID   `json:"default_payout_account_id"`
	RentPayoutAccountID     *ids.ID   `json:"rent_payout_account_id"`
	SalePayoutAccountID     *ids.ID   `json:"sale_payout_account_id"`
	ShortletPayoutAccountID *ids.ID   `json:"shortlet_payout_account_id"`
	Version                 int64     `json:"version"`
	UpdatedAt               time.Time `json:"updated_at"`
}

// PayoutPreferencesInput replaces the mapping (each account must be ACTIVE).
type PayoutPreferencesInput struct {
	DefaultPayoutAccountID  *ids.ID `json:"default_payout_account_id,omitempty"`
	RentPayoutAccountID     *ids.ID `json:"rent_payout_account_id,omitempty"`
	SalePayoutAccountID     *ids.ID `json:"sale_payout_account_id,omitempty"`
	ShortletPayoutAccountID *ids.ID `json:"shortlet_payout_account_id,omitempty"`
}

func prefsView(p db.PayoutProfile) PayoutPreferencesView {
	return PayoutPreferencesView{DefaultPayoutAccountID: p.DefaultPayoutAccountID, RentPayoutAccountID: p.RentPayoutAccountID,
		SalePayoutAccountID: p.SalePayoutAccountID, ShortletPayoutAccountID: p.ShortletPayoutAccountID, Version: p.Version, UpdatedAt: p.UpdatedAt}
}

// GetPayoutPreferences returns the mapping (version 0 when never saved).
func (s *Service) GetPayoutPreferences(ctx context.Context, subj authz.Subject) (PayoutPreferencesView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPayoutRead, ids.Nil); err != nil {
		return PayoutPreferencesView{}, err
	}
	p, err := s.q.GetPayoutProfile(ctx, s.dbq(ctx), accountOf(subj))
	if isNoRows(err) {
		return PayoutPreferencesView{}, nil
	}
	return prefsView(p), err
}

// PutPayoutPreferences replaces the mapping with If-Match (step-up).
func (s *Service) PutPayoutPreferences(ctx context.Context, subj authz.Subject, expected int64, in PayoutPreferencesInput) (PayoutPreferencesView, error) {
	var out PayoutPreferencesView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActPayoutWrite, ids.Nil); err != nil {
			return err
		}
		tx, acc := mustTx(ctx), accountOf(subj)
		for _, id := range []*ids.ID{in.DefaultPayoutAccountID, in.RentPayoutAccountID, in.SalePayoutAccountID, in.ShortletPayoutAccountID} {
			if id == nil {
				continue
			}
			a, err := s.q.GetPayoutAccount(ctx, tx, db.GetPayoutAccountParams{ID: *id, AccountID: acc})
			if isNoRows(err) || (err == nil && a.Status != "ACTIVE") {
				return precondition("PAYOUT_ACCOUNT_NOT_ACTIVE", "Every mapped payout account must be ACTIVE.")
			}
			if err != nil {
				return err
			}
		}
		prof, err := s.lockPayoutProfile(ctx, acc)
		if err != nil {
			return err
		}
		if expected != prof.Version {
			return lifecycle.ErrVersionConflict()
		}
		prof.DefaultPayoutAccountID, prof.RentPayoutAccountID = in.DefaultPayoutAccountID, in.RentPayoutAccountID
		prof.SalePayoutAccountID, prof.ShortletPayoutAccountID = in.SalePayoutAccountID, in.ShortletPayoutAccountID
		saved, err := s.savePayoutProfile(ctx, prof)
		if err != nil {
			return err
		}
		if err := s.record(ctx, "owner.payout_preferences.update", "owner_account", acc, map[string]any{"version": saved.Version}, nil); err != nil {
			return err
		}
		a, err := s.q.GetAccount(ctx, tx, acc)
		if err != nil {
			return err
		}
		if err := s.enqueueAlert(ctx, tx, a, "PAYOUT_CHANGED", ""); err != nil {
			return err
		}
		out = prefsView(saved)
		return nil
	})
	return out, err
}

// TaxProfileView is the masked tax/reporting profile.
type TaxProfileView struct {
	TaxIDType            *string   `json:"tax_id_type"`
	TaxIDLast4           *string   `json:"tax_id_last4"`
	ResidencyCountry     *string   `json:"residency_country"`
	ReportingPreferences []string  `json:"reporting_preferences"`
	PolicyVersion        *string   `json:"policy_version"`
	Version              int64     `json:"version"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// TaxProfileInput replaces the tax profile. Tax rules are not hard-coded:
// the profile records identifiers and preferences only.
type TaxProfileInput struct {
	TaxIDType            *string  `json:"tax_id_type,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,31}" ex:"TIN"`
	TaxID                *string  `json:"tax_id,omitempty" v:"min=4,max=40,pattern=[A-Za-z0-9-]+" doc:"Only a keyed fingerprint and the last 4 characters are stored"`
	ResidencyCountry     *string  `json:"residency_country,omitempty" v:"format=country"`
	ReportingPreferences []string `json:"reporting_preferences,omitempty" v:"max=10,enum=ANNUAL_STATEMENT|QUARTERLY_STATEMENT|WITHHOLDING_CERTIFICATES"`
}

func taxView(t db.TaxProfile) TaxProfileView {
	return TaxProfileView{TaxIDType: t.TaxIDType, TaxIDLast4: t.TaxIDLast4, ResidencyCountry: t.ResidencyCountry,
		ReportingPreferences: nonNil(t.ReportingPreferences), PolicyVersion: t.PolicyVersion, Version: t.Version, UpdatedAt: t.UpdatedAt}
}

// GetTaxProfile returns the masked tax profile (version 0 when never saved).
func (s *Service) GetTaxProfile(ctx context.Context, subj authz.Subject) (TaxProfileView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPayoutRead, ids.Nil); err != nil {
		return TaxProfileView{}, err
	}
	t, err := s.q.GetTaxProfile(ctx, s.dbq(ctx), accountOf(subj))
	if isNoRows(err) {
		return TaxProfileView{ReportingPreferences: []string{}}, nil
	}
	return taxView(t), err
}

// PutTaxProfile replaces the tax profile with If-Match (step-up).
func (s *Service) PutTaxProfile(ctx context.Context, subj authz.Subject, expected int64, in TaxProfileInput) (TaxProfileView, error) {
	if err := schema.Validate(in); err != nil {
		return TaxProfileView{}, err
	}
	if (in.TaxID == nil) != (in.TaxIDType == nil) {
		return TaxProfileView{}, apperrors.InvalidRequest("tax_id and tax_id_type must be given together.")
	}
	var out TaxProfileView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActTaxWrite, ids.Nil); err != nil {
			return err
		}
		tx, now, acc := mustTx(ctx), s.now(), accountOf(subj)
		var last4, fp *string
		if in.TaxID != nil {
			id := strings.ToUpper(*in.TaxID)
			l := id[len(id)-4:]
			h := s.Hasher.Hash("tax-id", acc.String(), *in.TaxIDType, id)
			last4, fp = &l, &h
		}
		prefs := nonNil(in.ReportingPreferences)
		slices.Sort(prefs)
		cur, err := s.q.GetTaxProfile(ctx, tx, acc)
		var t db.TaxProfile
		switch {
		case isNoRows(err):
			if expected != 0 {
				return lifecycle.ErrVersionConflict()
			}
			t, err = s.q.InsertTaxProfile(ctx, tx, db.InsertTaxProfileParams{AccountID: acc, TaxIDType: in.TaxIDType, TaxIDLast4: last4, TaxIDFingerprint: fp,
				ResidencyCountry: in.ResidencyCountry, ReportingPreferences: prefs, PolicyVersion: &s.Policy.Version, UpdatedAt: now})
			if isUnique(err, "") {
				return lifecycle.ErrVersionConflict()
			}
		case err != nil:
			return err
		default:
			if expected != cur.Version {
				return lifecycle.ErrVersionConflict()
			}
			if in.TaxID == nil {
				last4, fp = cur.TaxIDLast4, cur.TaxIDFingerprint
				in.TaxIDType = cur.TaxIDType
			}
			t, err = s.q.UpdateTaxProfile(ctx, tx, db.UpdateTaxProfileParams{AccountID: acc, Version: cur.Version, TaxIDType: in.TaxIDType, TaxIDLast4: last4,
				TaxIDFingerprint: fp, ResidencyCountry: in.ResidencyCountry, ReportingPreferences: prefs, PolicyVersion: &s.Policy.Version, UpdatedAt: now})
			if isNoRows(err) {
				return lifecycle.ErrVersionConflict()
			}
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "owner.tax_profile.update", "owner_account", acc, map[string]any{"version": t.Version, "tax_id_changed": in.TaxID != nil}, nil); err != nil {
			return err
		}
		a, err := s.q.GetAccount(ctx, tx, acc)
		if err != nil {
			return err
		}
		if err := s.enqueueAlert(ctx, tx, a, "TAX_PROFILE_CHANGED", ""); err != nil {
			return err
		}
		out = taxView(t)
		return nil
	})
	return out, err
}

// ListStatements returns payment-service statements (financial authority).
func (s *Service) ListStatements(ctx context.Context, subj authz.Subject, cursor string, limit int) ([]payment.Statement, string, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPayoutRead, ids.Nil); err != nil {
		return nil, "", err
	}
	if s.Payment == nil {
		return nil, "", unavailable("Owner statements", "payment-service")
	}
	st, next, err := s.Payment.ListStatements(ctx, accountOf(subj), cursor, limit)
	if err != nil {
		return nil, "", downstream("payment-service", err)
	}
	return st, next, nil
}

// ActivateCooledPayoutAccounts completes cooling-off periods (worker).
func (s *Service) ActivateCooledPayoutAccounts(ctx context.Context, max int32) (int, error) {
	n := 0
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx, now := mustTx(ctx), s.now()
		rows, err := s.q.ActivateCooledPayoutAccounts(ctx, tx, db.ActivateCooledPayoutAccountsParams{Now: now, MaxRows: max})
		if err != nil {
			return err
		}
		if _, err := s.q.ApplyDuePayoutChanges(ctx, tx, db.ApplyDuePayoutChangesParams{Now: now, MaxRows: max}); err != nil {
			return err
		}
		for _, a := range rows {
			ctx := withAccountActor(ctx, a.AccountID)
			if err := s.record(ctx, "owner.payout_account.activate", "payout_account", a.ID, map[string]any{"status": "ACTIVE"}, nil); err != nil {
				return err
			}
			acc, err := s.q.GetAccount(ctx, tx, a.AccountID)
			if err != nil {
				return err
			}
			if err := s.enqueueAlert(ctx, tx, acc, "PAYOUT_CHANGED", ""); err != nil {
				return err
			}
		}
		n = len(rows)
		return nil
	})
	return n, err
}
