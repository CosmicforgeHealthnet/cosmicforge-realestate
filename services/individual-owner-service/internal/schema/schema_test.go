package schema

import (
	"reflect"
	"testing"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

type inner struct {
	Code string `json:"code" v:"required,pattern=[A-Z]{2}"`
}

type sample struct {
	Title    string       `json:"title" v:"required,min=3,max=10"`
	Kind     string       `json:"kind" v:"required,enum=A|B"`
	Count    *int         `json:"count,omitempty" v:"min=1,max=5"`
	Tags     []string     `json:"tags" v:"max=2,enum=x|y"`
	ID       ids.ID       `json:"id" v:"required"`
	Price    *money.Money `json:"price" v:"min=0"`
	Phone    string       `json:"phone,omitempty" v:"format=e164"`
	When     string       `json:"when,omitempty" v:"format=date"`
	Children []inner      `json:"children"`
}

func TestValidate(t *testing.T) {
	five := 6
	bad := sample{Title: "ab", Kind: "C", Count: &five, Tags: []string{"x", "z", "y"}, Price: &money.Money{AmountMinor: -1, Currency: "NGN"},
		Phone: "123", When: "2026-13-01", Children: []inner{{Code: "abc"}}}
	err := Validate(&bad)
	e := apperrors.From(err)
	fields := map[string]bool{}
	for _, d := range e.Details {
		fields[d.Field] = true
	}
	for _, f := range []string{"title", "kind", "count", "tags", "tags[1]", "id", "price", "phone", "when", "children[0].code"} {
		if !fields[f] {
			t.Errorf("expected detail for %s, got %+v", f, e.Details)
		}
	}
	good := sample{Title: "abc", Kind: "A", ID: ids.New(), Tags: []string{"x"}, Phone: "+2348012345678", When: "2026-10-09", Children: []inner{{Code: "AB"}}}
	if err := Validate(good); err != nil {
		t.Fatalf("unexpected %v", err)
	}
}

func TestJSONSchemaMirrorsRules(t *testing.T) {
	c := Components{}
	ref := c.Ref(reflect.TypeFor[sample](), true)
	if ref["$ref"] != "#/components/schemas/sampleInput" {
		t.Fatalf("ref %v", ref)
	}
	s := c["sampleInput"]
	props := s["properties"].(map[string]any)
	title := props["title"].(map[string]any)
	if title["minLength"].(int64) != 3 || title["maxLength"].(int64) != 10 {
		t.Fatalf("title %v", title)
	}
	if s["additionalProperties"] != false {
		t.Fatal("requests must be closed")
	}
	req := s["required"].([]any)
	if len(req) != 3 {
		t.Fatalf("required %v", req)
	}
	if _, ok := c["Money"]; !ok {
		t.Fatal("money component")
	}
}
