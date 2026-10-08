package templates

import (
	"strings"
	"testing"
)

func TestRenderIsSubstitutionOnly(t *testing.T) {
	vars := map[string]string{"name": `<script>alert(1)</script>`, "code": "{{evil}}"}
	html := Render(`<p>Hi {{name}}, code {{ code }}</p>`, vars, true)
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("html not escaped: %s", html)
	}
	if !strings.Contains(html, "{{evil}}") {
		t.Fatal("values must be inserted literally, never re-evaluated")
	}
	text := Render("Code: {{code}}\nBye", map[string]string{"code": "12\n34\x00"}, false)
	if text != "Code: 12 34\nBye" {
		t.Fatalf("%q", text)
	}
}

func TestValidateTemplate(t *testing.T) {
	if err := ValidateTemplate("Hello {{name}}"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{{.Secret}}`, `{{range .X}}{{end}}`, `{{ call .F }}`, `{{Name}}`, `}} stray`} {
		if ValidateTemplate(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if p := Placeholders("{{a}} {{b}} {{a}}"); strings.Join(p, ",") != "a,b" {
		t.Fatal(p)
	}
}

func TestCheckVariables(t *testing.T) {
	if CheckVariables([]string{"code"}, map[string]string{"code": "1"}) != nil {
		t.Fatal("valid")
	}
	if CheckVariables([]string{"code"}, map[string]string{}) == nil {
		t.Fatal("missing")
	}
	if CheckVariables([]string{"code"}, map[string]string{"code": "1", "extra": "x"}) == nil {
		t.Fatal("undeclared")
	}
	if CheckVariables([]string{"code"}, map[string]string{"code": strings.Repeat("x", 1001)}) == nil {
		t.Fatal("too long")
	}
}
