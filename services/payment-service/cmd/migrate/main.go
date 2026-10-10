package main

import (
	"context"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	"os"
	"strings"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" && os.Getenv("DATABASE_URL_FILE") != "" {
		if b, e := os.ReadFile(os.Getenv("DATABASE_URL_FILE")); e == nil {
			dbURL = strings.TrimSpace(string(b))
		}
	}
	if e := migrate.Run(context.Background(), os.Args[1:], dbURL, migrations.FS, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
