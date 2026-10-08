package platform

import "testing"

func TestTwelveComponentsSevenDomains(t *testing.T) {
	if len(Services()) != 12 {
		t.Fatalf("expected 12 deployable components, got %d", len(Services()))
	}
	if len(Domains()) != 7 {
		t.Fatalf("expected 7 user domains, got %d", len(Domains()))
	}
	for _, d := range Domains() {
		s, ok := ServiceForDomain(d)
		if !ok || !KnownService(string(s)) {
			t.Fatalf("domain %s has no service", d)
		}
		back, ok := DomainForService(s)
		if !ok || back != d {
			t.Fatalf("round trip failed for %s", d)
		}
	}
	for _, shared := range []Service{VerificationService, PaymentService, NotificationService, MediaDocumentService, APIGateway} {
		if _, ok := DomainForService(shared); ok {
			t.Fatalf("%s must not be a user-domain authority", shared)
		}
	}
	if KnownService("identity-service") {
		t.Fatal("there is no central identity service")
	}
	if KnownDomain("identity") || !KnownDomain("artisan") {
		t.Fatal("domain lookup wrong")
	}
}
