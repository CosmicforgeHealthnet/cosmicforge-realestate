// Package schema validates request DTOs from struct tags and derives their
// JSON Schema from the same tags, so the OpenAPI contract and the runtime
// validation cannot drift apart.
//
// Tag `v` (comma separated):
//
//	required          pointer non-nil, string non-blank, ID non-zero, slice non-empty
//	min=N, max=N      string length (runes) / integer value / slice length
//	enum=A|B|C        allowed string values (also applied to []string items)
//	pattern=RE        anchored regular expression (must be the last rule)
//	format=F          email | e164 | date | currency | country | uri
//	shallow           do not validate the contents of a slice/struct
//
// Tag `doc` is a description and `ex` an example (documentation only).
package schema

import (
	"fmt"
	"net/mail"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

// Rules are the parsed constraints of one field.
type Rules struct {
	Required bool
	Min, Max *int64
	Enum     []string
	Pattern  string
	Format   string
	Shallow  bool // do not validate slice/struct contents (validated later, e.g. per import row)
}

var cache sync.Map // reflect.StructField key -> Rules

// Parse parses a `v` tag.
func Parse(tag string) Rules {
	if r, ok := cache.Load(tag); ok {
		return r.(Rules)
	}
	var r Rules
	rest := tag
	if i := strings.Index(tag, "pattern="); i >= 0 {
		// pattern= must be the last rule; it may contain commas.
		r.Pattern = tag[i+len("pattern="):]
		regex(r.Pattern)
		rest = tag[:i]
	}
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		k, val, _ := strings.Cut(part, "=")
		switch k {
		case "":
		case "required":
			r.Required = true
		case "min", "max":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				panic("schema: bad " + k + " in tag " + tag)
			}
			if k == "min" {
				r.Min = &n
			} else {
				r.Max = &n
			}
		case "enum":
			r.Enum = strings.Split(val, "|")
		case "format":
			r.Format = val
		case "shallow":
			r.Shallow = true
		default:
			panic("schema: unknown rule " + k + " in tag " + tag)
		}
	}
	cache.Store(tag, r)
	return r
}

var regexes sync.Map

func regex(p string) *regexp.Regexp {
	if r, ok := regexes.Load(p); ok {
		return r.(*regexp.Regexp)
	}
	re := regexp.MustCompile("^(?:" + p + ")$")
	regexes.Store(p, re)
	return re
}

var (
	idType    = reflect.TypeFor[ids.ID]()
	timeType  = reflect.TypeFor[time.Time]()
	moneyType = reflect.TypeFor[money.Money]()
	e164      = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
	country   = regexp.MustCompile(`^[A-Z]{2}$`)
)

// Validate checks v (a struct or pointer to struct) and returns an
// INVALID_REQUEST error listing every failing field.
func Validate(v any) error {
	var details []apperrors.Detail
	walk(reflect.ValueOf(v), "", &details)
	if len(details) == 0 {
		return nil
	}
	return apperrors.InvalidRequest("Request validation failed.", details...)
}

func add(d *[]apperrors.Detail, field, code, msg string) {
	*d = append(*d, apperrors.Detail{Field: field, Code: code, Message: msg})
}

func walk(v reflect.Value, prefix string, d *[]apperrors.Detail) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || v.Type() == timeType || v.Type() == moneyType {
		return
	}
	t := v.Type()
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
			walk(v.Field(i), prefix, d)
			continue
		}
		field(v.Field(i), prefix+name, Parse(f.Tag.Get("v")), d)
	}
}

// JSONName returns the wire name of f ("" for embedded structs without a tag).
func JSONName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "" && !f.Anonymous {
		return f.Name
	}
	return name
}

