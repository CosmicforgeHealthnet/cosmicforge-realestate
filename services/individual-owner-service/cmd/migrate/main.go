// Command migrate applies individual_owner_db Goose migrations:
//
//	migrate [up | preflight | down-to <version> | status | version]
//
// DATABASE_URL (or DATABASE_URL_FILE) must point at individual_owner_db.
// "up" first verifies, without changing the database, that the required
// extensions are installed or creatable by the connected role, and refuses
// to apply anything otherwise; "preflight" runs only that check.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		if p := os.Getenv("DATABASE_URL_FILE"); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				url = strings.TrimSpace(string(b))
			}
		}
	}
	opts := migrate.Options{Extensions: migrations.RequiredExtensions}
	if err := migrate.RunWith(context.Background(), os.Args[1:], url, migrations.FS, os.Stdout, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
