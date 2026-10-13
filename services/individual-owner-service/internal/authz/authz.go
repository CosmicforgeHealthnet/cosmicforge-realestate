// Package authz is individual-owner-service's policy-enforcement point
// (Section 5 C). One engine evaluates every protected command — from HTTP
// handlers, workers and internal service routes — against declarative rules:
//
//   - who the subject is (owner session, gateway-delegated actor, service
//     principal acting with or without a delegated actor);
//   - the acting account's lifecycle state and step-up/MFA freshness;
//   - for property-scoped actions, the subject's relationship to the exact
//     property (owner, co-owner, authorized representative, witness/
//     signatory, delegated manager), the capability that relationship holds,
//     its validity window and revocation, co-owner consent obligations,
//     property authority verification and admin restrictions.
//
// Authentication alone never authorizes a business action; unknown actions
// and missing facts fail closed.
package authz

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Action names a protected operation, e.g. "account.mfa.disable".
type Action string

// Capability is a property-scoped permission a delegated role may hold.
type Capability string

// Role is a subject's relationship to a property (Section 5 principals).
type Role string

const (
	RoleOwner          Role = "OWNER"
	RoleCoOwner        Role = "CO_OWNER"
	RoleRepresentative Role = "AUTHORIZED_REPRESENTATIVE"
	RoleSignatory      Role = "WITNESS_SIGNATORY"
	RoleManager        Role = "DELEGATED_MANAGER"
)

// SubjectKind classifies the principal.
type SubjectKind string

const (
	// KindOwnerSession: an individual-owner account authenticated with its own
	// access token and live session (reached through the gateway).
	KindOwnerSession SubjectKind = "OWNER_SESSION"
	// KindGatewayActor: a user actor asserted by the gateway's service token
	// without an owner session (never sufficient for owner-account actions).
	KindGatewayActor SubjectKind = "GATEWAY_DELEGATED_ACTOR"
	// KindService: a service principal on /internal/v1, optionally carrying a
	// delegated actor from its own domain.
	KindService SubjectKind = "SERVICE_PRINCIPAL"
	// KindSystem: this service's own workers.
	KindSystem SubjectKind = "SYSTEM"
)

// AccountContext is the verified state of the acting owner account/session.
type AccountContext struct {
	AccountID    ids.ID
	SessionID    ids.ID
	Status       domain.AccountStatus
	StepUpAt     *time.Time
	StepUpMethod string
	AuthMethod   string
	MFAEnabled   bool
}

// Subject is the evaluated principal.
type Subject struct {
	Kind        SubjectKind
	Service     platform.Service // calling service (KindService)
	ActorDomain platform.Domain  // delegated/asserted actor domain
	ActorID     ids.ID
	Account     *AccountContext // KindOwnerSession only
}

// Resource identifies the object of an action.
type Resource struct {
	AccountID  ids.ID // target owner account (self-service and admin actions)
	PropertyID ids.ID // property-scoped actions
}

// Relationship is one authority link between a subject and a property, as
// resolved from owner-domain records (ownership, consent, representative
// mandates, manager assignments).
type Relationship struct {
	Role         Role
	ReferenceID  ids.ID       // ownership/mandate/assignment record
	Capabilities []Capability // explicit grants for delegated roles
	ValidFrom    *time.Time
	ValidUntil   *time.Time
	RevokedAt    *time.Time
}

// PropertyFacts are the authorization-relevant facts of a property for one subject.
type PropertyFacts struct {
	Exists            bool
	AuthorityVerified bool // property authority verification current (Verification Service read model)
	AdminRestricted   bool
	CoOwnerCount      int // co-owners other than the subject
	Relationships     []Relationship
}

// AuthorityResolver loads property facts for a subject. Implementations read
// only individual_owner_db (or explicit contracts); never another service's DB.
type AuthorityResolver interface {
	PropertyFacts(ctx context.Context, s Subject, propertyID ids.ID) (PropertyFacts, error)
}

// ErrNoPropertyStore is returned by the default resolver.
var ErrNoPropertyStore = errors.New("authz: property authority store not configured")

