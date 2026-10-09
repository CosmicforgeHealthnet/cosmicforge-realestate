package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	ohttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

type collection struct {
	Info     json.RawMessage   `json:"info"`
	Event    json.RawMessage   `json:"event"`
	Variable []variable        `json:"variable"`
	Item     []json.RawMessage `json:"item"`
}

type variable struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

type folder struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Item        []json.RawMessage `json:"item"`
}

type kv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type pmURL struct {
	Raw  string   `json:"raw"`
	Host []string `json:"host"`
	Path []string `json:"path"`
}

type pmBody struct {
	Mode    string `json:"mode"`
	Raw     string `json:"raw"`
	Options struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
}

type pmRequest struct {
	Method      string  `json:"method"`
	Header      []kv    `json:"header"`
	URL         pmURL   `json:"url"`
	Description string  `json:"description"`
	Body        *pmBody `json:"body,omitempty"`
}

type pmScript struct {
	Type string   `json:"type"`
	Exec []string `json:"exec"`
}

type pmEvent struct {
	Listen string   `json:"listen"`
	Script pmScript `json:"script"`
}

type pmItem struct {
	Name     string    `json:"name"`
	Event    []pmEvent `json:"event"`
	Request  pmRequest `json:"request"`
	Response []any     `json:"response"`
}

// versionVar names the Postman variable holding a route's resource version.
func versionVar(rt ohttp.Route) string {
	ms := paramRE.FindAllStringSubmatch(rt.Path, -1)
	segs := strings.Split(strings.Trim(rt.Path, "/"), "/")
	last := segs[len(segs)-1]
	if len(ms) > 0 && strings.HasSuffix(last, "}") {
		return strings.TrimSuffix(ms[len(ms)-1][1], "_id") + "_version"
	}
	return strings.ReplaceAll(last, "-", "_") + "_version"
}

