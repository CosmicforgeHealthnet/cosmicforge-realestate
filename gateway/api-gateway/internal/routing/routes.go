// Package routing holds the gateway's edge route registry. It maps public
// path prefixes to backend services, authentication requirements and rate
// limit categories. It contains no business logic.
package routing

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// AuthMode controls user-token handling on a route.
type AuthMode int

const (
	// AuthRequired: a valid user access token from a trusted issuer is mandatory.
	AuthRequired AuthMode = iota
	// AuthOptional: anonymous access is allowed; a presented token must be valid.
	AuthOptional
	// AuthNone: user tokens are not evaluated (provider webhooks).
	AuthNone
)

// Category names a rate-limit policy.
type Category string

const (
	CategoryPublic        Category = "public"
	CategoryAuthenticated Category = "authenticated"
	CategoryAuthSensitive Category = "auth-sensitive"
	CategoryUpload        Category = "upload-initiation"
	CategoryWebhook       Category = "webhook"
)

// Rule overrides the category for a method + sub-path within a route.
type Rule struct {
	Method   string // "" = any
	Prefix   string // full path prefix
	Category Category
}

// Route is one edge mapping.
type Route struct {
	Name    string
	Prefix  string // must end with "/"
	Service platform.Service
	Auth    AuthMode
	// Category is the default rate-limit category; authenticated requests on
	// public routes use CategoryAuthenticated.
	Category Category
	Rules    []Rule
	// ActorDomains restricts which user domains' tokens are accepted (nil =
	// any trusted domain). Domain-owned routes accept only their own domain
	// (cross-domain token misuse is rejected at the edge; the domain service
	// still authorizes).
	ActorDomains []platform.Domain
	// ForwardUserToken forwards the end user's Authorization header. Domain
	// services re-verify their own tokens; shared platform services rely on
	// the gateway's signed delegation and never receive user tokens.
	ForwardUserToken bool
	// MaxBodyBytes overrides the global request body limit.
	MaxBodyBytes int64
	Upstream     *url.URL
}

// CategoryFor returns the rate-limit category for a request.
func (r Route) CategoryFor(method, path string, authenticated bool) Category {
	for _, rule := range r.Rules {
		if (rule.Method == "" || rule.Method == method) && strings.HasPrefix(path, rule.Prefix) {
			return rule.Category
		}
	}
	if r.Category == CategoryPublic && authenticated {
		return CategoryAuthenticated
	}
	return r.Category
}

// AllowsDomain reports whether tokens of d are accepted on this route.
func (r Route) AllowsDomain(d platform.Domain) bool {
	if len(r.ActorDomains) == 0 {
		return true
	}
	for _, x := range r.ActorDomains {
		if x == d {
			return true
		}
	}
	return false
}

// domainPrefixes are the reserved public prefixes of the seven user-domain
// services. They are mounted only when an upstream URL is configured, so the
// gateway never exposes functionality that does not exist yet.
var domainPrefixes = map[platform.Domain]string{
	platform.DomainMarketplace:          "/api/v1/marketplace/",
	platform.DomainIndividualOwner:      "/api/v1/owner/", // ADR-0011
	platform.DomainBusinessOperations:   "/api/v1/business-operations/",
	platform.DomainPropertyManager:      "/api/v1/property-manager/",
	platform.DomainArtisan:              "/api/v1/artisan/",
	platform.DomainProfessionalServices: "/api/v1/professional-services/",
	platform.DomainAdmin:                "/api/v1/admin/",
}

// DomainPrefix returns the reserved prefix of a domain.
func DomainPrefix(d platform.Domain) string { return domainPrefixes[d] }

// Build assembles the registry from configured upstream URLs.
//
// upstreams maps service -> base URL; webhooks maps provider name -> service.
func Build(upstreams map[platform.Service]*url.URL, webhooks map[string]platform.Service) ([]Route, error) {
	var routes []Route
	add := func(r Route) error {
		u, ok := upstreams[r.Service]
		if !ok || u == nil {
			return nil // not deployed yet: not routed
		}
		r.Upstream = u
		routes = append(routes, r)
		return nil
	}

	// Shared platform services (F3–F5).
	_ = add(Route{Name: "payments", Prefix: "/api/v1/payments/", Service: platform.PaymentService,
		Auth: AuthRequired, Category: CategoryAuthenticated})
	_ = add(Route{Name: "verification", Prefix: "/api/v1/verification/", Service: platform.VerificationService,
		Auth: AuthRequired, Category: CategoryAuthenticated})
	_ = add(Route{Name: "media", Prefix: "/api/v1/media/", Service: platform.MediaDocumentService,
		Auth: AuthRequired, Category: CategoryAuthenticated,
		Rules: []Rule{{Method: "POST", Prefix: "/api/v1/media/uploads", Category: CategoryUpload}}})
	_ = add(Route{Name: "notifications", Prefix: "/api/v1/notifications/", Service: platform.NotificationService,
		Auth: AuthRequired, Category: CategoryAuthenticated})

	// User-domain services: reserved, mounted only once deployed.
	for d, prefix := range domainPrefixes {
		svc, _ := platform.ServiceForDomain(d)
		rules := []Rule{{Prefix: prefix + "auth/", Category: CategoryAuthSensitive}}
		if d == platform.DomainBusinessOperations {
			rules = append(rules, Rule{Prefix: prefix + "staff/invitations/accept", Category: CategoryAuthSensitive})
		}
		_ = add(Route{Name: string(d), Prefix: prefix, Service: svc, Auth: AuthOptional, Category: CategoryPublic,
			ActorDomains: []platform.Domain{d}, ForwardUserToken: true,
			Rules: rules})
	}

	// Section 3 canonical business contract. The previously reserved prefix
	// remains available with identical authentication and rate-limit policy.
	_ = add(Route{Name: "business", Prefix: "/api/v1/business/", Service: platform.BusinessOperationsService,
		Auth: AuthOptional, Category: CategoryPublic,
		ActorDomains: []platform.Domain{platform.DomainBusinessOperations}, ForwardUserToken: true,
		Rules: []Rule{{Prefix: "/api/v1/business/auth/", Category: CategoryAuthSensitive}, {Prefix: "/api/v1/business/staff/invitations/accept", Category: CategoryAuthSensitive}}})

	// Provider webhooks: /webhooks/v1/<provider>/...
	providers := make([]string, 0, len(webhooks))
	for p := range webhooks {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, p := range providers {
		svc := webhooks[p]
		if !validProvider(p) {
			return nil, fmt.Errorf("routing: invalid webhook provider name %q", p)
		}
		if svc == platform.PaymentService || svc == platform.VerificationService || svc == platform.NotificationService || svc == platform.MediaDocumentService {
			_ = add(Route{Name: "webhook-" + p, Prefix: "/webhooks/v1/" + p + "/", Service: svc, Auth: AuthNone,
				Category: CategoryWebhook, MaxBodyBytes: 256 << 10})
			continue
		}
		return nil, fmt.Errorf("routing: webhook provider %q mapped to unsupported service %q", p, svc)
	}

	// Longest prefix first.
	sort.Slice(routes, func(i, j int) bool { return len(routes[i].Prefix) > len(routes[j].Prefix) })
	return routes, nil
}

func validProvider(p string) bool {
	if p == "" || len(p) > 40 {
		return false
	}
	for _, c := range p {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// Match returns the route for path (longest prefix).
func Match(routes []Route, path string) (Route, bool) {
	for _, r := range routes {
		if strings.HasPrefix(path, r.Prefix) || path+"/" == r.Prefix {
			return r, true
		}
	}
	return Route{}, false
}
