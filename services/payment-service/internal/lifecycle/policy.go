// Package lifecycle implements blueprint Sections 4–6 over the existing Financial Core.
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5"
	"math/big"
	"time"
)

type Service struct{ *finance.Service }

func New(s *finance.Service) *Service {
	l := &Service{s}
	s.SetTransferReadback(func(ctx context.Context, provider ids.ID, ref string) error {
		var id ids.ID
		e := s.DB.QueryRow(ctx, `SELECT payout_id FROM payout_attempts WHERE provider_account_id=$1 AND provider_reference=$2`, provider, ref).Scan(&id)
		if e != nil {
			return e
		}
		p, e := l.ResolvePayout(trusted(ctx), id)
		if e != nil {
			return e
		}
		if !has([]string{"CONFIRMED_PAID", "CONFIRMED_FAILED"}, p.State) {
			return core.Fail(503, "PAYOUT_CONFIRMATION_PENDING")
		}
		return nil
	})
	return l
}
func trusted(ctx context.Context) context.Context {
	return serviceauth.WithPrincipal(ctx, serviceauth.Principal{Service: platform.PaymentService})
}
func has(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
func raw(v any) string       { b, _ := json.Marshal(v); return string(b) }
func fail(code string) error { return core.Fail(422, code) }
func sum(xs []int64) (int64, error) {
	n := new(big.Int)
	for _, x := range xs {
		if x <= 0 {
			return 0, fail("POSITIVE_MINOR_UNITS_REQUIRED")
		}
		n.Add(n, big.NewInt(x))
	}
	if !n.IsInt64() {
		return 0, fail("MONEY_OVERFLOW")
	}
	return n.Int64(), nil
}
func mul(n int64, bps int) (int64, error) {
	x := new(big.Int).Mul(big.NewInt(n), big.NewInt(int64(bps)))
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(x, big.NewInt(10000), r)
	if r.Sign() != 0 || !q.IsInt64() {
		return 0, fail("EXPLICIT_ROUNDING_ALLOCATION_REQUIRED")
	}
	return q.Int64(), nil
}
func lock(ctx context.Context, tx pgx.Tx, key string) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key)
	return e
}

type query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func load(ctx context.Context, q query, sql string, id ids.ID, out any) error {
	var b []byte
	e := q.QueryRow(ctx, sql, id).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return core.Fail(404, "FINANCIAL_RECORD_NOT_FOUND")
	}
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}

type Family struct {
	TemplateVersion      string   `json:"template_version"`
	JurisdictionRequired bool     `json:"jurisdiction_policy_required"`
	CancellationRef      string   `json:"cancellation_policy_ref"`
	RefundRef            string   `json:"refund_policy_ref"`
	DisputeRef           string   `json:"dispute_policy_ref"`
	Key                  string   `json:"policy_key"`
	Payers               []string `json:"payers"`
	Components           []string `json:"components"`
	Roles                []string `json:"beneficiary_roles"`
	Conditions           []string `json:"release_conditions"`
	Deposit              bool     `json:"separate_deposit"`
	Regulated            bool     `json:"regulated_partner_required"`
	Enabled              bool     `json:"enabled"`
}