// NoPropertyAuthority is a fail-closed resolver (tests and misconfiguration);
// production uses the individual_owner_db-backed resolver in usecases.
type NoPropertyAuthority struct{}

// PropertyFacts implements AuthorityResolver.
func (NoPropertyAuthority) PropertyFacts(context.Context, Subject, ids.ID) (PropertyFacts, error) {
	return PropertyFacts{}, ErrNoPropertyStore
}

// ServiceGrant allows a service principal to perform an action, optionally
// only when it delegates an actor from one of ActorDomains.
type ServiceGrant struct {
	Service       platform.Service
	ActorDomains  []platform.Domain // nil = no delegated actor required
	RequiresActor bool
}

// Rule is the declarative policy of one action.
type Rule struct {
	Action Action
	// SelfService: an owner session may act on its own account.
	SelfService bool
	// AccountStatuses restricts the acting owner account's state.
	AccountStatuses []domain.AccountStatus
	StepUp          bool
	RequireMFA      bool
	// Property-scoped evaluation.
	PropertyScoped           bool
	Roles                    []Role
	Capability               Capability
	CoOwnerConsent           bool
	RequireVerifiedAuthority bool
	// AllowRestricted permits the action on an admin-restricted property
	// (reads and remediation); every other property action is blocked.
	AllowRestricted bool
	// Service principals allowed on internal routes.
	Services []ServiceGrant
	// System marks actions this service's workers may perform.
	System bool
}

// Obligation is a condition the caller must satisfy before executing.
type Obligation string

const ObligationCoOwnerConsent Obligation = "CO_OWNER_CONSENT_REQUIRED"

// Decision is the evaluation result.
type Decision struct {
	Allowed     bool
	Code        string
	Message     string
	Status      int
	Obligations []Obligation
	Via         *Relationship
}

// Err converts a denial into the platform error.
func (d Decision) Err() error {
	if d.Allowed {
		return nil
	}
	return apperrors.New(d.Status, d.Code, d.Message)
}

func deny(status int, code, msg string) Decision {
	return Decision{Status: status, Code: code, Message: msg}
}

// Engine evaluates rules.
type Engine struct {
	rules    map[Action]Rule
	resolver AuthorityResolver
	stepUp   time.Duration
	now      func() time.Time
	denials  observability.Counter
}

// Config configures the engine.
type Config struct {
	Rules        []Rule
	Resolver     AuthorityResolver
	StepUpWindow time.Duration
	Now          func() time.Time
}

// New builds an engine. Duplicate or empty rules panic (programmer error).
func New(cfg Config) *Engine {
	e := &Engine{rules: map[Action]Rule{}, resolver: cfg.Resolver, stepUp: cfg.StepUpWindow, now: cfg.Now,
		denials: observability.NewCounter("individual-owner-service", "owner_authz_denials_total", "Authorization denials by action and code")}
	if e.resolver == nil {
		e.resolver = NoPropertyAuthority{}
	}
	if e.stepUp <= 0 {
		e.stepUp = 5 * time.Minute
	}
	if e.now == nil {
		e.now = time.Now
	}
	for _, r := range cfg.Rules {
		if r.Action == "" {
			panic("authz: rule without action")
		}
		if _, dup := e.rules[r.Action]; dup {
			panic("authz: duplicate rule " + string(r.Action))
		}
		if r.PropertyScoped && len(r.Roles) == 0 {
			panic("authz: property rule without roles " + string(r.Action))
		}
		e.rules[r.Action] = r
	}
	return e
}

// StepUpWindow returns the configured freshness window.
func (e *Engine) StepUpWindow() time.Duration { return e.stepUp }

// Rule returns the rule for a (for documentation and tests).
func (e *Engine) Rule(a Action) (Rule, bool) { r, ok := e.rules[a]; return r, ok }

// Require evaluates and returns the denial error, if any.
func (e *Engine) Require(ctx context.Context, s Subject, a Action, res Resource) (Decision, error) {
	d := e.Authorize(ctx, s, a, res)
	return d, d.Err()
}

