package routing

import (
	"net/url"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func u(s string) *url.URL { x, _ := url.Parse(s); return x }

func TestBuildAndMatch(t *testing.T) {
	routes, err := Build(map[platform.Service]*url.URL{
		platform.VerificationService: u("http://v:8080"), platform.MediaDocumentService: u("http://m:8080"),
		platform.ArtisanService: u("http://a:8080"),
	}, map[string]platform.Service{"sandbox-kyc": platform.VerificationService})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := Match(routes, "/api/v1/media/uploads"); !ok || r.Service != platform.MediaDocumentService {
		t.Fatal("media")
	}
	if _, ok := Match(routes, "/api/v1/notifications/x"); ok {
		t.Fatal("unconfigured service must not be routed")
	}
	r, _ := Match(routes, "/api/v1/media/uploads")
	if r.CategoryFor("POST", "/api/v1/media/uploads", true) != CategoryUpload || r.CategoryFor("GET", "/api/v1/media/x", true) != CategoryAuthenticated {
		t.Fatal("upload category")
	}
	a, _ := Match(routes, "/api/v1/artisan/auth/login")
	if a.CategoryFor("POST", "/api/v1/artisan/auth/login", false) != CategoryAuthSensitive || a.Auth != AuthOptional ||
		!a.AllowsDomain(platform.DomainArtisan) || a.AllowsDomain(platform.DomainAdmin) || !a.ForwardUserToken {
		t.Fatal("domain route policy")
	}
	if a.CategoryFor("GET", "/api/v1/artisan/jobs", true) != CategoryAuthenticated || a.CategoryFor("GET", "/api/v1/artisan/jobs", false) != CategoryPublic {
		t.Fatal("public/auth category")
	}
	w, ok := Match(routes, "/webhooks/v1/sandbox-kyc/callbacks")
	if !ok || w.Auth != AuthNone || w.Category != CategoryWebhook {
		t.Fatal("webhook")
	}
	for _, r := range routes {
		if r.Prefix[len(r.Prefix)-1] != '/' {
			t.Fatalf("prefix %s must end with /", r.Prefix)
		}
	}
}

func TestWebhookValidation(t *testing.T) {
	ups := map[platform.Service]*url.URL{platform.ArtisanService: u("http://a")}
	if _, err := Build(ups, map[string]platform.Service{"Bad Name": platform.VerificationService}); err == nil {
		t.Fatal("invalid provider accepted")
	}
	if _, err := Build(ups, map[string]platform.Service{"x": platform.ArtisanService}); err == nil {
		t.Fatal("webhook to domain service accepted")
	}
}

func TestBusinessSupplyWebhookExplicitRegistrationAndInternalIsolation(t *testing.T) {
	ups := map[platform.Service]*url.URL{platform.BusinessOperationsService: u("http://business:8080")}
	routes, err := Build(ups, map[string]platform.Service{"business-channels": platform.BusinessOperationsService})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := Match(routes, "/webhooks/v1/business-channels/COSMICFORGE_MARKETPLACE/connection")
	if !ok || r.Auth != AuthNone || r.ForwardUserToken || r.Service != platform.BusinessOperationsService || r.Category != CategoryWebhook || r.MaxBodyBytes != 64<<10 {
		t.Fatal("Business provider callback policy")
	}
	if _, ok = Match(routes, "/internal/v1/business-supply/channel-receipts"); ok {
		t.Fatal("Internal supply contract exposed through gateway")
	}
	if _, err = Build(ups, map[string]platform.Service{"arbitrary": platform.BusinessOperationsService}); err == nil {
		t.Fatal("Arbitrary business webhook prefix accepted")
	}
	routes, err = Build(ups, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok = Match(routes, "/webhooks/v1/business-channels/x/y"); ok {
		t.Fatal("Unconfigured callback should not route")
	}
}
