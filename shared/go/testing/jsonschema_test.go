package testkit

import "testing"

func TestValidateJSONSchemaAgainstPlatformSchemas(t *testing.T) {
	ValidateJSONSchema(t, "../../../contracts/schemas/money.schema.json", []byte(`{"amount_minor":2500050,"currency":"NGN"}`))
	ValidateJSONSchema(t, "../../../contracts/schemas/error-envelope.schema.json",
		[]byte(`{"error":{"code":"PROPERTY_NOT_FOUND","message":"Property not found.","request_id":"r1"}}`))
	ft := &fakeT{TB: t}
	func() {
		defer func() { _ = recover() }()
		ValidateJSONSchema(ft, "../../../contracts/schemas/money.schema.json", []byte(`{"amount_minor":1.5,"currency":"NGN"}`))
	}()
	if !ft.failed {
		t.Fatal("float amount must fail the money schema")
	}
}

type fakeT struct {
	testing.TB
	failed bool
}

func (f *fakeT) Fatalf(string, ...any) { f.failed = true; panic("fatal") }
func (f *fakeT) Helper()               {}
