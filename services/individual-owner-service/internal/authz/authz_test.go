package authz

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

var now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func tp(t time.Time) *time.Time { return &t }

type fakeResolver struct {
	facts map[ids.ID]PropertyFacts
	err   error
}

func (f fakeResolver) PropertyFacts(_ context.Context, _ Subject, id ids.ID) (PropertyFacts, error) {
	if f.err != nil {
		return PropertyFacts{}, f.err
	}
	return f.facts[id], nil
}

const (
	capPublish Capability = "listing.publish"
	capRepair  Capability = "maintenance.approve"
)

// Test-only property rules exercising the semantics the Section 6/8
// catalogue will register.
func propertyRules() []Rule {
	return []Rule{
		{Action: "property.publish_listing", PropertyScoped: true, Roles: []Role{RoleOwner, RoleCoOwner, RoleRepresentative, RoleManager},
			Capability: capPublish, CoOwnerConsent: true, RequireVerifiedAuthority: true, AccountStatuses: []domain.AccountStatus{domain.StatusActive},
			Services: []ServiceGrant{{Service: platform.PropertyManagerService, ActorDomains: []platform.Domain{platform.DomainPropertyManager}, RequiresActor: true}}},
		{Action: "property.change_payout", PropertyScoped: true, Roles: []Role{RoleOwner}, StepUp: true,
			AccountStatuses: []domain.AccountStatus{domain.StatusActive},
			Services:        []ServiceGrant{{Service: platform.PropertyManagerService, ActorDomains: []platform.Domain{platform.DomainPropertyManager}, RequiresActor: true}}},
	}
}

func engine(res AuthorityResolver) *Engine {
	return New(Config{Rules: append(AccountRules(), propertyRules()...), Resolver: res, StepUpWindow: 5 * time.Minute, Now: func() time.Time { return now }})
}

func owner(status domain.AccountStatus, stepUp *time.Time) Subject {
	return Subject{Kind: KindOwnerSession, ActorDomain: platform.DomainIndividualOwner, ActorID: ids.New(),
		Account: &AccountContext{AccountID: ids.New(), SessionID: ids.New(), Status: status, StepUpAt: stepUp}}
}

