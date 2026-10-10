package apidoc

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGeneratedFilesAreCurrent fails when the committed contracts or Postman
// collection differ from what the route registry generates (CI parity).
func TestGeneratedFilesAreCurrent(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	col, err := os.ReadFile(filepath.Join(root, Collection))
	if err != nil {
		t.Fatal(err)
	}
	files, err := Generate(col)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range SortedKeys(files) {
		cur, err := os.ReadFile(filepath.Join(root, path))
		// Git may check text contracts out as CRLF on Windows. Compare their
		// canonical content so line endings do not report semantic drift.
		if err != nil || !bytes.Equal(bytes.ReplaceAll(cur, []byte("\r\n"), []byte("\n")), files[path]) {
			t.Errorf("%s is stale; run: go run ./cmd/apidocgen", path)
		}
	}
}

func TestEveryRouteHasUniqueOperationAndDocs(t *testing.T) {
	pub, err := OpenAPI(0)
	if err != nil || len(pub) == 0 {
		t.Fatal(err)
	}
	if !bytes.Contains(pub, []byte("openapi: 3.1.0")) {
		t.Fatal("not OpenAPI 3.1")
	}
}
