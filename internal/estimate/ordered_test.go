// Go only: an object whose keys keep their insertion order, as a JavaScript object's.
package estimate

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOrderedKeepsTheOrderKeysCameIn(t *testing.T) {
	var o Ordered[int]
	o.Set("eu-west-3", 1)
	o.Set("eu-west-1", 2)
	o.Set("eu-west-3", 3) // a known key keeps its place, as a JavaScript object's
	if !reflect.DeepEqual(o.Keys(), []string{"eu-west-3", "eu-west-1"}) || o.Len() != 2 {
		t.Fatalf("keys %q", o.Keys())
	}
	if v, ok := o.Get("eu-west-3"); !ok || v != 3 {
		t.Fatalf("eu-west-3: %d %v", v, ok)
	}
	if _, ok := o.Get("us-east-1"); ok {
		t.Fatal("us-east-1 set")
	}
	b, err := json.Marshal(o)
	if err != nil || string(b) != `{"eu-west-3":3,"eu-west-1":2}` {
		t.Fatalf("%s %v", b, err)
	}
	var back Ordered[int]
	if err := json.Unmarshal([]byte(`{"222222222222": 1, "111111111111": 2}`), &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Keys(), []string{"222222222222", "111111111111"}) {
		t.Fatalf("keys %q", back.Keys())
	}
	var empty Ordered[Totals]
	if b, _ := json.Marshal(empty); string(b) != "{}" {
		t.Fatalf("empty %s", b)
	}
	if err := json.Unmarshal([]byte(`[1]`), &back); err == nil {
		t.Fatal("an array read as an object")
	}
}