func TestAccountRules(t *testing.T) {
	e := engine(nil)
	ctx := context.Background()
	fresh, stale := tp(now.Add(-time.Minute)), tp(now.Add(-6*time.Minute))
	cases := []struct {
		name string
		s    Subject
		a    Action
		res  func(s Subject) Resource
		code string
	}{
		{"active reads own account", owner(domain.StatusActive, nil), ActAccountRead, nil, ""},
		{"pending may verify contact", owner(domain.StatusPendingVerification, nil), ActContactVerify, nil, ""},
		{"mfa disable without step-up", owner(domain.StatusActive, nil), ActMFADisable, nil, "STEP_UP_REQUIRED"},
		{"mfa disable with stale step-up", owner(domain.StatusActive, stale), ActMFADisable, nil, "STEP_UP_REQUIRED"},
		{"mfa disable with fresh step-up", owner(domain.StatusActive, fresh), ActMFADisable, nil, ""},
		{"future-dated step-up rejected", owner(domain.StatusActive, tp(now.Add(time.Hour))), ActMFADisable, nil, "STEP_UP_REQUIRED"},
		{"suspended session cannot act", owner(domain.StatusSuspended, fresh), ActAccountRead, nil, "ACCOUNT_UNAVAILABLE"},
		{"restricted cannot request closure", owner(domain.StatusRestricted, fresh), ActClosureRequest, nil, "ACCOUNT_RESTRICTED"},
		{"closure cancel only when pending", owner(domain.StatusActive, nil), ActClosureCancel, nil, "ACCOUNT_NOT_ACTIVE"},
		{"IDOR on another account", owner(domain.StatusActive, nil), ActAccountRead, func(Subject) Resource { return Resource{AccountID: ids.New()} }, "ACCOUNT_NOT_FOUND"},
		{"own account resource", owner(domain.StatusActive, nil), ActAccountRead, func(s Subject) Resource { return Resource{AccountID: s.Account.AccountID} }, ""},
		{"owner escalates to admin action", owner(domain.StatusActive, fresh), ActAdminSuspend, nil, "PERMISSION_DENIED"},
		{"unknown action fails closed", owner(domain.StatusActive, fresh), "account.anything", nil, "PERMISSION_DENIED"},
		{"gateway actor without session", Subject{Kind: KindGatewayActor, ActorDomain: platform.DomainIndividualOwner, ActorID: ids.New()}, ActAccountRead, nil, "AUTHENTICATION_REQUIRED"},
		{"admin service with admin actor", Subject{Kind: KindService, Service: platform.AdminService, ActorDomain: platform.DomainAdmin, ActorID: ids.New()}, ActAdminSuspend, nil, ""},
		{"admin service without actor", Subject{Kind: KindService, Service: platform.AdminService}, ActAdminSuspend, nil, "DELEGATED_ACTOR_REQUIRED"},
		{"admin service with non-admin actor", Subject{Kind: KindService, Service: platform.AdminService, ActorDomain: platform.DomainMarketplace, ActorID: ids.New()}, ActAdminSuspend, nil, "PERMISSION_DENIED"},
		{"other service token cannot suspend", Subject{Kind: KindService, Service: platform.MarketplaceService, ActorDomain: platform.DomainAdmin, ActorID: ids.New()}, ActAdminSuspend, nil, "PERMISSION_DENIED"},
		{"gateway service principal cannot suspend", Subject{Kind: KindService, Service: platform.APIGateway}, ActAdminSuspend, nil, "PERMISSION_DENIED"},
		{"service cannot use self-service", Subject{Kind: KindService, Service: platform.AdminService, ActorDomain: platform.DomainAdmin, ActorID: ids.New()}, ActMFADisable, nil, "PERMISSION_DENIED"},
		{"system finalizes closure", Subject{Kind: KindSystem}, ActSystemFinalizeClosure, nil, ""},
		{"system cannot disable mfa", Subject{Kind: KindSystem}, ActMFADisable, nil, "PERMISSION_DENIED"},
	}
	for _, c := range cases {
		var res Resource
		if c.res != nil {
			res = c.res(c.s)
		}
		d := e.Authorize(ctx, c.s, c.a, res)
		if (c.code == "") != d.Allowed || (!d.Allowed && d.Code != c.code) {
			t.Errorf("%s: allowed=%v code=%q want %q", c.name, d.Allowed, d.Code, c.code)
		}
	}
}