// Authorize evaluates a against the rules. It never returns Allowed for an
// unknown action.
func (e *Engine) Authorize(ctx context.Context, s Subject, a Action, res Resource) Decision {
	d := e.evaluate(ctx, s, a, res)
	if !d.Allowed {
		e.denials.Add(ctx, 1, attribute.String("action", string(a)), attribute.String("code", d.Code))
		logging.FromContext(ctx).InfoContext(ctx, "authorization denied", "action", string(a), "code", d.Code,
			"subject_kind", string(s.Kind), "service", string(s.Service))
	}
	return d
}

func (e *Engine) evaluate(ctx context.Context, s Subject, a Action, res Resource) Decision {
	r, ok := e.rules[a]
	if !ok {
		return deny(http.StatusForbidden, apperrors.CodePermissionDenied, "This operation is not permitted.")
	}
	switch s.Kind {
	case KindSystem:
		if r.System {
			return Decision{Allowed: true}
		}
		return deny(http.StatusForbidden, apperrors.CodePermissionDenied, "System actor is not permitted to perform this operation.")
	case KindService:
		return e.evaluateService(ctx, r, s, res)
	case KindOwnerSession:
		return e.evaluateOwner(ctx, r, s, res)
	default:
		// A gateway-asserted actor without a verified owner session never
		// carries owner-domain authority.
		return deny(http.StatusUnauthorized, apperrors.CodeAuthenticationRequired, "Authentication is required.")
	}
}

func (e *Engine) evaluateService(ctx context.Context, r Rule, s Subject, res Resource) Decision {
	if r.RequireMFA {
		return deny(http.StatusForbidden, "OWNER_SESSION_REQUIRED", "A current owner session with recent MFA is required.")
	}
	var grant *ServiceGrant
	for i := range r.Services {
		if r.Services[i].Service == s.Service {
			grant = &r.Services[i]
		}
	}
	if grant == nil {
		return deny(http.StatusForbidden, apperrors.CodePermissionDenied, "The calling service is not authorized for this operation.")
	}
	hasActor := s.ActorDomain != "" && !s.ActorID.IsZero()
	if grant.RequiresActor && !hasActor {
		return deny(http.StatusForbidden, "DELEGATED_ACTOR_REQUIRED", "This operation must be attributable to a delegated actor.")
	}
	if hasActor && len(grant.ActorDomains) > 0 && !slices.Contains(grant.ActorDomains, s.ActorDomain) {
		return deny(http.StatusForbidden, apperrors.CodePermissionDenied, "The delegated actor's domain is not authorized for this operation.")
	}
	if r.PropertyScoped {
		if !hasActor {
			return deny(http.StatusForbidden, "DELEGATED_ACTOR_REQUIRED", "Property operations must be attributable to a delegated actor.")
		}
		return e.evaluateProperty(ctx, r, s, res)
	}
	return Decision{Allowed: true}
}

