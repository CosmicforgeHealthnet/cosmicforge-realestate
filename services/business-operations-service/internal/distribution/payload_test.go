package distribution

import "testing"

func TestPayloadDigestSurvivesJSONBRepresentationWithoutLosingInt64Precision(t *testing.T) {
	compact := []byte(`{"quantity":1,"amount_minor":9223372036854775807,"nested":{"z":2,"a":1}}`)
	jsonb := []byte(`{ "nested": {"a": 1, "z": 2}, "amount_minor": 9223372036854775807, "quantity": 1 }`)
	a, e := PayloadHash(compact)
	if e != nil {
		t.Fatal(e)
	}
	b, e := PayloadHash(jsonb)
	if e != nil || a != b {
		t.Fatalf("JSONB changed durable digest %s %s %v", a, b, e)
	}
	changed, e := PayloadHash([]byte(`{"quantity":1,"amount_minor":9223372036854775806,"nested":{"z":2,"a":1}}`))
	if e != nil || changed == a {
		t.Fatal("Adjacent int64 monetary values collided after decoding")
	}
	if _, e = PayloadHash([]byte(`{} {}`)); e == nil {
		t.Fatal("Multiple payloads accepted")
	}
}
