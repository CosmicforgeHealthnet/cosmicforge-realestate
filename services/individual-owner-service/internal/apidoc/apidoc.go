// Package apidoc generates the Sections 6–13 API contracts from the route
// registry and the event catalogue:
//
//	contracts/public/openapi/individual-owner-domain.yaml
//	contracts/internal/openapi/individual-owner-service-domain.yaml
//	contracts/events/owners/<event_type>.schema.json
//	postman/CosmicForge_RealEstate.postman_collection.json (folders under "02 Individual Owner")
//
// The Section 5 (authentication) documents remain hand-written. A test fails
// when the committed files differ from the generated output.
package apidoc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	ohttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
)

// Paths relative to the repository root.
const (
	PublicSpec   = "contracts/public/openapi/individual-owner-domain.yaml"
	InternalSpec = "contracts/internal/openapi/individual-owner-service-domain.yaml"
	EventsDir    = "contracts/events/owners/"
	Collection   = "postman/CosmicForge_RealEstate.postman_collection.json"
	OwnerFolder  = "02 Individual Owner"
	marker       = "Generated from the individual-owner-service route registry (cmd/apidocgen); do not edit by hand."
)

var paramRE = regexp.MustCompile(`\{([a-z_]+)\}`)

// OpenAPI renders the document of one audience.
func OpenAPI(aud ohttp.Audience) ([]byte, error) {
	comps := schema.Components{}
	paths := map[string]any{}
	for _, rt := range ohttp.DomainRoutes() {
		if rt.Audience != aud {
			continue
		}
		item, _ := paths[rt.FullPath()].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[rt.FullPath()] = item
		}
		item[strings.ToLower(rt.Method)] = operation(rt, comps)
	}
	comps["Page"] = map[string]any{"$ref": "../../schemas/pagination.schema.json"}
	security := map[string]any{"OwnerBearer": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "owner at+jwt (EdDSA)"}}
	title, desc, server, sec := "Individual Owner — domain API (Sections 6–13)", publicDescription, "https://api.realestate.cosmicforge.example", "OwnerBearer"
	if aud == ohttp.Service {
		security = map[string]any{"ServiceBearer": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "service+jwt (EdDSA)"}}
		title, desc, server, sec = "individual-owner-service — internal domain API (Section 9)", internalDescription, "http://individual-owner-service:8080", "ServiceBearer"
	}
	schemas := map[string]any{}
	for k, v := range comps {
		schemas[k] = v
	}
	doc := map[string]any{
		"openapi":  "3.1.0",
		"info":     map[string]any{"title": title, "version": "1.0.0", "description": desc},
		"servers":  []any{map[string]any{"url": server}},
		"security": []any{map[string]any{sec: []any{}}},
		"paths":    paths,
		"components": map[string]any{
			"securitySchemes": security,
			"schemas":         schemas,
			"responses": map[string]any{"Error": map[string]any{"description": "Platform error envelope.",
				"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "../../schemas/error-envelope.schema.json"}}}}},
		},
	}
	var buf bytes.Buffer
	buf.WriteString("# " + marker + "\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

const publicDescription = `Owner-facing domain API of individual-owner-service (Sections 6–13 of the Individual Owner
Backend Implementation Specification v1.0), reached only through the API Gateway under
/api/v1/owner/*. Authentication, sessions and account security (Section 5) are documented in
individual-owner.yaml.

Conventions
* Authorization: Bearer <owner access token>. Every property-scoped operation is authorized
  against the caller's relationship to that exact property (owner, co-owner, authorized
  representative with explicit capabilities and validity window); unrelated properties answer 404.
* Commands that create, publish, approve, withdraw, decide or have financial/external side effects
  require Idempotency-Key: same key + same body replays the stored response
  (Idempotent-Replayed: true); same key + different body → 409 IDEMPOTENCY_CONFLICT.
* PATCH/PUT require If-Match with the quoted resource version from GET (428 when missing,
  409 VERSION_CONFLICT when stale). Singleton resources accept If-Match: "0" to create.
* Operations marked step-up need recent session authentication (403 STEP_UP_REQUIRED).
  Sensitive financial, ownership and access actions additionally require enrolled MFA and an
  independent recent second factor; a password or phone OTP alone does not satisfy them.
* Collections use cursor pagination: {"data": [...], "page": {"next_cursor", "has_more"}}.
* Features whose external dependency is not configured or not implemented answer
  503 FEATURE_UNAVAILABLE (never a simulated success).`

const internalDescription = `Service-to-service domain API of individual-owner-service (Section 9). Authorization:
Bearer <service JWT> (aud individual-owner-service). A valid token is not sufficient: each operation
has a capability allow-list of calling services (and delegated actor domains), evaluated by the
service's policy engine. No other service ever reads individual_owner_db directly.`

func operation(rt ohttp.Route, comps schema.Components) map[string]any {
	op := map[string]any{"operationId": rt.Op, "summary": rt.Summary, "tags": []any{rt.Tag}}
	desc := rt.Doc
	if rt.StepUp {
		desc = strings.TrimSpace("Step-up required. " + desc)
	}
	if desc != "" {
		op["description"] = desc
	}
	var params []any
	for _, m := range paramRE.FindAllStringSubmatch(rt.Path, -1) {
		params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}})
	}
	for _, q := range rt.Query {
		s := map[string]any{"type": "string"}
		if len(q.Enum) > 0 {
			s["enum"] = toAny(q.Enum)
		}
		p := map[string]any{"name": q.Name, "in": "query", "schema": s}
		if q.Required {
			p["required"] = true
		}
		if q.Doc != "" {
			p["description"] = q.Doc
		}
		params = append(params, p)
	}
	if rt.List {
		params = append(params, map[string]any{"name": "cursor", "in": "query", "schema": map[string]any{"type": "string"}},
			map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 20}})
	}
	if rt.Idempotent {
		params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "minLength": 1, "maxLength": 255}})
	}
	if rt.IfMatch {
		params = append(params, map[string]any{"name": "If-Match", "in": "header", "required": true, "schema": map[string]any{"type": "string"},
			"description": `Quoted resource version, e.g. "3"`})
	}
	if len(params) > 0 {
		op["parameters"] = params
	}
	if rt.Req != nil && rt.Req != reflect.TypeFor[ohttp.Empty]() {
		op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": comps.Ref(rt.Req, true)}}}
	}
	body := responseSchema(rt, comps)
	responses := map[string]any{strconv.Itoa(rt.Status): map[string]any{"description": http.StatusText(rt.Status),
		"content": map[string]any{"application/json": map[string]any{"schema": body}}}}
	for _, c := range errorStatuses(rt) {
		responses[strconv.Itoa(c)] = map[string]any{"$ref": "#/components/responses/Error"}
	}
	op["responses"] = responses
	sec := "OwnerBearer"
	if rt.Audience == ohttp.Service {
		sec = "ServiceBearer"
	}
	op["security"] = []any{map[string]any{sec: []any{}}}
	return op
}

