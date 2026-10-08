package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoaderAccumulatesErrorsWithoutValues(t *testing.T) {
	l := FromMap(map[string]string{
		"APP_ENV": "prod", "PORT": "eighty", "TIMEOUT": "soon", "DB_PASSWORD": "hunter2", "FLAG": "maybe",
	})
	l.Environment()
	l.Int("PORT", 8080)
	l.Duration("TIMEOUT", time.Second)
	l.Required("DATABASE_URL")
	l.Bool("FLAG", false)
	l.Secret("SIGNING_KEY", true)
	err := l.Err()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{"APP_ENV", "PORT", "TIMEOUT", "DATABASE_URL", "FLAG", "SIGNING_KEY"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %s in %s", want, msg)
		}
	}
	for _, leak := range []string{"eighty", "soon", "hunter2", "maybe"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("value leaked: %s", msg)
		}
	}
}

func TestLoaderValues(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "key")
	_ = os.WriteFile(p, []byte("filesecret\n"), 0o600)
	l := FromMap(map[string]string{
		"APP_ENV": "staging", "N": "5", "D": "2s", "B": "true", "L": "a, b,,c", "K_FILE": p, "U": "https://x.example/y",
	})
	if l.Environment() != EnvStaging || l.Int("N", 0) != 5 || l.Duration("D", 0) != 2*time.Second || !l.Bool("B", false) {
		t.Fatal("typed values wrong")
	}
	if got := l.List("L", nil); strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("list %v", got)
	}
	if l.Secret("K", true).Reveal() != "filesecret" {
		t.Fatal("secret file")
	}
	if l.URL("U", true).Host != "x.example" {
		t.Fatal("url")
	}
	if l.String("MISSING", "def") != "def" {
		t.Fatal("default")
	}
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
}
