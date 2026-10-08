// Command migrate applies media_document_db Goose migrations:
//
//	migrate [up | down-to <version> | status | version]
//
// DATABASE_URL (or DATABASE_URL_FILE) must point at media_document_db.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/migrations"
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
	if err := migrate.Run(context.Background(), os.Args[1:], url, migrations.FS, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
