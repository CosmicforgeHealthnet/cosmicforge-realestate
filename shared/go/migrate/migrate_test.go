package migrate_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	. "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestRoundTripAndCLI(t *testing.T) {
	fsys := os.DirFS("testdata/migrations")
	testkit.MigrationRoundTrip(t, fsys)

	db := testkit.NewDatabase(t, nil)
	ctx := context.Background()
	var out bytes.Buffer
	if err := Run(ctx, []string{"up"}, db.URL, fsys, &out); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, []string{"version"}, db.URL, fsys, &out); err != nil || !strings.Contains(out.String(), "2") {
		t.Fatalf("version: %v %q", err, out.String())
	}
	out.Reset()
	if err := Run(ctx, []string{"status"}, db.URL, fsys, &out); err != nil || !strings.Contains(out.String(), "applied") {
		t.Fatalf("status: %v %q", err, out.String())
	}
	if err := Run(ctx, []string{"down-to", "1"}, db.URL, fsys, &out); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, []string{"bogus"}, db.URL, fsys, &out); err == nil {
		t.Fatal("unknown command accepted")
	}
	if err := Run(ctx, nil, "", fsys, &out); err == nil {
		t.Fatal("missing URL accepted")
	}
}
