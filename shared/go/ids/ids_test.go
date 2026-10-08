package ids

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewIsValidV7(t *testing.T) {
	for i := 0; i < 1000; i++ {
		id := New()
		if err := id.ValidateV7(); err != nil {
			t.Fatalf("New() produced invalid v7 id %s: %v", id, err)
		}
		if id.Version() != 7 {
			t.Fatalf("version = %d", id.Version())
		}
		if id[8]&0xC0 != 0x80 {
			t.Fatalf("variant bits wrong: %08b", id[8])
		}
	}
}

func TestNewTimestamp(t *testing.T) {
	before := time.Now().Add(-time.Millisecond)
	id := New()
	after := time.Now().Add(2 * time.Millisecond)
	ts := id.Time()
	if ts.Before(before.Truncate(time.Millisecond)) || ts.After(after) {
		t.Fatalf("embedded time %v not within [%v,%v]", ts, before, after)
	}
}

func TestUniquenessLargeBatchConcurrent(t *testing.T) {
	const workers = 8
	const per = 50000
	var mu sync.Mutex
	seen := make(map[ID]struct{}, workers*per)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]ID, per)
			for i := range local {
				local[i] = New()
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				if _, dup := seen[id]; dup {
					t.Errorf("duplicate id %s", id)
					return
				}
				seen[id] = struct{}{}
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*per {
		t.Fatalf("expected %d unique ids, got %d", workers*per, len(seen))
	}
}

func TestMonotonicWithinProcess(t *testing.T) {
	prev := New()
	for i := 0; i < 200000; i++ {
		next := New()
		if Compare(prev, next) >= 0 {
			t.Fatalf("ids not strictly increasing: %s then %s", prev, next)
		}
		prev = next
	}
}

func TestCounterOverflowAdvancesTimestamp(t *testing.T) {
	fixed := time.UnixMilli(1_800_000_000_000)
	old := now
	now = func() time.Time { return fixed }
	defer func() { now = old }()
	prev := New()
	for i := 0; i < 10000; i++ { // > 4096 per ms forces overflow
		next := New()
		if Compare(prev, next) >= 0 {
			t.Fatalf("overflow broke ordering at %d", i)
		}
		prev = next
	}
	if !prev.Time().After(fixed) {
		t.Fatalf("expected timestamp to advance after counter overflow")
	}
}

func TestStringCanonicalLowercaseRoundTrip(t *testing.T) {
	id := New()
	s := id.String()
	if s != strings.ToLower(s) || len(s) != 36 {
		t.Fatalf("not canonical: %q", s)
	}
	parsed, err := ParseV7(s)
	if err != nil || parsed != id {
		t.Fatalf("round trip failed: %v %s", err, parsed)
	}
	upper, err := Parse(strings.ToUpper(s))
	if err != nil || upper != id || upper.String() != s {
		t.Fatalf("uppercase input should normalize: %v", err)
	}
}

func TestParseFailures(t *testing.T) {
	cases := []string{
		"",
		"not-a-uuid",
		"0190b6d5-2c4b-7a3e-9f1c-8f2d4a6b8c0",    // short
		"0190b6d5-2c4b-7a3e-9f1c-8f2d4a6b8c0d0",  // long
		"0190b6d52c4b-7a3e-9f1c-8f2d4a6b8c0d-",   // dash misplaced
		"0190b6d5-2c4b-7a3e-9f1c-8f2d4a6b8c0g",   // non hex
		"{0190b6d5-2c4b-7a3e-9f1c-8f2d4a6b8c0d}", // braces
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("Parse(%q) expected error", c)
		}
	}
	// v4 is well-formed but not v7.
	if _, err := ParseV7("f47ac10b-58cc-4372-a567-0e02b2c3d479"); err != ErrNotV7 {
		t.Errorf("expected ErrNotV7, got %v", err)
	}
	if _, err := ParseV7("00000000-0000-0000-0000-000000000000"); err != ErrNil {
		t.Errorf("expected ErrNil, got %v", err)
	}
	// Wrong variant with version 7.
	if _, err := ParseV7("0190b6d5-2c4b-7a3e-cf1c-8f2d4a6b8c0d"); err != ErrNotV7 {
		t.Errorf("expected ErrNotV7 for bad variant, got %v", err)
	}
}

func TestJSONSerialization(t *testing.T) {
	type payload struct {
		ID    ID  `json:"id"`
		Empty ID  `json:"empty"`
		Ptr   *ID `json:"ptr,omitempty"`
	}
	id := New()
	b, err := json.Marshal(payload{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"` + id.String() + `","empty":null}`
	if string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	var back payload
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != id || !back.Empty.IsZero() {
		t.Fatalf("decode mismatch: %+v", back)
	}
	if err := json.Unmarshal([]byte(`{"id":"bogus"}`), &back); err == nil {
		t.Fatal("expected error decoding bogus id")
	}
	if err := json.Unmarshal([]byte(`{"id":123}`), &back); err == nil {
		t.Fatal("expected error decoding numeric id")
	}
	m := map[ID]int{id: 1}
	if _, err := json.Marshal(m); err != nil {
		t.Fatalf("map key marshal: %v", err)
	}
}

func TestSQLValuerScanner(t *testing.T) {
	id := New()
	v, err := id.Value()
	if err != nil || v.(string) != id.String() {
		t.Fatalf("Value: %v %v", v, err)
	}
	if v, _ := Nil.Value(); v != nil {
		t.Fatalf("nil ID should be SQL NULL, got %v", v)
	}
	var got ID
	for _, src := range []any{id.String(), []byte(id.String()), id[:], [16]byte(id)} {
		got = Nil
		if err := got.Scan(src); err != nil || got != id {
			t.Fatalf("Scan(%T) = %v %s", src, err, got)
		}
	}
	if err := got.Scan(nil); err != nil || !got.IsZero() {
		t.Fatalf("Scan(nil): %v", err)
	}
	if err := got.Scan(42); err == nil {
		t.Fatal("expected scan error for int")
	}
}

func TestNewAt(t *testing.T) {
	ts := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	id := NewAt(ts)
	if !id.Time().Equal(ts) || id.ValidateV7() != nil {
		t.Fatalf("NewAt mismatch: %v", id.Time())
	}
}

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}