func (e *Engine) evaluateOwner(ctx context.Context, r Rule, s Subject, res Resource) Decision {
	acc := s.Account
	if acc == nil || acc.AccountID.IsZero() || acc.SessionID.IsZero() {
		return deny(http.StatusUnauthorized, apperrors.CodeAuthenticationRequired, "Authentication is required.")
	}
	if !r.SelfService && !r.PropertyScoped {
		return deny(http.StatusForbidden, apperrors.CodePermissionDenied, "This operation is not available to owner accounts.")
	}
	if !acc.Status.CanAuthenticate() {
		return deny(http.StatusForbidden, "ACCOUNT_UNAVAILABLE", "This account cannot perform operations.")
	}
	if len(r.AccountStatuses) > 0 && !slices.Contains(r.AccountStatuses, acc.Status) {
		code := "ACCOUNT_NOT_ACTIVE"
		if acc.Status == domain.StatusRestricted {
			code = "ACCOUNT_RESTRICTED"
		}
		return deny(http.StatusForbidden, code, "The account's current status does not permit this operation.")
	}
	if r.SelfService && !res.AccountID.IsZero() && res.AccountID != acc.AccountID {
		// Never reveal whether another account exists (IDOR).
		return deny(http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Account not found.")
	}
	if r.RequireMFA && (!acc.MFAEnabled || !slices.Contains([]string{"TOTP", "SMS", "EMAIL", "RECOVERY_CODE"}, acc.StepUpMethod) || (acc.AuthMethod == "PHONE_OTP" && acc.StepUpMethod == "SMS")) {
		return deny(http.StatusForbidden, "MFA_REQUIRED", "This operation requires multi-factor authentication to be enabled.")
	}
	if r.StepUp && (acc.StepUpAt == nil || e.now().Sub(*acc.StepUpAt) > e.stepUp || acc.StepUpAt.After(e.now().Add(time.Minute))) {
		return deny(http.StatusForbidden, "STEP_UP_REQUIRED", "Recent re-authentication is required for this operation.")
	}
	if r.PropertyScoped {
		return e.evaluateProperty(ctx, r, s, res)
	}
	return Decision{Allowed: true}
}

func (e *Engine) evaluateProperty(ctx context.Context, r Rule, s Subject, res Resource) Decision {
	if res.PropertyID.IsZero() {
		return deny(http.StatusBadRequest, apperrors.CodeInvalidRequest, "A property is required for this operation.")
	}
	facts, err := e.resolver.PropertyFacts(ctx, s, res.PropertyID)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "property authority unavailable", "error", err.Error())
		return deny(http.StatusServiceUnavailable, apperrors.CodeServiceUnavailable, "Property authority could not be evaluated.")
	}
	if !facts.Exists {
		return deny(http.StatusNotFound, "PROPERTY_NOT_FOUND", "Property not found.")
	}
	now := e.now()
	var via *Relationship
	for i := range facts.Relationships {
		rel := facts.Relationships[i]
		if !slices.Contains(r.Roles, rel.Role) || !active(rel, now) {
			continue
		}
		switch rel.Role {
		case RoleOwner, RoleCoOwner:
			// Primary authority: role-level grant.
		default:
			// Delegated roles hold only explicitly granted capabilities.
			if r.Capability == "" || !slices.Contains(rel.Capabilities, r.Capability) {
				continue
			}
		}
		if via == nil || rank(rel.Role) < rank(via.Role) {
			via = &rel
		}
	}
	if via == nil {
		// Related-but-unauthorized and unrelated subjects get the same answer
		// when they have no live link at all, preventing enumeration.
		if len(facts.Relationships) == 0 {
			return deny(http.StatusNotFound, "PROPERTY_NOT_FOUND", "Property not found.")
		}
		return deny(http.StatusForbidden, "PROPERTY_AUTHORITY_REQUIRED", "You do not hold the authority required for this property operation.")
	}
	if facts.AdminRestricted && !r.AllowRestricted {
		return deny(http.StatusForbidden, "PROPERTY_RESTRICTED", "This property is restricted by platform review.")
	}
	if r.RequireVerifiedAuthority && !facts.AuthorityVerified {
		return deny(http.StatusUnprocessableEntity, "PROPERTY_AUTHORITY_UNVERIFIED", "Property authority must be verified first.")
	}
	d := Decision{Allowed: true, Via: via}
	if r.CoOwnerConsent && facts.CoOwnerCount > 0 && (via.Role == RoleOwner || via.Role == RoleCoOwner) {
		d.Obligations = append(d.Obligations, ObligationCoOwnerConsent)
	}
	return d
}

func active(rel Relationship, now time.Time) bool {
	if rel.RevokedAt != nil && !rel.RevokedAt.After(now) {
		return false
	}
	if rel.ValidFrom != nil && now.Before(*rel.ValidFrom) {
		return false
	}
	// An expired delegation loses authority immediately (no grace period).
	if rel.ValidUntil != nil && !now.Before(*rel.ValidUntil) {
		return false
	}
	return true
}

func rank(r Role) int {
	switch r {
	case RoleOwner:
		return 0
	case RoleCoOwner:
		return 1
	case RoleRepresentative:
		return 2
	case RoleManager:
		return 3
	}
	return 4
}
