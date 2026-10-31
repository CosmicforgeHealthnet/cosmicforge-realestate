package integration

import (
	"context"
	"encoding/json"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestBusinessRegisteredRoutesMatchOpenAPIAndOfficialPostman(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	read := func(path string, v any) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	read("contracts/public/openapi/business-operations.yaml", &spec)
	documented := []string{}
	for path, methods := range spec.Paths {
		for method := range methods {
			documented = append(documented, strings.ToUpper(method)+" "+path)
		}
	}
	registered := []string{}
	err := chi.Walk(h.domain.Auth.Router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered = append(registered, method+" /api/v1/business"+route)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		Name    string `json:"name"`
		Request *struct {
			Method string `json:"method"`
			URL    struct {
				Raw string `json:"raw"`
			} `json:"url"`
		} `json:"request"`
		Item []json.RawMessage `json:"item"`
	}
	var collection struct {
		Item []item `json:"item"`
	}
	read("postman/CosmicForge_RealEstate.postman_collection.json", &collection)
	postman := []string{}
	found := false
	var walk func([]json.RawMessage)
	walk = func(raw []json.RawMessage) {
		for _, entry := range raw {
			var i item
			if err := json.Unmarshal(entry, &i); err != nil {
				t.Fatal(err)
			}
			if i.Request != nil {
				postman = append(postman, i.Name)
				if i.Name != i.Request.Method+" "+regexp.MustCompile(`\{\{business_([^}]+)\}\}`).ReplaceAllString(strings.TrimPrefix(i.Request.URL.Raw, "{{base_url}}"), "{$1}") {
					t.Fatalf("Postman request name/path drift: %s", i.Name)
				}
			}
			walk(i.Item)
		}
	}
	for _, folder := range collection.Item {
		if folder.Name == "03 Business Operations" {
			found = true
			walk(folder.Item)
		}
	}
	if !found {
		t.Fatal("official handoff folder missing")
	}
	sort.Strings(registered)
	sort.Strings(documented)
	sort.Strings(postman)
	if !reflect.DeepEqual(registered, documented) {
		t.Fatalf("handler/OpenAPI routes differ\nhandlers: %v\nOpenAPI: %v", registered, documented)
	}
	if !reflect.DeepEqual(documented, postman) {
		t.Fatalf("OpenAPI/Postman routes differ\nOpenAPI: %v\nPostman: %v", documented, postman)
	}
	t.Logf("certified %d canonical method/path pairs across handlers, OpenAPI and Postman", len(registered))
}

func validateStoredBusinessEvents(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, `SELECT event_type,jsonb_strip_nulls(jsonb_build_object('event_id',event_id,'event_type',event_type,'producer',producer,'aggregate_type',aggregate_type,'aggregate_id',aggregate_id,'occurred_at',occurred_at,'trace_id',trace_id,'actor',CASE WHEN actor_id IS NOT NULL THEN jsonb_build_object('domain',actor_domain,'id',actor_id) END,'metadata',metadata,'payload',payload)) FROM outbox_events WHERE producer='business-operations-service' ORDER BY occurred_at,event_id`)
	if err != nil {
		t.Fatal(err)
	}
	type stored struct {
		typ string
		raw []byte
	}
	events := []stored{}
	for rows.Next() {
		var e stored
		if err := rows.Scan(&e.typ, &e.raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	for _, e := range events {
		testkit.ValidateJSONSchema(t, filepath.Join(root, "contracts/events/business", e.typ+".schema.json"), e.raw)
	}
}