func field(v reflect.Value, name string, r Rules, d *[]apperrors.Detail) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			if r.Required {
				add(d, name, "REQUIRED", name+" is required")
			}
			return
		}
		v = v.Elem()
	}
	switch {
	case v.Type() == idType:
		if r.Required && v.Interface().(ids.ID).IsZero() {
			add(d, name, "REQUIRED", name+" is required")
		}
		return
	case v.Type() == timeType:
		if r.Required && v.Interface().(time.Time).IsZero() {
			add(d, name, "REQUIRED", name+" is required")
		}
		return
	case v.Type() == moneyType:
		m := v.Interface().(money.Money)
		if r.Required && m.Currency == "" {
			add(d, name, "REQUIRED", name+" is required")
			return
		}
		if m.Currency != "" {
			if err := m.Validate(); err != nil {
				add(d, name, "INVALID", name+" must have a valid ISO 4217 currency")
			}
			if r.Min != nil && m.AmountMinor < *r.Min {
				add(d, name, "OUT_OF_RANGE", fmt.Sprintf("%s.amount_minor must be at least %d", name, *r.Min))
			}
			if r.Max != nil && m.AmountMinor > *r.Max {
				add(d, name, "OUT_OF_RANGE", fmt.Sprintf("%s.amount_minor must be at most %d", name, *r.Max))
			}
		}
		return
	}
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		if strings.TrimSpace(s) == "" {
			if r.Required {
				add(d, name, "REQUIRED", name+" is required")
			}
			return
		}
		str(s, name, r, d)
	case reflect.Int, reflect.Int32, reflect.Int64:
		n := v.Int()
		if n == 0 && !r.Required {
			return // optional non-pointer number: zero means "not set"
		}
		if r.Min != nil && n < *r.Min {
			add(d, name, "OUT_OF_RANGE", fmt.Sprintf("%s must be at least %d", name, *r.Min))
		}
		if r.Max != nil && n > *r.Max {
			add(d, name, "OUT_OF_RANGE", fmt.Sprintf("%s must be at most %d", name, *r.Max))
		}
	case reflect.Float64:
		f := v.Float()
		if r.Min != nil && f < float64(*r.Min) {
			add(d, name, "OUT_OF_RANGE", fmt.Sprintf("%s must be at least %d", name, *r.Min))
		}
		if r.Max != nil && f > float64(*r.Max) {
			add(d, name, "OUT_OF_RANGE", fmt.Sprintf("%s must be at most %d", name, *r.Max))
		}
	case reflect.Slice:
		n := int64(v.Len())
		if r.Required && n == 0 {
			add(d, name, "REQUIRED", name+" must not be empty")
		}
		if r.Min != nil && n < *r.Min {
			add(d, name, "OUT_OF_RANGE", fmt.Sprintf("%s must have at least %d items", name, *r.Min))
		}
		if r.Max != nil && n > *r.Max {
			add(d, name, "OUT_OF_RANGE", fmt.Sprintf("%s must have at most %d items", name, *r.Max))
		}
		for i := 0; i < v.Len() && !r.Shallow; i++ {
			e := v.Index(i)
			item := fmt.Sprintf("%s[%d]", name, i)
			if e.Kind() == reflect.String {
				str(e.String(), item, Rules{Enum: r.Enum, Pattern: r.Pattern, Format: r.Format}, d)
			} else {
				walk(e, item+".", d)
			}
		}
	case reflect.Struct:
		walk(v, name+".", d)
	}
}

func str(s, name string, r Rules, d *[]apperrors.Detail) {
	n := int64(utf8.RuneCountInString(s))
	if r.Min != nil && n < *r.Min {
		add(d, name, "TOO_SHORT", fmt.Sprintf("%s must be at least %d characters", name, *r.Min))
	}
	if r.Max != nil && n > *r.Max {
		add(d, name, "TOO_LONG", fmt.Sprintf("%s must be at most %d characters", name, *r.Max))
	}
	if len(r.Enum) > 0 {
		ok := false
		for _, e := range r.Enum {
			ok = ok || e == s
		}
		if !ok {
			add(d, name, "INVALID_VALUE", name+" must be one of "+strings.Join(r.Enum, ", "))
		}
	}
	if r.Pattern != "" && !regex(r.Pattern).MatchString(s) {
		add(d, name, "INVALID_FORMAT", name+" has an invalid format")
	}
	switch r.Format {
	case "email":
		a, err := mail.ParseAddress(s)
		if err != nil || a.Address != s || len(s) > 254 {
			add(d, name, "INVALID_FORMAT", name+" must be an email address")
		}
	case "e164":
		if !e164.MatchString(s) {
			add(d, name, "INVALID_FORMAT", name+" must be an E.164 phone number")
		}
	case "date":
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			add(d, name, "INVALID_FORMAT", name+" must be a date (YYYY-MM-DD)")
		}
	case "currency":
		if !money.Currency(s).Valid() {
			add(d, name, "INVALID_FORMAT", name+" must be an ISO 4217 currency code")
		}
	case "country":
		if !country.MatchString(s) {
			add(d, name, "INVALID_FORMAT", name+" must be an ISO 3166-1 alpha-2 country code")
		}
	case "uri":
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			add(d, name, "INVALID_FORMAT", name+" must be an https URL")
		}
	}
}

// ParseDate parses a validated YYYY-MM-DD string (nil for nil/empty).
func ParseDate(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := time.Parse(time.DateOnly, *s)
	if err != nil {
		return nil
	}
	return &t
}
