// Package templates renders notification templates safely. Templates are
// platform-authored data, but rendering still allows nothing except
// {{variable}} substitution: no expressions, loops, includes or function
// calls, so untrusted variable values can never execute. Values are
// HTML-escaped in HTML bodies and control characters are stripped.
package templates

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var placeholder = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]{0,63})\s*\}\}`)

// Placeholders returns the variable names a template text references.
func Placeholders(text string) []string {
	seen := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateTemplate rejects any "{{" that is not a plain placeholder, so a
// template cannot smuggle in other syntax.
func ValidateTemplate(text string) error {
	rest := placeholder.ReplaceAllString(text, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		return fmt.Errorf("templates: only {{variable}} placeholders are allowed")
	}
	return nil
}

// MaxValueLength bounds each variable value.
const MaxValueLength = 1000

// CheckVariables validates values against the declared variable set: all
// required present, no undeclared names, bounded length, valid UTF-8.
func CheckVariables(required []string, vars map[string]string) error {
	allowed := map[string]bool{}
	for _, r := range required {
		allowed[r] = true
		if strings.TrimSpace(vars[r]) == "" {
			return fmt.Errorf("variable %q is required", r)
		}
	}
	for k, v := range vars {
		if !allowed[k] {
			return fmt.Errorf("variable %q is not declared by the template", k)
		}
		if !utf8.ValidString(v) || utf8.RuneCountInString(v) > MaxValueLength {
			return fmt.Errorf("variable %q is invalid or too long", k)
		}
	}
	return nil
}

func clean(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
}

// Render substitutes variables into text. escapeHTML is used for HTML bodies.
func Render(text string, vars map[string]string, escapeHTML bool) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		v := clean(vars[name])
		if escapeHTML {
			v = html.EscapeString(v)
		}
		return v
	})
}
