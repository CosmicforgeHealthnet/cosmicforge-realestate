package testkit

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Route is an implemented HTTP route (method + chi-style path).
type Route struct {
	Method string
	Path   string
}

// OpenAPIOperations returns "METHOD path" for every operation in an OpenAPI
// document.
func OpenAPIOperations(t testing.TB, specPath string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read %s: %v", specPath, err)
	}
	var doc struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Fatalf("%s must be OpenAPI 3.1, got %q", specPath, doc.OpenAPI)
	}
	ops := map[string]bool{}
	for p, item := range doc.Paths {
		for m := range item {
			switch m {
			case "get", "post", "put", "patch", "delete", "head", "options":
				ops[strings.ToUpper(m)+" "+p] = true
			}
		}
	}
	return ops
}

// AssertRoutesMatchOpenAPI fails when an implemented route (under one of the
// prefixes) is undocumented, or a documented operation is not implemented.
func AssertRoutesMatchOpenAPI(t testing.TB, specPath string, routes []Route, prefixes ...string) {
	t.Helper()
	spec := OpenAPIOperations(t, specPath)
	impl := map[string]bool{}
	for _, r := range routes {
		p := strings.TrimSuffix(r.Path, "/")
		if p == "" {
			p = "/"
		}
		for _, pre := range prefixes {
			if strings.HasPrefix(p, pre) {
				impl[r.Method+" "+p] = true
			}
		}
	}
	var missing, extra []string
	for op := range impl {
		if !spec[op] {
			missing = append(missing, op)
		}
	}
	for op := range spec {
		path := strings.SplitN(op, " ", 2)[1]
		in := false
		for _, pre := range prefixes {
			if strings.HasPrefix(path, pre) {
				in = true
			}
		}
		if in && !impl[op] {
			extra = append(extra, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("implemented but not in %s:\n  %s", specPath, strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("documented in %s but not implemented:\n  %s", specPath, strings.Join(extra, "\n  "))
	}
}