func Families() []Family {
	families := []Family{
		{Key: "RESIDENTIAL_INITIAL", Payers: []string{"TENANT"}, Components: []string{"RENT", "LEGAL", "ONBOARDING", "PLATFORM", "DEPOSIT", "SERVICE", "UTILITY", "TAX", "PROCESSING"}, Roles: []string{"OWNER", "CO_OWNER", "PROFESSIONAL", "PROFESSIONAL_FIRM", "MANAGER", "MANAGEMENT_COMPANY", "PLATFORM"}, Conditions: []string{"SIGNED_AGREEMENT", "ACCEPTED_QUOTE", "POSSESSION"}, Deposit: true},
		{Key: "RESIDENTIAL_SUBSEQUENT", Payers: []string{"TENANT"}, Components: []string{"RENT", "SERVICE", "UTILITY", "TAX", "PROCESSING"}, Roles: []string{"OWNER", "CO_OWNER", "MANAGER", "MANAGEMENT_COMPANY", "PLATFORM"}, Conditions: []string{"VALID_TENANCY", "RENT_DUE"}},
		{Key: "TENANCY_RENEWAL", Payers: []string{"TENANT"}, Components: []string{"RENT", "SERVICE", "UTILITY", "TAX", "PROCESSING"}, Roles: []string{"OWNER", "CO_OWNER", "MANAGER", "MANAGEMENT_COMPANY", "PLATFORM"}, Conditions: []string{"VALID_TENANCY", "APPROVED_CHANGE"}},
		{Key: "RESIDENTIAL_DEPOSIT", Payers: []string{"TENANT"}, Components: []string{"DEPOSIT"}, Roles: []string{"CUSTOMER"}, Conditions: []string{"MOVE_OUT", "APPROVED_CLAIM", "DISPUTE_DECISION"}, Deposit: true},
		{Key: "COMMERCIAL_LEASE", Payers: []string{"LESSEE", "CORPORATE_LESSEE"}, Components: []string{"RENT", "FIT_OUT", "SERVICE", "DEPOSIT", "TAX", "PROCESSING"}, Roles: []string{"CORPORATE_OWNER", "MANAGER", "MANAGEMENT_COMPANY", "SERVICE_COMPANY", "PLATFORM"}, Conditions: []string{"EXECUTED_LEASE", "COMMENCEMENT", "CORPORATE_SIGNATORY"}, Deposit: true},
		{Key: "LAND_LEASE", Payers: []string{"LESSEE"}, Components: []string{"RESERVATION", "OCCUPATION", "INSTALLMENT", "DEPOSIT", "PROFESSIONAL", "PROCESSING"}, Roles: []string{"OWNER", "CO_OWNER", "CORPORATE_OWNER", "PROFESSIONAL", "PLATFORM"}, Conditions: []string{"LAND_AUTHORITY", "POSSESSION", "MILESTONE"}, Deposit: true},
		{Key: "PROPERTY_PURCHASE", Payers: []string{"BUYER"}, Components: []string{"RESERVATION", "PURCHASE_DEPOSIT", "INSTALLMENT", "SALE", "PROFESSIONAL", "REGISTRATION", "TAX", "PROCESSING"}, Roles: []string{"OWNER", "CO_OWNER", "CORPORATE_OWNER", "PROFESSIONAL", "PROFESSIONAL_FIRM", "SERVICE_COMPANY", "PLATFORM"}, Conditions: []string{"TITLE", "DUE_DILIGENCE", "CLOSING", "HANDOVER", "REGISTRATION"}, Deposit: true},
		{Key: "LAND_PURCHASE", Payers: []string{"BUYER"}, Components: []string{"RESERVATION", "SALE", "SURVEY", "TITLE", "CLOSING", "PROCESSING"}, Roles: []string{"OWNER", "CO_OWNER", "CORPORATE_OWNER", "PROFESSIONAL", "PROFESSIONAL_FIRM", "PLATFORM"}, Conditions: []string{"SURVEY", "TITLE", "ENCUMBRANCE", "POSSESSION", "CLOSING"}},
		{Key: "OFF_PLAN", Payers: []string{"BUYER"}, Components: []string{"RESERVATION", "CONSTRUCTION", "HANDOVER", "RESERVE", "PROFESSIONAL", "PROCESSING"}, Roles: []string{"DEVELOPER", "SERVICE_COMPANY", "PROFESSIONAL", "PROFESSIONAL_FIRM", "PLATFORM"}, Conditions: []string{"INDEPENDENT_CERTIFICATION", "PROJECT_ALLOCATION", "SNAGGING"}, Deposit: true},
		{Key: "SHORT_LET", Payers: []string{"GUEST"}, Components: []string{"ACCOMMODATION", "CLEANING", "BOOKING", "DEPOSIT", "ADD_ON", "PROCESSING"}, Roles: []string{"OWNER", "HOSPITALITY", "MANAGER", "MANAGEMENT_COMPANY", "ARTISAN", "SERVICE_COMPANY", "PLATFORM"}, Conditions: []string{"CHECK_IN", "STAY_COMPLETE", "CHECK_OUT"}, Deposit: true},
		{Key: "HOTEL", Payers: []string{"GUEST", "CORPORATE_BOOKER"}, Components: []string{"ROOM", "BOARD", "SERVICE", "EXTRA", "TAX", "DEPOSIT", "PENALTY", "PROCESSING"}, Roles: []string{"HOSPITALITY", "SERVICE_COMPANY", "PLATFORM"}, Conditions: []string{"RATE_PLAN", "FOLIO", "CHECK_IN", "CHECK_OUT", "EXTRAS", "NO_SHOW"}, Deposit: true},
		{Key: "EVENT_VENUE", Payers: []string{"EVENT_CUSTOMER"}, Components: []string{"RESERVATION", "EVENT", "DEPOSIT", "CLEANING_RESERVE", "PROCESSING"}, Roles: []string{"OWNER", "HOSPITALITY", "SERVICE_COMPANY", "PLATFORM"}, Conditions: []string{"EVENT_COMPLETE", "CLEANUP", "DAMAGE_ASSESSMENT"}, Deposit: true},
		{Key: "MAINTENANCE", Payers: []string{"TENANT", "OWNER", "BUSINESS"}, Components: []string{"CALL_OUT", "MATERIALS", "LABOUR", "MILESTONE", "EXTRA", "PROCESSING"}, Roles: []string{"ARTISAN", "SERVICE_COMPANY", "PLATFORM"}, Conditions: []string{"QUOTE", "ACCESS", "ATTENDANCE", "COMPLETION", "ACCEPTANCE", "WARRANTY"}},
		{Key: "PROFESSIONAL_SERVICE", Payers: []string{"BUYER", "OWNER", "BUSINESS"}, Components: []string{"RETAINER", "APPOINTMENT", "MILESTONE", "REPORT", "VISIT", "EXPENSE", "PROCESSING"}, Roles: []string{"PROFESSIONAL", "PROFESSIONAL_FIRM", "PLATFORM"}, Conditions: []string{"MANDATE", "CONFLICT_CHECK", "DELIVERABLE", "ACCEPTANCE"}},
		{Key: "MANAGER_FEE", Payers: []string{"OWNER", "BUSINESS"}, Components: []string{"MANAGER_FEE", "INSPECTION", "BOOKING", "SERVICE", "PROCESSING"}, Roles: []string{"MANAGER", "MANAGEMENT_COMPANY", "PLATFORM"}, Conditions: []string{"MANDATE", "FEE_AGREEMENT", "SERVICE_PERIOD"}},
		{Key: "UTILITIES", Payers: []string{"TENANT", "OWNER", "BUSINESS"}, Components: []string{"UTILITY", "SERVICE", "SHARED_METER", "BUDGET", "PROCESSING"}, Roles: []string{"SERVICE_COMPANY", "HOSPITALITY", "PLATFORM"}, Conditions: []string{"VERIFIED_BILL", "SERVICE", "ADJUSTMENT"}},
		{Key: "PROMOTION_SUBSCRIPTION", Payers: []string{"OWNER", "BUSINESS"}, Components: []string{"ADVERT", "SUBSCRIPTION", "PROCESSING"}, Roles: []string{"PLATFORM", "SERVICE_COMPANY"}, Conditions: []string{"PUBLISHED", "SERVICE_PERIOD", "REVENUE_EARNED"}},
		{Key: "REGULATED_FINANCE", Payers: []string{"CUSTOMER", "REGULATED_PARTNER"}, Components: []string{"PREMIUM", "LOAN_INSTALLMENT", "PARTNER_FEE", "PROCESSING"}, Roles: []string{"REGULATED_PARTNER"}, Conditions: []string{"PARTNER_CONTRACT", "PARTNER_APPROVAL"}, Regulated: true},
	}
	for i := range families {
		families[i].TemplateVersion = "blueprint-v1.0"
		families[i].JurisdictionRequired = true
		families[i].CancellationRef = "UNCONFIGURED"
		families[i].RefundRef = "UNCONFIGURED"
		families[i].DisputeRef = "UNCONFIGURED"
	}
	return families
}
func family(key string) (Family, error) {
	for _, f := range Families() {
		if f.Key == key {
			return f, nil
		}
	}
	return Family{}, fail("UNKNOWN_POLICY_FAMILY")
}

