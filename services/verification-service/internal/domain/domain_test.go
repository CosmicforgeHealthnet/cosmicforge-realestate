package domain

import (
	"testing"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func TestStateMachineIsExactlyTheApprovedModel(t *testing.T) {
	all := []Status{StatusDraft, StatusSubmitted, StatusProcessing, StatusManualReview, StatusVerified, StatusRejected, StatusExpired}
	allowed := map[[2]Status]bool{
		{StatusDraft, StatusSubmitted}:         true,
		{StatusSubmitted, StatusProcessing}:    true,
		{StatusProcessing, StatusVerified}:     true,
		{StatusProcessing, StatusRejected}:     true,
		{StatusProcessing, StatusManualReview}: true,
		{StatusManualReview, StatusVerified}:   true,
		{StatusManualReview, StatusRejected}:   true,
		{StatusVerified, StatusExpired}:        true,
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[[2]Status{from, to}]
			if Machine.Can(from, to) != want {
				t.Errorf("%s -> %s: got %v want %v", from, to, Machine.Can(from, to), want)
			}
			err := Machine.Transition(from, to)
			if !want && apperrors.CodeOf(err) != apperrors.CodeInvalidStateTransition {
				t.Errorf("%s -> %s should be INVALID_STATE_TRANSITION", from, to)
			}
		}
	}
	for _, s := range []Status{StatusRejected, StatusExpired} {
		if !Machine.Terminal(s) {
			t.Errorf("%s must be terminal", s)
		}
	}
}

func TestCallerAllowList(t *testing.T) {
	sub := func(st SubjectType, d platform.Domain) Subject { return Subject{Type: st, Domain: d, ID: ids.New()} }
	cases := []struct {
		caller platform.Service
		typ    Type
		sub    Subject
		ok     bool
	}{
		{platform.IndividualOwnerService, TypeIdentity, sub(SubjectUser, platform.DomainIndividualOwner), true},
		{platform.IndividualOwnerService, TypeIdentity, sub(SubjectUser, platform.DomainArtisan), false}, // other domain's subject
		{platform.PaymentService, TypeIdentity, sub(SubjectUser, platform.DomainIndividualOwner), false}, // shared service
		{platform.APIGateway, TypeIdentity, sub(SubjectUser, platform.DomainIndividualOwner), false},     // gateway
		{platform.MarketplaceService, TypePropertyAuthority, sub(SubjectProperty, platform.DomainMarketplace), false},
		{platform.PropertyManagerService, TypePropertyAuthority, sub(SubjectProperty, platform.DomainPropertyManager), true},
		{platform.ArtisanService, TypeProfessionalCredential, sub(SubjectProfessional, platform.DomainArtisan), true},
		{platform.IndividualOwnerService, TypeProfessionalCredential, sub(SubjectProfessional, platform.DomainIndividualOwner), false},
		{platform.BusinessOperationsService, TypeBusiness, sub(SubjectBusiness, platform.DomainBusinessOperations), true},
		{platform.AdminService, TypeBusiness, sub(SubjectBusiness, platform.DomainAdmin), false},
	}
	for _, c := range cases {
		err := CanInitiate(c.caller, c.typ, c.sub)
		if (err == nil) != c.ok {
			t.Errorf("%s %s %s/%s: err=%v want ok=%v", c.caller, c.typ, c.sub.Type, c.sub.Domain, err, c.ok)
		}
	}
	if !SupportsSubject(TypeIdentity, SubjectUser) || SupportsSubject(TypeIdentity, SubjectProperty) {
		t.Fatal("subject compatibility")
	}
	if !CanRead(platform.AdminService, platform.DomainArtisan, "artisan-service") ||
		!CanRead(platform.ArtisanService, platform.DomainArtisan, "artisan-service") ||
		CanRead(platform.MarketplaceService, platform.DomainArtisan, "artisan-service") ||
		CanRead(platform.NotificationService, platform.DomainArtisan, "artisan-service") {
		t.Fatal("read policy")
	}
}
