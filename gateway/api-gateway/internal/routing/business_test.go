package routing

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"net/url"
	"testing"
)

func TestBusinessCanonicalAndCompatibilityPolicies(t *testing.T) {
	routes, err := Build(map[platform.Service]*url.URL{platform.BusinessOperationsService: u("http://business:8080")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"/api/v1/business/", "/api/v1/business-operations/"} {
		r, ok := Match(routes, prefix+"auth/login")
		if !ok || r.Service != platform.BusinessOperationsService || r.Auth != AuthOptional || !r.ForwardUserToken || !r.AllowsDomain(platform.DomainBusinessOperations) || r.AllowsDomain(platform.DomainIndividualOwner) || r.CategoryFor("POST", prefix+"auth/login", false) != CategoryAuthSensitive {
			t.Fatalf("unsafe business route: %+v", r)
		}
	}
	for _, path := range []string{"/internal/v1/business/organisation", "/api/v1/business-other/", "/api/v1/businesses/"} {
		if _, ok := Match(routes, path); ok {
			t.Fatalf("unexpected route %s", path)
		}
	}
	routes, err = Build(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Match(routes, "/api/v1/business/organisation"); ok {
		t.Fatal("unconfigured upstream exposed")
	}
}
