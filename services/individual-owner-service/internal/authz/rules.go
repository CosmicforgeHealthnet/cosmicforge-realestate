package authz

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Section 5 account actions.
const (
	ActAccountRead             Action = "account.read"
	ActAccountUpdate           Action = "account.profile.update"
	ActContactVerify           Action = "account.contact.verify"
	ActContactChange           Action = "account.contact.change"
	ActPasswordChange          Action = "account.password.change"
	ActStepUp                  Action = "account.step_up"
	ActLogout                  Action = "account.logout"
	ActSessionsRead            Action = "account.sessions.read"
	ActSessionsRevoke          Action = "account.sessions.revoke"
	ActDevicesRead             Action = "account.devices.read"
	ActDevicesTrust            Action = "account.devices.trust"
	ActDevicesRevoke           Action = "account.devices.revoke"
	ActSecurityEventsRead      Action = "account.security_events.read"
	ActMFARead                 Action = "account.mfa.read"
	ActMFAEnroll               Action = "account.mfa.enroll"
	ActMFADisable              Action = "account.mfa.disable"
	ActRecoveryCodesRegenerate Action = "account.mfa.recovery_codes.regenerate"
	ActClosureRequest          Action = "account.closure.request"
	ActClosureCancel           Action = "account.closure.cancel"

	ActAdminAccountRead    Action = "account.admin.read"
	ActAdminRestrict       Action = "account.admin.restrict"
	ActAdminSuspend        Action = "account.admin.suspend"
	ActAdminReinstate      Action = "account.admin.reinstate"
	ActAdminRevokeSessions Action = "account.admin.sessions.revoke"

	ActSystemFinalizeClosure Action = "account.system.finalize_closure"
)

// signedIn are the states in which an account may use self-service.
var signedIn = []domain.AccountStatus{domain.StatusPendingVerification, domain.StatusActive, domain.StatusRestricted, domain.StatusClosurePending}

// adminActor: admin-service acting for an attributable admin account.
var adminActor = []ServiceGrant{{Service: platform.AdminService, ActorDomains: []platform.Domain{platform.DomainAdmin}, RequiresActor: true}}

// AccountRules is the Section 5 policy table. Step-up guards every command
// that could hand the account to an attacker holding only a stolen session.
func AccountRules() []Rule {
	self := func(a Action, stepUp bool, statuses ...domain.AccountStatus) Rule {
		if len(statuses) == 0 {
			statuses = signedIn
		}
		return Rule{Action: a, SelfService: true, StepUp: stepUp, AccountStatuses: statuses}
	}
	return []Rule{
		self(ActAccountRead, false),
		self(ActAccountUpdate, false),
		self(ActContactVerify, false),
		self(ActContactChange, true),
		self(ActPasswordChange, false), // requires the current password in the command itself
		self(ActStepUp, false),
		self(ActLogout, false),
		self(ActSessionsRead, false),
		self(ActSessionsRevoke, false),
		self(ActDevicesRead, false),
		self(ActDevicesTrust, true),
		self(ActDevicesRevoke, false),
		self(ActSecurityEventsRead, false),
		self(ActMFARead, false),
		self(ActMFAEnroll, true),
		self(ActMFADisable, true),
		self(ActRecoveryCodesRegenerate, true),
		self(ActClosureRequest, true, domain.StatusPendingVerification, domain.StatusActive),
		self(ActClosureCancel, false, domain.StatusClosurePending),
		{Action: ActAdminAccountRead, Services: adminActor},
		{Action: ActAdminRestrict, Services: adminActor},
		{Action: ActAdminSuspend, Services: adminActor},
		{Action: ActAdminReinstate, Services: adminActor},
		{Action: ActAdminRevokeSessions, Services: adminActor},
		{Action: ActSystemFinalizeClosure, System: true},
	}
}
