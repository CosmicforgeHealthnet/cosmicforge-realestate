package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
)

func main() {
	u := os.Getenv("DATABASE_URL")
	if u == "" {
		if p := os.Getenv("DATABASE_URL_FILE"); p != "" {
			b, err := os.ReadFile(p)
			if err != nil {
				fmt.Fprintln(os.Stderr, "DATABASE_URL_FILE could not be read")
				os.Exit(1)
			}
			u = strings.TrimSpace(string(b))
		}
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Path != "/business_operations_db" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL must point to business_operations_db")
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "down-to" {
		fmt.Fprintln(os.Stderr, "down-to is disabled for this service; use a reviewed forward migration")
		os.Exit(1)
	}
	if err := migrate.Run(context.Background(), os.Args[1:], u, migrations.FS, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
