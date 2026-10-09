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

// Options declares a service's database prerequisites.
type Options struct {
	// Extensions are the PostgreSQL extensions the migrations create.
	// "up" verifies them with Preflight before applying anything.
	Extensions []string
}

// Run implements a small CLI: up | down-to <version> | status | version.
func Run(ctx context.Context, args []string, url string, fsys fs.FS, out io.Writer) error {
	return RunWith(ctx, args, url, fsys, out, Options{})
}

// RunWith is Run with prerequisite checks. It adds the "preflight" command
// (checks only), and "up" refuses to start when a prerequisite is missing.
func RunWith(ctx context.Context, args []string, url string, fsys fs.FS, out io.Writer, opts Options) error {
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
	case "preflight":
		if err := Preflight(ctx, db, opts.Extensions); err != nil {
			return err
		}
		fmt.Fprintln(out, "preflight ok")
		return nil
	case "up":
		if err := Preflight(ctx, db, opts.Extensions); err != nil {
			return err
		}
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
	return fmt.Errorf("migrate: unknown command %q (up | preflight | down-to <v> | status | version)", cmd)
}

// ErrPrerequisite is wrapped by every failed preflight check.
var ErrPrerequisite = errors.New("migrate: database prerequisite missing")

// Preflight checks, without changing the database, that each extension is
// installed or can be created by the connected role: it must be available
// on the server and the role must be a superuser, or the extension must be
// trusted (PostgreSQL 13+) and the role must hold CREATE on the database and
// on the current schema. Otherwise it returns ErrPrerequisite with the exact
// statement an administrator must run first.
func Preflight(ctx context.Context, db *sql.DB, extensions []string) error {
	for _, ext := range extensions {
		var (
			installed, available, trusted, super, dbCreate, schemaCreate bool
			database, role                                               string
			version                                                      int
		)
		err := db.QueryRowContext(ctx, `SELECT
			EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1),
			EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = $1),
			coalesce((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false),
			has_database_privilege(current_database(), 'CREATE'),
			coalesce(has_schema_privilege(current_schema(), 'CREATE'), false),
			current_database(), current_user, current_setting('server_version_num')::int`, ext).
			Scan(&installed, &available, &super, &dbCreate, &schemaCreate, &database, &role, &version)
		if err != nil {
			return fmt.Errorf("migrate: preflight query: %w", err)
		}
		if installed {
			continue
		}
		admin := fmt.Sprintf("an administrator must run `CREATE EXTENSION IF NOT EXISTS %s;` in database %q before migrating", ext, database)
		if !available {
			return fmt.Errorf("%w: extension %s is not available on this PostgreSQL server (install the server's contrib package or allow it "+
				"in the managed database's extension list); then %s", ErrPrerequisite, ext, admin)
		}
		if super {
			continue
		}
		if version >= 130000 {
			if err := db.QueryRowContext(ctx, `SELECT coalesce(bool_or(v.trusted), false) FROM pg_available_extension_versions v
				JOIN pg_available_extensions a ON a.name = v.name AND a.default_version = v.version WHERE v.name = $1`, ext).Scan(&trusted); err != nil {
				return fmt.Errorf("migrate: preflight query: %w", err)
			}
		}
		if trusted && dbCreate && schemaCreate {
			continue
		}
		return fmt.Errorf("%w: role %q cannot create extension %s in database %q (needs superuser, or CREATE on the database and schema for a "+
			"trusted extension; trusted=%t database_create=%t schema_create=%t); %s", ErrPrerequisite, role, ext, database, trusted, dbCreate, schemaCreate, admin)
	}
	return nil
}