type Policy struct {
	AllowSelfCertification bool       `json:"allow_self_certification"`
	ID                     ids.ID     `json:"policy_id"`
	Key                    string     `json:"policy_key"`
	Version                string     `json:"version"`
	Jurisdiction           string     `json:"jurisdiction"`
	Currency               string     `json:"currency"`
	ContractType           string     `json:"contract_type"`
	Product                string     `json:"product"`
	EffectiveFrom          time.Time  `json:"effective_from"`
	EffectiveUntil         *time.Time `json:"effective_until,omitempty"`
	Active                 bool       `json:"active"`
	ApprovalRef            string     `json:"approval_ref"`
	CancellationRef        string     `json:"cancellation_policy_ref"`
	RefundRef              string     `json:"refund_policy_ref"`
	DisputeRef             string     `json:"dispute_policy_ref"`
	AdjustmentRef          string     `json:"adjustment_policy_ref"`
	Conditions             []string   `json:"release_conditions"`
	EvidenceServices       []string   `json:"evidence_services"`
	Schedules              []string   `json:"schedules"`
	RequiresCustody        bool       `json:"requires_custody"`
	HumanReview            bool       `json:"human_review"`
	PartnerApprovalRef     string     `json:"partner_approval_ref"`
	CommissionBPS          int        `json:"owner_commission_bps"`
	CommissionContractRef  string     `json:"commission_contract_ref"`
	WithdrawalLimitMinor   int64      `json:"withdrawal_limit_minor"`
	DualApprovalAboveMinor int64      `json:"dual_approval_above_minor"`
}