func responseSchema(rt ohttp.Route, comps schema.Components) map[string]any {
	var body map[string]any
	if rt.List {
		body = map[string]any{"type": "object", "required": []any{"data", "page"}, "properties": map[string]any{
			"data": map[string]any{"type": "array", "items": comps.Ref(rt.Resp, false)}, "page": map[string]any{"$ref": "#/components/schemas/Page"}}}
	} else {
		body = map[string]any{"type": "object", "required": []any{"data"}, "properties": map[string]any{"data": comps.Ref(rt.Resp, false)}}
	}
	return body
}

func errorStatuses(rt ohttp.Route) []int {
	codes := []int{400, 401, 403, 404}
	if rt.Idempotent || rt.IfMatch {
		codes = append(codes, 409)
	}
	if rt.IfMatch {
		codes = append(codes, 428)
	}
	codes = append(codes, rt.Errors...)
	seen := map[int]bool{}
	unique := []int{}
	for _, c := range codes {
		if !seen[c] {
			unique = append(unique, c)
			seen[c] = true
		}
	}
	return unique
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// EventSchemas renders one JSON Schema per produced event type.
func EventSchemas() (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, e := range usecases.ProducedEvents() {
		comps := schema.Components{}
		payload := comps.Ref(e.Payload, false)
		defs := map[string]any{}
		for k, v := range comps {
			defs[k] = rewriteRefs(v)
		}
		doc := map[string]any{
			"$schema":     "https://json-schema.org/draft/2020-12/schema",
			"$id":         "https://contracts.cosmicforge.realestate/events/owners/" + e.Type + ".schema.json",
			"title":       e.Type,
			"description": e.Description + " Producer: individual-owner-service. Consumers: " + e.Consumers + ". Aggregate: " + e.Aggregate + ". " + marker,
			"allOf":       []any{map[string]any{"$ref": "../../schemas/event-envelope.schema.json"}},
			"properties": map[string]any{
				"event_type":     map[string]any{"const": e.Type},
				"producer":       map[string]any{"const": "individual-owner-service"},
				"aggregate_type": map[string]any{"const": e.Aggregate},
				"payload":        rewriteRefs(payload),
			},
			"$defs": defs,
		}
		b, err := marshalJSON(doc)
		if err != nil {
			return nil, err
		}
		out[EventsDir+e.Type+".schema.json"] = b
	}
	return out, nil
}

func rewriteRefs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, val := range t {
			if s, ok := val.(string); ok && k == "$ref" && strings.HasPrefix(s, "#/components/schemas/") {
				m[k] = "#/$defs/" + strings.TrimPrefix(s, "#/components/schemas/")
				continue
			}
			m[k] = rewriteRefs(val)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = rewriteRefs(t[i])
		}
		return out
	}
	return v
}

func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Generate returns every generated file keyed by repository-relative path.
// collection is the current Postman collection (its non-generated content is preserved).
func Generate(collection []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	pub, err := OpenAPI(ohttp.Owner)
	if err != nil {
		return nil, err
	}
	out[PublicSpec] = pub
	in, err := OpenAPI(ohttp.Service)
	if err != nil {
		return nil, err
	}
	out[InternalSpec] = in
	ev, err := EventSchemas()
	if err != nil {
		return nil, err
	}
	for k, v := range ev {
		out[k] = v
	}
	col, err := MergePostman(collection)
	if err != nil {
		return nil, err
	}
	out[Collection] = col
	return out, nil
}

// SortedKeys returns map keys in order (stable output for callers).
func SortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func init() {
	seen := map[string]bool{}
	for _, rt := range ohttp.DomainRoutes() {
		if seen[rt.Op] {
			panic("apidoc: duplicate operationId " + rt.Op)
		}
		seen[rt.Op] = true
		if rt.Summary == "" || rt.Tag == "" {
			panic("apidoc: route without summary/tag " + rt.Op)
		}
	}
}
