package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	ohttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func TestEveryDomainHandoffExampleMatchesWireSchema(t *testing.T) {
	page, err := os.ReadFile("../../../../contracts/schemas/pagination.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var ps map[string]any
	if err = json.Unmarshal(page, &ps); err != nil {
		t.Fatal(err)
	}
	for _, rt := range ohttp.DomainRoutes() {
		vars := map[string]bool{}
		item := postmanItem(rt, vars)
		c := schema.Components{}
		response := responseSchema(rt, c)
		c["Page"] = ps
		validate := func(s map[string]any, value any) {
			root := map[string]any{"components": map[string]any{"schemas": c}, "allOf": []any{s}}
			b, _ := json.Marshal(root)
			doc, e := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if e != nil {
				t.Fatal(e)
			}
			compiler := jsonschema.NewCompiler()
			compiler.AssertFormat()
			uri := "https://example.invalid/" + rt.Op + ".json"
			if e = compiler.AddResource(uri, doc); e != nil {
				t.Fatal(e)
			}
			sch, e := compiler.Compile(uri)
			if e != nil {
				t.Fatalf("%s compile: %v", rt.Op, e)
			}
			b, _ = json.Marshal(value)
			v, e := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if e != nil {
				t.Fatal(e)
			}
			if e = sch.Validate(v); e != nil {
				t.Errorf("%s example: %v", rt.Op, e)
			}
		}
		var responseBody any
		first := item.Response[0].(map[string]any)
		if err := json.Unmarshal([]byte(first["body"].(string)), &responseBody); err != nil {
			t.Fatal(err)
		}
		validate(response, responseBody)
		if item.Request.Body != nil {
			raw := item.Request.Body.Raw
			for name := range vars {
				raw = strings.ReplaceAll(raw, "{{"+name+"}}", "019a2b3c-0000-7000-8000-000000000001")
			}
			var request any
			if err := json.Unmarshal([]byte(raw), &request); err != nil {
				t.Fatal(err)
			}
			validate(c.Ref(rt.Req, true), request)
		}
		for _, q := range rt.Query {
			if !strings.Contains(item.Request.URL.Raw, q.Name+"=") {
				t.Errorf("%s query %s absent", rt.Op, q.Name)
			}
		}
		captures := inferredCaptures(rt)
		for k, v := range rt.Captures {
			captures[k] = v
		}
		for variable, path := range captures {
			s := response
			for _, part := range strings.Split(path, ".") {
				if ref, ok := s["$ref"].(string); ok {
					s = c[strings.TrimPrefix(ref, "#/components/schemas/")]
				}
				if _, err := strconv.Atoi(part); err == nil && s["type"] == "array" {
					s, _ = s["items"].(map[string]any)
					continue
				}
				props, _ := s["properties"].(map[string]any)
				s, _ = props[part].(map[string]any)
				if s == nil {
					t.Errorf("%s capture %s points outside response schema: %s", rt.Op, variable, path)
					break
				}
			}
		}
		if rt.Audience == ohttp.Service && !strings.HasPrefix(rt.Tag, "_") {
			t.Errorf("internal route in frontend folder: %s", rt.Op)
		}
		if len(item.Response) != 1+len(errorStatuses(rt)) {
			t.Fatal("missing error examples")
		}
	}
}

func TestAccountHandoffResponsesMatchOpenAPI(t *testing.T) {
	colBytes, err := os.ReadFile("../../../../" + Collection)
	if err != nil {
		t.Fatal(err)
	}
	var col struct {
		Item []map[string]any `json:"item"`
	}
	if err = json.Unmarshal(colBytes, &col); err != nil {
		t.Fatal(err)
	}
	var requests []map[string]any
	var walk func([]any)
	walk = func(items []any) {
		for _, raw := range items {
			it := raw.(map[string]any)
			if req, ok := it["request"].(map[string]any); ok && !strings.Contains(req["description"].(string), marker) {
				requests = append(requests, it)
			}
			if children, ok := it["item"].([]any); ok {
				walk(children)
			}
		}
	}
	for _, f := range col.Item {
		if f["name"] == OwnerFolder {
			walk(f["item"].([]any))
		}
	}
	for _, path := range []string{"public/openapi/individual-owner.yaml", "internal/openapi/individual-owner-service.yaml"} {
		b, err := os.ReadFile("../../../../contracts/" + path)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]any
		if err = yaml.Unmarshal(b, &root); err != nil {
			t.Fatal(err)
		}
		resolve := func(ref string) map[string]any {
			var v any = root
			for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				v = v.(map[string]any)[part]
			}
			return v.(map[string]any)
		}
		paths := root["paths"].(map[string]any)
		for _, it := range requests {
			req := it["request"].(map[string]any)
			url := req["url"].(map[string]any)
			parts := url["path"].([]any)
			ss := []string{}
			for _, p := range parts {
				ss = append(ss, strings.ReplaceAll(strings.ReplaceAll(p.(string), "{{", "{"), "}}", "}"))
			}
			p := paths["/"+strings.Join(ss, "/")]
			if p == nil {
				continue
			}
			op := p.(map[string]any)[strings.ToLower(req["method"].(string))].(map[string]any)
			responses := op["responses"].(map[string]any)
			for _, example := range it["response"].([]any) {
				ex := example.(map[string]any)
				code := strconv.Itoa(int(ex["code"].(float64)))
				rsp, ok := responses[code].(map[string]any)
				if !ok {
					t.Errorf("%s undocumented response %s", it["name"], code)
					continue
				}
				if ref, ok := rsp["$ref"].(string); ok {
					rsp = resolve(ref)
				}
				content, ok := rsp["content"].(map[string]any)
				if !ok {
					continue
				}
				s := content["application/json"].(map[string]any)["schema"]
				root["allOf"] = []any{s}
				b, _ := json.Marshal(root)
				doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
				if err != nil {
					t.Fatal(err)
				}
				compiler := jsonschema.NewCompiler()
				compiler.AssertFormat()
				uri := "https://contracts.cosmicforge.realestate/" + path
				if err = compiler.AddResource(uri, doc); err != nil {
					t.Fatal(err)
				}
				files, err := filepath.Glob("../../../../contracts/schemas/*.schema.json")
				if err != nil {
					t.Fatal(err)
				}
				for _, file := range files {
					b, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
					if err != nil {
						t.Fatal(err)
					}
					if err = compiler.AddResource("https://contracts.cosmicforge.realestate/schemas/"+filepath.Base(file), doc); err != nil {
						t.Fatal(err)
					}
				}
				sch, err := compiler.Compile(uri)
				if err != nil {
					t.Fatal(err)
				}
				body := regexp.MustCompile(`\{\{[a-z_]+\}\}`).ReplaceAllString(ex["body"].(string), "019a2b3c-0000-7000-8000-000000000001")
				value, err := jsonschema.UnmarshalJSON(strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				if err = sch.Validate(value); err != nil {
					t.Errorf("%s %s saved response: %v", it["name"], code, err)
				}
			}
		}
	}
}

func TestHandoffPreservesNonOwnerCollectionAndStableOperations(t *testing.T) {
	raw, err := os.ReadFile("../../../../" + Collection)
	if err != nil {
		t.Fatal(err)
	}
	var before, after collection
	if err = json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	generated, err := MergePostman(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(generated, &after); err != nil {
		t.Fatal(err)
	}
	for i, b := range before.Item {
		var f folder
		_ = json.Unmarshal(b, &f)
		if f.Name == OwnerFolder {
			continue
		}
		var x, y any
		_ = json.Unmarshal(b, &x)
		_ = json.Unmarshal(after.Item[i], &y)
		if !reflect.DeepEqual(x, y) {
			t.Errorf("unrelated folder changed: %s", f.Name)
		}
	}
	seen := map[string]bool{}
	for _, rt := range ohttp.DomainRoutes() {
		key := fmt.Sprintf("%s %s", rt.Method, rt.FullPath())
		if seen[key] || seen[rt.Op] {
			t.Errorf("duplicate operation: %s", rt.Op)
		}
		seen[key] = true
		seen[rt.Op] = true
	}
}
