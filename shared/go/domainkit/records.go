package domainkit

import (
	"context"
	"errors"
	"regexp"
	"slices"

	"github.com/jackc/pgx/v5"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

var tableName = regexp.MustCompile(`^[a-z][a-z0-9_]{2,62}$`)

// State is a locked governed record's current state and version.
type State struct {
	State   string
	Version int64
}

// Lock reads and row-locks a governed record (tables with id, state,
// version). table must be a constant identifier.
func Lock(ctx context.Context, tx pgx.Tx, table string, id ids.ID) (State, error) {
	if !tableName.MatchString(table) {
		return State{}, errors.New("domainkit: invalid table")
	}
	var s State
	err := tx.QueryRow(ctx, `SELECT state,version FROM `+table+` WHERE id=$1 FOR UPDATE`, id).Scan(&s.State, &s.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, apperrors.NotFound("RESOURCE_NOT_FOUND", "Resource not found.")
	}
	return s, err
}

// Move applies one governed transition at the expected version. The
// database guard independently enforces the same edge.
func Move(ctx context.Context, tx pgx.Tx, table string, id ids.ID, expected int64, from []string, to string) (State, error) {
	s, err := Lock(ctx, tx, table, id)
	if err != nil {
		return s, err
	}
	if expected > 0 && s.Version != expected {
		return s, apperrors.Conflict("VERSION_CONFLICT", "The record version has changed.")
	}
	if !slices.Contains(from, s.State) {
		return s, apperrors.Conflict("INVALID_TRANSITION", "Transition to "+to+" is not allowed from "+s.State+".")
	}
	if _, err = tx.Exec(ctx, `UPDATE `+table+` SET state=$2,version=version+1,updated_at=now() WHERE id=$1`, id, to); err != nil {
		return s, err
	}
	return State{State: to, Version: s.Version + 1}, nil
}
