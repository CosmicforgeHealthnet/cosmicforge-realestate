// Package lifecycle implements the Section 7 canonical state machines as
// data and applies transitions safely:
//
//   - only declared edges are allowed (INVALID_STATE_TRANSITION otherwise);
//   - the row is locked and updated with compare-and-swap on version, so
//     concurrent contradictory commands cannot both succeed;
//   - an optional expected version (If-Match) rejects stale commands;
//   - every transition is appended to lifecycle_transitions (immutable
//     history) in the caller's transaction, attributed from the request
//     context (actor, calling service).
//
// Authorization, preconditions, audit and outbox events are the caller's
// responsibility and run in the same transaction.
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Machine is one aggregate's lifecycle.
type Machine struct {
	Aggregate string // lifecycle_transitions.aggregate_type / event aggregate
	Table     string // owning table: id, property_id, status, version, updated_at
	// PropertyColumn holds the property reference ("id" for the property
	// root itself, "NULL" for aggregates not scoped to a property).
	PropertyColumn string
	states         map[string]bool
	order          []string
	edges          map[string]map[string]bool
}

var ident = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// New defines a machine. It panics on programmer error (unknown state in an
// edge, unsafe identifiers) so a bad definition never reaches production.
func New(aggregate, table string, states []string, edges map[string][]string) *Machine {
	if !ident.MatchString(aggregate) || !ident.MatchString(table) {
		panic("lifecycle: invalid aggregate/table identifier")
	}
	m := &Machine{Aggregate: aggregate, Table: table, PropertyColumn: "property_id", states: map[string]bool{}, order: states, edges: map[string]map[string]bool{}}
	if table == "properties" {
		m.PropertyColumn = "id"
	}
	for _, s := range states {
		m.states[s] = true
	}
	for from, tos := range edges {
		if !m.states[from] {
			panic(fmt.Sprintf("lifecycle %s: unknown state %s", aggregate, from))
		}
		m.edges[from] = map[string]bool{}
		for _, to := range tos {
			if !m.states[to] || to == from {
				panic(fmt.Sprintf("lifecycle %s: invalid edge %s -> %s", aggregate, from, to))
			}
			m.edges[from][to] = true
		}
	}
	return m
}

// States lists the declared states in order.
func (m *Machine) States() []string { return append([]string(nil), m.order...) }

// Unscoped marks a machine whose aggregate is not property-scoped.
func (m *Machine) Unscoped() *Machine { m.PropertyColumn = "NULL"; return m }

// Valid reports whether s is a state.
func (m *Machine) Valid(s string) bool { return m.states[s] }

// Can reports whether from -> to is declared.
func (m *Machine) Can(from, to string) bool { return m.edges[from][to] }

