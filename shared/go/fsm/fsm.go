// Package fsm is a minimal, explicit state-transition table. Services declare
// their approved state machine (FOUNDATION_STATE_MACHINES.md) as data so that
// illegal transitions are rejected uniformly with INVALID_STATE_TRANSITION.
package fsm

import (
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
)

// Machine is an immutable transition table.
type Machine[S ~string] struct {
	edges map[S]map[S]bool
}

// New builds a machine from from -> []to edges.
func New[S ~string](edges map[S][]S) Machine[S] {
	m := Machine[S]{edges: map[S]map[S]bool{}}
	for from, tos := range edges {
		if m.edges[from] == nil {
			m.edges[from] = map[S]bool{}
		}
		for _, to := range tos {
			m.edges[from][to] = true
		}
	}
	return m
}

// Can reports whether from -> to is allowed.
func (m Machine[S]) Can(from, to S) bool { return m.edges[from][to] }

// Transition returns nil or a 409 INVALID_STATE_TRANSITION error.
func (m Machine[S]) Transition(from, to S) error {
	if m.Can(from, to) {
		return nil
	}
	return apperrors.InvalidStateTransition(string(from), string(to))
}

// Terminal reports whether no transitions leave s.
func (m Machine[S]) Terminal(s S) bool { return len(m.edges[s]) == 0 }

// Targets returns allowed next states of s.
func (m Machine[S]) Targets(s S) []S {
	var out []S
	for to := range m.edges[s] {
		out = append(out, to)
	}
	return out
}
