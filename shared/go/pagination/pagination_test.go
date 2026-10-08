package pagination

import (
	"encoding/json"
	"testing"
	"time"
)

type cur struct {
	CreatedAt time.Time `json:"c"`
	ID        string    `json:"i"`
}

func TestLimit(t *testing.T) {
	if n, _ := Limit("", 0, 0); n != DefaultLimit {
		t.Fatal("default")
	}
	if n, _ := Limit("500", 20, 100); n != 100 {
		t.Fatal("bounded")
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		if _, err := Limit(bad, 20, 100); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestBuildAndCursorRoundTrip(t *testing.T) {
	rows := []int{1, 2, 3, 4}
	resp, err := Build(rows, 3, func(i int) any { return cur{ID: "id" + string(rune('0'+i))} })
	if err != nil || len(resp.Data) != 3 || !resp.Page.HasMore || resp.Page.NextCursor == nil {
		t.Fatalf("%+v %v", resp, err)
	}
	var c cur
	ok, err := Decode(*resp.Page.NextCursor, &c)
	if err != nil || !ok || c.ID != "id3" {
		t.Fatalf("decode %+v %v", c, err)
	}
	last, _ := Build([]int{1}, 3, func(i int) any { return nil })
	b, _ := json.Marshal(last)
	if string(b) != `{"data":[1],"page":{"next_cursor":null,"has_more":false}}` {
		t.Fatalf("shape %s", b)
	}
	empty, _ := Build[int](nil, 3, nil)
	b, _ = json.Marshal(empty)
	if string(b) != `{"data":[],"page":{"next_cursor":null,"has_more":false}}` {
		t.Fatalf("empty shape %s", b)
	}
	if _, err := Decode("!!!", &c); err == nil {
		t.Fatal("bad cursor accepted")
	}
}
