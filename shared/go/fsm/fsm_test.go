package fsm

import (
	"testing"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
)

type st string

func TestMachine(t *testing.T) {
	m := New(map[st][]st{"A": {"B"}, "B": {"C", "D"}})
	if !m.Can("A", "B") || m.Can("A", "C") || m.Can("C", "A") {
		t.Fatal("edges")
	}
	err := m.Transition("A", "D")
	if apperrors.CodeOf(err) != apperrors.CodeInvalidStateTransition || apperrors.From(err).Status != 409 {
		t.Fatalf("got %v", err)
	}
	if !m.Terminal("C") || m.Terminal("B") || len(m.Targets("B")) != 2 {
		t.Fatal("terminal/targets")
	}
}
