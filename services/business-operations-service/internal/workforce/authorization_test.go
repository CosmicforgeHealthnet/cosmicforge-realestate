package workforce

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"testing"
	"time"
)

func TestAuthorizationIsolationAndRevocation(t *testing.T) {
	now := time.Now().UTC()
	org, branch, account := ids.New(), ids.New(), ids.New()
	company := Scope{Organisation: org, Kind: "COMPANY", ID: org}
	scope := Scope{Organisation: org, Kind: "BRANCH", ID: branch}
	capability := permissions.Permission("staff:read")
	base := Membership{AccountID: account, OrganisationID: org, Status: "ACTIVE", EffectiveAt: now.Add(-time.Hour), RoleCapabilities: permissions.NewSet(capability), Grants: []Grant{{Capability: capability, Scope: company, Inherit: true, EffectiveAt: now.Add(-time.Hour)}}}
	input := DecisionInput{AccountID: account, Target: Target{Scope: scope, Ancestors: []Scope{company}}, Capability: capability, Now: now}
	if err := Authorize(base, input); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*Membership, *DecisionInput)
	}{
		{"cross tenant", func(m *Membership, i *DecisionInput) { i.Target.Scope.Organisation = ids.New() }},
		{"wrong account", func(m *Membership, i *DecisionInput) { i.AccountID = ids.New() }},
		{"suspended", func(m *Membership, i *DecisionInput) { m.Status = "SUSPENDED" }},
		{"membership expired", func(m *Membership, i *DecisionInput) { m.ExpiresAt = &now }},
		{"role revoked", func(m *Membership, i *DecisionInput) { m.RoleCapabilities = permissions.NewSet() }},
		{"grant revoked", func(m *Membership, i *DecisionInput) { m.Grants[0].RevokedAt = &now }},
		{"grant expired", func(m *Membership, i *DecisionInput) { m.Grants[0].ExpiresAt = &now }},
		{"grant future", func(m *Membership, i *DecisionInput) { m.Grants[0].EffectiveAt = now.Add(time.Second) }},
		{"no inheritance", func(m *Membership, i *DecisionInput) { m.Grants[0].Inherit = false }},
		{"other branch", func(m *Membership, i *DecisionInput) { m.Grants[0].Scope = scope; i.Target.Scope.ID = ids.New() }},
		{"explicit deny", func(m *Membership, i *DecisionInput) {
			m.Grants = append(m.Grants, Grant{Capability: capability, Scope: scope, Deny: true})
		}},
		{"restricted", func(m *Membership, i *DecisionInput) { i.Restricted = true }},
		{"MFA missing", func(m *Membership, i *DecisionInput) { m.Grants[0].RequireMFA = true; i.StepUpWindow = 5 * time.Minute }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			m.Grants = append([]Grant(nil), base.Grants...)
			i := input
			tc.mutate(&m, &i)
			if Authorize(m, i) == nil {
				t.Fatal("unsafe authorization allowed")
			}
		})
	}
	limit, amount := int64(100), int64(101)
	base.Grants[0].AmountLimitMinor = &limit
	base.Grants[0].Currency = "EUR"
	input.AmountMinor = &amount
	input.Currency = "EUR"
	if Authorize(base, input) == nil {
		t.Fatal("approval limit exceeded")
	}
	amount = 100
	if Authorize(base, input) != nil {
		t.Fatal("exact minor-unit limit denied")
	}
	input.Currency = "USD"
	if Authorize(base, input) == nil {
		t.Fatal("currency confusion")
	}
}

func TestDelegationCannotExpandAuthority(t *testing.T) {
	now := time.Now().UTC()
	until := now.Add(time.Hour)
	org, account, delegate := ids.New(), ids.New(), ids.New()
	scope := Scope{Organisation: org, Kind: "COMPANY", ID: org}
	capability := permissions.Permission("staff:read")
	target := Target{Scope: scope}
	m := Membership{AccountID: account, OrganisationID: org, Status: "ACTIVE", RoleCapabilities: permissions.NewSet(capability), Grants: []Grant{{Capability: capability, Scope: scope, Inherit: true, ExpiresAt: &until}}}
	g := Grant{Capability: capability, Scope: scope, Inherit: true, EffectiveAt: now, ExpiresAt: &until}
	if ValidateDelegation(m, delegate, g, target, now, time.Hour) != nil {
		t.Fatal("valid delegation denied")
	}
	later := until.Add(time.Minute)
	g.ExpiresAt = &later
	if ValidateDelegation(m, delegate, g, target, now, 2*time.Hour) == nil {
		t.Fatal("delegation outlives authority")
	}
	g.ExpiresAt = &until
	g.Capability = "roles:write"
	if ValidateDelegation(m, delegate, g, target, now, time.Hour) == nil {
		t.Fatal("capability escalation")
	}
	g.Capability = capability
	m.Grants[0].RequireMFA = true
	if ValidateDelegation(m, delegate, g, target, now, time.Hour) == nil {
		t.Fatal("MFA removed by delegation")
	}
	g.RequireMFA = true
	expiry := now.Add(time.Minute)
	m.ExpiresAt = &expiry
	if ValidateDelegation(m, delegate, g, target, now, time.Hour) == nil {
		t.Fatal("delegation outlives membership")
	}
}