func (s *Service) PutPolicy(ctx context.Context, p Policy) (Policy, error) {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return p, e
	}
	f, e := family(p.Key)
	if e != nil {
		return p, e
	}
	if p.ID.ValidateV7() != nil || p.Version == "" || p.Jurisdiction == "" || !core.ValidCurrency(p.Currency) || p.ContractType == "" || p.Product == "" || p.EffectiveFrom.IsZero() || p.CommissionBPS < 0 || p.CommissionBPS > 10000 || p.WithdrawalLimitMinor < 0 || p.DualApprovalAboveMinor < 0 {
		return p, fail("INVALID_POLICY_VERSION")
	}
	if p.EffectiveUntil != nil && !p.EffectiveUntil.After(p.EffectiveFrom) {
		return p, fail("INVALID_POLICY_WINDOW")
	}
	if p.Active && (p.ApprovalRef == "" || p.CancellationRef == "" || p.RefundRef == "" || p.DisputeRef == "" || p.AdjustmentRef == "" || len(p.Conditions) == 0 || len(p.EvidenceServices) == 0 || len(p.Schedules) == 0 || (f.Regulated && p.PartnerApprovalRef == "")) {
		return p, fail("APPROVED_POLICY_PACK_REQUIRED")
	}
	if p.CommissionBPS > 0 && p.CommissionContractRef == "" {
		return p, fail("COMMISSION_CONTRACT_REQUIRED")
	}
	for _, c := range p.Conditions {
		if !has(f.Conditions, c) {
			return p, fail("CATEGORY_CONDITION_MISMATCH")
		}
		for _, service := range p.EvidenceServices {
			if !platform.KnownService(service) || has([]string{string(platform.PaymentService), string(platform.APIGateway), string(platform.NotificationService), string(platform.MediaDocumentService)}, service) {
				return p, fail("BUSINESS_EVIDENCE_SERVICE_REQUIRED")
			}
			if has(p.Conditions, "INDEPENDENT_CERTIFICATION") && service == string(platform.BusinessOperationsService) && !p.AllowSelfCertification {
				return p, fail("INDEPENDENT_CERTIFIER_REQUIRED")
			}
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "policy:"+p.Key+":"+p.Jurisdiction+":"+p.ContractType+":"+p.Product); e != nil {
		return p, e
	}
	var prev Policy
	e = load(ctx, tx, `SELECT snapshot FROM financial_policies WHERE policy_id=$1`, p.ID, &prev)
	if e == nil {
		if core.Fingerprint(prev) != core.Fingerprint(p) {
			return p, core.Fail(409, "POLICY_VERSION_IMMUTABLE")
		}
		return prev, nil
	}
	if p.Active {
		var overlap bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM financial_policies WHERE active AND policy_key=$1 AND jurisdiction=$2 AND currency=$3 AND contract_type=$4 AND product=$5 AND effective_from<COALESCE($7,'infinity'::timestamptz) AND LEAST(COALESCE(effective_until,'infinity'::timestamptz),COALESCE((SELECT min(d.effective_at) FROM policy_deactivations d WHERE d.policy_id=financial_policies.policy_id),'infinity'::timestamptz))>$6)`, p.Key, p.Jurisdiction, p.Currency, p.ContractType, p.Product, p.EffectiveFrom, p.EffectiveUntil).Scan(&overlap)
		if e != nil {
			return p, e
		}
		if overlap {
			return p, core.Fail(409, "POLICY_EFFECTIVE_WINDOW_OVERLAP")
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO financial_policies(policy_id,policy_key,version,jurisdiction,currency,contract_type,product,effective_from,effective_until,active,snapshot,fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, p.ID, p.Key, p.Version, p.Jurisdiction, p.Currency, p.ContractType, p.Product, p.EffectiveFrom, p.EffectiveUntil, p.Active, raw(p), core.Fingerprint(p))
	if e != nil {
		return p, e
	}
	if e = s.Audit(ctx, tx, "policy.version_recorded", p.ID.String(), p); e != nil {
		return p, e
	}
	return p, tx.Commit(ctx)
}
func (s *Service) Policy(ctx context.Context, id ids.ID) (Policy, error) {
	if _, e := core.Caller(ctx); e != nil {
		return Policy{}, e
	}
	var p Policy
	e := load(ctx, s.DB, `SELECT snapshot FROM financial_policies WHERE policy_id=$1`, id, &p)
	return p, e
}
func policyAt(p Policy, t time.Time) error {
	if !p.Active || t.Before(p.EffectiveFrom) || (p.EffectiveUntil != nil && !t.Before(*p.EffectiveUntil)) {
		return core.Fail(403, "POLICY_DISABLED_OR_NOT_EFFECTIVE")
	}
	return nil
}
