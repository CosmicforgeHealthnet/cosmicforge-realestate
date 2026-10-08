package usecases

import (
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func TestBackoffIsExponentialBoundedAndHonoursRetryAfter(t *testing.T) {
	s := &Service{opts: Options{RetryBase: 30 * time.Second, RetryMax: 4 * time.Minute, Jitter: func(time.Duration) time.Duration { return 0 }}}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 4 * time.Minute}
	for i, w := range want {
		if got := s.backoff(int32(i+1), 0); got != w {
			t.Fatalf("attempt %d: %v want %v", i+1, got, w)
		}
	}
	if got := s.backoff(1, 90*time.Second); got != 90*time.Second {
		t.Fatalf("retry-after: %v", got)
	}
	if got := s.backoff(1, 10*time.Hour); got != 4*time.Minute {
		t.Fatalf("retry-after must be capped: %v", got)
	}
	o := Options{}
	o.defaults()
	for range 100 {
		if j := o.Jitter(time.Minute); j < 0 || j > 12*time.Second {
			t.Fatalf("jitter %v", j)
		}
	}
}

func TestAuthorizeRecipient(t *testing.T) {
	r := Recipient{Domain: platform.DomainArtisan, ID: ids.New()}
	ok := []Principal{{Service: platform.ArtisanService}, {Service: platform.AdminService}, {Service: platform.PaymentService},
		{Service: platform.VerificationService}, {Service: platform.APIGateway, ActorDomain: "artisan", ActorID: r.ID}}
	for _, p := range ok {
		if err := AuthorizeRecipient(p, r); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	bad := []Principal{{Service: platform.MarketplaceService}, {Service: platform.MediaDocumentService},
		{Service: platform.APIGateway, ActorDomain: "artisan", ActorID: ids.New()}, {Service: platform.APIGateway}}
	for _, p := range bad {
		if err := AuthorizeRecipient(p, r); err == nil {
			t.Fatalf("%+v must be denied", p)
		}
	}
	if err := AuthorizeRecipient(Principal{Service: platform.AdminService}, Recipient{Domain: "nowhere", ID: ids.New()}); err == nil {
		t.Fatal("unknown domain")
	}
}
