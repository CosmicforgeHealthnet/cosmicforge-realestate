// Command apidocgen regenerates the Sections 6–13 OpenAPI documents, event
// JSON Schemas and Postman folders from the route registry.
//
//	go run ./cmd/apidocgen            # write files
//	go run ./cmd/apidocgen -check     # fail when committed files are stale
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/apidoc"
)

func main() {
	root := flag.String("root", "../..", "repository root")
	check := flag.Bool("check", false, "verify instead of writing")
	flag.Parse()
	col, err := os.ReadFile(filepath.Join(*root, apidoc.Collection))
	if err != nil {
		fail(err)
	}
	files, err := apidoc.Generate(col)
	if err != nil {
		fail(err)
	}
	stale := 0
	for _, path := range apidoc.SortedKeys(files) {
		full := filepath.Join(*root, path)
		cur, _ := os.ReadFile(full)
		if bytes.Equal(cur, files[path]) {
			continue
		}
		if *check {
			fmt.Fprintln(os.Stderr, "stale:", path)
			stale++
			continue
		}
		if err := os.WriteFile(full, files[path], 0o644); err != nil {
			fail(err)
		}
		fmt.Println("wrote", path)
	}
	if stale > 0 {
		fail(fmt.Errorf("%d generated file(s) are stale; run go run ./cmd/apidocgen", stale))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "apidocgen:", err)
	os.Exit(1)
}
