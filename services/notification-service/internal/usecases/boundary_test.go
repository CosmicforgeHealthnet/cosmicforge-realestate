package usecases

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Notification business logic must stay provider-neutral: it may use the
// channels.Sender/Registry/Message/Result/Error abstractions, never a
// concrete adapter or its configuration.
func TestBusinessLogicDoesNotDependOnConcreteProviders(t *testing.T) {
	concrete := regexp.MustCompile(`channels\.(FCM|SMTP|Twilio|NewFCM|NewSMTP|NewTwilio|FCMConfig|SMTPConfig|TwilioConfig|FCMTokenSourceFromJSON)\b|(?i)twilio|smtp|firebase|oauth2`)
	for _, dir := range []string{".", "../domain", "../templates", "../http"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			af, err := parser.ParseFile(token.NewFileSet(), f, src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range af.Imports {
				if p := strings.Trim(imp.Path.Value, `"`); strings.Contains(p, "oauth2") || strings.Contains(p, "net/smtp") || strings.Contains(p, "firebase") {
					t.Errorf("%s imports provider package %s", f, p)
				}
			}
			code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(src), "")
			if m := concrete.FindString(code); m != "" {
				t.Errorf("%s references concrete provider %q", f, m)
			}
		}
	}
}
