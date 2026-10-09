package domain

import (
	"testing"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
)

func TestAccountTransitionTable(t *testing.T) {
	type tc struct {
		from, to   AccountStatus
		actor      TransitionActor
		resolution string
		wantCode   string // "" = allowed
	}
	cases := []tc{
		{StatusPendingVerification, StatusActive, ActorSystem, "", ""},
		{StatusPendingVerification, StatusActive, ActorOwner, "", apperrors.CodePermissionDenied},
		{StatusPendingVerification, StatusActive, ActorAdmin, "", apperrors.CodePermissionDenied},
		{StatusActive, StatusRestricted, ActorAdmin, "", ""},
		{StatusActive, StatusRestricted, ActorOwner, "", apperrors.CodePermissionDenied},
		{StatusActive, StatusSuspended, ActorAdmin, "", ""},
		{StatusRestricted, StatusActive, ActorAdmin, "", "RESOLUTION_REFERENCE_REQUIRED"},
		{StatusRestricted, StatusActive, ActorAdmin, "CASE-2026-0001", ""},
		{StatusRestricted, StatusActive, ActorSystem, "CASE-2026-0001", apperrors.CodePermissionDenied},
		{StatusSuspended, StatusActive, ActorAdmin, "x", "RESOLUTION_REFERENCE_REQUIRED"},
		{StatusSuspended, StatusActive, ActorAdmin, "ADM/77/2026", ""},
		{StatusSuspended, StatusClosurePending, ActorOwner, "", apperrors.CodeInvalidStateTransition},
		{StatusRestricted, StatusClosurePending, ActorOwner, "", apperrors.CodeInvalidStateTransition},
		{StatusActive, StatusClosurePending, ActorOwner, "", ""},
		{StatusClosurePending, StatusClosed, ActorSystem, "", ""},
		{StatusClosurePending, StatusClosed, ActorOwner, "", apperrors.CodePermissionDenied},
		{StatusClosurePending, StatusActive, ActorOwner, "", ""},
		{StatusClosed, StatusActive, ActorAdmin, "CASE-1", apperrors.CodeInvalidStateTransition},
		{StatusActive, StatusPendingVerification, ActorSystem, "", apperrors.CodeInvalidStateTransition},
		{StatusActive, StatusActive, ActorAdmin, "CASE-1", apperrors.CodeInvalidStateTransition},
	}
	for _, c := range cases {
		_, err := CheckAccountTransition(c.from, c.to, c.actor, c.resolution)
		got := apperrors.CodeOf(err)
		if got != c.wantCode {
			t.Errorf("%s -> %s by %s: got %q want %q", c.from, c.to, c.actor, got, c.wantCode)
		}
	}
}

func TestClosedIsTerminalAndEveryStateKnown(t *testing.T) {
	if len(AccountTargets(StatusClosed)) != 0 {
		t.Fatal("CLOSED must be terminal")
	}
	for e := range accountTransitions {
		if !e.from.Valid() || !e.to.Valid() {
			t.Fatalf("unknown state in edge %v", e)
		}
	}
	for _, s := range AccountStatuses() {
		if s != StatusClosed && len(AccountTargets(s)) == 0 {
			t.Fatalf("%s has no exits", s)
		}
	}
}

func TestCanAuthenticate(t *testing.T) {
	want := map[AccountStatus]bool{StatusPendingVerification: true, StatusActive: true, StatusRestricted: true,
		StatusClosurePending: true, StatusSuspended: false, StatusClosed: false}
	for s, w := range want {
		if s.CanAuthenticate() != w {
			t.Errorf("%s CanAuthenticate=%v", s, !w)
		}
	}
}

func TestSection7MachinesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Machines() {
		if seen[m.Aggregate] {
			t.Fatalf("duplicate machine %s", m.Aggregate)
		}
		seen[m.Aggregate] = true
		terminal := 0
		for _, s := range m.States() {
			if m.Terminal(s) {
				terminal++
			}
		}
		if terminal == 0 {
			t.Errorf("%s has no terminal state", m.Aggregate)
		}
	}
	// Spot-check canonical edges from §7.
	cases := []struct {
		ok       bool
		from, to string
		m        interface{ Can(string, string) bool }
	}{
		{true, "PAUSED", "PUBLISHED", Listing}, {false, "DRAFT", "PUBLISHED", Listing},
		{true, "SENT", "ACCEPTED", RentalOffer}, {false, "ACCEPTED", "WITHDRAWN", RentalOffer},
		{true, "SIGNING", "FULLY_SIGNED", Agreement}, {false, "EFFECTIVE", "CANCELLED", Agreement},
		{true, "CLOSING", "COMPLETED", Sale}, {false, "COMPLETED", "CANCELLED", Sale}, {false, "OFFERING", "COMPLETED", Sale},
		{true, "HOLD", "PENDING_PAYMENT", ShortletBooking}, {false, "COMPLETED", "CANCELLED", ShortletBooking},
		{true, "ACTIVE", "REVOKED", ManagerAssignment}, {false, "REVOKED", "ACTIVE", ManagerAssignment},
		{true, "ACTIVE", "COMPROMISED", AccessGrant}, {false, "REVOKED", "ACTIVE", AccessGrant},
		{true, "DECLINED", "APPEAL", Application}, {false, "APPROVED", "DECLINED", Application},
		{true, "REFUND_PENDING", "REFUNDED", RentObligation}, {false, "PAID", "DUE", RentObligation},
		{true, "TRANSFER_PENDING", "TRANSFERRED", Ownership}, {false, "REJECTED", "VERIFIED", Ownership},
	}
	for _, c := range cases {
		if c.m.Can(c.from, c.to) != c.ok {
			t.Errorf("%s -> %s expected %v", c.from, c.to, c.ok)
		}
	}
}
