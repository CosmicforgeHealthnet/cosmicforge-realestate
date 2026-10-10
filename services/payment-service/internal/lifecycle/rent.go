package lifecycle

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"time"
)

type Agreement struct {
	ID                   ids.ID            `json:"agreement_id"`
	Domain               string            `json:"domain"`
	Reference            string            `json:"agreement_ref"`
	Version              string            `json:"version"`
	PreviousID           *ids.ID           `json:"previous_id,omitempty"`
	ApprovalRef          string            `json:"approval_ref"`
	ChangeEvidenceRef    string            `json:"change_evidence_ref"`
	BusinessRef          string            `json:"business_ref"`
	PayerWalletID        ids.ID            `json:"payer_wallet_id"`
	PayerPartyRef        string            `json:"payer_party_ref"`
	PayerConsentRef      string            `json:"payer_consent_ref"`
	TenancyType          string            `json:"tenancy_type"`
	Status               string            `json:"status"`
	Schedule             string            `json:"schedule"`
	Currency             string            `json:"currency"`
	Jurisdiction         string            `json:"jurisdiction"`
	EffectiveFrom        time.Time         `json:"effective_from"`
	StartsAt             time.Time         `json:"starts_at"`
	EndsAt               *time.Time        `json:"ends_at,omitempty"`
	InitialPolicyID      ids.ID            `json:"initial_policy_id"`
	RecurringPolicyID    ids.ID            `json:"recurring_policy_id"`
	DepositPolicyID      *ids.ID           `json:"deposit_policy_id,omitempty"`
	Components           []Component       `json:"components"`
	RentShares           []RentShare       `json:"rent_shares"`
	PlatformAuthorityID  *ids.ID           `json:"platform_authority_id,omitempty"`
	ManagerAuthorityID   *ids.ID           `json:"manager_authority_id,omitempty"`
	ManagerFeeMinor      int64             `json:"manager_fee_minor"`
	ManagerContractRef   string            `json:"manager_contract_ref"`
	ComponentAuthorities map[string]ids.ID `json:"component_authorities"`
}
type RentShare struct {
	AuthorityID  ids.ID `json:"authority_id"`
	OwnershipBPS int    `json:"ownership_bps"`
}