func hasVersion(t reflect.Type) bool {
	if t == nil {
		return false
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	f, ok := t.FieldByName("Version")
	return ok && f.Type.Kind() == reflect.Int64
}

func postmanItem(rt ohttp.Route, vars map[string]bool) pmItem {
	host := "{{base_url}}"
	auth := "Bearer {{individual_owner_token}}"
	if rt.Audience == ohttp.Service {
		host, auth = "{{individual_owner_service_url}}", "Bearer {{service_token}}"
		vars["service_token"] = true
	}
	path := paramRE.ReplaceAllStringFunc(rt.FullPath(), func(m string) string {
		name := strings.Trim(m, "{}")
		vars[name] = true
		return "{{" + name + "}}"
	})
	raw := host + path
	if rt.List {
		raw += "?limit=20"
	}
	headers := []kv{{"X-Request-ID", "{{request_id}}"}, {"Authorization", auth}}
	var body *pmBody
	if rt.Req != nil && rt.Req != reflect.TypeFor[ohttp.Empty]() {
		headers = append(headers, kv{"Content-Type", "application/json"})
		ex, _ := json.MarshalIndent(example(rt.Req, "", vars, 0), "", "  ")
		body = &pmBody{Mode: "raw", Raw: string(ex)}
		body.Options.Raw.Language = "json"
	}
	if rt.Idempotent {
		headers = append(headers, kv{"Idempotency-Key", "{{idempotency_key}}"})
	}
	if rt.IfMatch {
		v := versionVar(rt)
		vars[v] = true
		headers = append(headers, kv{"If-Match", `"{{` + v + `}}"`})
	}
	desc := rt.Summary + "."
	if rt.StepUp {
		desc += " Step-up required (POST /auth/step-up first)."
	}
	if rt.Doc != "" {
		desc += "\n\n" + rt.Doc
	}
	desc += "\n\n" + marker
	exec := []string{
		fmt.Sprintf("pm.test('status is %d', () => pm.response.to.have.status(%d));", rt.Status, rt.Status),
		"pm.test('request id echoed', () => pm.expect(pm.response.headers.get('X-Request-ID')).to.be.a('string'));",
	}
	captures := map[string]string{}
	for k, v := range rt.Captures {
		captures[k] = v
	}
	if (rt.Method == "GET" && !rt.List || rt.IfMatch) && hasVersion(rt.Resp) {
		captures[versionVar(rt)] = "data.version"
	}
	if len(captures) > 0 {
		exec = append(exec, "const pick = (o, p) => p.split('.').reduce((a, k) => (a == null ? undefined : a[k]), o);",
			"if (pm.response.code < 300) {", "  const body = pm.response.json();")
		keys := make([]string, 0, len(captures))
		for k := range captures {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			vars[k] = true
			exec = append(exec, fmt.Sprintf("  if (pick(body, '%s') !== undefined) pm.collectionVariables.set('%s', String(pick(body, '%s')));", captures[k], k, captures[k]))
		}
		exec = append(exec, "}")
	}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return pmItem{Name: rt.Summary, Event: []pmEvent{{Listen: "test", Script: pmScript{Type: "text/javascript", Exec: exec}}},
		Request:  pmRequest{Method: rt.Method, Header: headers, URL: pmURL{Raw: raw, Host: []string{host}, Path: segs}, Description: desc, Body: body},
		Response: []any{}}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

var (
	idType    = reflect.TypeFor[ids.ID]()
	timeType  = reflect.TypeFor[time.Time]()
	moneyType = reflect.TypeFor[money.Money]()
)

// example builds a minimal valid request body: required fields, fields with
// an `ex` example, and nested required objects.
func example(t reflect.Type, name string, vars map[string]bool, depth int) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case idType:
		if strings.HasSuffix(name, "_id") {
			vars[name] = true
			return "{{" + name + "}}"
		}
		return "019a2b3c-0000-7000-8000-000000000001"
	case timeType:
		return "2026-12-01T10:00:00Z"
	case moneyType:
		return map[string]any{"amount_minor": 150000000, "currency": "NGN"}
	}
	switch t.Kind() {
	case reflect.Struct:
		obj := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			jn := schema.JSONName(f)
			if jn == "-" {
				continue
			}
			if f.Anonymous && jn == "" {
				if m, ok := example(f.Type, "", vars, depth).(map[string]any); ok {
					for k, v := range m {
						obj[k] = v
					}
				}
				continue
			}
			r := schema.Parse(f.Tag.Get("v"))
			if !r.Required && f.Tag.Get("ex") == "" {
				continue
			}
			obj[jn] = fieldExample(f, jn, r, vars, depth)
		}
		return obj
	case reflect.Slice:
		return []any{example(t.Elem(), name, vars, depth+1)}
	}
	return scalar(t, name, schema.Rules{})
}

func fieldExample(f reflect.StructField, name string, r schema.Rules, vars map[string]bool, depth int) any {
	if ex := f.Tag.Get("ex"); ex != "" {
		switch f.Type.Kind() {
		case reflect.Int, reflect.Int32, reflect.Int64:
			n, _ := strconv.Atoi(ex)
			return n
		}
		return ex
	}
	t := f.Type
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String {
		return []any{scalar(t.Elem(), name, r)}
	}
	if t.Kind() == reflect.Struct || t.Kind() == reflect.Slice || t == idType {
		return example(t, name, vars, depth+1)
	}
	return scalar(t, name, r)
}

