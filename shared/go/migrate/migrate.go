// Package migrate runs a service's Goose migrations (embedded SQL files).
// Each service owns its migrations directory and applies it only to its own
// database.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
	"github.com/pressly/goose/v3"
)

// Open opens a database/sql handle for migrations.
func Open(url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, errors.New("migrate: invalid database URL")
	}
	return db, nil
}

func provider(db *sql.DB, fsys fs.FS) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, db, fsys)
}

// Up applies all pending migrations.
func Up(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	p, err := provider(db, fsys)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// DownTo rolls back to version (0 = empty schema).
func DownTo(ctx context.Context, db *sql.DB, fsys fs.FS, version int64) error {
	p, err := provider(db, fsys)
	if err != nil {
		return err
	}
	_, err = p.DownTo(ctx, version)
	return err
}

// Version returns the current applied version.
func Version(ctx context.Context, db *sql.DB, fsys fs.FS) (int64, error) {
	p, err := provider(db, fsys)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}

// Status writes migration status lines to w.
func Status(ctx context.Context, db *sql.DB, fsys fs.FS, w io.Writer) error {
	p, err := provider(db, fsys)
	if err != nil {
		return err
	}
	st, err := p.Status(ctx)
	if err != nil {
		return err
	}
	for _, s := range st {
		applied := "pending"
		if s.State == goose.StateApplied {
			applied = "applied " + s.AppliedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		fmt.Fprintf(w, "%05d %-50s %s\n", s.Source.Version, s.Source.Path, applied)
	}
	return nil
}

// Run implements a small CLI: up | down-to <version> | status | version.
func Run(ctx context.Context, args []string, url string, fsys fs.FS, out io.Writer) error {
	if url == "" {
		return errors.New("migrate: DATABASE_URL is required")
	}
	db, err := Open(url)
	if err != nil {
		return err
	}
	defer db.Close()
	cmd := "up"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "up":
		return Up(ctx, db, fsys)
	case "down-to":
		if len(args) != 2 {
			return errors.New("usage: down-to <version>")
		}
		v, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || v < 0 {
			return errors.New("migrate: invalid version")
		}
		return DownTo(ctx, db, fsys, v)
	case "status":
		return Status(ctx, db, fsys, out)
	case "version":
		v, err := Version(ctx, db, fsys)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, v)
		return nil
	}
	return fmt.Errorf("migrate: unknown command %q (up | down-to <v> | status | version)", cmd)
}