func (s *Service) PutAgreement(ctx context.Context, in Agreement) (Agreement, error) {
	if e := core.DomainCaller(ctx, in.Domain); e != nil {
		return in, e
	}
	if !has([]string{"individual-owner", "business-operations"}, in.Domain) || in.ID.ValidateV7() != nil || in.Reference == "" || in.Version == "" || in.ApprovalRef == "" || in.BusinessRef == "" || in.PayerConsentRef == "" || in.PayerPartyRef == "" || in.EffectiveFrom.IsZero() || in.StartsAt.IsZero() || !has([]string{"FIXED_TERM", "CONTINUING"}, in.TenancyType) || !has([]string{"ACTIVE", "TERMINATED"}, in.Status) || in.ManagerFeeMinor < 0 || len(in.RentShares) == 0 {
		return in, fail("AUTHORITATIVE_AGREEMENT_REQUIRED")
	}
	if in.TenancyType == "FIXED_TERM" && (in.EndsAt == nil || !in.EndsAt.After(in.StartsAt)) {
		return in, fail("FIXED_TERM_DATES_REQUIRED")
	}
	if in.ManagerFeeMinor > 0 && (in.ManagerAuthorityID == nil || in.ManagerContractRef == "") {
		return in, fail("MANAGER_FEE_CONTRACT_REQUIRED")
	}
	initial, e := s.Policy(ctx, in.InitialPolicyID)
	if e != nil {
		return in, e
	}
	recurring, e := s.Policy(ctx, in.RecurringPolicyID)
	if e != nil {
		return in, e
	}
	if initial.Key != "RESIDENTIAL_INITIAL" || !has([]string{"RESIDENTIAL_SUBSEQUENT", "TENANCY_RENEWAL"}, recurring.Key) || initial.Currency != in.Currency || recurring.Currency != in.Currency || initial.Jurisdiction != in.Jurisdiction || recurring.Jurisdiction != in.Jurisdiction || !has(initial.Schedules, in.Schedule) || !has(recurring.Schedules, in.Schedule) {
		return in, fail("RENT_POLICY_MISMATCH")
	}
	var bps int
	for _, x := range in.RentShares {
		if x.OwnershipBPS <= 0 || x.OwnershipBPS > 10000 {
			return in, fail("VERIFIED_OWNERSHIP_REQUIRED")
		}
		bps += x.OwnershipBPS
	}
	if bps != 10000 {
		return in, fail("COOWNER_PERCENTAGES_NOT_RECONCILED")
	}
	rentCount := 0
	seen := map[string]bool{}
	for _, c := range in.Components {
		if c.AmountMinor <= 0 || seen[c.Ref] || c.Ref == "" {
			return in, fail("INVALID_RENT_COMPONENT")
		}
		seen[c.Ref] = true
		if c.Kind == "RENT" {
			rentCount++
			if c.Recurrence != "RECURRING" {
				return in, fail("RENT_RECURRENCE_REQUIRED")
			}
		}
		if c.Kind == "DEPOSIT" && (c.Recurrence != "REFUNDABLE_DEPOSIT" || in.DepositPolicyID == nil) {
			return in, fail("DEPOSIT_MUST_BE_SEPARATE")
		}
		if c.Kind == "LEGAL" && (c.Recurrence != "ACTUAL_SERVICE_ONLY" || c.ServiceEvidenceRef == "") {
			return in, fail("ACTUAL_LEGAL_SERVICE_REQUIRED")
		}
		if has([]string{"ONBOARDING", "PLATFORM"}, c.Kind) && c.Recurrence != "ONE_TIME" {
			return in, fail("INITIAL_CHARGE_RECURRENCE_FORBIDDEN")
		}
	}
	if rentCount != 1 {
		return in, fail("ONE_BASE_RENT_REQUIRED")
	}
	p, _ := core.Caller(ctx)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return in, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "agreement:"+string(p.Service)+in.Reference); e != nil {
		return in, e
	}
	var old Agreement
	e = load(ctx, tx, `SELECT snapshot FROM rent_agreements WHERE agreement_id=$1`, in.ID, &old)
	if e == nil {
		if core.Fingerprint(old) != core.Fingerprint(in) {
			return in, core.Fail(409, "AGREEMENT_VERSION_IMMUTABLE")
		}
		return old, nil
	}
	var count int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM rent_agreements WHERE source_service=$1 AND agreement_ref=$2`, p.Service, in.Reference).Scan(&count)
	if e != nil {
		return in, e
	}
	if count > 0 {
		if in.PreviousID == nil || in.ChangeEvidenceRef == "" {
			return in, fail("AUTHORITATIVE_MATERIAL_CHANGE_REQUIRED")
		}
		if e = load(ctx, tx, `SELECT snapshot FROM rent_agreements WHERE agreement_id=$1`, *in.PreviousID, &old); e != nil {
			return in, e
		}
		if old.Reference != in.Reference || !in.EffectiveFrom.After(old.EffectiveFrom) || in.PayerWalletID != old.PayerWalletID {
			return in, fail("AGREEMENT_SUPERSESSION_MISMATCH")
		}
		var latest ids.ID
		e = tx.QueryRow(ctx, `SELECT agreement_id FROM rent_agreements WHERE source_service=$1 AND agreement_ref=$2 ORDER BY effective_from DESC LIMIT 1`, p.Service, in.Reference).Scan(&latest)
		if e != nil {
			return in, e
		}
		if latest != *in.PreviousID {
			return in, core.Fail(409, "AGREEMENT_SUPERSESSION_MISMATCH")
		}
	} else if in.PreviousID != nil {
		return in, fail("AGREEMENT_SUPERSESSION_MISMATCH")
	}
	_, e = tx.Exec(ctx, `INSERT INTO rent_agreements(agreement_id,source_service,agreement_ref,version,fingerprint,snapshot,effective_from) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.ID, p.Service, in.Reference, in.Version, core.Fingerprint(in), raw(in), in.EffectiveFrom)
	if e != nil {
		return in, e
	}
	if e = s.Audit(ctx, tx, "rent.agreement_referenced", in.ID.String(), in); e != nil {
		return in, e
	}
	return in, tx.Commit(ctx)
}