// Targets returns the states reachable from s.
func (m *Machine) Targets(s string) []string {
	var out []string
	for t := range m.edges[s] {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Terminal reports whether s has no exits.
func (m *Machine) Terminal(s string) bool { return len(m.edges[s]) == 0 }

// Check validates from -> to.
func (m *Machine) Check(from, to string) error {
	if m.Can(from, to) {
		return nil
	}
	return apperrors.InvalidStateTransition(from, to)
}

// Row is the lifecycle view of an aggregate row.
type Row struct {
	ID         ids.ID
	PropertyID *ids.ID
	Status     string
	Version    int64
}

// Lock loads and row-locks id. pgx.ErrNoRows when absent.
func (m *Machine) Lock(ctx context.Context, q db.DBTX, id ids.ID) (Row, error) {
	var r Row
	err := q.QueryRow(ctx, `SELECT id, `+m.PropertyColumn+`::uuid, status, version FROM `+m.Table+` WHERE id = $1 FOR UPDATE`, id).
		Scan(&r.ID, &r.PropertyID, &r.Status, &r.Version)
	return r, err
}

// Change describes one transition.
type Change struct {
	To              string
	Command         string
	Reason          string
	ExpectedVersion int64 // 0 = no If-Match precondition
	Metadata        map[string]any
	Now             time.Time
}

// ErrVersionConflict is returned for stale or lost compare-and-swap updates.
func ErrVersionConflict() *apperrors.Error {
	return apperrors.Conflict("VERSION_CONFLICT", "The resource was modified concurrently. Reload and retry.")
}

// Apply performs a locked, compare-and-swap transition and records history.
func (m *Machine) Apply(ctx context.Context, q db.DBTX, r Row, ch Change) (Row, error) {
	if ch.ExpectedVersion > 0 && ch.ExpectedVersion != r.Version {
		return Row{}, ErrVersionConflict()
	}
	if err := m.Check(r.Status, ch.To); err != nil {
		return Row{}, err
	}
	if ch.Now.IsZero() {
		return Row{}, errors.New("lifecycle: Change.Now required")
	}
	var next int64
	err := q.QueryRow(ctx, `UPDATE `+m.Table+` SET status = $1, version = version + 1, updated_at = $2
		WHERE id = $3 AND version = $4 AND status = $5 RETURNING version`, ch.To, ch.Now, r.ID, r.Version, r.Status).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return Row{}, ErrVersionConflict()
	}
	if err != nil {
		return Row{}, err
	}
	rc := requestcontext.From(ctx)
	actorType, actorDomain, actorID := "SYSTEM", (*string)(nil), (*ids.ID)(nil)
	if !rc.Actor.IsZero() {
		actorType, actorDomain, actorID = "USER", &rc.Actor.Domain, &rc.Actor.ID
	} else if rc.CallingService != "" {
		actorType = "SERVICE"
	}
	var md []byte
	if len(ch.Metadata) > 0 {
		md, _ = json.Marshal(ch.Metadata)
	}
	var calling *string
	if rc.CallingService != "" {
		calling = &rc.CallingService
	}
	var reason *string
	if ch.Reason != "" {
		reason = &ch.Reason
	}
	command := ch.Command
	if command == "" {
		command = "transition"
	}
	if _, err := q.Exec(ctx, `INSERT INTO lifecycle_transitions (id, aggregate_type, aggregate_id, property_id, from_status, to_status,
		command, reason, actor_type, actor_domain, actor_id, calling_service, aggregate_version, metadata, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		ids.New(), m.Aggregate, r.ID, r.PropertyID, r.Status, ch.To, command, reason, actorType, actorDomain, actorID, calling, next, md, ch.Now); err != nil {
		return Row{}, err
	}
	return Row{ID: r.ID, PropertyID: r.PropertyID, Status: ch.To, Version: next}, nil
}

// RecordCreation appends the initial-state history row for a new aggregate.
func (m *Machine) RecordCreation(ctx context.Context, q db.DBTX, id ids.ID, propertyID *ids.ID, status string, now time.Time) error {
	if !m.Valid(status) {
		return fmt.Errorf("lifecycle %s: invalid initial state %s", m.Aggregate, status)
	}
	rc := requestcontext.From(ctx)
	actorType, actorDomain, actorID := "SYSTEM", (*string)(nil), (*ids.ID)(nil)
	if !rc.Actor.IsZero() {
		actorType, actorDomain, actorID = "USER", &rc.Actor.Domain, &rc.Actor.ID
	} else if rc.CallingService != "" {
		actorType = "SERVICE"
	}
	_, err := q.Exec(ctx, `INSERT INTO lifecycle_transitions (id, aggregate_type, aggregate_id, property_id, from_status, to_status,
		command, actor_type, actor_domain, actor_id, aggregate_version, occurred_at)
		VALUES ($1,$2,$3,$4,'',$5,'create',$6,$7,$8,1,$9)`, ids.New(), m.Aggregate, id, propertyID, status, actorType, actorDomain, actorID, now)
	return err
}
