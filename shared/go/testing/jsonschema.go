package testkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateJSONSchema validates doc against the JSON Schema file at
// schemaPath (relative $refs resolve against the file's directory).
func ValidateJSONSchema(t testing.TB, schemaPath string, doc []byte) {
	t.Helper()
	abs, err := filepath.Abs(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if root := contractsRoot(abs); root != "" {
		c.UseLoader(jsonschema.SchemeURLLoader{
			"file":  jsonschema.FileLoader{},
			"https": contractsLoader{root: root},
		})
	}
	sch, err := c.Compile(abs)
	if err != nil {
		t.Fatalf("compile %s: %v", schemaPath, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if err := sch.Validate(inst); err != nil {
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, doc, "", "  ")
		t.Fatalf("%s does not match %s:\n%v\n%s", "document", filepath.Base(schemaPath), err, pretty.String())
	}
}

// ContractsIDPrefix is the $id namespace of repository contracts.
const ContractsIDPrefix = "https://contracts.cosmicforge.realestate/"

// contractsLoader resolves contract $ids to files under contracts/.
type contractsLoader struct{ root string }

func (l contractsLoader) Load(url string) (any, error) {
	if !strings.HasPrefix(url, ContractsIDPrefix) {
		return nil, fmt.Errorf("refusing to load remote schema %s", url)
	}
	f, err := os.Open(filepath.Join(l.root, filepath.FromSlash(strings.TrimPrefix(url, ContractsIDPrefix))))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jsonschema.UnmarshalJSON(f)
}

func contractsRoot(path string) string {
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "contracts" {
			return dir
		}
	}
	return ""
}