type RentRequest struct {
	AgreementID ids.ID    `json:"agreement_id"`
	PeriodRef   string    `json:"period_ref"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	DueAt       time.Time `json:"due_at"`
	Initial     bool      `json:"initial"`
	Stream      string    `json:"stream"`
}
type Obligation struct {
	ID ids.ID `json:"obligation_id"`
	RentRequest
	Quote            Quote  `json:"quote"`
	AgreementVersion string `json:"agreement_version"`
	Schedule         string `json:"schedule"`
	Status           string `json:"status"`
}

func (s *Service) RentObligation(ctx context.Context, in RentRequest) (Obligation, error) {
	var agreement Agreement
	if e := load(ctx, s.DB, `SELECT snapshot FROM rent_agreements WHERE agreement_id=$1`, in.AgreementID, &agreement); e != nil {
		return Obligation{}, e
	}
	if e := core.DomainCaller(ctx, agreement.Domain); e != nil {
		return Obligation{}, e
	}
	if in.PeriodRef == "" || in.DueAt.IsZero() || !in.PeriodEnd.After(in.PeriodStart) || !has([]string{"RENT", "DEPOSIT"}, in.Stream) {
		return Obligation{}, fail("RENT_PERIOD_REQUIRED")
	}
	p, _ := core.Caller(ctx)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Obligation{}, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "rent:"+string(p.Service)+agreement.Reference); e != nil {
		return Obligation{}, e
	}
	var prior ids.ID
	var fp string
	e = tx.QueryRow(ctx, `SELECT obligation_id,fingerprint FROM rent_obligations WHERE source_service=$1 AND agreement_ref=$2 AND period_ref=$3 AND stream=$4`, p.Service, agreement.Reference, in.PeriodRef, in.Stream).Scan(&prior, &fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return Obligation{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		var old Obligation
		e = load(ctx, tx, `SELECT snapshot FROM rent_obligations WHERE obligation_id=$1`, prior, &old)
		return old, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Obligation{}, e
	}
	var latest ids.ID
	e = tx.QueryRow(ctx, `SELECT agreement_id FROM rent_agreements WHERE source_service=$1 AND agreement_ref=$2 AND effective_from<=$3 ORDER BY effective_from DESC LIMIT 1`, p.Service, agreement.Reference, in.PeriodStart).Scan(&latest)
	if e != nil {
		return Obligation{}, e
	}
	if latest != agreement.ID || agreement.Status != "ACTIVE" || in.PeriodStart.Before(agreement.StartsAt) || (agreement.EndsAt != nil && in.PeriodEnd.After(*agreement.EndsAt)) {
		return Obligation{}, core.Fail(403, "ACTIVE_AGREEMENT_PERIOD_REQUIRED")
	}
	var overlapping bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rent_obligations WHERE source_service=$1 AND agreement_ref=$2 AND stream=$3 AND (snapshot->>'period_start')::timestamptz<$5 AND (snapshot->>'period_end')::timestamptz>$4)`, p.Service, agreement.Reference, in.Stream, in.PeriodStart, in.PeriodEnd).Scan(&overlapping)
	if e != nil {
		return Obligation{}, e
	}
	if overlapping {
		return Obligation{}, core.Fail(409, "RENT_PERIOD_OVERLAP")
	}
	policyID := agreement.RecurringPolicyID
	if in.Initial {
		policyID = agreement.InitialPolicyID
	}
	if in.Stream == "DEPOSIT" {
		if agreement.DepositPolicyID == nil {
			return Obligation{}, fail("DEPOSIT_POLICY_REQUIRED")
		}
		policyID = *agreement.DepositPolicyID
	}
	var policy Policy
	e = load(ctx, tx, `SELECT snapshot FROM financial_policies WHERE policy_id=$1`, policyID, &policy)
	if e != nil {
		return Obligation{}, e
	}
	if e = policyAt(policy, in.PeriodStart); e != nil {
		return Obligation{}, e
	}
	components := []Component{}
	amounts := []int64{}
	for _, c := range agreement.Components {
		if in.Stream == "DEPOSIT" {
			if c.Recurrence != "REFUNDABLE_DEPOSIT" {
				continue
			}
		} else {
			if c.Recurrence == "REFUNDABLE_DEPOSIT" {
				continue
			}
			if !in.Initial && c.Recurrence != "RECURRING" {
				continue
			}
		}
		components = append(components, c)
		amounts = append(amounts, c.AmountMinor)
	}
	total, e := sum(amounts)
	if e != nil || total <= 0 {
		return Obligation{}, fail("NO_ELIGIBLE_RENT_COMPONENTS")
	}
	if in.Stream == "RENT" && in.Initial {
		var anyInitial bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rent_obligations WHERE source_service=$1 AND agreement_ref=$2 AND stream='RENT' AND snapshot->>'initial'='true')`, p.Service, agreement.Reference).Scan(&anyInitial)
		if e != nil {
			return Obligation{}, e
		}
		if anyInitial {
			return Obligation{}, core.Fail(409, "INITIAL_CHARGES_ALREADY_ISSUED")
		}
	}
	out := Obligation{ID: ids.New(), RentRequest: in, AgreementVersion: agreement.Version, Schedule: agreement.Schedule, Status: "DUE"}
	out.Quote = Quote{ID: ids.New(), Domain: agreement.Domain, BusinessRef: agreement.BusinessRef, PayerWalletID: agreement.PayerWalletID, PayerType: "TENANT", PayerConsentRef: agreement.PayerConsentRef, LegalPartyRef: agreement.PayerPartyRef, QuoteVersion: agreement.Version + ":" + in.PeriodRef + ":" + in.Stream, AgreementRef: agreement.Reference, AgreementVersion: agreement.Version, PolicyID: policyID, Currency: agreement.Currency, Jurisdiction: agreement.Jurisdiction, Schedule: agreement.Schedule, AmountMinor: total, Components: components}
	if e = s.validateQuoteWith(ctx, tx, out.Quote); e != nil {
		return out, e
	}
	if e = insertQuote(ctx, tx, out.Quote, "rent:"+out.ID.String(), string(p.Service)); e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO rent_obligations(obligation_id,agreement_id,agreement_ref,period_ref,source_service,stream,quote_id,due_at,fingerprint,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, out.ID, agreement.ID, agreement.Reference, in.PeriodRef, p.Service, in.Stream, out.Quote.ID, in.DueAt, core.Fingerprint(in), raw(out))
	if e != nil {
		return out, e
	}
	if e = s.Emit(ctx, tx, "finance.rent_obligation_created.v1", "rent_obligation", out.ID, out); e != nil {
		return out, e
	}
	if e = s.Audit(ctx, tx, "rent.obligation_issued", out.ID.String(), out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) Obligation(ctx context.Context, id ids.ID) (Obligation, error) {
	var o Obligation
	e := load(ctx, s.DB, `SELECT snapshot FROM rent_obligations WHERE obligation_id=$1`, id, &o)
	if e != nil {
		return o, e
	}
	if _, e = s.Quote(ctx, o.Quote.ID); e != nil {
		return Obligation{}, e
	}
	var state string
	e = s.DB.QueryRow(ctx, `SELECT COALESCE((SELECT state FROM payment_intents WHERE locked_quote_id=$1),'DUE')`, o.Quote.ID).Scan(&state)
	o.Status = state
	return o, e
}
func (s *Service) RentAllocation(ctx context.Context, id ids.ID, intent ids.ID) (Allocation, error) {
	o, e := s.Obligation(ctx, id)
	if e != nil {
		return Allocation{}, e
	}
	var a Agreement
	if e = load(ctx, s.DB, `SELECT snapshot FROM rent_agreements WHERE agreement_id=$1`, o.AgreementID, &a); e != nil {
		return Allocation{}, e
	}
	if e = core.DomainCaller(ctx, a.Domain); e != nil {
		return Allocation{}, e
	}
	var matched bool
	e = s.DB.QueryRow(ctx, `SELECT locked_quote_id=$2 FROM payment_intents WHERE intent_id=$1`, intent, o.Quote.ID).Scan(&matched)
	if e != nil {
		return Allocation{}, e
	}
	if !matched {
		return Allocation{}, fail("RENT_INTENT_MISMATCH")
	}
	policy, e := s.Policy(ctx, o.Quote.PolicyID)
	if e != nil {
		return Allocation{}, e
	}
	lines := []LineInput{}
	if a.ManagerFeeMinor > 0 {
		var authority Authority
		if e = load(ctx, s.DB, `SELECT snapshot FROM beneficiary_authorities WHERE authority_id=$1`, *a.ManagerAuthorityID, &authority); e != nil {
			return Allocation{}, e
		}
		if !has([]string{"MANAGER", "MANAGEMENT_COMPANY"}, authority.Role) || authority.MandateRef != a.ManagerContractRef {
			return Allocation{}, fail("MANAGER_FEE_CONTRACT_MISMATCH")
		}
	}
	for _, c := range o.Quote.Components {
		if c.Kind == "RENT" {
			commission, e := mul(c.AmountMinor, policy.CommissionBPS)
			if e != nil {
				return Allocation{}, e
			}
			net := c.AmountMinor - commission - a.ManagerFeeMinor
			if net <= 0 {
				return Allocation{}, fail("RENT_FEES_EXCEED_PROCEEDS")
			}
			for _, owner := range a.RentShares {
				amount, e := mul(net, owner.OwnershipBPS)
				if e != nil {
					return Allocation{}, e
				}
				if amount > 0 {
					lines = append(lines, LineInput{AuthorityID: owner.AuthorityID, ComponentRef: c.Ref, Kind: "BENEFICIARY", AmountMinor: amount, Currency: o.Quote.Currency})
				}
			}
			if commission > 0 {
				if a.PlatformAuthorityID == nil {
					return Allocation{}, fail("COMMISSION_AUTHORITY_REQUIRED")
				}
				lines = append(lines, LineInput{AuthorityID: *a.PlatformAuthorityID, ComponentRef: c.Ref, Kind: "PLATFORM", AmountMinor: commission, Currency: o.Quote.Currency})
			}
			if a.ManagerFeeMinor > 0 {
				lines = append(lines, LineInput{AuthorityID: *a.ManagerAuthorityID, ComponentRef: c.Ref, Kind: "BENEFICIARY", AmountMinor: a.ManagerFeeMinor, Currency: o.Quote.Currency})
			}
		} else {
			authority, ok := a.ComponentAuthorities[c.Ref]
			if !ok {
				return Allocation{}, fail("COMPONENT_BENEFICIARY_REQUIRED")
			}
			kind := "BENEFICIARY"
			if c.Kind == "DEPOSIT" {
				kind = "DEPOSIT"
			}
			if has([]string{"ONBOARDING", "PLATFORM"}, c.Kind) {
				kind = "PLATFORM"
			}
			if c.Kind == "TAX" {
				kind = "TAX"
			}
			if c.Kind == "PROCESSING" {
				kind = "PROCESSING"
			}
			lines = append(lines, LineInput{AuthorityID: authority, ComponentRef: c.Ref, Kind: kind, AmountMinor: c.AmountMinor, Currency: o.Quote.Currency})
		}
	}
	return s.Allocate(ctx, AllocationInput{IntentID: intent, Version: 1, Lines: lines})
}

// Receipt is a projection of immutable quote/components and confirmed financial
// states. It never converts a deposit to rent or claims an unconfirmed payout.
type Receipt struct {
	Quote           Quote        `json:"quote"`
	IntentID        *ids.ID      `json:"intent_id,omitempty"`
	CollectionState string       `json:"collection_state"`
	SettlementState string       `json:"settlement_state"`
	Allocations     []Allocation `json:"allocations"`
}

func (s *Service) Receipt(ctx context.Context, quote ids.ID) (Receipt, error) {
	q, e := s.Quote(ctx, quote)
	if e != nil {
		return Receipt{}, e
	}
	out := Receipt{Quote: q, CollectionState: "NOT_INITIATED", SettlementState: "PENDING", Allocations: []Allocation{}}
	var intent ids.ID
	e = s.DB.QueryRow(ctx, `SELECT intent_id,state,settlement_state FROM payment_intents WHERE locked_quote_id=$1`, quote).Scan(&intent, &out.CollectionState, &out.SettlementState)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.IntentID = &intent
	rows, e := s.DB.Query(ctx, `SELECT allocation_id FROM allocation_snapshots WHERE intent_id=$1 ORDER BY version`, intent)
	if e != nil {
		return out, e
	}
	xs := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		xs = append(xs, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, id := range xs {
		a, e := s.Allocation(ctx, id)
		if e != nil {
			var denied *apperrors.Error
			if errors.As(e, &denied) && denied.Status == 403 {
				// A payer may read its quote and collection receipt without
				// gaining access to another party's allocation or wallet.
				continue
			}
			return out, e
		}
		out.Allocations = append(out.Allocations, a)
	}
	return out, nil
}