func TestPropertyAuthority(t *testing.T) {
	ctx := context.Background()
	prop := ids.New()
	mk := func(f PropertyFacts) *Engine { return engine(fakeResolver{facts: map[ids.ID]PropertyFacts{prop: f}}) }
	act := owner(domain.StatusActive, tp(now))
	mgr := Subject{Kind: KindService, Service: platform.PropertyManagerService, ActorDomain: platform.DomainPropertyManager, ActorID: ids.New()}
	rel := func(r Role, caps ...Capability) Relationship {
		return Relationship{Role: r, ReferenceID: ids.New(), Capabilities: caps}
	}
	verified := func(rels ...Relationship) PropertyFacts {
		return PropertyFacts{Exists: true, AuthorityVerified: true, Relationships: rels}
	}
	cases := []struct {
		name  string
		facts PropertyFacts
		s     Subject
		a     Action
		code  string
		oblig bool
	}{
		{"sole owner publishes", verified(rel(RoleOwner)), act, "property.publish_listing", "", false},
		{"owner with co-owners needs consent", func() PropertyFacts { f := verified(rel(RoleOwner)); f.CoOwnerCount = 1; return f }(), act, "property.publish_listing", "", true},
		{"co-owner needs consent", func() PropertyFacts { f := verified(rel(RoleCoOwner)); f.CoOwnerCount = 1; return f }(), act, "property.publish_listing", "", true},
		{"co-owner cannot change payout (owner only)", verified(rel(RoleCoOwner)), act, "property.change_payout", "PROPERTY_AUTHORITY_REQUIRED", false},
		{"unverified authority", PropertyFacts{Exists: true, Relationships: []Relationship{rel(RoleOwner)}}, act, "property.publish_listing", "PROPERTY_AUTHORITY_UNVERIFIED", false},
		{"admin restricted property", func() PropertyFacts { f := verified(rel(RoleOwner)); f.AdminRestricted = true; return f }(), act, "property.publish_listing", "PROPERTY_RESTRICTED", false},
		{"unrelated subject sees not found", verified(), act, "property.publish_listing", "PROPERTY_NOT_FOUND", false},
		{"missing property", PropertyFacts{}, act, "property.publish_listing", "PROPERTY_NOT_FOUND", false},
		{"representative with capability", verified(rel(RoleRepresentative, capPublish)), act, "property.publish_listing", "", false},
		{"representative without capability", verified(rel(RoleRepresentative, capRepair)), act, "property.publish_listing", "PROPERTY_AUTHORITY_REQUIRED", false},
		{"expired representative", verified(Relationship{Role: RoleRepresentative, Capabilities: []Capability{capPublish}, ValidUntil: tp(now)}), act, "property.publish_listing", "PROPERTY_AUTHORITY_REQUIRED", false},
		{"not-yet-valid representative", verified(Relationship{Role: RoleRepresentative, Capabilities: []Capability{capPublish}, ValidFrom: tp(now.Add(time.Hour))}), act, "property.publish_listing", "PROPERTY_AUTHORITY_REQUIRED", false},
		{"manager with delegated capability", verified(rel(RoleManager, capPublish)), mgr, "property.publish_listing", "", false},
		{"revoked manager loses authority immediately", verified(Relationship{Role: RoleManager, Capabilities: []Capability{capPublish}, RevokedAt: tp(now)}), mgr, "property.publish_listing", "PROPERTY_AUTHORITY_REQUIRED", false},
		{"expired manager mandate", verified(Relationship{Role: RoleManager, Capabilities: []Capability{capPublish}, ValidUntil: tp(now.Add(-time.Second))}), mgr, "property.publish_listing", "PROPERTY_AUTHORITY_REQUIRED", false},
		{"manager cannot change payout", verified(rel(RoleManager, capPublish, "payout.change")), mgr, "property.change_payout", "PROPERTY_AUTHORITY_REQUIRED", false},
		{"manager service without actor", verified(rel(RoleManager, capPublish)), Subject{Kind: KindService, Service: platform.PropertyManagerService}, "property.publish_listing", "DELEGATED_ACTOR_REQUIRED", false},
		{"artisan service not granted", verified(rel(RoleManager, capPublish)), Subject{Kind: KindService, Service: platform.ArtisanService, ActorDomain: platform.DomainArtisan, ActorID: ids.New()}, "property.publish_listing", "PERMISSION_DENIED", false},
		{"payout change requires step-up", verified(rel(RoleOwner)), owner(domain.StatusActive, nil), "property.change_payout", "STEP_UP_REQUIRED", false},
		{"restricted owner cannot publish", verified(rel(RoleOwner)), owner(domain.StatusRestricted, tp(now)), "property.publish_listing", "ACCOUNT_RESTRICTED", false},
	}
	for _, c := range cases {
		d := mk(c.facts).Authorize(ctx, c.s, c.a, Resource{PropertyID: prop})
		if (c.code == "") != d.Allowed || (!d.Allowed && d.Code != c.code) {
			t.Errorf("%s: allowed=%v code=%q want %q", c.name, d.Allowed, d.Code, c.code)
		}
		if d.Allowed && c.oblig != (len(d.Obligations) == 1 && d.Obligations[0] == ObligationCoOwnerConsent) {
			t.Errorf("%s: obligations %v", c.name, d.Obligations)
		}
	}
}

func TestPropertyFailsClosedWithoutStore(t *testing.T) {
	d := engine(nil).Authorize(context.Background(), owner(domain.StatusActive, tp(now)), "property.publish_listing", Resource{PropertyID: ids.New()})
	if d.Allowed || d.Code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("%+v", d)
	}
	d = engine(fakeResolver{err: errors.New("db down")}).Authorize(context.Background(), owner(domain.StatusActive, tp(now)), "property.publish_listing", Resource{PropertyID: ids.New()})
	if d.Allowed {
		t.Fatal("resolver error must deny")
	}
	d = engine(nil).Authorize(context.Background(), owner(domain.StatusActive, tp(now)), "property.publish_listing", Resource{})
	if d.Allowed || d.Code != "INVALID_REQUEST" {
		t.Fatalf("%+v", d)
	}
}

func TestRuleValidationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	New(Config{Rules: []Rule{{Action: "a.b"}, {Action: "a.b"}}})
}
