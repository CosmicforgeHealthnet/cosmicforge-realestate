package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// Components collects named object schemas for an OpenAPI document.
type Components map[string]map[string]any

// Ref returns the schema of t, registering named structs as components.
// request selects request semantics (required from `v:"required"`);
// response schemas mark every non-pointer, non-omitempty field required.
func (c Components) Ref(t reflect.Type, request bool) map[string]any {
	return c.of(t, request, Rules{})
}

func (c Components) of(t reflect.Type, request bool, r Rules) map[string]any {
	nullable := false
	if t.Kind() == reflect.Pointer {
		t, nullable = t.Elem(), true
	}
	s := c.base(t, request, r)
	if nullable && !request {
		if typ, ok := s["type"].(string); ok {
			s["type"] = []any{typ, "null"}
		} else if ref, ok := s["$ref"]; ok {
			s = map[string]any{"oneOf": []any{map[string]any{"$ref": ref}, map[string]any{"type": "null"}}}
		}
	}
	return s
}

func constrain(s map[string]any, r Rules, kind string) map[string]any {
	switch kind {
	case "string":
		if r.Min != nil {
			s["minLength"] = *r.Min
		}
		if r.Max != nil {
			s["maxLength"] = *r.Max
		}
		if len(r.Enum) > 0 {
			s["enum"] = toAny(r.Enum)
		}
		if r.Pattern != "" {
			s["pattern"] = "^(?:" + r.Pattern + ")$"
		}
		switch r.Format {
		case "email", "date", "uri":
			s["format"] = r.Format
		case "e164":
			s["pattern"] = `^\+[1-9][0-9]{7,14}$`
		case "currency":
			s["pattern"] = "^[A-Z]{3}$"
		case "country":
			s["pattern"] = "^[A-Z]{2}$"
		}
	case "integer", "number":
		if r.Min != nil {
			s["minimum"] = *r.Min
		}
		if r.Max != nil {
			s["maximum"] = *r.Max
		}
	case "array":
		if r.Min != nil {
			s["minItems"] = *r.Min
		}
		if r.Max != nil {
			s["maxItems"] = *r.Max
		}
		if r.Required && r.Min == nil {
			s["minItems"] = 1
		}
	}
	return s
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func (c Components) base(t reflect.Type, request bool, r Rules) map[string]any {
	switch t {
	case idType:
		return map[string]any{"type": "string", "format": "uuid"}
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case moneyType:
		c["Money"] = map[string]any{"type": "object", "required": []any{"amount_minor", "currency"}, "additionalProperties": false,
			"properties": map[string]any{
				"amount_minor": map[string]any{"type": "integer", "description": "Integer amount in the currency's minor unit."},
				"currency":     map[string]any{"type": "string", "pattern": "^[A-Z]{3}$"},
			}}
		return map[string]any{"$ref": "#/components/schemas/Money"}
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{"description": "Embedded JSON document"}
	}
	if t == reflect.TypeFor[time.Duration]() {
		return map[string]any{"type": "integer"}
	}
	switch t.Kind() {
	case reflect.String:
		return constrain(map[string]any{"type": "string"}, r, "string")
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int32, reflect.Int64, reflect.Uint32:
		return constrain(map[string]any{"type": "integer"}, r, "integer")
	case reflect.Float64:
		return constrain(map[string]any{"type": "number"}, r, "number")
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		items := c.of(t.Elem(), request, Rules{Enum: r.Enum, Pattern: r.Pattern, Format: r.Format})
		return constrain(map[string]any{"type": "array", "items": items}, r, "array")
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": c.of(t.Elem(), request, Rules{})}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		name := t.Name()
		if name == "" {
			return c.object(t, request)
		}
		if request {
			name += "Input"
		}
		if _, ok := c[name]; !ok {
			c[name] = map[string]any{} // placeholder breaks recursion
			c[name] = c.object(t, request)
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	return map[string]any{}
}

func (c Components) object(t reflect.Type, request bool) map[string]any {
	props := map[string]any{}
	var required []any
	var fields func(t reflect.Type)
	fields = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := JSONName(f)
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" {
				ft := f.Type
				if ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				fields(ft)
				continue
			}
			r := Parse(f.Tag.Get("v"))
			s := c.of(f.Type, request, r)
			if _, isRef := s["$ref"]; !isRef {
				if doc := f.Tag.Get("doc"); doc != "" {
					s["description"] = doc
				}
				if ex := f.Tag.Get("ex"); ex != "" {
					s["examples"] = []any{ex}
				}
			}
			props[name] = s
			omit := strings.Contains(f.Tag.Get("json"), "omitempty") || strings.Contains(f.Tag.Get("json"), "omitzero")
			if request && r.Required || !request && f.Type.Kind() != reflect.Pointer && !omit {
				required = append(required, name)
			}
		}
	}
	fields(t)
	s := map[string]any{"type": "object", "properties": props}
	if request {
		s["additionalProperties"] = false
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
