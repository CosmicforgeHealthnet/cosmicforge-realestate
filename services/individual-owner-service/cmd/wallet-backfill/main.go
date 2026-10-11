// wallet-backfill provisions structures through the existing delivery queue.
// Default dry run; resumable --after UUIDv7. No opening balance or external API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
)

func main() {
	dry := flag.Bool("dry-run", true, "report missing durable registrations without writing")
	after := flag.String("after", "", "last successfully scanned account UUIDv7")
	limit := flag.Int("limit", 100, "batch size 1–1000")
	flag.Parse()
	cursor := ids.Nil
	if *after != "" {
		v, e := ids.ParseV7(*after)
		if e != nil {
			fail()
		}
		cursor = v
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		if p := os.Getenv("DATABASE_URL_FILE"); p != "" {
			b, e := os.ReadFile(p)
			if e != nil {
				fail()
			}
			url = strings.TrimSpace(string(b))
		}
	}
	pool, e := pgxpool.New(context.Background(), url)
	if e != nil {
		fail()
	}
	defer pool.Close()
	out, e := usecases.QueueWalletBackfill(context.Background(), pool, cursor, *limit, *dry)
	if e != nil {
		fail()
	}
	if e = json.NewEncoder(os.Stdout).Encode(out); e != nil {
		fail()
	}
}
func fail() {
	fmt.Fprintln(os.Stderr, "Wallet backfill failed; sensitive database details suppressed. Resolve the failed batch and retry the same cursor.")
	os.Exit(1)
}