func scalar(t reflect.Type, name string, r schema.Rules) any {
	if len(r.Enum) > 0 {
		return r.Enum[0]
	}
	switch t.Kind() {
	case reflect.String:
		switch r.Format {
		case "email":
			return "owner.example@example.com"
		case "e164":
			return "+2348012345678"
		case "date":
			return "2026-12-01"
		case "currency":
			return "NGN"
		case "country":
			return "NG"
		case "uri":
			return "https://example.com"
		}
		if r.Pattern != "" {
			return "VALUE"
		}
		switch {
		case strings.Contains(name, "title"):
			return "3-bedroom flat in Lekki"
		case strings.Contains(name, "name"):
			return "Sample name"
		case strings.Contains(name, "line1"):
			return "12 Admiralty Way"
		case strings.Contains(name, "city"):
			return "Lagos"
		case strings.Contains(name, "state"):
			return "Lagos"
		case strings.Contains(name, "timezone"):
			return "Africa/Lagos"
		}
		return "string"
	case reflect.Bool:
		return false
	case reflect.Int, reflect.Int32, reflect.Int64:
		if r.Min != nil && *r.Min > 0 {
			return *r.Min
		}
		return 1
	case reflect.Float64:
		return 6.4281
	}
	return nil
}

// MergePostman regenerates the registry folders under "02 Individual Owner",
// keeping every hand-written item and folder untouched.
func MergePostman(raw []byte) ([]byte, error) {
	var col collection
	if err := json.Unmarshal(raw, &col); err != nil {
		return nil, err
	}
	vars := map[string]bool{}
	var tags []string
	byTag := map[string][]json.RawMessage{}
	for _, rt := range ohttp.DomainRoutes() {
		b, err := rawJSON(postmanItem(rt, vars))
		if err != nil {
			return nil, err
		}
		if _, ok := byTag[rt.Tag]; !ok {
			tags = append(tags, rt.Tag)
		}
		byTag[rt.Tag] = append(byTag[rt.Tag], b)
	}
	found := false
	for i, it := range col.Item {
		var top folder
		if err := json.Unmarshal(it, &top); err != nil || top.Name != OwnerFolder {
			continue
		}
		found = true
		var subs []json.RawMessage
		seen := map[string]bool{}
		for _, sub := range top.Item {
			var f folder
			if err := json.Unmarshal(sub, &f); err != nil || f.Item == nil {
				subs = append(subs, sub)
				continue
			}
			kept := []json.RawMessage{}
			for _, item := range f.Item {
				if !bytes.Contains(item, []byte(jsonString(marker))) {
					kept = append(kept, item)
				}
			}
			kept = append(kept, byTag[f.Name]...)
			seen[f.Name] = true
			if len(kept) == 0 && f.Description == marker {
				continue
			}
			f.Item = kept
			b, err := rawJSON(f)
			if err != nil {
				return nil, err
			}
			subs = append(subs, b)
		}
		for _, tag := range tags {
			if seen[tag] {
				continue
			}
			b, err := rawJSON(folder{Name: tag, Description: marker, Item: byTag[tag]})
			if err != nil {
				return nil, err
			}
			subs = append(subs, b)
		}
		top.Item = subs
		top.Description = ownerFolderDescription
		b, err := rawJSON(top)
		if err != nil {
			return nil, err
		}
		col.Item[i] = b
	}
	if !found {
		return nil, fmt.Errorf("apidoc: folder %q not found", OwnerFolder)
	}
	have := map[string]bool{}
	for _, v := range col.Variable {
		have[v.Key] = true
	}
	var names []string
	for v := range vars {
		if !have[v] {
			names = append(names, v)
		}
	}
	sortStrings(names)
	for _, n := range names {
		col.Variable = append(col.Variable, variable{Key: n, Value: "", Type: "string"})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(col); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func jsonString(s string) string {
	b, _ := rawJSON(s)
	return strings.Trim(string(b), `"`)
}

// rawJSON marshals without HTML escaping (the collection is human-edited JSON).
func rawJSON(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

const ownerFolderDescription = "Individual Owner (individual-owner-service). Frontend requests go through the gateway with {{individual_owner_token}}; " +
	"_Internal and _Admin requests call the service directly with a service JWT ({{service_token}}). Implemented: Section 5 account security " +
	"(Frontend, _Admin) and the Sections 6–13 domain API generated from the route registry (profile, banking & tax, properties, ownership & " +
	"verification and later contexts). Features whose dependency is not configured answer 503 FEATURE_UNAVAILABLE."
